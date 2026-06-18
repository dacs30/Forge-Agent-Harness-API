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
    user_id             TEXT   NOT NULL DEFAULT '',
    tenant_id           TEXT   NOT NULL DEFAULT '',
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
const createEnvironmentsTenantIDIndex = `CREATE INDEX IF NOT EXISTS idx_environments_tenant_id ON environments(tenant_id)`

const createSnapshotsTable = `
CREATE TABLE IF NOT EXISTS snapshots (
    id             TEXT   PRIMARY KEY,
    user_id        TEXT   NOT NULL DEFAULT '',
    tenant_id      TEXT   NOT NULL DEFAULT '',
    environment_id TEXT   NOT NULL DEFAULT '',
    image_id       TEXT   NOT NULL,
    label          TEXT   NOT NULL DEFAULT '',
    size           BIGINT NOT NULL DEFAULT 0,
    created_at     BIGINT NOT NULL
)`

const createSnapshotsUserIDIndex = `CREATE INDEX IF NOT EXISTS idx_snapshots_user_id ON snapshots(user_id)`
const createSnapshotsTenantIDIndex = `CREATE INDEX IF NOT EXISTS idx_snapshots_tenant_id ON snapshots(tenant_id)`

// createSkillsTable is parameterised by the binary column type, which differs
// between SQLite (BLOB) and PostgreSQL (BYTEA).
func createSkillsTable(blobType string) string {
	return `
CREATE TABLE IF NOT EXISTS skills (
    id         TEXT   PRIMARY KEY,
    user_id    TEXT   NOT NULL DEFAULT '',
    tenant_id  TEXT   NOT NULL DEFAULT '',
    name       TEXT   NOT NULL,
    archive    ` + blobType + ` NOT NULL,
    size       BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    UNIQUE(user_id, name)
)`
}

const createSkillsUserIDIndex = `CREATE INDEX IF NOT EXISTS idx_skills_user_id ON skills(user_id)`
const createSkillsTenantIDIndex = `CREATE INDEX IF NOT EXISTS idx_skills_tenant_id ON skills(tenant_id)`

