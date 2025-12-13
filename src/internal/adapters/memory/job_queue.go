package memory

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type InMemoryJobQueue struct {
	queue []*domain.TranscodeJob
	jobs  map[string]*domain.TranscodeJob // Persistence
	mu    sync.RWMutex
	cond  *sync.Cond
}

func NewJobQueue() *InMemoryJobQueue {
	jq := &InMemoryJobQueue{
		queue: make([]*domain.TranscodeJob, 0),
		jobs:  make(map[string]*domain.TranscodeJob),
	}
	jq.cond = sync.NewCond(&jq.mu)
	return jq
}

func (q *InMemoryJobQueue) Enqueue(ctx context.Context, job *domain.TranscodeJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.jobs[job.ID] = job
	q.queue = append(q.queue, job)
	log.Printf("[Queue] Enqueued job %s (Queue size: %d)", job.ID, len(q.queue))
	q.cond.Signal()
	return nil
}

func (q *InMemoryJobQueue) Dequeue(ctx context.Context) (*domain.TranscodeJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.queue) == 0 {
		return nil, errors.New("empty queue")
	}

	job := q.queue[0]
	q.queue = q.queue[1:]
	log.Printf("[Queue] Dequeued job %s (Queue size: %d)", job.ID, len(q.queue))
	return job, nil
}

func (q *InMemoryJobQueue) GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	job, ok := q.jobs[jobID]
	if !ok {
		return nil, errors.New("job not found")
	}
	return job, nil
}

func (q *InMemoryJobQueue) UpdateJob(ctx context.Context, job *domain.TranscodeJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, ok := q.jobs[job.ID]; !ok {
		return errors.New("job not found")
	}
	q.jobs[job.ID] = job
	return nil
}

func (q *InMemoryJobQueue) ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	active := make([]*domain.TranscodeJob, 0)
	for _, job := range q.jobs {
		if job.Status == "Processing" || job.Status == "Ready" {
			active = append(active, job)
		}
	}
	return active, nil
}
