package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/paramahastha/configly/server/internal/model"
)

// schema defines all tables and indexes. PRAGMAs are set via DSN params in NewSQLite,
// not here, to avoid re-running them on every Exec call.
const schema = `
CREATE TABLE IF NOT EXISTS environments (
	id         TEXT PRIMARY KEY,
	project    TEXT NOT NULL,
	name       TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	deleted_at DATETIME,
	UNIQUE(project, name)
);

CREATE TABLE IF NOT EXISTS configs (
	id          TEXT PRIMARY KEY,
	project     TEXT NOT NULL,
	environment TEXT NOT NULL,
	key         TEXT NOT NULL,
	type        TEXT NOT NULL,
	value       TEXT NOT NULL DEFAULT '',
	rollout     INTEGER NOT NULL DEFAULT 0,
	rules       TEXT,
	description TEXT NOT NULL DEFAULT '',
	version     INTEGER NOT NULL DEFAULT 1,
	created_at  DATETIME NOT NULL,
	created_by  TEXT NOT NULL DEFAULT '',
	updated_at  DATETIME NOT NULL,
	updated_by  TEXT NOT NULL DEFAULT '',
	deleted_at  DATETIME,
	UNIQUE(project, environment, key),
	FOREIGN KEY(project, environment) REFERENCES environments(project, name)
);
CREATE INDEX IF NOT EXISTS idx_configs_project_env ON configs(project, environment);

CREATE TABLE IF NOT EXISTS config_versions (
	id          TEXT PRIMARY KEY,
	config_id   TEXT NOT NULL REFERENCES configs(id),
	project     TEXT NOT NULL,
	environment TEXT NOT NULL,
	key         TEXT NOT NULL,
	type        TEXT NOT NULL,
	value       TEXT NOT NULL DEFAULT '',
	rollout     INTEGER NOT NULL DEFAULT 0,
	rules       TEXT,
	version     INTEGER NOT NULL,
	created_at  DATETIME NOT NULL,
	created_by  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_config_versions_id_ver ON config_versions(config_id, version DESC);

CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	email         TEXT UNIQUE NOT NULL,
	password_hash VARCHAR(255) NOT NULL,
	role          TEXT NOT NULL DEFAULT 'viewer',
	created_at    DATETIME NOT NULL,
	deleted_at    DATETIME
);

CREATE TABLE IF NOT EXISTS project_members (
	user_id    TEXT NOT NULL REFERENCES users(id),
	project    TEXT NOT NULL,
	role       TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	PRIMARY KEY(user_id, project)
);

CREATE TABLE IF NOT EXISTS api_keys (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users(id),
	name         TEXT NOT NULL DEFAULT '',
	prefix       TEXT NOT NULL DEFAULT '',
	key_hash     TEXT UNIQUE NOT NULL,
	created_at   DATETIME NOT NULL,
	created_by   TEXT NOT NULL DEFAULT '',
	last_used_at DATETIME,
	expires_at   DATETIME
);

CREATE TABLE IF NOT EXISTS audit (
	id          TEXT PRIMARY KEY,
	actor       TEXT NOT NULL,
	action      TEXT NOT NULL,
	resource    TEXT NOT NULL DEFAULT '',
	project     TEXT NOT NULL DEFAULT '',
	environment TEXT NOT NULL DEFAULT '',
	before      TEXT,
	after       TEXT,
	created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_project_time ON audit(project, created_at DESC);
`

// SQLite implements Store backed by a local SQLite file.
type SQLite struct {
	db        *sql.DB
	mu        sync.RWMutex
	snapshots map[string]*model.Snapshot // keyed by "project:env"
}

// NewSQLite opens (or creates) the database at path and applies the schema.
// WAL mode, busy-timeout, and FK enforcement are set via DSN parameters.
func NewSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite allows one writer at a time; a single connection avoids "database is locked"
	// errors from concurrent writes without sacrificing throughput on a single-node deployment.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SQLite{db: db, snapshots: make(map[string]*model.Snapshot)}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

// ---------------------------------------------------------------------------
// Environments / Projects
// ---------------------------------------------------------------------------

func (s *SQLite) CreateEnvironment(ctx context.Context, env *model.Environment) error {
	now := time.Now().UTC()
	if env.ID == "" {
		env.ID = uuid.NewString()
	}
	env.CreatedAt = now
	env.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO environments(id, project, name, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		env.ID, env.Project, env.Name, env.CreatedAt, env.UpdatedAt)
	return err
}

