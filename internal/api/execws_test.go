package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"haas/internal/auth"
	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

// ---------------------------------------------------------------------------
// Mock interactive session
// ---------------------------------------------------------------------------

type resizeCall struct{ cols, rows uint }

// mockSession is a controllable InteractiveSession for WebSocket tests.
// Writes and resizes are signalled via buffered channels so tests can
// synchronise without sleeps.
type mockSession struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	writeCh   chan []byte    // receives a copy of each Write payload
	resizeCh  chan resizeCall
	closeOnce sync.Once
}

func newMockSession() *mockSession {
	pr, pw := io.Pipe()
	return &mockSession{
		pr:       pr,
		pw:       pw,
		writeCh:  make(chan []byte, 32),
		resizeCh: make(chan resizeCall, 32),
	}
}

func (s *mockSession) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	s.writeCh <- cp
	return len(p), nil
}

func (s *mockSession) Reader() io.Reader { return s.pr }

func (s *mockSession) Resize(_ context.Context, cols, rows uint) error {
	s.resizeCh <- resizeCall{cols, rows}
	return nil
}

// Close is idempotent. Closing pw signals EOF to the pipe reader which
// unblocks the Docker→WS goroutine inside the handler.
func (s *mockSession) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.pw.Close() })
	return err
}

func (s *mockSession) ExecID() string { return "mock-exec-id" }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// wsTestServer spins up an httptest.Server wired to a router that uses the
// given session. exitCode is returned by the mock ExecExitCode.
// The server is automatically closed when the test ends.
func wsTestServer(t *testing.T, sess *mockSession, exitCode int) (*httptest.Server, store.Store, *auth.Manager) {
	t.Helper()

	s := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	e := &engine.MockEngine{}

	if sess != nil {
		e.ExecInteractiveFn = func(_ context.Context, _ string, _ domain.ExecRequest) (engine.InteractiveSession, error) {
			return sess, nil
		}
	}
	e.ExecExitCodeFn = func(_ context.Context, _ string) (int, error) {
		return exitCode, nil
	}

	l := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Load()
	cfg.APIKeys = []string{testAPIKey}
	mgr := auth.New(cfg.APIKeys)

	router := NewRouter(s, e, l, cfg, mgr)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return srv, s, mgr
}

// wsDialEnv dials the WS exec endpoint for envID using testAPIKey.
// It sets a 5 s read deadline so tests never hang indefinitely.
func wsDialEnv(t *testing.T, srv *httptest.Server, envID string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/environments/" + envID + "/exec/ws"
	header := http.Header{"Authorization": []string{"Bearer " + testAPIKey}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	t.Cleanup(func() { conn.Close() })
	return conn
}

// wsDialEnvExpectFail dials the WS endpoint and expects the server to reject
// the upgrade. It returns the HTTP response status code.
func wsDialEnvExpectFail(t *testing.T, srv *httptest.Server, envID string, authHeader string) int {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/environments/" + envID + "/exec/ws"
	var header http.Header
	if authHeader != "" {
		header = http.Header{"Authorization": []string{authHeader}}
	}
	_, resp, err := websocket.DefaultDialer.Dial(url, header)
	if err == nil {
		t.Fatal("expected ws dial to fail, but it succeeded")
	}
	if resp == nil {
		t.Fatal("expected HTTP response alongside ws dial error")
	}
	return resp.StatusCode
}

// nextMsg reads and decodes the next WebSocket output message. Fails the test
// if the read or decode fails.
func nextMsg(t *testing.T, conn *websocket.Conn) domain.WSOutputMessage {
	t.Helper()
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}
	var msg domain.WSOutputMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal ws message: %v", err)
	}
	return msg
}

// drainUntilExit reads messages until it finds one with stream=="exit",
// collecting all output along the way. Fails the test on timeout.
func drainUntilExit(t *testing.T, conn *websocket.Conn) (output string, exitData string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	for {
		msg := nextMsg(t, conn)
		switch msg.Stream {
		case "output":
			output += msg.Data
		case "exit":
			return output, msg.Data
		}
	}
}

