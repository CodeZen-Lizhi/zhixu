package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// ScopedTerminalHook 通过稳定结果 Port 参与 Workflow 成功终态提交。
type ScopedTerminalHook struct {
	results organizingapp.ScopedTerminalResultWriter
}

// NewScopedTerminalHook 注入结果写入者，公共 Workflow 层不依赖数据库驱动。
func NewScopedTerminalHook(results organizingapp.ScopedTerminalResultWriter) (*ScopedTerminalHook, error) {
	if nilScopedDependency(results) {
		return nil, terminalUnavailable(errors.New("organizing terminal result writer is unavailable"))
	}
	return &ScopedTerminalHook{results: results}, nil
}

// OnWorkflowNodeTerminalScoped 只处理 Organizing 最终节点的成功终态。
func (hook *ScopedTerminalHook) OnWorkflowNodeTerminalScoped(ctx context.Context, scope foundation.TransactionScope, event workflowapp.WorkflowNodeTerminalEvent) error {
	definitionKey, expectedKind, final := terminalContract(event.NodeKind)
	if !final || event.Outcome != workflowapp.WorkflowTerminalOutcomeSucceeded {
		return nil
	}
	if hook == nil || nilScopedDependency(hook.results) {
		return terminalUnavailable(errors.New("organizing terminal result writer is unavailable"))
	}
	if ctx == nil {
		return terminalInvalid(errors.New("organizing terminal context is invalid"))
	}
	if !validID(event.WorkspaceID) || !validID(event.WorkflowRunID) || !validID(event.NodeRunID) || !validID(event.NodeAttemptID) ||
		event.WorkflowRunID == event.NodeRunID || event.TerminalAt.IsZero() {
		return terminalInvalid(errors.New("organizing terminal event identity is invalid"))
	}
	receipt, err := decodeFinalReceipt([]byte(event.TerminalOutput))
	if err != nil {
		return err
	}
	if receipt.Kind != expectedKind {
		return terminalInvalid(errors.New("organizing terminal receipt kind does not match the final node"))
	}
	result := organizingdomain.RunResult{ID: receipt.ResultID, WorkspaceID: event.WorkspaceID, RunBindingID: receipt.RunBindingID,
		SnapshotID: receipt.SnapshotID, WorkflowRunID: event.WorkflowRunID, NodeRunID: event.NodeRunID, Kind: receipt.Kind,
		ResultRef: receipt.ResultRef, ResultHash: receipt.ResultHash, CreatedAt: canonicalTime(event.TerminalAt)}
	if err := result.Validate(); err != nil {
		return terminalInvalid(fmt.Errorf("validate organizing terminal result: %w", err))
	}
	return hook.results.BindSucceededResultScoped(ctx, scope, organizingapp.TerminalResultRequest{Result: result, DefinitionKey: definitionKey, DefinitionVersion: DefinitionVersion})
}

var _ workflowapp.ScopedWorkflowTerminalHook = (*ScopedTerminalHook)(nil)
