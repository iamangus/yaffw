package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type TranscodeWorker struct {
	queue      ports.JobQueue
	workerID   string
	ffmpegPath string
	tempDir    string
}

func NewTranscodeWorker(queue ports.JobQueue, id string) *TranscodeWorker {
	// Use system ffmpeg
	ffmpegPath := "ffmpeg"

	// Create temp dir
	cwd, _ := os.Getwd()
	tempDir := filepath.Join(cwd, "temp", "transcode")
	os.MkdirAll(tempDir, 0755)

	return &TranscodeWorker{
		queue:      queue,
		workerID:   id,
		ffmpegPath: ffmpegPath,
		tempDir:    tempDir,
	}
}

func (w *TranscodeWorker) Start(ctx context.Context) {
	log.Printf("Worker %s started using ffmpeg at %s", w.workerID, w.ffmpegPath)

	// Start Internal HTTP Server for serving segments
	go func() {
		port := os.Getenv("WORKER_PORT")
		if port == "" {
			port = "8080"
		}
		log.Printf("Worker %s starting internal file server on :%s serving %s", w.workerID, port, w.tempDir)

		mux := http.NewServeMux()
		mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
			// Handle Playlist
			if strings.HasSuffix(r.URL.Path, ".m3u8") {
				// Path: /job-id/stream.m3u8
				parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
				if len(parts) >= 1 {
					jobID := parts[0]
					jobDir := filepath.Join(w.tempDir, jobID)

					// OPTIMIZATION: Check if we can serve the FFmpeg playlist directly
					// If it starts with Sequence 0, it's a complete playlist (no crash recovery needed yet)
					playlistPath := filepath.Join(jobDir, "stream.m3u8")
					content, err := os.ReadFile(playlistPath)
					if err == nil {
						if strings.Contains(string(content), "#EXT-X-MEDIA-SEQUENCE:0") {
							// Fast path: Serve directly
							rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
							rw.Write(content)
							return
						}
					}

					ServeDynamicPlaylist(rw, jobID, jobDir)
					return
				}
			}
			// Default file server
			http.FileServer(http.Dir(w.tempDir)).ServeHTTP(rw, r)
		})

		if err := http.ListenAndServe(":"+port, mux); err != nil {
			log.Printf("Worker file server failed: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			job, err := w.queue.Dequeue(ctx)
			if err != nil {
				if err.Error() != "empty queue" {
					log.Printf("Dequeue error: %v", err)
				}
				time.Sleep(1 * time.Second)
				continue
			}
			log.Printf("Worker dequeued job %s (Status: %s, Restarts: %d)", job.ID, job.Status, job.RestartCount)
			w.processJob(ctx, job)
		}
	}
}

