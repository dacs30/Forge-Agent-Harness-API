package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

const (
	tenantAKey = "tenant-a-key"
	tenantBKey = "tenant-b-key"
)

// multitenantDeps wires two tenants sharing a single store/router.
func multitenantDeps(t *testing.T) (store.Store, http.Handler, string, string) {
	t.Helper()
	s := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	e := &engine.MockEngine{}
	l := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Load()
	cfg.APIKeys = []string{tenantAKey, tenantBKey}
	mgr := auth.New(cfg.APIKeys)

	userA, _ := mgr.UserID(tenantAKey)
	userB, _ := mgr.UserID(tenantBKey)

	router := NewRouter(s, e, l, cfg, mgr)
	return s, router, userA, userB
}

// seedEnv creates an environment in the store directly, owned by userID.
func seedEnv(t *testing.T, s store.Store, id, userID string) {
	t.Helper()
	env := &domain.Environment{
		ID:          id,
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "container_" + id,
		Spec:        domain.EnvironmentSpec{Image: "alpine:latest"},
		CreatedAt:   time.Now(),
		LastUsedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(60 * time.Minute),
	}
	if err := s.Create(context.Background(), env); err != nil {
		t.Fatalf("seed env %s: %v", id, err)
	}
}

func TestGetEnvironment_WrongTenant(t *testing.T) {
	s, router, _, userB := multitenantDeps(t)
	seedEnv(t, s, "env_a1", userB) // owned by B

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_a1", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey) // A tries to read B's env
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListEnvironments_Isolation(t *testing.T) {
	s, router, userA, userB := multitenantDeps(t)
	seedEnv(t, s, "env_a1", userA)
	seedEnv(t, s, "env_a2", userA)
	seedEnv(t, s, "env_b1", userB)

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "env_b1") {
		t.Fatal("tenant A's list response must not contain tenant B's environment")
	}
	if !strings.Contains(body, "env_a1") || !strings.Contains(body, "env_a2") {
		t.Fatal("tenant A's list response is missing their own environments")
	}
}

func TestExec_WrongTenant(t *testing.T) {
	s, router, _, userB := multitenantDeps(t)
	seedEnv(t, s, "env_b1", userB) // owned by B

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_b1/exec",
		strings.NewReader(`{"command":["echo","hi"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tenantAKey) // A tries to exec into B's env
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListFiles_WrongTenant(t *testing.T) {
	s, router, _, userB := multitenantDeps(t)
	seedEnv(t, s, "env_b1", userB)

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_b1/files?path=/", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReadFile_WrongTenant(t *testing.T) {
	s, router, _, userB := multitenantDeps(t)
	seedEnv(t, s, "env_b1", userB)

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_b1/files/content?path=/etc/hosts", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWriteFile_WrongTenant(t *testing.T) {
	s, router, _, userB := multitenantDeps(t)
	seedEnv(t, s, "env_b1", userB)

	req := httptest.NewRequest(http.MethodPut, "/v1/environments/env_b1/files/content?path=/tmp/x.txt",
		strings.NewReader("data"))
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
