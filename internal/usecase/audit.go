// Package usecase contains Strato's application business logic. Use cases
// depend exclusively on domain ports and entities — never on transport,
// SQL, or storage SDKs — which is what keeps them unit-testable with mocks.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// Auditor records audit events best-effort: an audit failure is logged and
// counted but never fails the user's request. Compliance-grade guaranteed
// delivery would swap this for an outbox table written in the same
// transaction as the mutation.
type Auditor struct {
	repo domain.AuditRepository
	log  *slog.Logger
}

// NewAuditor constructs an Auditor.
func NewAuditor(repo domain.AuditRepository, log *slog.Logger) *Auditor {
	return &Auditor{repo: repo, log: log}
}

// Record appends an audit entry, enriching it with request metadata (IP,
// user agent) from the context. It survives request cancellation: a user
// closing the connection mid-download must not erase the download record.
func (a *Auditor) Record(ctx context.Context, userID *uuid.UUID, action entity.AuditAction, resourceType, resourceID string, metadata map[string]any) {
	meta := auth.RequestMetaFromContext(ctx)
	entry := &entity.AuditEntry{
		UserID:       userID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IP:           meta.IP,
		UserAgent:    meta.UserAgent,
		Metadata:     metadata,
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.repo.Insert(dctx, entry); err != nil {
		a.log.Error("audit insert failed", "action", action, "error", err)
	}
}
