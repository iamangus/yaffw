package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type PostgresHistoryRepo struct {
	db *sql.DB
}

func NewHistoryRepo(db *sql.DB) *PostgresHistoryRepo {
	return &PostgresHistoryRepo{db: db}
}

func (r *PostgresHistoryRepo) InitSchema() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS watch_history (
			user_id TEXT NOT NULL,
			media_id TEXT NOT NULL,
			position DOUBLE PRECISION DEFAULT 0,
			finished BOOLEAN DEFAULT FALSE,
			updated_at TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (user_id, media_id)
		);
	`)
	return err
}

func (r *PostgresHistoryRepo) GetProgress(ctx context.Context, userID, mediaID string) (*domain.WatchProgress, error) {
	query := `
		SELECT user_id, media_id, position, finished, updated_at
		FROM watch_history
		WHERE user_id = $1 AND media_id = $2
	`
	row := r.db.QueryRowContext(ctx, query, userID, mediaID)

	var p domain.WatchProgress
	err := row.Scan(&p.UserID, &p.MediaID, &p.Position, &p.Finished, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil // Return nil if no progress found, not error
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PostgresHistoryRepo) SaveProgress(ctx context.Context, p *domain.WatchProgress) error {
	query := `
		INSERT INTO watch_history (user_id, media_id, position, finished, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, media_id) DO UPDATE SET
			position = EXCLUDED.position,
			finished = EXCLUDED.finished,
			updated_at = EXCLUDED.updated_at;
	`
	_, err := r.db.ExecContext(ctx, query, p.UserID, p.MediaID, p.Position, p.Finished, p.UpdatedAt)
	return err
}

func (r *PostgresHistoryRepo) ListContinueWatching(ctx context.Context, userID string) ([]domain.MediaItem, error) {
	// Join media_items and watch_history
	// Only return items that are NOT finished and have > 0 progress
	query := `
		SELECT m.id, m.path, m.title, m.type, m.poster_url, m.duration, m.created_at,
		       w.position, w.finished, w.updated_at
		FROM media_items m
		JOIN watch_history w ON m.id = w.media_id
		WHERE w.user_id = $1 AND w.finished = FALSE AND w.position > 0
		ORDER BY w.updated_at DESC
		LIMIT 20
	`
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.MediaItem
	for rows.Next() {
		var item domain.MediaItem
		var typeStr string
		var durationInt int64
		var p domain.WatchProgress

		err := rows.Scan(
			&item.ID, &item.Path, &item.Title, &typeStr, &item.PosterURL, &durationInt, &item.CreatedAt,
			&p.Position, &p.Finished, &p.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		item.Type = domain.MediaType(typeStr)
		item.Duration = time.Duration(durationInt)
		p.UserID = userID
		p.MediaID = item.ID
		item.ViewProgress = &p

		items = append(items, item)
	}
	return items, nil
}
