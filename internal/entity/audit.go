package entity

import (
	"time"

	"github.com/google/uuid"
)

// AuditAction enumerates recorded actions. String-typed (not iota) so log
// rows stay self-describing.
type AuditAction string

// Audited actions.
const (
	AuditUserRegistered  AuditAction = "user.registered"
	AuditUserLogin       AuditAction = "user.login"
	AuditUserLoginFailed AuditAction = "user.login_failed"
	AuditUserLogout      AuditAction = "user.logout"
	AuditTokenReuse      AuditAction = "auth.token_reuse_detected"
	AuditFileUploaded    AuditAction = "file.uploaded"
	AuditFileDownloaded  AuditAction = "file.downloaded"
	AuditFileDeleted     AuditAction = "file.deleted"
	AuditFileRestored    AuditAction = "file.restored"
	AuditFileLocked      AuditAction = "file.locked"
	AuditFileUnlocked    AuditAction = "file.unlocked"
	AuditVersionRestored AuditAction = "file.version_restored"
	AuditShareCreated    AuditAction = "share.created"
	AuditShareRevoked    AuditAction = "share.revoked"
	AuditShareAccessed   AuditAction = "share.accessed"
)

// AuditEntry is one append-only audit record. UserID is nil for anonymous
// actions (e.g. public-link access).
type AuditEntry struct {
	ID           int64
	UserID       *uuid.UUID
	Action       AuditAction
	ResourceType string
	ResourceID   string
	IP           string
	UserAgent    string
	Metadata     map[string]any
	CreatedAt    time.Time
}
