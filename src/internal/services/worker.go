package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/config"
	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

// TranscodeWorker runs ffmpeg for transcoding jobs dequeued from the queue.
// Each worker instance processes one job at a time; horizontal scaling is achieved
// by running multiple worker replicas that all pull from the same Postgres-backed queue.
type TranscodeWorker struct {
	queue      ports.JobQueue
	workerID   string
	ffmpegPath string
	segmentDir string
	cfg        config.TranscodeConfig
	encoder    config.EncoderConfig
}

// NewTranscodeWorker creates a worker that writes segment files into segmentDir.
// If segmentDir is empty, it falls back to ./temp/transcode under the current working directory.
// The segmentDir should be a shared filesystem (NFS/RWX PVC) mounted by both workers
// and the Control Plane so segments are directly readable by the segment handler.
func NewTranscodeWorker(queue ports.JobQueue, id string, tcfg config.TranscodeConfig, enc config.EncoderConfig, segmentDir string) *TranscodeWorker {
	ffmpegPath := "ffmpeg"

	if segmentDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			log.Printf("[Worker %s] Warning: os.Getwd failed (%v); falling back to /tmp for segmentDir", id, err)
			cwd = "/tmp"
		}
		segmentDir = filepath.Join(cwd, "temp", "transcode")
		log.Printf("[Worker %s] Warning: segmentDir was empty, falling back to %s", id, segmentDir)
	}
	if err := os.MkdirAll(segmentDir, 0755); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to create segmentDir %s: %v (worker will fail per-job at MkdirAll)", id, segmentDir, err)
	}

	tcfg = tcfg.WithDefaults()
	if enc.Preset == "" {
		enc = config.DefaultEncoder()
	}

	return &TranscodeWorker{
		queue:      queue,
		workerID:   id,
		ffmpegPath: ffmpegPath,
		segmentDir: segmentDir,
		cfg:        tcfg,
		encoder:    enc,
	}
}

// Start is the worker's main loop. It blocks until ctx is cancelled, dequeuing
// and processing jobs one at a time. On error or empty queue it sleeps 1s before retrying.
func (w *TranscodeWorker) Start(ctx context.Context) {
	log.Printf("Worker %s started using ffmpeg at %s (segmentDir: %s)", w.workerID, w.ffmpegPath, w.segmentDir)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			job, err := w.queue.Dequeue(ctx, w.workerID)
			if err != nil {
				if !errors.Is(err, ports.ErrEmptyQueue) {
					log.Printf("[Worker %s] Dequeue error: %v", w.workerID, err)
				}
				time.Sleep(1 * time.Second)
				continue
			}
			log.Printf("Worker dequeued job %s (Status: %s, Restarts: %d)", job.ID, job.Status, job.RestartCount)
			w.processJob(ctx, job)
		}
	}
}

