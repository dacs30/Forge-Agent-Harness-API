// Package auth handles API key authentication and maps keys to stable user IDs.
// Raw API keys are never stored — only their SHA-256 hashes.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type contextKey string

const userIDKey contextKey = "user_id"

// haasNamespace is a fixed UUID used as the namespace when deriving user IDs via UUID v5.
var haasNamespace = uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")

// Entry is a key-hash → user-ID pair, used to bootstrap the persistent store.
type Entry struct {
	KeyHash string
	UserID  string
}

// Manager maps hashed API keys to stable user IDs and provides HTTP middleware.
// User IDs are derived deterministically (UUID v5) so the same key always
// produces the same user ID — even across restarts and without a database.
type Manager struct {
	hashToUser map[string]string // sha256hex(key) → userID
}

// New builds a Manager from the raw API keys loaded from config.
func New(keys []string) *Manager {
	m := &Manager{hashToUser: make(map[string]string, len(keys))}
	for _, k := range keys {
		if k == "" {
			continue
		}
		h := HashKey(k)
		m.hashToUser[h] = deriveUserID(h)
	}
	return m
}

// UserID returns the stable user ID for a raw API key.
// Returns ("", false) if the key is not registered.
// Uses constant-time comparison to prevent timing-based key enumeration.
func (m *Manager) UserID(rawKey string) (string, bool) {
	candidate := []byte(HashKey(rawKey))
	for storedHash, userID := range m.hashToUser {
		if subtle.ConstantTimeCompare([]byte(storedHash), candidate) == 1 {
			return userID, true
		}
	}
	return "", false
}

// Entries returns all (keyHash, userID) pairs.
// Used by main to bootstrap user rows in the persistent store.
func (m *Manager) Entries() []Entry {
	out := make([]Entry, 0, len(m.hashToUser))
	for hash, userID := range m.hashToUser {
		out = append(out, Entry{KeyHash: hash, UserID: userID})
	}
	return out
}

// Middleware authenticates requests via Bearer token and injects the resolved
// userID into the request context. Rejects unknown keys with 401.
func (m *Manager) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			userID, ok := m.UserID(token)
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"invalid or missing API key","code":401}`)) //nolint:errcheck
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext returns the authenticated user ID stored in the context.
// Returns an empty string if not set (should never happen on authenticated routes).
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// HashKey returns the hex-encoded SHA-256 hash of a raw API key.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func deriveUserID(keyHash string) string {
	return uuid.NewSHA1(haasNamespace, []byte(keyHash)).String()
}
