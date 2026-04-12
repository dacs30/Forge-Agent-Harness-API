package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/engine"
	"haas/internal/store"
)

func NewRouter(s store.Store, e engine.Engine, logger *slog.Logger, cfg *config.Config, authMgr *auth.Manager) http.Handler {
	r := chi.NewRouter()

	// Middleware stack
	r.Use(RequestIDMiddleware)
	r.Use(RecoveryMiddleware(logger))
	r.Use(LoggingMiddleware(logger))
	r.Use(middleware.RealIP)

	// Health check — no auth required
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	envHandler := NewEnvironmentHandler(s, e, logger, cfg)
	execHandler := NewExecHandler(s, e, logger)
	filesHandler := NewFilesHandler(s, e, logger, cfg)

	r.Route("/v1/environments", func(r chi.Router) {
		r.Use(authMgr.Middleware())
		r.Post("/", envHandler.Create)
		r.Get("/", envHandler.List)

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", envHandler.Get)
			r.Delete("/", envHandler.Destroy)
			r.Post("/exec", execHandler.Exec)

			r.Route("/files", func(r chi.Router) {
				r.Get("/", filesHandler.List)
				r.Get("/content", filesHandler.Read)
				r.Put("/content", filesHandler.Write)
			})
		})
	})

	return r
}
