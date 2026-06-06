package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/service"
	"haas/internal/store"
)

type EnvironmentHandler struct {
	service *service.EnvironmentService
}

func NewEnvironmentHandler(svc *service.EnvironmentService) *EnvironmentHandler {
	return &EnvironmentHandler{service: svc}
}

type CreateEnvironmentRequest struct {
	Image         string            `json:"image"`
	CPU           float64           `json:"cpu"`
	MemoryMB      int64             `json:"memory_mb"`
	DiskMB        int64             `json:"disk_mb"`
	NetworkPolicy string            `json:"network_policy"`
	EnvVars       map[string]string `json:"env_vars"`
	SnapshotID    string            `json:"snapshot_id"`
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

	userID := auth.UserIDFromContext(r.Context())
	tenantID := auth.TenantIDFromContext(r.Context())

	env, err := h.service.CreateEnvironment(r.Context(), tenantID, userID, service.CreateEnvironmentInput{
		Image:         req.Image,
		CPU:           req.CPU,
		MemoryMB:      req.MemoryMB,
		DiskMB:        req.DiskMB,
		NetworkPolicy: req.NetworkPolicy,
		EnvVars:       req.EnvVars,
		SnapshotID:    req.SnapshotID,
	})
	if err != nil {
		writeCreateEnvironmentError(w, err)
		return
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

	if err := h.service.DestroyEnvironment(r.Context(), id, userID); err != nil {
		writeDestroyEnvironmentError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *EnvironmentHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	env, err := h.service.GetEnvironment(r.Context(), id, userID)
	if err != nil {
		writeGetEnvironmentError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, env)
}

func (h *EnvironmentHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())

	envs, err := h.service.ListEnvironments(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list environments")
		return
	}
	writeJSON(w, http.StatusOK, envs)
}

// ListTenant returns all environments owned by the tenant across all end-users.
// GET /v1/tenant/environments
func (h *EnvironmentHandler) ListTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	envs, err := h.service.ListTenantEnvironments(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list environments")
		return
	}
	writeJSON(w, http.StatusOK, envs)
}

func writeCreateEnvironmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrImageRequired):
		writeError(w, http.StatusBadRequest, service.ErrImageRequired.Error())
	case errors.Is(err, service.ErrImageNotAllowed):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrInvalidCPU):
		writeError(w, http.StatusBadRequest, service.ErrInvalidCPU.Error())
	case errors.Is(err, service.ErrInvalidMemory):
		writeError(w, http.StatusBadRequest, service.ErrInvalidMemory.Error())
	case errors.Is(err, service.ErrInvalidNetwork):
		writeError(w, http.StatusBadRequest, service.ErrInvalidNetwork.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "snapshot not found")
	case errors.Is(err, service.ErrGetSnapshot):
		writeError(w, http.StatusInternalServerError, "failed to get snapshot")
	case errors.Is(err, service.ErrCreateContainer):
		writeError(w, http.StatusInternalServerError, "failed to create container")
	case errors.Is(err, service.ErrStartContainer):
		writeError(w, http.StatusInternalServerError, "failed to start container")
	default:
		writeError(w, http.StatusInternalServerError, "failed to create environment")
	}
}

func writeDestroyEnvironmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrGetEnvironment):
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	default:
		writeError(w, http.StatusInternalServerError, "failed to delete environment")
	}
}

func writeGetEnvironmentError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to get environment")
}
