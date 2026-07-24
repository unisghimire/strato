package mocks

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// FileRepo is an in-memory domain.FileRepository. It keeps version/blob
// links so denormalized read fields behave like the SQL joins.
type FileRepo struct {
	mu    sync.Mutex
	files map[uuid.UUID]*entity.File

	versions *VersionRepo
	blobs    *BlobRepo
}

// NewFileRepo constructs the fake, linked to version and blob fakes for
// denormalization.
func NewFileRepo(versions *VersionRepo, blobs *BlobRepo) *FileRepo {
	return &FileRepo{files: map[uuid.UUID]*entity.File{}, versions: versions, blobs: blobs}
}

// Create inserts a file, enforcing sibling-name uniqueness among live files.
func (r *FileRepo) Create(_ context.Context, f *entity.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.files {
		if existing.OwnerID == f.OwnerID && !existing.IsDeleted &&
			uuidPtrEq(existing.FolderID, f.FolderID) && existing.Name == f.Name {
			return domain.ErrAlreadyExists
		}
	}
	cp := *f
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.files[f.ID] = &cp
	return nil
}

// GetByID fetches a file (including soft-deleted).
func (r *FileRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return r.denormalize(f), nil
}

// GetByName fetches a live file by sibling-unique name.
func (r *FileRepo) GetByName(_ context.Context, ownerID uuid.UUID, folderID *uuid.UUID, name string) (*entity.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.files {
		if f.OwnerID == ownerID && !f.IsDeleted && uuidPtrEq(f.FolderID, folderID) && f.Name == name {
			return r.denormalize(f), nil
		}
	}
	return nil, domain.ErrNotFound
}

