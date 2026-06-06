package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/service"
	"haas/internal/store"
)

type SnapshotHandler struct {
	service *service.SnapshotService
}

func NewSnapshotHandler(svc *service.SnapshotService) *SnapshotHandler {
	return &SnapshotHandler{service: svc}
}

type createSnapshotRequest struct {
	Label string `json:"label"`
}

// Create snapshots a running environment's filesystem.
// POST /v1/environments/{id}/snapshots
func (h *SnapshotHandler) Create(w http.ResponseWriter, r *http.Request) {
	envID := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())
	tenantID := auth.TenantIDFromContext(r.Context())

	var req createSnapshotRequest
	// Label is optional — ignore decode error and proceed with empty label.
	_ = decodeJSON(r, &req)

	snap, err := h.service.CreateSnapshot(r.Context(), tenantID, userID, envID, req.Label)
	if err != nil {
		writeCreateSnapshotError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, snap)
}

// List returns all snapshots for the authenticated user.
// GET /v1/snapshots
func (h *SnapshotHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())

	snaps, err := h.service.ListSnapshots(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list snapshots")
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}

// Get returns a single snapshot by ID.
// GET /v1/snapshots/{id}
func (h *SnapshotHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	snap, err := h.service.GetSnapshot(r.Context(), id, userID)
	if err != nil {
		writeGetSnapshotError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// ListTenant returns all snapshots owned by the tenant across all end-users.
// GET /v1/tenant/snapshots
func (h *SnapshotHandler) ListTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	snaps, err := h.service.ListTenantSnapshots(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list snapshots")
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}

// Delete removes a snapshot and its underlying Docker image.
// DELETE /v1/snapshots/{id}
func (h *SnapshotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	if err := h.service.DeleteSnapshot(r.Context(), id, userID); err != nil {
		writeDeleteSnapshotError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeCreateSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrGetEnvironment):
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	case errors.Is(err, service.ErrEnvironmentNotRunning):
		writeError(w, http.StatusConflict, service.ErrEnvironmentNotRunning.Error())
	case errors.Is(err, service.ErrStoreSnapshot):
		writeError(w, http.StatusInternalServerError, "failed to store snapshot")
	default:
		writeError(w, http.StatusInternalServerError, "failed to create snapshot")
	}
}

func writeGetSnapshotError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "snapshot not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to get snapshot")
}

func writeDeleteSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "snapshot not found")
	case errors.Is(err, service.ErrGetSnapshot):
		writeError(w, http.StatusInternalServerError, "failed to get snapshot")
	default:
		writeError(w, http.StatusInternalServerError, "failed to delete snapshot")
	}
}
