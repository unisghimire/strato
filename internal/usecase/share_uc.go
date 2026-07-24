package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/pkg/crypto"
	"github.com/unisghimire/strato/pkg/pagination"
)

// ShareUseCase implements private grants and public links.
type ShareUseCase struct {
	shares  domain.ShareRepository
	files   domain.FileRepository
	users   domain.UserRepository
	auditor *Auditor
	clock   domain.Clock
}

// NewShareUseCase wires the share use case.
func NewShareUseCase(
	shares domain.ShareRepository,
	files domain.FileRepository,
	users domain.UserRepository,
	auditor *Auditor,
	clock domain.Clock,
) *ShareUseCase {
	return &ShareUseCase{shares: shares, files: files, users: users, auditor: auditor, clock: clock}
}

// CreateShare grants a named user access to a file. Requires owner-level
// rights on the file (owners can share; grantees with "owner" permission can
// re-share).
func (u *ShareUseCase) CreateShare(ctx context.Context, ident *domain.Identity, fileID uuid.UUID, granteeEmail string, perm entity.Permission, expiresAt *time.Time) (*entity.Share, error) {
	if !perm.Valid() {
		return nil, fmt.Errorf("%w: unknown permission", domain.ErrInvalidArgument)
	}
	if err := u.validateExpiry(expiresAt); err != nil {
		return nil, err
	}
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionOwner, u.clock.Now())
	if err != nil {
		return nil, err
	}
	if f.IsDeleted {
		return nil, domain.ErrNotFound
	}

	grantee, err := u.users.GetByEmail(ctx, strings.ToLower(strings.TrimSpace(granteeEmail)))
	if err != nil {
		return nil, fmt.Errorf("%w: no such user", domain.ErrNotFound)
	}
	if grantee.ID == f.OwnerID {
		return nil, fmt.Errorf("%w: cannot share a file with its owner", domain.ErrInvalidArgument)
	}

	share := &entity.Share{
		ID:         uuid.New(),
		FileID:     f.ID,
		OwnerID:    ident.UserID,
		GranteeID:  &grantee.ID,
		Permission: perm,
		ExpiresAt:  expiresAt,
	}
	if err := u.shares.Create(ctx, share); err != nil {
		if errIs(err, domain.ErrAlreadyExists) {
			return nil, fmt.Errorf("%w: user already has access to this file", domain.ErrAlreadyExists)
		}
		return nil, err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditShareCreated, "share", share.ID.String(),
		map[string]any{"file_id": f.ID.String(), "grantee_id": grantee.ID.String(), "permission": string(perm)})
	return share, nil
}

// CreatePublicLink mints a tokenized link with optional expiry and password.
// The raw token is returned exactly once; only its SHA-256 is stored.
func (u *ShareUseCase) CreatePublicLink(ctx context.Context, ident *domain.Identity, fileID uuid.UUID, perm entity.Permission, expiresAt *time.Time, password string) (*entity.Share, string, error) {
	// Public links are read-only or editor; granting "owner" to the world
	// is never sensible.
	if perm != entity.PermissionViewer && perm != entity.PermissionEditor {
		return nil, "", fmt.Errorf("%w: public links support viewer or editor permission", domain.ErrInvalidArgument)
	}
	if err := u.validateExpiry(expiresAt); err != nil {
		return nil, "", err
	}
	f, err := authorizeFile(ctx, u.files, u.shares, ident, fileID, entity.PermissionOwner, u.clock.Now())
	if err != nil {
		return nil, "", err
	}
	if f.IsDeleted {
		return nil, "", domain.ErrNotFound
	}

	token, err := crypto.RandomToken(32)
	if err != nil {
		return nil, "", err
	}
	var passwordHash *string
	if password != "" {
		if err := validatePassword(password); err != nil {
			return nil, "", err
		}
		h, err := crypto.HashPassword(password, crypto.DefaultArgon2Params)
		if err != nil {
			return nil, "", err
		}
		passwordHash = &h
	}

	share := &entity.Share{
		ID:           uuid.New(),
		FileID:       f.ID,
		OwnerID:      ident.UserID,
		TokenHash:    crypto.HashToken(token),
		Permission:   perm,
		PasswordHash: passwordHash,
		ExpiresAt:    expiresAt,
	}
	if err := u.shares.Create(ctx, share); err != nil {
		return nil, "", err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditShareCreated, "share", share.ID.String(),
		map[string]any{"file_id": f.ID.String(), "public": true, "permission": string(perm)})
	return share, token, nil
}

// List pages shares created by the caller, optionally scoped to one file.
func (u *ShareUseCase) List(ctx context.Context, ident *domain.Identity, fileID *uuid.UUID, cur pagination.Cursor, limit int) ([]*entity.Share, error) {
	return u.shares.ListByOwner(ctx, ident.UserID, fileID, cur, limit)
}

// Revoke deactivates a share. Only its creator or an admin may revoke.
func (u *ShareUseCase) Revoke(ctx context.Context, ident *domain.Identity, shareID uuid.UUID) error {
	share, err := u.shares.GetByID(ctx, shareID)
	if err != nil {
		return domain.ErrNotFound
	}
	if share.OwnerID != ident.UserID && ident.Role != entity.RoleAdmin {
		return domain.ErrNotFound // hide existence from non-owners
	}
	if err := u.shares.Revoke(ctx, shareID); err != nil {
		if errIs(err, domain.ErrNotFound) {
			return nil // already revoked: idempotent
		}
		return err
	}
	u.auditor.Record(ctx, &ident.UserID, entity.AuditShareRevoked, "share", shareID.String(), nil)
	return nil
}

// ResolvePublicLink validates a public-link token (and password, when set)
// and returns the shared file. Anonymous access is audited.
func (u *ShareUseCase) ResolvePublicLink(ctx context.Context, token, password string) (*entity.File, *entity.Share, error) {
	share, err := u.shares.GetByTokenHash(ctx, crypto.HashToken(token))
	if err != nil {
		return nil, nil, domain.ErrNotFound
	}
	if !share.IsActive(u.clock.Now()) {
		return nil, nil, domain.ErrNotFound
	}
	if share.PasswordHash != nil {
		ok, err := crypto.VerifyPassword(password, *share.PasswordHash)
		if err != nil || !ok {
			return nil, nil, domain.ErrPasswordRequired
		}
	}
	f, err := u.files.GetByID(ctx, share.FileID)
	if err != nil || f.IsDeleted {
		return nil, nil, domain.ErrNotFound
	}
	u.auditor.Record(ctx, nil, entity.AuditShareAccessed, "share", share.ID.String(),
		map[string]any{"file_id": f.ID.String()})
	return f, share, nil
}

func (u *ShareUseCase) validateExpiry(expiresAt *time.Time) error {
	if expiresAt != nil && expiresAt.Before(u.clock.Now()) {
		return fmt.Errorf("%w: expiry is in the past", domain.ErrInvalidArgument)
	}
	return nil
}
