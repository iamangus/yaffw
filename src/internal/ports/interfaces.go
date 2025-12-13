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

type JobQueue interface {
	Enqueue(ctx context.Context, job *domain.TranscodeJob) error
	Dequeue(ctx context.Context) (*domain.TranscodeJob, error)
	// Simple state tracking for the demo
	GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error)
	UpdateJob(ctx context.Context, job *domain.TranscodeJob) error
	// Management
	ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error)
}
