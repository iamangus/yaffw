package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log"

	_ "github.com/lib/pq"
	"github.com/yaffw/yaffw/src/internal/domain"
)

type PostgresJobQueue struct {
	db *sql.DB
}

func NewConnection(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

func NewJobQueue(db *sql.DB) *PostgresJobQueue {
	return &PostgresJobQueue{db: db}
}

func (q *PostgresJobQueue) InitSchema() error {
	// Create transcode_jobs table
	_, err := q.db.Exec(`
		CREATE TABLE IF NOT EXISTS transcode_jobs (
			id VARCHAR(255) PRIMARY KEY,
			media_id VARCHAR(255) NOT NULL,
			file_path TEXT NOT NULL,
			status VARCHAR(50) NOT NULL, -- Pending, Processing, Ready, Failed
			worker_id VARCHAR(255),
			worker_address VARCHAR(255),
			stream_url TEXT,
			target_video_codec VARCHAR(50),
			target_audio_codec VARCHAR(50),
			target_container VARCHAR(50),
			needs_transcode BOOLEAN,
			last_heartbeat TIMESTAMPTZ,
			restart_count INT DEFAULT 0,
			last_segment_num INT DEFAULT 0,
			transcoded_duration FLOAT DEFAULT 0,
			start_segment INT DEFAULT 0,
			start_time FLOAT DEFAULT 0,
			recovery_in_progress BOOLEAN DEFAULT FALSE,
			last_recovery_time TIMESTAMPTZ,
			last_accessed_at TIMESTAMPTZ DEFAULT NOW(),
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	// Migration: Add last_accessed_at if it doesn't exist
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ DEFAULT NOW();`)

	// Create job_segments table
	_, err = q.db.Exec(`
		CREATE TABLE IF NOT EXISTS job_segments (
			job_id VARCHAR(255) REFERENCES transcode_jobs(id),
			sequence_id INT NOT NULL,
			duration FLOAT NOT NULL,
			worker_id VARCHAR(255) NOT NULL,
			worker_addr VARCHAR(255) NOT NULL,
			timestamp FLOAT NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (job_id, sequence_id)
		);
	`)
	return err
}

func (q *PostgresJobQueue) Enqueue(ctx context.Context, job *domain.TranscodeJob) error {
	// UPSERT (ON CONFLICT DO UPDATE) to handle re-queuing existing jobs
	query := `
		INSERT INTO transcode_jobs (
			id, media_id, file_path, status, target_video_codec, target_audio_codec, 
			target_container, needs_transcode, restart_count, start_segment, start_time,
			recovery_in_progress, last_recovery_time, last_accessed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, 
			$7, $8, $9, $10, $11,
			$12, $13, NOW()
		)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			restart_count = EXCLUDED.restart_count,
			start_segment = EXCLUDED.start_segment,
			start_time = EXCLUDED.start_time,
			recovery_in_progress = EXCLUDED.recovery_in_progress,
			last_recovery_time = EXCLUDED.last_recovery_time,
			last_accessed_at = NOW();
	`

	_, err := q.db.ExecContext(ctx, query,
		job.ID, job.MediaID, job.FilePath, job.Status, job.TargetVideoCodec, job.TargetAudioCodec,
		job.TargetContainer, job.NeedsTranscode, job.RestartCount, job.StartSegment, job.StartTime,
		job.RecoveryInProgress, job.LastRecoveryTime,
	)

	if err == nil {
		log.Printf("[PostgresQueue] Enqueued job %s", job.ID)
	}
	return err
}

func (q *PostgresJobQueue) Dequeue(ctx context.Context) (*domain.TranscodeJob, error) {
	// SKIP LOCKED magic
	query := `
		UPDATE transcode_jobs
		SET status = 'Processing', last_heartbeat = NOW()
		WHERE id = (
			SELECT id
			FROM transcode_jobs
			WHERE status = 'Pending'
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING 
			id, media_id, file_path, status, worker_id, worker_address, stream_url,
			target_video_codec, target_audio_codec, target_container, needs_transcode,
			last_heartbeat, restart_count, last_segment_num, transcoded_duration,
			start_segment, start_time, recovery_in_progress, last_recovery_time, last_accessed_at;
	`

	row := q.db.QueryRowContext(ctx, query)

	job := &domain.TranscodeJob{}
	var workerID, workerAddr, streamUrl sql.NullString
	var lastHeartbeat, lastRecoveryTime, lastAccessedAt sql.NullTime

	err := row.Scan(
		&job.ID, &job.MediaID, &job.FilePath, &job.Status, &workerID, &workerAddr, &streamUrl,
		&job.TargetVideoCodec, &job.TargetAudioCodec, &job.TargetContainer, &job.NeedsTranscode,
		&lastHeartbeat, &job.RestartCount, &job.LastSegmentNum, &job.TranscodedDuration,
		&job.StartSegment, &job.StartTime, &job.RecoveryInProgress, &lastRecoveryTime, &lastAccessedAt,
	)

	if err == sql.ErrNoRows {
		return nil, errors.New("empty queue")
	}
	if err != nil {
		return nil, err
	}

	job.WorkerID = workerID.String
	job.WorkerAddress = workerAddr.String
	job.StreamURL = streamUrl.String
	job.LastHeartbeat = lastHeartbeat.Time
	job.LastRecoveryTime = lastRecoveryTime.Time
	job.LastAccessedAt = lastAccessedAt.Time

	log.Printf("[PostgresQueue] Dequeued job %s", job.ID)
	return job, nil
}

func (q *PostgresJobQueue) GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error) {
	query := `
		SELECT 
			id, media_id, file_path, status, worker_id, worker_address, stream_url,
			target_video_codec, target_audio_codec, target_container, needs_transcode,
			last_heartbeat, restart_count, last_segment_num, transcoded_duration,
			start_segment, start_time, recovery_in_progress, last_recovery_time, last_accessed_at
		FROM transcode_jobs WHERE id = $1
	`
	row := q.db.QueryRowContext(ctx, query, jobID)

	job := &domain.TranscodeJob{}
	var workerID, workerAddr, streamUrl sql.NullString
	var lastHeartbeat, lastRecoveryTime, lastAccessedAt sql.NullTime

	err := row.Scan(
		&job.ID, &job.MediaID, &job.FilePath, &job.Status, &workerID, &workerAddr, &streamUrl,
		&job.TargetVideoCodec, &job.TargetAudioCodec, &job.TargetContainer, &job.NeedsTranscode,
		&lastHeartbeat, &job.RestartCount, &job.LastSegmentNum, &job.TranscodedDuration,
		&job.StartSegment, &job.StartTime, &job.RecoveryInProgress, &lastRecoveryTime, &lastAccessedAt,
	)
	if err != nil {
		return nil, err
	}

	job.WorkerID = workerID.String
	job.WorkerAddress = workerAddr.String
	job.StreamURL = streamUrl.String
	job.LastHeartbeat = lastHeartbeat.Time
	job.LastRecoveryTime = lastRecoveryTime.Time
	job.LastAccessedAt = lastAccessedAt.Time

	// Fetch segments
	segments, err := q.getSegments(ctx, jobID)
	if err != nil {
		return nil, err
	}
	job.Segments = segments

	return job, nil
}

