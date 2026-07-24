package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// UserRepo implements domain.UserRepository.
type UserRepo struct{ db *DB }

// NewUserRepo constructs a UserRepo.
func NewUserRepo(db *DB) *UserRepo { return &UserRepo{db: db} }

var _ domain.UserRepository = (*UserRepo)(nil)

const userCols = `id, email, password_hash, display_name, role, created_at, updated_at, deleted_at`

// Create inserts a new user; the caller supplies the ID.
func (r *UserRepo) Create(ctx context.Context, u *entity.User) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, role)
		VALUES ($1, $2, $3, $4, $5)`,
		u.ID, u.Email, u.PasswordHash, u.DisplayName, u.Role)
	return mapErr(err)
}

// GetByID fetches a live (non-deleted) user.
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.User, error) {
	return r.scanOne(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE id = $1 AND deleted_at IS NULL`, id))
}

// GetByEmail fetches a live user by (case-insensitive) email.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*entity.User, error) {
	return r.scanOne(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE email = $1 AND deleted_at IS NULL`, email))
}

// UpdatePasswordHash replaces the stored hash (rehash-on-login upgrades).
func (r *UserRepo) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	tag, err := r.db.q(ctx).Exec(ctx,
		`UPDATE users SET password_hash = $2 WHERE id = $1 AND deleted_at IS NULL`, id, hash)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func (r *UserRepo) scanOne(row rowScanner) (*entity.User, error) {
	var u entity.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role,
		&u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}
