// Package mocks provides in-memory fakes of the domain ports. Fakes (real
// behavior, fake storage) are preferred over assertion mocks here: use-case
// tests exercise genuine multi-step flows — upload → complete → download —
// against them, which catches contract drift that per-call expectation mocks
// hide.
package mocks

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/pagination"
)

// NopTx satisfies domain.TxManager without transactional semantics —
// acceptable for fakes whose maps are already guarded by mutexes.
type NopTx struct{}

// WithinTx runs fn directly.
func (NopTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// FixedClock is a controllable domain.Clock.
type FixedClock struct {
	mu sync.Mutex
	T  time.Time
}

// Now returns the fixed time.
func (c *FixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.T
}

// Advance moves the clock forward.
func (c *FixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.T = c.T.Add(d)
}

// --- Users ---

// UserRepo is an in-memory domain.UserRepository.
type UserRepo struct {
	mu    sync.Mutex
	users map[uuid.UUID]*entity.User
}

// NewUserRepo constructs the fake.
func NewUserRepo() *UserRepo { return &UserRepo{users: map[uuid.UUID]*entity.User{}} }

// Create inserts a user, enforcing email uniqueness.
func (r *UserRepo) Create(_ context.Context, u *entity.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.users {
		if strings.EqualFold(existing.Email, u.Email) {
			return domain.ErrAlreadyExists
		}
	}
	cp := *u
	cp.CreatedAt = time.Now()
	r.users[u.ID] = &cp
	return nil
}

// GetByID fetches a user.
func (r *UserRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}

// GetByEmail fetches a user by email.
func (r *UserRepo) GetByEmail(_ context.Context, email string) (*entity.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Email, email) {
			cp := *u
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

// UpdatePasswordHash replaces the stored hash.
func (r *UserRepo) UpdatePasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.PasswordHash = hash
	return nil
}

// --- Sessions ---

// SessionRepo is an in-memory domain.SessionRepository.
type SessionRepo struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]*entity.Session
}

// NewSessionRepo constructs the fake.
func NewSessionRepo() *SessionRepo {
	return &SessionRepo{sessions: map[uuid.UUID]*entity.Session{}}
}

// Create inserts a session.
func (r *SessionRepo) Create(_ context.Context, s *entity.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.sessions[s.ID] = &cp
	return nil
}

// GetByTokenHash finds a session by digest.
func (r *SessionRepo) GetByTokenHash(_ context.Context, tokenHash []byte) (*entity.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if bytes.Equal(s.TokenHash, tokenHash) {
			cp := *s
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

// Revoke marks one session revoked.
func (r *SessionRepo) Revoke(_ context.Context, id uuid.UUID, replacedBy *uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.RevokedAt != nil {
		return domain.ErrNotFound
	}
	now := time.Now()
	s.RevokedAt = &now
	s.ReplacedBy = replacedBy
	return nil
}

// RevokeFamily revokes a whole rotation family.
func (r *SessionRepo) RevokeFamily(_ context.Context, familyID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, s := range r.sessions {
		if s.FamilyID == familyID && s.RevokedAt == nil {
			s.RevokedAt = &now
		}
	}
	return nil
}

// DeleteExpired removes expired sessions.
func (r *SessionRepo) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for id, s := range r.sessions {
		if s.ExpiresAt.Before(before) {
			delete(r.sessions, id)
			n++
		}
	}
	return n, nil
}

// --- Quotas ---

// QuotaRepo is an in-memory domain.QuotaRepository.
type QuotaRepo struct {
	mu     sync.Mutex
	quotas map[uuid.UUID]*entity.Quota
}

// NewQuotaRepo constructs the fake.
func NewQuotaRepo() *QuotaRepo { return &QuotaRepo{quotas: map[uuid.UUID]*entity.Quota{}} }

// Create inserts a quota row.
func (r *QuotaRepo) Create(_ context.Context, q *entity.Quota) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *q
	r.quotas[q.UserID] = &cp
	return nil
}

