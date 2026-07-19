package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// Permission 是 Workflow Definition 声明的 canonical Capability 别名。
type Permission = capability.Capability

const (
	// PermissionReadLocal 允许读取当前 Workspace 内的受控对象。
	PermissionReadLocal = capability.ReadLocal
	// PermissionReadExternal 允许访问策略明确允许的外部公开资源。
	PermissionReadExternal = capability.ReadExternal
	// PermissionWriteProposal 允许创建候选内容或 Proposal。
	PermissionWriteProposal = capability.WriteProposal
	// PermissionWriteKnowledge 允许进入已批准的知识写回流程。
	PermissionWriteKnowledge = capability.WriteKnowledge
	// PermissionGitWrite 允许进入已批准的 Git 提交流程。
	PermissionGitWrite = capability.GitWrite
	// PermissionIndexMaintenance 允许执行受控的索引维护操作。
	PermissionIndexMaintenance = capability.IndexMaintenance
	// PermissionEvaluationRun 允许运行受控的版本化评测。
	PermissionEvaluationRun = capability.EvaluationRun
)

// RetryPolicy declares the bounded business retry policy for one node.
type RetryPolicy struct {
	MaxRetries int           `json:"max_retries"`
	BaseDelay  time.Duration `json:"base_delay"`
	MaxDelay   time.Duration `json:"max_delay"`
}

// NodeDefinition 是注册后不可变的 Workflow DAG 节点契约。
type NodeDefinition struct {
	Key                 string                `json:"key"`
	Kind                string                `json:"kind"`
	Dependencies        []string              `json:"dependencies,omitempty"`
	InputSchemaVersion  int                   `json:"input_schema_version"`
	OutputSchemaVersion int                   `json:"output_schema_version"`
	RetryPolicy         RetryPolicy           `json:"retry_policy"`
	RequiredPermissions []Permission          `json:"required_permissions,omitempty"`
	AllowedTools        []toolsdomain.ToolRef `json:"allowed_tools,omitempty"`
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
