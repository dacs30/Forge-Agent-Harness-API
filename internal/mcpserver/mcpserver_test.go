package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"haas/pkg/apitypes"
)

// TestClientFromContext_PerKeyRouting verifies that each tenant's MCP request is
// proxied to the REST API using that tenant's own key — not a hardcoded first key.
func TestClientFromContext_PerKeyRouting(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]*apitypes.Environment{}) //nolint:errcheck
	}))
	defer srv.Close()

	s := New(srv.URL, []string{"key-alpha", "key-beta"})

	tests := []struct {
		name     string
		ctxKey   string
		wantAuth string
	}{
		{"key-alpha in context", "key-alpha", "Bearer key-alpha"},
		{"key-beta in context", "key-beta", "Bearer key-beta"},
		{"unknown key falls back to first", "key-unknown", "Bearer key-alpha"},
		{"no context key falls back to first", "", "Bearer key-alpha"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctxKey != "" {
				ctx = context.WithValue(ctx, mcpAPIKeyContextKey{}, tc.ctxKey)
			}
			if _, err := s.clientFromContext(ctx).listEnvironments(ctx); err != nil {
				t.Fatalf("listEnvironments: %v", err)
			}
			if gotAuth != tc.wantAuth {
				t.Fatalf("Authorization header: want %q, got %q", tc.wantAuth, gotAuth)
			}
		})
	}
}

// TestHaasClient_WriteFile_ForwardsUserID ensures that writeFile — which builds
// its own http.Request rather than going through do() — still sets X-Haas-User-ID
// when the client has a userID set.
func TestHaasClient_WriteFile_ForwardsUserID(t *testing.T) {
	var gotUserID, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = r.Header.Get("X-Haas-User-ID")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newHaasClient(srv.URL, "tenant-key").withUserID("alice")
	if err := c.writeFile(context.Background(), "env_1", "/tmp/test.txt", "hello"); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	if gotUserID != "alice" {
		t.Fatalf("X-Haas-User-ID: want %q, got %q", "alice", gotUserID)
	}
	if gotAuth != "Bearer tenant-key" {
		t.Fatalf("Authorization: want %q, got %q", "Bearer tenant-key", gotAuth)
	}
}

// TestHaasClient_WriteFile_NoUserID_OmitsHeader ensures that writeFile does NOT
// set X-Haas-User-ID when no end-user scope is configured on the client.
func TestHaasClient_WriteFile_NoUserID_OmitsHeader(t *testing.T) {
	var gotUserID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = r.Header.Get("X-Haas-User-ID")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newHaasClient(srv.URL, "tenant-key") // no withUserID
	if err := c.writeFile(context.Background(), "env_1", "/tmp/test.txt", "hello"); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	if gotUserID != "" {
		t.Fatalf("X-Haas-User-ID should be absent, got %q", gotUserID)
	}
}

func TestHandleListTenantEnvironments_IncludesOwnerFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenant/environments" {
			t.Fatalf("path: want /v1/tenant/environments, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"id":"env_alice",
			"tenant_id":"tenant-a",
			"user_id":"user-alice",
			"spec":{"image":"alpine:latest","cpu":1,"memory_mb":512,"disk_mb":1024,"network_policy":"none"},
			"status":"running",
			"created_at":"2026-01-01T00:00:00Z",
			"last_used_at":"2026-01-01T00:00:00Z",
			"expires_at":"2026-01-01T01:00:00Z"
		}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	s := New(srv.URL, []string{"tenant-key"})
	result, err := s.handleListTenantEnvironments(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListTenantEnvironments: %v", err)
	}

	text := toolResultText(t, result)
	for _, want := range []string{`"tenant_id": "tenant-a"`, `"user_id": "user-alice"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("tenant environment output missing %s:\n%s", want, text)
		}
	}
}

func TestHandleListTenantSnapshots_IncludesOwnerFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenant/snapshots" {
			t.Fatalf("path: want /v1/tenant/snapshots, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"id":"snap_alice",
			"tenant_id":"tenant-a",
			"user_id":"user-alice",
			"environment_id":"env_alice",
			"image_id":"img_alice",
			"label":"alice",
			"size":123,
			"created_at":"2026-01-01T00:00:00Z"
		}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	s := New(srv.URL, []string{"tenant-key"})
	result, err := s.handleListTenantSnapshots(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListTenantSnapshots: %v", err)
	}

	text := toolResultText(t, result)
	for _, want := range []string{`"tenant_id": "tenant-a"`, `"user_id": "user-alice"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("tenant snapshot output missing %s:\n%s", want, text)
		}
	}
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result.IsError {
		t.Fatal("tool result unexpectedly marked as error")
	}
	if len(result.Content) != 1 {
		t.Fatalf("tool result content length: want 1, got %d", len(result.Content))
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool result content type: want mcp.TextContent, got %T", result.Content[0])
	}
	return content.Text
}