func (w *TranscodeWorker) processJob(ctx context.Context, job *domain.TranscodeJob) {
	log.Printf("[Worker %s] Picked up job %s for media %s", w.workerID, job.ID, job.FilePath)

	// Update Status: Processing & Immediate Heartbeat
	job.Status = "Processing"
	job.WorkerID = w.workerID

	// Set Worker Address
	workerIP := os.Getenv("WORKER_IP")
	if workerIP == "" {
		workerIP = "127.0.0.1"
	}
	workerPort := os.Getenv("WORKER_PORT")
	if workerPort == "" {
		workerPort = "8080"
	}
	job.WorkerAddress = fmt.Sprintf("http://%s:%s", workerIP, workerPort)

	job.LastHeartbeat = time.Now() // Critical: Set heartbeat immediately
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
	jobDir := filepath.Join(w.tempDir, job.ID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to create job directory %s: %v", w.workerID, jobDir, err)
		job.Status = "Failed"
		w.queue.UpdateJob(ctx, job)
		return
	}

	outputPath := filepath.Join(jobDir, "stream.m3u8")

	// Start Heartbeat Loop (Managed via context)
	// We start this AFTER sending the first update, but parallel to FFmpeg
	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel() // Safety fallback

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				job.LastHeartbeat = time.Now()
				if err := w.queue.UpdateJob(context.Background(), job); err != nil {
					log.Printf("[Worker %s] WARNING: Heartbeat update failed for job %s: %v", w.workerID, job.ID, err)
				}
			}
		}
	}()

	// Calculate resume point based on actual transcoded content
	startSegment := 0
	startTime := 0.0
	if job.RestartCount > 0 {
		// Analyze existing segments to determine actual progress
		startSegment, startTime = w.analyzeExistingSegments(jobDir)
		if startSegment > 0 {
			log.Printf("[Worker %s] RECOVERY: Resuming job %s from segment %d (time: %.2fs). Previous segments found.",
				w.workerID, job.ID, startSegment, startTime)

			// Update job with progress info
			job.LastSegmentNum = startSegment - 1
			job.TranscodedDuration = startTime

			// Hard Reset Strategy: Delete the old playlist.
			// Our dynamic playlist generator will recreate it properly.
			os.Remove(outputPath)
		}
	}

	// Build FFmpeg command based on job settings
	args := w.buildFFmpegArgs(job, jobDir, outputPath, startSegment, startTime)
	cmd := exec.Command(w.ffmpegPath, args...)

	// Redirect logs to file for debugging
	logFile, err := os.Create(filepath.Join(jobDir, "ffmpeg.log"))
	if err == nil {
		cmd.Stderr = logFile
		cmd.Stdout = logFile
		defer logFile.Close()
	} else {
		log.Printf("Failed to create log file: %v", err)
	}

	log.Printf("Running FFmpeg: %s", cmd.String())

	if err := cmd.Start(); err != nil {
		log.Printf("[Worker %s] CRITICAL: Failed to start FFmpeg process for job %s: %v", w.workerID, job.ID, err)
		job.Status = "Failed"
		w.queue.UpdateJob(ctx, job)
		return
	}

	// Poll for manifest creation (up to 30s)
	ready := false
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(outputPath); err == nil {
			ready = true
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !ready {
		log.Printf("[Worker %s] ERROR: Timed out waiting for HLS manifest creation at %s", w.workerID, outputPath)
		cmd.Process.Kill()
		job.Status = "Failed"
		w.queue.UpdateJob(ctx, job)
		return
	}

	// Update Job
	job.Status = "Ready"
	job.StreamURL = fmt.Sprintf("/hls/%s/stream.m3u8", job.ID)
	w.queue.UpdateJob(ctx, job)

	if job.RestartCount > 0 {
		log.Printf("[Worker %s] RECOVERY: Stream manifest ready for job %s. Stream resumed. (FFmpeg PID %d)", w.workerID, job.ID, cmd.Process.Pid)
	} else {
		log.Printf("[Worker %s] Job %s Ready. Stream started. (FFmpeg PID %d)", w.workerID, job.ID, cmd.Process.Pid)
	}

	// Wait for completion (blocking this goroutine to keep heartbeats alive)
	err = cmd.Wait()
	hbCancel() // Stop heartbeats only after FFmpeg finishes
	
	if err != nil {
		log.Printf("[Worker %s] Job %s FFmpeg finished with error: %v", w.workerID, job.ID, err)
	} else {
		log.Printf("[Worker %s] Job %s FFmpeg finished successfully", w.workerID, job.ID)
	}
}

// analyzeExistingSegments scans the job directory to find existing segments
// and calculates the actual transcoded duration by probing segment files
func (w *TranscodeWorker) analyzeExistingSegments(jobDir string) (nextSegment int, totalDuration float64) {
	files, err := filepath.Glob(filepath.Join(jobDir, "segment_*.ts"))
	if err != nil || len(files) == 0 {
		return 0, 0.0
	}

	// Parse segment numbers and sort them
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
	// Iterate backwards to find the last valid segment
	for i := len(segments) - 1; i >= 0; i-- {
		seg := segments[i]
		_, duration, err := w.probeSegment(seg.Path)
		
		if err != nil {
			log.Printf("[Worker %s] RECOVERY: Found corrupt segment %s (Error: %v). Deleting...", w.workerID, seg.Path, err)
			os.Remove(seg.Path)
			continue // Skip this segment and check the previous one
		}

		// Optimization: If we found a valid segment, we assume previous ones are valid (tail corruption only)
		// Check if the segment is significantly shorter than target duration (4s), indicating truncation
		if duration < 2.0 {
			log.Printf("[Worker %s] RECOVERY: Segment %s is valid but truncated (Duration: %.2fs). Deleting...", w.workerID, seg.Path, duration)
			os.Remove(seg.Path)
			continue
		}

		log.Printf("[Worker %s] RECOVERY: Segment %s is valid (Duration: %.2fs). Stopping verification.", w.workerID, seg.Path, duration)
		break
	}

	// Re-scan to get the clean list after deletion
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

	// Calculate resume point from the last segment's timestamp + duration
	// This handles cases where segments might overlap or have gaps, ensuring we resume exactly where we left off.
	lastSeg := segments[len(segments)-1]
	startTimeVal, duration, err := w.probeSegment(lastSeg.Path)
	if err != nil {
		log.Printf("[Worker %s] RECOVERY: Failed to probe last segment %s: %v. Fallback to 0.", w.workerID, lastSeg.Path, err)
		return 0, 0.0
	}
	
	totalDuration = startTimeVal + duration

	// Next segment is one after the highest numbered segment
	nextSegment = segments[len(segments)-1].Number + 1

	log.Printf("[Worker %s] RECOVERY ANALYSIS: Found %d existing segments (Range: %d-%d), resume time: %.2fs (Last Seg Start: %.2f + Dur: %.2f), next segment: %d",
		w.workerID, len(segments), segments[0].Number, segments[len(segments)-1].Number, totalDuration, startTimeVal, duration, nextSegment)

	return nextSegment, totalDuration
}