func (q *PostgresJobQueue) getSegments(ctx context.Context, jobID string) ([]domain.Segment, error) {
	rows, err := q.db.QueryContext(ctx, "SELECT sequence_id, duration, worker_id, worker_addr, timestamp FROM job_segments WHERE job_id = $1 ORDER BY sequence_id ASC", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var segments []domain.Segment
	for rows.Next() {
		var s domain.Segment
		if err := rows.Scan(&s.SequenceID, &s.Duration, &s.WorkerID, &s.WorkerAddr, &s.Timestamp); err != nil {
			return nil, err
		}
		segments = append(segments, s)
	}
	return segments, nil
}

func (q *PostgresJobQueue) UpdateJob(ctx context.Context, job *domain.TranscodeJob) error {
	// We handle the "Max Value" logic for TranscodedDuration and LastSegmentNum here in SQL
	// to prevent race conditions from staler heartbeats, matching the memory fix.
	query := `
		UPDATE transcode_jobs SET
			status = $2,
			worker_id = $3,
			worker_address = $4,
			stream_url = $5,
			last_heartbeat = $6,
			recovery_in_progress = $7,
			last_segment_num = GREATEST(last_segment_num, $8),
			transcoded_duration = GREATEST(transcoded_duration, $9)
		WHERE id = $1
	`
	res, err := q.db.ExecContext(ctx, query,
		job.ID, job.Status, job.WorkerID, job.WorkerAddress, job.StreamURL,
		job.LastHeartbeat, job.RecoveryInProgress,
		job.LastSegmentNum, job.TranscodedDuration,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("job not found")
	}
	return nil
}

func (q *PostgresJobQueue) AddSegment(ctx context.Context, jobID string, segment domain.Segment) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert segment (ignore duplicates)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO job_segments (job_id, sequence_id, duration, worker_id, worker_addr, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (job_id, sequence_id) DO NOTHING
	`, jobID, segment.SequenceID, segment.Duration, segment.WorkerID, segment.WorkerAddr, segment.Timestamp)
	if err != nil {
		return err
	}

	// Update job aggregate stats
	_, err = tx.ExecContext(ctx, `
		UPDATE transcode_jobs 
		SET last_segment_num = GREATEST(last_segment_num, $2),
		    transcoded_duration = transcoded_duration + $3
		WHERE id = $1
	`, jobID, segment.SequenceID, segment.Duration)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (q *PostgresJobQueue) MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) (int, error) {
	// Delete segments from this worker
	res, err := q.db.ExecContext(ctx, "DELETE FROM job_segments WHERE job_id = $1 AND worker_id = $2", jobID, workerID)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()
	log.Printf("[PostgresQueue] Deleted %d segments from dead worker %s for job %s", deleted, workerID, jobID)

	// Get the new last segment number
	var lastSeg sql.NullInt32
	err = q.db.QueryRowContext(ctx, "SELECT MAX(sequence_id) FROM job_segments WHERE job_id = $1", jobID).Scan(&lastSeg)
	if err != nil {
		return 0, err
	}

	// Reset the job's last_segment_num to match reality
	// Also re-calculate duration (optional but good for consistency)
	newLastSeg := int(lastSeg.Int32) // 0 if null

	_, err = q.db.ExecContext(ctx, `
		UPDATE transcode_jobs 
		SET last_segment_num = $2
		WHERE id = $1
	`, jobID, newLastSeg)

	return newLastSeg, err
}

func (q *PostgresJobQueue) PurgeJobs(ctx context.Context, jobID string) error {
	// In memory impl this removed pending jobs from the queue array.
	// In SQL, pending jobs are just rows with status='Pending'.
	// Since ID is unique, PurgeJobs(jobID) for a specific ID implies removing/canceling that specific job?
	// The interface comment says "Purge any stale pending tasks for this job".
	// Since we use UPSERT in Enqueue, there's only ever one row per JobID.
	// If the intent is to reset a Pending job that shouldn't be there, we can just Delete it?
	// But Wait! In `memory/job_queue.go`:
	/*
		func (q *InMemoryJobQueue) PurgeJobs(ctx context.Context, jobID string) error {
			// ... filters out jobs with ID == jobID from q.queue ...
		}
	*/
	// This implies there could be multiple entries in the 'queue' list in memory for the same JobID?
	// Ah, the memory queue is a slice of pointers. Enqueue appends.
	// In Postgres, we maintain a single row per JobID.
	// So "Purging" a pending job just means ensure it's not Pending anymore if we want to cancel it?
	// OR, if we assume the caller calls Purge before Enqueueing a *new* recovery task,
	// checking `triggerJITRecovery` in main.go:
	// `queue.PurgeJobs(ctx, job.ID)` is called before `job.Status = "Pending"; queue.Enqueue(...)`.
	// Since we use UPSERT in Enqueue, we effectively overwrite the old state anyway.
	// So this might be a no-op for Postgres if we enforce uniqueness on JobID.

	// However, if we want to be safe and match semantics:
	// If the job is currently Pending, we could set it to Failed or something?
	// But `triggerJITRecovery` immediately sets it to Pending again.
	// So, we don't really need to do anything complex here given our UPSERT logic.
	// Just logging for now.
	log.Printf("[PostgresQueue] PurgeJobs called for %s (Handled via UPSERT logic in Enqueue)", jobID)
	return nil
}

func (q *PostgresJobQueue) DeleteJob(ctx context.Context, jobID string) error {
	// Delete segments first (FK)
	_, err := q.db.ExecContext(ctx, "DELETE FROM job_segments WHERE job_id = $1", jobID)
	if err != nil {
		return err
	}
	// Delete job
	_, err = q.db.ExecContext(ctx, "DELETE FROM transcode_jobs WHERE id = $1", jobID)
	if err == nil {
		log.Printf("[PostgresQueue] Deleted job %s and its segments", jobID)
	}
	return err
}

func (q *PostgresJobQueue) TouchJob(ctx context.Context, jobID string) error {
	_, err := q.db.ExecContext(ctx, "UPDATE transcode_jobs SET last_accessed_at = NOW() WHERE id = $1", jobID)
	return err
}

func (q *PostgresJobQueue) ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error) {
	query := `
		SELECT 
			id, media_id, file_path, status, worker_id, worker_address, stream_url,
			target_video_codec, target_audio_codec, target_container, needs_transcode,
			last_heartbeat, restart_count, last_segment_num, transcoded_duration,
			start_segment, start_time, recovery_in_progress, last_recovery_time, last_accessed_at
		FROM transcode_jobs 
		WHERE status IN ('Processing', 'Ready', 'Pending', 'Completed')
	`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*domain.TranscodeJob
	for rows.Next() {
		job := &domain.TranscodeJob{}
		var workerID, workerAddr, streamUrl sql.NullString
		var lastHeartbeat, lastRecoveryTime, lastAccessedAt sql.NullTime

		err := rows.Scan(
			&job.ID, &job.MediaID, &job.FilePath, &job.Status, &workerID, &workerAddr, &streamUrl,
			&job.TargetVideoCodec, &job.TargetAudioCodec, &job.TargetContainer, &job.NeedsTranscode,
			&lastHeartbeat, &job.RestartCount, &job.LastSegmentNum, &job.TranscodedDuration,
			&job.StartSegment, &job.StartTime, &job.RecoveryInProgress, &lastRecoveryTime, &lastAccessedAt,
		)
		if err != nil {
			return nil, err
		}

		job.WorkerID = workerID.String
		job.WorkerAddress = workerAddr.String
		job.StreamURL = streamUrl.String
		job.LastHeartbeat = lastHeartbeat.Time
		job.LastRecoveryTime = lastRecoveryTime.Time
		job.LastAccessedAt = lastAccessedAt.Time

		// NOTE: We generally don't need full segments list for the monitor check,
		// but if we did, we'd fetch them here.
		// For `startPassiveRecoveryMonitor`, we mostly need LastHeartbeat and WorkerID.
		jobs = append(jobs, job)
	}
	return jobs, nil
}
