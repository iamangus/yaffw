package ports

import (
	"context"
	"github.com/yaffw/yaffw/src/internal/domain"
)

type MediaRepository interface {
	GetByID(ctx context.Context, id string) (*domain.MediaItem, error)
	ListAll(ctx context.Context) ([]domain.MediaItem, error)
	Save(ctx context.Context, item *domain.MediaItem) error
}

type UserRepository interface {
	GetByID(ctx context.Context, id string) (*domain.User, error)
	Save(ctx context.Context, user *domain.User) error
}

type HistoryRepository interface {
	GetProgress(ctx context.Context, userID, mediaID string) (*domain.WatchProgress, error)
	SaveProgress(ctx context.Context, progress *domain.WatchProgress) error
	ListContinueWatching(ctx context.Context, userID string) ([]domain.MediaItem, error)
}

type JobQueue interface {
	Enqueue(ctx context.Context, job *domain.TranscodeJob) error
	Dequeue(ctx context.Context) (*domain.TranscodeJob, error)
	// Simple state tracking for the demo
	GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error)
	UpdateJob(ctx context.Context, job *domain.TranscodeJob) error
	AddSegment(ctx context.Context, jobID string, segment domain.Segment) error
	MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) (int, error)
	PurgeJobs(ctx context.Context, jobID string) error
	DeleteJob(ctx context.Context, jobID string) error
	TouchJob(ctx context.Context, jobID string) error
	// Management
	ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error)
}

type MetadataProvider interface {
	Search(ctx context.Context, title string, year int, mediaType domain.MediaType) ([]domain.MediaMetadata, error)
	GetDetails(ctx context.Context, remoteID string, mediaType domain.MediaType) (*domain.MediaMetadata, error)
}

type MetadataQueue interface {
	Enqueue(ctx context.Context, job *domain.MetadataJob) error
	Dequeue(ctx context.Context) (*domain.MetadataJob, error)
	MarkCompleted(ctx context.Context, jobID string) error
	MarkFailed(ctx context.Context, jobID string, reason string) error
}

type LockManager interface {
	// TryAcquireLock attempts to acquire a distributed lock.
	// Returns true if acquired, false if taken by another pod.
	TryAcquireLock(ctx context.Context, key string, ttlSeconds int) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

type ImageStore interface {
	// Save downloads image from remoteURL and saves to storage (NFS)
	// Returns the public/relative URL (e.g. "/artwork/movie.jpg")
	Save(ctx context.Context, remoteURL string, filename string) (string, error)
}
