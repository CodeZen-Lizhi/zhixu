package workflowpostgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// GORMToolCallRecoveryFence locks Workflow execution facts before the Tools
// owner locks and recovers its started Tool Call.
type GORMToolCallRecoveryFence struct {
	database *gorm.DB
}

// NewGORMToolCallRecoveryFence validates the shared platform root.
func NewGORMToolCallRecoveryFence(pool *platformpostgres.Pool) (*GORMToolCallRecoveryFence, error) {
	if pool == nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", errors.New("workflow Tool recovery fence pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", err)
	}
	if !validGORMWorkflowDatabase(database) {
		return nil, gormWorkflowUnavailable("WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", errors.New("workflow Tool recovery fence database is unavailable"))
	}
	return &GORMToolCallRecoveryFence{database: database}, nil
}

// LockToolCallRecoveryScoped performs an unlocked binding preflight and then
// locks Node Run followed by Node Attempt with SKIP LOCKED.
func (fence *GORMToolCallRecoveryFence) LockToolCallRecoveryScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	request application.ToolCallRecoveryFenceRequest,
) (application.ToolCallRecoveryFenceResult, error) {
	if fence == nil || !validGORMWorkflowDatabase(fence.database) {
		return application.ToolCallRecoveryFenceResult{}, gormWorkflowUnavailable("WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", errors.New("workflow Tool recovery fence is unavailable"))
	}
	if ctx == nil || !validGORMToolCallRecoveryFenceRequest(request) {
		return application.ToolCallRecoveryFenceResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_INVALID", false, errors.New("workflow Tool recovery fence request is invalid"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return application.ToolCallRecoveryFenceResult{}, gormWorkflowUnavailable("WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE", err)
	}

	result, err := lockGORMToolCallRecoveryFence(ctx, transaction.WithContext(ctx), request)
	if err != nil {
		return application.ToolCallRecoveryFenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_TOOL_CALL_RECOVERY_FENCE_UNAVAILABLE")
	}
	return result, nil
}

func lockGORMToolCallRecoveryFence(
	ctx context.Context,
	transaction *gorm.DB,
	request application.ToolCallRecoveryFenceRequest,
) (application.ToolCallRecoveryFenceResult, error) {
	found, err := gormToolCallRecoveryBindingExists(ctx, transaction, request)
	if err != nil || !found {
		return application.ToolCallRecoveryFenceResult{}, err
	}

	var result application.ToolCallRecoveryFenceResult
	var nodeRunID, parentRunID, runStatus, nodeStatus string
	var nodeLeaseOwner sql.NullString
	var nodeLeaseUntil sql.NullTime
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		n.id::text,n.run_id::text,r.status,n.status,n.attempt,n.lease_owner,n.lease_until
		FROM workflow.node_run AS n
		JOIN workflow.run AS r ON r.id=n.run_id AND r.workspace_id=?::uuid
		WHERE n.id=?::uuid AND n.run_id=?::uuid
		FOR UPDATE OF n SKIP LOCKED`,
		string(request.WorkspaceID), string(request.NodeRunID), string(request.WorkflowRunID),
	)
	if err != nil {
		return application.ToolCallRecoveryFenceResult{}, err
	}
	if err := row.Scan(
		&nodeRunID, &parentRunID, &runStatus, &nodeStatus, &result.NodeAttempt,
		&nodeLeaseOwner, &nodeLeaseUntil,
	); err != nil {
		if gormWorkflowNoRows(err) {
			return application.ToolCallRecoveryFenceResult{Found: true, Skipped: true}, nil
		}
		return application.ToolCallRecoveryFenceResult{}, err
	}
	result.WorkflowStatus = domain.RunStatus(runStatus)
	result.NodeStatus = domain.NodeStatus(nodeStatus)
	result.NodeLeaseOwner, result.NodeLeaseOwnerSet = nodeLeaseOwner.String, nodeLeaseOwner.Valid
	result.NodeLeaseUntil, result.NodeLeaseUntilSet = nodeLeaseUntil.Time.UTC(), nodeLeaseUntil.Valid
	if foundation.ID(nodeRunID) != request.NodeRunID || foundation.ID(parentRunID) != request.WorkflowRunID {
		return application.ToolCallRecoveryFenceResult{}, nil
	}

	var nodeAttemptID, parentNodeRunID, attemptStatus string
	var attemptLeaseOwner sql.NullString
	var attemptLeaseUntil sql.NullTime
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		id::text,node_run_id::text,status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt
		WHERE id=?::uuid AND node_run_id=?::uuid
		FOR UPDATE SKIP LOCKED`, string(request.NodeAttemptID), string(request.NodeRunID))
	if err != nil {
		return application.ToolCallRecoveryFenceResult{}, err
	}
	if err := row.Scan(
		&nodeAttemptID, &parentNodeRunID, &attemptStatus, &result.AttemptNo,
		&attemptLeaseOwner, &attemptLeaseUntil,
	); err != nil {
		if gormWorkflowNoRows(err) {
			return application.ToolCallRecoveryFenceResult{Found: true, Skipped: true}, nil
		}
		return application.ToolCallRecoveryFenceResult{}, err
	}
	result.AttemptStatus = domain.AttemptStatus(attemptStatus)
	result.AttemptLeaseOwner, result.AttemptLeaseOwnerSet = attemptLeaseOwner.String, attemptLeaseOwner.Valid
	result.AttemptLeaseUntil, result.AttemptLeaseUntilSet = attemptLeaseUntil.Time.UTC(), attemptLeaseUntil.Valid
	if foundation.ID(nodeAttemptID) != request.NodeAttemptID || foundation.ID(parentNodeRunID) != request.NodeRunID {
		return application.ToolCallRecoveryFenceResult{}, nil
	}
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT clock_timestamp()`)
	if err != nil {
		return application.ToolCallRecoveryFenceResult{}, err
	}
	if err := row.Scan(&result.DatabaseNow); err != nil {
		return application.ToolCallRecoveryFenceResult{}, err
	}
	result.DatabaseNow = result.DatabaseNow.UTC()
	if !validGORMToolCallRecoveryFenceResult(result) {
		return application.ToolCallRecoveryFenceResult{}, nil
	}

	result.Found = true
	result.Stale = staleGORMToolCallExecution(result)
	return result, nil
}

func gormToolCallRecoveryBindingExists(
	ctx context.Context,
	transaction *gorm.DB,
	request application.ToolCallRecoveryFenceRequest,
) (bool, error) {
	var marker int
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT 1
		FROM workflow.run AS r
		JOIN workflow.node_run AS n ON n.run_id=r.id
		JOIN workflow.node_attempt AS a ON a.node_run_id=n.id
		WHERE r.workspace_id=?::uuid AND r.id=?::uuid
		  AND n.id=?::uuid AND a.id=?::uuid`,
		string(request.WorkspaceID), string(request.WorkflowRunID),
		string(request.NodeRunID), string(request.NodeAttemptID),
	)
	if err != nil {
		return false, err
	}
	if err := row.Scan(&marker); err != nil {
		if gormWorkflowNoRows(err) {
			return false, nil
		}
		return false, err
	}
	return marker == 1, nil
}

