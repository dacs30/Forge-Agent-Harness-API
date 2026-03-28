package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "request_id"

func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(wrapped, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", r.Context().Value(requestIDKey),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap allows middleware like http.Flusher to work through the wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func AuthMiddleware(keys []string) func(http.Handler) http.Handler {
	keySet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keySet[k] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if _, ok := keySet[token]; !ok {
				writeError(w, http.StatusUnauthorized, "invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					logger.Error("panic recovered", "error", err, "path", r.URL.Path)
					writeError(w, http.StatusInternalServerError, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
