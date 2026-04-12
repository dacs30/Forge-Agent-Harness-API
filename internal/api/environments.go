package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

type EnvironmentHandler struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
	config *config.Config
}

func NewEnvironmentHandler(s store.Store, e engine.Engine, l *slog.Logger, cfg *config.Config) *EnvironmentHandler {
	return &EnvironmentHandler{store: s, engine: e, logger: l, config: cfg}
}

type CreateEnvironmentRequest struct {
	Image         string            `json:"image"`
	CPU           float64           `json:"cpu"`
	MemoryMB      int64             `json:"memory_mb"`
	DiskMB        int64             `json:"disk_mb"`
	NetworkPolicy string            `json:"network_policy"`
	EnvVars       map[string]string `json:"env_vars"`
}

type CreateEnvironmentResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Image  string `json:"image"`
}

func (h *EnvironmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateEnvironmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Image == "" {
		writeError(w, http.StatusBadRequest, "image is required")
		return
	}

	if len(h.config.AllowedImages) > 0 && !imageAllowed(req.Image, h.config.AllowedImages) {
		writeError(w, http.StatusForbidden, "image not allowed: "+req.Image)
		return
	}

	// Apply defaults
	if req.CPU <= 0 {
		req.CPU = h.config.DefaultCPU
	}
	if req.MemoryMB <= 0 {
		req.MemoryMB = h.config.DefaultMemoryMB
	}
	if req.DiskMB <= 0 {
		req.DiskMB = h.config.DefaultDiskMB
	}
	if req.NetworkPolicy == "" {
		req.NetworkPolicy = h.config.DefaultNetworkPolicy
	}

	// Validate limits
	if req.CPU < 0.1 || req.CPU > 4 {
		writeError(w, http.StatusBadRequest, "cpu must be between 0.1 and 4")
		return
	}
	if req.MemoryMB < 128 || req.MemoryMB > 8192 {
		writeError(w, http.StatusBadRequest, "memory_mb must be between 128 and 8192")
		return
	}

	np := domain.NetworkPolicy(req.NetworkPolicy)
	if !np.Valid() {
		writeError(w, http.StatusBadRequest, "network_policy must be none, egress-limited, or full")
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	now := time.Now()
	env := &domain.Environment{
		ID:     "env_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
		UserID: userID,
		Spec: domain.EnvironmentSpec{
			Image:         req.Image,
			CPU:           req.CPU,
			MemoryMB:      req.MemoryMB,
			DiskMB:        req.DiskMB,
			NetworkPolicy: np,
			EnvVars:       req.EnvVars,
		},
		Status:     domain.StatusCreating,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(h.config.MaxLifetime),
	}

	// Store first so we can track the environment
	if err := h.store.Create(r.Context(), env); err != nil {
		h.logger.Error("failed to store environment", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create environment")
		return
	}

	// Create and start container
	containerID, err := h.engine.CreateContainer(r.Context(), env)
	if err != nil {
		if delErr := h.store.Delete(r.Context(), env.ID, userID); delErr != nil {
			h.logger.Error("failed to delete environment after container create failure", "error", delErr, "env_id", env.ID)
		}
		h.logger.Error("failed to create container", "error", err, "env_id", env.ID)
		writeError(w, http.StatusInternalServerError, "failed to create container")
		return
	}

	env.ContainerID = containerID
	if err := h.engine.StartContainer(r.Context(), containerID); err != nil {
		h.engine.StopContainer(r.Context(), containerID)
		if delErr := h.store.Delete(r.Context(), env.ID, userID); delErr != nil {
			h.logger.Error("failed to delete environment after container start failure", "error", delErr, "env_id", env.ID)
		}
		h.logger.Error("failed to start container", "error", err, "env_id", env.ID)
		writeError(w, http.StatusInternalServerError, "failed to start container")
		return
	}

	env.Status = domain.StatusRunning
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update environment status to running", "error", err, "env_id", env.ID)
	}

	writeJSON(w, http.StatusCreated, CreateEnvironmentResponse{
		ID:     env.ID,
		Status: string(env.Status),
		Image:  env.Spec.Image,
	})
}

func (h *EnvironmentHandler) Destroy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	env, err := h.store.Get(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "environment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get environment")
		return
	}

	if env.ContainerID != "" {
		if err := h.engine.StopContainer(r.Context(), env.ContainerID); err != nil {
			h.logger.Error("failed to stop container", "error", err, "env_id", id)
		}
	}

	if err := h.store.Delete(r.Context(), id, userID); err != nil {
		h.logger.Error("failed to delete environment record", "error", err, "env_id", id)
		writeError(w, http.StatusInternalServerError, "failed to delete environment")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *EnvironmentHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	env, err := h.store.Get(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "environment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get environment")
		return
	}

	writeJSON(w, http.StatusOK, env)
}

func (h *EnvironmentHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())

	envs, err := h.store.List(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list environments")
		return
	}
	writeJSON(w, http.StatusOK, envs)
}

func imageAllowed(image string, allowlist []string) bool {
	for _, allowed := range allowlist {
		if image == allowed {
			return true
		}
	}
	return false
}
