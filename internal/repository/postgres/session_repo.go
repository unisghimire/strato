package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// SessionRepo implements domain.SessionRepository.
type SessionRepo struct{ db *DB }

// NewSessionRepo constructs a SessionRepo.
func NewSessionRepo(db *DB) *SessionRepo { return &SessionRepo{db: db} }

var _ domain.SessionRepository = (*SessionRepo)(nil)

// Create inserts a refresh-token session.
func (r *SessionRepo) Create(ctx context.Context, s *entity.Session) error {
	var ip any
	if s.IP != "" {
		ip = s.IP
	}
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO sessions (id, user_id, family_id, token_hash, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.UserID, s.FamilyID, s.TokenHash, s.UserAgent, ip, s.ExpiresAt)
	return mapErr(err)
}

// GetByTokenHash looks up a session by refresh-token digest. Revoked and
// expired sessions ARE returned — the use case must distinguish "revoked"
// (reuse attack) from "missing".
func (r *SessionRepo) GetByTokenHash(ctx context.Context, tokenHash []byte) (*entity.Session, error) {
	var s entity.Session
	var ip *string
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT id, user_id, family_id, token_hash, user_agent, host(ip), expires_at,
		       created_at, revoked_at, replaced_by
		FROM sessions WHERE token_hash = $1`, tokenHash).
		Scan(&s.ID, &s.UserID, &s.FamilyID, &s.TokenHash, &s.UserAgent, &ip,
			&s.ExpiresAt, &s.CreatedAt, &s.RevokedAt, &s.ReplacedBy)
	if err != nil {
		return nil, mapErr(err)
	}
	if ip != nil {
		s.IP = *ip
	}
	return &s, nil
}

// Revoke marks a session revoked, optionally linking its rotation successor.
func (r *SessionRepo) Revoke(ctx context.Context, id uuid.UUID, replacedBy *uuid.UUID) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), replaced_by = $2
		WHERE id = $1 AND revoked_at IS NULL`, id, replacedBy)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RevokeFamily revokes all sessions in a rotation family. Idempotent.
func (r *SessionRepo) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE family_id = $1 AND revoked_at IS NULL`, familyID)
	return mapErr(err)
}

// DeleteExpired removes sessions expired before the given time (GC).
func (r *SessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.db.q(ctx).Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1`, before)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}
