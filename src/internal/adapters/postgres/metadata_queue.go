package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type PostgresMetadataQueue struct {
	db *sql.DB
}

func NewMetadataQueue(db *sql.DB) *PostgresMetadataQueue {
	return &PostgresMetadataQueue{db: db}
}

func (q *PostgresMetadataQueue) InitSchema() error {
	_, err := q.db.Exec(`
		CREATE TABLE IF NOT EXISTS metadata_jobs (
			id VARCHAR(255) PRIMARY KEY,
			media_id VARCHAR(255) NOT NULL,
			status VARCHAR(50) NOT NULL, -- Pending, Processing, Completed, Failed
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW(),
			fail_reason TEXT
		);
	`)
	return err
}

func (q *PostgresMetadataQueue) Enqueue(ctx context.Context, job *domain.MetadataJob) error {
	query := `
		INSERT INTO metadata_jobs (id, media_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			updated_at = NOW();
	`
	// If CreatedAt is zero, use Now
	createdAt := job.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	_, err := q.db.ExecContext(ctx, query, job.ID, job.MediaID, job.Status, createdAt)
	if err == nil {
		log.Printf("[MetadataQueue] Enqueued job %s", job.ID)
	}
	return err
}

func (q *PostgresMetadataQueue) Dequeue(ctx context.Context) (*domain.MetadataJob, error) {
	query := `
		UPDATE metadata_jobs
		SET status = 'Processing', updated_at = NOW()
		WHERE id = (
			SELECT id
			FROM metadata_jobs
			WHERE status = 'Pending'
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, media_id, status, created_at;
	`

	row := q.db.QueryRowContext(ctx, query)
	var job domain.MetadataJob
	err := row.Scan(&job.ID, &job.MediaID, &job.Status, &job.CreatedAt)

	if err == sql.ErrNoRows {
		return nil, errors.New("empty queue")
	}
	if err != nil {
		return nil, err
	}

	return &job, nil
}

func (q *PostgresMetadataQueue) MarkCompleted(ctx context.Context, jobID string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE metadata_jobs 
		SET status = 'Completed', updated_at = NOW() 
		WHERE id = $1
	`, jobID)
	return err
}

func (q *PostgresMetadataQueue) MarkFailed(ctx context.Context, jobID string, reason string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE metadata_jobs 
		SET status = 'Failed', updated_at = NOW(), fail_reason = $2
		WHERE id = $1
	`, jobID, reason)
	return err
}
