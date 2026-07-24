package entity

import (
	"time"

	"github.com/google/uuid"
)

// Permission is the access level a share grants. Levels are ordered:
// owner ⊃ editor ⊃ viewer.
type Permission string

// Share permission levels.
const (
	PermissionViewer Permission = "viewer"
	PermissionEditor Permission = "editor"
	PermissionOwner  Permission = "owner"
)

// Allows reports whether p satisfies the required level.
func (p Permission) Allows(required Permission) bool {
	rank := map[Permission]int{PermissionViewer: 1, PermissionEditor: 2, PermissionOwner: 3}
	return rank[p] >= rank[required]
}

// Valid reports whether p is a known permission.
func (p Permission) Valid() bool {
	switch p {
	case PermissionViewer, PermissionEditor, PermissionOwner:
		return true
	}
	return false
}

// Share grants access to a file: either to a named user (GranteeID set) or
// via a public link (TokenHash set) — never both. Public links may carry an
// Argon2id password gate and an expiry.
type Share struct {
	ID           uuid.UUID
	FileID       uuid.UUID
	OwnerID      uuid.UUID
	GranteeID    *uuid.UUID
	TokenHash    []byte
	Permission   Permission
	PasswordHash *string
	ExpiresAt    *time.Time
	CreatedAt    time.Time
	RevokedAt    *time.Time
}

// IsActive reports whether the share currently grants access.
func (s *Share) IsActive(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	if s.ExpiresAt != nil && now.After(*s.ExpiresAt) {
		return false
	}
	return true
}

// IsPublicLink reports whether this share is token-addressed.
func (s *Share) IsPublicLink() bool { return s.TokenHash != nil }
