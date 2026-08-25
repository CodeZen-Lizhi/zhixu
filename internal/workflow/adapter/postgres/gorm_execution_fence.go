package workflowpostgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// GORMWorkspaceAnalysisExecutionFence locks Workflow-owned execution facts for
// an Agent operation without owning the caller's transaction.
type GORMWorkspaceAnalysisExecutionFence struct {
	database *gorm.DB
}

// NewGORMWorkspaceAnalysisExecutionFence validates the shared platform root.
func NewGORMWorkspaceAnalysisExecutionFence(pool *platformpostgres.Pool) (*GORMWorkspaceAnalysisExecutionFence, error) {
	if pool == nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", errors.New("workflow execution fence pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", err)
	}
	if !validGORMWorkflowDatabase(database) {
		return nil, gormWorkflowUnavailable("WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", errors.New("workflow execution fence database is unavailable"))
	}
	return &GORMWorkspaceAnalysisExecutionFence{database: database}, nil
}

// LockWorkspaceAnalysisExecutionScoped locks Run, Node Run, and Node Attempt
// in order and returns false for any missing or mismatched binding.
func (fence *GORMWorkspaceAnalysisExecutionFence) LockWorkspaceAnalysisExecutionScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	request application.WorkspaceAnalysisExecutionFenceRequest,
) (application.WorkspaceAnalysisExecutionFenceSnapshot, bool, error) {
	if fence == nil || !validGORMWorkflowDatabase(fence.database) {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, gormWorkflowUnavailable("WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", errors.New("workflow execution fence is unavailable"))
	}
	if ctx == nil || !validGORMWorkflowFenceRequest(request) {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_INVALID", false, errors.New("workflow execution fence request is invalid"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, gormWorkflowUnavailable("WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE", err)
	}
	snapshot, found, err := lockGORMWorkspaceAnalysisExecution(ctx, transaction.WithContext(ctx), request)
	if err != nil {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_UNAVAILABLE")
	}
	return snapshot, found, nil
}