// processJob is the per-job state machine: Processing → Ready → Completed/Failed.
// It spawns a heartbeat goroutine, runs ffmpeg, watches the .m3u8 manifest for segment
// durations, and handles recovery analysis for restarted jobs. On cancel/failure it
// cleans up the job directory; on success it leaves segments for the CP watchdog.
func (w *TranscodeWorker) processJob(ctx context.Context, job *domain.TranscodeJob) {
	log.Printf("[Worker %s] Picked up job %s for media %s", w.workerID, job.ID, job.FilePath)

	// Update Status: Processing & Immediate Heartbeat
	job.Status = "Processing"
	job.WorkerID = w.workerID
	job.LastHeartbeat = time.Now()
	if err := w.queue.UpdateJob(ctx, job); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to mark job %s as Processing: %v", w.workerID, job.ID, err)
	} else {
		if job.RestartCount > 0 {
			log.Printf("[Worker %s] RECOVERY: Job %s marked as Processing (Restart #%d)", w.workerID, job.ID, job.RestartCount)
		} else {
			log.Printf("[Worker %s] Job %s marked as Processing", w.workerID, job.ID)
		}
	}

	// Prepare Output Directory
	jobDir := filepath.Join(w.segmentDir, job.ID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to create job directory %s: %v", w.workerID, jobDir, err)
		job.Status = "Failed"
		job.FailureReason = fmt.Sprintf("Failed to create job directory: %v", err)
		if err := w.queue.UpdateJob(ctx, job); err != nil {
			log.Printf("[Worker %s] CRITICAL: Failed to mark job %s as Failed (mkdir): %v", w.workerID, job.ID, err)
		}
		return
	}

	outputPath := filepath.Join(jobDir, "stream.m3u8")

	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// Heartbeat loop uses cheap Heartbeat() for 1s ticks, UpdateJob only for status changes.
	// Heartbeat loop: uses context.Background() instead of jobCtx deliberately —
	// the status check must outlive a cancelled jobCtx so we can detect the cancellation
	// (job deletion, worker reassignment) and stop ffmpeg via jobCancel().
	go func() {
		hbInterval := time.Duration(w.cfg.HeartbeatInterval) * time.Second
		ticker := time.NewTicker(hbInterval)
		defer ticker.Stop()

		statusInterval := time.Duration(w.cfg.StatusCheckInterval) * time.Second
		statusTicker := time.NewTicker(statusInterval)
		defer statusTicker.Stop()

		for {
			select {
			case <-jobCtx.Done():
				return
			case <-statusTicker.C:
				currentJob, err := w.queue.GetJob(context.Background(), job.ID)
				if err != nil {
					log.Printf("[Worker %s] Job %s missing/deleted. Stopping.", w.workerID, job.ID)
					jobCancel()
					return
				}
				if currentJob.Status == "Cancelled" || currentJob.Status == "Pending" || currentJob.WorkerID != w.workerID {
					log.Printf("[Worker %s] Job %s no longer owned by this worker (Status: %s, Owner: %s). Stopping.",
						w.workerID, job.ID, currentJob.Status, currentJob.WorkerID)
					jobCancel()
					return
				}
			case <-ticker.C:
				// Cheap heartbeat — no full-row write, no segment fetch
				if err := w.queue.Heartbeat(context.Background(), job.ID, w.workerID); err != nil {
					log.Printf("[Worker %s] Heartbeat failed for job %s: %v", w.workerID, job.ID, err)
					jobCancel()
					return
				}
			}
		}
	}()

	// Calculate resume point
	startSegment := 0
	startTime := 0.0

	if job.StartSegment > 0 {
		startSegment = job.StartSegment
		startTime = job.StartTime
		log.Printf("[Worker %s] JIT START: Starting job %s at segment %d (time: %.2fs) as requested by Control Plane.",
			w.workerID, job.ID, startSegment, startTime)
	} else if job.RestartCount > 0 {
		startSegment, startTime = w.analyzeExistingSegments(jobDir)
		if startSegment > 0 {
			log.Printf("[Worker %s] LOCAL RECOVERY: Resuming job %s from segment %d (time: %.2fs).",
				w.workerID, job.ID, startSegment, startTime)
			job.LastSegmentNum = startSegment - 1
			job.TranscodedDuration = startTime
			if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
				log.Printf("[Worker %s] Warning: failed to remove stale manifest %s: %v", w.workerID, outputPath, err)
			}
		}
	}

	// Phase 3: For range sub-jobs, use range boundaries
	rangeEnd := 0.0
	if job.ParentJobID != "" && job.RangeEnd > 0 {
		startSegment = job.StartSegment
		startTime = job.RangeStart
		rangeEnd = job.RangeEnd
		log.Printf("[Worker %s] RANGE SUB-JOB: %s range %d/%d (%.1fs-%.1fs), startSeg=%d",
			w.workerID, job.ID, job.RangeIndex, job.TotalRanges, job.RangeStart, job.RangeEnd, startSegment)
	}

	// Build FFmpeg command
	args := w.buildFFmpegArgs(job, jobDir, outputPath, startSegment, startTime, rangeEnd)
	cmd := exec.CommandContext(jobCtx, w.ffmpegPath, args...)

	// Stream ffmpeg output to stdout/stderr (captured by container runtime)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("Running FFmpeg: %s", cmd.String())

	if err := cmd.Start(); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to start FFmpeg for job %s: %v", w.workerID, job.ID, err)
		job.Status = "Failed"
		job.FailureReason = fmt.Sprintf("Failed to start ffmpeg: %v", err)
		if err := w.queue.UpdateJob(ctx, job); err != nil {
			log.Printf("[Worker %s] CRITICAL: Failed to mark job %s as Failed (ffmpeg start): %v", w.workerID, job.ID, err)
		}
		w.cleanupJobDir(jobDir)
		return
	}

	// Poll for manifest creation
	manifestTimeout := time.Duration(w.cfg.ManifestTimeout) * time.Second
	ready := false
	deadline := time.Now().Add(manifestTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(outputPath); err == nil {
			ready = true
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !ready {
		log.Printf("[Worker %s] ERROR: Timed out waiting for HLS manifest at %s", w.workerID, outputPath)
		if err := cmd.Process.Kill(); err != nil {
			log.Printf("[Worker %s] Warning: failed to kill ffmpeg process for job %s: %v", w.workerID, job.ID, err)
		}
		job.Status = "Failed"
		job.FailureReason = "Manifest creation timeout"
		if err := w.queue.UpdateJob(ctx, job); err != nil {
			log.Printf("[Worker %s] CRITICAL: Failed to mark job %s as Failed (manifest timeout): %v", w.workerID, job.ID, err)
		}
		w.cleanupJobDir(jobDir)
		return
	}

	// Update Job to Ready
	job.Status = "Ready"
	job.StreamURL = fmt.Sprintf("/hls/%s/stream.m3u8", job.ID)
	if err := w.queue.UpdateJob(ctx, job); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to mark job %s as Ready: %v", w.workerID, job.ID, err)
	}

	if job.RestartCount > 0 {
		log.Printf("[Worker %s] RECOVERY: Stream ready for job %s. (FFmpeg PID %d)", w.workerID, job.ID, cmd.Process.Pid)
	} else {
		log.Printf("[Worker %s] Job %s Ready. Stream started. (FFmpeg PID %d)", w.workerID, job.ID, cmd.Process.Pid)
	}

	// Segment watcher uses ffmpeg's .m3u8 manifest for durations instead of ffprobe per segment.
	// Report segments under the parent job ID for sub-jobs (Phase 3).
	reportJobID := job.ID
	if job.ParentJobID != "" {
		reportJobID = job.ParentJobID
	}
	go w.watchSegments(jobCtx, reportJobID, jobDir, startSegment)

	// Wait for completion
	err := cmd.Wait()

	if err != nil {
		if jobCtx.Err() != nil {
			log.Printf("[Worker %s] Job %s Cancelled/Stopped.", w.workerID, job.ID)
			// Clean up temp dir on cancel
			w.cleanupJobDir(jobDir)
		} else {
			log.Printf("[Worker %s] Job %s FFmpeg finished with error: %v", w.workerID, job.ID, err)
			job.Status = "Failed"
			job.FailureReason = fmt.Sprintf("ffmpeg error: %v", err)
			if err := w.queue.UpdateJob(context.Background(), job); err != nil {
				log.Printf("[Worker %s] CRITICAL: Failed to mark job %s as Failed (ffmpeg error): %v", w.workerID, job.ID, err)
			}
		}
	} else {
		log.Printf("[Worker %s] Job %s FFmpeg finished successfully", w.workerID, job.ID)
		job.Status = "Completed"
		if err := w.queue.UpdateJob(context.Background(), job); err != nil {
			log.Printf("[Worker %s] Failed to mark job %s as Completed: %v", w.workerID, job.ID, err)
		}
		// Phase 3: Check if parent is now fully completed
		if job.ParentJobID != "" {
			w.checkParentCompletion(ctx, job.ParentJobID, job.TotalRanges)
		}
	}
}

