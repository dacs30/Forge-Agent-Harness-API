package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haas/internal/domain"
)

func TestCreateSnapshot_EnvironmentNotFound(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_missing/snapshots", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusNotFound, "environment not found")
}

func TestCreateSnapshot_EnvironmentNotRunning(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	now := time.Now()
	if err := s.Create(context.Background(), &domain.Environment{
		ID:         "env_stopped",
		TenantID:   userID,
		UserID:     userID,
		Status:     domain.StatusStopped,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(60 * time.Minute),
	}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_stopped/snapshots", strings.NewReader(`{"label":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusConflict, "environment must be running to create a snapshot")
}

func TestGetSnapshot_NotFound(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	req := httptest.NewRequest(http.MethodGet, "/v1/snapshots/snap_missing", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusNotFound, "snapshot not found")
}

func assertErrorResponse(t *testing.T, w *httptest.ResponseRecorder, wantCode int, wantError string) {
	t.Helper()
	if w.Code != wantCode {
		t.Fatalf("expected status %d, got %d: %s", wantCode, w.Code, w.Body.String())
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error != wantError {
		t.Fatalf("expected error %q, got %q", wantError, resp.Error)
	}
	if resp.Code != wantCode {
		t.Fatalf("expected code %d, got %d", wantCode, resp.Code)
	}
}
