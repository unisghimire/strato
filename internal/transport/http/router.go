package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/internal/middleware"
)

// RouterDeps carries everything the HTTP tier needs.
type RouterDeps struct {
	Log      *slog.Logger
	Tokens   domain.TokenIssuer
	Limiter  domain.RateLimiter
	RateCfg  config.RateLimit
	Gateway  http.Handler
	Upload   *UploadHandler
	Download *DownloadHandler
	Public   *PublicHandler
	// Readiness probes; each should return nil when the dependency is up.
	ReadyChecks map[string]func(context.Context) error
}

// NewRouter assembles the HTTP mux:
//
//	PUT  /v1/uploads/{session_id}/chunks/{index}  raw chunk upload (auth)
//	GET  /v1/files/{file_id}/content              streaming download (auth or signed)
//	GET  /public/{token}                          anonymous public links
//	GET  /healthz, /readyz                        probes
//	*                                             grpc-gateway (JSON API)
func NewRouter(d RouterDeps) http.Handler {
	mux := http.NewServeMux()

	authRequired := middleware.HTTPAuthenticate(d.Tokens, false)
	authOptional := middleware.HTTPAuthenticate(d.Tokens, true)

	mux.Handle("PUT /v1/uploads/{session_id}/chunks/{index}", authRequired(d.Upload))
	mux.Handle("GET /v1/files/{file_id}/content", authOptional(d.Download))
	mux.Handle("GET /public/{token}", d.Public)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		for name, check := range d.ReadyChecks {
			if err := check(r.Context()); err != nil {
				d.Log.Warn("readiness check failed", "dependency", name, "error", err)
				http.Error(w, name+" unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	// Everything else goes to the JSON gateway (which enforces auth via the
	// gRPC interceptor chain it forwards to).
	mux.Handle("/", d.Gateway)

	// Outer middleware, outermost first.
	var h http.Handler = mux
	h = middleware.HTTPRateLimit(d.Limiter, d.RateCfg, d.Log)(h)
	h = middleware.HTTPLogging()(h)
	h = middleware.HTTPRecovery(d.Log)(h)
	h = middleware.HTTPRequestMeta(d.Log)(h)
	h = middleware.SecurityHeaders(h)
	return h
}
