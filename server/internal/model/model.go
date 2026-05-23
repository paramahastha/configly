package model

import (
	"encoding/json"
	"time"
)

type ConfigType string

const (
	ConfigTypeString ConfigType = "string"
	ConfigTypeInt    ConfigType = "int"
	ConfigTypeFloat  ConfigType = "float"
	ConfigTypeBool   ConfigType = "bool"
	ConfigTypeJSON   ConfigType = "json"
	ConfigTypeFlag   ConfigType = "flag"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Rollout is a percentage in [0, 100]. Enforced by the service layer.
type Rollout int

type Environment struct {
	ID        string     `json:"id"`
	Project   string     `json:"project"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type Config struct {
	ID          string          `json:"id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Key         string          `json:"key"`
	Type        ConfigType      `json:"type"`
	Value       string          `json:"value"`
	Rollout     Rollout         `json:"rollout"`
	Rules       json.RawMessage `json:"rules,omitempty"`
	Description string          `json:"description,omitempty"`
	Version     int             `json:"version"`
	UpdatedAt   time.Time       `json:"updated_at"`
	UpdatedBy   string          `json:"updated_by"`
	DeletedAt   *time.Time      `json:"deleted_at,omitempty"`
}

// ConfigVersion stores a full copy of each version so history is self-contained
// without joining back to Config. Duplication of Project/Environment/Key is intentional.
type ConfigVersion struct {
	ID          string          `json:"id"`
	ConfigID    string          `json:"config_id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Key         string          `json:"key"`
	Type        ConfigType      `json:"type"`
	Value       string          `json:"value"`
	Rollout     Rollout         `json:"rollout"`
	Rules       json.RawMessage `json:"rules,omitempty"`
	Version     int             `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	CreatedBy   string          `json:"created_by"`
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// ProjectMember records a user's role within a specific project.
// Roles are scoped per-project so a user can be admin of one project and viewer of another.
type ProjectMember struct {
	UserID    string    `json:"user_id"`
	Project   string    `json:"project"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// APIKey belongs to a user and can be rotated or revoked independently.
// The raw key is returned once on creation; only KeyHash is persisted.
type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"` // first 8 chars, shown in listings
	KeyHash    string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type AuditEvent struct {
	ID        string    `json:"id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	Project   string    `json:"project"`
	Env       string    `json:"env"`
	Before    string    `json:"before,omitempty"`
	After     string    `json:"after,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ConfigEntry is the SDK-facing projection of a config.
// Internal DB fields (ID, UpdatedBy, Version, etc.) are excluded from SDK payloads.
type ConfigEntry struct {
	Type    ConfigType      `json:"type"`
	Value   string          `json:"value"`
	Rollout Rollout         `json:"rollout"`
	Rules   json.RawMessage `json:"rules,omitempty"`
}

type Snapshot struct {
	Project     string                 `json:"project"`
	Environment string                 `json:"environment"`
	ETag        string                 `json:"etag"`
	UpdatedAt   time.Time              `json:"updated_at"`
	Configs     map[string]ConfigEntry `json:"configs"`
}
