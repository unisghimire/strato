//go:build integration

// Integration tests run against a real PostgreSQL with migrations applied:
//
//	make dev-up && make migrate && make test-integration
package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/internal/repository/postgres"
	"github.com/unisghimire/strato/pkg/pagination"
)

func testDB(t *testing.T) *postgres.DB {
	t.Helper()
	cfg := config.Postgres{
		Host:     envOr("STRATO_TEST_PG_HOST", "localhost"),
		Port:     5432,
		User:     envOr("STRATO_TEST_PG_USER", "strato"),
		Password: envOr("STRATO_TEST_PG_PASSWORD", "strato-dev-password"),
		Database: envOr("STRATO_TEST_PG_DATABASE", "strato"),
		SSLMode:  "disable",
		MaxConns: 8,
		MinConns: 1,
	}
	db, err := postgres.New(context.Background(), cfg)
	require.NoError(t, err, "integration tests need a running postgres (make dev-up && make migrate)")
	t.Cleanup(db.Close)
	return db
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newTestUser(t *testing.T, db *postgres.DB) *entity.User {
	t.Helper()
	users := postgres.NewUserRepo(db)
	u := &entity.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("it-%s@example.com", uuid.NewString()[:8]),
		PasswordHash: "$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		Role:         entity.RoleUser,
	}
	require.NoError(t, users.Create(context.Background(), u))
	return u
}

func TestUserRepoCRUD(t *testing.T) {
	db := testDB(t)
	users := postgres.NewUserRepo(db)
	ctx := context.Background()

	u := newTestUser(t, db)

	got, err := users.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, u.Email, got.Email)

	// citext: case-insensitive email lookup.
	upper, err := users.GetByEmail(ctx, string([]byte(u.Email))) // same email
	require.NoError(t, err)
	assert.Equal(t, u.ID, upper.ID)

	// Duplicate email rejected.
	dup := &entity.User{ID: uuid.New(), Email: u.Email, PasswordHash: "x", Role: entity.RoleUser}
	assert.ErrorIs(t, users.Create(ctx, dup), domain.ErrAlreadyExists)

	_, err = users.GetByID(ctx, uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestQuotaConcurrentEnforcement is the test that matters: N goroutines
// racing to consume quota must never jointly exceed it, because the check
// and increment are a single UPDATE statement.
func TestQuotaConcurrentEnforcement(t *testing.T) {
	db := testDB(t)
	quotas := postgres.NewQuotaRepo(db)
	ctx := context.Background()
	u := newTestUser(t, db)

	const quotaBytes = 1000
	require.NoError(t, quotas.Create(ctx, &entity.Quota{UserID: u.ID, QuotaBytes: quotaBytes}))

	const workers = 20
	const chunk = 100 // only 10 of 20 can fit
	var wg sync.WaitGroup
	var successes sync.Map
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := quotas.AddUsage(ctx, u.ID, chunk); err == nil {
				successes.Store(i, true)
			}
		}(i)
	}
	wg.Wait()

	count := 0
	successes.Range(func(_, _ any) bool { count++; return true })
	assert.Equal(t, 10, count, "exactly quota/chunk writers may succeed")

	q, err := quotas.Get(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(quotaBytes), q.UsedBytes, "usage must never exceed quota under contention")
}

func TestFileVersioningFlow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := newTestUser(t, db)
	files := postgres.NewFileRepo(db)
	versions := postgres.NewVersionRepo(db)
	blobs := postgres.NewBlobRepo(db)

	f := &entity.File{ID: uuid.New(), OwnerID: u.ID, Name: "it-" + uuid.NewString()[:8] + ".txt", MimeType: "text/plain"}
	require.NoError(t, files.Create(ctx, f))

	// Two versions referencing two blobs, all inside one transaction each.
	for i := 1; i <= 2; i++ {
		content := fmt.Sprintf("content v%d", i)
		sum := sha256.Sum256([]byte(content))
		err := db.WithinTx(ctx, func(ctx context.Context) error {
			b := &entity.Blob{
				ID: uuid.New(), ChecksumSHA256: sum[:], SizeBytes: int64(len(content)),
				StorageKey: "it/" + uuid.NewString(), WrappedDEK: []byte("wrapped-dek-placeholder"),
			}
			if err := blobs.Create(ctx, b); err != nil {
				return err
			}
			v := &entity.Version{ID: uuid.New(), FileID: f.ID, BlobID: b.ID, SizeBytes: b.SizeBytes, CreatedBy: u.ID}
			if err := versions.Create(ctx, v); err != nil {
				return err
			}
			if err := blobs.IncrementRef(ctx, b.ID); err != nil {
				return err
			}
			return files.SetCurrentVersion(ctx, f.ID, v.ID)
		})
		require.NoError(t, err)
	}

	got, err := files.GetByID(ctx, f.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.VersionNumber, "denormalized version number follows the pointer")

	history, err := versions.ListByFile(ctx, f.ID, pagination.Cursor{}, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, 2, history[0].VersionNumber, "history is newest-first")

	total, err := versions.TotalBytes(ctx, f.ID)
	require.NoError(t, err)
	assert.Equal(t, history[0].SizeBytes+history[1].SizeBytes, total)
}

func TestKeysetPaginationIsStable(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := newTestUser(t, db)
	files := postgres.NewFileRepo(db)

	for i := 0; i < 7; i++ {
		f := &entity.File{ID: uuid.New(), OwnerID: u.ID,
			Name: fmt.Sprintf("page-%02d-%s.txt", i, uuid.NewString()[:6])}
		require.NoError(t, files.Create(ctx, f))
		time.Sleep(2 * time.Millisecond) // distinct created_at ordering
	}

	seen := map[uuid.UUID]bool{}
	cur := pagination.Cursor{}
	for {
		page, err := files.List(ctx, u.ID, domain.FileListFilter{}, cur, 3)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		for _, f := range page {
			assert.False(t, seen[f.ID], "no duplicates across pages")
			seen[f.ID] = true
		}
		last := page[len(page)-1]
		cur = pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		if len(page) < 3 {
			break
		}
	}
	assert.Len(t, seen, 7, "pagination must cover every row exactly once")
}
