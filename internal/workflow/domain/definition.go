package domain

import "time"

// Permission is a stable capability name declared by a workflow definition.
type Permission string

const (
	PermissionReadLocal        Permission = "READ_LOCAL"
	PermissionReadExternal     Permission = "READ_EXTERNAL"
	PermissionWriteProposal    Permission = "WRITE_PROPOSAL"
	PermissionWriteKnowledge   Permission = "WRITE_KNOWLEDGE"
	PermissionGitWrite         Permission = "GIT_WRITE"
	PermissionAdminMaintenance Permission = "ADMIN_MAINTENANCE"
)

// RetryPolicy declares the bounded business retry policy for one node.
type RetryPolicy struct {
	MaxRetries int           `json:"max_retries"`
	BaseDelay  time.Duration `json:"base_delay"`
	MaxDelay   time.Duration `json:"max_delay"`
}

// NodeDefinition is one immutable node in a registered canonical workflow DAG.
type NodeDefinition struct {
	Key                 string       `json:"key"`
	Kind                string       `json:"kind"`
	Dependencies        []string     `json:"dependencies,omitempty"`
	InputSchemaVersion  int          `json:"input_schema_version"`
	OutputSchemaVersion int          `json:"output_schema_version"`
	RetryPolicy         RetryPolicy  `json:"retry_policy"`
	RequiredPermissions []Permission `json:"required_permissions,omitempty"`
}

// CanonicalGraph is a normalized, stable workflow DAG representation.
type CanonicalGraph struct {
	Nodes []NodeDefinition `json:"nodes"`
}

// RegisteredDefinition is a server-owned immutable workflow definition.
type RegisteredDefinition struct {
	Key                string
	Version            int64
	InputSchemaVersion int
	Graph              CanonicalGraph
	GraphHash          string
}
