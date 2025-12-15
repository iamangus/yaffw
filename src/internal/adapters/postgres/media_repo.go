package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// 1. Create base table
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS media_items (
			id VARCHAR(255) PRIMARY KEY,
			path TEXT NOT NULL,
			title TEXT NOT NULL,
			type VARCHAR(50),
			poster_url TEXT,
			duration BIGINT,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			metadata_status VARCHAR(50) DEFAULT 'New',
			metadata JSONB
		);
	`)
	if err != nil {
		return err
	}

	// 2. Migration: Add columns if they don't exist (idempotent)
	// We can ignore errors here if columns exist, or use IF NOT EXISTS if PG version supports it (PG 9.6+ doesn't for columns easily)
	// Simple way: Try adding, ignore error.
	r.db.Exec(`ALTER TABLE media_items ADD COLUMN IF NOT EXISTS metadata_status VARCHAR(50) DEFAULT 'New';`)
	r.db.Exec(`ALTER TABLE media_items ADD COLUMN IF NOT EXISTS metadata JSONB;`)

	return nil
}

func (r *PostgresMediaRepo) Save(ctx context.Context, item *domain.MediaItem) error {
	metadataJSON, err := json.Marshal(item.Metadata)
	if err != nil {
		return err
	}
	// If Metadata is nil, we store NULL or empty JSON? Let's store NULL.
	var metadataVal interface{} = metadataJSON
	if item.Metadata == nil {
		metadataVal = nil
	}

	query := `
		INSERT INTO media_items (id, path, title, type, poster_url, duration, created_at, metadata_status, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			path = EXCLUDED.path,
			title = EXCLUDED.title,
			type = EXCLUDED.type,
			poster_url = EXCLUDED.poster_url,
			duration = EXCLUDED.duration,
			metadata_status = EXCLUDED.metadata_status,
			metadata = EXCLUDED.metadata;
	`

	_, err = r.db.ExecContext(ctx, query,
		item.ID,
		item.Path,
		item.Title,
		string(item.Type),
		item.PosterURL,
		int64(item.Duration),
		item.CreatedAt,
		item.MetadataStatus,
		metadataVal,
	)
	return err
}

func (r *PostgresMediaRepo) GetByID(ctx context.Context, id string) (*domain.MediaItem, error) {
	query := `
		SELECT id, path, title, type, poster_url, duration, created_at, metadata_status, metadata
		FROM media_items
		WHERE id = $1
	`

	row := r.db.QueryRowContext(ctx, query, id)

	var item domain.MediaItem
	var typeStr string
	var durationInt int64
	var statusStr sql.NullString
	var metadataJSON []byte

	err := row.Scan(
		&item.ID,
		&item.Path,
		&item.Title,
		&typeStr,
		&item.PosterURL,
		&durationInt,
		&item.CreatedAt,
		&statusStr,
		&metadataJSON,
	)

	if err == sql.ErrNoRows {
		return nil, errors.New("item not found")
	}
	if err != nil {
		return nil, err
	}

	item.Type = domain.MediaType(typeStr)
	item.Duration = time.Duration(durationInt)

	if statusStr.Valid {
		item.MetadataStatus = domain.MetadataStatus(statusStr.String)
	} else {
		item.MetadataStatus = domain.MetadataStatusNew
	}

	if len(metadataJSON) > 0 {
		var md domain.MediaMetadata
		if err := json.Unmarshal(metadataJSON, &md); err == nil {
			item.Metadata = &md
		}
	}

	return &item, nil
}

func (r *PostgresMediaRepo) ListAll(ctx context.Context) ([]domain.MediaItem, error) {
	query := `
		SELECT id, path, title, type, poster_url, duration, created_at, metadata_status, metadata
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
		var statusStr sql.NullString
		var metadataJSON []byte

		err := rows.Scan(
			&item.ID,
			&item.Path,
			&item.Title,
			&typeStr,
			&item.PosterURL,
			&durationInt,
			&item.CreatedAt,
			&statusStr,
			&metadataJSON,
		)
		if err != nil {
			return nil, err
		}

		item.Type = domain.MediaType(typeStr)
		item.Duration = time.Duration(durationInt)

		if statusStr.Valid {
			item.MetadataStatus = domain.MetadataStatus(statusStr.String)
		} else {
			item.MetadataStatus = domain.MetadataStatusNew
		}

		if len(metadataJSON) > 0 {
			var md domain.MediaMetadata
			if err := json.Unmarshal(metadataJSON, &md); err == nil {
				item.Metadata = &md
			}
		}

		items = append(items, item)
	}

	return items, nil
}
