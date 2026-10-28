package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

// Splitter handles Phase 3 range-parallel transcoding:
// probes keyframes, computes keyframe-aligned split points, and enqueues sub-jobs.
type Splitter struct {
	queue       ports.JobQueue
	ffprobePath string
}

func NewSplitter(queue ports.JobQueue, ffprobePath string) *Splitter {
	return &Splitter{queue: queue, ffprobePath: ffprobePath}
}

// ProbeKeyframes runs ffprobe to get keyframe PTS times from a video file.
// Returns a sorted slice of keyframe timestamps in seconds.
func (s *Splitter) ProbeKeyframes(ctx context.Context, filePath string) ([]float64, error) {
	cmd := exec.CommandContext(ctx, s.ffprobePath,
		"-show_packets",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags",
		"-of", "json",
		filePath,
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe keyframes failed: %v (stderr: %s)", err, stderr.String())
	}

	var result struct {
		Packets []struct {
			PTSTime string `json:"pts_time"`
			Flags   string `json:"flags"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("invalid json: %v", err)
	}

	var keyframes []float64
	for _, pkt := range result.Packets {
		if strings.Contains(pkt.Flags, "K_") {
			t, err := strconv.ParseFloat(pkt.PTSTime, 64)
			if err == nil {
				keyframes = append(keyframes, t)
			}
		}
	}

	sort.Float64s(keyframes)
	if len(keyframes) == 0 {
		return nil, fmt.Errorf("no keyframes found in %s", filePath)
	}
	return keyframes, nil
}

// ProbeDuration runs ffprobe to get the media duration in seconds.
func (s *Splitter) ProbeDuration(ctx context.Context, filePath string) (float64, error) {
	cmd := exec.CommandContext(ctx, s.ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "json",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration failed: %v", err)
	}

	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(result.Format.Duration, 64)
}

// ComputeSplitPoints calculates N keyframe-aligned split points for parallel transcoding.
// Returns split points where split[0]=0, split[N]=duration, and each interior point
// is the nearest keyframe <= the ideal equal-time cut.
func ComputeSplitPoints(keyframes []float64, duration float64, n int, hlsSegmentTime float64) []float64 {
	if n <= 1 || len(keyframes) == 0 {
		return []float64{0, duration}
	}

	// Guard: if media is too short, fall back to single job
	if duration < float64(n)*2*hlsSegmentTime {
		return []float64{0, duration}
	}

	splits := make([]float64, 0, n+1)
	splits = append(splits, 0.0)

	for i := 1; i < n; i++ {
		ideal := float64(i) * duration / float64(n)
		// Find nearest keyframe <= ideal
		best := 0.0
		for _, kf := range keyframes {
			if kf <= ideal {
				best = kf
			} else {
				break
			}
		}
		// Guard: collapse tiny ranges (< 2 * hls_time) into predecessor
		if best-splits[len(splits)-1] < 2*hlsSegmentTime {
			continue
		}
		splits = append(splits, best)
	}

	splits = append(splits, duration)
	return splits
}

// SplitAndEnqueue probes keyframes, computes split points, and enqueues N sub-jobs
// under a parent job. Returns the parent job ID and number of sub-jobs created.
func (s *Splitter) SplitAndEnqueue(ctx context.Context, parentJob *domain.TranscodeJob, n int, hlsSegmentTime float64) (string, int, error) {
	if n <= 1 {
		// No parallelism — enqueue as a single job (today's behavior)
		parentJob.Status = "Pending"
		if err := s.queue.Enqueue(ctx, parentJob); err != nil {
			return parentJob.ID, 0, err
		}
		return parentJob.ID, 1, nil
	}

	// Probe keyframes and duration
	keyframes, err := s.ProbeKeyframes(ctx, parentJob.FilePath)
	if err != nil {
		log.Printf("[Splitter] Keyframe probe failed for %s: %v. Falling back to single job.", parentJob.FilePath, err)
		parentJob.Status = "Pending"
		if err := s.queue.Enqueue(ctx, parentJob); err != nil {
			return parentJob.ID, 0, err
		}
		return parentJob.ID, 1, nil
	}

	duration, err := s.ProbeDuration(ctx, parentJob.FilePath)
	if err != nil || duration <= 0 {
		log.Printf("[Splitter] Duration probe failed for %s: %v. Falling back to single job.", parentJob.FilePath, err)
		parentJob.Status = "Pending"
		if err := s.queue.Enqueue(ctx, parentJob); err != nil {
			return parentJob.ID, 0, err
		}
		return parentJob.ID, 1, nil
	}

	splits := ComputeSplitPoints(keyframes, duration, n, hlsSegmentTime)
	actualN := len(splits) - 1

	// Enqueue parent job (tracks aggregate status)
	parentJob.Status = "Pending"
	parentJob.TotalRanges = actualN
	if err := s.queue.Enqueue(ctx, parentJob); err != nil {
		return parentJob.ID, 0, err
	}

	// Enqueue sub-jobs
	for i := 0; i < actualN; i++ {
		subJob := &domain.TranscodeJob{
			ID:               fmt.Sprintf("%s-r%d", parentJob.ID, i),
			MediaID:          parentJob.MediaID,
			FilePath:         parentJob.FilePath,
			Status:           "Pending",
			TargetVideoCodec: parentJob.TargetVideoCodec,
			TargetAudioCodec: parentJob.TargetAudioCodec,
			TargetContainer:  parentJob.TargetContainer,
			NeedsTranscode:   parentJob.NeedsTranscode,
			StartSegment:     int(splits[i] / hlsSegmentTime),
			StartTime:        splits[i],
			ParentJobID:      parentJob.ID,
			RangeIndex:       i,
			TotalRanges:      actualN,
			RangeStart:       splits[i],
			RangeEnd:         splits[i+1],
		}
		if err := s.queue.Enqueue(ctx, subJob); err != nil {
			log.Printf("[Splitter] Failed to enqueue sub-job %s: %v", subJob.ID, err)
			continue
		}
		log.Printf("[Splitter] Enqueued sub-job %s (range %d/%d: %.1fs-%.1fs, startSeg=%d)",
			subJob.ID, i, actualN, splits[i], splits[i+1], subJob.StartSegment)
	}

	IncRecoveryCount() // counts split events too
	return parentJob.ID, actualN, nil
}
