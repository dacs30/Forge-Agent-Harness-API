package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"haas/internal/auth"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

var upgrader = websocket.Upgrader{
	// Allow all origins — callers are authenticated via Bearer token, not origin.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type ExecWSHandler struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
}

func NewExecWSHandler(s store.Store, e engine.Engine, l *slog.Logger) *ExecWSHandler {
	return &ExecWSHandler{store: s, engine: e, logger: l}
}

// ExecWS upgrades the connection to WebSocket and runs an interactive TTY session
// inside the environment. The command defaults to ["bash"] when not specified.
//
// Query parameters:
//
//	cmd         — command argument (repeatable, e.g. ?cmd=bash or ?cmd=python3&cmd=script.py)
//	working_dir — working directory inside the container
//
// Client → Server messages (JSON):
//
//	{"type":"input",  "data":"ls -la\n"}
//	{"type":"resize", "cols":120, "rows":40}
//
// Server → Client messages (JSON):
//
//	{"stream":"output", "data":"..."}   — merged stdout/stderr (TTY mode)
//	{"stream":"exit",   "data":"0"}     — process exited; data is the exit code string
//	{"stream":"error",  "data":"..."}   — internal error before the process started
func (h *ExecWSHandler) ExecWS(w http.ResponseWriter, r *http.Request) {
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

	cmd := r.URL.Query()["cmd"]
	if len(cmd) == 0 {
		cmd = []string{"bash"}
	}
	workingDir := r.URL.Query().Get("working_dir")

	// Upgrade HTTP → WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("websocket upgrade failed", "error", err, "env_id", id)
		return // upgrader already wrote the HTTP error
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	req := domain.ExecRequest{
		Command:    cmd,
		WorkingDir: workingDir,
	}

	session, err := h.engine.ExecInteractive(ctx, env.ContainerID, req)
	if err != nil {
		h.logger.Error("exec interactive failed", "error", err, "env_id", id)
		wsSend(conn, nil, domain.WSOutputMessage{Stream: "error", Data: "failed to start session"})
		return
	}
	defer session.Close()

	h.logger.Info("websocket exec started", "env_id", id, "command", cmd)

	env.LastUsedAt = time.Now()
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update last used time", "error", err, "env_id", id)
	}

	// mu guards all WebSocket writes — gorilla requires single-writer.
	var mu sync.Mutex

	var wg sync.WaitGroup

	// Docker → WebSocket: stream TTY output.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		buf := make([]byte, 32*1024)
		for {
			n, err := session.Reader().Read(buf)
			if n > 0 {
				msg := domain.WSOutputMessage{Stream: "output", Data: string(buf[:n])}
				if sendErr := wsSend(conn, &mu, msg); sendErr != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}

		// Fetch exit code once the process finishes.
		exitCode, err := h.engine.ExecExitCode(ctx, session.ExecID())
		if err != nil {
			h.logger.Error("failed to get exit code", "error", err, "env_id", id)
			exitCode = -1
		}
		wsSend(conn, &mu, domain.WSOutputMessage{Stream: "exit", Data: intToString(exitCode)}) //nolint:errcheck
	}()

	// WebSocket → Docker: forward input and resize events.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg domain.WSInputMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "input":
				if _, err := session.Write([]byte(msg.Data)); err != nil {
					return
				}
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					if err := session.Resize(ctx, msg.Cols, msg.Rows); err != nil {
						h.logger.Warn("resize failed", "error", err, "env_id", id)
					}
				}
			}
		}
	}()

	wg.Wait()
	h.logger.Info("websocket exec finished", "env_id", id)
}

// wsSend serialises msg as JSON and writes it as a WebSocket text frame.
// mu may be nil when called from a single goroutine.
func wsSend(conn *websocket.Conn, mu *sync.Mutex, msg domain.WSOutputMessage) error {
	data, _ := json.Marshal(msg)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}
