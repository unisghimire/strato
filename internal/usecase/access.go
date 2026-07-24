package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// authorizeFile is the single authorization gate for file access, shared by
// the file, upload, and share use cases so policy cannot drift between them.
//
// IDOR defense: a caller with no relationship to the file gets ErrNotFound —
// indistinguishable from a nonexistent ID, so file IDs cannot be probed.
// ErrPermissionDenied is reserved for callers who legitimately know the file
// exists (they hold some grant) but asked for more than it allows.
func authorizeFile(
	ctx context.Context,
	files domain.FileRepository,
	shares domain.ShareRepository,
	ident *domain.Identity,
	fileID uuid.UUID,
	need entity.Permission,
	now time.Time,
) (*entity.File, error) {
	f, err := files.GetByID(ctx, fileID)
	if err != nil {
		return nil, domain.ErrNotFound
	}

	// Owners and admins hold full rights, including over trashed files.
	if f.OwnerID == ident.UserID || ident.Role == entity.RoleAdmin {
		return f, nil
	}

	// Grantees never see trashed files.
	if f.IsDeleted {
		return nil, domain.ErrNotFound
	}
	grant, err := shares.FindGrant(ctx, fileID, ident.UserID)
	if err != nil {
		return nil, domain.ErrNotFound
	}
	// Expiry/revocation are already filtered in SQL; re-checking here is
	// defense in depth.
	if !grant.IsActive(now) {
		return nil, domain.ErrNotFound
	}
	if !grant.Permission.Allows(need) {
		return nil, domain.ErrPermissionDenied
	}
	return f, nil
}

// validateName enforces object-name hygiene once for files and folders:
// length bounds, no path separators or traversal sequences, no control
// characters. This is the directory-traversal guard — names are only ever
// literal labels, never interpreted as paths anywhere in the system.
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 {
		return "", fmt.Errorf("%w: name must be 1-255 characters", domain.ErrInvalidArgument)
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("%w: reserved name", domain.ErrInvalidArgument)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("%w: name must not contain path separators", domain.ErrInvalidArgument)
	}
	for _, r := range name {
		if r < 0x20 {
			return "", fmt.Errorf("%w: name must not contain control characters", domain.ErrInvalidArgument)
		}
	}
	return name, nil
}

// parseOptionalID converts an optional wire ID ("" = absent) to *uuid.UUID.
func parseOptionalID(s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil //nolint:nilnil // absent is a valid, distinct state
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed id", domain.ErrInvalidArgument)
	}
	return &id, nil
}

// errIs reports whether err matches any of the target sentinels.
func errIs(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}
