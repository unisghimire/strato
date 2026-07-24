// Package middleware provides gRPC interceptors and HTTP middleware:
// request metadata, panic recovery, structured logging, metrics, rate
// limiting, and authentication. Order matters and is fixed in Chain.
package middleware

import (
	"context"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/pkg/logger"
)

// publicMethods require no access token.
var publicMethods = map[string]bool{
	"/strato.v1.AuthService/Register":     true,
	"/strato.v1.AuthService/Login":        true,
	"/strato.v1.AuthService/RefreshToken": true,
	"/strato.v1.AuthService/Logout":       true,
}

// authRateLimited methods get the stricter (anti-credential-stuffing) budget.
var authRateLimited = map[string]bool{
	"/strato.v1.AuthService/Register": true,
	"/strato.v1.AuthService/Login":    true,
}

// RequestMeta extracts client IP, user agent, and request ID into the
// context for logging and audit.
func RequestMeta() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		meta := auth.RequestMeta{}
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			meta.IP = hostOnly(p.Addr.String())
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ua := md.Get("user-agent"); len(ua) > 0 {
				meta.UserAgent = ua[0]
			}
			// Honor proxy-forwarded client IP when present (gateway sets it).
			if fwd := md.Get("x-forwarded-for"); len(fwd) > 0 && fwd[0] != "" {
				meta.IP = strings.TrimSpace(strings.Split(fwd[0], ",")[0])
			}
		}
		return handler(auth.WithRequestMeta(ctx, meta), req)
	}
}

// Recovery converts panics into codes.Internal instead of tearing down the
// process, with a stack trace in the logs and never in the response.
func Recovery(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic recovered", "method", info.FullMethod,
					"panic", r, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// Logging emits one structured line per RPC with latency and status, and
// injects a request-scoped logger into the context.
func Logging(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		requestID := uuid.NewString()
		reqLog := log.With("request_id", requestID, "method", info.FullMethod)
		ctx = logger.WithContext(ctx, reqLog)

		start := time.Now()
		resp, err := handler(ctx, req)

		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		level := slog.LevelInfo
		if code == codes.Internal || code == codes.Unknown {
			level = slog.LevelError
		}
		reqLog.Log(ctx, level, "rpc",
			"code", code.String(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return resp, err
	}
}

// RateLimit enforces per-caller budgets: authenticated calls key on user ID,
// anonymous calls on client IP. Login/Register get the stricter budget.
// Redis failure fails open (logged) — availability over strictness.
func RateLimit(limiter domain.RateLimiter, cfg config.RateLimit, log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !cfg.Enabled {
			return handler(ctx, req)
		}
		key := callerKey(ctx)
		limit := cfg.RequestsPerMinute
		if authRateLimited[info.FullMethod] {
			key = "auth:" + key
			limit = cfg.AuthRequestsPerMinute
		}
		allowed, err := limiter.Allow(ctx, key, limit, time.Minute)
		if err != nil {
			log.Warn("rate limiter degraded, failing open", "error", err)
		}
		if !allowed {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// Authentication validates the Bearer token on non-public methods and
// attaches the identity to the context.
func Authentication(tokens domain.TokenIssuer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if publicMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		token, ok := bearerFromMD(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing bearer token")
		}
		ident, err := tokens.ParseAccessToken(token)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		return handler(auth.WithIdentity(ctx, ident), req)
	}
}

func bearerFromMD(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", false
	}
	const prefix = "bearer "
	if len(vals[0]) <= len(prefix) || !strings.EqualFold(vals[0][:len(prefix)], prefix) {
		return "", false
	}
	return vals[0][len(prefix):], true
}

// callerKey prefers the authenticated user, falling back to peer IP.
func callerKey(ctx context.Context) string {
	if token, ok := bearerFromMD(ctx); ok && len(token) > 16 {
		// Key on a token prefix rather than re-parsing: the auth interceptor
		// downstream still fully validates it.
		return "tok:" + token[:16]
	}
	return "ip:" + auth.RequestMetaFromContext(ctx).IP
}

func hostOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return strings.Trim(addr[:i], "[]")
	}
	return addr
}
