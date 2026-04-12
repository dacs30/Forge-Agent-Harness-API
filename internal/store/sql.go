package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"haas/internal/domain"
)

// SQLStore is a database/sql-backed Store compatible with both SQLite and PostgreSQL.
// Times are stored as Unix seconds (BIGINT) to avoid driver/timezone inconsistencies.
// EnvVars are stored as a JSON blob.
type SQLStore struct {
	db          *sql.DB
	isPG        bool // true = use $N placeholders; false = use ?
	idleTimeout time.Duration
	maxLifetime time.Duration
}

// NewSQLStore runs schema migrations and returns a ready SQLStore.
// driver should be the value passed to sql.Open ("sqlite" or "pgx").
func NewSQLStore(db *sql.DB, driver string, idleTimeout, maxLifetime time.Duration) (*SQLStore, error) {
	s := &SQLStore{
		db:          db,
		isPG:        strings.HasPrefix(driver, "pgx") || strings.HasPrefix(driver, "postgres"),
		idleTimeout: idleTimeout,
		maxLifetime: maxLifetime,
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// --- Schema ------------------------------------------------------------------

const createUsersTable = `
CREATE TABLE IF NOT EXISTS users (
    id         TEXT   PRIMARY KEY,
    created_at BIGINT NOT NULL
)`

const createAPIKeysTable = `
CREATE TABLE IF NOT EXISTS api_keys (
    key_hash   TEXT   PRIMARY KEY,
    user_id    TEXT   NOT NULL REFERENCES users(id),
    created_at BIGINT NOT NULL
)`

const createEnvironmentsTable = `
CREATE TABLE IF NOT EXISTS environments (
    id                  TEXT   PRIMARY KEY,
    user_id             TEXT   NOT NULL DEFAULT '' REFERENCES users(id),
    status              TEXT   NOT NULL,
    container_id        TEXT   NOT NULL DEFAULT '',
    created_at          BIGINT NOT NULL,
    last_used_at        BIGINT NOT NULL,
    expires_at          BIGINT NOT NULL,
    spec_image          TEXT   NOT NULL,
    spec_cpu            REAL   NOT NULL,
    spec_memory_mb      BIGINT NOT NULL,
    spec_disk_mb        BIGINT NOT NULL,
    spec_network_policy TEXT   NOT NULL,
    spec_env_vars       TEXT   NOT NULL DEFAULT '{}'
)`

const createEnvironmentsUserIDIndex = `CREATE INDEX IF NOT EXISTS idx_environments_user_id ON environments(user_id)`

func (s *SQLStore) migrate() error {
	stmts := []string{createUsersTable, createAPIKeysTable, createEnvironmentsTable, createEnvironmentsUserIDIndex}
	if !s.isPG {
		stmts = append([]string{"PRAGMA foreign_keys = ON"}, stmts...)
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	// Safe migration: add user_id to existing environments tables from before multi-tenancy.
	return s.addColumnIfMissing("environments", "user_id", "TEXT NOT NULL DEFAULT ''")
}

// addColumnIfMissing adds a column to a table only if it does not already exist.
// Handles the difference between PostgreSQL (IF NOT EXISTS) and SQLite (inspect then alter).
//
// Security note: table, column, and definition are always compile-time constants
// passed from migrate() — never derived from user input — so fmt.Sprintf is safe here.
//
// Concurrency note: migrate() is called once at startup before the server accepts
// connections, so there is no concurrent-migration race in practice.
func (s *SQLStore) addColumnIfMissing(table, column, definition string) error {
	if s.isPG {
		_, err := s.db.Exec(fmt.Sprintf(
			"ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, column, definition,
		))
		return err
	}
	// SQLite: check pragma first
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, pk int
		var name, colType, notNull string
		var dflt *string // nullable
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // already exists
		}
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

// --- Store interface ---------------------------------------------------------

func (s *SQLStore) BootstrapUser(ctx context.Context, keyHash, userID string) error {
	now := time.Now().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // error irrelevant after commit or on rollback path

	upsertUser := s.rebind(`
		INSERT INTO users (id, created_at) VALUES (?, ?)
		ON CONFLICT (id) DO NOTHING`)
	if _, err := tx.ExecContext(ctx, upsertUser, userID, now); err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}

	upsertKey := s.rebind(`
		INSERT INTO api_keys (key_hash, user_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (key_hash) DO UPDATE SET user_id = EXCLUDED.user_id`)
	if _, err := tx.ExecContext(ctx, upsertKey, keyHash, userID, now); err != nil {
		return fmt.Errorf("upsert api_key: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *SQLStore) Create(ctx context.Context, env *domain.Environment) error {
	envVars, err := marshalEnvVars(env.Spec.EnvVars)
	if err != nil {
		return err
	}
	q := s.rebind(`
		INSERT INTO environments
			(id, user_id, status, container_id, created_at, last_used_at, expires_at,
			 spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err = s.db.ExecContext(ctx, q,
		env.ID, env.UserID, string(env.Status), env.ContainerID,
		env.CreatedAt.Unix(), env.LastUsedAt.Unix(), env.ExpiresAt.Unix(),
		env.Spec.Image, env.Spec.CPU, env.Spec.MemoryMB, env.Spec.DiskMB,
		string(env.Spec.NetworkPolicy), envVars,
	)
	return err
}

// Get returns the environment by ID. If userID is non-empty, it must match — otherwise ErrNotFound.
func (s *SQLStore) Get(ctx context.Context, id, userID string) (*domain.Environment, error) {
	var (
		q    string
		args []any
	)
	if userID == "" {
		q = s.rebind(`SELECT id, user_id, status, container_id, created_at, last_used_at, expires_at,
			spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars
			FROM environments WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`SELECT id, user_id, status, container_id, created_at, last_used_at, expires_at,
			spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars
			FROM environments WHERE id = ? AND user_id = ?`)
		args = []any{id, userID}
	}
	row := s.db.QueryRowContext(ctx, q, args...)
	env, err := scanEnv(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return env, err
}

func (s *SQLStore) Update(ctx context.Context, env *domain.Environment) error {
	envVars, err := marshalEnvVars(env.Spec.EnvVars)
	if err != nil {
		return err
	}
	q := s.rebind(`
		UPDATE environments SET
			status = ?, container_id = ?, last_used_at = ?, expires_at = ?,
			spec_cpu = ?, spec_memory_mb = ?, spec_disk_mb = ?, spec_env_vars = ?
		WHERE id = ? AND user_id = ?`)
	res, err := s.db.ExecContext(ctx, q,
		string(env.Status), env.ContainerID, env.LastUsedAt.Unix(), env.ExpiresAt.Unix(),
		env.Spec.CPU, env.Spec.MemoryMB, env.Spec.DiskMB, envVars,
		env.ID, env.UserID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes the environment. If userID is non-empty, it must match — otherwise ErrNotFound.
// Pass userID="" for admin/reaper use (removes regardless of owner).
func (s *SQLStore) Delete(ctx context.Context, id, userID string) error {
	var (
		q    string
		args []any
	)
	if userID == "" {
		q = s.rebind(`DELETE FROM environments WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`DELETE FROM environments WHERE id = ? AND user_id = ?`)
		args = []any{id, userID}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns environments. If userID is non-empty, only that tenant's environments are returned.
func (s *SQLStore) List(ctx context.Context, userID string) ([]*domain.Environment, error) {
	var (
		q    string
		args []any
	)
	base := `SELECT id, user_id, status, container_id, created_at, last_used_at, expires_at,
		spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars
		FROM environments`
	if userID == "" {
		q = base
	} else {
		q = s.rebind(base + ` WHERE user_id = ?`)
		args = []any{userID}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEnvs(rows)
}

func (s *SQLStore) ListExpired(ctx context.Context) ([]*domain.Environment, error) {
	now := time.Now()
	idleCutoff := now.Add(-s.idleTimeout).Unix()
	expiryCutoff := now.Unix()

	q := s.rebind(`
		SELECT id, user_id, status, container_id, created_at, last_used_at, expires_at,
			spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars
		FROM environments
		WHERE status NOT IN ('stopped', 'destroyed')
		AND (last_used_at < ? OR expires_at < ?)`)
	rows, err := s.db.QueryContext(ctx, q, idleCutoff, expiryCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEnvs(rows)
}

// --- helpers -----------------------------------------------------------------

// rebind replaces ? placeholders with $1, $2, ... for PostgreSQL.
// It skips ? characters that appear inside single-quoted string literals.
func (s *SQLStore) rebind(q string) string {
	if !s.isPG {
		return q
	}
	n := 0
	inStr := false
	var sb strings.Builder
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			inStr = !inStr
		}
		if c == '?' && !inStr {
			n++
			sb.WriteString(fmt.Sprintf("$%d", n))
		} else {
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanEnv(row scanner) (*domain.Environment, error) {
	var (
		env           domain.Environment
		status        string
		networkPolicy string
		createdAt     int64
		lastUsedAt    int64
		expiresAt     int64
		envVarsJSON   string
	)
	err := row.Scan(
		&env.ID, &env.UserID, &status, &env.ContainerID,
		&createdAt, &lastUsedAt, &expiresAt,
		&env.Spec.Image, &env.Spec.CPU, &env.Spec.MemoryMB, &env.Spec.DiskMB,
		&networkPolicy, &envVarsJSON,
	)
	if err != nil {
		return nil, err
	}
	env.Status = domain.EnvironmentStatus(status)
	env.Spec.NetworkPolicy = domain.NetworkPolicy(networkPolicy)
	env.CreatedAt = time.Unix(createdAt, 0).UTC()
	env.LastUsedAt = time.Unix(lastUsedAt, 0).UTC()
	env.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	env.Spec.EnvVars, err = unmarshalEnvVars(envVarsJSON)
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func scanEnvs(rows *sql.Rows) ([]*domain.Environment, error) {
	var envs []*domain.Environment
	for rows.Next() {
		env, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}
	return envs, rows.Err()
}

func marshalEnvVars(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal env_vars: %w", err)
	}
	return string(b), nil
}

func unmarshalEnvVars(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("unmarshal env_vars: %w", err)
	}
	return m, nil
}
