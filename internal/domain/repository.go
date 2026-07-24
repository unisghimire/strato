package domain

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// TxManager runs a function inside a database transaction. Repositories
// called with the derived context join the same transaction, which lets use
// cases compose multi-repository invariants (e.g. "create version + bump
// blob refcount + charge quota" atomically) without knowing about SQL.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// UserRepository persists accounts.
type UserRepository interface {
	Create(ctx context.Context, u *entity.User) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.User, error)
	GetByEmail(ctx context.Context, email string) (*entity.User, error)
	UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error
}

// SessionRepository persists refresh-token sessions.
type SessionRepository interface {
	Create(ctx context.Context, s *entity.Session) error
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*entity.Session, error)
	// Revoke marks one session revoked, recording its successor if rotating.
	Revoke(ctx context.Context, id uuid.UUID, replacedBy *uuid.UUID) error
	// RevokeFamily revokes every session in a rotation family (theft response).
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// FolderRepository persists folders.
type FolderRepository interface {
	Create(ctx context.Context, f *entity.Folder) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Folder, error)
	ListChildren(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Folder, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	// HasChildren reports whether the folder contains live folders or files.
	HasChildren(ctx context.Context, id uuid.UUID) (bool, error)
}

// FileListFilter narrows file listings.
type FileListFilter struct {
	FolderID       *uuid.UUID
	IncludeDeleted bool
	Descending     bool
}

// FileRepository persists file metadata. All reads join the current version
// to populate denormalized size/checksum fields.
type FileRepository interface {
	Create(ctx context.Context, f *entity.File) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.File, error)
	GetByName(ctx context.Context, ownerID uuid.UUID, folderID *uuid.UUID, name string) (*entity.File, error)
	List(ctx context.Context, ownerID uuid.UUID, filter FileListFilter, cur pagination.Cursor, limit int) ([]*entity.File, error)
	// Search matches name substrings (trigram-indexed) with optional MIME filter.
	Search(ctx context.Context, ownerID uuid.UUID, query, mimeType string, cur pagination.Cursor, limit int) ([]*entity.File, error)
	Rename(ctx context.Context, id uuid.UUID, newName string) error
	Move(ctx context.Context, id uuid.UUID, folderID *uuid.UUID) error
	SetCurrentVersion(ctx context.Context, fileID, versionID uuid.UUID) error
	SoftDelete(ctx context.Context, id uuid.UUID) error
	Restore(ctx context.Context, id uuid.UUID) error
	SetLock(ctx context.Context, id uuid.UUID, userID *uuid.UUID) error
	// ListDeletedBefore returns soft-deleted files past the trash retention
	// window (purge candidates for GC).
	ListDeletedBefore(ctx context.Context, olderThan time.Time, limit int) ([]*entity.File, error)
	// Purge hard-deletes a file row (versions cascade). Blob refs and quota
	// must be released by the caller in the same transaction beforehand.
	Purge(ctx context.Context, id uuid.UUID) error
}

// VersionRepository persists immutable file history.
type VersionRepository interface {
	// Create inserts the next version; the version number is assigned inside
	// the statement (MAX+1 under the enclosing transaction) to avoid races.
	Create(ctx context.Context, v *entity.Version) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Version, error)
	ListByFile(ctx context.Context, fileID uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Version, error)
	// TotalBytes sums the logical size of every version of a file (the
	// amount of quota to release on purge).
	TotalBytes(ctx context.Context, fileID uuid.UUID) (int64, error)
}

// BlobRepository persists the content-addressed blob index.
type BlobRepository interface {
	Create(ctx context.Context, b *entity.Blob) error
	GetByChecksum(ctx context.Context, sha256 []byte) (*entity.Blob, error)
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Blob, error)
	IncrementRef(ctx context.Context, id uuid.UUID) error
	DecrementRef(ctx context.Context, id uuid.UUID) error
	// DecrementRefsForFile releases every blob referenced by a file's
	// versions (used on purge).
	DecrementRefsForFile(ctx context.Context, fileID uuid.UUID) error
	// ListOrphaned returns zero-ref blobs older than grace, for GC.
	ListOrphaned(ctx context.Context, olderThan time.Time, limit int) ([]*entity.Blob, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// UploadRepository persists resumable upload sessions and chunk receipts.
type UploadRepository interface {
	CreateSession(ctx context.Context, s *entity.UploadSession) error
	GetSession(ctx context.Context, id uuid.UUID) (*entity.UploadSession, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status entity.UploadStatus) error
	// SaveChunk upserts a chunk receipt (re-uploading a chunk is idempotent).
	SaveChunk(ctx context.Context, c *entity.Chunk) error
	ListChunks(ctx context.Context, sessionID uuid.UUID) ([]*entity.Chunk, error)
	// ListExpired returns pending sessions past their TTL, for GC.
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.UploadSession, error)
}

// ShareRepository persists shares and public links.
type ShareRepository interface {
	Create(ctx context.Context, s *entity.Share) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Share, error)
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*entity.Share, error)
	// FindGrant returns the active share granting userID access to fileID,
	// or ErrNotFound.
	FindGrant(ctx context.Context, fileID, userID uuid.UUID) (*entity.Share, error)
	ListByOwner(ctx context.Context, ownerID uuid.UUID, fileID *uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Share, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}

// QuotaRepository persists storage accounting. AddUsage must be safe under
// concurrency (atomic UPDATE, CHECK-guarded) and return ErrQuotaExceeded
// when a positive delta does not fit.
type QuotaRepository interface {
	Create(ctx context.Context, q *entity.Quota) error
	Get(ctx context.Context, userID uuid.UUID) (*entity.Quota, error)
	AddUsage(ctx context.Context, userID uuid.UUID, delta int64) error
}

// AuditRepository appends audit records. Implementations must never block
// the request path on failure — auditing is best-effort by design.
type AuditRepository interface {
	Insert(ctx context.Context, e *entity.AuditEntry) error
}
