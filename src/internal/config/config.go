package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ControlPlaneConfig holds configuration for the Control Plane service
type ControlPlaneConfig struct {
	DatabaseURL    string          `json:"database_url" yaml:"database_url"`
	Port           string          `json:"port" yaml:"port"`
	FrontendAPIKey string          `json:"frontend_api_key" yaml:"frontend_api_key"`
	Libraries      []LibraryConfig `json:"libraries" yaml:"libraries"`
	Transcode      TranscodeConfig `json:"transcode" yaml:"transcode"`
}

// LibraryConfig represents a filesystem-based library
type LibraryConfig struct {
	Name               string `json:"name" yaml:"name"`
	Path               string `json:"path" yaml:"path"`
	Type               string `json:"type" yaml:"type"`                           // "movie" or "series"
	MetadataProvider   string `json:"metadata_provider" yaml:"metadata_provider"` // "nfo" (default) or "omniarr"
	OmniarrURL         string `json:"omniarr_url,omitempty" yaml:"omniarr_url,omitempty"`
	OmniarrAPIKey      string `json:"omniarr_api_key,omitempty" yaml:"omniarr_api_key,omitempty"`
	ScanInterval       string `json:"scan_interval" yaml:"scan_interval"`               // e.g., "10m"
	WatchFS            bool   `json:"watch_fs" yaml:"watch_fs"`                         // enable inotify
	MissingGracePeriod string `json:"missing_grace_period" yaml:"missing_grace_period"` // "never" (default), "30d", etc.
}

// TranscodeConfig holds tunable thresholds for the transcode subsystem.
// All values default sensibly if zero/unset — see WithDefaults().
type TranscodeConfig struct {
	HLSSegmentTime      int    `json:"hls_segment_time" yaml:"hls_segment_time"`           // seconds, default 4
	HeartbeatInterval   int    `json:"heartbeat_interval" yaml:"heartbeat_interval"`       // seconds, default 1
	StatusCheckInterval int    `json:"status_check_interval" yaml:"status_check_interval"` // seconds, default 5
	DeadWorkerThreshold int    `json:"dead_worker_threshold" yaml:"dead_worker_threshold"` // seconds, default 3
	IdleTimeout         int    `json:"idle_timeout" yaml:"idle_timeout"`                   // seconds, default 60
	CompletedRetention  int    `json:"completed_retention" yaml:"completed_retention"`     // seconds, default 300
	ManifestTimeout     int    `json:"manifest_timeout" yaml:"manifest_timeout"`           // seconds, default 30
	MaxRestarts         int    `json:"max_restarts" yaml:"max_restarts"`                   // default 3
	InternalToken       string `json:"internal_token" yaml:"internal_token"`               // shared secret for /internal/queue
	ParallelRanges      int    `json:"parallel_ranges" yaml:"parallel_ranges"`             // Phase 3: N sub-jobs per file, 0/1 = single job
	SegmentDir          string `json:"segment_dir" yaml:"segment_dir"`                     // shared FS path for segment files (NFS), default "./temp/transcode"
}

// EncoderConfig holds ffmpeg encoder settings.
type EncoderConfig struct {
	Preset  string `json:"preset" yaml:"preset"`   // default "veryfast"
	CRF     int    `json:"crf" yaml:"crf"`         // default 23
	MaxRate string `json:"maxrate" yaml:"maxrate"` // e.g. "5M"
	BufSize string `json:"bufsize" yaml:"bufsize"` // e.g. "10M"
}

// WithDefaults returns a copy with zero values replaced by defaults.
func (c TranscodeConfig) WithDefaults() TranscodeConfig {
	d := c
	if d.HLSSegmentTime == 0 {
		d.HLSSegmentTime = 4
	}
	if d.HeartbeatInterval == 0 {
		d.HeartbeatInterval = 1
	}
	if d.StatusCheckInterval == 0 {
		d.StatusCheckInterval = 5
	}
	if d.DeadWorkerThreshold == 0 {
		d.DeadWorkerThreshold = 3
	}
	if d.IdleTimeout == 0 {
		d.IdleTimeout = 60
	}
	if d.CompletedRetention == 0 {
		d.CompletedRetention = 300
	}
	if d.ManifestTimeout == 0 {
		d.ManifestTimeout = 30
	}
	if d.MaxRestarts == 0 {
		d.MaxRestarts = 3
	}
	if d.SegmentDir == "" {
		d.SegmentDir = "./temp/transcode"
	}
	return d
}

// DefaultEncoder returns sensible encoder defaults.
func DefaultEncoder() EncoderConfig {
	return EncoderConfig{
		Preset:  "veryfast",
		CRF:     23,
		MaxRate: "5M",
		BufSize: "10M",
	}
}

// ComputePlaneConfig holds configuration for the Compute Plane service
type ComputePlaneConfig struct {
	ControlURL string          `json:"control_url" yaml:"control_url"`
	Transcode  TranscodeConfig `json:"transcode" yaml:"transcode"`
	Encoder    EncoderConfig   `json:"encoder" yaml:"encoder"`
}

// Load loads the configuration from a file (YAML or JSON)
func Load(path string, cfg interface{}) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open config file %s: %w", path, err)
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		decoder := yaml.NewDecoder(file)
		if err := decoder.Decode(cfg); err != nil {
			return fmt.Errorf("failed to decode YAML config file %s: %w", path, err)
		}
	} else {
		// Default to JSON for compatibility or other extensions
		decoder := json.NewDecoder(file)
		if err := decoder.Decode(cfg); err != nil {
			return fmt.Errorf("failed to decode JSON config file %s: %w", path, err)
		}
	}

	return nil
}
