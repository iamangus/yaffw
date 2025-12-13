package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq"
	"github.com/yaffw/yaffw/src/internal/domain"
)

type PostgresMediaRepo struct {
	db *sql.DB
}

func NewMediaRepo(db *sql.DB) *PostgresMediaRepo {
	return &PostgresMediaRepo{db: db}
}

func (r *PostgresMediaRepo) InitSchema() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS media_items (
			id VARCHAR(255) PRIMARY KEY,
			path TEXT NOT NULL,
			title TEXT NOT NULL,
			type VARCHAR(50),
			poster_url TEXT,
			duration BIGINT,
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	return err
}

func (r *PostgresMediaRepo) Save(ctx context.Context, item *domain.MediaItem) error {
	query := `
		INSERT INTO media_items (id, path, title, type, poster_url, duration, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			path = EXCLUDED.path,
			title = EXCLUDED.title,
			type = EXCLUDED.type,
			poster_url = EXCLUDED.poster_url,
			duration = EXCLUDED.duration;
	`
	
	_, err := r.db.ExecContext(ctx, query,
		item.ID,
		item.Path,
		item.Title,
		string(item.Type),
		item.PosterURL,
		int64(item.Duration),
		item.CreatedAt,
	)
	return err
}

func (r *PostgresMediaRepo) GetByID(ctx context.Context, id string) (*domain.MediaItem, error) {
	query := `
		SELECT id, path, title, type, poster_url, duration, created_at
		FROM media_items
		WHERE id = $1
	`
	
	row := r.db.QueryRowContext(ctx, query, id)
	
	var item domain.MediaItem
	var typeStr string
	var durationInt int64
	
	err := row.Scan(
		&item.ID,
		&item.Path,
		&item.Title,
		&typeStr,
		&item.PosterURL,
		&durationInt,
		&item.CreatedAt,
	)
	
	if err == sql.ErrNoRows {
		return nil, errors.New("item not found")
	}
	if err != nil {
		return nil, err
	}
	
	item.Type = domain.MediaType(typeStr)
	item.Duration = time.Duration(durationInt)
	
	return &item, nil
}

func (r *PostgresMediaRepo) ListAll(ctx context.Context) ([]domain.MediaItem, error) {
	query := `
		SELECT id, path, title, type, poster_url, duration, created_at
		FROM media_items
		ORDER BY title ASC
	`
	
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var items []domain.MediaItem
	for rows.Next() {
		var item domain.MediaItem
		var typeStr string
		var durationInt int64
		
		err := rows.Scan(
			&item.ID,
			&item.Path,
			&item.Title,
			&typeStr,
			&item.PosterURL,
			&durationInt,
			&item.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		
		item.Type = domain.MediaType(typeStr)
		item.Duration = time.Duration(durationInt)
		items = append(items, item)
	}
	
	return items, nil
}
