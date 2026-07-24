package usecase

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/internal/service"
	"github.com/unisghimire/strato/pkg/crypto"
	"github.com/unisghimire/strato/pkg/pagination"
)

// FileConfig carries file use-case policy.
type FileConfig struct {
	SignedURLTTL time.Duration
}

// FileUseCase implements file metadata operations, folders, versioning,
// locking, and decrypting downloads.
type FileUseCase struct {
	tx       domain.TxManager
	files    domain.FileRepository
	folders  domain.FolderRepository
	versions domain.VersionRepository
	blobs    domain.BlobRepository
	shares   domain.ShareRepository
	quotas   domain.QuotaRepository
	store    domain.BlobStore
	signer   *service.URLSigner
	auditor  *Auditor
	clock    domain.Clock
	kek      []byte
	cfg      FileConfig
}

// NewFileUseCase wires the file use case.
func NewFileUseCase(
	tx domain.TxManager,
	files domain.FileRepository,
	folders domain.FolderRepository,
	versions domain.VersionRepository,
	blobs domain.BlobRepository,
	shares domain.ShareRepository,
	quotas domain.QuotaRepository,
	store domain.BlobStore,
	signer *service.URLSigner,
	auditor *Auditor,
	clock domain.Clock,
	kek []byte,
	cfg FileConfig,
) *FileUseCase {
	return &FileUseCase{
		tx: tx, files: files, folders: folders, versions: versions, blobs: blobs,
		shares: shares, quotas: quotas, store: store, signer: signer,
		auditor: auditor, clock: clock, kek: kek, cfg: cfg,
	}
}

// Get returns file metadata for any caller with viewer access.
func (u *FileUseCase) Get(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) (*entity.File, error) {
	return authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionViewer, u.clock.Now())
}

// List pages the caller's own files in a folder.
func (u *FileUseCase) List(ctx context.Context, ident *domain.Identity, folderID *uuid.UUID, includeDeleted, descending bool, cur pagination.Cursor, limit int) ([]*entity.File, error) {
	if folderID != nil {
		if err := u.ownFolder(ctx, ident, *folderID); err != nil {
			return nil, err
		}
	}
	return u.files.List(ctx, ident.UserID, domain.FileListFilter{
		FolderID:       folderID,
		IncludeDeleted: includeDeleted,
		Descending:     descending,
	}, cur, limit)
}

// Search matches the caller's files by name substring and optional MIME type.
func (u *FileUseCase) Search(ctx context.Context, ident *domain.Identity, query, mimeType string, cur pagination.Cursor, limit int) ([]*entity.File, error) {
	if len(query) < 2 || len(query) > 100 {
		return nil, fmt.Errorf("%w: query must be 2-100 characters", domain.ErrInvalidArgument)
	}
	return u.files.Search(ctx, ident.UserID, query, mimeType, cur, limit)
}

// Rename renames a file (editor access, lock respected).
func (u *FileUseCase) Rename(ctx context.Context, ident *domain.Identity, fileID uuid.UUID, newName string) (*entity.File, error) {
	newName, err := validateName(newName)
	if err != nil {
		return nil, err
	}
	f, err := u.mutableFile(ctx, ident, fileID, entity.PermissionEditor)
	if err != nil {
		return nil, err
	}
	if err := u.files.Rename(ctx, f.ID, newName); err != nil {
		return nil, err
	}
	return u.files.GetByID(ctx, f.ID)
}

// Move re-parents a file into another folder owned by the file's owner.
func (u *FileUseCase) Move(ctx context.Context, ident *domain.Identity, fileID uuid.UUID, targetFolderID *uuid.UUID) (*entity.File, error) {
	f, err := u.mutableFile(ctx, ident, fileID, entity.PermissionEditor)
	if err != nil {
		return nil, err
	}
	if targetFolderID != nil {
		folder, err := u.folders.GetByID(ctx, *targetFolderID)
		if err != nil || folder.OwnerID != f.OwnerID {
			return nil, domain.ErrNotFound
		}
	}
	if err := u.files.Move(ctx, f.ID, targetFolderID); err != nil {
		return nil, err
	}
	return u.files.GetByID(ctx, f.ID)
}

// Delete soft-deletes a file (owner permission level, lock respected).
// Content and history are retained until the trash retention window ends.
func (u *FileUseCase) Delete(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) error {
	f, err := u.mutableFile(ctx, ident, fileID, entity.PermissionOwner)
	if err != nil {
		return err
	}
	if err := u.files.SoftDelete(ctx, f.ID); err != nil {
		return err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditFileDeleted, "file", f.ID.String(), nil)
	return nil
}