// probeSegment uses ffprobe to get start time and duration of a segment
func (w *TranscodeWorker) probeSegment(segmentPath string) (startTime float64, duration float64, err error) {
	// Use system ffprobe
	ffprobePath := "ffprobe"

	cmd := exec.Command(ffprobePath,
		"-v", "error", // Use error level to catch issues
		"-print_format", "json",
		"-show_format",
		segmentPath,
	)

	// Capture stderr to see why it failed
	var stderr strings.Builder
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		return 0.0, 0.0, fmt.Errorf("ffprobe failed: %v (Stderr: %s)", err, stderr.String())
	}

	var result struct {
		Format struct {
			Duration  string `json:"duration"`
			StartTime string `json:"start_time"`
		} `json:"format"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return 0.0, 0.0, fmt.Errorf("invalid json: %v", err)
	}

	duration, err = strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil {
		return 0.0, 0.0, fmt.Errorf("invalid duration: %v", err)
	}

	// StartTime might be missing or "N/A" in some cases, default to 0 if parsing fails
	// But for HLS segments it should be there.
	startTime, _ = strconv.ParseFloat(result.Format.StartTime, 64)

	return startTime, duration, nil
}

func (w *TranscodeWorker) buildFFmpegArgs(job *domain.TranscodeJob, jobDir, outputPath string, startSegment int, startTime float64) []string {
	args := []string{}

	// Seeking input (if resuming) - use accurate seek time from segment analysis
	if startSegment > 0 && startTime > 0 {
		// Use precise time from segment analysis instead of estimated time
		args = append(args, "-ss", fmt.Sprintf("%.3f", startTime))
	}

	args = append(args, "-i", job.FilePath)

	// Determine video codec
	videoCodec := job.TargetVideoCodec
	if videoCodec == "" {
		videoCodec = "libx264" // Default fallback
	}

	// Force re-encode on resume to ensure timestamp alignment and accurate seeking.
	// Stream copy with seeking often causes gaps/discontinuities in HLS.
	if startSegment > 0 {
		videoCodec = "libx264"
	}

	if videoCodec == "copy" {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args,
			"-c:v", videoCodec,
			"-preset", "ultrafast",
			"-pix_fmt", "yuv420p",
			"-profile:v", "main",
			"-g", "100", // Force keyframe every 4s (25fps * 4) to match HLS segment time
			"-keyint_min", "100",
			"-sc_threshold", "0", // Disable scene change detection to enforce strict GOP
		)
	}

	// Determine audio codec
	audioCodec := job.TargetAudioCodec
	if audioCodec == "" {
		audioCodec = "aac" // Default fallback
	}

	if audioCodec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args,
			"-c:a", audioCodec,
			"-ac", "2", // Downmix to stereo for browser compatibility
		)
	}

	// Disable subtitles
	args = append(args, "-sn")

	// HLS output settings
	args = append(args,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(jobDir, "segment_%03d.ts"),
	)

	// If resuming, we need to tell HLS muxer to start numbering segments correctly
	if startSegment > 0 {
		args = append(args, "-start_number", fmt.Sprintf("%d", startSegment))
		args = append(args, "-output_ts_offset", fmt.Sprintf("%.3f", startTime))
		// Disabled append_list in favor of Hard Reset strategy
		// args = append(args, "-hls_flags", "append_list")
	}

	args = append(args, outputPath)

	log.Printf("[Worker %s] FFmpeg args: video=%s, audio=%s, startSeg=%d, seekTime=%.2fs",
		w.workerID, videoCodec, audioCodec, startSegment, startTime)

	return args
}
