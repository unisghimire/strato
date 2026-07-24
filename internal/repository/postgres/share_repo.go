package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// ShareRepo implements domain.ShareRepository.
type ShareRepo struct{ db *DB }

// NewShareRepo constructs a ShareRepo.
func NewShareRepo(db *DB) *ShareRepo { return &ShareRepo{db: db} }

var _ domain.ShareRepository = (*ShareRepo)(nil)

const shareCols = `id, file_id, owner_id, grantee_id, token_hash, permission,
	password_hash, expires_at, created_at, revoked_at`

// Create inserts a share (private grant or public link).
func (r *ShareRepo) Create(ctx context.Context, s *entity.Share) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO shares
			(id, file_id, owner_id, grantee_id, token_hash, permission, password_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		s.ID, s.FileID, s.OwnerID, s.GranteeID, s.TokenHash, s.Permission, s.PasswordHash, s.ExpiresAt)
	return mapErr(err)
}

// GetByID fetches one share.
func (r *ShareRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Share, error) {
	return r.scanOne(ctx, `SELECT `+shareCols+` FROM shares WHERE id = $1`, id)
}

// GetByTokenHash resolves a public-link token digest to its share.
func (r *ShareRepo) GetByTokenHash(ctx context.Context, tokenHash []byte) (*entity.Share, error) {
	return r.scanOne(ctx, `SELECT `+shareCols+` FROM shares WHERE token_hash = $1`, tokenHash)
}

// FindGrant returns the active private grant for (fileID, userID).
// Expiry and revocation are filtered here; the use case still re-checks
// IsActive for defense in depth.
func (r *ShareRepo) FindGrant(ctx context.Context, fileID, userID uuid.UUID) (*entity.Share, error) {
	return r.scanOne(ctx, `
		SELECT `+shareCols+` FROM shares
		WHERE file_id = $1 AND grantee_id = $2 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`, fileID, userID)
}

// ListByOwner pages shares created by ownerID, optionally scoped to a file.
func (r *ShareRepo) ListByOwner(ctx context.Context, ownerID uuid.UUID, fileID *uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Share, error) {
	sql := `SELECT ` + shareCols + ` FROM shares WHERE owner_id = $1 AND revoked_at IS NULL`
	args := []any{ownerID}
	if fileID != nil {
		sql += fmt.Sprintf(` AND file_id = $%d`, len(args)+1)
		args = append(args, *fileID)
	}
	if !cur.IsZero() {
		sql += fmt.Sprintf(` AND (created_at, id) > ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, cur.CreatedAt, cur.ID)
	}
	sql += fmt.Sprintf(` ORDER BY created_at, id LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := r.db.q(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*entity.Share
	for rows.Next() {
		var s entity.Share
		if err := rows.Scan(&s.ID, &s.FileID, &s.OwnerID, &s.GranteeID, &s.TokenHash,
			&s.Permission, &s.PasswordHash, &s.ExpiresAt, &s.CreatedAt, &s.RevokedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, &s)
	}
	return out, mapErr(rows.Err())
}

// Revoke deactivates a share. Idempotent on already-revoked shares is a
// not-found so callers can surface it.
func (r *ShareRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.q(ctx).Exec(ctx,
		`UPDATE shares SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *ShareRepo) scanOne(ctx context.Context, sql string, args ...any) (*entity.Share, error) {
	var s entity.Share
	err := r.db.q(ctx).QueryRow(ctx, sql, args...).
		Scan(&s.ID, &s.FileID, &s.OwnerID, &s.GranteeID, &s.TokenHash,
			&s.Permission, &s.PasswordHash, &s.ExpiresAt, &s.CreatedAt, &s.RevokedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}
