package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

type ExecHandler struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
}

func NewExecHandler(s store.Store, e engine.Engine, l *slog.Logger) *ExecHandler {
	return &ExecHandler{store: s, engine: e, logger: l}
}

func (h *ExecHandler) Exec(w http.ResponseWriter, r *http.Request) {
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

	if env.Status != domain.StatusRunning {
		writeError(w, http.StatusConflict, "environment is not running")
		return
	}

	var req domain.ExecRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(req.Command) == 0 {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	// Apply timeout
	ctx := r.Context()
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	h.logger.Info("executing command",
		"env_id", id,
		"command", req.Command,
		"working_dir", req.WorkingDir,
	)

	reader, err := h.engine.Exec(ctx, env.ContainerID, req)
	if err != nil {
		h.logger.Error("exec failed", "error", err, "env_id", id)
		writeError(w, http.StatusInternalServerError, "exec failed")
		return
	}
	defer reader.Close()

	// Update last used timestamp
	env.LastUsedAt = time.Now()
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update environment last used time", "error", err, "env_id", id)
	}

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

	err = engine.DemuxDockerStream(reader, func(stream string, data []byte) error {
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
		h.logger.Error("stream error", "error", err, "env_id", id)
	}

	// Get exit code
	type execIDer interface {
		ExecID() string
	}
	if e, ok := reader.(execIDer); ok {
		exitCode, err := h.engine.ExecExitCode(ctx, e.ExecID())
		if err != nil {
			h.logger.Error("failed to get exit code", "error", err, "env_id", id)
			exitCode = -1
		}
		event := domain.ExecEvent{
			Stream: "exit",
			Data:   intToString(exitCode),
		}
		encoder.Encode(event)
		flusher.Flush()
	}
}

func intToString(i int) string {
	return fmt.Sprintf("%d", i)
}
