package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type PostgresUserRepo struct {
	db *sql.DB
}

func NewUserRepo(db *sql.DB) *PostgresUserRepo {
	return &PostgresUserRepo{db: db}
}

func (r *PostgresUserRepo) InitSchema() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			last_seen TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	return err
}

func (r *PostgresUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	query := `
		SELECT id, email, created_at, last_seen
		FROM users
		WHERE id = $1
	`
	row := r.db.QueryRowContext(ctx, query, id)

	var user domain.User
	err := row.Scan(&user.ID, &user.Email, &user.CreatedAt, &user.LastSeen)
	if err == sql.ErrNoRows {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *PostgresUserRepo) Save(ctx context.Context, user *domain.User) error {
	query := `
		INSERT INTO users (id, email, created_at, last_seen)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			email = EXCLUDED.email,
			last_seen = EXCLUDED.last_seen;
	`
	_, err := r.db.ExecContext(ctx, query, user.ID, user.Email, user.CreatedAt, user.LastSeen)
	return err
}
