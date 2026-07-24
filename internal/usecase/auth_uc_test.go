package usecase_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// bytesReader avoids importing bytes in every test file.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func TestRegisterCreatesUserAndQuota(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	user, err := e.authUC.Register(ctx, "Alice@Example.com", "a-long-password", "Alice")
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", user.Email, "email must be normalized")

	quota, err := e.quotas.Get(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(testQuota), quota.QuotaBytes)
	assert.Zero(t, quota.UsedBytes)
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	_, err := e.authUC.Register(ctx, "dup@example.com", "a-long-password", "")
	require.NoError(t, err)
	_, err = e.authUC.Register(ctx, "DUP@example.com", "another-long-password", "")
	assert.ErrorIs(t, err, domain.ErrAlreadyExists)
}

func TestRegisterValidatesInput(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	_, err := e.authUC.Register(ctx, "not-an-email", "a-long-password", "")
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)

	_, err = e.authUC.Register(ctx, "ok@example.com", "short", "")
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
}

func TestLoginIssuesTokenPair(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.authUC.Register(ctx, "user@example.com", "a-long-password", "")
	require.NoError(t, err)

	pair, err := e.authUC.Login(ctx, "user@example.com", "a-long-password")
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	assert.True(t, pair.AccessExpiresAt.After(e.clock.Now()))
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.authUC.Register(ctx, "user@example.com", "a-long-password", "")
	require.NoError(t, err)

	_, err = e.authUC.Login(ctx, "user@example.com", "wrong-password!")
	assert.ErrorIs(t, err, domain.ErrUnauthenticated)

	_, err = e.authUC.Login(ctx, "ghost@example.com", "a-long-password")
	assert.ErrorIs(t, err, domain.ErrUnauthenticated,
		"unknown email must be indistinguishable from wrong password")
}

func TestRefreshRotatesToken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.authUC.Register(ctx, "user@example.com", "a-long-password", "")
	require.NoError(t, err)
	pair, err := e.authUC.Login(ctx, "user@example.com", "a-long-password")
	require.NoError(t, err)

	rotated, err := e.authUC.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, pair.RefreshToken, rotated.RefreshToken, "refresh token must rotate")

	// The new token works.
	_, err = e.authUC.Refresh(ctx, rotated.RefreshToken)
	require.NoError(t, err)
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.authUC.Register(ctx, "user@example.com", "a-long-password", "")
	require.NoError(t, err)
	pair, err := e.authUC.Login(ctx, "user@example.com", "a-long-password")
	require.NoError(t, err)

	rotated, err := e.authUC.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)

	// Replay of the rotated-out token = theft signal.
	_, err = e.authUC.Refresh(ctx, pair.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrTokenReuse)

	// The whole family — including the "legitimate" successor — is dead.
	_, err = e.authUC.Refresh(ctx, rotated.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrUnauthenticated)

	assert.Contains(t, e.audit.Actions(), entity.AuditTokenReuse)
}

func TestLogoutRevokesSessionFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.authUC.Register(ctx, "user@example.com", "a-long-password", "")
	require.NoError(t, err)
	pair, err := e.authUC.Login(ctx, "user@example.com", "a-long-password")
	require.NoError(t, err)

	require.NoError(t, e.authUC.Logout(ctx, pair.RefreshToken))

	_, err = e.authUC.Refresh(ctx, pair.RefreshToken)
	assert.Error(t, err, "refresh after logout must fail")

	// Idempotent.
	assert.NoError(t, e.authUC.Logout(ctx, pair.RefreshToken))
}

func TestExpiredRefreshTokenRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.authUC.Register(ctx, "user@example.com", "a-long-password", "")
	require.NoError(t, err)
	pair, err := e.authUC.Login(ctx, "user@example.com", "a-long-password")
	require.NoError(t, err)

	e.clock.Advance(721 * time.Hour) // past RefreshTokenTTL
	_, err = e.authUC.Refresh(ctx, pair.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrUnauthenticated)
}
