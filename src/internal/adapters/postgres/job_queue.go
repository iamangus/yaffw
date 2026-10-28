package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	_ "github.com/lib/pq"
	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type PostgresJobQueue struct {
	db *sql.DB
}

func NewConnection(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	// Tune connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

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
			status VARCHAR(50) NOT NULL,
			worker_id VARCHAR(255),
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
			discontinuity_count INT DEFAULT 0,
			failure_reason TEXT DEFAULT '',
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	// Migrations for existing tables
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ DEFAULT NOW();`)
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS discontinuity_count INT DEFAULT 0;`)
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS failure_reason TEXT DEFAULT '';`)
	// Phase 3: Range-parallel transcoding columns
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS parent_job_id VARCHAR(255) REFERENCES transcode_jobs(id) ON DELETE CASCADE;`)
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS range_index INT DEFAULT 0;`)
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS total_ranges INT DEFAULT 1;`)
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS range_start FLOAT DEFAULT 0;`)
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs ADD COLUMN IF NOT EXISTS range_end FLOAT DEFAULT 0;`)
	_, _ = q.db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_parent ON transcode_jobs(parent_job_id) WHERE parent_job_id IS NOT NULL;`)

	// Shared-FS refactor: drop columns that are no longer populated.
	_, _ = q.db.Exec(`ALTER TABLE transcode_jobs DROP COLUMN IF EXISTS worker_address;`)
	_, _ = q.db.Exec(`ALTER TABLE job_segments DROP COLUMN IF EXISTS worker_addr;`)
	_, _ = q.db.Exec(`ALTER TABLE job_segments DROP COLUMN IF EXISTS dead;`)

	// Create job_segments table
	_, err = q.db.Exec(`
		CREATE TABLE IF NOT EXISTS job_segments (
			job_id VARCHAR(255) REFERENCES transcode_jobs(id),
			sequence_id INT NOT NULL,
			duration FLOAT NOT NULL,
			worker_id VARCHAR(255) NOT NULL,
			timestamp FLOAT NOT NULL,
			range_index INT DEFAULT 0,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (job_id, sequence_id)
		);
	`)
	if err != nil {
		return err
	}

	// Migration: add columns to existing job_segments
	_, _ = q.db.Exec(`ALTER TABLE job_segments ADD COLUMN IF NOT EXISTS range_index INT DEFAULT 0;`)

	// media_streams table for probe caching
	_, err = q.db.Exec(`
		CREATE TABLE IF NOT EXISTS media_streams (
			media_id VARCHAR(255) PRIMARY KEY,
			video_codec VARCHAR(50),
			audio_codec VARCHAR(50),
			container VARCHAR(50),
			width INT,
			height INT,
			duration FLOAT,
			keyframe_times TEXT,
			probed_at TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	return nil
}

// jobColumns is the canonical column list for SELECT/RETURNING, kept in one place
// to avoid Scan misalignment across Dequeue/GetJob/ListActiveJobs.
const jobColumns = `id, media_id, file_path, status, worker_id, stream_url,
			target_video_codec, target_audio_codec, target_container, needs_transcode,
			last_heartbeat, restart_count, last_segment_num, transcoded_duration,
			start_segment, start_time, recovery_in_progress, last_recovery_time, last_accessed_at,
			discontinuity_count, failure_reason,
			parent_job_id, range_index, total_ranges, range_start, range_end`

// scanJob scans a row into a TranscodeJob using the canonical column order above.
func scanJob(scanner interface{ Scan(...interface{}) error }) (*domain.TranscodeJob, error) {
	job := &domain.TranscodeJob{}
	var workerID, streamUrl, failureReason, parentJobID sql.NullString
	var lastHeartbeat, lastRecoveryTime, lastAccessedAt sql.NullTime

	err := scanner.Scan(
		&job.ID, &job.MediaID, &job.FilePath, &job.Status, &workerID, &streamUrl,
		&job.TargetVideoCodec, &job.TargetAudioCodec, &job.TargetContainer, &job.NeedsTranscode,
		&lastHeartbeat, &job.RestartCount, &job.LastSegmentNum, &job.TranscodedDuration,
		&job.StartSegment, &job.StartTime, &job.RecoveryInProgress, &lastRecoveryTime, &lastAccessedAt,
		&job.DiscontinuityCount, &failureReason,
		&parentJobID, &job.RangeIndex, &job.TotalRanges, &job.RangeStart, &job.RangeEnd,
	)
	if err != nil {
		return nil, err
	}

	job.WorkerID = workerID.String
	job.StreamURL = streamUrl.String
	job.LastHeartbeat = lastHeartbeat.Time
	job.LastRecoveryTime = lastRecoveryTime.Time
	job.LastAccessedAt = lastAccessedAt.Time
	job.FailureReason = failureReason.String
	job.ParentJobID = parentJobID.String
	return job, nil
}