// List filters files like the SQL implementation. ownerID uuid.Nil matches
// any owner (used internally by FolderRepo.HasChildren).
func (r *FileRepo) List(_ context.Context, ownerID uuid.UUID, filter domain.FileListFilter, _ pagination.Cursor, limit int) ([]*entity.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.File
	for _, f := range r.files {
		if ownerID != uuid.Nil && f.OwnerID != ownerID {
			continue
		}
		if !filter.IncludeDeleted && f.IsDeleted {
			continue
		}
		if !uuidPtrEq(f.FolderID, filter.FolderID) {
			continue
		}
		out = append(out, r.denormalize(f))
	}
	sort.Slice(out, func(i, j int) bool {
		if filter.Descending {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Search matches names by substring.
func (r *FileRepo) Search(_ context.Context, ownerID uuid.UUID, query, mimeType string, _ pagination.Cursor, limit int) ([]*entity.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.File
	for _, f := range r.files {
		if f.OwnerID != ownerID || f.IsDeleted {
			continue
		}
		if !strings.Contains(strings.ToLower(f.Name), strings.ToLower(query)) {
			continue
		}
		if mimeType != "" && f.MimeType != mimeType {
			continue
		}
		out = append(out, r.denormalize(f))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Rename renames a live file.
func (r *FileRepo) Rename(_ context.Context, id uuid.UUID, newName string) error {
	return r.mutate(id, func(f *entity.File) { f.Name = newName })
}

// Move re-parents a live file.
func (r *FileRepo) Move(_ context.Context, id uuid.UUID, folderID *uuid.UUID) error {
	return r.mutate(id, func(f *entity.File) { f.FolderID = folderID })
}

// SetCurrentVersion moves the version pointer.
func (r *FileRepo) SetCurrentVersion(_ context.Context, fileID, versionID uuid.UUID) error {
	return r.mutate(fileID, func(f *entity.File) { f.CurrentVersionID = &versionID })
}

// SoftDelete marks a file deleted.
func (r *FileRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	return r.mutate(id, func(f *entity.File) {
		now := time.Now()
		f.IsDeleted = true
		f.DeletedAt = &now
	})
}

// Restore un-deletes a file.
func (r *FileRepo) Restore(_ context.Context, id uuid.UUID) error {
	return r.mutate(id, func(f *entity.File) {
		f.IsDeleted = false
		f.DeletedAt = nil
	})
}

// SetLock sets or clears the advisory lock.
func (r *FileRepo) SetLock(_ context.Context, id uuid.UUID, userID *uuid.UUID) error {
	return r.mutate(id, func(f *entity.File) {
		f.LockedBy = userID
		if userID != nil {
			now := time.Now()
			f.LockedAt = &now
		} else {
			f.LockedAt = nil
		}
	})
}

// ListDeletedBefore returns purge candidates.
func (r *FileRepo) ListDeletedBefore(_ context.Context, olderThan time.Time, limit int) ([]*entity.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.File
	for _, f := range r.files {
		if f.IsDeleted && f.DeletedAt != nil && f.DeletedAt.Before(olderThan) {
			out = append(out, r.denormalize(f))
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Purge hard-deletes a file and its versions.
func (r *FileRepo) Purge(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	if _, ok := r.files[id]; !ok {
		r.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(r.files, id)
	r.mu.Unlock()
	r.versions.deleteByFile(ctx, id)
	return nil
}

func (r *FileRepo) mutate(id uuid.UUID, fn func(*entity.File)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	if !ok {
		return domain.ErrNotFound
	}
	fn(f)
	f.UpdatedAt = time.Now()
	return nil
}

// denormalize populates size/checksum/version from the current version,
// like the SQL LEFT JOINs. Caller holds r.mu.
func (r *FileRepo) denormalize(f *entity.File) *entity.File {
	cp := *f
	if f.CurrentVersionID != nil && r.versions != nil {
		if v, err := r.versions.GetByID(context.Background(), *f.CurrentVersionID); err == nil {
			cp.SizeBytes = v.SizeBytes
			cp.VersionNumber = v.VersionNumber
			if r.blobs != nil {
				if b, err := r.blobs.GetByID(context.Background(), v.BlobID); err == nil {
					cp.ChecksumSHA256 = b.ChecksumSHA256
				}
			}
		}
	}
	return &cp
}

// --- Versions ---

// VersionRepo is an in-memory domain.VersionRepository.
type VersionRepo struct {
	mu       sync.Mutex
	versions map[uuid.UUID]*entity.Version
	blobs    *BlobRepo
}

// NewVersionRepo constructs the fake.
func NewVersionRepo(blobs *BlobRepo) *VersionRepo {
	return &VersionRepo{versions: map[uuid.UUID]*entity.Version{}, blobs: blobs}
}

// Create assigns MAX+1 like the SQL implementation.
func (r *VersionRepo) Create(_ context.Context, v *entity.Version) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxN := 0
	for _, existing := range r.versions {
		if existing.FileID == v.FileID && existing.VersionNumber > maxN {
			maxN = existing.VersionNumber
		}
	}
	v.VersionNumber = maxN + 1
	v.CreatedAt = time.Now()
	cp := *v
	r.versions[v.ID] = &cp
	return nil
}

// GetByID fetches a version with blob checksum denormalized.
func (r *VersionRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Version, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.versions[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *v
	if r.blobs != nil {
		if b, err := r.blobs.GetByID(context.Background(), v.BlobID); err == nil {
			cp.ChecksumSHA256 = b.ChecksumSHA256
		}
	}
	return &cp, nil
}

// ListByFile returns history newest-first.
func (r *VersionRepo) ListByFile(_ context.Context, fileID uuid.UUID, _ pagination.Cursor, limit int) ([]*entity.Version, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Version
	for _, v := range r.versions {
		if v.FileID == fileID {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VersionNumber > out[j].VersionNumber })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// TotalBytes sums all version sizes for a file.
func (r *VersionRepo) TotalBytes(_ context.Context, fileID uuid.UUID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, v := range r.versions {
		if v.FileID == fileID {
			total += v.SizeBytes
		}
	}
	return total, nil
}

func (r *VersionRepo) deleteByFile(_ context.Context, fileID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.versions {
		if v.FileID == fileID {
			delete(r.versions, id)
		}
	}
}

// --- Blobs ---

// BlobRepo is an in-memory domain.BlobRepository.
type BlobRepo struct {
	mu    sync.Mutex
	blobs map[uuid.UUID]*entity.Blob
}

// NewBlobRepo constructs the fake.
func NewBlobRepo() *BlobRepo { return &BlobRepo{blobs: map[uuid.UUID]*entity.Blob{}} }

// Create inserts, enforcing checksum uniqueness.
func (r *BlobRepo) Create(_ context.Context, b *entity.Blob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.blobs {
		if bytes.Equal(existing.ChecksumSHA256, b.ChecksumSHA256) {
			return domain.ErrAlreadyExists
		}
	}
	cp := *b
	cp.CreatedAt = time.Now()
	r.blobs[b.ID] = &cp
	return nil
}

// GetByChecksum is the dedup lookup.
func (r *BlobRepo) GetByChecksum(_ context.Context, sha256 []byte) (*entity.Blob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.blobs {
		if bytes.Equal(b.ChecksumSHA256, sha256) {
			cp := *b
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

// GetByID fetches a blob.
func (r *BlobRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Blob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.blobs[id]; ok {
		cp := *b
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}

// IncrementRef adds a reference.
func (r *BlobRepo) IncrementRef(_ context.Context, id uuid.UUID) error {
	return r.addRef(id, 1)
}

// DecrementRef releases a reference.
func (r *BlobRepo) DecrementRef(_ context.Context, id uuid.UUID) error {
	return r.addRef(id, -1)
}

// DecrementRefsForFile releases refs for every version of a file.
func (r *BlobRepo) DecrementRefsForFile(_ context.Context, _ uuid.UUID) error {
	// The fake keeps this simple; GC tests wire counts explicitly.
	return nil
}

// ListOrphaned returns zero-ref blobs older than the cutoff.
func (r *BlobRepo) ListOrphaned(_ context.Context, olderThan time.Time, limit int) ([]*entity.Blob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Blob
	for _, b := range r.blobs {
		if b.RefCount == 0 && b.CreatedAt.Before(olderThan) {
			cp := *b
			out = append(out, &cp)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Delete removes a zero-ref blob.
func (r *BlobRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.blobs[id]
	if !ok || b.RefCount != 0 {
		return domain.ErrNotFound
	}
	delete(r.blobs, id)
	return nil
}

func (r *BlobRepo) addRef(id uuid.UUID, delta int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.blobs[id]
	if !ok {
		return domain.ErrNotFound
	}
	b.RefCount += delta
	if b.RefCount < 0 {
		b.RefCount = 0
	}
	return nil
}

// --- Uploads ---

// UploadRepo is an in-memory domain.UploadRepository.
type UploadRepo struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]*entity.UploadSession
	chunks   map[uuid.UUID]map[int]*entity.Chunk
}

// NewUploadRepo constructs the fake.
func NewUploadRepo() *UploadRepo {
	return &UploadRepo{
		sessions: map[uuid.UUID]*entity.UploadSession{},
		chunks:   map[uuid.UUID]map[int]*entity.Chunk{},
	}
}

// CreateSession inserts a session.
func (r *UploadRepo) CreateSession(_ context.Context, s *entity.UploadSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	cp.CreatedAt = time.Now()
	r.sessions[s.ID] = &cp
	return nil
}

// GetSession fetches a session.
func (r *UploadRepo) GetSession(_ context.Context, id uuid.UUID) (*entity.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}

// UpdateStatus transitions session state.
func (r *UploadRepo) UpdateStatus(_ context.Context, id uuid.UUID, status entity.UploadStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return domain.ErrNotFound
	}
	s.Status = status
	return nil
}

// SaveChunk upserts a receipt.
func (r *UploadRepo) SaveChunk(_ context.Context, c *entity.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chunks[c.SessionID] == nil {
		r.chunks[c.SessionID] = map[int]*entity.Chunk{}
	}
	cp := *c
	cp.UploadedAt = time.Now()
	r.chunks[c.SessionID][c.Index] = &cp
	return nil
}

// ListChunks returns receipts ordered by index.
func (r *UploadRepo) ListChunks(_ context.Context, sessionID uuid.UUID) ([]*entity.Chunk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Chunk
	for _, c := range r.chunks[sessionID] {
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// ListExpired returns pending sessions past TTL.
func (r *UploadRepo) ListExpired(_ context.Context, now time.Time, limit int) ([]*entity.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.UploadSession
	for _, s := range r.sessions {
		if s.Status == entity.UploadPending && s.ExpiresAt.Before(now) {
			cp := *s
			out = append(out, &cp)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- Shares ---

// ShareRepo is an in-memory domain.ShareRepository.
type ShareRepo struct {
	mu     sync.Mutex
	shares map[uuid.UUID]*entity.Share
}

// NewShareRepo constructs the fake.
func NewShareRepo() *ShareRepo { return &ShareRepo{shares: map[uuid.UUID]*entity.Share{}} }

// Create inserts a share, enforcing one active grant per (file, grantee).
func (r *ShareRepo) Create(_ context.Context, s *entity.Share) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.GranteeID != nil {
		for _, existing := range r.shares {
			if existing.RevokedAt == nil && existing.FileID == s.FileID &&
				existing.GranteeID != nil && *existing.GranteeID == *s.GranteeID {
				return domain.ErrAlreadyExists
			}
		}
	}
	cp := *s
	cp.CreatedAt = time.Now()
	r.shares[s.ID] = &cp
	return nil
}

// GetByID fetches a share.
func (r *ShareRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Share, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.shares[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}

// GetByTokenHash resolves a public-link digest.
func (r *ShareRepo) GetByTokenHash(_ context.Context, tokenHash []byte) (*entity.Share, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.shares {
		if bytes.Equal(s.TokenHash, tokenHash) {
			cp := *s
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

// FindGrant returns the active grant for (file, user).
func (r *ShareRepo) FindGrant(_ context.Context, fileID, userID uuid.UUID) (*entity.Share, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, s := range r.shares {
		if s.FileID == fileID && s.GranteeID != nil && *s.GranteeID == userID && s.IsActive(now) {
			cp := *s
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

// ListByOwner lists active shares created by ownerID.
func (r *ShareRepo) ListByOwner(_ context.Context, ownerID uuid.UUID, fileID *uuid.UUID, _ pagination.Cursor, limit int) ([]*entity.Share, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Share
	for _, s := range r.shares {
		if s.OwnerID != ownerID || s.RevokedAt != nil {
			continue
		}
		if fileID != nil && s.FileID != *fileID {
			continue
		}
		cp := *s
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Revoke deactivates a share.
func (r *ShareRepo) Revoke(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.shares[id]
	if !ok || s.RevokedAt != nil {
		return domain.ErrNotFound
	}
	now := time.Now()
	s.RevokedAt = &now
	return nil
}