// cleanupJobDir removes the job's segment directory from the shared filesystem.
// Called on cancel/failure paths. On success paths, segments are left on disk
// for the Control Plane watchdog to clean up after the idle retention period.
func (w *TranscodeWorker) cleanupJobDir(jobDir string) {
	if err := os.RemoveAll(jobDir); err != nil {
		log.Printf("[Worker %s] Failed to clean up job dir %s: %v", w.workerID, jobDir, err)
	} else {
		log.Printf("[Worker %s] Cleaned up job dir %s", w.workerID, jobDir)
	}
}

// checkParentCompletion checks if all sub-jobs for a range-parallel parent are done,
// and if so marks the parent job as Completed. This uses an optional interface assertion
// because only the Postgres adapter implements parent-completion tracking; the HTTP
// queue adapter (used by workers) silently skips this.
func (w *TranscodeWorker) checkParentCompletion(ctx context.Context, parentJobID string, totalRanges int) {
	if pq, ok := w.queue.(interface {
		IsParentJobCompleted(ctx context.Context, parentJobID string, totalRanges int) (bool, error)
		MarkParentCompleted(ctx context.Context, parentJobID string) error
	}); ok {
		done, err := pq.IsParentJobCompleted(ctx, parentJobID, totalRanges)
		if err == nil && done {
			log.Printf("[Worker %s] All %d sub-jobs completed for parent %s. Marking parent Completed.", w.workerID, totalRanges, parentJobID)
			if err := pq.MarkParentCompleted(ctx, parentJobID); err != nil {
				log.Printf("[Worker %s] Failed to mark parent %s as Completed: %v", w.workerID, parentJobID, err)
			}
		}
	}
}

