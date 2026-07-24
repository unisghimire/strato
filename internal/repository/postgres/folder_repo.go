package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// FolderRepo implements domain.FolderRepository.
type FolderRepo struct{ db *DB }

// NewFolderRepo constructs a FolderRepo.
func NewFolderRepo(db *DB) *FolderRepo { return &FolderRepo{db: db} }

var _ domain.FolderRepository = (*FolderRepo)(nil)

const folderCols = `id, owner_id, parent_id, name, created_at, updated_at, deleted_at`

// Create inserts a folder.
func (r *FolderRepo) Create(ctx context.Context, f *entity.Folder) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO folders (id, owner_id, parent_id, name)
		VALUES ($1, $2, $3, $4)`, f.ID, f.OwnerID, f.ParentID, f.Name)
	return mapErr(err)
}

// GetByID fetches a live folder.
func (r *FolderRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Folder, error) {
	var f entity.Folder
	err := r.db.q(ctx).QueryRow(ctx,
		`SELECT `+folderCols+` FROM folders WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&f.ID, &f.OwnerID, &f.ParentID, &f.Name, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &f, nil
}

// ListChildren pages through subfolders using a keyset cursor.
func (r *FolderRepo) ListChildren(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Folder, error) {
	sql := `SELECT ` + folderCols + ` FROM folders
		WHERE owner_id = $1 AND parent_id IS NOT DISTINCT FROM $2 AND deleted_at IS NULL`
	args := []any{ownerID, parentID}
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

	var out []*entity.Folder
	for rows.Next() {
		var f entity.Folder
		if err := rows.Scan(&f.ID, &f.OwnerID, &f.ParentID, &f.Name, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, &f)
	}
	return out, mapErr(rows.Err())
}

// SoftDelete marks the folder deleted.
func (r *FolderRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE folders SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// HasChildren reports whether live folders or files exist under the folder.
func (r *FolderRepo) HasChildren(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM folders WHERE parent_id = $1 AND deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM files WHERE folder_id = $1 AND is_deleted = false)`, id).
		Scan(&exists)
	return exists, mapErr(err)
}
