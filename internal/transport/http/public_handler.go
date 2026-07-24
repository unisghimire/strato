package http

import (
	"net/http"

	"github.com/unisghimire/strato/internal/middleware"
	"github.com/unisghimire/strato/internal/usecase"
)

// PublicHandler serves public share links anonymously:
//
//	GET /public/{token}                  → download the shared file
//	GET /public/{token}?password=...     → password-gated variant
//
// The token authorizes; no account or bearer token is involved.
type PublicHandler struct {
	shares  *usecase.ShareUseCase
	files   *usecase.FileUseCase
	metrics *middleware.Metrics
}

// NewPublicHandler constructs a PublicHandler.
func NewPublicHandler(shares *usecase.ShareUseCase, files *usecase.FileUseCase, metrics *middleware.Metrics) *PublicHandler {
	return &PublicHandler{shares: shares, files: files, metrics: metrics}
}

// ServeHTTP resolves the link and streams the file.
func (h *PublicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" || len(token) > 128 {
		http.Error(w, "malformed link", http.StatusBadRequest)
		return
	}
	// Password may arrive via header (preferred: stays out of access logs)
	// or query parameter (link-friendly).
	password := r.Header.Get("X-Share-Password")
	if password == "" {
		password = r.URL.Query().Get("password")
	}

	f, _, err := h.shares.ResolvePublicLink(r.Context(), token, password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rc, err := h.files.OpenFileContent(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rc.Close()

	serveStream(w, r, rc, f, h.metrics)
}
