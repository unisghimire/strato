package usecase_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/domain"
	pag "github.com/unisghimire/strato/pkg/pagination"
)

func pagination() pag.Cursor { return pag.Cursor{} }

func TestFileAccessIsIsolatedBetweenUsers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	mallory := e.registerUser(t, "mallory@example.com")

	fileID := uuid.MustParse(e.uploadFile(t, alice, "private.txt", dummyContent(100)))

	// A stranger probing the ID gets NotFound — never PermissionDenied,
	// which would confirm the file exists (IDOR).
	_, err := e.fileUC.Get(ctx, mallory, fileID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, _, err = e.fileUC.OpenDownload(ctx, mallory, fileID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	err = e.fileUC.Delete(ctx, mallory, fileID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestDeleteAndRestore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "doc.txt", dummyContent(100)))

	require.NoError(t, e.fileUC.Delete(ctx, alice, fileID))

	// Deleted files don't show in listings...
	files, err := e.fileUC.List(ctx, alice, nil, false, false, pagination(), 50)
	require.NoError(t, err)
	assert.Empty(t, files)

	// ...but the owner can still see them with include_deleted and restore.
	files, err = e.fileUC.List(ctx, alice, nil, true, false, pagination(), 50)
	require.NoError(t, err)
	require.Len(t, files, 1)

	restored, err := e.fileUC.Restore(ctx, alice, fileID)
	require.NoError(t, err)
	assert.False(t, restored.IsDeleted)
}

func TestRenameValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "old.txt", dummyContent(10)))

	_, err := e.fileUC.Rename(ctx, alice, fileID, "../../etc/passwd")
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)

	f, err := e.fileUC.Rename(ctx, alice, fileID, "new.txt")
	require.NoError(t, err)
	assert.Equal(t, "new.txt", f.Name)
}

func TestLockingBlocksOtherEditors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	bob := e.registerUser(t, "bob@example.com")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "contested.txt", dummyContent(10)))

	// Give Bob editor access, then Bob locks the file.
	_, err := e.shareUC.CreateShare(ctx, alice, fileID, "bob@example.com", "editor", nil)
	require.NoError(t, err)
	require.NoError(t, e.fileUC.Lock(ctx, bob, fileID))

	// Alice (owner) is blocked from mutating while Bob holds the lock.
	_, err = e.fileUC.Rename(ctx, alice, fileID, "renamed.txt")
	assert.ErrorIs(t, err, domain.ErrFileLocked)

	// But the owner may forcibly unlock.
	require.NoError(t, e.fileUC.Unlock(ctx, alice, fileID))
	_, err = e.fileUC.Rename(ctx, alice, fileID, "renamed.txt")
	assert.NoError(t, err)
}

func TestVersionRestoreIsAppendOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")

	v1Content := []byte("version one contents")
	fileID := uuid.MustParse(e.uploadFile(t, alice, "doc.txt", v1Content))
	e.uploadFile(t, alice, "doc.txt", []byte("version two, rather different"))

	versions, err := e.fileUC.ListVersions(ctx, alice, fileID, pagination(), 10)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	v1 := versions[1] // newest-first ordering

	f, err := e.fileUC.RestoreVersion(ctx, alice, fileID, v1.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, f.VersionNumber, "restore appends a new version, never rewrites history")
	assert.Equal(t, int64(len(v1Content)), f.SizeBytes)
}

func TestFolderLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")

	folder, err := e.fileUC.CreateFolder(ctx, alice, "Documents", nil)
	require.NoError(t, err)

	child, err := e.fileUC.CreateFolder(ctx, alice, "Taxes", &folder.ID)
	require.NoError(t, err)

	// Non-empty folders refuse deletion.
	err = e.fileUC.DeleteFolder(ctx, alice, folder.ID)
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)

	require.NoError(t, e.fileUC.DeleteFolder(ctx, alice, child.ID))
	require.NoError(t, e.fileUC.DeleteFolder(ctx, alice, folder.ID))
}

func TestSearchFindsBySubstring(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	e.uploadFile(t, alice, "Quarterly Report Q3.pdf", dummyContent(10))
	e.uploadFile(t, alice, "vacation-photo.jpg", dummyContent(20))

	hits, err := e.fileUC.Search(ctx, alice, "report", "", pagination(), 50)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "Quarterly Report Q3.pdf", hits[0].Name)
}
