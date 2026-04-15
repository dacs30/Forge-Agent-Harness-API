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

const (
	// wsMaxMessageBytes caps incoming WebSocket frame size. Input frames
	// (keystrokes, resize events) are tiny JSON objects; 32 KB is generous.
	// Frames exceeding this limit cause gorilla to close the connection with 1009.
	wsMaxMessageBytes = 32 * 1024

	// wsPongWait is how long the server waits for a pong before declaring the
	// client dead and closing the connection.
	wsPongWait = 60 * time.Second

	// wsPingInterval is how often the server sends a ping. Must be less than
	// wsPongWait so the client has time to reply before the deadline fires.
	wsPingInterval = (wsPongWait * 9) / 10 // 54 s

	// wsWriteWait is the per-write deadline. A stuck or slow client will be
	// disconnected after this many seconds rather than blocking indefinitely.
	wsWriteWait = 10 * time.Second
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

	// Resource limits: cap incoming frame size and enforce a read deadline
	// that is reset by each pong. This prevents:
	//   • Memory exhaustion from huge frames (SetReadLimit)
	//   • Connections held open indefinitely by silent clients (pong deadline)
	conn.SetReadLimit(wsMaxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(wsPongWait)) //nolint:errcheck
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

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
		conn.Close()
		return
	}

	h.logger.Info("websocket exec started", "env_id", id, "command", cmd)

	// closeAll shuts down both the exec session and the WebSocket connection
	// exactly once, regardless of which side exits first.
	//
	// Why both must be closed together:
	//   • Closing session unblocks session.Reader().Read() in the Docker→WS goroutine.
	//   • Closing conn   unblocks conn.ReadMessage()      in the WS→Docker goroutine.
	// Without this, whichever goroutine exits first leaves the other stuck
	// forever on a blocking read, and wg.Wait() never returns.
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			cancel()
			session.Close()
			conn.Close()
		})
	}
	defer closeAll()

	// mu guards all WebSocket writes — gorilla requires single-writer.
	var mu sync.Mutex

	// Ping goroutine: sends periodic pings so the pong-based read deadline
	// does not fire on otherwise-idle-but-alive connections.
	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				// WriteControl is safe to call concurrently with WriteMessage.
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
					closeAll()
					return
				}
			}
		}
	}()

	// Keep-alive: update LastUsedAt every 30 s so the reaper does not kill the
	// container while a terminal session is in progress. Without this, any
	// session longer than the idle timeout (default 10 min) would be reaped.
	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-keepAlive.C:
				env.LastUsedAt = time.Now()
				if err := h.store.Update(context.Background(), env); err != nil {
					h.logger.Warn("failed to refresh last-used during ws session", "error", err, "env_id", id)
				}
			}
		}
	}()

	// Record initial session start.
	env.LastUsedAt = time.Now()
	if err := h.store.Update(r.Context(), env); err != nil {
		h.logger.Error("failed to update last used time", "error", err, "env_id", id)
	}

	var wg sync.WaitGroup

	// Docker → WebSocket: stream TTY output.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer closeAll() // unblocks conn.ReadMessage() in the peer goroutine

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

		// Fetch exit code once the process finishes. Use a fresh context because
		// the session context may already be cancelled (e.g. client disconnected).
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer exitCancel()
		exitCode, err := h.engine.ExecExitCode(exitCtx, session.ExecID())
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
		defer closeAll() // unblocks session.Reader().Read() in the peer goroutine

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

// wsSend serialises msg as JSON, sets a per-write deadline, and writes it as a
// WebSocket text frame. mu may be nil when called from a single goroutine.
// The write deadline prevents a slow or stuck client from blocking the goroutine
// indefinitely.
func wsSend(conn *websocket.Conn, mu *sync.Mutex, msg domain.WSOutputMessage) error {
	data, _ := json.Marshal(msg)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	conn.SetWriteDeadline(time.Now().Add(wsWriteWait)) //nolint:errcheck
	return conn.WriteMessage(websocket.TextMessage, data)
}