// analyzeExistingSegments scans the shared job directory for existing .ts files
// (written by a previous worker that died), validates the trailing segments with ffprobe,
// and returns the next segment number and accumulated duration for ffmpeg resume.
// Corrupt or truncated trailing segments are removed before the scan returns.
func (w *TranscodeWorker) analyzeExistingSegments(jobDir string) (nextSegment int, totalDuration float64) {
	files, err := filepath.Glob(filepath.Join(jobDir, "segment_*.ts"))
	if err != nil || len(files) == 0 {
		return 0, 0.0
	}

	type segmentFile struct {
		Number int
		Path   string
	}

	var segments []segmentFile
	for _, f := range files {
		base := filepath.Base(f)
		numStr := strings.TrimSuffix(strings.TrimPrefix(base, "segment_"), ".ts")
		if num, err := strconv.Atoi(numStr); err == nil {
			segments = append(segments, segmentFile{Number: num, Path: f})
		}
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].Number < segments[j].Number
	})

	if len(segments) == 0 {
		return 0, 0.0
	}

	// Verify segments from last to first, removing corrupt ones
	for i := len(segments) - 1; i >= 0; i-- {
		seg := segments[i]
		_, duration, err := w.probeSegment(seg.Path)
		if err != nil {
			log.Printf("[Worker %s] RECOVERY: Found corrupt segment %s. Deleting...", w.workerID, seg.Path)
			if err := os.Remove(seg.Path); err != nil && !os.IsNotExist(err) {
				log.Printf("[Worker %s] Warning: failed to remove corrupt segment %s: %v", w.workerID, seg.Path, err)
			}
			continue
		}
		if duration < 2.0 {
			log.Printf("[Worker %s] RECOVERY: Segment %s is truncated (Dur: %.2fs). Deleting...", w.workerID, seg.Path, duration)
			if err := os.Remove(seg.Path); err != nil && !os.IsNotExist(err) {
				log.Printf("[Worker %s] Warning: failed to remove truncated segment %s: %v", w.workerID, seg.Path, err)
			}
			continue
		}
		break
	}

	// Re-scan
	files, _ = filepath.Glob(filepath.Join(jobDir, "segment_*.ts"))
	segments = []segmentFile{}
	for _, f := range files {
		base := filepath.Base(f)
		numStr := strings.TrimSuffix(strings.TrimPrefix(base, "segment_"), ".ts")
		if num, err := strconv.Atoi(numStr); err == nil {
			segments = append(segments, segmentFile{Number: num, Path: f})
		}
	}
	sort.Slice(segments, func(i, j int) bool {
		return segments[i].Number < segments[j].Number
	})

	if len(segments) == 0 {
		return 0, 0.0
	}

	lastSeg := segments[len(segments)-1]
	startTimeVal, duration, err := w.probeSegment(lastSeg.Path)
	if err != nil {
		log.Printf("[Worker %s] RECOVERY: Last segment %s probe failed (%v). Trying second-to-last.", w.workerID, lastSeg.Path, err)
		if len(segments) >= 2 {
			lastSeg = segments[len(segments)-2]
			startTimeVal, duration, err = w.probeSegment(lastSeg.Path)
		}
		if err != nil {
			log.Printf("[Worker %s] RECOVERY: Fallback probe also failed. Restarting from segment 0.", w.workerID)
			return 0, 0.0
		}
	}

	totalDuration = startTimeVal + duration
	nextSegment = segments[len(segments)-1].Number + 1

	log.Printf("[Worker %s] RECOVERY: Found %d segments (Range: %d-%d), resume: %.2fs, next: %d",
		w.workerID, len(segments), segments[0].Number, segments[len(segments)-1].Number, totalDuration, nextSegment)

	return nextSegment, totalDuration
}

