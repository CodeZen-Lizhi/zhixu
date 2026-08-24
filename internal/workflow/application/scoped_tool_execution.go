package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// ToolExecutionPolicySnapshotRequest binds the Workflow facts required for a
// Tool execution policy decision in the caller-owned transaction.
type ToolExecutionPolicySnapshotRequest struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
}

// ToolExecutionPolicySnapshot is an immutable projection of the locked
// Workflow definition, run, node, attempt, and database time facts.
type ToolExecutionPolicySnapshot struct {
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
	NodeKind          string
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

	DatabaseNow time.Time
}

// ScopedToolExecutionPolicySnapshot locks and returns raw Workflow policy
// facts without making a Tool admission decision or managing the transaction.
type ScopedToolExecutionPolicySnapshot interface {
	LockToolExecutionPolicyScoped(
		context.Context,
		foundation.TransactionScope,
		ToolExecutionPolicySnapshotRequest,
	) (ToolExecutionPolicySnapshot, bool, error)
}

// ToolCallRecoveryFenceRequest binds the Workflow facts required to decide
// whether a started Tool Call has lost its execution lease.
type ToolCallRecoveryFenceRequest struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
}

// ToolCallRecoveryFenceResult distinguishes an absent binding from a lock
// skipped due to contention and from a locked stale execution.
type ToolCallRecoveryFenceResult struct {
	// Found reports that the unlocked binding preflight found the complete
	// Workspace -> Run -> Node -> Attempt binding. Skipped implies Found.
	Found   bool
	Skipped bool
	Stale   bool

	WorkflowStatus domain.RunStatus

	NodeStatus        domain.NodeStatus
	NodeAttempt       int
	NodeLeaseOwner    string
	NodeLeaseOwnerSet bool
	NodeLeaseUntil    time.Time
	NodeLeaseUntilSet bool

	AttemptStatus        domain.AttemptStatus
	AttemptNo            int
	AttemptLeaseOwner    string
	AttemptLeaseOwnerSet bool
	AttemptLeaseUntil    time.Time
	AttemptLeaseUntilSet bool

	DatabaseNow time.Time
}

// ScopedToolCallRecoveryFence locks Workflow execution facts in recovery lock
// order without accessing or managing the Tool Call owned by the caller.
type ScopedToolCallRecoveryFence interface {
	LockToolCallRecoveryScoped(
		context.Context,
		foundation.TransactionScope,
		ToolCallRecoveryFenceRequest,
	) (ToolCallRecoveryFenceResult, error)
}
