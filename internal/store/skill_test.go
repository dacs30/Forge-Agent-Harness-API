package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"haas/internal/domain"
)

func skillStores(t *testing.T) map[string]Store {
	t.Helper()
	return map[string]Store{
		"memory": NewMemoryStore(5*time.Minute, 30*time.Minute),
		"sql":    newTestSQLStore(t),
	}
}

func newSkill(id, userID, tenantID, name string) *domain.Skill {
	now := time.Now().UTC().Truncate(time.Second)
	return &domain.Skill{
		ID:        id,
		UserID:    userID,
		TenantID:  tenantID,
		Name:      name,
		SizeBytes: 0,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestStore_SkillCRUD(t *testing.T) {
	for backend, s := range skillStores(t) {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()

			skill := newSkill("skill_a", "user1", "tenant1", "my-skill")
			skill.SizeBytes = 3
			if err := s.UpsertSkill(ctx, skill, []byte("abc")); err != nil {
				t.Fatalf("upsert: %v", err)
			}

			got, err := s.GetSkill(ctx, "skill_a", "user1")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Name != "my-skill" || got.SizeBytes != 3 {
				t.Fatalf("unexpected skill: %+v", got)
			}

			archive, err := s.GetSkillArchive(ctx, "skill_a", "user1")
			if err != nil {
				t.Fatalf("get archive: %v", err)
			}
			if string(archive) != "abc" {
				t.Fatalf("archive mismatch: %q", archive)
			}

			// Tenant isolation: another user cannot read it.
			if _, err := s.GetSkill(ctx, "skill_a", "user2"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound for other user, got %v", err)
			}

			// List scoped to user.
			skills, err := s.ListSkills(ctx, "user1")
			if err != nil || len(skills) != 1 {
				t.Fatalf("list user1: %v len=%d", err, len(skills))
			}

			// Delete.
			if err := s.DeleteSkill(ctx, "skill_a", "user1"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := s.GetSkill(ctx, "skill_a", "user1"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound after delete, got %v", err)
			}
		})
	}
}

func TestStore_SkillUpsertReplacesByName(t *testing.T) {
	for backend, s := range skillStores(t) {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()

			first := newSkill("skill_first", "user1", "tenant1", "dup")
			if err := s.UpsertSkill(ctx, first, []byte("v1")); err != nil {
				t.Fatalf("upsert first: %v", err)
			}

			// Re-register the same (user, name) with a new generated ID; the store
			// must keep the original ID and replace the archive.
			second := newSkill("skill_second", "user1", "tenant1", "dup")
			if err := s.UpsertSkill(ctx, second, []byte("v2")); err != nil {
				t.Fatalf("upsert second: %v", err)
			}
			if second.ID != "skill_first" {
				t.Fatalf("expected stable id skill_first, got %q", second.ID)
			}

			skills, err := s.ListSkills(ctx, "user1")
			if err != nil || len(skills) != 1 {
				t.Fatalf("expected 1 skill, got %d (err %v)", len(skills), err)
			}
			archive, _ := s.GetSkillArchive(ctx, "skill_first", "user1")
			if string(archive) != "v2" {
				t.Fatalf("expected replaced archive v2, got %q", archive)
			}
		})
	}
}

func TestStore_ListSkillsByTenant(t *testing.T) {
	for backend, s := range skillStores(t) {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()

			if err := s.UpsertSkill(ctx, newSkill("s1", "userA", "tenant1", "one"), []byte("a")); err != nil {
				t.Fatalf("upsert s1: %v", err)
			}
			if err := s.UpsertSkill(ctx, newSkill("s2", "userB", "tenant1", "two"), []byte("b")); err != nil {
				t.Fatalf("upsert s2: %v", err)
			}
			if err := s.UpsertSkill(ctx, newSkill("s3", "userC", "tenant2", "three"), []byte("c")); err != nil {
				t.Fatalf("upsert s3: %v", err)
			}

			skills, err := s.ListSkillsByTenant(ctx, "tenant1")
			if err != nil {
				t.Fatalf("list by tenant: %v", err)
			}
			if len(skills) != 2 {
				t.Fatalf("expected 2 skills for tenant1, got %d", len(skills))
			}
		})
	}
}