// Get fetches a quota.
func (r *QuotaRepo) Get(_ context.Context, userID uuid.UUID) (*entity.Quota, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.quotas[userID]; ok {
		cp := *q
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}

// AddUsage mirrors the SQL implementation's semantics exactly.
func (r *QuotaRepo) AddUsage(_ context.Context, userID uuid.UUID, delta int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.quotas[userID]
	if !ok {
		return domain.ErrNotFound
	}
	if delta >= 0 && q.UsedBytes+delta > q.QuotaBytes {
		return domain.ErrQuotaExceeded
	}
	q.UsedBytes += delta
	if q.UsedBytes < 0 {
		q.UsedBytes = 0
	}
	return nil
}

// --- Folders ---

// FolderRepo is an in-memory domain.FolderRepository.
type FolderRepo struct {
	mu      sync.Mutex
	folders map[uuid.UUID]*entity.Folder
	files   *FileRepo // for HasChildren
}

// NewFolderRepo constructs the fake; pass the FileRepo so HasChildren sees
// files too.
func NewFolderRepo(files *FileRepo) *FolderRepo {
	return &FolderRepo{folders: map[uuid.UUID]*entity.Folder{}, files: files}
}

// Create inserts a folder.
func (r *FolderRepo) Create(_ context.Context, f *entity.Folder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *f
	cp.CreatedAt = time.Now()
	r.folders[f.ID] = &cp
	return nil
}

// GetByID fetches a live folder.
func (r *FolderRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Folder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.folders[id]; ok && f.DeletedAt == nil {
		cp := *f
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}

// ListChildren lists live subfolders.
func (r *FolderRepo) ListChildren(_ context.Context, ownerID uuid.UUID, parentID *uuid.UUID, _ pagination.Cursor, limit int) ([]*entity.Folder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Folder
	for _, f := range r.folders {
		if f.OwnerID == ownerID && f.DeletedAt == nil && uuidPtrEq(f.ParentID, parentID) {
			cp := *f
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SoftDelete marks the folder deleted.
func (r *FolderRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.folders[id]
	if !ok || f.DeletedAt != nil {
		return domain.ErrNotFound
	}
	now := time.Now()
	f.DeletedAt = &now
	return nil
}

// HasChildren reports live children.
func (r *FolderRepo) HasChildren(ctx context.Context, id uuid.UUID) (bool, error) {
	r.mu.Lock()
	for _, f := range r.folders {
		if f.ParentID != nil && *f.ParentID == id && f.DeletedAt == nil {
			r.mu.Unlock()
			return true, nil
		}
	}
	r.mu.Unlock()

	files, err := r.files.List(ctx, uuid.Nil, domain.FileListFilter{FolderID: &id}, pagination.Cursor{}, 1)
	if err != nil {
		return false, err
	}
	return len(files) > 0, nil
}

func uuidPtrEq(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// --- Blob store ---

// BlobStore is an in-memory domain.BlobStore.
type BlobStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

// NewBlobStore constructs the fake.
func NewBlobStore() *BlobStore { return &BlobStore{objects: map[string][]byte{}} }

// Put stores exactly size bytes, mirroring MinIO's Content-Length contract.
func (s *BlobStore) Put(_ context.Context, key string, r io.Reader, size int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return io.ErrUnexpectedEOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}

// Get opens a reader over the object.
func (s *BlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Delete removes an object (idempotent).
func (s *BlobStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

// SignedGetURL returns a fake URL.
func (s *BlobStore) SignedGetURL(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "https://fake.example/" + key, nil
}

// Len reports stored object count (test assertions).
func (s *BlobStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// --- Distributed lock ---

// Lock is an in-memory domain.DistributedLock.
type Lock struct {
	mu   sync.Mutex
	held map[string]bool
}

// NewLock constructs the fake.
func NewLock() *Lock { return &Lock{held: map[string]bool{}} }

// TryLock acquires if free.
func (l *Lock) TryLock(_ context.Context, key string, _ time.Duration) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] {
		return nil, false, nil
	}
	l.held[key] = true
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.held, key)
	}, true, nil
}

// --- Audit ---

// AuditRepo records entries in memory.
type AuditRepo struct {
	mu      sync.Mutex
	Entries []*entity.AuditEntry
}

// NewAuditRepo constructs the fake.
func NewAuditRepo() *AuditRepo { return &AuditRepo{} }

// Insert appends an entry.
func (r *AuditRepo) Insert(_ context.Context, e *entity.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Entries = append(r.Entries, e)
	return nil
}

// Actions lists recorded action names (test assertions).
func (r *AuditRepo) Actions() []entity.AuditAction {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]entity.AuditAction, len(r.Entries))
	for i, e := range r.Entries {
		out[i] = e.Action
	}
	return out
}
