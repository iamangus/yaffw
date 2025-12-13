package services

import (
	"context"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type PlaybackService struct {
	mediaRepo   ports.MediaRepository
	ffprobePath string
}

func NewPlaybackService(repo ports.MediaRepository, ffprobePath string) *PlaybackService {
	return &PlaybackService{
		mediaRepo:   repo,
		ffprobePath: ffprobePath,
	}
}

// ProbeMedia uses ffprobe (or ffmpeg -i) to get media info
func (s *PlaybackService) ProbeMedia(ctx context.Context, filePath string) (*domain.MediaProbe, error) {
	// Use ffmpeg -i since we may not have ffprobe
	ffmpegPath := strings.Replace(s.ffprobePath, "ffprobe", "ffmpeg", 1)

	cmd := exec.CommandContext(ctx, ffmpegPath, "-i", filePath)
	output, _ := cmd.CombinedOutput() // ffmpeg -i returns non-zero, ignore error

	probe := &domain.MediaProbe{}
	outputStr := string(output)

	// Parse video codec
	videoRegex := regexp.MustCompile(`Stream #\d+:\d+.*Video: (\w+)`)
	if matches := videoRegex.FindStringSubmatch(outputStr); len(matches) > 1 {
		probe.VideoCodec = strings.ToLower(matches[1])
	}

	// Parse audio codec
	audioRegex := regexp.MustCompile(`Stream #\d+:\d+.*Audio: (\w+)`)
	if matches := audioRegex.FindStringSubmatch(outputStr); len(matches) > 1 {
		probe.AudioCodec = strings.ToLower(matches[1])
	}

	// Parse resolution
	resRegex := regexp.MustCompile(`(\d{3,4})x(\d{3,4})`)
	if matches := resRegex.FindStringSubmatch(outputStr); len(matches) > 2 {
		probe.Width, _ = strconv.Atoi(matches[1])
		probe.Height, _ = strconv.Atoi(matches[2])
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
	default:
		probe.Container = ext
	}

	log.Printf("Probed %s: video=%s audio=%s container=%s res=%dx%d",
		filePath, probe.VideoCodec, probe.AudioCodec, probe.Container, probe.Width, probe.Height)

	return probe, nil
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
		job.TargetVideoCodec = "libx264" // Safest fallback
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

	// If we need to transcode, we're outputting HLS
	if job.NeedsTranscode {
		job.TargetContainer = "hls"
	}

	return job
}
