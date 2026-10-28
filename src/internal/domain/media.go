package domain

import (
	"errors"
	"time"
)

var ErrRecoveryInProgress = errors.New("recovery already in progress")

type MediaType string

const (
	MediaTypeMovie     MediaType = "Movie"
	MediaTypeEpisode   MediaType = "Episode"
	MediaTypeSeries    MediaType = "Series"
	MediaTypeSeason    MediaType = "Season"    // Intermediate container
	MediaTypeContainer MediaType = "Container" // Generic container
)

type CastMember struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	ImageURL string `json:"imageUrl"`
}

type MediaMetadata struct {
	Title       string        `json:"title"`
	Overview    string        `json:"overview"`
	ReleaseDate time.Time     `json:"releaseDate"`
	Rating      float64       `json:"rating"` // 0-10
	Genres      []string      `json:"genres"`
	Cast        []CastMember  `json:"cast"`
	BackdropURL string        `json:"backdropUrl"`
	PosterImage string        `json:"posterImage"`
	OriginalID  string        `json:"originalId"`
	Duration    time.Duration `json:"duration,omitempty"`

	// NFO-supported fields
	OriginalTitle string            `json:"originalTitle,omitempty"`
	Tagline       string            `json:"tagline,omitempty"`
	Countries     []string          `json:"countries,omitempty"`
	Studios       []string          `json:"studios,omitempty"`
	Directors     []string          `json:"directors,omitempty"`
	Writers       []string          `json:"writers,omitempty"`
	Certification string            `json:"certification,omitempty"`
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`
}

type MediaItem struct {
	ID         string                 `json:"id"`
	LibraryID  string                 `json:"libraryId"`
	ParentID   string                 `json:"parentId,omitempty"`
	Path       string                 `json:"-"`
	Title      string                 `json:"title"`
	Type       MediaType              `json:"type"`
	Duration   time.Duration          `json:"duration,omitempty"`
	CreatedAt  time.Time              `json:"createdAt"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`

	// In-memory only (not persisted in DB)
	Identity  string         `json:"-"`                     // e.g., "tmdb:603" or "hierarchy:..."
	PosterURL string         `json:"posterUrl,omitempty"`   // computed during scan
	BackURL   string         `json:"backdropUrl,omitempty"` // computed during scan
	Metadata  *MediaMetadata `json:"metadata,omitempty"`    // populated on-demand for detail view
	Missing   bool           `json:"missing,omitempty"`     // file not found during last scan
}

type TranscodeJob struct {
	ID        string
	MediaID   string
	FilePath  string
	Status    string // Pending, Processing, Ready, Failed, Completed, Cancelled
	WorkerID  string
	StreamURL string // Public URL for the client

	// Target encoding settings (determined by client capabilities)
	TargetVideoCodec string // e.g., "libx264", "libx265", "copy"
	TargetAudioCodec string // e.g., "aac", "copy"
	TargetContainer  string // e.g., "hls", "mp4"
	NeedsTranscode   bool

	// Recovery fields
	LastHeartbeat time.Time
	RestartCount  int

	// Progress tracking for seamless resume
	LastSegmentNum     int     // Last segment number written before crash
	TranscodedDuration float64 // Total seconds transcoded so far

	// JIT/Recovery Start Point (set by Control Plane)
	StartSegment int     `json:"startSegment"`
	StartTime    float64 `json:"startTime"`

	// Recovery deduplication tracking
	RecoveryInProgress bool      // Set to true when JIT recovery is triggered
	LastRecoveryTime   time.Time // Timestamp of last recovery trigger
	LastAccessedAt     time.Time // Timestamp of last client access (playlist or segment)

	// C3: Number of past worker deaths, used for #EXT-X-DISCONTINUITY-SEQUENCE
	DiscontinuityCount int `json:"discontinuityCount"`

	// C4: Reason for failure, surfaced to client on permanent failure
	FailureReason string `json:"failureReason"`

	// V2: Segment Tracking for Ephemeral Workers
	Segments []Segment

	// Phase 3: Range-parallel transcoding
	ParentJobID string  `json:"parentJobId"` // Non-empty for sub-jobs
	RangeIndex  int     `json:"rangeIndex"`  // 0..N-1 for sub-jobs
	TotalRanges int     `json:"totalRanges"` // N (on parent)
	RangeStart  float64 `json:"rangeStart"`  // seconds, keyframe-aligned
	RangeEnd    float64 `json:"rangeEnd"`    // seconds, keyframe-aligned
}

type Segment struct {
	SequenceID int     `json:"seq"`
	Duration   float64 `json:"dur"`
	WorkerID   string  `json:"worker"` // Which worker produced this (for discontinuity detection)
	Timestamp  float64 `json:"ts"`     // Start time of segment
}

// ClientCapabilities represents what the client browser can play
type ClientCapabilities struct {
	// Video codecs
	H264 bool `json:"h264"`
	H265 bool `json:"h265"`
	VP9  bool `json:"vp9"`
	AV1  bool `json:"av1"`

	// Audio codecs
	AAC  bool `json:"aac"`
	AC3  bool `json:"ac3"`
	EAC3 bool `json:"eac3"`
	Opus bool `json:"opus"`

	// Containers
	HLS  bool `json:"hls"`
	MP4  bool `json:"mp4"`
	WebM bool `json:"webm"`
	MKV  bool `json:"mkv"`

	// Max resolution
	MaxWidth  int `json:"maxWidth"`
	MaxHeight int `json:"maxHeight"`
}

// MediaProbe contains info about a media file's streams
type MediaProbe struct {
	VideoCodec string
	AudioCodec string
	Container  string
	Width      int
	Height     int
}
