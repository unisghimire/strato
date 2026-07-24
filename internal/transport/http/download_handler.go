package http

import (
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/entity"
	"github.com/unisghimire/strato/internal/middleware"
	"github.com/unisghimire/strato/internal/usecase"
	"github.com/unisghimire/strato/pkg/logger"
)

// DownloadHandler serves decrypted file content:
//
//	GET /v1/files/{file_id}/content            (Bearer token)
//	GET /v1/files/{file_id}/content?uid&exp&sig (signed URL)
//
// Bytes stream store → decrypt → client in constant memory.
type DownloadHandler struct {
	files   *usecase.FileUseCase
	metrics *middleware.Metrics
}

// NewDownloadHandler constructs a DownloadHandler.
func NewDownloadHandler(files *usecase.FileUseCase, metrics *middleware.Metrics) *DownloadHandler {
	return &DownloadHandler{files: files, metrics: metrics}
}

// ServeHTTP handles the content GET.
func (h *DownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fileID, err := uuid.Parse(r.PathValue("file_id"))
	if err != nil {
		http.Error(w, "malformed file id", http.StatusBadRequest)
		return
	}

	var (
		rc io.ReadCloser
		f  *entity.File
	)
	if ident, identErr := auth.IdentityFromContext(r.Context()); identErr == nil {
		rc, f, err = h.files.OpenDownload(r.Context(), ident, fileID)
	} else {
		// No bearer token: fall back to signed-URL query parameters.
		rc, f, err = h.files.VerifySignedDownload(r.Context(), fileID, r.URL.Query())
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rc.Close()

	serveStream(w, r, rc, f, h.metrics)
}

// serveStream writes download headers and streams plaintext to the client.
// Shared with the public-link handler.
func serveStream(w http.ResponseWriter, r *http.Request, rc io.Reader, f *entity.File, metrics *middleware.Metrics) {
	contentType := f.MimeType
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", f.SizeBytes))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.Name))
	w.Header().Set("Cache-Control", "private, no-store")

	n, err := io.Copy(w, rc)
	metrics.AddDownloadBytes(n)
	if err != nil {
		// Headers are gone; all we can do is log (client likely disconnected
		// or a storage/decrypt fault surfaced mid-stream).
		logger.FromContext(r.Context()).Warn("download stream interrupted",
			"file_id", f.ID, "bytes_sent", n, "error", err)
	}
}
