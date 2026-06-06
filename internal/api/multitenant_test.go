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

	"github.com/google/uuid"

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
// TenantID defaults to userID (the no-end-user-scoping case).
func seedEnv(t *testing.T, s store.Store, id, userID string) {
	t.Helper()
	seedEnvWithTenant(t, s, id, userID, userID)
}

// seedEnvWithTenant creates an environment with explicit tenant and user IDs.
func seedEnvWithTenant(t *testing.T, s store.Store, id, tenantID, userID string) {
	t.Helper()
	env := &domain.Environment{
		ID:          id,
		TenantID:    tenantID,
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

// seedSnapshot creates a snapshot in the store directly, owned by userID under tenantID.
func seedSnapshot(t *testing.T, s store.Store, id, tenantID, userID string) {
	t.Helper()
	snap := &domain.Snapshot{
		ID:            id,
		TenantID:      tenantID,
		UserID:        userID,
		EnvironmentID: "env_for_" + id,
		ImageID:       "img_" + id,
		Label:         id,
		CreatedAt:     time.Now(),
	}
	if err := s.CreateSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("seed snapshot %s: %v", id, err)
	}
}

// TestEndUserIsolation verifies that two end-users under the same tenant cannot
// see each other's environments, but the tenant can see both.
func TestEndUserIsolation(t *testing.T) {
	s, router, userA, _ := multitenantDeps(t)

	// Derive end-user scoped IDs the same way auth middleware does.
	tenantUUID := uuid.MustParse(userA)
	aliceID := uuid.NewSHA1(tenantUUID, []byte("alice")).String()
	bobID := uuid.NewSHA1(tenantUUID, []byte("bob")).String()

	// Seed environments as if they were created by alice and bob under tenant A.
	seedEnvWithTenant(t, s, "env_alice", userA, aliceID)
	seedEnvWithTenant(t, s, "env_bob", userA, bobID)

	// Alice can see her own environment.
	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_alice", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	req.Header.Set("X-Haas-User-ID", "alice")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("alice GET own env: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Alice cannot see Bob's environment.
	req = httptest.NewRequest(http.MethodGet, "/v1/environments/env_bob", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	req.Header.Set("X-Haas-User-ID", "alice")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("alice GET bob's env: expected 404, got %d", w.Code)
	}

	// Alice's list only shows her environments.
	req = httptest.NewRequest(http.MethodGet, "/v1/environments", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	req.Header.Set("X-Haas-User-ID", "alice")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("alice list: expected 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "env_bob") {
		t.Fatal("alice's list must not contain bob's environment")
	}
	if !strings.Contains(w.Body.String(), "env_alice") {
		t.Fatal("alice's list is missing her own environment")
	}

	// Tenant-admin view shows both environments.
	req = httptest.NewRequest(http.MethodGet, "/v1/tenant/environments", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant list: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "env_alice") || !strings.Contains(body, "env_bob") {
		t.Fatalf("tenant list must contain both alice's and bob's environments, got: %s", body)
	}
}

// TestTenantList_DoesNotLeakAcrossTenants verifies the tenant endpoint only shows
// environments for the authenticated tenant, not other tenants.
func TestTenantList_DoesNotLeakAcrossTenants(t *testing.T) {
	s, router, userA, userB := multitenantDeps(t)

	seedEnvWithTenant(t, s, "env_a1", userA, userA)
	seedEnvWithTenant(t, s, "env_b1", userB, userB)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/environments", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "env_b1") {
		t.Fatal("tenant A's admin view must not contain tenant B's environment")
	}
	if !strings.Contains(body, "env_a1") {
		t.Fatal("tenant A's admin view is missing their own environment")
	}
}

// TestSnapshotEndUserIsolation verifies that end-users under the same tenant cannot
// see each other's snapshots, but the tenant admin view shows all.
func TestSnapshotEndUserIsolation(t *testing.T) {
	s, router, userA, _ := multitenantDeps(t)

	tenantUUID := uuid.MustParse(userA)
	aliceID := uuid.NewSHA1(tenantUUID, []byte("alice")).String()
	bobID := uuid.NewSHA1(tenantUUID, []byte("bob")).String()

	seedSnapshot(t, s, "snap_alice", userA, aliceID)
	seedSnapshot(t, s, "snap_bob", userA, bobID)

	// Alice's list only shows her snapshot.
	req := httptest.NewRequest(http.MethodGet, "/v1/snapshots", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	req.Header.Set("X-Haas-User-ID", "alice")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("alice list snapshots: expected 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "snap_bob") {
		t.Fatal("alice's snapshot list must not contain bob's snapshot")
	}
	if !strings.Contains(w.Body.String(), "snap_alice") {
		t.Fatal("alice's snapshot list is missing her own snapshot")
	}

	// Alice cannot get Bob's snapshot directly.
	req = httptest.NewRequest(http.MethodGet, "/v1/snapshots/snap_bob", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	req.Header.Set("X-Haas-User-ID", "alice")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("alice GET bob's snapshot: expected 404, got %d", w.Code)
	}

	// Tenant admin view shows both snapshots.
	req = httptest.NewRequest(http.MethodGet, "/v1/tenant/snapshots", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant list snapshots: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "snap_alice") || !strings.Contains(body, "snap_bob") {
		t.Fatalf("tenant snapshot list must contain both users' snapshots, got: %s", body)
	}
}

// TestSnapshotTenantList_DoesNotLeakAcrossTenants verifies the tenant snapshot
// endpoint only shows snapshots for the authenticated tenant.
func TestSnapshotTenantList_DoesNotLeakAcrossTenants(t *testing.T) {
	s, router, userA, userB := multitenantDeps(t)

	seedSnapshot(t, s, "snap_a1", userA, userA)
	seedSnapshot(t, s, "snap_b1", userB, userB)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/snapshots", nil)
	req.Header.Set("Authorization", "Bearer "+tenantAKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "snap_b1") {
		t.Fatal("tenant A's snapshot admin view must not contain tenant B's snapshot")
	}
	if !strings.Contains(body, "snap_a1") {
		t.Fatal("tenant A's snapshot admin view is missing their own snapshot")
	}
}
