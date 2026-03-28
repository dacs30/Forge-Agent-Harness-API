package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"haas/internal/api"
	"haas/internal/config"
	"haas/internal/engine"
	"haas/internal/lifecycle"
	"haas/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	_ = godotenv.Load() // load .env if present; silently ignored in production

	cfg := config.Load()

	if len(cfg.APIKeys) == 0 {
		logger.Error("HAAS_API_KEYS is required but not set")
		os.Exit(1)
	}

	memStore := store.NewMemoryStore(cfg.IdleTimeout, cfg.MaxLifetime)

	eng, err := engine.NewDockerEngine(cfg, logger)
	if err != nil {
		logger.Error("failed to create docker engine", "error", err)
		os.Exit(1)
	}

	reaper := lifecycle.NewReaper(memStore, eng, logger, cfg.IdleTimeout, cfg.MaxLifetime)
	go reaper.Start()

	router := api.NewRouter(memStore, eng, logger, cfg)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // disabled for streaming
		IdleTimeout:  120 * time.Second,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("starting server", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	logger.Info("shutting down")

	reaper.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}
