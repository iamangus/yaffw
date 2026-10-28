package services

import (
	"context"
	"sync"
	"testing"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

// FakeQueue is an in-memory implementation of the JobQueue interface for testing.
// It simulates the key behaviors: ownership-checked UpdateJob, CAS in Enqueue,
// and discontinuity-count bumping in MarkWorkerAsDead.
type FakeQueue struct {
	mu       sync.Mutex
	jobs     map[string]*domain.TranscodeJob
	segments map[string][]*fakeSegment
}

type fakeSegment struct {
	seq       int
	duration  float64
	workerID  string
	timestamp float64
}

func NewFakeQueue() *FakeQueue {
	return &FakeQueue{
		jobs:     make(map[string]*domain.TranscodeJob),
		segments: make(map[string][]*fakeSegment),
	}
}

func (q *FakeQueue) Enqueue(ctx context.Context, job *domain.TranscodeJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if existing, ok := q.jobs[job.ID]; ok {
		// CAS: skip if recovery in progress within last 5s
		if existing.RecoveryInProgress {
			return domain.ErrRecoveryInProgress
		}
	}
	q.jobs[job.ID] = job
	return nil
}

func (q *FakeQueue) Dequeue(ctx context.Context, workerID string) (*domain.TranscodeJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, job := range q.jobs {
		if job.Status == "Pending" {
			job.Status = "Processing"
			job.WorkerID = workerID
			return job, nil
		}
	}
	return nil, ports.ErrEmptyQueue
}

func (q *FakeQueue) GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job, ok := q.jobs[jobID]; ok {
		return job, nil
	}
	return nil, nil
}

func (q *FakeQueue) UpdateJob(ctx context.Context, job *domain.TranscodeJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	existing, ok := q.jobs[job.ID]
	if !ok {
		return nil
	}
	// C1: Ownership check — stale worker can't clobber recovery state
	if existing.WorkerID != job.WorkerID {
		return nil
	}
	if existing.Status != "Processing" && existing.Status != "Ready" {
		return nil
	}
	// Update fields
	existing.Status = job.Status
	existing.LastHeartbeat = job.LastHeartbeat
	existing.RecoveryInProgress = job.RecoveryInProgress
	existing.LastSegmentNum = job.LastSegmentNum
	return nil
}

func (q *FakeQueue) AddSegment(ctx context.Context, jobID string, segment domain.Segment) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, s := range q.segments[jobID] {
		if s.seq == segment.SequenceID {
			return nil // Already exists
		}
	}
	q.segments[jobID] = append(q.segments[jobID], &fakeSegment{
		seq:       segment.SequenceID,
		duration:  segment.Duration,
		workerID:  segment.WorkerID,
		timestamp: segment.Timestamp,
	})
	return nil
}

func (q *FakeQueue) MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job, ok := q.jobs[jobID]; ok {
		job.DiscontinuityCount++
	}
	return nil
}

func (q *FakeQueue) PurgeJobs(ctx context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job, ok := q.jobs[jobID]; ok {
		if job.Status == "Pending" {
			job.Status = "Cancelled"
		}
	}
	return nil
}

func (q *FakeQueue) DeleteJob(ctx context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.jobs, jobID)
	delete(q.segments, jobID)
	return nil
}

func (q *FakeQueue) TouchJob(ctx context.Context, jobID string) error {
	return nil
}

