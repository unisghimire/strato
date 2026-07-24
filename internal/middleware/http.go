package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/pkg/logger"
)

// HTTP middleware mirrors the gRPC interceptors for the raw byte-transfer
// endpoints (chunk upload, download, public links) that bypass the gateway.

// HTTPRequestMeta attaches client IP/UA and a request-scoped logger.
func HTTPRequestMeta(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			ctx := auth.WithRequestMeta(r.Context(), auth.RequestMeta{
				IP:        ip,
				UserAgent: r.UserAgent(),
			})
			reqLog := log.With("request_id", uuid.NewString(), "path", r.URL.Path, "method", r.Method)
			ctx = logger.WithContext(ctx, reqLog)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// HTTPRecovery converts handler panics to 500s.
func HTTPRecovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("http panic recovered", "path", r.URL.Path, "panic", rec)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// HTTPLogging emits one line per request.
func HTTPLogging() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.FromContext(r.Context()).Info("http",
				"status", sw.status, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// HTTPRateLimit applies the sliding-window limiter keyed by bearer-token
// prefix or client IP.
func HTTPRateLimit(limiter domain.RateLimiter, cfg config.RateLimit, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			key := "ip:" + clientIP(r)
			if token := bearerFromHeader(r); token != "" && len(token) > 16 {
				key = "tok:" + token[:16]
			}
			allowed, err := limiter.Allow(r.Context(), key, cfg.RequestsPerMinute, time.Minute)
			if err != nil {
				log.Warn("rate limiter degraded, failing open", "error", err)
			}
			if !allowed {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// HTTPAuthenticate validates the Bearer token and attaches the identity.
// Endpoints supporting signed-URL access pass allowAnonymous=true and do
// their own token verification.
func HTTPAuthenticate(tokens domain.TokenIssuer, allowAnonymous bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerFromHeader(r)
			if token == "" {
				if allowAnonymous {
					next.ServeHTTP(w, r)
					return
				}
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}
			ident, err := tokens.ParseAccessToken(token)
			if err != nil {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), ident)))
		})
	}
}

// SecurityHeaders sets defensive response headers on every route.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