// seedRunningEnv creates a running environment in the store owned by userID.
func seedRunningEnv(t *testing.T, s store.Store, id, userID string) {
	t.Helper()
	env := &domain.Environment{
		ID:          id,
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "container_" + id,
		Spec:        domain.EnvironmentSpec{Image: "alpine:latest"},
		CreatedAt:   time.Now(),
		LastUsedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(60 * time.Minute),
	}
	if err := s.Create(context.Background(), env); err != nil {
		t.Fatalf("seed running env %s: %v", id, err)
	}
}

// ---------------------------------------------------------------------------
// Upgrade path and access control
// ---------------------------------------------------------------------------

func TestExecWS_NoAuth(t *testing.T) {
	srv, s, mgr := wsTestServer(t, newMockSession(), 0)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_noauth", userID)

	code := wsDialEnvExpectFail(t, srv, "env_noauth", "" /* no auth */)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestExecWS_InvalidAuth(t *testing.T) {
	srv, s, mgr := wsTestServer(t, newMockSession(), 0)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_badauth", userID)

	code := wsDialEnvExpectFail(t, srv, "env_badauth", "Bearer wrong-key")
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestExecWS_NotFound(t *testing.T) {
	srv, _, _ := wsTestServer(t, newMockSession(), 0)

	code := wsDialEnvExpectFail(t, srv, "env_nonexistent", "Bearer "+testAPIKey)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestExecWS_WrongTenant(t *testing.T) {
	srv, s, _ := wsTestServer(t, newMockSession(), 0)
	// Seed an env owned by a different user; the test key should see 404.
	seedRunningEnv(t, s, "env_other", "some-other-user-id")

	code := wsDialEnvExpectFail(t, srv, "env_other", "Bearer "+testAPIKey)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 (tenant isolation), got %d", code)
	}
}

func TestExecWS_NotRunning(t *testing.T) {
	srv, s, mgr := wsTestServer(t, newMockSession(), 0)
	userID, _ := mgr.UserID(testAPIKey)

	// Seed a stopped environment.
	env := &domain.Environment{
		ID:         "env_stopped",
		UserID:     userID,
		Status:     domain.StatusStopped,
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		ExpiresAt:  time.Now().Add(60 * time.Minute),
	}
	s.Create(context.Background(), env) //nolint:errcheck

	code := wsDialEnvExpectFail(t, srv, "env_stopped", "Bearer "+testAPIKey)
	if code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// Session output streaming
// ---------------------------------------------------------------------------

func TestExecWS_OutputStreaming(t *testing.T) {
	sess := newMockSession()
	srv, s, mgr := wsTestServer(t, sess, 0)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_out", userID)

	conn := wsDialEnv(t, srv, "env_out")

	// Push output from the container side.
	if _, err := sess.pw.Write([]byte("hello world\n")); err != nil {
		t.Fatalf("write to session: %v", err)
	}

	msg := nextMsg(t, conn)
	if msg.Stream != "output" {
		t.Fatalf("expected stream=output, got %s", msg.Stream)
	}
	if msg.Data != "hello world\n" {
		t.Fatalf("expected data 'hello world\\n', got %q", msg.Data)
	}

	// Close the session to trigger exit.
	sess.Close()
	_, exitData := drainUntilExit(t, conn)
	if exitData != "0" {
		t.Fatalf("expected exit code 0, got %q", exitData)
	}
}

// ---------------------------------------------------------------------------
// Input forwarding
// ---------------------------------------------------------------------------

func TestExecWS_InputForwarding(t *testing.T) {
	sess := newMockSession()
	srv, s, mgr := wsTestServer(t, sess, 0)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_in", userID)

	conn := wsDialEnv(t, srv, "env_in")

	// Send an input message from the client.
	msg := domain.WSInputMessage{Type: "input", Data: "ls -la\n"}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write input message: %v", err)
	}

	// Wait for the handler goroutine to forward it to the session.
	select {
	case got := <-sess.writeCh:
		if string(got) != "ls -la\n" {
			t.Fatalf("expected 'ls -la\\n', got %q", string(got))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: input was not forwarded to session")
	}

	sess.Close()
}

// ---------------------------------------------------------------------------
// Resize forwarding
// ---------------------------------------------------------------------------

func TestExecWS_ResizeForwarding(t *testing.T) {
	sess := newMockSession()
	srv, s, mgr := wsTestServer(t, sess, 0)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_resize", userID)

	conn := wsDialEnv(t, srv, "env_resize")

	msg := domain.WSInputMessage{Type: "resize", Cols: 120, Rows: 40}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write resize message: %v", err)
	}

	select {
	case got := <-sess.resizeCh:
		if got.cols != 120 || got.rows != 40 {
			t.Fatalf("expected resize 120x40, got %dx%d", got.cols, got.rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: resize was not forwarded to session")
	}

	sess.Close()
}

// ---------------------------------------------------------------------------
// Exit code
// ---------------------------------------------------------------------------

func TestExecWS_ExitCode(t *testing.T) {
	sess := newMockSession()
	srv, s, mgr := wsTestServer(t, sess, 42)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_exit", userID)

	conn := wsDialEnv(t, srv, "env_exit")

	// Close the session immediately to trigger the exit-code path.
	sess.Close()

	_, exitData := drainUntilExit(t, conn)
	if exitData != "42" {
		t.Fatalf("expected exit code '42', got %q", exitData)
	}
}

// ---------------------------------------------------------------------------
// Clean shutdown — no goroutine leak
// ---------------------------------------------------------------------------

// TestExecWS_ShutdownOnSessionClose verifies that when the container process
// exits (session output EOF), the server closes the WebSocket connection and
// the handler returns cleanly. We confirm this by reading until we get the
// exit message and then verifying the connection is closed.
func TestExecWS_ShutdownOnSessionClose(t *testing.T) {
	sess := newMockSession()
	srv, s, mgr := wsTestServer(t, sess, 0)
	userID, _ := mgr.UserID(testAPIKey)
	seedRunningEnv(t, s, "env_shutdown", userID)

	conn := wsDialEnv(t, srv, "env_shutdown")

	// Simulate the container process exiting.
	sess.Close()

	// Drain until we receive the exit message.
	_, _ = drainUntilExit(t, conn)

	// After the handler sends the exit message and closes the connection, the
	// next read must fail (connection closed). Give the server side a moment
	// to complete its cleanup.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed after session exit, but ReadMessage succeeded")
	}
}

// ---------------------------------------------------------------------------
// LastUsedAt is updated at session start (reaper guard)
// ---------------------------------------------------------------------------

func TestExecWS_LastUsedAtUpdatedOnStart(t *testing.T) {
	sess := newMockSession()
	srv, s, mgr := wsTestServer(t, sess, 0)
	userID, _ := mgr.UserID(testAPIKey)

	before := time.Now().Add(-5 * time.Minute) // seed with a stale timestamp
	env := &domain.Environment{
		ID:          "env_keepalive",
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "container_env_keepalive",
		Spec:        domain.EnvironmentSpec{Image: "alpine:latest"},
		CreatedAt:   time.Now(),
		LastUsedAt:  before,
		ExpiresAt:   time.Now().Add(60 * time.Minute),
	}
	s.Create(context.Background(), env) //nolint:errcheck

	conn := wsDialEnv(t, srv, "env_keepalive")

	// Give the handler goroutine a moment to call store.Update with the fresh timestamp.
	time.Sleep(100 * time.Millisecond)

	updated, err := s.Get(context.Background(), "env_keepalive", userID)
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if !updated.LastUsedAt.After(before) {
		t.Fatalf("expected LastUsedAt to be updated after session start, got %v (was %v)", updated.LastUsedAt, before)
	}

	sess.Close()
	conn.Close()
}
