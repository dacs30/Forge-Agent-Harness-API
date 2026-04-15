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
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

type SnapshotHandler struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
}

func NewSnapshotHandler(s store.Store, e engine.Engine, l *slog.Logger) *SnapshotHandler {
	return &SnapshotHandler{store: s, engine: e, logger: l}
}

type createSnapshotRequest struct {
	Label string `json:"label"`
}

// Create snapshots a running environment's filesystem.
// POST /v1/environments/{id}/snapshots
func (h *SnapshotHandler) Create(w http.ResponseWriter, r *http.Request) {
	envID := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	env, err := h.store.Get(r.Context(), envID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "environment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get environment")
		return
	}

	if env.Status != domain.StatusRunning {
		writeError(w, http.StatusConflict, "environment must be running to create a snapshot")
		return
	}

	var req createSnapshotRequest
	// Label is optional — ignore decode error and proceed with empty label.
	_ = decodeJSON(r, &req)

	snapID := "snap_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]

	imageID, err := h.engine.SnapshotContainer(r.Context(), env.ContainerID, snapID)
	if err != nil {
		h.logger.Error("failed to snapshot container", "error", err, "env_id", envID)
		writeError(w, http.StatusInternalServerError, "failed to create snapshot")
		return
	}

	snap := &domain.Snapshot{
		ID:            snapID,
		UserID:        userID,
		EnvironmentID: envID,
		ImageID:       imageID,
		Label:         req.Label,
		CreatedAt:     time.Now(),
	}

	if err := h.store.CreateSnapshot(r.Context(), snap); err != nil {
		h.logger.Error("failed to store snapshot", "error", err, "snap_id", snapID)
		// Best-effort cleanup of the Docker image we just created.
		if delErr := h.engine.DeleteSnapshotImage(r.Context(), imageID); delErr != nil {
			h.logger.Error("failed to clean up snapshot image after store error", "error", delErr, "image_id", imageID)
		}
		writeError(w, http.StatusInternalServerError, "failed to store snapshot")
		return
	}

	h.logger.Info("snapshot created", "snap_id", snapID, "env_id", envID)
	writeJSON(w, http.StatusCreated, snap)
}

// List returns all snapshots for the authenticated user.
// GET /v1/snapshots
func (h *SnapshotHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())

	snaps, err := h.store.ListSnapshots(r.Context(), userID)
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

	snap, err := h.store.GetSnapshot(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get snapshot")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// Delete removes a snapshot and its underlying Docker image.
// DELETE /v1/snapshots/{id}
func (h *SnapshotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	snap, err := h.store.GetSnapshot(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get snapshot")
		return
	}

	// Remove the Docker image first. Log but don't abort if it's already gone.
	if err := h.engine.DeleteSnapshotImage(r.Context(), snap.ImageID); err != nil {
		h.logger.Warn("failed to delete snapshot image (may already be removed)", "error", err, "image_id", snap.ImageID)
	}

	if err := h.store.DeleteSnapshot(r.Context(), id, userID); err != nil {
		h.logger.Error("failed to delete snapshot record", "error", err, "snap_id", id)
		writeError(w, http.StatusInternalServerError, "failed to delete snapshot")
		return
	}

	h.logger.Info("snapshot deleted", "snap_id", id)
	w.WriteHeader(http.StatusNoContent)
}
