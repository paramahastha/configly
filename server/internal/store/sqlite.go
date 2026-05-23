package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/paramahastha/configly/server/internal/model"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;

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
	updated_at  DATETIME NOT NULL,
	updated_by  TEXT NOT NULL DEFAULT '',
	deleted_at  DATETIME,
	UNIQUE(project, environment, key)
);
CREATE INDEX IF NOT EXISTS idx_configs_project_env ON configs(project, environment);

CREATE TABLE IF NOT EXISTS config_versions (
	id          TEXT PRIMARY KEY,
	config_id   TEXT NOT NULL,
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
	password_hash VARCHAR(64) NOT NULL,
	created_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS project_members (
	user_id    TEXT NOT NULL,
	project    TEXT NOT NULL,
	role       TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	PRIMARY KEY(user_id, project)
);

CREATE TABLE IF NOT EXISTS api_keys (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL,
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
	before      TEXT NOT NULL DEFAULT '',
	after       TEXT NOT NULL DEFAULT '',
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
func NewSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
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

func (s *SQLite) ListEnvironments(ctx context.Context, project string) ([]model.Environment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project, name, created_at, updated_at FROM environments
		 WHERE project = ? AND deleted_at IS NULL ORDER BY name`,
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

func (s *SQLite) UpsertConfig(ctx context.Context, c *model.Config, actor string) error {
	now := time.Now().UTC()
	c.UpdatedAt = now
	c.UpdatedBy = actor

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var existingID string
	var existingVersion int
	err = tx.QueryRowContext(ctx,
		`SELECT id, version FROM configs WHERE project=? AND environment=? AND key=? AND deleted_at IS NULL`,
		c.Project, c.Environment, c.Key).Scan(&existingID, &existingVersion)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		c.ID = uuid.NewString()
		c.Version = 1
		_, err = tx.ExecContext(ctx,
			`INSERT INTO configs(id, project, environment, key, type, value, rollout, rules, description, version, updated_at, updated_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Project, c.Environment, c.Key, c.Type, c.Value,
			c.Rollout, nullableRules(c.Rules), c.Description, c.Version, c.UpdatedAt, actor)
	case err == nil:
		c.ID = existingID
		c.Version = existingVersion + 1
		_, err = tx.ExecContext(ctx,
			`UPDATE configs SET type=?, value=?, rollout=?, rules=?, description=?,
			 version=?, updated_at=?, updated_by=? WHERE id=?`,
			c.Type, c.Value, c.Rollout, nullableRules(c.Rules), c.Description,
			c.Version, c.UpdatedAt, actor, c.ID)
	default:
		return err
	}
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO config_versions(id, config_id, project, environment, key, type, value, rollout, rules, version, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), c.ID, c.Project, c.Environment, c.Key,
		c.Type, c.Value, c.Rollout, nullableRules(c.Rules), c.Version, now, actor)
	if err != nil {
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
		`SELECT id, project, environment, key, type, value, rollout, rules, description, version, updated_at, updated_by
		 FROM configs WHERE project=? AND environment=? AND key=? AND deleted_at IS NULL`,
		project, env, key).Scan(
		&c.ID, &c.Project, &c.Environment, &c.Key, &c.Type, &c.Value,
		&c.Rollout, &rules, &c.Description, &c.Version, &c.UpdatedAt, &c.UpdatedBy)
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
		`SELECT id, project, environment, key, type, value, rollout, rules, description, version, updated_at, updated_by
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
			&c.Rollout, &rules, &c.Description, &c.Version, &c.UpdatedAt, &c.UpdatedBy); err != nil {
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

// Rollback creates a new version with the values from the target historical version.
// It does not delete history — rollback is itself a new version.
func (s *SQLite) Rollback(ctx context.Context, configID string, version int, actor string) error {
	var target model.ConfigVersion
	var rules sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT type, value, rollout, rules FROM config_versions
		 WHERE config_id=? AND version=?`,
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
	err = s.db.QueryRowContext(ctx,
		`SELECT project, environment, key FROM configs WHERE id=?`, configID).
		Scan(&project, &env, &key)
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
	return s.UpsertConfig(ctx, c, actor)
}

// ---------------------------------------------------------------------------
// Snapshot (cached)
// ---------------------------------------------------------------------------

func (s *SQLite) Snapshot(ctx context.Context, project, env string) (*model.Snapshot, error) {
	cacheKey := project + ":" + env

	s.mu.RLock()
	if snap, ok := s.snapshots[cacheKey]; ok {
		s.mu.RUnlock()
		return snap, nil
	}
	s.mu.RUnlock()

	configs, err := s.ListConfigs(ctx, project, env)
	if err != nil {
		return nil, err
	}

	snap := &model.Snapshot{
		Project:     project,
		Environment: env,
		UpdatedAt:   time.Now().UTC(),
		Configs:     make(map[string]model.ConfigEntry, len(configs)),
	}
	for _, c := range configs {
		snap.Configs[c.Key] = model.ConfigEntry{
			Type:    c.Type,
			Value:   c.Value,
			Rollout: c.Rollout,
			Rules:   c.Rules,
		}
	}
	snap.ETag = computeETag(configs)

	s.mu.Lock()
	s.snapshots[cacheKey] = snap
	s.mu.Unlock()
	return snap, nil
}

func (s *SQLite) invalidate(project, env string) {
	s.mu.Lock()
	delete(s.snapshots, project+":"+env)
	s.mu.Unlock()
}

// computeETag hashes sorted "key=value/version|..." pairs to produce a stable 16-char hex tag.
func computeETag(configs []model.Config) string {
	keys := make([]string, len(configs))
	byKey := make(map[string]model.Config, len(configs))
	for i, c := range configs {
		keys[i] = c.Key
		byKey[c.Key] = c
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		c := byKey[k]
		fmt.Fprintf(h, "%s=%s/%d|", k, c.Value, c.Version)
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(id, email, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		u.ID, u.Email, u.PasswordHash, u.CreatedAt)
	return err
}

func (s *SQLite) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE email=?`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

func (s *SQLite) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, created_at FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.CreatedAt); err != nil {
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

// GetUserByAPIKey hashes rawKey (SHA-256 hex) and looks up the matching api_key row,
// then loads the associated user. Returns (nil, nil, nil) if not found.
func (s *SQLite) GetUserByAPIKey(ctx context.Context, rawKey string) (*model.User, *model.APIKey, error) {
	hash := hashKey(rawKey)

	var k model.APIKey
	var lastUsed sql.NullTime
	var expires sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, prefix, key_hash, created_at, last_used_at, expires_at
		 FROM api_keys WHERE key_hash=?`, hash).
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
		`SELECT id, email, password_hash, created_at FROM users WHERE id=?`, k.UserID).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &user, &k, nil
}

func hashKey(rawKey string) string {
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
		`SELECT user_id, project, role, created_at, updated_at FROM project_members WHERE user_id=? AND project=?`,
		userID, project).Scan(&m.UserID, &m.Project, &m.Role, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}

func (s *SQLite) ListProjectMembers(ctx context.Context, project string) ([]model.ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, project, role, created_at, updated_at FROM project_members WHERE project=? ORDER BY user_id`,
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
		e.ID, e.Actor, e.Action, e.Resource, e.Project, e.Environment, e.Before, e.After, e.CreatedAt)
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
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Resource,
			&e.Project, &e.Environment, &e.Before, &e.After, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func nullableRules(r []byte) interface{} {
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
