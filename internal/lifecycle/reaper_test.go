package lifecycle

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

func TestReaper_ReapsExpiredEnvironments(t *testing.T) {
	s := store.NewMemoryStore(1*time.Millisecond, 1*time.Millisecond)
	stoppedContainers := make([]string, 0)
	e := &engine.MockEngine{
		StopContainerFn: func(ctx context.Context, containerID string) error {
			stoppedContainers = append(stoppedContainers, containerID)
			return nil
		},
	}
	l := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Add an expired environment
	env := &domain.Environment{
		ID:          "env_expired",
		Status:      domain.StatusRunning,
		ContainerID: "container_expired",
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		LastUsedAt:  time.Now().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().Add(-1 * time.Hour),
	}
	s.Create(context.Background(), env)

	r := NewReaper(s, e, l, 1*time.Millisecond, 1*time.Millisecond)
	r.reap()

	if len(stoppedContainers) != 1 {
		t.Fatalf("expected 1 stopped container, got %d", len(stoppedContainers))
	}
	if stoppedContainers[0] != "container_expired" {
		t.Fatalf("expected container_expired, got %s", stoppedContainers[0])
	}

	// Verify env was deleted from store
	_, err := s.Get(context.Background(), "env_expired", "")
	if err != store.ErrNotFound {
		t.Fatalf("expected env to be deleted from store")
	}
}
