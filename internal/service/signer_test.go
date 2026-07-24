package service

import (
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/mocks"
)

func TestURLSignerRoundTrip(t *testing.T) {
	clock := &mocks.FixedClock{T: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	s := NewURLSigner([]byte("secret"), clock)
	fileID, userID := uuid.New(), uuid.New()

	q := s.Sign(fileID, userID, 15*time.Minute)
	got, err := s.Verify(fileID, q)
	require.NoError(t, err)
	assert.Equal(t, userID, got)
}

func TestURLSignerRejectsExpired(t *testing.T) {
	clock := &mocks.FixedClock{T: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	s := NewURLSigner([]byte("secret"), clock)
	fileID, userID := uuid.New(), uuid.New()

	q := s.Sign(fileID, userID, time.Minute)
	clock.Advance(2 * time.Minute)
	_, err := s.Verify(fileID, q)
	assert.ErrorIs(t, err, domain.ErrUnauthenticated)
}

func TestURLSignerBindsAllFields(t *testing.T) {
	clock := &mocks.FixedClock{T: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	s := NewURLSigner([]byte("secret"), clock)
	fileID, userID := uuid.New(), uuid.New()
	q := s.Sign(fileID, userID, 15*time.Minute)

	t.Run("different file id", func(t *testing.T) {
		_, err := s.Verify(uuid.New(), q)
		assert.ErrorIs(t, err, domain.ErrUnauthenticated)
	})

	t.Run("swapped user id", func(t *testing.T) {
		tampered, _ := url.ParseQuery(q.Encode())
		tampered.Set("uid", uuid.NewString())
		_, err := s.Verify(fileID, tampered)
		assert.ErrorIs(t, err, domain.ErrUnauthenticated)
	})

	t.Run("extended expiry", func(t *testing.T) {
		tampered, _ := url.ParseQuery(q.Encode())
		tampered.Set("exp", "99999999999")
		_, err := s.Verify(fileID, tampered)
		assert.ErrorIs(t, err, domain.ErrUnauthenticated)
	})

	t.Run("different signing key", func(t *testing.T) {
		other := NewURLSigner([]byte("other-secret"), clock)
		_, err := other.Verify(fileID, q)
		assert.ErrorIs(t, err, domain.ErrUnauthenticated)
	})
}
