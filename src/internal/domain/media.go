package domain

import "time"

type MediaType string

const (
	MediaTypeMovie   MediaType = "Movie"
	MediaTypeEpisode MediaType = "Episode"
	MediaTypeSeries  MediaType = "Series"
)

type MetadataStatus string

const (
	MetadataStatusNew        MetadataStatus = "New"
	MetadataStatusPending    MetadataStatus = "Pending"
	MetadataStatusProcessing MetadataStatus = "Processing"
	MetadataStatusReady      MetadataStatus = "Ready"
	MetadataStatusFailed     MetadataStatus = "Failed"
)

type MediaMetadata struct {
	Title       string    `json:"title"`
	Overview    string    `json:"overview"`
	ReleaseDate time.Time `json:"releaseDate"`
	Rating      float64   `json:"rating"` // 0-10
	Genres      []string  `json:"genres"`
	BackdropURL string    `json:"backdropUrl"`
	PosterImage string    `json:"posterImage"` // Temporary holder for remote URL
	OriginalID  string    `json:"originalId"`  // e.g. TMDB ID
}

type MediaItem struct {
	ID        string
	Path      string
	Title     string
	Type      MediaType
	PosterURL string
	Duration  time.Duration
	CreatedAt time.Time

	// Metadata
	MetadataStatus MetadataStatus `json:"metadataStatus"`
	Metadata       *MediaMetadata `json:"metadata,omitempty"`

	// User-specific fields (populated on demand)
	ViewProgress *WatchProgress `json:"viewProgress,omitempty"`
}

type MetadataJob struct {
	ID        string
	MediaID   string
	Status    string // Pending, Processing, Completed, Failed
	CreatedAt time.Time
}

type TranscodeJob struct {
	ID            string
	MediaID       string
	FilePath      string
	Status        string // Pending, Processing, Ready, Failed
	WorkerID      string
	WorkerAddress string // IP:Port of the worker serving the files
	StreamURL     string // Public URL for the client

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

	// V2: Segment Tracking for Ephemeral Workers
	Segments []Segment
}

type Segment struct {
	SequenceID int     `json:"seq"`
	Duration   float64 `json:"dur"`
	WorkerID   string  `json:"worker"` // Which worker produced this
	WorkerAddr string  `json:"addr"`   // Address to fetch it from
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
