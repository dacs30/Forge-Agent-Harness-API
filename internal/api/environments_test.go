package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

const testAPIKey = "test-key"

func testDeps() (store.Store, engine.Engine, *slog.Logger, *config.Config, *auth.Manager) {
	s := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	e := &engine.MockEngine{}
	l := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Load()
	cfg.APIKeys = []string{testAPIKey}
	mgr := auth.New(cfg.APIKeys)
	return s, e, l, cfg, mgr
}

func TestCreateEnvironment(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	body := `{"image":"alpine:latest","cpu":1,"memory_mb":512}`
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp CreateEnvironmentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if resp.Status != "running" {
		t.Fatalf("expected status running, got %s", resp.Status)
	}
	if resp.Image != "alpine:latest" {
		t.Fatalf("expected image alpine:latest, got %s", resp.Image)
	}
}

func TestCreateEnvironment_MissingImage(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	body := `{"cpu":1,"memory_mb":512}`
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreateEnvironment_InvalidCPU(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	body := `{"image":"alpine:latest","cpu":10,"memory_mb":512}`
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateEnvironment_ImageNotAllowed(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	cfg.AllowedImages = []string{"ubuntu:22.04", "python:3.12"}
	router := NewRouter(s, e, l, cfg, mgr)

	body := `{"image":"alpine:latest","cpu":1,"memory_mb":512}`
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateEnvironment_ImageAllowed(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	cfg.AllowedImages = []string{"ubuntu:22.04", "alpine:latest"}
	router := NewRouter(s, e, l, cfg, mgr)

	body := `{"image":"alpine:latest","cpu":1,"memory_mb":512}`
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDestroyEnvironment(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	userID, _ := mgr.UserID(testAPIKey)

	// Create an environment in the store owned by the test user
	env := &domain.Environment{
		ID:          "env_test123",
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "container123",
		CreatedAt:   time.Now(),
		LastUsedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(60 * time.Minute),
	}
	s.Create(context.Background(), env)

	req := httptest.NewRequest(http.MethodDelete, "/v1/environments/env_test123", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted from store
	_, err := s.Get(context.Background(), "env_test123", userID)
	if err != store.ErrNotFound {
		t.Fatalf("expected env to be deleted, got err: %v", err)
	}
}

func TestDestroyEnvironment_NotFound(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	req := httptest.NewRequest(http.MethodDelete, "/v1/environments/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDestroyEnvironment_WrongTenant(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	// Create an environment owned by a different user
	env := &domain.Environment{
		ID:         "env_other",
		UserID:     "other-user-id",
		Status:     domain.StatusRunning,
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		ExpiresAt:  time.Now().Add(60 * time.Minute),
	}
	s.Create(context.Background(), env)

	req := httptest.NewRequest(http.MethodDelete, "/v1/environments/env_other", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should look like a 404 — do not reveal the environment exists
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong tenant, got %d", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %s", resp["status"])
	}
}
