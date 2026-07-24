package usecase_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/domain"
)

func TestUploadDownloadRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	content := dummyContent(3*testChunkSize + 100) // 4 chunks, last partial

	fileID := e.uploadFile(t, alice, "report.pdf", content)

	f, err := e.fileUC.Get(ctx, alice, uuid.MustParse(fileID))
	require.NoError(t, err)
	assert.Equal(t, "report.pdf", f.Name)
	assert.Equal(t, int64(len(content)), f.SizeBytes)
	assert.Equal(t, 1, f.VersionNumber)

	// Content survives the encrypt → store → decrypt pipeline byte-exact.
	rc, _, err := e.fileUC.OpenDownload(ctx, alice, f.ID)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// Quota was charged.
	quota, err := e.quotas.Get(ctx, alice.UserID)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), quota.UsedBytes)
}

func TestUploadCreatesNewVersionOnSameName(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")

	fileID := e.uploadFile(t, alice, "notes.txt", dummyContent(500))
	fileID2 := e.uploadFile(t, alice, "notes.txt", []byte("completely new contents of the file"))
	assert.Equal(t, fileID, fileID2, "same name must version the same file, not fork it")

	f, err := e.fileUC.Get(ctx, alice, uuid.MustParse(fileID))
	require.NoError(t, err)
	assert.Equal(t, 2, f.VersionNumber)

	versions, err := e.fileUC.ListVersions(ctx, alice, f.ID, pagination(), 10)
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}

func TestUploadDeduplicatesIdenticalContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	bob := e.registerUser(t, "bob@example.com")
	content := dummyContent(2 * testChunkSize)
	sum := sha256.Sum256(content)

	e.uploadFile(t, alice, "shared.bin", content)
	objectsAfterFirst := e.store.Len()

	// Bob initiates the same content: dedup fast path, zero chunk uploads.
	res, err := e.uploadUC.InitUpload(ctx, bob, "bobs-copy.bin", "", "application/octet-stream",
		int64(len(content)), hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	assert.True(t, res.AlreadyExists)

	f, err := e.uploadUC.Complete(ctx, bob, res.Session.ID)
	require.NoError(t, err)

	assert.Equal(t, objectsAfterFirst, e.store.Len(), "no new storage object for duplicate content")
	blob, err := e.blobs.GetByChecksum(ctx, sum[:])
	require.NoError(t, err)
	assert.Equal(t, 2, blob.RefCount, "both versions reference one blob")

	// Bob still downloads his logical copy fine.
	rc, _, err := e.fileUC.OpenDownload(ctx, bob, f.ID)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestCompleteRejectsMissingChunks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	content := dummyContent(3 * testChunkSize)
	sum := sha256.Sum256(content)

	res, err := e.uploadUC.InitUpload(ctx, alice, "partial.bin", "", "",
		int64(len(content)), hex.EncodeToString(sum[:]))
	require.NoError(t, err)

	// Only chunk 0 of 3 uploaded.
	_, err = e.uploadUC.UploadChunk(ctx, alice, res.Session.ID, 0, bytesReader(content[:testChunkSize]))
	require.NoError(t, err)

	_, err = e.uploadUC.Complete(ctx, alice, res.Session.ID)
	assert.ErrorIs(t, err, domain.ErrUploadIncomplete)

	// Resume: status names the received chunks so the client can fill gaps.
	_, received, err := e.uploadUC.Status(ctx, alice, res.Session.ID)
	require.NoError(t, err)
	assert.Equal(t, []int{0}, received)
}

func TestCompleteRejectsChecksumMismatch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	content := dummyContent(testChunkSize)
	wrongSum := sha256.Sum256([]byte("different content"))

	res, err := e.uploadUC.InitUpload(ctx, alice, "corrupt.bin", "", "",
		int64(len(content)), hex.EncodeToString(wrongSum[:]))
	require.NoError(t, err)
	_, err = e.uploadUC.UploadChunk(ctx, alice, res.Session.ID, 0, bytesReader(content))
	require.NoError(t, err)

	_, err = e.uploadUC.Complete(ctx, alice, res.Session.ID)
	assert.ErrorIs(t, err, domain.ErrChecksumMismatch)
}

func TestUploadEnforcesQuota(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")

	// Fill most of the quota, then try to exceed it.
	e.uploadFile(t, alice, "big1.bin", dummyContent(testQuota-2*testChunkSize))

	content := dummyContent(3 * testChunkSize)
	sum := sha256.Sum256(content)
	_, err := e.uploadUC.InitUpload(ctx, alice, "big2.bin", "", "",
		int64(len(content)), hex.EncodeToString(sum[:]))
	assert.ErrorIs(t, err, domain.ErrQuotaExceeded)
}

func TestUploadSessionIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	mallory := e.registerUser(t, "mallory@example.com")
	content := dummyContent(testChunkSize)
	sum := sha256.Sum256(content)

	res, err := e.uploadUC.InitUpload(ctx, alice, "secret.bin", "", "",
		int64(len(content)), hex.EncodeToString(sum[:]))
	require.NoError(t, err)

	// Another user cannot see, feed, or complete the session — and cannot
	// tell whether it exists (IDOR defense).
	_, _, err = e.uploadUC.Status(ctx, mallory, res.Session.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = e.uploadUC.UploadChunk(ctx, mallory, res.Session.ID, 0, bytesReader(content))
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = e.uploadUC.Complete(ctx, mallory, res.Session.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestInitUploadValidatesNames(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.registerUser(t, "alice@example.com")
	sum := sha256.Sum256([]byte("x"))
	checksum := hex.EncodeToString(sum[:])

	for _, name := range []string{"", "..", "a/b.txt", "a\\b.txt", "nul\x00byte", string(make([]byte, 300))} {
		_, err := e.uploadUC.InitUpload(ctx, alice, name, "", "", 1, checksum)
		assert.ErrorIs(t, err, domain.ErrInvalidArgument, "name %q must be rejected", name)
	}
}
