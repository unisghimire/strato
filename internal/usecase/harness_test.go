package usecase_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/mocks"
	"github.com/unisghimire/strato/internal/service"
	"github.com/unisghimire/strato/internal/usecase"
)

// env wires every use case against in-memory fakes — a complete, fast,
// deterministic application core for tests.
type env struct {
	users    *mocks.UserRepo
	sessions *mocks.SessionRepo
	quotas   *mocks.QuotaRepo
	folders  *mocks.FolderRepo
	files    *mocks.FileRepo
	versions *mocks.VersionRepo
	blobs    *mocks.BlobRepo
	uploads  *mocks.UploadRepo
	shares   *mocks.ShareRepo
	audit    *mocks.AuditRepo
	store    *mocks.BlobStore
	clock    *mocks.FixedClock

	authUC   *usecase.AuthUseCase
	fileUC   *usecase.FileUseCase
	uploadUC *usecase.UploadUseCase
	shareUC  *usecase.ShareUseCase
}

const (
	testChunkSize = 1024
	testQuota     = 1 << 20 // 1 MiB
)

var testKEK = make([]byte, 32) // zero key is fine for tests

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		users:    mocks.NewUserRepo(),
		sessions: mocks.NewSessionRepo(),
		quotas:   mocks.NewQuotaRepo(),
		blobs:    mocks.NewBlobRepo(),
		uploads:  mocks.NewUploadRepo(),
		shares:   mocks.NewShareRepo(),
		audit:    mocks.NewAuditRepo(),
		store:    mocks.NewBlobStore(),
		clock:    &mocks.FixedClock{T: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)},
	}
	e.versions = mocks.NewVersionRepo(e.blobs)
	e.files = mocks.NewFileRepo(e.versions, e.blobs)
	e.folders = mocks.NewFolderRepo(e.files)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditor := usecase.NewAuditor(e.audit, log)
	tx := mocks.NopTx{}
	tokens := auth.NewJWTManager(config.Auth{
		JWTSecret:      "test-secret-at-least-32-characters!!",
		Issuer:         "strato-test",
		AccessTokenTTL: 15 * time.Minute,
	}, e.clock)
	signer := service.NewURLSigner([]byte("test-signing-secret"), e.clock)

	var err error
	e.authUC, err = usecase.NewAuthUseCase(tx, e.users, e.sessions, e.quotas, tokens, auditor, e.clock,
		usecase.AuthConfig{RefreshTokenTTL: 720 * time.Hour, DefaultQuotaBytes: testQuota})
	require.NoError(t, err)

	e.fileUC = usecase.NewFileUseCase(tx, e.files, e.folders, e.versions, e.blobs, e.shares,
		e.quotas, e.store, signer, auditor, e.clock, testKEK,
		usecase.FileConfig{SignedURLTTL: 15 * time.Minute})

	e.uploadUC = usecase.NewUploadUseCase(tx, e.uploads, e.files, e.folders, e.versions, e.blobs,
		e.shares, e.quotas, e.store, mocks.NewLock(), auditor, e.clock, testKEK,
		usecase.UploadConfig{ChunkSize: testChunkSize, MaxFileSize: 10 << 20, SessionTTL: time.Hour})

	e.shareUC = usecase.NewShareUseCase(e.shares, e.files, e.users, auditor, e.clock)
	return e
}

// registerUser creates an account and returns its identity.
func (e *env) registerUser(t *testing.T, email string) *domain.Identity {
	t.Helper()
	user, err := e.authUC.Register(context.Background(), email, "sufficiently-long-password", "Test User")
	require.NoError(t, err)
	return &domain.Identity{UserID: user.ID, Email: user.Email, Role: user.Role}
}

// uploadFile drives the full chunked pipeline and returns the file ID as a
// string for convenience.
func (e *env) uploadFile(t *testing.T, ident *domain.Identity, name string, content []byte) string {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256(content)

	res, err := e.uploadUC.InitUpload(ctx, ident, name, "", "application/octet-stream",
		int64(len(content)), hex.EncodeToString(sum[:]))
	require.NoError(t, err)

	if !res.AlreadyExists {
		for i := 0; i < res.Session.TotalChunks; i++ {
			start := i * testChunkSize
			end := min(start+testChunkSize, len(content))
			_, err := e.uploadUC.UploadChunk(ctx, ident, res.Session.ID, i,
				bytesReader(content[start:end]))
			require.NoError(t, err)
		}
	}
	f, err := e.uploadUC.Complete(ctx, ident, res.Session.ID)
	require.NoError(t, err)
	return f.ID.String()
}

// dummyContent returns deterministic bytes of the given length.
func dummyContent(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 31)
	}
	return b
}
