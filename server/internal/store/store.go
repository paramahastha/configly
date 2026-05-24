package store

import (
	"context"
	"errors"
	"time"

	"github.com/paramahastha/configly/server/internal/model"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

type Store interface {
	// Environments / projects
	CreateEnvironment(ctx context.Context, env *model.Environment) error
	DeleteEnvironment(ctx context.Context, project, name string) error
	ListEnvironments(ctx context.Context, project string) ([]model.Environment, error)
	ListProjects(ctx context.Context) ([]string, error)

	// Configs
	UpsertConfig(ctx context.Context, c *model.Config, actor string) error
	GetConfig(ctx context.Context, project, env, key string) (*model.Config, error)
	ListConfigs(ctx context.Context, project, env string) ([]model.Config, error)
	DeleteConfig(ctx context.Context, project, env, key, actor string) error

	// Versions / rollback
	ListVersions(ctx context.Context, configID string, limit int) ([]model.ConfigVersion, error)
	// Rollback restores configID to version and returns the project and environment it belongs to
	// so callers can publish change notifications.
	Rollback(ctx context.Context, configID string, version int, actor string) (project, env string, err error)

	// Snapshot (hot read path — in-memory cached)
	Snapshot(ctx context.Context, project, env string) (*model.Snapshot, error)

	// Users
	CreateUser(ctx context.Context, u *model.User) error
	DeleteUser(ctx context.Context, id string) error
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListUsers(ctx context.Context) ([]model.User, error)

	// API Keys
	// CreateAPIKey hashes rawKey and persists only the hash; the raw key must be stored by the caller.
	CreateAPIKey(ctx context.Context, k *model.APIKey, rawKey, actor string) error
	RevokeAPIKey(ctx context.Context, id string) error
	// UpdateLastUsedAt records when the key was last used for authentication.
	UpdateLastUsedAt(ctx context.Context, keyID string, t time.Time) error
	// GetUserByAPIKey hashes rawKey with SHA-256 and looks up the matching api_key row,
	// then loads the associated user. Returns both so the caller can call UpdateLastUsedAt.
	GetUserByAPIKey(ctx context.Context, rawKey string) (*model.User, *model.APIKey, error)

	// RBAC — roles are scoped per project via ProjectMember
	SetProjectMember(ctx context.Context, m *model.ProjectMember) error
	RemoveProjectMember(ctx context.Context, userID, project string) error
	GetProjectMember(ctx context.Context, userID, project string) (*model.ProjectMember, error)
	ListProjectMembers(ctx context.Context, project string) ([]model.ProjectMember, error)

	// Audit
	AppendAudit(ctx context.Context, e *model.AuditEvent) error
	ListAudit(ctx context.Context, project string, limit int) ([]model.AuditEvent, error)

	Close() error
}
