package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// BlobRepo implements domain.BlobRepository.
type BlobRepo struct{ db *DB }

// NewBlobRepo constructs a BlobRepo.
func NewBlobRepo(db *DB) *BlobRepo { return &BlobRepo{db: db} }

var _ domain.BlobRepository = (*BlobRepo)(nil)

const blobCols = `id, checksum_sha256, size_bytes, storage_key, wrapped_dek, ref_count, created_at`

// Create inserts a blob index row.
func (r *BlobRepo) Create(ctx context.Context, b *entity.Blob) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO blobs (id, checksum_sha256, size_bytes, storage_key, wrapped_dek, ref_count)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		b.ID, b.ChecksumSHA256, b.SizeBytes, b.StorageKey, b.WrappedDEK, b.RefCount)
	return mapErr(err)
}

// GetByChecksum is the dedup lookup: does this exact content already exist?
func (r *BlobRepo) GetByChecksum(ctx context.Context, sha256 []byte) (*entity.Blob, error) {
	return r.scanOne(ctx, `SELECT `+blobCols+` FROM blobs WHERE checksum_sha256 = $1`, sha256)
}

// GetByID fetches one blob.
func (r *BlobRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Blob, error) {
	return r.scanOne(ctx, `SELECT `+blobCols+` FROM blobs WHERE id = $1`, id)
}

// IncrementRef adds one reference.
func (r *BlobRepo) IncrementRef(ctx context.Context, id uuid.UUID) error {
	return r.execOne(ctx, `UPDATE blobs SET ref_count = ref_count + 1 WHERE id = $1`, id)
}

// DecrementRef releases one reference; floors at zero via the CHECK guard
// being pre-empted with GREATEST (an accounting bug must not poison deletes).
func (r *BlobRepo) DecrementRef(ctx context.Context, id uuid.UUID) error {
	return r.execOne(ctx, `UPDATE blobs SET ref_count = GREATEST(ref_count - 1, 0) WHERE id = $1`, id)
}

// DecrementRefsForFile releases one reference per version of the file,
// counting duplicate blob references correctly (a file whose v1 and v3 share
// content decrements that blob twice).
func (r *BlobRepo) DecrementRefsForFile(ctx context.Context, fileID uuid.UUID) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		UPDATE blobs b
		SET ref_count = GREATEST(b.ref_count - refs.n, 0)
		FROM (
			SELECT blob_id, COUNT(*) AS n
			FROM file_versions WHERE file_id = $1
			GROUP BY blob_id
		) refs
		WHERE b.id = refs.blob_id`, fileID)
	return mapErr(err)
}

// ListOrphaned returns zero-ref blobs past the grace window for GC. The
// grace period protects blobs created by in-flight uploads that have not yet
// attached a version.
func (r *BlobRepo) ListOrphaned(ctx context.Context, olderThan time.Time, limit int) ([]*entity.Blob, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT `+blobCols+` FROM blobs
		WHERE ref_count = 0 AND created_at < $1
		ORDER BY created_at LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*entity.Blob
	for rows.Next() {
		var b entity.Blob
		if err := rows.Scan(&b.ID, &b.ChecksumSHA256, &b.SizeBytes, &b.StorageKey,
			&b.WrappedDEK, &b.RefCount, &b.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, &b)
	}
	return out, mapErr(rows.Err())
}

// Delete removes a blob row. Only called by GC after the storage object is
// gone; ref_count = 0 is re-checked to close the race with a concurrent
// dedup hit.
func (r *BlobRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return r.execOne(ctx, `DELETE FROM blobs WHERE id = $1 AND ref_count = 0`, id)
}

func (r *BlobRepo) execOne(ctx context.Context, sql string, args ...any) error {
	tag, err := r.db.q(ctx).Exec(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *BlobRepo) scanOne(ctx context.Context, sql string, args ...any) (*entity.Blob, error) {
	var b entity.Blob
	err := r.db.q(ctx).QueryRow(ctx, sql, args...).
		Scan(&b.ID, &b.ChecksumSHA256, &b.SizeBytes, &b.StorageKey, &b.WrappedDEK, &b.RefCount, &b.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}
