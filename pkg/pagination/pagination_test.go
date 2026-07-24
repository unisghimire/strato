package pagination

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{CreatedAt: time.Date(2026, 7, 25, 12, 0, 0, 123456789, time.UTC), ID: uuid.New()}
	got, err := Decode(c.Encode())
	require.NoError(t, err)
	assert.True(t, c.CreatedAt.Equal(got.CreatedAt))
	assert.Equal(t, c.ID, got.ID)
}

func TestDecodeEmptyIsFirstPage(t *testing.T) {
	c, err := Decode("")
	require.NoError(t, err)
	assert.True(t, c.IsZero())
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"not base64 !!!", "aGVsbG8", "eyJicm9rZW4"} {
		_, err := Decode(bad)
		assert.ErrorIs(t, err, ErrInvalidCursor, "input: %q", bad)
	}
}

func TestClampPageSize(t *testing.T) {
	assert.Equal(t, DefaultPageSize, ClampPageSize(0))
	assert.Equal(t, DefaultPageSize, ClampPageSize(-5))
	assert.Equal(t, 25, ClampPageSize(25))
	assert.Equal(t, MaxPageSize, ClampPageSize(10_000))
}
