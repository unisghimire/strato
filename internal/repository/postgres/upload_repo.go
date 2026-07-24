package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// UploadRepo implements domain.UploadRepository.
type UploadRepo struct{ db *DB }

// NewUploadRepo constructs an UploadRepo.
func NewUploadRepo(db *DB) *UploadRepo { return &UploadRepo{db: db} }

var _ domain.UploadRepository = (*UploadRepo)(nil)

const sessionCols = `id, user_id, folder_id, name, mime_type, size_bytes, checksum_sha256,
	chunk_size, total_chunks, status, created_at, expires_at`

// CreateSession inserts an upload session.
func (r *UploadRepo) CreateSession(ctx context.Context, s *entity.UploadSession) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO upload_sessions
			(id, user_id, folder_id, name, mime_type, size_bytes, checksum_sha256,
			 chunk_size, total_chunks, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.ID, s.UserID, s.FolderID, s.Name, s.MimeType, s.SizeBytes, s.ChecksumSHA256,
		s.ChunkSize, s.TotalChunks, s.Status, s.ExpiresAt)
	return mapErr(err)
}

// GetSession fetches one session.
func (r *UploadRepo) GetSession(ctx context.Context, id uuid.UUID) (*entity.UploadSession, error) {
	var s entity.UploadSession
	err := r.db.q(ctx).QueryRow(ctx,
		`SELECT `+sessionCols+` FROM upload_sessions WHERE id = $1`, id).
		Scan(&s.ID, &s.UserID, &s.FolderID, &s.Name, &s.MimeType, &s.SizeBytes,
			&s.ChecksumSHA256, &s.ChunkSize, &s.TotalChunks, &s.Status, &s.CreatedAt, &s.ExpiresAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// UpdateStatus transitions a session's lifecycle state.
func (r *UploadRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status entity.UploadStatus) error {
	tag, err := r.db.q(ctx).Exec(ctx,
		`UPDATE upload_sessions SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SaveChunk upserts a chunk receipt. Retransmitting a chunk (client retry
// after a dropped response) simply overwrites the previous receipt.
func (r *UploadRepo) SaveChunk(ctx context.Context, c *entity.Chunk) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO upload_chunks (session_id, chunk_index, size_bytes, checksum_sha256, storage_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, chunk_index) DO UPDATE
		SET size_bytes = EXCLUDED.size_bytes,
		    checksum_sha256 = EXCLUDED.checksum_sha256,
		    storage_key = EXCLUDED.storage_key,
		    uploaded_at = now()`,
		c.SessionID, c.Index, c.SizeBytes, c.ChecksumSHA256, c.StorageKey)
	return mapErr(err)
}

// ListChunks returns received chunks ordered by index.
func (r *UploadRepo) ListChunks(ctx context.Context, sessionID uuid.UUID) ([]*entity.Chunk, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT session_id, chunk_index, size_bytes, checksum_sha256, storage_key, uploaded_at
		FROM upload_chunks WHERE session_id = $1 ORDER BY chunk_index`, sessionID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*entity.Chunk
	for rows.Next() {
		var c entity.Chunk
		if err := rows.Scan(&c.SessionID, &c.Index, &c.SizeBytes, &c.ChecksumSHA256,
			&c.StorageKey, &c.UploadedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, &c)
	}
	return out, mapErr(rows.Err())
}

// ListExpired returns pending sessions past their TTL for GC.
func (r *UploadRepo) ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.UploadSession, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT `+sessionCols+` FROM upload_sessions
		WHERE status = 'pending' AND expires_at < $1
		ORDER BY expires_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*entity.UploadSession
	for rows.Next() {
		var s entity.UploadSession
		if err := rows.Scan(&s.ID, &s.UserID, &s.FolderID, &s.Name, &s.MimeType, &s.SizeBytes,
			&s.ChecksumSHA256, &s.ChunkSize, &s.TotalChunks, &s.Status, &s.CreatedAt, &s.ExpiresAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, &s)
	}
	return out, mapErr(rows.Err())
}
