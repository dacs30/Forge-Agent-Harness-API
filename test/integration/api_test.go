//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"haas/internal/api"
	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/engine"
	"haas/internal/store"
	"haas/test/testutil"
)

const integrationAPIKey = "integration-test-key"

func setupServer(t *testing.T) http.Handler {
	t.Helper()
	testutil.SkipIfNoDocker(t)

	cfg := config.Load()
	cfg.APIKeys = []string{integrationAPIKey}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := store.NewMemoryStore(cfg.IdleTimeout, cfg.MaxLifetime)
	e, err := engine.NewDockerEngine(cfg, logger)
	if err != nil {
		t.Fatalf("failed to create docker engine: %v", err)
	}

	authMgr := auth.New(cfg.APIKeys)
	return api.NewRouter(s, e, logger, cfg, authMgr)
}

// authReq is a helper that creates a request pre-loaded with the test API key.
func authReq(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer "+integrationAPIKey)
	return req
}

func TestFullLifecycle(t *testing.T) {
	router := setupServer(t)

	// 1. Create environment
	createBody := `{"image":"alpine:latest","cpu":0.5,"memory_mb":256,"network_policy":"none"}`
	req := authReq(http.MethodPost, "/v1/environments/", bytes.NewBufferString(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var createResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	json.NewDecoder(w.Body).Decode(&createResp)
	envID := createResp.ID
	t.Logf("created environment: %s", envID)

	// Allow container to fully start
	time.Sleep(1 * time.Second)

	// 2. Execute command
	execBody := `{"command":["echo","hello world"]}`
	req = authReq(http.MethodPost, "/v1/environments/"+envID+"/exec", bytes.NewBufferString(execBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("exec: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	t.Logf("exec output: %s", w.Body.String())

	// 3. Write file
	fileContent := []byte("hello from haas")
	req = authReq(http.MethodPut, "/v1/environments/"+envID+"/files/content?path=/tmp/test.txt", bytes.NewReader(fileContent))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("write file: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Read file back
	req = authReq(http.MethodGet, "/v1/environments/"+envID+"/files/content?path=/tmp/test.txt", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read file: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data, _ := io.ReadAll(w.Body)
	if string(data) != "hello from haas" {
		t.Fatalf("expected 'hello from haas', got '%s'", string(data))
	}

	// 5. List files
	req = authReq(http.MethodGet, "/v1/environments/"+envID+"/files/?path=/tmp", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list files: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	t.Logf("files: %s", w.Body.String())

	// 6. Destroy environment
	req = authReq(http.MethodDelete, "/v1/environments/"+envID, nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("destroy: expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuth_MissingKey(t *testing.T) {
	router := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/", nil) // no auth header
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_InvalidKey(t *testing.T) {
	router := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
