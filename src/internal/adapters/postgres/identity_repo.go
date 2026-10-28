package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/yaffw/yaffw/src/internal/ports"
)

type PostgresIdentityRepo struct {
	db *sql.DB
}

func NewIdentityRepo(db *sql.DB) *PostgresIdentityRepo {
	return &PostgresIdentityRepo{db: db}
}

func (r *PostgresIdentityRepo) InitSchema(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS item_identity (
			uuid          VARCHAR(36) PRIMARY KEY,
			library_id    VARCHAR(255) NOT NULL,
			path_hash     VARCHAR(64) NOT NULL,
			identity      VARCHAR(255),
			hierarchy_key VARCHAR(255),
			title         VARCHAR(255),
			last_seen     TIMESTAMP NOT NULL,
			status        VARCHAR(20) DEFAULT 'active',
			missing_since TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_item_identity_path_hash ON item_identity(path_hash);
		CREATE INDEX IF NOT EXISTS idx_item_identity_hierarchy ON item_identity(hierarchy_key);
		CREATE INDEX IF NOT EXISTS idx_item_identity_identity ON item_identity(identity);
	`)
	return err
}

func (r *PostgresIdentityRepo) GetByPathHash(ctx context.Context, pathHash string) (*ports.IdentityRecord, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since
		 FROM item_identity WHERE path_hash = $1`, pathHash)
	return scanIdentityRecord(row)
}

func (r *PostgresIdentityRepo) GetByIdentity(ctx context.Context, identity string) (*ports.IdentityRecord, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since
		 FROM item_identity WHERE identity = $1`, identity)
	return scanIdentityRecord(row)
}

func (r *PostgresIdentityRepo) GetByHierarchyKey(ctx context.Context, hierarchyKey string) (*ports.IdentityRecord, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since
		 FROM item_identity WHERE hierarchy_key = $1`, hierarchyKey)
	return scanIdentityRecord(row)
}

func (r *PostgresIdentityRepo) GetByID(ctx context.Context, uuid string) (*ports.IdentityRecord, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since
		 FROM item_identity WHERE uuid = $1`, uuid)
	return scanIdentityRecord(row)
}

func (r *PostgresIdentityRepo) Save(ctx context.Context, rec *ports.IdentityRecord) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO item_identity (uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (uuid) DO UPDATE SET
		   library_id = EXCLUDED.library_id,
		   path_hash = EXCLUDED.path_hash,
		   identity = EXCLUDED.identity,
		   hierarchy_key = EXCLUDED.hierarchy_key,
		   title = EXCLUDED.title,
		   last_seen = EXCLUDED.last_seen,
		   status = EXCLUDED.status,
		   missing_since = EXCLUDED.missing_since`,
		rec.UUID, rec.LibraryID, rec.PathHash, rec.Identity, rec.HierarchyKey,
		rec.Title, rec.LastSeen, rec.Status, rec.MissingSince)
	return err
}

func (r *PostgresIdentityRepo) MarkMissing(ctx context.Context, uuid string) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx,
		`UPDATE item_identity SET status = 'missing', missing_since = $1 WHERE uuid = $2`,
		now, uuid)
	return err
}

func (r *PostgresIdentityRepo) ListMissing(ctx context.Context) ([]*ports.IdentityRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since
		 FROM item_identity WHERE status = 'missing'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentityRows(rows)
}

func (r *PostgresIdentityRepo) ListAll(ctx context.Context) ([]*ports.IdentityRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT uuid, library_id, path_hash, identity, hierarchy_key, title, last_seen, status, missing_since
		 FROM item_identity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentityRows(rows)
}

func scanIdentityRecord(row *sql.Row) (*ports.IdentityRecord, error) {
	rec := &ports.IdentityRecord{}
	var missingSince sql.NullTime
	err := row.Scan(
		&rec.UUID, &rec.LibraryID, &rec.PathHash, &rec.Identity,
		&rec.HierarchyKey, &rec.Title, &rec.LastSeen, &rec.Status, &missingSince,
	)
	if err != nil {
		return nil, err
	}
	if missingSince.Valid {
		rec.MissingSince = &missingSince.Time
	}
	return rec, nil
}

func scanIdentityRows(rows *sql.Rows) ([]*ports.IdentityRecord, error) {
	var records []*ports.IdentityRecord
	for rows.Next() {
		rec := &ports.IdentityRecord{}
		var missingSince sql.NullTime
		err := rows.Scan(
			&rec.UUID, &rec.LibraryID, &rec.PathHash, &rec.Identity,
			&rec.HierarchyKey, &rec.Title, &rec.LastSeen, &rec.Status, &missingSince,
		)
		if err != nil {
			return nil, err
		}
		if missingSince.Valid {
			rec.MissingSince = &missingSince.Time
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}
