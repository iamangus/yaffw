package postgres

import (
	"context"
	"database/sql"
	"log"
	"os"
	"time"
)

type PostgresLockManager struct {
	db       *sql.DB
	workerID string
}

func NewLockManager(db *sql.DB) *PostgresLockManager {
	// Generate a unique ID for this pod (e.g. Hostname)
	workerID, err := os.Hostname()
	if err != nil {
		workerID = "unknown-" + time.Now().String()
	}
	return &PostgresLockManager{
		db:       db,
		workerID: workerID,
	}
}

func (l *PostgresLockManager) InitSchema() error {
	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS locks (
			key VARCHAR(255) PRIMARY KEY,
			holder_id VARCHAR(255) NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		);
	`)
	return err
}

func (l *PostgresLockManager) TryAcquireLock(ctx context.Context, key string, ttlSeconds int) (bool, error) {
	// 1. Delete expired locks (Self-cleaning)
	// We can do this opportunistically or via a background job. Doing it here is simple.
	_, err := l.db.ExecContext(ctx, "DELETE FROM locks WHERE key = $1 AND expires_at < NOW()", key)
	if err != nil {
		return false, err
	}

	// 2. Try to insert
	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	_, err = l.db.ExecContext(ctx, `
		INSERT INTO locks (key, holder_id, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET
			holder_id = EXCLUDED.holder_id,
			expires_at = EXCLUDED.expires_at
		WHERE locks.holder_id = $2; -- Only allow extending own lock (Reentrancy/Heartbeat)
	`, key, l.workerID, expiresAt)

	if err != nil {
		// If duplicate key error (and not own lock), we failed to acquire
		// BUT 'ON CONFLICT' usually suppresses the error unless the WHERE clause fails to match.
		// If another holder has the lock, ON CONFLICT runs, but WHERE fails, so nothing updates.
		// We need to check RowsAffected to be sure.
		return false, nil
	}

	// Check if we actually inserted or updated a row
	// Wait, standard SQL INSERT ON CONFLICT doesn't return an error if it does nothing.
	// But ExecContext result does return RowsAffected.
	// However, a simpler way is:
	// "INSERT ... ON CONFLICT DO NOTHING"
	// If RowsAffected == 1, we got it.
	// BUT we also want to support heartbeats (extending own lock).

	// Let's retry with a cleaner query logic:

	// A. Try to acquire (Insert if not exists)
	res, err := l.db.ExecContext(ctx, `
		INSERT INTO locks (key, holder_id, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING
	`, key, l.workerID, expiresAt)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return true, nil // Acquired new lock
	}

	// B. If failed, check if WE hold it and extend
	res, err = l.db.ExecContext(ctx, `
		UPDATE locks SET expires_at = $3
		WHERE key = $1 AND holder_id = $2
	`, key, l.workerID, expiresAt)
	if err != nil {
		return false, err
	}
	rows, _ = res.RowsAffected()
	return rows > 0, nil
}

func (l *PostgresLockManager) ReleaseLock(ctx context.Context, key string) error {
	_, err := l.db.ExecContext(ctx, `
		DELETE FROM locks WHERE key = $1 AND holder_id = $2
	`, key, l.workerID)

	if err == nil {
		log.Printf("[LockManager] Released lock %s", key)
	}
	return err
}
