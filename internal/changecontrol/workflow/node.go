// Package changecontrolworkflow adapts Safe Writeback Application Saga to a
// durable Workflow node without making Workflow payload the writeback fact source.
package changecontrolworkflow

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Resumer 是固定 Safe Writeback Node 使用的 Application 端口。
type Resumer interface {
	Resume(context.Context, foundation.ID, string) (application.WritebackResult, error)
}

// Node 执行一条已由 Atomic Begin 创建的 Durable Writeback Execution。
// Credential、正文、目标路径和 Git 参数不会进入 Node 输入。
type Node struct{ resumer Resumer }

// NewNode 创建固定 Safe Writeback Workflow Node。
func NewNode(resumer Resumer) (*Node, error) {
	if resumer == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_WORKFLOW_NODE_UNAVAILABLE", false, errors.New("writeback resumer is nil"))
	}
	return &Node{resumer: resumer}, nil
}

// Input 只保存可恢复的稳定 Execution 与 Workflow 身份。
type Input struct {
	SchemaVersion int           `json:"schema_version"`
	ExecutionID   foundation.ID `json:"execution_id"`
	WorkspaceID   foundation.ID `json:"workspace_id"`
	WorkflowRunID foundation.ID `json:"workflow_run_id"`
	NodeRunID     foundation.ID `json:"node_run_id"`
}

// Output 表示写回已经发布到 verifying/index_pending；不表示 Retrieval 或 Regression 已完成。
type Output struct {
	SchemaVersion int                    `json:"schema_version"`
	ExecutionID   foundation.ID          `json:"execution_id"`
	ProposalID    foundation.ID          `json:"proposal_id"`
	RevisionID    foundation.ID          `json:"revision_id"`
	Status        domain.WritebackStatus `json:"status"`
	GitCommit     string                 `json:"git_commit"`
	ResultHash    string                 `json:"result_hash"`
	DiffHash      string                 `json:"diff_hash"`
	IndexStatus   string                 `json:"index_status"`
}

// Execute 使用 Worker 当前 lease owner 继续 Saga；lease owner 不写入持久化 Node input。
func (n *Node) Execute(ctx context.Context, input Input, leaseOwner string) (Output, error) {
	if n == nil || n.resumer == nil {
		return Output{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_WORKFLOW_NODE_UNAVAILABLE", false, errors.New("writeback node is unavailable"))
	}
	if input.SchemaVersion != application.SafeWritebackSchemaVersion || input.ExecutionID == "" || input.WorkspaceID == "" || input.WorkflowRunID == "" || input.NodeRunID == "" || strings.TrimSpace(leaseOwner) == "" {
		return Output{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_WORKFLOW_INPUT_INVALID", false, errors.New("writeback workflow input is incomplete"))
	}
	result, err := n.resumer.Resume(ctx, input.ExecutionID, strings.TrimSpace(leaseOwner))
	if err != nil {
		return Output{}, err
	}
	if result.ExecutionID != input.ExecutionID || result.WorkspaceID != input.WorkspaceID || result.WorkflowRunID != input.WorkflowRunID || result.NodeRunID != input.NodeRunID {
		return Output{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_WORKFLOW_BINDING_INVALID", false, domain.ErrWritebackIdentityConflict)
	}
	if result.ProposalID == "" || result.RevisionID == "" || result.Status != domain.WritebackStatusVerifying || result.IndexStatus != application.WritebackIndexStatusPending || result.CleanupPending || result.RecoveryRequired || !domain.ValidGitHead(result.GitCommit) || !domain.ValidHash(result.ResultHash) || !domain.ValidHash(result.DiffHash) {
		return Output{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_WORKFLOW_RESULT_INVALID", false, domain.ErrWritebackIdentityConflict)
	}
	return Output{
		SchemaVersion: application.SafeWritebackSchemaVersion, ExecutionID: result.ExecutionID,
		ProposalID: result.ProposalID, RevisionID: result.RevisionID, Status: result.Status,
		GitCommit: strings.ToLower(result.GitCommit), ResultHash: strings.ToLower(result.ResultHash),
		DiffHash: strings.ToLower(result.DiffHash), IndexStatus: result.IndexStatus,
	}, nil
}
