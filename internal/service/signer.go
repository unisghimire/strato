// Package service holds infrastructure-flavored application services that
// don't fit a repository: URL signing and background garbage collection.
package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/domain"
)

// URLSigner mints application-layer signed download URLs.
//
// Why not S3 presigned URLs for downloads: blobs are encrypted at rest with
// per-blob DEKs, so raw object bytes are useless to a client — downloads
// must stream through the API for decryption. The signed URL therefore
// authorizes a short-lived, credential-free GET against the API itself
// (shareable into browsers, curl, download managers), and the API keeps
// enforcing decryption and audit on the way through.
type URLSigner struct {
	secret []byte
	clock  domain.Clock
}

// NewURLSigner constructs a signer. secret should be high-entropy and can be
// rotated independently of the JWT secret.
func NewURLSigner(secret []byte, clock domain.Clock) *URLSigner {
	return &URLSigner{secret: secret, clock: clock}
}

// Sign produces query parameters authorizing userID to download fileID until
// expiry. The signature binds file, user, and expiry; none can be swapped.
func (s *URLSigner) Sign(fileID, userID uuid.UUID, ttl time.Duration) url.Values {
	exp := s.clock.Now().Add(ttl).Unix()
	v := url.Values{}
	v.Set("uid", userID.String())
	v.Set("exp", strconv.FormatInt(exp, 10))
	v.Set("sig", s.signature(fileID, userID, exp))
	return v
}

// Verify checks a signed query against fileID and returns the authorized
// user. All failures collapse to ErrUnauthenticated.
func (s *URLSigner) Verify(fileID uuid.UUID, query url.Values) (uuid.UUID, error) {
	userID, err := uuid.Parse(query.Get("uid"))
	if err != nil {
		return uuid.Nil, domain.ErrUnauthenticated
	}
	exp, err := strconv.ParseInt(query.Get("exp"), 10, 64)
	if err != nil || s.clock.Now().Unix() > exp {
		return uuid.Nil, domain.ErrUnauthenticated
	}
	expected := s.signature(fileID, userID, exp)
	if !hmac.Equal([]byte(expected), []byte(query.Get("sig"))) {
		return uuid.Nil, domain.ErrUnauthenticated
	}
	return userID, nil
}

func (s *URLSigner) signature(fileID, userID uuid.UUID, exp int64) string {
	mac := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(mac, "%s|%s|%d", fileID, userID, exp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
