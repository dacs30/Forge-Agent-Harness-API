package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/service"
	"haas/internal/store"
)

type FilesHandler struct {
	service *service.FileService
}

func NewFilesHandler(svc *service.FileService) *FilesHandler {
	return &FilesHandler{service: svc}
}

func (h *FilesHandler) List(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())
	raw := r.URL.Query().Get("path")

	files, err := h.service.ListFiles(r.Context(), id, userID, raw)
	if err != nil {
		writeListFilesError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, files)
}

func (h *FilesHandler) Read(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())
	raw := r.URL.Query().Get("path")

	path, reader, err := h.service.ReadFile(r.Context(), id, userID, raw)
	if err != nil {
		writeReadFileError(w, err)
		return
	}
	defer reader.Close()

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
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())
	raw := r.URL.Query().Get("path")

	if err := h.service.WriteFile(r.Context(), id, userID, raw, r.Body); err != nil {
		writeWriteFileError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeListFilesError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrGetEnvironment):
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	case errors.Is(err, service.ErrEnvironmentNotRunning):
		writeError(w, http.StatusConflict, "environment is not running")
	case errors.Is(err, service.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid path")
	default:
		writeError(w, http.StatusInternalServerError, "failed to list files")
	}
}

func writeReadFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrGetEnvironment):
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	case errors.Is(err, service.ErrEnvironmentNotRunning):
		writeError(w, http.StatusConflict, "environment is not running")
	case errors.Is(err, service.ErrPathRequired):
		writeError(w, http.StatusBadRequest, "path query parameter is required")
	case errors.Is(err, service.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid path")
	default:
		writeError(w, http.StatusInternalServerError, "failed to read file")
	}
}

func writeWriteFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrGetEnvironment):
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	case errors.Is(err, service.ErrEnvironmentNotRunning):
		writeError(w, http.StatusConflict, "environment is not running")
	case errors.Is(err, service.ErrPathRequired):
		writeError(w, http.StatusBadRequest, "path query parameter is required")
	case errors.Is(err, service.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid path")
	case errors.Is(err, service.ErrReadRequestBody):
		writeError(w, http.StatusBadRequest, "failed to read request body")
	default:
		writeError(w, http.StatusInternalServerError, "failed to write file")
	}
}
