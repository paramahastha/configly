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

type Environment struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Config struct {
	ID          string          `json:"id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Key         string          `json:"key"`
	Type        ConfigType      `json:"type"`
	Value       string          `json:"value"`
	Rollout     int             `json:"rollout"`
	Rules       json.RawMessage `json:"rules,omitempty"`
	Description string          `json:"description,omitempty"`
	Version     int             `json:"version"`
	UpdatedAt   time.Time       `json:"updated_at"`
	UpdatedBy   string          `json:"updated_by"`
}

type ConfigVersion struct {
	ID          string          `json:"id"`
	ConfigID    string          `json:"config_id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Key         string          `json:"key"`
	Type        ConfigType      `json:"type"`
	Value       string          `json:"value"`
	Rollout     int             `json:"rollout"`
	Rules       json.RawMessage `json:"rules,omitempty"`
	Version     int             `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	CreatedBy   string          `json:"created_by"`
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	APIKey       string    `json:"api_key"`
	CreatedAt    time.Time `json:"created_at"`
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

type Snapshot struct {
	Project     string            `json:"project"`
	Environment string            `json:"environment"`
	ETag        string            `json:"etag"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Configs     map[string]Config `json:"configs"`
}
