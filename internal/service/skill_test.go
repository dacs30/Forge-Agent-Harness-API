package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

// makeSkillTarGz builds a gzip-compressed tar archive from the given files,
// keyed by their path relative to the skill root.
func makeSkillTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func newTestSkillService(s store.Store, e engine.Engine) *SkillService {
	return NewSkillService(s, e, testLogger(), "/root/.claude/skills", 10<<20)
}

func TestSkillService_RegisterAndList(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	svc := newTestSkillService(mem, &engine.MockEngine{})

	archive := makeSkillTarGz(t, map[string]string{
		"SKILL.md":      "# my skill",
		"scripts/go.sh": "echo hi",
	})

	skill, err := svc.RegisterSkill(ctx, "tenant", "user", "my-skill", bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if skill.Name != "my-skill" || skill.UserID != "user" {
		t.Fatalf("unexpected skill: %+v", skill)
	}
	if skill.SizeBytes == 0 {
		t.Fatalf("expected non-zero size")
	}

	skills, err := svc.ListSkills(ctx, "user")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
}

func TestSkillService_RegisterRejectsMissingManifest(t *testing.T) {
	ctx := context.Background()
	svc := newTestSkillService(store.NewMemoryStore(time.Minute, time.Minute), &engine.MockEngine{})

	archive := makeSkillTarGz(t, map[string]string{"readme.txt": "no manifest here"})
	_, err := svc.RegisterSkill(ctx, "tenant", "user", "bad", bytes.NewReader(archive))
	if !errors.Is(err, ErrSkillMissingManifest) {
		t.Fatalf("expected ErrSkillMissingManifest, got %v", err)
	}
}

func TestSkillService_RegisterRejectsBadName(t *testing.T) {
	ctx := context.Background()
	svc := newTestSkillService(store.NewMemoryStore(time.Minute, time.Minute), &engine.MockEngine{})
	archive := makeSkillTarGz(t, map[string]string{"SKILL.md": "x"})

	for _, name := range []string{"", "../escape", "has/slash", "bad name"} {
		_, err := svc.RegisterSkill(ctx, "tenant", "user", name, bytes.NewReader(archive))
		if err == nil {
			t.Fatalf("expected error for name %q", name)
		}
	}
}

func TestSkillService_RegisterRejectsNonGzip(t *testing.T) {
	ctx := context.Background()
	svc := newTestSkillService(store.NewMemoryStore(time.Minute, time.Minute), &engine.MockEngine{})

	_, err := svc.RegisterSkill(ctx, "tenant", "user", "plain", bytes.NewReader([]byte("not a gzip")))
	if !errors.Is(err, ErrInvalidSkillArchive) {
		t.Fatalf("expected ErrInvalidSkillArchive, got %v", err)
	}
}

func TestSkillService_RegisterRejectsTraversalEntry(t *testing.T) {
	ctx := context.Background()
	svc := newTestSkillService(store.NewMemoryStore(time.Minute, time.Minute), &engine.MockEngine{})

	archive := makeSkillTarGz(t, map[string]string{
		"SKILL.md":       "x",
		"../../etc/evil": "pwned",
	})
	_, err := svc.RegisterSkill(ctx, "tenant", "user", "evil", bytes.NewReader(archive))
	if !errors.Is(err, ErrInvalidSkillArchive) {
		t.Fatalf("expected ErrInvalidSkillArchive, got %v", err)
	}
}

func TestSkillService_UpsertReplacesByName(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(time.Minute, time.Minute)
	svc := newTestSkillService(mem, &engine.MockEngine{})

	a1 := makeSkillTarGz(t, map[string]string{"SKILL.md": "v1"})
	first, err := svc.RegisterSkill(ctx, "tenant", "user", "dup", bytes.NewReader(a1))
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}

	a2 := makeSkillTarGz(t, map[string]string{"SKILL.md": "v2 longer content"})
	second, err := svc.RegisterSkill(ctx, "tenant", "user", "dup", bytes.NewReader(a2))
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected stable ID across upsert, got %q then %q", first.ID, second.ID)
	}
	skills, _ := svc.ListSkills(ctx, "user")
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after upsert, got %d", len(skills))
	}
}

func TestSkillService_InstallToEnvironment(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(time.Minute, time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	mock := &engine.MockEngine{}
	svc := newTestSkillService(mem, mock)

	archive := makeSkillTarGz(t, map[string]string{"SKILL.md": "x"})
	if err := svc.InstallSkillToEnvironment(ctx, "env_test", "user", "adhoc", bytes.NewReader(archive)); err != nil {
		t.Fatalf("install: %v", err)
	}

	if len(mock.ExtractedArchives) != 1 || mock.ExtractedArchives[0] != "/root/.claude/skills/adhoc" {
		t.Fatalf("expected extract into skill dir, got %v", mock.ExtractedArchives)
	}
}

func TestSkillService_ListInstalledSkills(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(time.Minute, time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	manifests := map[string]string{
		"/root/.claude/skills/pdf-tools/SKILL.md": "---\nname: pdf-tools\ndescription: Work with PDF files\n---\n# body",
		"/root/.claude/skills/no-name/SKILL.md":   "---\ndescription: nameless skill\n---\n",
	}
	mock := &engine.MockEngine{
		ListFilesFn: func(_ context.Context, _, path string) ([]domain.FileInfo, error) {
			if path != "/root/.claude/skills" {
				return nil, nil
			}
			return []domain.FileInfo{
				{Name: "pdf-tools", IsDir: true},
				{Name: "no-name", IsDir: true},
				{Name: "loose.txt", IsDir: false}, // not a directory → skipped
			}, nil
		},
		ReadFileFn: func(_ context.Context, _, path string) (io.ReadCloser, error) {
			content, ok := manifests[path]
			if !ok {
				return nil, errors.New("no such file")
			}
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
	svc := newTestSkillService(mem, mock)

	skills, err := svc.ListInstalledSkills(ctx, "env_test", "user")
	if err != nil {
		t.Fatalf("list installed: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 installed skills, got %d (%+v)", len(skills), skills)
	}

	byName := map[string]domain.InstalledSkill{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	if byName["pdf-tools"].Description != "Work with PDF files" {
		t.Fatalf("unexpected pdf-tools: %+v", byName["pdf-tools"])
	}
	if byName["pdf-tools"].Path != "/root/.claude/skills/pdf-tools" {
		t.Fatalf("unexpected path: %q", byName["pdf-tools"].Path)
	}
	// Falls back to the directory name when frontmatter has no name.
	if _, ok := byName["no-name"]; !ok {
		t.Fatalf("expected fallback to dir name 'no-name', got %+v", skills)
	}
}

func TestSkillService_ListInstalledSkills_EmptyWhenDirMissing(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(time.Minute, time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	mock := &engine.MockEngine{
		ListFilesFn: func(context.Context, string, string) ([]domain.FileInfo, error) {
			return nil, errors.New("no such directory")
		},
	}
	svc := newTestSkillService(mem, mock)

	skills, err := svc.ListInstalledSkills(ctx, "env_test", "user")
	if err != nil {
		t.Fatalf("expected no error when dir missing, got %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("expected empty list, got %d", len(skills))
	}
}

func TestSkillService_InstallToEnvironment_RequiresRunning(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(time.Minute, time.Minute)
	// not seeded => not found
	svc := newTestSkillService(mem, &engine.MockEngine{})

	archive := makeSkillTarGz(t, map[string]string{"SKILL.md": "x"})
	err := svc.InstallSkillToEnvironment(ctx, "missing", "user", "adhoc", bytes.NewReader(archive))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
