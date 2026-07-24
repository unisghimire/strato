package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// QuotaRepo implements domain.QuotaRepository.
type QuotaRepo struct{ db *DB }

// NewQuotaRepo constructs a QuotaRepo.
func NewQuotaRepo(db *DB) *QuotaRepo { return &QuotaRepo{db: db} }

var _ domain.QuotaRepository = (*QuotaRepo)(nil)

// Create inserts the quota row for a new user.
func (r *QuotaRepo) Create(ctx context.Context, q *entity.Quota) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO storage_quotas (user_id, quota_bytes, used_bytes)
		VALUES ($1, $2, $3)`, q.UserID, q.QuotaBytes, q.UsedBytes)
	return mapErr(err)
}

// Get fetches a user's quota.
func (r *QuotaRepo) Get(ctx context.Context, userID uuid.UUID) (*entity.Quota, error) {
	var q entity.Quota
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT user_id, quota_bytes, used_bytes, updated_at
		FROM storage_quotas WHERE user_id = $1`, userID).
		Scan(&q.UserID, &q.QuotaBytes, &q.UsedBytes, &q.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &q, nil
}

// AddUsage atomically adjusts used_bytes. A positive delta that would exceed
// the quota affects zero rows and returns ErrQuotaExceeded; concurrent
// uploads therefore cannot jointly overshoot (the check and update are one
// statement). Negative deltas floor at zero rather than tripping the CHECK.
func (r *QuotaRepo) AddUsage(ctx context.Context, userID uuid.UUID, delta int64) error {
	if delta >= 0 {
		tag, err := r.db.q(ctx).Exec(ctx, `
			UPDATE storage_quotas
			SET used_bytes = used_bytes + $2
			WHERE user_id = $1 AND used_bytes + $2 <= quota_bytes`, userID, delta)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" { // check_violation
				return domain.ErrQuotaExceeded
			}
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			// Distinguish missing quota row from an over-quota write.
			if _, getErr := r.Get(ctx, userID); getErr != nil {
				return getErr
			}
			return domain.ErrQuotaExceeded
		}
		return nil
	}

	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE storage_quotas
		SET used_bytes = GREATEST(used_bytes + $2, 0)
		WHERE user_id = $1`, userID, delta)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
