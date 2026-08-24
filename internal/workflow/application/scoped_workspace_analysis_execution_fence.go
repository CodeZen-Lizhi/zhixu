package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// WorkspaceAnalysisExecutionFenceRequest binds the Workflow facts that an
// Agent Workspace Analysis operation must lock in its caller-owned scope.
type WorkspaceAnalysisExecutionFenceRequest struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
}

// WorkspaceAnalysisExecutionFenceSnapshot is an immutable value projection of
// the locked Workflow definition, run, node, and attempt facts.
type WorkspaceAnalysisExecutionFenceSnapshot struct {
	DefinitionID      foundation.ID
	DefinitionKey     string
	DefinitionVersion int64
	DefinitionGraph   string

	WorkspaceID     foundation.ID
	WorkflowRunID   foundation.ID
	WorkflowStatus  domain.RunStatus
	PauseRequested  bool
	CancelRequested bool

	NodeRunID         foundation.ID
	NodeKey           string
	NodeStatus        domain.NodeStatus
	NodeAttempt       int
	NodeLeaseOwner    string
	NodeLeaseOwnerSet bool
	NodeLeaseUntil    time.Time
	NodeLeaseUntilSet bool

	NodeAttemptID        foundation.ID
	AttemptStatus        domain.AttemptStatus
	AttemptNo            int
	AttemptLeaseOwner    string
	AttemptLeaseOwnerSet bool
	AttemptLeaseUntil    time.Time
	AttemptLeaseUntilSet bool
}

// ScopedWorkspaceAnalysisExecutionFence locks Workflow-owned execution facts
// in the caller's transaction without managing that transaction.
type ScopedWorkspaceAnalysisExecutionFence interface {
	LockWorkspaceAnalysisExecutionScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisExecutionFenceRequest,
	) (WorkspaceAnalysisExecutionFenceSnapshot, bool, error)
}
