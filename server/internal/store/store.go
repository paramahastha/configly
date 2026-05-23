package store

import (
	"context"

	"github.com/paramahastha/configly/server/internal/model"
)

type Store interface {
	// Environments / projects
	CreateEnvironment(ctx context.Context, env *model.Environment) error
	ListEnvironments(ctx context.Context, project string) ([]model.Environment, error)
	ListProjects(ctx context.Context) ([]string, error)

	// Configs
	UpsertConfig(ctx context.Context, c *model.Config, actor string) error
	GetConfig(ctx context.Context, project, env, key string) (*model.Config, error)
	ListConfigs(ctx context.Context, project, env string) ([]model.Config, error)
	DeleteConfig(ctx context.Context, project, env, key, actor string) error

	// Versions / rollback
	ListVersions(ctx context.Context, configID string, limit int) ([]model.ConfigVersion, error)
	Rollback(ctx context.Context, configID string, version int, actor string) error

	// Snapshot (hot read path — in-memory cached)
	Snapshot(ctx context.Context, project, env string) (*model.Snapshot, error)

	// Users
	CreateUser(ctx context.Context, u *model.User) error
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListUsers(ctx context.Context) ([]model.User, error)

	// API Keys
	CreateAPIKey(ctx context.Context, k *model.APIKey, actor string) error
	// GetUserByAPIKey hashes rawKey with SHA-256 and looks up the matching api_key row,
	// then loads the associated user. Returns both so the caller can update LastUsedAt.
	GetUserByAPIKey(ctx context.Context, rawKey string) (*model.User, *model.APIKey, error)

	// RBAC — roles are scoped per project via ProjectMember
	SetProjectMember(ctx context.Context, m *model.ProjectMember) error
	GetProjectMember(ctx context.Context, userID, project string) (*model.ProjectMember, error)
	ListProjectMembers(ctx context.Context, project string) ([]model.ProjectMember, error)

	// Audit
	AppendAudit(ctx context.Context, e *model.AuditEvent) error
	ListAudit(ctx context.Context, project string, limit int) ([]model.AuditEvent, error)

	Close() error
}
