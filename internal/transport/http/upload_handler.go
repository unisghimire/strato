package http

import (
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/unisghimire/strato/internal/auth"
	"github.com/unisghimire/strato/internal/middleware"
	"github.com/unisghimire/strato/internal/usecase"
)

// UploadHandler serves raw chunk uploads:
//
//	PUT /v1/uploads/{session_id}/chunks/{index}
//
// The body is the chunk bytes, streamed straight into staging storage —
// never buffered in memory, never base64'd through JSON.
type UploadHandler struct {
	uploads   *usecase.UploadUseCase
	metrics   *middleware.Metrics
	chunkSize int64
}

// NewUploadHandler constructs an UploadHandler.
func NewUploadHandler(uploads *usecase.UploadUseCase, metrics *middleware.Metrics, chunkSize int64) *UploadHandler {
	return &UploadHandler{uploads: uploads, metrics: metrics, chunkSize: chunkSize}
}

// ServeHTTP handles the chunk PUT.
func (h *UploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ident, err := auth.IdentityFromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := uuid.Parse(r.PathValue("session_id"))
	if err != nil {
		http.Error(w, "malformed session id", http.StatusBadRequest)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		http.Error(w, "malformed chunk index", http.StatusBadRequest)
		return
	}

	// Hard cap the request body: chunk size + 1 so oversized chunks fail
	// inside the store put (short write of the limit) rather than filling
	// staging storage.
	body := http.MaxBytesReader(w, r.Body, h.chunkSize+1)
	defer body.Close()

	chunk, err := h.uploads.UploadChunk(r.Context(), ident, sessionID, index, body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.metrics.AddUploadBytes(chunk.SizeBytes)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":      sessionID.String(),
		"chunk_index":     chunk.Index,
		"size_bytes":      chunk.SizeBytes,
		"checksum_sha256": hex.EncodeToString(chunk.ChecksumSHA256),
	})
}
