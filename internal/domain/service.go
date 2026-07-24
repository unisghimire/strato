package domain

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/entity"
)

// BlobStore abstracts object storage (MinIO in this deployment, any
// S3-compatible store in general). Keys are opaque to callers.
type BlobStore interface {
	// Put streams r into the object identified by key. size must be exact
	// (-1 is rejected: it disables multipart sizing and Content-Length).
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens a streaming reader over the object.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	// SignedGetURL mints a presigned download URL carrying a
	// Content-Disposition for filename, valid for ttl.
	SignedGetURL(ctx context.Context, key, filename string, ttl time.Duration) (string, error)
}

// Identity is the authenticated principal attached to a request context.
type Identity struct {
	UserID uuid.UUID
	Email  string
	Role   entity.Role
}

// TokenIssuer mints and validates access tokens. The use-case layer depends
// on this interface; the JWT implementation lives in internal/auth.
type TokenIssuer interface {
	IssueAccessToken(user *entity.User) (token string, expiresAt time.Time, err error)
	ParseAccessToken(token string) (*Identity, error)
}

// RateLimiter enforces request budgets. Allow reports whether the caller
// identified by key may proceed under limit events per window.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// DistributedLock provides short-lived cross-instance mutual exclusion
// (e.g. serializing CompleteUpload for one session across replicas).
type DistributedLock interface {
	// TryLock returns release=nil, ok=false when the lock is held elsewhere.
	TryLock(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool, err error)
}

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// RealClock is the production Clock.
type RealClock struct{}

// Now returns the current UTC time.
func (RealClock) Now() time.Time { return time.Now().UTC() }
