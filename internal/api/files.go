package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

// sanitizePath resolves traversal sequences by forcing the path to be rooted at
// "/" before cleaning, so "../../etc/passwd" becomes "/etc/passwd" and can
// never escape the container's filesystem root.
func sanitizePath(raw string) (string, error) {
	if strings.ContainsAny(raw, "\x00\r\n") {
		return "", fmt.Errorf("path contains invalid characters")
	}
	return filepath.Clean("/" + raw), nil
}

type FilesHandler struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
	cfg    *config.Config
}

func NewFilesHandler(s store.Store, e engine.Engine, l *slog.Logger, cfg *config.Config) *FilesHandler {
	return &FilesHandler{store: s, engine: e, logger: l, cfg: cfg}
}

func (h *FilesHandler) getRunningEnv(w http.ResponseWriter, r *http.Request) (*domain.Environment, bool) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	env, err := h.store.Get(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "environment not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to get environment")
		return nil, false
	}

	if env.Status != domain.StatusRunning {
		writeError(w, http.StatusConflict, "environment is not running")
		return nil, false
	}

	return env, true
}

func (h *FilesHandler) List(w http.ResponseWriter, r *http.Request) {
	env, ok := h.getRunningEnv(w, r)
	if !ok {
		return
	}

	raw := r.URL.Query().Get("path")
	if raw == "" {
		raw = "/"
	}
	path, err := sanitizePath(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	files, err := h.engine.ListFiles(r.Context(), env.ContainerID, path)
	if err != nil {
		h.logger.Error("list files failed", "error", err, "env_id", env.ID, "path", path)
		writeError(w, http.StatusInternalServerError, "failed to list files")
		return
	}

	env.LastUsedAt = time.Now()
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update environment last used time", "error", err, "env_id", env.ID)
	}

	writeJSON(w, http.StatusOK, files)
}

func (h *FilesHandler) Read(w http.ResponseWriter, r *http.Request) {
	env, ok := h.getRunningEnv(w, r)
	if !ok {
		return
	}

	raw := r.URL.Query().Get("path")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	path, err := sanitizePath(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	reader, err := h.engine.ReadFile(r.Context(), env.ContainerID, path)
	if err != nil {
		h.logger.Error("read file failed", "error", err, "env_id", env.ID, "path", path)
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}
	defer reader.Close()

	env.LastUsedAt = time.Now()
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update environment last used time", "error", err, "env_id", env.ID)
	}

	fileName := filepath.Base(path)
	contentType := mime.TypeByExtension(filepath.Ext(fileName))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fileName}))
	io.Copy(w, reader)
}

func (h *FilesHandler) Write(w http.ResponseWriter, r *http.Request) {
	env, ok := h.getRunningEnv(w, r)
	if !ok {
		return
	}

	raw := r.URL.Query().Get("path")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	path, err := sanitizePath(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	data, err := readBodyMax(r, h.cfg.MaxFileUploadMB<<20)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	if err := h.engine.WriteFile(r.Context(), env.ContainerID, path, bytes.NewReader(data)); err != nil {
		h.logger.Error("write file failed", "error", err, "env_id", env.ID, "path", path)
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	env.LastUsedAt = time.Now()
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update environment last used time", "error", err, "env_id", env.ID)
	}

	w.WriteHeader(http.StatusNoContent)
}
