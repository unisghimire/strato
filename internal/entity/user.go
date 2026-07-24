// Package entity defines Strato's core domain types. Entities are pure data
// with domain behavior only — no persistence, transport, or framework
// concerns — so every outer layer depends inward on this package.
package entity

import (
	"time"

	"github.com/google/uuid"
)

// Role controls coarse-grained authorization.
type Role string

// Supported roles.
const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// User is a registered account.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	DisplayName  string
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// IsAdmin reports whether the user holds the admin role.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// Session is one refresh-token credential. Tokens rotate on every refresh;
// FamilyID ties rotations of a single login together so that reuse of an
// already-rotated token (theft indicator) revokes the whole family.
type Session struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	FamilyID   uuid.UUID
	TokenHash  []byte
	UserAgent  string
	IP         string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *uuid.UUID
}

// IsActive reports whether the session can still mint access tokens.
func (s *Session) IsActive(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// Quota tracks a user's storage allowance and usage.
type Quota struct {
	UserID     uuid.UUID
	QuotaBytes int64
	UsedBytes  int64
	UpdatedAt  time.Time
}

// CanStore reports whether n additional bytes fit within the quota.
func (q *Quota) CanStore(n int64) bool {
	return q.UsedBytes+n <= q.QuotaBytes
}
