package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

// ErrEmptyQueue is returned by Dequeue when no pending jobs are available.
var ErrEmptyQueue = errors.New("empty queue")

type JobQueue interface {
	Enqueue(ctx context.Context, job *domain.TranscodeJob) error
	Dequeue(ctx context.Context, workerID string) (*domain.TranscodeJob, error)
	GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error)
	UpdateJob(ctx context.Context, job *domain.TranscodeJob) error
	AddSegment(ctx context.Context, jobID string, segment domain.Segment) error
	MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) error
	PurgeJobs(ctx context.Context, jobID string) error
	DeleteJob(ctx context.Context, jobID string) error
	TouchJob(ctx context.Context, jobID string) error
	ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error)
	Heartbeat(ctx context.Context, jobID string, workerID string) error
	GetSegment(ctx context.Context, jobID string, seqID int) (*domain.Segment, error)
}

type LockManager interface {
	TryAcquireLock(ctx context.Context, key string, ttlSeconds int) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

// HierarchyInfo contains parsed directory/filename structure for a media file.
type HierarchyInfo struct {
	LibraryType   string // "movie" or "series"
	SeriesTitle   string // parsed from directory (TV only)
	SeasonNumber  int    // from "Season XX" dir or S01E01
	EpisodeNumber int    // from S01E01
	FileName      string // basename of media file
}

// DiscoveryResult is returned by a metadata provider during filesystem scanning.
// It provides the identity and basic info needed to generate a stable UUID.
type DiscoveryResult struct {
	Identity    string // e.g., "tmdb:603", "omniarr:abc-123", "hierarchy:series:Breaking Bad:season:1:episode:1"
	Title       string
	PosterURL   string // e.g., "/local-images/{uuid}/poster.jpg" or "/images/{library}/{entity_uuid}_poster.jpg"
	BackdropURL string
	Duration    time.Duration
}

// MetadataProvider abstracts metadata fetching from any source (NFO files, omniarr, TMDB, etc.)
type MetadataProvider interface {
	// Discover resolves identity + basic info during filesystem scan.
	// Called for NEW files only (existing files reuse stored identity).
	Discover(ctx context.Context, path string, info HierarchyInfo) (*DiscoveryResult, error)

	// FetchMetadata retrieves full metadata on-demand (detail view).
	FetchMetadata(ctx context.Context, item *domain.MediaItem) (*domain.MediaMetadata, error)
}

// IdentityRecord represents a row in the item_identity table.
// This is NOT metadata — it's the minimum state needed for UUID stability across rescans.
type IdentityRecord struct {
	UUID         string
	LibraryID    string
	PathHash     string
	Identity     string // e.g., "tmdb:603" or "hierarchy:..."
	HierarchyKey string // e.g., "series:Breaking Bad:season:1:episode:1"
	Title        string // for list view without calling provider
	LastSeen     time.Time
	Status       string // "active" or "missing"
	MissingSince *time.Time
}

// IdentityRepository persists item identity for UUID stability across rescans.
// This is NOT a metadata store — only identity anchors are persisted.
type IdentityRepository interface {
	InitSchema(ctx context.Context) error
	GetByPathHash(ctx context.Context, pathHash string) (*IdentityRecord, error)
	GetByIdentity(ctx context.Context, identity string) (*IdentityRecord, error)
	GetByHierarchyKey(ctx context.Context, hierarchyKey string) (*IdentityRecord, error)
	GetByID(ctx context.Context, uuid string) (*IdentityRecord, error)
	Save(ctx context.Context, record *IdentityRecord) error
	MarkMissing(ctx context.Context, uuid string) error
	ListMissing(ctx context.Context) ([]*IdentityRecord, error)
	ListAll(ctx context.Context) ([]*IdentityRecord, error)
}