func (q *PostgresJobQueue) Enqueue(ctx context.Context, job *domain.TranscodeJob) error {
	query := `
		INSERT INTO transcode_jobs (
			id, media_id, file_path, status, target_video_codec, target_audio_codec,
			target_container, needs_transcode, restart_count, start_segment, start_time,
			recovery_in_progress, last_recovery_time, last_accessed_at, failure_reason,
			parent_job_id, range_index, total_ranges, range_start, range_end
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, NOW(), $14,
			$15, $16, $17, $18, $19
		)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			restart_count = EXCLUDED.restart_count,
			start_segment = EXCLUDED.start_segment,
			start_time = EXCLUDED.start_time,
			recovery_in_progress = EXCLUDED.recovery_in_progress,
			last_recovery_time = EXCLUDED.last_recovery_time,
			failure_reason = EXCLUDED.failure_reason,
			worker_id = CASE WHEN EXCLUDED.recovery_in_progress THEN NULL ELSE transcode_jobs.worker_id END,
			stream_url = CASE WHEN EXCLUDED.recovery_in_progress THEN NULL ELSE transcode_jobs.stream_url END,
			last_heartbeat = CASE WHEN EXCLUDED.recovery_in_progress THEN NULL ELSE transcode_jobs.last_heartbeat END,
			last_accessed_at = NOW()
		WHERE transcode_jobs.recovery_in_progress = false
		   OR transcode_jobs.last_recovery_time < NOW() - interval '5 seconds'
	`

	res, err := q.db.ExecContext(ctx, query,
		job.ID, job.MediaID, job.FilePath, job.Status, job.TargetVideoCodec, job.TargetAudioCodec,
		job.TargetContainer, job.NeedsTranscode, job.RestartCount, job.StartSegment, job.StartTime,
		job.RecoveryInProgress, job.LastRecoveryTime, job.FailureReason,
		nullableStr(job.ParentJobID), job.RangeIndex, job.TotalRanges, job.RangeStart, job.RangeEnd,
	)
	if err != nil {
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		log.Printf("[PostgresQueue] Enqueue skipped for job %s (recovery in progress)", job.ID)
		return domain.ErrRecoveryInProgress
	}

	log.Printf("[PostgresQueue] Enqueued job %s", job.ID)
	return nil
}

func (q *PostgresJobQueue) Dequeue(ctx context.Context, workerID string) (*domain.TranscodeJob, error) {
	query := `
		UPDATE transcode_jobs
		SET status = 'Processing', worker_id = $1, last_heartbeat = NOW()
		WHERE id = (
			SELECT id
			FROM transcode_jobs
			WHERE status = 'Pending'
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING ` + jobColumns

	row := q.db.QueryRowContext(ctx, query, workerID)
	job, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, ports.ErrEmptyQueue
	}
	if err != nil {
		return nil, err
	}

	// H4: Return segments so recovery workers know what's been produced
	segments, err := q.getSegments(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	job.Segments = segments

	log.Printf("[PostgresQueue] Dequeued job %s for worker %s", job.ID, workerID)
	return job, nil
}

func (q *PostgresJobQueue) GetJob(ctx context.Context, jobID string) (*domain.TranscodeJob, error) {
	query := `SELECT ` + jobColumns + ` FROM transcode_jobs WHERE id = $1`
	row := q.db.QueryRowContext(ctx, query, jobID)
	job, err := scanJob(row)
	if err != nil {
		return nil, err
	}

	segments, err := q.getSegments(ctx, jobID)
	if err != nil {
		return nil, err
	}
	job.Segments = segments

	return job, nil
}

func (q *PostgresJobQueue) getSegments(ctx context.Context, jobID string) ([]domain.Segment, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT sequence_id, duration, worker_id, timestamp FROM job_segments WHERE job_id = $1 ORDER BY sequence_id ASC",
		jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var segments []domain.Segment
	for rows.Next() {
		var s domain.Segment
		if err := rows.Scan(&s.SequenceID, &s.Duration, &s.WorkerID, &s.Timestamp); err != nil {
			return nil, err
		}
		segments = append(segments, s)
	}
	return segments, nil
}