// probeSegment runs ffprobe on a single .ts file and returns its start_time and duration.
// Both values are needed because some containers report a non-zero start_time.
func (w *TranscodeWorker) probeSegment(segmentPath string) (startTime float64, duration float64, err error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		segmentPath,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return 0.0, 0.0, fmt.Errorf("ffprobe failed: %v; stderr: %s", err, stderr.String())
	}

	var result struct {
		Format struct {
			Duration  string `json:"duration"`
			StartTime string `json:"start_time"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0.0, 0.0, err
	}

	duration, err = strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil {
		log.Printf("[Worker %s] Warning: ffprobe returned non-numeric duration %q for %s: %v", w.workerID, result.Format.Duration, segmentPath, err)
	}
	startTime, err = strconv.ParseFloat(result.Format.StartTime, 64)
	if err != nil && result.Format.StartTime != "" {
		log.Printf("[Worker %s] Warning: ffprobe returned non-numeric start_time %q for %s: %v", w.workerID, result.Format.StartTime, segmentPath, err)
	}
	return startTime, duration, nil
}

// buildFFmpegArgs constructs the ffmpeg argument list for a transcoding job.
// Resume numbering is handled via -start_number and -output_ts_offset; input seeking
// via -ss is used for both JIT recovery (startTime) and range sub-jobs (rangeEnd).
// On resume, video codec is forced to libx264 because -ss stream-copy produces
// broken keyframe alignment at the segment boundary.
func (w *TranscodeWorker) buildFFmpegArgs(job *domain.TranscodeJob, jobDir, outputPath string, startSegment int, startTime float64, rangeEnd float64) []string {
	args := []string{}

	// Phase 3: For range sub-jobs, use -ss and -to for input seeking
	if rangeEnd > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startTime))
		args = append(args, "-to", fmt.Sprintf("%.3f", rangeEnd))
	} else if startSegment > 0 && startTime > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startTime))
	}

	args = append(args, "-i", job.FilePath)

	// Determine video codec
	videoCodec := job.TargetVideoCodec
	if videoCodec == "" {
		videoCodec = "libx264"
	}
	if startSegment > 0 {
		videoCodec = "libx264"
	}

	if videoCodec == "copy" {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args,
			"-c:v", videoCodec,
			"-preset", w.encoder.Preset,
			"-crf", strconv.Itoa(w.encoder.CRF),
			"-pix_fmt", "yuv420p",
			"-profile:v", "main",
		)
		// Bitrate cap
		if w.encoder.MaxRate != "" {
			args = append(args, "-maxrate", w.encoder.MaxRate, "-bufsize", w.encoder.BufSize)
		}
		// Use force_key_frames for accurate 4s segments regardless of fps
		hlsTime := w.cfg.HLSSegmentTime
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", hlsTime))
		args = append(args, "-sc_threshold", "0")

		// Downscale from client caps (stored in job via playback strategy — not yet wired, so skip if 0)
		// When MaxWidth/MaxHeight are available, add: -vf scale='min(iw,maxw)':-2
	}

	// Audio codec
	audioCodec := job.TargetAudioCodec
	if audioCodec == "" {
		audioCodec = "aac"
	}
	if audioCodec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args,
			"-c:a", audioCodec,
			"-ac", "2",
		)
	}

	// Phase 4: Extract subtitles as WebVTT instead of dropping them
	// Map optional subtitle stream (the ? makes it optional so no error if absent)
	args = append(args, "-map", "0:v:0", "-map", "0:a:0")
	args = append(args, "-map", "0:s:0?") // Optional subtitle
	// For HLS, convert subtitles to webvtt
	args = append(args, "-c:s", "webvtt")

	// HLS output settings
	hlsTime := strconv.Itoa(w.cfg.HLSSegmentTime)
	args = append(args,
		"-f", "hls",
		"-hls_time", hlsTime,
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(jobDir, "segment_%03d.ts"),
	)

	// Resume numbering
	if startSegment > 0 {
		args = append(args, "-start_number", fmt.Sprintf("%d", startSegment))
		args = append(args, "-output_ts_offset", fmt.Sprintf("%.3f", startTime))
	}

	args = append(args, outputPath)

	log.Printf("[Worker %s] FFmpeg args: video=%s preset=%s crf=%d, audio=%s, startSeg=%d, seekTime=%.2fs, rangeEnd=%.2fs",
		w.workerID, videoCodec, w.encoder.Preset, w.encoder.CRF, audioCodec, startSegment, startTime, rangeEnd)

	return args
}

// watchSegments polls ffmpeg's output .m3u8 manifest every 500ms, extracts segment
// durations and start times, and reports new segments to the queue via AddSegment.
// This avoids running ffprobe per segment (~1800 execs for a 2h movie).
// For sub-jobs (Phase 3), segments are reported under the parent job ID.
func (w *TranscodeWorker) watchSegments(ctx context.Context, jobID, jobDir string, startSegment int) {
	log.Printf("[Worker %s] Segment watcher started for job %s (jobDir: %s, startSeg: %d)", w.workerID, jobID, jobDir, startSegment)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	reportedSegments := make(map[int]bool)
	manifestPath := filepath.Join(jobDir, "stream.m3u8")

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Worker %s] Segment watcher stopped for job %s", w.workerID, jobID)
			return
		case <-ticker.C:
			// Parse the .m3u8 that ffmpeg writes to get segment durations
			entries := w.parseHLSManifest(manifestPath)
			for _, entry := range entries {
				if reportedSegments[entry.seq] {
					continue
				}
				if entry.seq < startSegment {
					reportedSegments[entry.seq] = true
					continue
				}

				seg := domain.Segment{
					SequenceID: entry.seq,
					Duration:   entry.duration,
					WorkerID:   w.workerID,
					Timestamp:  entry.startTime,
				}

				if err := w.queue.AddSegment(context.Background(), jobID, seg); err != nil {
					log.Printf("[Worker %s] Failed to report segment %d: %v", w.workerID, entry.seq, err)
				} else {
					log.Printf("[Worker %s] SEGMENT: %d (Dur: %.2fs, Start: %.2fs). Reported.",
						w.workerID, entry.seq, entry.duration, entry.startTime)
					reportedSegments[entry.seq] = true
					IncSegmentReported()
				}
			}
		}
	}
}

type manifestEntry struct {
	seq       int
	duration  float64
	startTime float64
}

// parseHLSManifest reads ffmpeg's output .m3u8 and extracts segment info.
// Returns nil if the manifest does not exist yet (ffmpeg has not created it).
func (w *TranscodeWorker) parseHLSManifest(manifestPath string) []manifestEntry {
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var entries []manifestEntry
	scanner := bufio.NewScanner(file)
	currentDuration := 0.0
	accumulatedTime := 0.0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			durStr := strings.TrimPrefix(line, "#EXTINF:")
			durStr = strings.TrimSuffix(durStr, ",")
			currentDuration, _ = strconv.ParseFloat(durStr, 64)
		} else if strings.HasPrefix(line, "segment_") && strings.HasSuffix(line, ".ts") {
			numStr := strings.TrimSuffix(strings.TrimPrefix(line, "segment_"), ".ts")
			if num, err := strconv.Atoi(numStr); err == nil {
				entries = append(entries, manifestEntry{
					seq:       num,
					duration:  currentDuration,
					startTime: accumulatedTime,
				})
				accumulatedTime += currentDuration
			}
			currentDuration = 0.0
		}
	}

	return entries
}
