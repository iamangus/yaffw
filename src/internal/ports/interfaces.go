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