func (s *SQLite) DeleteEnvironment(ctx context.Context, project, name string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE environments SET deleted_at=?, updated_at=?
		 WHERE project=? AND name=? AND deleted_at IS NULL`,
		now, now, project, name)
	return err
}

func (s *SQLite) ListEnvironments(ctx context.Context, project string) ([]model.Environment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project, name, created_at, updated_at FROM environments
		 WHERE project=? AND deleted_at IS NULL ORDER BY name`,
		project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Environment
	for rows.Next() {
		var e model.Environment
		if err := rows.Scan(&e.ID, &e.Project, &e.Name, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLite) ListProjects(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT project FROM environments WHERE deleted_at IS NULL ORDER BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Configs
// ---------------------------------------------------------------------------

// upsertTx performs the config insert-or-update and appends a config_versions row,
// all within the provided transaction. It mutates c.ID, c.Version, c.CreatedAt,
// c.CreatedBy, c.UpdatedAt, and c.UpdatedBy in place.
func (s *SQLite) upsertTx(ctx context.Context, tx *sql.Tx, c *model.Config, actor string, now time.Time) error {
	c.UpdatedAt = now
	c.UpdatedBy = actor

	var existingID, existingCreatedBy string
	var existingVersion int
	var existingCreatedAt time.Time
	err := tx.QueryRowContext(ctx,
		`SELECT id, version, created_at, created_by FROM configs
		 WHERE project=? AND environment=? AND key=? AND deleted_at IS NULL`,
		c.Project, c.Environment, c.Key).
		Scan(&existingID, &existingVersion, &existingCreatedAt, &existingCreatedBy)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		c.ID = uuid.NewString()
		c.Version = 1
		c.CreatedAt = now
		c.CreatedBy = actor
		_, err = tx.ExecContext(ctx,
			`INSERT INTO configs(id, project, environment, key, type, value, rollout, rules,
			 description, version, created_at, created_by, updated_at, updated_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Project, c.Environment, c.Key, c.Type, c.Value,
			c.Rollout, nullableJSON(c.Rules), c.Description, c.Version,
			c.CreatedAt, c.CreatedBy, c.UpdatedAt, actor)
	case err == nil:
		c.ID = existingID
		c.Version = existingVersion + 1
		c.CreatedAt = existingCreatedAt
		c.CreatedBy = existingCreatedBy
		_, err = tx.ExecContext(ctx,
			`UPDATE configs SET type=?, value=?, rollout=?, rules=?, description=?,
			 version=?, updated_at=?, updated_by=? WHERE id=?`,
			c.Type, c.Value, c.Rollout, nullableJSON(c.Rules), c.Description,
			c.Version, c.UpdatedAt, actor, c.ID)
	default:
		return err
	}
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO config_versions(id, config_id, project, environment, key,
		 type, value, rollout, rules, version, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), c.ID, c.Project, c.Environment, c.Key,
		c.Type, c.Value, c.Rollout, nullableJSON(c.Rules), c.Version, now, actor)
	return err
}

func (s *SQLite) UpsertConfig(ctx context.Context, c *model.Config, actor string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.upsertTx(ctx, tx, c, actor, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidate(c.Project, c.Environment)
	return nil
}

func (s *SQLite) GetConfig(ctx context.Context, project, env, key string) (*model.Config, error) {
	var c model.Config
	var rules sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project, environment, key, type, value, rollout, rules, description,
		 version, created_at, created_by, updated_at, updated_by
		 FROM configs WHERE project=? AND environment=? AND key=? AND deleted_at IS NULL`,
		project, env, key).Scan(
		&c.ID, &c.Project, &c.Environment, &c.Key, &c.Type, &c.Value,
		&c.Rollout, &rules, &c.Description, &c.Version,
		&c.CreatedAt, &c.CreatedBy, &c.UpdatedAt, &c.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rules.Valid {
		c.Rules = []byte(rules.String)
	}
	return &c, nil
}

func (s *SQLite) ListConfigs(ctx context.Context, project, env string) ([]model.Config, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project, environment, key, type, value, rollout, rules, description,
		 version, created_at, created_by, updated_at, updated_by
		 FROM configs WHERE project=? AND environment=? AND deleted_at IS NULL ORDER BY key`,
		project, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Config
	for rows.Next() {
		var c model.Config
		var rules sql.NullString
		if err := rows.Scan(&c.ID, &c.Project, &c.Environment, &c.Key, &c.Type, &c.Value,
			&c.Rollout, &rules, &c.Description, &c.Version,
			&c.CreatedAt, &c.CreatedBy, &c.UpdatedAt, &c.UpdatedBy); err != nil {
			return nil, err
		}
		if rules.Valid {
			c.Rules = []byte(rules.String)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteConfig(ctx context.Context, project, env, key, actor string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE configs SET deleted_at=?, updated_at=?, updated_by=?
		 WHERE project=? AND environment=? AND key=? AND deleted_at IS NULL`,
		now, now, actor, project, env, key)
	if err != nil {
		return err
	}
	s.invalidate(project, env)
	return nil
}

// ---------------------------------------------------------------------------
// Versions / Rollback
// ---------------------------------------------------------------------------

func (s *SQLite) ListVersions(ctx context.Context, configID string, limit int) ([]model.ConfigVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, config_id, project, environment, key, type, value, rollout, rules, version, created_at, created_by
		 FROM config_versions WHERE config_id=? ORDER BY version DESC LIMIT ?`,
		configID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ConfigVersion
	for rows.Next() {
		var v model.ConfigVersion
		var rules sql.NullString
		if err := rows.Scan(&v.ID, &v.ConfigID, &v.Project, &v.Environment, &v.Key,
			&v.Type, &v.Value, &v.Rollout, &rules, &v.Version, &v.CreatedAt, &v.CreatedBy); err != nil {
			return nil, err
		}
		if rules.Valid {
			v.Rules = []byte(rules.String)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Rollback restores a config to a historical version by creating a new version with
// those values. All reads and writes happen inside a single transaction so the
// config cannot be concurrently deleted between the lookup and the write.
func (s *SQLite) Rollback(ctx context.Context, configID string, version int, actor string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var target model.ConfigVersion
	var rules sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT type, value, rollout, rules FROM config_versions WHERE config_id=? AND version=?`,
		configID, version).Scan(&target.Type, &target.Value, &target.Rollout, &rules)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("version %d not found for config %s", version, configID)
	}
	if err != nil {
		return err
	}
	if rules.Valid {
		target.Rules = []byte(rules.String)
	}

	var project, env, key string
	err = tx.QueryRowContext(ctx,
		`SELECT project, environment, key FROM configs WHERE id=? AND deleted_at IS NULL`,
		configID).Scan(&project, &env, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("config %s not found or already deleted", configID)
	}
	if err != nil {
		return err
	}

	c := &model.Config{
		ID:          configID,
		Project:     project,
		Environment: env,
		Key:         key,
		Type:        target.Type,
		Value:       target.Value,
		Rollout:     target.Rollout,
		Rules:       target.Rules,
	}
	if err := s.upsertTx(ctx, tx, c, actor, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidate(project, env)
	return nil
}

// ---------------------------------------------------------------------------
// Snapshot (cached)
// ---------------------------------------------------------------------------

func (s *SQLite) Snapshot(ctx context.Context, project, env string) (*model.Snapshot, error) {
	cacheKey := project + ":" + env

	s.mu.RLock()
	snap, ok := s.snapshots[cacheKey]
	s.mu.RUnlock()
	if ok {
		return snap, nil
	}

	// Cache miss: build the snapshot outside any lock so DB I/O doesn't block readers.
	configs, err := s.ListConfigs(ctx, project, env)
	if err != nil {
		return nil, err
	}

	var latestAt time.Time
	newSnap := &model.Snapshot{
		Project:     project,
		Environment: env,
		Configs:     make(map[string]model.ConfigEntry, len(configs)),
	}
	for _, c := range configs {
		newSnap.Configs[c.Key] = model.ConfigEntry{
			Type:    c.Type,
			Value:   c.Value,
			Rollout: c.Rollout,
			Rules:   c.Rules,
		}
		if c.UpdatedAt.After(latestAt) {
			latestAt = c.UpdatedAt
		}
	}
	if latestAt.IsZero() {
		latestAt = time.Now().UTC()
	}
	newSnap.UpdatedAt = latestAt
	newSnap.ETag = computeETag(configs)

	// Re-check under write lock: another goroutine may have won the race.
	s.mu.Lock()
	if existing, ok := s.snapshots[cacheKey]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.snapshots[cacheKey] = newSnap
	s.mu.Unlock()
	return newSnap, nil
}

func (s *SQLite) invalidate(project, env string) {
	s.mu.Lock()
	delete(s.snapshots, project+":"+env)
	s.mu.Unlock()
}

// computeETag hashes all config fields that affect client behaviour.
// configs must be ordered by key (ListConfigs guarantees this).
func computeETag(configs []model.Config) string {
	h := sha256.New()
	for _, c := range configs {
		// Null byte separators prevent value collisions across fields.
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00", c.Key, c.Type, c.Value, c.Rollout, c.Version)
		h.Write(c.Rules)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (s *SQLite) CreateUser(ctx context.Context, u *model.User) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	u.CreatedAt = time.Now().UTC()
	if u.Role == "" {
		u.Role = model.RoleViewer
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(id, email, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.PasswordHash, u.Role, u.CreatedAt)
	return err
}

func (s *SQLite) DeleteUser(ctx context.Context, id string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err = tx.ExecContext(ctx,
		`UPDATE users SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM api_keys WHERE user_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_members WHERE user_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users
		 WHERE email=? AND deleted_at IS NULL`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

func (s *SQLite) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, role, created_at FROM users WHERE deleted_at IS NULL ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// API Keys
// ---------------------------------------------------------------------------

func (s *SQLite) CreateAPIKey(ctx context.Context, k *model.APIKey, actor string) error {
	if k.ID == "" {
		k.ID = uuid.NewString()
	}
	k.CreatedAt = time.Now().UTC()
	k.CreatedBy = actor
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys(id, user_id, name, prefix, key_hash, created_at, created_by, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.UserID, k.Name, k.Prefix, k.KeyHash, k.CreatedAt, actor, nullableTime(k.ExpiresAt))
	return err
}

func (s *SQLite) RevokeAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return err
}

func (s *SQLite) UpdateLastUsedAt(ctx context.Context, keyID string, t time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at=? WHERE id=?`, t.UTC(), keyID)
	return err
}

// GetUserByAPIKey hashes rawKey (SHA-256 hex) and looks up the matching api_key row,
// then loads the associated non-deleted user. Returns (nil, nil, nil) if not found.
func (s *SQLite) GetUserByAPIKey(ctx context.Context, rawKey string) (*model.User, *model.APIKey, error) {
	hash := HashAPIKey(rawKey)

	var k model.APIKey
	var lastUsed, expires sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, prefix, key_hash, created_at, last_used_at, expires_at
		 FROM api_keys WHERE key_hash=? AND (expires_at IS NULL OR expires_at > ?)`,
		hash, time.Now().UTC()).
		Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.KeyHash,
			&k.CreatedAt, &lastUsed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if expires.Valid {
		t := expires.Time
		k.ExpiresAt = &t
	}

	var user model.User
	err = s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users
		 WHERE id=? AND deleted_at IS NULL`, k.UserID).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &user, &k, nil
}

// HashAPIKey returns the SHA-256 hex digest of rawKey.
// Use this when building model.APIKey.KeyHash before calling CreateAPIKey.
func HashAPIKey(rawKey string) string {
	h := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// RBAC — ProjectMember
// ---------------------------------------------------------------------------

func (s *SQLite) SetProjectMember(ctx context.Context, m *model.ProjectMember) error {
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_members(user_id, project, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, project) DO UPDATE SET role=excluded.role, updated_at=excluded.updated_at`,
		m.UserID, m.Project, m.Role, m.CreatedAt, m.UpdatedAt)
	return err
}

func (s *SQLite) GetProjectMember(ctx context.Context, userID, project string) (*model.ProjectMember, error) {
	var m model.ProjectMember
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, project, role, created_at, updated_at FROM project_members
		 WHERE user_id=? AND project=?`,
		userID, project).Scan(&m.UserID, &m.Project, &m.Role, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}

func (s *SQLite) ListProjectMembers(ctx context.Context, project string) ([]model.ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, project, role, created_at, updated_at FROM project_members
		 WHERE project=? ORDER BY user_id`,
		project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProjectMember
	for rows.Next() {
		var m model.ProjectMember
		if err := rows.Scan(&m.UserID, &m.Project, &m.Role, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

func (s *SQLite) AppendAudit(ctx context.Context, e *model.AuditEvent) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	e.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit(id, actor, action, resource, project, environment, before, after, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Actor, e.Action, e.Resource, e.Project, e.Environment,
		nullableJSON(e.Before), nullableJSON(e.After), e.CreatedAt)
	return err
}

func (s *SQLite) ListAudit(ctx context.Context, project string, limit int) ([]model.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, actor, action, resource, project, environment, before, after, created_at
		 FROM audit WHERE project=? ORDER BY created_at DESC LIMIT ?`,
		project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuditEvent
	for rows.Next() {
		var e model.AuditEvent
		var before, after sql.NullString
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Resource,
			&e.Project, &e.Environment, &before, &after, &e.CreatedAt); err != nil {
			return nil, err
		}
		if before.Valid {
			e.Before = []byte(before.String)
		}
		if after.Valid {
			e.After = []byte(after.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nullableJSON stores JSON as NULL when empty rather than an empty string.
// Used for rules, before, and after columns so all JSON columns behave consistently.
func nullableJSON(r []byte) interface{} {
	if len(r) == 0 {
		return nil
	}
	return string(r)
}

func nullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return *t
}
