package services

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type PlaybackService struct {
	ffprobePath string
}

func NewPlaybackService(ffprobePath string) *PlaybackService {
	return &PlaybackService{
		ffprobePath: ffprobePath,
	}
}

// ProbeMedia uses ffprobe with JSON output (L8) — replaces fragile stderr regex parsing.
func (s *PlaybackService) ProbeMedia(ctx context.Context, filePath string) (*domain.MediaProbe, error) {
	cmd := exec.CommandContext(ctx, s.ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		filePath,
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, parseFFprobeError(err, stderr.String())
	}

	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	probe := &domain.MediaProbe{}
	for _, stream := range result.Streams {
		switch stream.CodecType {
		case "video":
			probe.VideoCodec = strings.ToLower(stream.CodecName)
			probe.Width = stream.Width
			probe.Height = stream.Height
		case "audio":
			probe.AudioCodec = strings.ToLower(stream.CodecName)
		}
	}

	// Determine container from extension
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp4", ".m4v":
		probe.Container = "mp4"
	case ".mkv":
		probe.Container = "mkv"
	case ".webm":
		probe.Container = "webm"
	case ".avi":
		probe.Container = "avi"
	case ".mov":
		probe.Container = "mov"
	case ".ts":
		probe.Container = "ts"
	default:
		probe.Container = ext
	}

	log.Printf("Probed %s: video=%s audio=%s container=%s res=%dx%d",
		filePath, probe.VideoCodec, probe.AudioCodec, probe.Container, probe.Width, probe.Height)

	return probe, nil
}

func parseFFprobeError(err error, stderr string) error {
	if stderr != "" {
		return &ffprobeError{err: err, stderr: stderr}
	}
	return err
}

type ffprobeError struct {
	err    error
	stderr string
}

func (e *ffprobeError) Error() string {
	return e.err.Error() + " (stderr: " + e.stderr + ")"
}

func (e *ffprobeError) Unwrap() error {
	return e.err
}

// DeterminePlaybackStrategy decides if we need to transcode
func (s *PlaybackService) DeterminePlaybackStrategy(
	probe *domain.MediaProbe,
	caps *domain.ClientCapabilities,
) *domain.TranscodeJob {

	job := &domain.TranscodeJob{
		NeedsTranscode:   false,
		TargetVideoCodec: "copy",
		TargetAudioCodec: "copy",
		TargetContainer:  "hls",
	}

	// Check video codec compatibility
	videoCompatible := false
	switch probe.VideoCodec {
	case "h264", "avc", "avc1":
		videoCompatible = caps.H264
	case "hevc", "h265", "hev1", "hvc1":
		videoCompatible = caps.H265
	case "vp9":
		videoCompatible = caps.VP9
	case "av1":
		videoCompatible = caps.AV1
	}

	if !videoCompatible {
		job.NeedsTranscode = true
		job.TargetVideoCodec = "libx264"
		log.Printf("Video codec %s not supported, will transcode to H.264", probe.VideoCodec)
	}

	// Check audio codec compatibility
	audioCompatible := false
	switch probe.AudioCodec {
	case "aac", "mp4a":
		audioCompatible = caps.AAC
	case "ac3", "ac-3":
		audioCompatible = caps.AC3
	case "eac3", "ec-3":
		audioCompatible = caps.EAC3
	case "opus":
		audioCompatible = caps.Opus
	}

	if !audioCompatible {
		job.NeedsTranscode = true
		job.TargetAudioCodec = "aac"
		log.Printf("Audio codec %s not supported, will transcode to AAC", probe.AudioCodec)
	}

	// Check container - browsers generally can't play MKV directly
	if probe.Container == "mkv" || probe.Container == "avi" {
		job.NeedsTranscode = true
		log.Printf("Container %s not supported for direct play, will remux/transcode", probe.Container)
	}

	if job.NeedsTranscode {
		job.TargetContainer = "hls"
	}

	return job
}

// ParseDuration parses a duration string from ffprobe JSON output.
func ParseDuration(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
