package domain

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AuthorizationStatus 是服务端 Write Authorization 的生命周期。
type AuthorizationStatus string

const (
	AuthorizationIssued   AuthorizationStatus = "issued"
	AuthorizationConsumed AuthorizationStatus = "consumed"
	AuthorizationRevoked  AuthorizationStatus = "revoked"
	AuthorizationExpired  AuthorizationStatus = "expired"
)

// Capability 是一次授权允许的最小副作用能力。
type Capability string

const (
	CapabilityWriteKnowledge Capability = "WRITE_KNOWLEDGE"
	CapabilityGitWrite       Capability = "GIT_WRITE"
)

// ToolAuthorization 是不可变审批绑定与可变消费状态的服务端记录。
// TokenHash 仅供持久化 Adapter 使用，禁止写入日志、API 或模型上下文。
type ToolAuthorization struct {
	ID, WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID, RevisionID, ApprovalID        foundation.ID
	ToolName                                  string
	Capability                                Capability
	Scope                                     string
	ApprovedChangeHash                        string
	TargetVersion                             string
	TokenHash                                 string
	IdempotencyKey                            string
	Status                                    AuthorizationStatus
	IssuedAt, ExpiresAt                       time.Time
	RevokedAt, ConsumedAt                     *time.Time
	Version                                   int64
}

// AuthorizationIssue 是签发授权的请求；Workflow Run/Node 必须已持久化。
type AuthorizationIssue struct {
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	ToolName                              string
	Capability                            Capability
	Scope                                 string
	IdempotencyKey                        string
	TTL                                   time.Duration
}

// AuthorizationIssueResult 返回服务端记录和首次签发时的一次性凭据。
type AuthorizationIssueResult struct {
	Authorization ToolAuthorization
	Credential    string
	Replayed      bool
}

// AuthorizationConsume 是消费授权时必须再次提供的完整绑定。
type AuthorizationConsume struct {
	Credential, IdempotencyKey            string
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	ToolName                              string
	Capability                            Capability
	Scope                                 string
	ApprovedChangeHash                    string
	TargetVersion                         string
	// At is retained for deterministic fakes; production adapters use trusted
	// database/service time instead of caller-provided timestamps.
	At time.Time
}

// AuthorizationConsumeResult 标记是否返回了已存在的幂等消费。
type AuthorizationConsumeResult struct {
	Authorization ToolAuthorization
	Replayed      bool
}

// AuthorizationRepository 持有授权的数据库不变量和原子消费边界。
type AuthorizationRepository interface {
	ValidateWorkflowContext(context.Context, foundation.ID, foundation.ID, foundation.ID) error
	GetAuthorization(context.Context, foundation.ID, string, string) (ToolAuthorization, error)
	CreateAuthorization(context.Context, ToolAuthorization) (AuthorizationIssueResult, error)
	ConsumeAuthorization(context.Context, AuthorizationConsume) (AuthorizationConsumeResult, error)
	RevokeAuthorization(context.Context, foundation.ID, time.Time) error
}

// ValidateAuthorizationConsumeBinding 比较消费请求与已持久化授权的全部不可变绑定。
// 调用方必须在读取目标或执行任何状态副作用前完成该校验；Repository 仍需重复校验。
func ValidateAuthorizationConsumeBinding(authorization ToolAuthorization, request AuthorizationConsume) error {
	if authorization.TokenHash != request.Credential ||
		authorization.IdempotencyKey != request.IdempotencyKey ||
		authorization.WorkspaceID != request.WorkspaceID ||
		authorization.WorkflowRunID != request.WorkflowRunID ||
		authorization.NodeRunID != request.NodeRunID ||
		authorization.ProposalID != request.ProposalID ||
		authorization.RevisionID != request.RevisionID ||
		authorization.ApprovalID != request.ApprovalID ||
		authorization.ToolName != request.ToolName ||
		authorization.Capability != request.Capability ||
		authorization.Scope != request.Scope ||
		!strings.EqualFold(authorization.ApprovedChangeHash, request.ApprovedChangeHash) ||
		!strings.EqualFold(authorization.TargetVersion, request.TargetVersion) {
		return errors.New("authorization binding does not match request")
	}
	return nil
}

// ValidateCapability 保证授权能力来自服务端白名单。
func ValidateCapability(capability Capability) error {
	switch capability {
	case CapabilityWriteKnowledge, CapabilityGitWrite:
		return nil
	default:
		return errors.New("unsupported write capability")
	}
}

// ValidateToolBinding restricts a capability to its registered side-effect tool.
func ValidateToolBinding(toolName string, capability Capability) error {
	if err := ValidateCapability(capability); err != nil {
		return err
	}
	switch capability {
	case CapabilityWriteKnowledge:
		if strings.TrimSpace(toolName) != "ApplyApprovedPatch" {
			return errors.New("write knowledge capability requires ApplyApprovedPatch")
		}
	case CapabilityGitWrite:
		if strings.TrimSpace(toolName) != "CreateGitCommit" {
			return errors.New("git write capability requires CreateGitCommit")
		}
	}
	return nil
}

// ExpectedAuthorizationScope binds a capability to the approved target path.
func ExpectedAuthorizationScope(targetPath string) string {
	return "target:" + targetPath
}

// ValidateAuthorizationIssue 校验签发请求的局部格式和 TTL 上限。
func ValidateAuthorizationIssue(command AuthorizationIssue, maxTTL time.Duration) error {
	if command.WorkspaceID == "" || command.WorkflowRunID == "" || command.NodeRunID == "" || command.ProposalID == "" || command.RevisionID == "" || command.ApprovalID == "" || strings.TrimSpace(command.ToolName) == "" || len(strings.TrimSpace(command.ToolName)) > 64 || strings.TrimSpace(command.Scope) == "" || len(strings.TrimSpace(command.Scope)) > 512 || strings.TrimSpace(command.IdempotencyKey) == "" || len(strings.TrimSpace(command.IdempotencyKey)) > 128 {
		return errors.New("authorization binding is incomplete")
	}
	if err := ValidateToolBinding(command.ToolName, command.Capability); err != nil {
		return err
	}
	if command.TTL <= 0 || maxTTL <= 0 || command.TTL > maxTTL {
		return errors.New("authorization ttl is outside the allowed range")
	}
	return nil
}