func lockGORMWorkspaceAnalysisExecution(
	ctx context.Context,
	transaction *gorm.DB,
	request application.WorkspaceAnalysisExecutionFenceRequest,
) (application.WorkspaceAnalysisExecutionFenceSnapshot, bool, error) {
	var snapshot application.WorkspaceAnalysisExecutionFenceSnapshot
	var definitionID, workspaceID, workflowRunID, runStatus string
	var pauseRequestedAt, cancelRequestedAt sql.NullTime
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		definition.id::text,definition.key,definition.version,definition.graph::text,
		run.workspace_id::text,run.id::text,run.status,run.pause_requested_at,run.cancel_requested_at
		FROM workflow.run AS run
		JOIN workflow.definition AS definition
		  ON definition.id=run.definition_id AND definition.workspace_id=run.workspace_id
		WHERE run.id=?::uuid AND run.workspace_id=?::uuid
		FOR UPDATE OF run`, string(request.WorkflowRunID), string(request.WorkspaceID))
	if err != nil {
		return snapshot, false, err
	}
	if err := row.Scan(
		&definitionID, &snapshot.DefinitionKey, &snapshot.DefinitionVersion, &snapshot.DefinitionGraph,
		&workspaceID, &workflowRunID, &runStatus, &pauseRequestedAt, &cancelRequestedAt,
	); err != nil {
		if gormWorkflowNoRows(err) {
			return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, nil
		}
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, err
	}
	snapshot.DefinitionID = foundation.ID(definitionID)
	snapshot.WorkspaceID = foundation.ID(workspaceID)
	snapshot.WorkflowRunID = foundation.ID(workflowRunID)
	snapshot.WorkflowStatus = domain.RunStatus(runStatus)
	snapshot.PauseRequested = pauseRequestedAt.Valid
	snapshot.CancelRequested = cancelRequestedAt.Valid
	if snapshot.WorkspaceID != request.WorkspaceID || snapshot.WorkflowRunID != request.WorkflowRunID || !validGORMWorkflowFenceRun(snapshot) {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, nil
	}

	var nodeRunID, parentRunID, nodeStatus string
	var nodeLeaseOwner sql.NullString
	var nodeLeaseUntil sql.NullTime
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		id::text,run_id::text,node_key,status,attempt,lease_owner,lease_until
		FROM workflow.node_run
		WHERE id=?::uuid AND run_id=?::uuid
		FOR UPDATE`, string(request.NodeRunID), string(request.WorkflowRunID))
	if err != nil {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, err
	}
	if err := row.Scan(
		&nodeRunID, &parentRunID, &snapshot.NodeKey, &nodeStatus, &snapshot.NodeAttempt,
		&nodeLeaseOwner, &nodeLeaseUntil,
	); err != nil {
		if gormWorkflowNoRows(err) {
			return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, nil
		}
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, err
	}
	snapshot.NodeRunID = foundation.ID(nodeRunID)
	snapshot.NodeStatus = domain.NodeStatus(nodeStatus)
	snapshot.NodeLeaseOwner, snapshot.NodeLeaseOwnerSet = nodeLeaseOwner.String, nodeLeaseOwner.Valid
	snapshot.NodeLeaseUntil, snapshot.NodeLeaseUntilSet = nodeLeaseUntil.Time.UTC(), nodeLeaseUntil.Valid
	if snapshot.NodeRunID != request.NodeRunID || foundation.ID(parentRunID) != request.WorkflowRunID || !validGORMWorkflowFenceNode(snapshot) {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, nil
	}

	var attemptID, parentNodeID, attemptStatus string
	var attemptLeaseOwner sql.NullString
	var attemptLeaseUntil sql.NullTime
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		id::text,node_run_id::text,status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt
		WHERE id=?::uuid AND node_run_id=?::uuid
		FOR UPDATE`, string(request.NodeAttemptID), string(request.NodeRunID))
	if err != nil {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, err
	}
	if err := row.Scan(
		&attemptID, &parentNodeID, &attemptStatus, &snapshot.AttemptNo,
		&attemptLeaseOwner, &attemptLeaseUntil,
	); err != nil {
		if gormWorkflowNoRows(err) {
			return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, nil
		}
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, err
	}
	snapshot.NodeAttemptID = foundation.ID(attemptID)
	snapshot.AttemptStatus = domain.AttemptStatus(attemptStatus)
	snapshot.AttemptLeaseOwner, snapshot.AttemptLeaseOwnerSet = attemptLeaseOwner.String, attemptLeaseOwner.Valid
	snapshot.AttemptLeaseUntil, snapshot.AttemptLeaseUntilSet = attemptLeaseUntil.Time.UTC(), attemptLeaseUntil.Valid
	if snapshot.NodeAttemptID != request.NodeAttemptID || foundation.ID(parentNodeID) != request.NodeRunID || !validGORMWorkflowFenceAttempt(snapshot) {
		return application.WorkspaceAnalysisExecutionFenceSnapshot{}, false, nil
	}
	return snapshot, true, nil
}

func validGORMWorkflowFenceRequest(request application.WorkspaceAnalysisExecutionFenceRequest) bool {
	return validGORMWorkflowID(request.WorkspaceID) && validGORMWorkflowID(request.WorkflowRunID) &&
		validGORMWorkflowID(request.NodeRunID) && validGORMWorkflowID(request.NodeAttemptID)
}

func validGORMWorkflowFenceRun(snapshot application.WorkspaceAnalysisExecutionFenceSnapshot) bool {
	if !validGORMWorkflowID(snapshot.DefinitionID) || strings.TrimSpace(snapshot.DefinitionKey) == "" ||
		snapshot.DefinitionVersion < 1 || strings.TrimSpace(snapshot.DefinitionGraph) == "" {
		return false
	}
	switch snapshot.WorkflowStatus {
	case domain.RunStatusPending, domain.RunStatusRunning, domain.RunStatusWaitingForHuman,
		domain.RunStatusRetryWait, domain.RunStatusPaused, domain.RunStatusSucceeded,
		domain.RunStatusFailed, domain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func validGORMWorkflowFenceNode(snapshot application.WorkspaceAnalysisExecutionFenceSnapshot) bool {
	if strings.TrimSpace(snapshot.NodeKey) == "" || snapshot.NodeAttempt < 0 ||
		snapshot.NodeLeaseOwnerSet != snapshot.NodeLeaseUntilSet {
		return false
	}
	switch snapshot.NodeStatus {
	case domain.NodeStatusPending, domain.NodeStatusRunning, domain.NodeStatusWaitingForHuman,
		domain.NodeStatusRetryWait, domain.NodeStatusPaused, domain.NodeStatusSucceeded,
		domain.NodeStatusFailed, domain.NodeStatusCancelled:
		return true
	default:
		return false
	}
}

func validGORMWorkflowFenceAttempt(snapshot application.WorkspaceAnalysisExecutionFenceSnapshot) bool {
	if snapshot.AttemptNo < 1 || snapshot.AttemptLeaseOwnerSet != snapshot.AttemptLeaseUntilSet {
		return false
	}
	switch snapshot.AttemptStatus {
	case domain.AttemptStatusRunning, domain.AttemptStatusSucceeded, domain.AttemptStatusWaitingForHuman,
		domain.AttemptStatusRetryScheduled, domain.AttemptStatusFailed, domain.AttemptStatusManualRecovery,
		domain.AttemptStatusLeaseLost, domain.AttemptStatusCancelled:
		return true
	default:
		return false
	}
}

func validGORMWorkflowID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

var _ application.ScopedWorkspaceAnalysisExecutionFence = (*GORMWorkspaceAnalysisExecutionFence)(nil)
