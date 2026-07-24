package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// FileRepo implements domain.FileRepository.
type FileRepo struct{ db *DB }

// NewFileRepo constructs a FileRepo.
func NewFileRepo(db *DB) *FileRepo { return &FileRepo{db: db} }

var _ domain.FileRepository = (*FileRepo)(nil)

// fileSelect joins the current version and its blob so read models carry
// size/checksum/version without N+1 queries.
const fileSelect = `
	SELECT f.id, f.owner_id, f.folder_id, f.name, f.mime_type, f.current_version_id,
	       f.locked_by, f.locked_at, f.is_deleted, f.deleted_at, f.created_at, f.updated_at,
	       COALESCE(v.size_bytes, 0), COALESCE(b.checksum_sha256, ''::bytea), COALESCE(v.version_number, 0)
	FROM files f
	LEFT JOIN file_versions v ON v.id = f.current_version_id
	LEFT JOIN blobs b ON b.id = v.blob_id`

// Create inserts file metadata (no version yet).
func (r *FileRepo) Create(ctx context.Context, f *entity.File) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO files (id, owner_id, folder_id, name, mime_type)
		VALUES ($1, $2, $3, $4, $5)`, f.ID, f.OwnerID, f.FolderID, f.Name, f.MimeType)
	return mapErr(err)
}

// GetByID fetches a file including soft-deleted ones; the use-case layer
// decides whether deleted files are visible for the given operation.
func (r *FileRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.File, error) {
	return scanFile(r.db.q(ctx).QueryRow(ctx, fileSelect+` WHERE f.id = $1`, id))
}

// GetByName fetches a live file by its sibling-unique name.
func (r *FileRepo) GetByName(ctx context.Context, ownerID uuid.UUID, folderID *uuid.UUID, name string) (*entity.File, error) {
	return scanFile(r.db.q(ctx).QueryRow(ctx, fileSelect+`
		WHERE f.owner_id = $1 AND f.folder_id IS NOT DISTINCT FROM $2
		  AND f.name = $3 AND f.is_deleted = false`, ownerID, folderID, name))
}

// List pages a folder's files with a keyset cursor.
func (r *FileRepo) List(ctx context.Context, ownerID uuid.UUID, filter domain.FileListFilter, cur pagination.Cursor, limit int) ([]*entity.File, error) {
	sql := fileSelect + ` WHERE f.owner_id = $1 AND f.folder_id IS NOT DISTINCT FROM $2`
	args := []any{ownerID, filter.FolderID}
	if !filter.IncludeDeleted {
		sql += ` AND f.is_deleted = false`
	}
	cmp, ord := ">", "ASC"
	if filter.Descending {
		cmp, ord = "<", "DESC"
	}
	if !cur.IsZero() {
		sql += fmt.Sprintf(` AND (f.created_at, f.id) %s ($%d, $%d)`, cmp, len(args)+1, len(args)+2)
		args = append(args, cur.CreatedAt, cur.ID)
	}
	sql += fmt.Sprintf(` ORDER BY f.created_at %s, f.id %s LIMIT $%d`, ord, ord, len(args)+1)
	args = append(args, limit)
	return r.queryFiles(ctx, sql, args...)
}

// Search matches file names by trigram-indexed substring with an optional
// MIME filter. Scoped to one owner — cross-tenant search is impossible by
// construction.
func (r *FileRepo) Search(ctx context.Context, ownerID uuid.UUID, query, mimeType string, cur pagination.Cursor, limit int) ([]*entity.File, error) {
	sql := fileSelect + ` WHERE f.owner_id = $1 AND f.is_deleted = false AND f.name ILIKE '%' || $2 || '%'`
	args := []any{ownerID, query}
	if mimeType != "" {
		sql += fmt.Sprintf(` AND f.mime_type = $%d`, len(args)+1)
		args = append(args, mimeType)
	}
	if !cur.IsZero() {
		sql += fmt.Sprintf(` AND (f.created_at, f.id) > ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, cur.CreatedAt, cur.ID)
	}
	sql += fmt.Sprintf(` ORDER BY f.created_at, f.id LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	return r.queryFiles(ctx, sql, args...)
}

// Rename changes the file name; sibling uniqueness is enforced by index.
func (r *FileRepo) Rename(ctx context.Context, id uuid.UUID, newName string) error {
	return r.execOne(ctx, `UPDATE files SET name = $2 WHERE id = $1 AND is_deleted = false`, id, newName)
}

// Move re-parents the file.
func (r *FileRepo) Move(ctx context.Context, id uuid.UUID, folderID *uuid.UUID) error {
	return r.execOne(ctx, `UPDATE files SET folder_id = $2 WHERE id = $1 AND is_deleted = false`, id, folderID)
}

// SetCurrentVersion moves the current-version pointer.
func (r *FileRepo) SetCurrentVersion(ctx context.Context, fileID, versionID uuid.UUID) error {
	return r.execOne(ctx, `UPDATE files SET current_version_id = $2 WHERE id = $1`, fileID, versionID)
}

// SoftDelete marks the file deleted (restorable).
func (r *FileRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	return r.execOne(ctx, `
		UPDATE files SET is_deleted = true, deleted_at = now()
		WHERE id = $1 AND is_deleted = false`, id)
}

// Restore un-deletes a file. Fails with ErrAlreadyExists if a live sibling
// took the name in the meantime (unique index).
func (r *FileRepo) Restore(ctx context.Context, id uuid.UUID) error {
	return r.execOne(ctx, `
		UPDATE files SET is_deleted = false, deleted_at = NULL
		WHERE id = $1 AND is_deleted = true`, id)
}

// SetLock sets (userID != nil) or clears (nil) the advisory file lock.
func (r *FileRepo) SetLock(ctx context.Context, id uuid.UUID, userID *uuid.UUID) error {
	if userID != nil {
		return r.execOne(ctx, `
			UPDATE files SET locked_by = $2, locked_at = now()
			WHERE id = $1 AND is_deleted = false`, id, userID)
	}
	return r.execOne(ctx, `
		UPDATE files SET locked_by = NULL, locked_at = NULL
		WHERE id = $1`, id)
}

// ListDeletedBefore returns purge candidates: files soft-deleted before the
// retention cutoff.
func (r *FileRepo) ListDeletedBefore(ctx context.Context, olderThan time.Time, limit int) ([]*entity.File, error) {
	return r.queryFiles(ctx, fileSelect+`
		WHERE f.is_deleted = true AND f.deleted_at < $1
		ORDER BY f.deleted_at LIMIT $2`, olderThan, limit)
}

// Purge hard-deletes a file; file_versions cascade via FK. The
// current_version_id self-reference is cleared first to satisfy the FK.
func (r *FileRepo) Purge(ctx context.Context, id uuid.UUID) error {
	if _, err := r.db.q(ctx).Exec(ctx,
		`UPDATE files SET current_version_id = NULL WHERE id = $1`, id); err != nil {
		return mapErr(err)
	}
	return r.execOne(ctx, `DELETE FROM files WHERE id = $1`, id)
}

func (r *FileRepo) execOne(ctx context.Context, sql string, args ...any) error {
	tag, err := r.db.q(ctx).Exec(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *FileRepo) queryFiles(ctx context.Context, sql string, args ...any) ([]*entity.File, error) {
	rows, err := r.db.q(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*entity.File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, mapErr(rows.Err())
}

func scanFile(row pgx.Row) (*entity.File, error) {
	var f entity.File
	err := row.Scan(&f.ID, &f.OwnerID, &f.FolderID, &f.Name, &f.MimeType, &f.CurrentVersionID,
		&f.LockedBy, &f.LockedAt, &f.IsDeleted, &f.DeletedAt, &f.CreatedAt, &f.UpdatedAt,
		&f.SizeBytes, &f.ChecksumSHA256, &f.VersionNumber)
	if err != nil {
		return nil, mapErr(err)
	}
	return &f, nil
}
