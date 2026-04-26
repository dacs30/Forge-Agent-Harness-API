package lifecycle

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"haas/internal/engine"
	"haas/internal/store"
)

type Reaper struct {
	store       store.Store
	engine      engine.Engine
	logger      *slog.Logger
	idleTimeout time.Duration
	maxLifetime time.Duration
	interval    time.Duration
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

func NewReaper(s store.Store, e engine.Engine, l *slog.Logger, idleTimeout, maxLifetime time.Duration) *Reaper {
	return &Reaper{
		store:       s,
		engine:      e,
		logger:      l,
		idleTimeout: idleTimeout,
		maxLifetime: maxLifetime,
		interval:    30 * time.Second,
		stopCh:      make(chan struct{}),
	}
}

func (r *Reaper) Start() {
	r.wg.Add(1)
	defer r.wg.Done()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info("reaper started", "interval", r.interval, "idle_timeout", r.idleTimeout, "max_lifetime", r.maxLifetime)

	for {
		select {
		case <-ticker.C:
			r.reap()
		case <-r.stopCh:
			r.logger.Info("reaper stopped")
			return
		}
	}
}

func (r *Reaper) Stop() {
	close(r.stopCh)
	r.wg.Wait()
}

func (r *Reaper) reap() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	expired, err := r.store.ListExpired(ctx)
	if err != nil {
		r.logger.Error("failed to list expired environments", "error", err)
		return
	}

	for _, env := range expired {
		r.logger.Info("reaping expired environment",
			"env_id", env.ID,
			"last_used", env.LastUsedAt,
			"expires_at", env.ExpiresAt,
		)

		if env.ContainerID != "" {
			if err := r.engine.StopContainer(ctx, env.ContainerID); err != nil {
				// Container may already be gone (OOM-killed, manual removal, etc.).
				// Log and proceed so the store record is always cleaned up.
				r.logger.Warn("failed to stop container during reap (proceeding with store delete)", "error", err, "env_id", env.ID)
			}
		}

		if err := r.store.Delete(ctx, env.ID, ""); err != nil { // "" = admin bypass, cleans all tenants
			r.logger.Error("failed to delete environment during reap", "error", err, "env_id", env.ID)
		}
	}

	if len(expired) > 0 {
		r.logger.Info("reaper cycle complete", "reaped", len(expired))
	}
}
