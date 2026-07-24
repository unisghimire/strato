package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// VersionRepo implements domain.VersionRepository.
type VersionRepo struct{ db *DB }

// NewVersionRepo constructs a VersionRepo.
func NewVersionRepo(db *DB) *VersionRepo { return &VersionRepo{db: db} }

var _ domain.VersionRepository = (*VersionRepo)(nil)

// Create inserts the next version for a file. The version number is computed
// as MAX+1 inside the INSERT; under the enclosing transaction plus the
// UNIQUE (file_id, version_number) constraint, concurrent writers cannot
// mint duplicate numbers — one of them retries via the unique violation.
func (r *VersionRepo) Create(ctx context.Context, v *entity.Version) error {
	err := r.db.q(ctx).QueryRow(ctx, `
		INSERT INTO file_versions (id, file_id, version_number, blob_id, size_bytes, created_by)
		VALUES ($1, $2,
		        (SELECT COALESCE(MAX(version_number), 0) + 1 FROM file_versions WHERE file_id = $2),
		        $3, $4, $5)
		RETURNING version_number, created_at`,
		v.ID, v.FileID, v.BlobID, v.SizeBytes, v.CreatedBy).
		Scan(&v.VersionNumber, &v.CreatedAt)
	return mapErr(err)
}

const versionSelect = `
	SELECT v.id, v.file_id, v.version_number, v.blob_id, v.size_bytes, v.created_by,
	       v.created_at, b.checksum_sha256
	FROM file_versions v
	JOIN blobs b ON b.id = v.blob_id`

// GetByID fetches one version.
func (r *VersionRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Version, error) {
	var v entity.Version
	err := r.db.q(ctx).QueryRow(ctx, versionSelect+` WHERE v.id = $1`, id).
		Scan(&v.ID, &v.FileID, &v.VersionNumber, &v.BlobID, &v.SizeBytes, &v.CreatedBy,
			&v.CreatedAt, &v.ChecksumSHA256)
	if err != nil {
		return nil, mapErr(err)
	}
	return &v, nil
}

// ListByFile pages version history, newest first.
func (r *VersionRepo) ListByFile(ctx context.Context, fileID uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Version, error) {
	sql := versionSelect + ` WHERE v.file_id = $1`
	args := []any{fileID}
	if !cur.IsZero() {
		sql += fmt.Sprintf(` AND (v.created_at, v.id) < ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, cur.CreatedAt, cur.ID)
	}
	sql += fmt.Sprintf(` ORDER BY v.created_at DESC, v.id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := r.db.q(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*entity.Version
	for rows.Next() {
		var v entity.Version
		if err := rows.Scan(&v.ID, &v.FileID, &v.VersionNumber, &v.BlobID, &v.SizeBytes,
			&v.CreatedBy, &v.CreatedAt, &v.ChecksumSHA256); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, &v)
	}
	return out, mapErr(rows.Err())
}

// TotalBytes sums logical bytes across all versions of a file.
func (r *VersionRepo) TotalBytes(ctx context.Context, fileID uuid.UUID) (int64, error) {
	var total int64
	err := r.db.q(ctx).QueryRow(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM file_versions WHERE file_id = $1`, fileID).
		Scan(&total)
	return total, mapErr(err)
}
