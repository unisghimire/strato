//go:build e2e

// End-to-end API tests against a fully running stack:
//
//	make dev-up && make migrate && make run   # in one terminal
//	make test-e2e                             # in another
//
// They exercise the REST surface exactly as a client would: register →
// login → chunked upload → download → share.
package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var baseURL = func() string {
	if v := os.Getenv("STRATO_E2E_BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}()

var client = &http.Client{Timeout: 30 * time.Second}

type session struct {
	t           *testing.T
	accessToken string
}

func (s *session) do(method, path string, body any, out any) *http.Response {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(s.t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(s.t, err)
	req.Header.Set("Content-Type", "application/json")
	if s.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.accessToken)
	}
	resp, err := client.Do(req)
	require.NoError(s.t, err)
	if out != nil {
		defer resp.Body.Close()
		require.NoError(s.t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp
}

func registerAndLogin(t *testing.T) *session {
	t.Helper()
	s := &session{t: t}
	email := fmt.Sprintf("e2e-%d@example.com", time.Now().UnixNano())

	resp := s.do("POST", "/v1/auth/register", map[string]string{
		"email": email, "password": "e2e-test-password", "display_name": "E2E",
	}, nil)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var login struct {
		AccessToken string `json:"access_token"`
	}
	resp = s.do("POST", "/v1/auth/login", map[string]string{
		"email": email, "password": "e2e-test-password",
	}, &login)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, login.AccessToken)
	s.accessToken = login.AccessToken
	return s
}

func TestFullUploadDownloadFlow(t *testing.T) {
	s := registerAndLogin(t)

	content := make([]byte, 3<<20) // 3 MiB
	_, err := rand.Read(content)
	require.NoError(t, err)
	sum := sha256.Sum256(content)

	// 1. Init upload session.
	var initResp struct {
		SessionID   string `json:"session_id"`
		ChunkSize   int64  `json:"chunk_size,string"`
		TotalChunks int32  `json:"total_chunks"`
	}
	resp := s.do("POST", "/v1/uploads", map[string]any{
		"name":            "e2e-file.bin",
		"mime_type":       "application/octet-stream",
		"size_bytes":      fmt.Sprint(len(content)),
		"checksum_sha256": hex.EncodeToString(sum[:]),
	}, &initResp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, initResp.SessionID)

	// 2. PUT each chunk as a raw body.
	for i := int64(0); i*initResp.ChunkSize < int64(len(content)); i++ {
		start := i * initResp.ChunkSize
		end := min(start+initResp.ChunkSize, int64(len(content)))
		req, err := http.NewRequest("PUT",
			fmt.Sprintf("%s/v1/uploads/%s/chunks/%d", baseURL, initResp.SessionID, i),
			bytes.NewReader(content[start:end]))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+s.accessToken)
		chunkResp, err := client.Do(req)
		require.NoError(t, err)
		chunkResp.Body.Close()
		require.Equal(t, http.StatusOK, chunkResp.StatusCode, "chunk %d", i)
	}

	// 3. Complete.
	var completeResp struct {
		File struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"file"`
	}
	resp = s.do("POST", "/v1/uploads/"+initResp.SessionID+"/complete", map[string]any{}, &completeResp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, completeResp.File.ID)

	// 4. Download and verify byte-exactness through decryption.
	req, err := http.NewRequest("GET", baseURL+"/v1/files/"+completeResp.File.ID+"/content", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	dlResp, err := client.Do(req)
	require.NoError(t, err)
	defer dlResp.Body.Close()
	require.Equal(t, http.StatusOK, dlResp.StatusCode)
	got, err := io.ReadAll(dlResp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	s := &session{t: t} // no token
	resp := s.do("GET", "/v1/files", nil, nil)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSignedDownloadURL(t *testing.T) {
	s := registerAndLogin(t)
	content := []byte("small signed-url test payload")
	sum := sha256.Sum256(content)

	var initResp struct {
		SessionID string `json:"session_id"`
	}
	resp := s.do("POST", "/v1/uploads", map[string]any{
		"name": "signed.txt", "mime_type": "text/plain",
		"size_bytes": fmt.Sprint(len(content)), "checksum_sha256": hex.EncodeToString(sum[:]),
	}, &initResp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	req, err := http.NewRequest("PUT",
		fmt.Sprintf("%s/v1/uploads/%s/chunks/0", baseURL, initResp.SessionID), bytes.NewReader(content))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	chunkResp, err := client.Do(req)
	require.NoError(t, err)
	chunkResp.Body.Close()

	var completeResp struct {
		File struct {
			ID string `json:"id"`
		} `json:"file"`
	}
	resp = s.do("POST", "/v1/uploads/"+initResp.SessionID+"/complete", map[string]any{}, &completeResp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var urlResp struct {
		URL string `json:"url"`
	}
	resp = s.do("GET", "/v1/files/"+completeResp.File.ID+":downloadUrl", nil, &urlResp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, urlResp.URL)

	// The signed URL works with NO Authorization header.
	dlResp, err := client.Get(baseURL + urlResp.URL)
	require.NoError(t, err)
	defer dlResp.Body.Close()
	require.Equal(t, http.StatusOK, dlResp.StatusCode)
	got, err := io.ReadAll(dlResp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}