// Restore un-deletes a file from trash.
func (u *FileUseCase) Restore(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) (*entity.File, error) {
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionOwner, u.clock.Now())
	if err != nil {
		return nil, err
	}
	if !f.IsDeleted {
		return f, nil // idempotent
	}
	if err := u.files.Restore(ctx, f.ID); err != nil {
		if errIs(err, domain.ErrAlreadyExists) {
			return nil, fmt.Errorf("%w: a file with this name exists again; rename it first", domain.ErrAlreadyExists)
		}
		return nil, err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditFileRestored, "file", f.ID.String(), nil)
	return u.files.GetByID(ctx, f.ID)
}

// --- Versioning ---

// ListVersions pages a file's history, newest first.
func (u *FileUseCase) ListVersions(ctx context.Context, ident *domain.Identity, fileID uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Version, error) {
	if _, err := u.Get(ctx, ident, fileID); err != nil {
		return nil, err
	}
	return u.versions.ListByFile(ctx, fileID, cur, limit)
}

// RestoreVersion makes an old version current by appending a new version
// that references the same blob — history stays immutable, and the restored
// bytes are charged to quota like any new version.
func (u *FileUseCase) RestoreVersion(ctx context.Context, ident *domain.Identity, fileID, versionID uuid.UUID) (*entity.File, error) {
	f, err := u.mutableFile(ctx, ident, fileID, entity.PermissionEditor)
	if err != nil {
		return nil, err
	}
	old, err := u.versions.GetByID(ctx, versionID)
	if err != nil || old.FileID != f.ID {
		return nil, domain.ErrNotFound
	}
	newVersion := &entity.Version{
		ID:        uuid.New(),
		FileID:    f.ID,
		BlobID:    old.BlobID,
		SizeBytes: old.SizeBytes,
		CreatedBy: ident.UserID,
	}
	err = u.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.versions.Create(ctx, newVersion); err != nil {
			return err
		}
		if err := u.blobs.IncrementRef(ctx, old.BlobID); err != nil {
			return err
		}
		if err := u.files.SetCurrentVersion(ctx, f.ID, newVersion.ID); err != nil {
			return err
		}
		return u.quotas.AddUsage(ctx, f.OwnerID, old.SizeBytes)
	})
	if err != nil {
		return nil, err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditVersionRestored, "file", f.ID.String(),
		map[string]any{"version_id": versionID.String()})
	return u.files.GetByID(ctx, f.ID)
}

// --- Locking ---

// Lock takes the advisory write lock. Re-locking one's own lock is a no-op;
// someone else's lock yields ErrFileLocked.
func (u *FileUseCase) Lock(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) error {
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionEditor, u.clock.Now())
	if err != nil {
		return err
	}
	if f.LockedBy != nil {
		if *f.LockedBy == ident.UserID {
			return nil
		}
		return domain.ErrFileLocked
	}
	if err := u.files.SetLock(ctx, f.ID, &ident.UserID); err != nil {
		return err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditFileLocked, "file", f.ID.String(), nil)
	return nil
}

// Unlock releases the lock. Only the holder, the file owner, or an admin may
// release it.
func (u *FileUseCase) Unlock(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) error {
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionEditor, u.clock.Now())
	if err != nil {
		return err
	}
	if f.LockedBy == nil {
		return nil
	}
	holder := *f.LockedBy == ident.UserID
	privileged := f.OwnerID == ident.UserID || ident.Role == entity.RoleAdmin
	if !holder && !privileged {
		return domain.ErrFileLocked
	}
	if err := u.files.SetLock(ctx, f.ID, nil); err != nil {
		return err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditFileUnlocked, "file", f.ID.String(), nil)
	return nil
}

// --- Folders ---

// CreateFolder creates a folder under parent (nil = root).
func (u *FileUseCase) CreateFolder(ctx context.Context, ident *domain.Identity, name string, parentID *uuid.UUID) (*entity.Folder, error) {
	name, err := validateName(name)
	if err != nil {
		return nil, err
	}
	if parentID != nil {
		if err := u.ownFolder(ctx, ident, *parentID); err != nil {
			return nil, err
		}
	}
	folder := &entity.Folder{ID: uuid.New(), OwnerID: ident.UserID, ParentID: parentID, Name: name}
	if err := u.folders.Create(ctx, folder); err != nil {
		return nil, err
	}
	return u.folders.GetByID(ctx, folder.ID)
}

// ListFolder returns subfolders and a page of files. Subfolders are returned
// in full (bounded by practical folder sizes); files carry the pagination
// cursor.
func (u *FileUseCase) ListFolder(ctx context.Context, ident *domain.Identity, folderID *uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Folder, []*entity.File, error) {
	if folderID != nil {
		if err := u.ownFolder(ctx, ident, *folderID); err != nil {
			return nil, nil, err
		}
	}
	folders, err := u.folders.ListChildren(ctx, ident.UserID, folderID, pagination.Cursor{}, 1000)
	if err != nil {
		return nil, nil, err
	}
	files, err := u.files.List(ctx, ident.UserID, domain.FileListFilter{FolderID: folderID}, cur, limit)
	if err != nil {
		return nil, nil, err
	}
	return folders, files, nil
}

