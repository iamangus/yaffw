package services

import (
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
	"sync"
)

var (
	durationCache = make(map[string]float64)
	cacheMutex    sync.RWMutex
)

// SegmentInfo holds info about a single .ts segment
type SegmentInfo struct {
	Number   int
	Duration float64
	FilePath string
}

// getSegmentDuration uses ffprobe to get actual duration of a segment file
func getSegmentDuration(segmentPath string) (float64, error) {
	// Check cache first
	cacheMutex.RLock()
	if duration, ok := durationCache[segmentPath]; ok {
		cacheMutex.RUnlock()
		return duration, nil
	}
	cacheMutex.RUnlock()

	// Use system ffprobe
	ffprobePath := "ffprobe"

	// Use ffprobe to get duration in JSON format
	cmd := exec.Command(ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		segmentPath,
	)

	output, err := cmd.Output()
	if err != nil {
		return 4.0, err // Fallback to 4.0s on error
	}

	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return 4.0, err
	}

	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil {
		return 4.0, err
	}

	// Update cache
	cacheMutex.Lock()
	durationCache[segmentPath] = duration
	cacheMutex.Unlock()

	return duration, nil
}

func ServeDynamicPlaylist(w http.ResponseWriter, jobID, jobDir string) {
	log.Printf("[Playlist] Generating dynamic playlist for job %s", jobID)

	// 1. Scan directory for segments
	files, err := os.ReadDir(jobDir)
	if err != nil {
		log.Printf("[Playlist] Failed to read job dir %s: %v", jobDir, err)
		http.Error(w, "Failed to read job dir", http.StatusInternalServerError)
		return
	}

	var segments []SegmentInfo
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "segment_") && strings.HasSuffix(f.Name(), ".ts") {
			numStr := strings.TrimSuffix(strings.TrimPrefix(f.Name(), "segment_"), ".ts")
			if num, err := strconv.Atoi(numStr); err == nil {
				segPath := filepath.Join(jobDir, f.Name())
				segments = append(segments, SegmentInfo{
					Number:   num,
					FilePath: segPath,
				})
			}
		}
	}

	// 2. Sort segments by number
	sort.Slice(segments, func(i, j int) bool {
		return segments[i].Number < segments[j].Number
	})

	if len(segments) == 0 {
		log.Printf("[Playlist] No segments found in %s", jobDir)
		http.Error(w, "No segments found", http.StatusNotFound)
		return
	}

	// 3. Get actual durations for each segment
	maxDuration := 4.0
	for i := range segments {
		// Optimization: Skip probing for older segments (assume target duration)
		// We only probe the last 10 segments to catch the variable tip.
		// This speeds up recovery significantly when there are hundreds of segments.
		if i < len(segments)-10 {
			segments[i].Duration = 4.0
		} else {
			duration, err := getSegmentDuration(segments[i].FilePath)
			if err != nil {
				duration = 4.0
			}
			segments[i].Duration = duration
		}

		if segments[i].Duration > maxDuration {
			maxDuration = segments[i].Duration
		}
	}

	// 4. Find discontinuities (gaps in segment numbering = worker crashed and resumed)
	type discontinuity struct {
		afterIndex int // Index in segments slice after which discontinuity occurs
		gapStart   int // Segment number where gap starts
		gapEnd     int // Segment number where gap ends
	}
	var discontinuities []discontinuity

	for i := 1; i < len(segments); i++ {
		if segments[i].Number != segments[i-1].Number+1 {
			discontinuities = append(discontinuities, discontinuity{
				afterIndex: i - 1,
				gapStart:   segments[i-1].Number,
				gapEnd:     segments[i].Number,
			})
			log.Printf("[Playlist] Discontinuity: gap between segment %d and %d",
				segments[i-1].Number, segments[i].Number)
		}
	}

	// 5. Build Playlist
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	fmt.Fprintf(w, "#EXTM3U\n")
	fmt.Fprintf(w, "#EXT-X-VERSION:6\n") // Version 6 for better discontinuity support
	fmt.Fprintf(w, "#EXT-X-TARGETDURATION:%d\n", int(maxDuration)+1)
	fmt.Fprintf(w, "#EXT-X-MEDIA-SEQUENCE:0\n") // Always 0 for VOD-style playback
	fmt.Fprintf(w, "#EXT-X-PLAYLIST-TYPE:EVENT\n")
	fmt.Fprintf(w, "#EXT-X-INDEPENDENT-SEGMENTS\n") // Each segment can be decoded independently

	// Track which discontinuities we've emitted
	discoIndex := 0

	for i, seg := range segments {
		// Check if we need to emit a discontinuity before this segment
		if discoIndex < len(discontinuities) && discontinuities[discoIndex].afterIndex == i-1 {
			fmt.Fprintf(w, "#EXT-X-DISCONTINUITY\n")
			discoIndex++
		}

		fmt.Fprintf(w, "#EXTINF:%.6f,\n", seg.Duration)
		fmt.Fprintf(w, "segment_%03d.ts\n", seg.Number)
	}

	// Don't add ENDLIST - playlist is still growing while transcoding

	log.Printf("[Playlist] Generated: %d segments, %d discontinuities, first=%d, last=%d",
		len(segments), len(discontinuities), segments[0].Number, segments[len(segments)-1].Number)
}