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

// GORMToolExecutionPolicySnapshot reads Workflow-owned policy facts without
// owning the caller's transaction or making a Tool admission decision.
type GORMToolExecutionPolicySnapshot struct {
	database *gorm.DB
}

// NewGORMToolExecutionPolicySnapshot validates the shared platform root.
func NewGORMToolExecutionPolicySnapshot(pool *platformpostgres.Pool) (*GORMToolExecutionPolicySnapshot, error) {
	if pool == nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", errors.New("workflow Tool policy pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", err)
	}
	if !validGORMWorkflowDatabase(database) {
		return nil, gormWorkflowUnavailable("WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", errors.New("workflow Tool policy database is unavailable"))
	}
	return &GORMToolExecutionPolicySnapshot{database: database}, nil
}

// LockToolExecutionPolicyScoped takes the legacy-equivalent shared locks and
// returns the resulting raw Workflow facts.
func (reader *GORMToolExecutionPolicySnapshot) LockToolExecutionPolicyScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	request application.ToolExecutionPolicySnapshotRequest,
) (application.ToolExecutionPolicySnapshot, bool, error) {
	if reader == nil || !validGORMWorkflowDatabase(reader.database) {
		return application.ToolExecutionPolicySnapshot{}, false, gormWorkflowUnavailable("WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", errors.New("workflow Tool policy reader is unavailable"))
	}
	if ctx == nil || !validGORMToolExecutionPolicyRequest(request) {
		return application.ToolExecutionPolicySnapshot{}, false, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_TOOL_EXECUTION_POLICY_INVALID", false, errors.New("workflow Tool policy request is invalid"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return application.ToolExecutionPolicySnapshot{}, false, gormWorkflowUnavailable("WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE", err)
	}

	snapshot, found, err := lockGORMToolExecutionPolicy(ctx, transaction.WithContext(ctx), request)
	if err != nil {
		return application.ToolExecutionPolicySnapshot{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_TOOL_EXECUTION_POLICY_UNAVAILABLE")
	}
	return snapshot, found, nil
}

func lockGORMToolExecutionPolicy(
	ctx context.Context,
	transaction *gorm.DB,
	request application.ToolExecutionPolicySnapshotRequest,
) (application.ToolExecutionPolicySnapshot, bool, error) {
	var snapshot application.ToolExecutionPolicySnapshot
	var definitionID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var runStatus, nodeStatus, attemptStatus string
	var pauseRequestedAt, cancelRequestedAt sql.NullTime
	var nodeLeaseOwner, attemptLeaseOwner sql.NullString
	var nodeLeaseUntil, attemptLeaseUntil sql.NullTime

	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		d.id::text,d.key,d.version,d.graph::text,
		r.workspace_id::text,r.id::text,r.status,r.pause_requested_at,r.cancel_requested_at,
		n.id::text,n.node_key,n.node_type,n.status,n.attempt,n.lease_owner,n.lease_until,
		a.id::text,a.status,a.attempt_no,a.lease_owner,a.lease_until,
		clock_timestamp()
		FROM workflow.run AS r
		JOIN workflow.definition AS d
		  ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
		JOIN workflow.node_run AS n ON n.run_id=r.id
		JOIN workflow.node_attempt AS a ON a.node_run_id=n.id
		WHERE r.workspace_id=?::uuid AND r.id=?::uuid
		  AND n.id=?::uuid AND a.id=?::uuid
		FOR SHARE OF r,d,n,a`,
		string(request.WorkspaceID), string(request.WorkflowRunID),
		string(request.NodeRunID), string(request.NodeAttemptID),
	)
	if err != nil {
		return snapshot, false, err
	}
	if err := row.Scan(
		&definitionID, &snapshot.DefinitionKey, &snapshot.DefinitionVersion, &snapshot.DefinitionGraph,
		&workspaceID, &workflowRunID, &runStatus, &pauseRequestedAt, &cancelRequestedAt,
		&nodeRunID, &snapshot.NodeKey, &snapshot.NodeKind, &nodeStatus, &snapshot.NodeAttempt,
		&nodeLeaseOwner, &nodeLeaseUntil,
		&nodeAttemptID, &attemptStatus, &snapshot.AttemptNo, &attemptLeaseOwner, &attemptLeaseUntil,
		&snapshot.DatabaseNow,
	); err != nil {
		if gormWorkflowNoRows(err) {
			return application.ToolExecutionPolicySnapshot{}, false, nil
		}
		return application.ToolExecutionPolicySnapshot{}, false, err
	}

	snapshot.DefinitionID = foundation.ID(definitionID)
	snapshot.WorkspaceID = foundation.ID(workspaceID)
	snapshot.WorkflowRunID = foundation.ID(workflowRunID)
	snapshot.WorkflowStatus = domain.RunStatus(runStatus)
	snapshot.PauseRequested = pauseRequestedAt.Valid
	snapshot.CancelRequested = cancelRequestedAt.Valid
	snapshot.NodeRunID = foundation.ID(nodeRunID)
	snapshot.NodeStatus = domain.NodeStatus(nodeStatus)
	snapshot.NodeLeaseOwner, snapshot.NodeLeaseOwnerSet = nodeLeaseOwner.String, nodeLeaseOwner.Valid
	snapshot.NodeLeaseUntil, snapshot.NodeLeaseUntilSet = nodeLeaseUntil.Time.UTC(), nodeLeaseUntil.Valid
	snapshot.NodeAttemptID = foundation.ID(nodeAttemptID)
	snapshot.AttemptStatus = domain.AttemptStatus(attemptStatus)
	snapshot.AttemptLeaseOwner, snapshot.AttemptLeaseOwnerSet = attemptLeaseOwner.String, attemptLeaseOwner.Valid
	snapshot.AttemptLeaseUntil, snapshot.AttemptLeaseUntilSet = attemptLeaseUntil.Time.UTC(), attemptLeaseUntil.Valid
	snapshot.DatabaseNow = snapshot.DatabaseNow.UTC()

	if snapshot.WorkspaceID != request.WorkspaceID || snapshot.WorkflowRunID != request.WorkflowRunID ||
		snapshot.NodeRunID != request.NodeRunID || snapshot.NodeAttemptID != request.NodeAttemptID ||
		!validGORMToolExecutionPolicySnapshot(snapshot) {
		return application.ToolExecutionPolicySnapshot{}, false, nil
	}
	return snapshot, true, nil
}

func validGORMToolExecutionPolicyRequest(request application.ToolExecutionPolicySnapshotRequest) bool {
	return validGORMWorkflowID(request.WorkspaceID) && validGORMWorkflowID(request.WorkflowRunID) &&
		validGORMWorkflowID(request.NodeRunID) && validGORMWorkflowID(request.NodeAttemptID)
}

func validGORMToolExecutionPolicySnapshot(snapshot application.ToolExecutionPolicySnapshot) bool {
	fenceSnapshot := application.WorkspaceAnalysisExecutionFenceSnapshot{
		DefinitionID: snapshot.DefinitionID, DefinitionKey: snapshot.DefinitionKey,
		DefinitionVersion: snapshot.DefinitionVersion, DefinitionGraph: snapshot.DefinitionGraph,
		WorkflowStatus: snapshot.WorkflowStatus,
		NodeKey:        snapshot.NodeKey, NodeStatus: snapshot.NodeStatus, NodeAttempt: snapshot.NodeAttempt,
		NodeLeaseOwnerSet: snapshot.NodeLeaseOwnerSet, NodeLeaseUntilSet: snapshot.NodeLeaseUntilSet,
		AttemptStatus: snapshot.AttemptStatus, AttemptNo: snapshot.AttemptNo,
		AttemptLeaseOwnerSet: snapshot.AttemptLeaseOwnerSet, AttemptLeaseUntilSet: snapshot.AttemptLeaseUntilSet,
	}
	return strings.TrimSpace(snapshot.NodeKind) != "" && !snapshot.DatabaseNow.IsZero() &&
		validGORMWorkflowFenceRun(fenceSnapshot) && validGORMWorkflowFenceNode(fenceSnapshot) &&
		validGORMWorkflowFenceAttempt(fenceSnapshot)
}

var _ application.ScopedToolExecutionPolicySnapshot = (*GORMToolExecutionPolicySnapshot)(nil)