func (q *PostgresJobQueue) UpdateJob(ctx context.Context, job *domain.TranscodeJob) error {
	query := `
		UPDATE transcode_jobs SET
			status = $2,
			worker_id = $3,
			stream_url = $4,
			last_heartbeat = $5,
			recovery_in_progress = $6,
			last_segment_num = GREATEST(last_segment_num, $7),
			failure_reason = $8
		WHERE id = $1
			AND worker_id = $3
			AND status IN ('Processing', 'Ready')
	`
	res, err := q.db.ExecContext(ctx, query,
		job.ID, job.Status, job.WorkerID, job.StreamURL,
		job.LastHeartbeat, job.RecoveryInProgress,
		job.LastSegmentNum, job.FailureReason,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("job not found or worker mismatch")
	}
	return nil
}

func (q *PostgresJobQueue) AddSegment(ctx context.Context, jobID string, segment domain.Segment) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO job_segments (job_id, sequence_id, duration, worker_id, timestamp, range_index)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (job_id, sequence_id) DO NOTHING
	`, jobID, segment.SequenceID, segment.Duration, segment.WorkerID, segment.Timestamp, 0)
	if err != nil {
		return err
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected > 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE transcode_jobs
			SET last_segment_num = GREATEST(last_segment_num, $2),
			    transcoded_duration = transcoded_duration + $3
			WHERE id = $1
		`, jobID, segment.SequenceID, segment.Duration)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE transcode_jobs
			SET last_segment_num = GREATEST(last_segment_num, $2)
			WHERE id = $1
		`, jobID, segment.SequenceID)
	}
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (q *PostgresJobQueue) MarkWorkerAsDead(ctx context.Context, jobID string, workerID string) error {
	_, err := q.db.ExecContext(ctx,
		"UPDATE transcode_jobs SET discontinuity_count = discontinuity_count + 1 WHERE id = $1",
		jobID)
	if err != nil {
		return err
	}
	log.Printf("[PostgresQueue] Worker %s marked dead for job %s (discontinuity_count incremented)", workerID, jobID)
	return nil
}

func (q *PostgresJobQueue) PurgeJobs(ctx context.Context, jobID string) error {
	// Cancel any Pending rows for this job ID.
	_, err := q.db.ExecContext(ctx,
		"UPDATE transcode_jobs SET status = 'Cancelled' WHERE id = $1 AND status = 'Pending'",
		jobID)
	if err == nil {
		log.Printf("[PostgresQueue] PurgeJobs: cancelled pending job %s", jobID)
	}
	return err
}

func (q *PostgresJobQueue) DeleteJob(ctx context.Context, jobID string) error {
	_, err := q.db.ExecContext(ctx, "DELETE FROM job_segments WHERE job_id = $1", jobID)
	if err != nil {
		return err
	}
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

// Cheap heartbeat — only updates last_heartbeat + ownership check. No full-row write, no segment fetch.
func (q *PostgresJobQueue) Heartbeat(ctx context.Context, jobID string, workerID string) error {
	res, err := q.db.ExecContext(ctx,
		"UPDATE transcode_jobs SET last_heartbeat = NOW() WHERE id = $1 AND worker_id = $2 AND status IN ('Processing','Ready')",
		jobID, workerID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return errors.New("job not found or worker mismatch")
	}
	return nil
}

// Single-row segment lookup — avoids fetching all segments on every .ts request.
func (q *PostgresJobQueue) GetSegment(ctx context.Context, jobID string, seqID int) (*domain.Segment, error) {
	var s domain.Segment
	err := q.db.QueryRowContext(ctx,
		"SELECT sequence_id, duration, worker_id, timestamp FROM job_segments WHERE job_id = $1 AND sequence_id = $2",
		jobID, seqID,
	).Scan(&s.SequenceID, &s.Duration, &s.WorkerID, &s.Timestamp)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (q *PostgresJobQueue) ListActiveJobs(ctx context.Context) ([]*domain.TranscodeJob, error) {
	query := `SELECT ` + jobColumns + ` FROM transcode_jobs WHERE status IN ('Processing', 'Ready', 'Pending', 'Completed', 'Failed')`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*domain.TranscodeJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// --- Phase 3: Sub-job helpers ---

// EnqueueSubJob inserts a range sub-job linked to a parent.
func (q *PostgresJobQueue) EnqueueSubJob(ctx context.Context, job *domain.TranscodeJob) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO transcode_jobs (
			id, media_id, file_path, status, target_video_codec, target_audio_codec,
			target_container, needs_transcode, start_segment, start_time,
			parent_job_id, range_index, total_ranges, range_start, range_end
		) VALUES (
			$1, $2, $3, 'Pending', $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13, $14
		)
	`, job.ID, job.MediaID, job.FilePath, job.TargetVideoCodec, job.TargetAudioCodec,
		job.TargetContainer, job.NeedsTranscode, job.StartSegment, job.StartTime,
		nullableStr(job.ParentJobID), job.RangeIndex, job.TotalRanges, job.RangeStart, job.RangeEnd,
	)
	return err
}