func validGORMToolCallRecoveryFenceRequest(request application.ToolCallRecoveryFenceRequest) bool {
	return validGORMWorkflowID(request.WorkspaceID) && validGORMWorkflowID(request.WorkflowRunID) &&
		validGORMWorkflowID(request.NodeRunID) && validGORMWorkflowID(request.NodeAttemptID)
}

func validGORMToolCallRecoveryFenceResult(result application.ToolCallRecoveryFenceResult) bool {
	fenceSnapshot := application.WorkspaceAnalysisExecutionFenceSnapshot{
		WorkflowStatus: result.WorkflowStatus,
		NodeKey:        "recovery", NodeStatus: result.NodeStatus, NodeAttempt: result.NodeAttempt,
		NodeLeaseOwnerSet: result.NodeLeaseOwnerSet, NodeLeaseUntilSet: result.NodeLeaseUntilSet,
		AttemptStatus: result.AttemptStatus, AttemptNo: result.AttemptNo,
		AttemptLeaseOwnerSet: result.AttemptLeaseOwnerSet, AttemptLeaseUntilSet: result.AttemptLeaseUntilSet,
	}
	return !result.DatabaseNow.IsZero() && validGORMWorkflowFenceNode(fenceSnapshot) &&
		validGORMWorkflowFenceAttempt(fenceSnapshot) && validGORMWorkflowRunStatus(result.WorkflowStatus)
}

func validGORMWorkflowRunStatus(status domain.RunStatus) bool {
	switch status {
	case domain.RunStatusPending, domain.RunStatusRunning, domain.RunStatusWaitingForHuman,
		domain.RunStatusRetryWait, domain.RunStatusPaused, domain.RunStatusSucceeded,
		domain.RunStatusFailed, domain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func staleGORMToolCallExecution(result application.ToolCallRecoveryFenceResult) bool {
	if result.WorkflowStatus != domain.RunStatusRunning || result.NodeStatus != domain.NodeStatusRunning ||
		result.AttemptStatus != domain.AttemptStatusRunning || result.NodeAttempt != result.AttemptNo {
		return true
	}
	if !result.NodeLeaseOwnerSet || !result.AttemptLeaseOwnerSet ||
		result.NodeLeaseOwner != result.AttemptLeaseOwner ||
		!result.NodeLeaseUntilSet || !result.AttemptLeaseUntilSet {
		return true
	}
	return !result.NodeLeaseUntil.Equal(result.AttemptLeaseUntil) || !result.NodeLeaseUntil.After(result.DatabaseNow)
}

var _ application.ScopedToolCallRecoveryFence = (*GORMToolCallRecoveryFence)(nil)
