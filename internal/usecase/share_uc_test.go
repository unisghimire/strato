package usecase_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

func TestPrivateShareGrantsAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	bob := e.registerUser(t, "bob@example.com")
	content := dummyContent(300)
	fileID := uuid.MustParse(e.uploadFile(t, alice, "shared.txt", content))

	// Before sharing: nothing.
	_, err := e.fileUC.Get(ctx, bob, fileID)
	require.ErrorIs(t, err, domain.ErrNotFound)

	_, err = e.shareUC.CreateShare(ctx, alice, fileID, "bob@example.com", entity.PermissionViewer, nil)
	require.NoError(t, err)

	// Viewer: read + download OK...
	rc, _, err := e.fileUC.OpenDownload(ctx, bob, fileID)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// ...but no write access.
	_, err = e.fileUC.Rename(ctx, bob, fileID, "hijacked.txt")
	assert.ErrorIs(t, err, domain.ErrPermissionDenied)
}

func TestShareRevocationCutsAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	bob := e.registerUser(t, "bob@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "temp.txt", dummyContent(10)))

	share, err := e.shareUC.CreateShare(ctx, alice, fileID, "bob@example.com", entity.PermissionViewer, nil)
	require.NoError(t, err)
	require.NoError(t, e.shareUC.Revoke(ctx, alice, share.ID))

	_, err = e.fileUC.Get(ctx, bob, fileID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestShareExpiryCutsAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	bob := e.registerUser(t, "bob@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "timed.txt", dummyContent(10)))

	expiry := e.clock.Now().Add(time.Hour)
	_, err := e.shareUC.CreateShare(ctx, alice, fileID, "bob@example.com", entity.PermissionViewer, &expiry)
	require.NoError(t, err)

	_, err = e.fileUC.Get(ctx, bob, fileID)
	require.NoError(t, err, "access works before expiry")

	e.clock.Advance(2 * time.Hour)
	_, err = e.fileUC.Get(ctx, bob, fileID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "access dies at expiry")
}

func TestPublicLinkFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "public.txt", dummyContent(50)))

	share, token, err := e.shareUC.CreatePublicLink(ctx, alice, fileID, entity.PermissionViewer, nil, "")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.True(t, share.IsPublicLink())

	f, _, err := e.shareUC.ResolvePublicLink(ctx, token, "")
	require.NoError(t, err)
	assert.Equal(t, fileID, f.ID)

	_, _, err = e.shareUC.ResolvePublicLink(ctx, "wrong-token", "")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPasswordProtectedPublicLink(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "gated.txt", dummyContent(50)))

	_, token, err := e.shareUC.CreatePublicLink(ctx, alice, fileID, entity.PermissionViewer, nil, "link-password-1")
	require.NoError(t, err)

	_, _, err = e.shareUC.ResolvePublicLink(ctx, token, "")
	assert.ErrorIs(t, err, domain.ErrPasswordRequired)
	_, _, err = e.shareUC.ResolvePublicLink(ctx, token, "wrong-password!")
	assert.ErrorIs(t, err, domain.ErrPasswordRequired)

	f, _, err := e.shareUC.ResolvePublicLink(ctx, token, "link-password-1")
	require.NoError(t, err)
	assert.Equal(t, fileID, f.ID)
}

func TestPublicLinkRejectsOwnerPermission(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "x.txt", dummyContent(10)))

	_, _, err := e.shareUC.CreatePublicLink(ctx, alice, fileID, entity.PermissionOwner, nil, "")
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
}

func TestOnlyOwnerLevelCanShare(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	bob := e.registerUser(t, "bob@example.com")
	carol := e.registerUser(t, "carol@example.com")
	_ = carol
	fileID := uuid.MustParse(e.uploadFile(t, alice, "y.txt", dummyContent(10)))

	// Bob holds only editor: re-sharing must fail.
	_, err := e.shareUC.CreateShare(ctx, alice, fileID, "bob@example.com", entity.PermissionEditor, nil)
	require.NoError(t, err)
	_, err = e.shareUC.CreateShare(ctx, bob, fileID, "carol@example.com", entity.PermissionViewer, nil)
	assert.ErrorIs(t, err, domain.ErrPermissionDenied)
}
