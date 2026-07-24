// Package pagination implements opaque keyset cursors.
//
// Offset pagination degrades linearly (OFFSET 1000000 scans a million rows)
// and skips/duplicates items under concurrent writes. Keyset cursors seek
// directly via an indexed (created_at, id) tuple: O(log n) at any depth and
// stable under insertion.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// DefaultPageSize and MaxPageSize bound list responses.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ErrInvalidCursor is returned for undecodable or malformed page tokens.
var ErrInvalidCursor = errors.New("pagination: invalid page token")

// Cursor is the keyset position: the sort key and tiebreaker id of the last
// item on the previous page.
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

// Encode serializes the cursor into an opaque URL-safe token. Clients must
// treat it as opaque; the format is not part of the API contract.
func (c Cursor) Encode() string {
	raw, _ := json.Marshal(c) // Cursor cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Decode parses a page token. An empty token yields a zero Cursor (first
// page) and no error.
func Decode(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}

// IsZero reports whether the cursor addresses the first page.
func (c Cursor) IsZero() bool {
	return c.CreatedAt.IsZero() && c.ID == uuid.Nil
}

// ClampPageSize normalizes a client-supplied page size into [1, MaxPageSize].
func ClampPageSize(n int32) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return int(n)
	}
}