// GetSubJobs returns all sub-jobs for a parent job.
func (q *PostgresJobQueue) GetSubJobs(ctx context.Context, parentJobID string) ([]*domain.TranscodeJob, error) {
	query := `SELECT ` + jobColumns + ` FROM transcode_jobs WHERE parent_job_id = $1 ORDER BY range_index ASC`
	rows, err := q.db.QueryContext(ctx, query, parentJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*domain.TranscodeJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// IsParentJobCompleted returns true if all sub-jobs of the parent are Completed.
func (q *PostgresJobQueue) IsParentJobCompleted(ctx context.Context, parentJobID string, totalRanges int) (bool, error) {
	var count int
	err := q.db.QueryRowContext(ctx,
		"SELECT count(*) FROM transcode_jobs WHERE parent_job_id = $1 AND status = 'Completed'",
		parentJobID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count >= totalRanges, nil
}

// MarkParentCompleted sets the parent job status to Completed.
func (q *PostgresJobQueue) MarkParentCompleted(ctx context.Context, parentJobID string) error {
	_, err := q.db.ExecContext(ctx,
		"UPDATE transcode_jobs SET status = 'Completed' WHERE id = $1",
		parentJobID)
	return err
}

// --- M6: Probe cache ---

// CacheProbe stores or updates probe results for a media item.
func (q *PostgresJobQueue) CacheProbe(ctx context.Context, mediaID string, probe *domain.MediaProbe) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO media_streams (media_id, video_codec, audio_codec, container, width, height, probed_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (media_id) DO UPDATE SET
			video_codec = EXCLUDED.video_codec,
			audio_codec = EXCLUDED.audio_codec,
			container = EXCLUDED.container,
			width = EXCLUDED.width,
			height = EXCLUDED.height,
			probed_at = NOW()
	`, mediaID, probe.VideoCodec, probe.AudioCodec, probe.Container, probe.Width, probe.Height)
	return err
}

// GetCachedProbe retrieves cached probe results.
func (q *PostgresJobQueue) GetCachedProbe(ctx context.Context, mediaID string) (*domain.MediaProbe, error) {
	var p domain.MediaProbe
	err := q.db.QueryRowContext(ctx,
		"SELECT video_codec, audio_codec, container, width, height FROM media_streams WHERE media_id = $1",
		mediaID,
	).Scan(&p.VideoCodec, &p.AudioCodec, &p.Container, &p.Width, &p.Height)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CacheKeyframes stores keyframe PTS times for a media item (Phase 3).
func (q *PostgresJobQueue) CacheKeyframes(ctx context.Context, mediaID string, keyframeTimes string) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO media_streams (media_id, keyframe_times, probed_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (media_id) DO UPDATE SET
			keyframe_times = EXCLUDED.keyframe_times,
			probed_at = NOW()
	`, mediaID, keyframeTimes)
	return err
}

// GetCachedKeyframes retrieves cached keyframe times.
func (q *PostgresJobQueue) GetCachedKeyframes(ctx context.Context, mediaID string) (string, error) {
	var kf string
	err := q.db.QueryRowContext(ctx,
		"SELECT keyframe_times FROM media_streams WHERE media_id = $1 AND keyframe_times IS NOT NULL",
		mediaID,
	).Scan(&kf)
	if err != nil {
		return "", err
	}
	return kf, nil
}

// nullableStr returns sql.NullString-compatible nil for empty strings (for nullable columns).
func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