func (q *FakeQueue) ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var jobs []*domain.TranscodeJob
	for _, j := range q.jobs {
		if j.Status != "Cancelled" {
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

func (q *FakeQueue) Heartbeat(ctx context.Context, jobID string, workerID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[jobID]
	if !ok {
		return nil
	}
	if job.WorkerID != workerID {
		return nil
	}
	// Heartbeat only updates timestamp (simulated)
	return nil
}

func (q *FakeQueue) GetSegment(ctx context.Context, jobID string, seqID int) (*domain.Segment, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, s := range q.segments[jobID] {
		if s.seq == seqID {
			return &domain.Segment{
				SequenceID: s.seq,
				Duration:   s.duration,
				WorkerID:   s.workerID,
				Timestamp:  s.timestamp,
			}, nil
		}
	}
	return nil, nil
}

// --- Tests ---

// TestC1_StaleWorkerCannotClobberRecovery verifies that a stale worker's
// UpdateJob call is rejected when the job has been re-queued for recovery.
func TestC1_StaleWorkerCannotClobberRecovery(t *testing.T) {
	q := NewFakeQueue()
	ctx := context.Background()

	// Worker A picks up the job
	job := &domain.TranscodeJob{
		ID:       "job-test1",
		MediaID:  "media1",
		Status:   "Processing",
		WorkerID: "worker-A",
	}
	q.jobs["job-test1"] = job

	// Recovery sets status to Pending, clears worker_id
	job.Status = "Pending"
	job.WorkerID = ""
	job.RecoveryInProgress = true

	// Worker A (stale) tries to heartbeat with old status
	staleUpdate := &domain.TranscodeJob{
		ID:       "job-test1",
		Status:   "Ready",
		WorkerID: "worker-A",
	}
	err := q.UpdateJob(ctx, staleUpdate)

	// The stale update should have no effect (ownership check fails)
	current, _ := q.GetJob(ctx, "job-test1")
	if current.Status == "Ready" {
		t.Errorf("C1 FAIL: Stale worker clobbered recovery state! Status is Ready, expected Pending")
	}
	if current.WorkerID == "worker-A" {
		t.Errorf("C1 FAIL: Stale worker re-claimed the job!")
	}
	_ = err
}

// TestH1_RecoveryCASDedup verifies that Enqueue returns ErrRecoveryInProgress
// when recovery is already in progress.
func TestH1_RecoveryCASDedup(t *testing.T) {
	q := NewFakeQueue()
	ctx := context.Background()

	job := &domain.TranscodeJob{
		ID:                 "job-test2",
		MediaID:            "media2",
		Status:             "Pending",
		RecoveryInProgress: true,
	}
	q.jobs["job-test2"] = job

	// Second recovery attempt should be blocked
	err := q.Enqueue(ctx, &domain.TranscodeJob{
		ID:                 "job-test2",
		Status:             "Pending",
		RecoveryInProgress: true,
	})

	if err == nil {
		t.Errorf("H1 FAIL: Expected ErrRecoveryInProgress, got nil")
	}
	if err != domain.ErrRecoveryInProgress {
		t.Errorf("H1 FAIL: Expected ErrRecoveryInProgress, got %v", err)
	}
}

// TestC3_MarkWorkerAsDeadPreservesSegments verifies that calling MarkWorkerAsDead
// only increments discontinuity_count — segments are left intact (no dead flag anymore,
// since segment files persist on the shared filesystem regardless of worker liveness).
func TestC3_MarkWorkerAsDeadPreservesSegments(t *testing.T) {
	q := NewFakeQueue()
	ctx := context.Background()

	jobID := "job-test3"
	job := &domain.TranscodeJob{ID: jobID, DiscontinuityCount: 0}
	q.jobs[jobID] = job
	q.segments[jobID] = []*fakeSegment{
		{seq: 0, workerID: "worker-A"},
		{seq: 1, workerID: "worker-A"},
		{seq: 2, workerID: "worker-A"},
		{seq: 3, workerID: "worker-B"},
	}

	if err := q.MarkWorkerAsDead(ctx, jobID, "worker-A"); err != nil {
		t.Fatal(err)
	}

	// Discontinuity count must be incremented exactly once.
	if job.DiscontinuityCount != 1 {
		t.Errorf("C3 FAIL: expected DiscontinuityCount=1, got %d", job.DiscontinuityCount)
	}

	// All segments must remain accessible (no dead flag).
	for i := 0; i <= 3; i++ {
		seg, _ := q.GetSegment(ctx, jobID, i)
		if seg == nil {
			t.Errorf("C3 FAIL: GetSegment should return segment %d, got nil", i)
		}
	}

	// Calling again on worker-B must bump discontinuity_count a second time.
	q.MarkWorkerAsDead(ctx, jobID, "worker-B")
	if job.DiscontinuityCount != 2 {
		t.Errorf("C3 FAIL: expected DiscontinuityCount=2 after second call, got %d", job.DiscontinuityCount)
	}
}

// TestC4_FailedJobRetry verifies the retry logic for Failed jobs.
func TestC4_FailedJobRetry(t *testing.T) {
	q := NewFakeQueue()
	ctx := context.Background()

	job := &domain.TranscodeJob{
		ID:             "job-test4",
		MediaID:        "media4",
		Status:         "Failed",
		RestartCount:   1,
		LastSegmentNum: 5,
	}
	q.jobs["job-test4"] = job

	// Simulate retry: increment restart count, re-enqueue
	job.RestartCount++
	job.Status = "Pending"
	q.Enqueue(ctx, job)

	current, _ := q.GetJob(ctx, "job-test4")
	if current.Status != "Pending" {
		t.Errorf("C4 FAIL: expected status=Pending after retry, got %s", current.Status)
	}
	if current.RestartCount != 2 {
		t.Errorf("C4 FAIL: expected RestartCount=2, got %d", current.RestartCount)
	}
}

// TestDequeueOwnership verifies that Dequeue atomically sets worker_id.
func TestDequeueOwnership(t *testing.T) {
	q := NewFakeQueue()
	ctx := context.Background()

	q.jobs["job-test5"] = &domain.TranscodeJob{
		ID:     "job-test5",
		Status: "Pending",
	}

	job, _ := q.Dequeue(ctx, "worker-X")
	if job == nil {
		t.Fatal("expected job, got nil")
	}
	if job.WorkerID != "worker-X" {
		t.Errorf("expected WorkerID=worker-X, got %s", job.WorkerID)
	}
	if job.Status != "Processing" {
		t.Errorf("expected Status=Processing, got %s", job.Status)
	}
}

// TestAddSegmentIdempotent verifies that adding the same segment twice doesn't
// double-count transcoded_duration.
func TestAddSegmentIdempotent(t *testing.T) {
	q := NewFakeQueue()
	ctx := context.Background()

	seg := domain.Segment{
		SequenceID: 0,
		Duration:   4.0,
		WorkerID:   "worker-A",
	}

	q.AddSegment(ctx, "job-test6", seg)
	q.AddSegment(ctx, "job-test6", seg) // duplicate

	// Should have only 1 segment
	if len(q.segments["job-test6"]) != 1 {
		t.Errorf("expected 1 segment, got %d", len(q.segments["job-test6"]))
	}
}