// DeleteFolder soft-deletes an empty folder. Non-empty folders are rejected
// rather than recursively deleted — recursive delete is an easy footgun and
// a deliberate non-feature at this layer.
func (u *FileUseCase) DeleteFolder(ctx context.Context, ident *domain.Identity, folderID uuid.UUID) error {
	if err := u.ownFolder(ctx, ident, folderID); err != nil {
		return err
	}
	hasChildren, err := u.folders.HasChildren(ctx, folderID)
	if err != nil {
		return err
	}
	if hasChildren {
		return fmt.Errorf("%w: folder is not empty", domain.ErrInvalidArgument)
	}
	return u.folders.SoftDelete(ctx, folderID)
}

// --- Download ---

// OpenDownload authorizes the caller and returns a decrypting reader over
// the file's current content. The caller must Close the reader.
func (u *FileUseCase) OpenDownload(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) (io.ReadCloser, *entity.File, error) {
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionViewer, u.clock.Now())
	if err != nil {
		return nil, nil, err
	}
	if f.IsDeleted {
		return nil, nil, domain.ErrNotFound
	}
	rc, err := u.OpenFileContent(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditFileDownloaded, "file", f.ID.String(), nil)
	return rc, f, nil
}

// OpenFileContent opens a decrypting reader for an already-authorized file.
// Exported for the share use case (public links authorize by token, not
// identity).
func (u *FileUseCase) OpenFileContent(ctx context.Context, f *entity.File) (io.ReadCloser, error) {
	if f.CurrentVersionID == nil {
		return nil, fmt.Errorf("%w: file has no content yet", domain.ErrNotFound)
	}
	version, err := u.versions.GetByID(ctx, *f.CurrentVersionID)
	if err != nil {
		return nil, err
	}
	blob, err := u.blobs.GetByID(ctx, version.BlobID)
	if err != nil {
		return nil, err
	}
	dek, err := crypto.UnwrapKey(u.kek, blob.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrapping DEK for blob %s: %w", blob.ID, err)
	}
	obj, err := u.store.Get(ctx, blob.StorageKey)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.NewStreamReader(obj, dek, crypto.DefaultSegmentSize)
	if err != nil {
		_ = obj.Close()
		return nil, err
	}
	return &readCloser{Reader: plain, closer: obj}, nil
}

// SignedDownloadQuery authorizes the caller, then mints short-lived signed
// query parameters for the raw download endpoint (credential-free GET).
func (u *FileUseCase) SignedDownloadQuery(ctx context.Context, ident *domain.Identity, fileID uuid.UUID) (url.Values, time.Duration, error) {
	if _, err := u.Get(ctx, ident, fileID); err != nil {
		return nil, 0, err
	}
	return u.signer.Sign(fileID, ident.UserID, u.cfg.SignedURLTTL), u.cfg.SignedURLTTL, nil
}

// VerifySignedDownload validates signed query params and returns the file
// after re-running authorization for the embedded user — revoked shares die
// with the share, not at URL expiry.
func (u *FileUseCase) VerifySignedDownload(ctx context.Context, fileID uuid.UUID, query url.Values) (io.ReadCloser, *entity.File, error) {
	userID, err := u.signer.Verify(fileID, query)
	if err != nil {
		return nil, nil, err
	}
	return u.OpenDownload(ctx, &domain.Identity{UserID: userID}, fileID)
}

// --- helpers ---

// mutableFile authorizes a write-level operation and enforces the advisory
// lock.
func (u *FileUseCase) mutableFile(ctx context.Context, ident *domain.Identity, fileID uuid.UUID, need entity.Permission) (*entity.File, error) {
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, need, u.clock.Now())
	if err != nil {
		return nil, err
	}
	if f.IsDeleted {
		return nil, domain.ErrNotFound
	}
	if f.IsLockedByOther(ident.UserID) {
		return nil, domain.ErrFileLocked
	}
	return f, nil
}

// ownFolder verifies the folder exists and belongs to the caller, hiding
// other users' folders as not-found.
func (u *FileUseCase) ownFolder(ctx context.Context, ident *domain.Identity, folderID uuid.UUID) error {
	folder, err := u.folders.GetByID(ctx, folderID)
	if err != nil || folder.OwnerID != ident.UserID {
		return domain.ErrNotFound
	}
	return nil
}

// readCloser pairs a decrypting reader with the underlying object handle.
type readCloser struct {
	io.Reader
	closer io.Closer
}

func (r *readCloser) Close() error { return r.closer.Close() }
