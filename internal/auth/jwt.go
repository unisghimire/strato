// Package auth implements token issuance/validation and the request identity
// context. It satisfies the domain.TokenIssuer port.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/entity"
)

// JWTManager mints and validates HMAC-SHA256 access tokens. Symmetric
// signing is appropriate while issuer and verifier are the same service;
// moving verification to an API gateway would motivate RS256/EdDSA instead.
type JWTManager struct {
	secret []byte
	issuer string
	ttl    time.Duration
	clock  domain.Clock
}

// NewJWTManager constructs a JWTManager.
func NewJWTManager(cfg config.Auth, clock domain.Clock) *JWTManager {
	return &JWTManager{
		secret: []byte(cfg.JWTSecret),
		issuer: cfg.Issuer,
		ttl:    cfg.AccessTokenTTL,
		clock:  clock,
	}
}

var _ domain.TokenIssuer = (*JWTManager)(nil)

type claims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
	Role  string `json:"role"`
}

// IssueAccessToken mints a short-lived access token for the user.
func (m *JWTManager) IssueAccessToken(user *entity.User) (string, time.Time, error) {
	now := m.clock.Now()
	expiresAt := now.Add(m.ttl)
	c := claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(),
		},
		Email: user.Email,
		Role:  string(user.Role),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing token: %w", err)
	}
	return token, expiresAt, nil
}

// ParseAccessToken validates signature, algorithm, issuer, and expiry, and
// returns the embedded identity. All failures collapse to
// domain.ErrUnauthenticated — callers get no oracle about why.
func (m *JWTManager) ParseAccessToken(tokenStr string) (*domain.Identity, error) {
	var c claims
	_, err := jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		// Pin the algorithm: accepting whatever the header claims enables
		// alg-confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithExpirationRequired(), jwt.WithTimeFunc(m.clock.Now))
	if err != nil {
		return nil, domain.ErrUnauthenticated
	}
	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return nil, domain.ErrUnauthenticated
	}
	return &domain.Identity{
		UserID: userID,
		Email:  c.Email,
		Role:   entity.Role(c.Role),
	}, nil
}