func (s *SQLStore) migrate() error {
	// Phase 1: create tables (no-op if they already exist).
	blobType := "BLOB"
	if s.isPG {
		blobType = "BYTEA"
	}
	tablestmts := []string{
		createUsersTable,
		createAPIKeysTable,
		createEnvironmentsTable,
		createSnapshotsTable,
		createSkillsTable(blobType),
	}
	if !s.isPG {
		tablestmts = append([]string{"PRAGMA foreign_keys = ON"}, tablestmts...)
	}
	for _, stmt := range tablestmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	// Phase 2: add any columns that are missing on pre-existing databases.
	// This MUST happen before index creation so the columns exist when indexed.
	for _, col := range []struct{ table, column, def string }{
		{"environments", "user_id", "TEXT NOT NULL DEFAULT ''"},
		{"environments", "tenant_id", "TEXT NOT NULL DEFAULT ''"},
		{"snapshots", "tenant_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing(col.table, col.column, col.def); err != nil {
			return err
		}
	}

	// Phase 3: create indexes (columns are guaranteed to exist now).
	for _, stmt := range []string{
		createEnvironmentsUserIDIndex,
		createEnvironmentsTenantIDIndex,
		createSnapshotsUserIDIndex,
		createSnapshotsTenantIDIndex,
		createSkillsUserIDIndex,
		createSkillsTenantIDIndex,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	// Phase 4: backfill tenant_id from user_id for rows that predate this column.
	for _, table := range []string{"environments", "snapshots"} {
		if _, err := s.db.Exec(s.rebind("UPDATE " + table + " SET tenant_id = user_id WHERE tenant_id = ''")); err != nil {
			return err
		}
	}

	// Phase 5 (PostgreSQL only): drop the FK on user_id so end-user derived UUIDs
	// (which are not in the users table) can be stored without a violation.
	if s.isPG {
		for _, stmt := range []string{
			"ALTER TABLE environments DROP CONSTRAINT IF EXISTS environments_user_id_fkey",
			"ALTER TABLE snapshots DROP CONSTRAINT IF EXISTS snapshots_user_id_fkey",
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				return err
			}
		}
	}

	return nil
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
			(id, user_id, tenant_id, status, container_id, created_at, last_used_at, expires_at,
			 spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err = s.db.ExecContext(ctx, q,
		env.ID, env.UserID, env.TenantID, string(env.Status), env.ContainerID,
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
		q = s.rebind(`SELECT id, user_id, tenant_id, status, container_id, created_at, last_used_at, expires_at,
			spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars
			FROM environments WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`SELECT id, user_id, tenant_id, status, container_id, created_at, last_used_at, expires_at,
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

// List returns environments. If userID is non-empty, only that user's environments are returned.
func (s *SQLStore) List(ctx context.Context, userID string) ([]*domain.Environment, error) {
	var (
		q    string
		args []any
	)
	base := `SELECT id, user_id, tenant_id, status, container_id, created_at, last_used_at, expires_at,
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

// ListByTenant returns all environments for a tenant across all end-users.
func (s *SQLStore) ListByTenant(ctx context.Context, tenantID string) ([]*domain.Environment, error) {
	q := s.rebind(`SELECT id, user_id, tenant_id, status, container_id, created_at, last_used_at, expires_at,
		spec_image, spec_cpu, spec_memory_mb, spec_disk_mb, spec_network_policy, spec_env_vars
		FROM environments WHERE tenant_id = ?`)
	rows, err := s.db.QueryContext(ctx, q, tenantID)
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
		SELECT id, user_id, tenant_id, status, container_id, created_at, last_used_at, expires_at,
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

// --- Snapshot operations -----------------------------------------------------

func (s *SQLStore) CreateSnapshot(ctx context.Context, snap *domain.Snapshot) error {
	q := s.rebind(`
		INSERT INTO snapshots (id, user_id, tenant_id, environment_id, image_id, label, size, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.db.ExecContext(ctx, q,
		snap.ID, snap.UserID, snap.TenantID, snap.EnvironmentID, snap.ImageID, snap.Label, snap.Size, snap.CreatedAt.Unix(),
	)
	return err
}

func (s *SQLStore) GetSnapshot(ctx context.Context, id, userID string) (*domain.Snapshot, error) {
	var q string
	var args []any
	if userID == "" {
		q = s.rebind(`SELECT id, user_id, tenant_id, environment_id, image_id, label, size, created_at FROM snapshots WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`SELECT id, user_id, tenant_id, environment_id, image_id, label, size, created_at FROM snapshots WHERE id = ? AND user_id = ?`)
		args = []any{id, userID}
	}
	row := s.db.QueryRowContext(ctx, q, args...)
	snap, err := scanSnapshot(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return snap, err
}

func (s *SQLStore) ListSnapshots(ctx context.Context, userID string) ([]*domain.Snapshot, error) {
	var q string
	var args []any
	base := `SELECT id, user_id, tenant_id, environment_id, image_id, label, size, created_at FROM snapshots`
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
	var snaps []*domain.Snapshot
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}

// ListSnapshotsByTenant returns all snapshots for a tenant across all end-users.
func (s *SQLStore) ListSnapshotsByTenant(ctx context.Context, tenantID string) ([]*domain.Snapshot, error) {
	q := s.rebind(`SELECT id, user_id, tenant_id, environment_id, image_id, label, size, created_at FROM snapshots WHERE tenant_id = ?`)
	rows, err := s.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snaps []*domain.Snapshot
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}

func (s *SQLStore) DeleteSnapshot(ctx context.Context, id, userID string) error {
	var q string
	var args []any
	if userID == "" {
		q = s.rebind(`DELETE FROM snapshots WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`DELETE FROM snapshots WHERE id = ? AND user_id = ?`)
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

func scanSnapshot(row scanner) (*domain.Snapshot, error) {
	var snap domain.Snapshot
	var createdAt int64
	err := row.Scan(&snap.ID, &snap.UserID, &snap.TenantID, &snap.EnvironmentID, &snap.ImageID, &snap.Label, &snap.Size, &createdAt)
	if err != nil {
		return nil, err
	}
	snap.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &snap, nil
}

// --- Skill operations --------------------------------------------------------

const skillColumns = `id, user_id, tenant_id, name, size, created_at, updated_at`

// UpsertSkill inserts a skill or, on (user_id, name) conflict, replaces its
// archive while preserving the original id and created_at.
func (s *SQLStore) UpsertSkill(ctx context.Context, skill *domain.Skill, archive []byte) error {
	q := s.rebind(`
		INSERT INTO skills (id, user_id, tenant_id, name, archive, size, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, name) DO UPDATE SET
			archive    = EXCLUDED.archive,
			size       = EXCLUDED.size,
			updated_at = EXCLUDED.updated_at`)
	_, err := s.db.ExecContext(ctx, q,
		skill.ID, skill.UserID, skill.TenantID, skill.Name, archive, skill.SizeBytes,
		skill.CreatedAt.Unix(), skill.UpdatedAt.Unix(),
	)
	if err != nil {
		return err
	}
	// Reflect the persisted identity (the original row on conflict) back to the caller.
	persisted, err := s.getSkillByName(ctx, skill.UserID, skill.Name)
	if err != nil {
		return err
	}
	skill.ID = persisted.ID
	skill.CreatedAt = persisted.CreatedAt
	return nil
}

func (s *SQLStore) getSkillByName(ctx context.Context, userID, name string) (*domain.Skill, error) {
	q := s.rebind(`SELECT ` + skillColumns + ` FROM skills WHERE user_id = ? AND name = ?`)
	row := s.db.QueryRowContext(ctx, q, userID, name)
	skill, err := scanSkill(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return skill, err
}

func (s *SQLStore) GetSkill(ctx context.Context, id, userID string) (*domain.Skill, error) {
	var q string
	var args []any
	if userID == "" {
		q = s.rebind(`SELECT ` + skillColumns + ` FROM skills WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`SELECT ` + skillColumns + ` FROM skills WHERE id = ? AND user_id = ?`)
		args = []any{id, userID}
	}
	row := s.db.QueryRowContext(ctx, q, args...)
	skill, err := scanSkill(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return skill, err
}

func (s *SQLStore) GetSkillArchive(ctx context.Context, id, userID string) ([]byte, error) {
	var q string
	var args []any
	if userID == "" {
		q = s.rebind(`SELECT archive FROM skills WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`SELECT archive FROM skills WHERE id = ? AND user_id = ?`)
		args = []any{id, userID}
	}
	var archive []byte
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&archive)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return archive, err
}

func (s *SQLStore) ListSkills(ctx context.Context, userID string) ([]*domain.Skill, error) {
	var q string
	var args []any
	base := `SELECT ` + skillColumns + ` FROM skills`
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
	return scanSkills(rows)
}

func (s *SQLStore) ListSkillsByTenant(ctx context.Context, tenantID string) ([]*domain.Skill, error) {
	q := s.rebind(`SELECT ` + skillColumns + ` FROM skills WHERE tenant_id = ?`)
	rows, err := s.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSkills(rows)
}

func (s *SQLStore) DeleteSkill(ctx context.Context, id, userID string) error {
	var q string
	var args []any
	if userID == "" {
		q = s.rebind(`DELETE FROM skills WHERE id = ?`)
		args = []any{id}
	} else {
		q = s.rebind(`DELETE FROM skills WHERE id = ? AND user_id = ?`)
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

func scanSkills(rows *sql.Rows) ([]*domain.Skill, error) {
	var skills []*domain.Skill
	for rows.Next() {
		skill, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, rows.Err()
}

func scanSkill(row scanner) (*domain.Skill, error) {
	var skill domain.Skill
	var createdAt, updatedAt int64
	err := row.Scan(&skill.ID, &skill.UserID, &skill.TenantID, &skill.Name, &skill.SizeBytes, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	skill.CreatedAt = time.Unix(createdAt, 0).UTC()
	skill.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &skill, nil
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
		&env.ID, &env.UserID, &env.TenantID, &status, &env.ContainerID,
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
