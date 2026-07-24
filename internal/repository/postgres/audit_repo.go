package postgres

import (
	"context"
	"encoding/json"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// AuditRepo implements domain.AuditRepository.
type AuditRepo struct{ db *DB }

// NewAuditRepo constructs an AuditRepo.
func NewAuditRepo(db *DB) *AuditRepo { return &AuditRepo{db: db} }

var _ domain.AuditRepository = (*AuditRepo)(nil)

// Insert appends one audit record.
func (r *AuditRepo) Insert(ctx context.Context, e *entity.AuditEntry) error {
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var ip any
	if e.IP != "" {
		ip = e.IP
	}
	_, err = r.db.q(ctx).Exec(ctx, `
		INSERT INTO audit_logs (user_id, action, resource_type, resource_id, ip, user_agent, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.UserID, e.Action, e.ResourceType, e.ResourceID, ip, e.UserAgent, metaJSON)
	return mapErr(err)
}
