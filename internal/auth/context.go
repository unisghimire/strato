package auth

import (
	"context"

	"github.com/unisghimire/strato/internal/domain"
)

type identityKey struct{}

// WithIdentity attaches the authenticated principal to the context. Only the
// auth middleware calls this; handlers and use cases read it.
func WithIdentity(ctx context.Context, id *domain.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns the authenticated principal, or
// domain.ErrUnauthenticated if the request never passed the auth middleware.
func IdentityFromContext(ctx context.Context) (*domain.Identity, error) {
	id, ok := ctx.Value(identityKey{}).(*domain.Identity)
	if !ok || id == nil {
		return nil, domain.ErrUnauthenticated
	}
	return id, nil
}

// RequestMeta carries transport facts (client IP, user agent) into the
// use-case layer for audit logging, without use cases importing transport.
type RequestMeta struct {
	IP        string
	UserAgent string
}

type metaKey struct{}

// WithRequestMeta attaches request metadata; called by middleware.
func WithRequestMeta(ctx context.Context, m RequestMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// RequestMetaFromContext returns request metadata, zero-valued if absent.
func RequestMetaFromContext(ctx context.Context) RequestMeta {
	m, _ := ctx.Value(metaKey{}).(RequestMeta)
	return m
}
