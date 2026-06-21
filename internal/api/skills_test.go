package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/pkg/apitypes"
)

func skillArchive(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	tw.Close()
	gz.Close()
	return &buf
}

func TestRegisterSkill_AndList(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	body := skillArchive(t, map[string]string{"SKILL.md": "# hi", "run.sh": "echo hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/?name=my-skill", body)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var skill apitypes.Skill
	if err := json.NewDecoder(w.Body).Decode(&skill); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if skill.Name != "my-skill" || skill.ID == "" {
		t.Fatalf("unexpected skill: %+v", skill)
	}

	// List.
	lreq := httptest.NewRequest(http.MethodGet, "/v1/skills/", nil)
	lreq.Header.Set("Authorization", "Bearer "+testAPIKey)
	lw := httptest.NewRecorder()
	router.ServeHTTP(lw, lreq)
	if lw.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", lw.Code)
	}
	var skills []apitypes.Skill
	if err := json.NewDecoder(lw.Body).Decode(&skills); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
}

func TestRegisterSkill_MissingName(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	body := skillArchive(t, map[string]string{"SKILL.md": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/", body)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "name query parameter is required")
}

func TestRegisterSkill_MissingManifest(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	body := skillArchive(t, map[string]string{"readme.txt": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/?name=bad", body)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "skill archive must contain a top-level SKILL.md")
}

func TestGetSkill_NotFound(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	req := httptest.NewRequest(http.MethodGet, "/v1/skills/skill_missing", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusNotFound, "skill not found")
}

func TestInstallSkillToEnvironment(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	now := time.Now()
	if err := s.Create(context.Background(), &domain.Environment{
		ID:          "env_run",
		TenantID:    userID,
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "c1",
		CreatedAt:   now,
		LastUsedAt:  now,
		ExpiresAt:   now.Add(60 * time.Minute),
	}); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	body := skillArchive(t, map[string]string{"SKILL.md": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_run/skills?name=adhoc", body)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("install: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	mock := e.(*engine.MockEngine)
	if len(mock.ExtractedArchives) != 1 || mock.ExtractedArchives[0] != "/root/.claude/skills/adhoc" {
		t.Fatalf("expected extract into skill dir, got %v", mock.ExtractedArchives)
	}
}

func TestListInstalledSkills(t *testing.T) {
	s, _, l, cfg, mgr := testDeps()
	mock := &engine.MockEngine{
		ListFilesFn: func(_ context.Context, _, path string) ([]domain.FileInfo, error) {
			return []domain.FileInfo{{Name: "pdf-tools", IsDir: true}}, nil
		},
		ReadFileFn: func(_ context.Context, _, path string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("---\nname: pdf-tools\ndescription: Work with PDFs\n---\n")), nil
		},
	}
	router := NewRouter(s, mock, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	now := time.Now()
	if err := s.Create(context.Background(), &domain.Environment{
		ID: "env_run", TenantID: userID, UserID: userID, Status: domain.StatusRunning,
		ContainerID: "c1", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_run/skills", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var skills []apitypes.InstalledSkill
	if err := json.NewDecoder(w.Body).Decode(&skills); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "pdf-tools" || skills[0].Description != "Work with PDFs" {
		t.Fatalf("unexpected skills: %+v", skills)
	}
}
