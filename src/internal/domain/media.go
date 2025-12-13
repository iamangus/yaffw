package domain

import "time"

type MediaType string

const (
	MediaTypeMovie   MediaType = "Movie"
	MediaTypeEpisode MediaType = "Episode"
	MediaTypeSeries  MediaType = "Series"
)

type MediaItem struct {
	ID        string
	Path      string
	Title     string
	Type      MediaType
	PosterURL string
	Duration  time.Duration
	CreatedAt time.Time
}

type TranscodeJob struct {
	ID        string
	MediaID   string
	FilePath  string
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
