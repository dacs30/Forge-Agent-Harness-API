package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/service"
	"haas/internal/store"
)

type ExecHandler struct {
	service *service.ExecService
}

func NewExecHandler(svc *service.ExecService) *ExecHandler {
	return &ExecHandler{service: svc}
}

func (h *ExecHandler) Exec(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	target, err := h.service.PrepareExec(r.Context(), id, userID)
	if err != nil {
		writePrepareExecError(w, err)
		return
	}

	var req domain.ExecRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Apply timeout
	ctx := r.Context()
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	session, err := h.service.StartExec(ctx, r.Context(), target, req)
	if err != nil {
		writeStartExecError(w, err)
		return
	}
	defer session.Reader.Close()

	// Stream NDJSON response
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)

	err = engine.DemuxDockerStream(session.Reader, func(stream string, data []byte) error {
		event := domain.ExecEvent{
			Stream: stream,
			Data:   string(data),
		}
		if err := encoder.Encode(event); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})

	if err != nil {
		h.service.LogStreamError(session, err)
	}

	// Get exit code
	if exitCode, ok := h.service.ExitCode(ctx, session); ok {
		event := domain.ExecEvent{
			Stream: "exit",
			Data:   intToString(exitCode),
		}
		encoder.Encode(event)
		flusher.Flush()
	}
}

func writePrepareExecError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrEnvironmentNotRunning):
		writeError(w, http.StatusConflict, "environment is not running")
	default:
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	}
}

func writeStartExecError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrCommandRequired):
		writeError(w, http.StatusBadRequest, "command is required")
	default:
		writeError(w, http.StatusInternalServerError, "exec failed")
	}
}

func intToString(i int) string {
	return fmt.Sprintf("%d", i)
}
