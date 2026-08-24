package workflowpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

const gormClaimRuntimeRunColumns = runtimeRunColumns

const gormClaimRuntimeNodeColumns = runtimeNodeColumns

const gormClaimRuntimeAttemptColumns = runtimeAttemptColumns

// Claim acquires or reclaims a DB-time lease in one platform-owned transaction.
func (repository *GORMRuntimeRepository) Claim(ctx context.Context, command application.ClaimCommand) (application.ClaimResult, error) {
	var result application.ClaimResult
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		claimed, claimErr := repository.gormClaimWithin(callbackCtx, transaction, command)
		if claimErr != nil {
			return claimErr
		}
		result = claimed
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "WORKFLOW_CLAIM_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = "WORKFLOW_CLAIM_COMMIT_FAILED"
		}
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, code)
	}
	return result, nil
}

func (repository *GORMRuntimeRepository) gormClaimWithin(ctx context.Context, transaction *gorm.DB, command application.ClaimCommand) (application.ClaimResult, error) {
	var runID string
	row, err := gormWorkflowRawRow(transaction, `SELECT run_id::text FROM workflow.node_run WHERE id=?`, string(command.NodeRunID))
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	if err := row.Scan(&runID); err != nil {
		if gormWorkflowNoRows(err) {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, err)
		}
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}

	run, err := gormClaimScanRuntimeRun(transaction, `SELECT `+gormClaimRuntimeRunColumns+` FROM workflow.run WHERE id=? FOR UPDATE`, runID)
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	definition, err := gormClaimScanDefinition(transaction, `SELECT id::text,workspace_id::text,key,version,graph,created_at FROM workflow.definition WHERE id=?`, string(run.DefinitionID))
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			return application.ClaimResult{}, err
		}
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	if definition.ID != run.DefinitionID || definition.WorkspaceID != run.WorkspaceID {
		return application.ClaimResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_BINDING_INVALID", false, errors.New("workflow run definition binding differs"))
	}

	node, err := gormClaimScanRuntimeNode(transaction, `SELECT `+gormClaimRuntimeNodeColumns+` FROM workflow.node_run WHERE id=? FOR UPDATE`, string(command.NodeRunID))
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	if node.DispatchNo < 1 || node.IdempotencyKey == "" {
		return application.ClaimResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED", false, errors.New("active workflow node lacks runtime identity"))
	}

	existing, found, err := gormClaimFindAttemptByDelivery(ctx, transaction, command.NodeRunID, command.DispatchNo, command.DeliveryID)
	if err != nil {
		return application.ClaimResult{}, err
	}
	now, err := gormClaimDatabaseNow(ctx, transaction)
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	if found {
		if !sameModelRuntimeOwner(existing.ModelSettingsRevision, existing.ModelRuntimeInstanceID, command.ModelRuntimeInstanceID) {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_MODEL_RUNTIME_BINDING_MISMATCH", false, errors.New("workflow delivery model runtime binding differs"))
		}
		if existing.Status == domain.AttemptStatusRunning && existing.LeaseOwner == command.LeaseOwner && node.Status == domain.NodeStatusRunning && existing.LeaseUntil.After(now) && node.LeaseUntil != nil && node.LeaseUntil.After(now) {
			return application.ClaimResult{Disposition: application.ClaimDispositionClaimed, Definition: definition, Run: run, Node: node, Attempt: existing, ObservedNodeKind: node.NodeType}, nil
		}
		if existing.Status != domain.AttemptStatusRunning || existing.LeaseUntil.After(now) {
			return application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true}, nil
		}
	}

	if domain.IsTerminalRunStatus(run.Status) || run.PauseRequestedAt != nil {
		return application.ClaimResult{Disposition: application.ClaimDispositionStale}, nil
	}
	if node.DispatchNo != command.DispatchNo {
		return application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true}, nil
	}
	if node.Status == domain.NodeStatusRetryWait && (node.NextAttemptAt == nil || node.NextAttemptAt.After(now)) {
		return application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true}, nil
	}
	if node.Status != domain.NodeStatusPending && node.Status != domain.NodeStatusRetryWait && node.Status != domain.NodeStatusRunning {
		return application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true}, nil
	}
	if node.Status == domain.NodeStatusRunning && node.LeaseUntil != nil && node.LeaseUntil.After(now) {
		var activeRiverJobID sql.NullInt64
		var activeRiverJobAttempt sql.NullInt64
		row, queryErr := gormWorkflowRawRow(transaction, `SELECT river_job_id,river_job_attempt FROM workflow.node_attempt WHERE node_run_id=? AND attempt_no=? AND status='running'`, string(node.ID), node.Attempt)
		if queryErr != nil {
			return application.ClaimResult{}, classifyGORMWorkflow(ctx, queryErr, "WORKFLOW_ACTIVE_ATTEMPT_QUERY_FAILED")
		}
		if queryErr := row.Scan(&activeRiverJobID, &activeRiverJobAttempt); queryErr != nil {
			return application.ClaimResult{}, classifyGORMWorkflow(ctx, queryErr, "WORKFLOW_ACTIVE_ATTEMPT_QUERY_FAILED")
		}
		if activeRiverJobID.Valid && activeRiverJobAttempt.Valid && activeRiverJobID.Int64 == command.RiverJobID && command.RiverJobAttempt > int(activeRiverJobAttempt.Int64) {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_LEASE_HELD", true, errors.New("previous delivery lease is still active"))
		}
		return application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true}, nil
	}

	modelSettingsRevision, modelRuntimeInstanceID, err := repository.gormClaimSelectModelRuntimeBinding(ctx, transaction, command.ModelRuntimeInstanceID)
	if err != nil {
		return application.ClaimResult{}, err
	}
	leaseReclaimed := false
	if node.Status == domain.NodeStatusRunning {
		if node.Attempt < 1 {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_ACTIVE_ATTEMPT_MISSING", false, errors.New("expired running node has no attempt projection"))
		}
		statement := transaction.Exec(`UPDATE workflow.node_attempt
			SET status='lease_lost',failure_class='lease_lost',error_kind=?,error_code='WORKFLOW_LEASE_LOST',error_summary='lease expired',lease_owner=NULL,lease_until=NULL,ended_at=?,heartbeat_at=?
			WHERE node_run_id=? AND attempt_no=? AND status='running'`,
			string(foundation.ErrorVersionConflict), now, now, string(node.ID), node.Attempt)
		if statement.Error != nil {
			return application.ClaimResult{}, classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_ATTEMPT_RECLAIM_FAILED")
		}
		leaseReclaimed = true
	}

	attemptNo := node.Attempt + 1
	var maxAttempt int
	row, err = gormWorkflowRawRow(transaction, `SELECT COALESCE(max(attempt_no),0) FROM workflow.node_attempt WHERE node_run_id=?`, string(node.ID))
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	if err := row.Scan(&maxAttempt); err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	if maxAttempt >= attemptNo {
		attemptNo = maxAttempt + 1
	}

	attemptID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return application.ClaimResult{}, err
	}
	leaseUntil := now.Add(command.LeaseDuration)
	statement := transaction.Exec(`INSERT INTO workflow.node_attempt
		(id,node_run_id,attempt_no,dispatch_no,retry_no,river_job_id,river_job_attempt,delivery_id,lease_owner,lease_until,status,model_settings_revision,model_runtime_instance_id,started_at,heartbeat_at)
		VALUES(?,?,?,?,?,?,NULLIF(?,0),?,?,?,'running',?,?::uuid,?,?)`,
		string(attemptID), string(node.ID), attemptNo, node.DispatchNo, node.RetryNo,
		command.RiverJobID, command.RiverJobAttempt, command.DeliveryID, command.LeaseOwner,
		leaseUntil, nullableInt64(modelSettingsRevision), nullableFoundationID(modelRuntimeInstanceID), now, now)
	if statement.Error != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_ATTEMPT_CREATE_FAILED")
	}

	node, err = gormClaimScanRuntimeNode(transaction, `UPDATE workflow.node_run
		SET status='running',attempt=?,lease_owner=?,lease_until=?,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,updated_at=?,version=version+1
		WHERE id=?
		RETURNING `+gormClaimRuntimeNodeColumns,
		attemptNo, command.LeaseOwner, leaseUntil, now, string(node.ID))
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_CLAIM_FAILED")
	}
	statement = transaction.Exec(`UPDATE workflow.run
		SET status='running',version=version+1,updated_at=?
		WHERE id=? AND status IN ('pending','retry_wait','waiting_for_human','running') AND pause_requested_at IS NULL AND cancel_requested_at IS NULL`, now, string(run.ID))
	if statement.Error != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_RUN_START_FAILED")
	}
	run, err = gormClaimScanRuntimeRun(transaction, `SELECT `+gormClaimRuntimeRunColumns+` FROM workflow.run WHERE id=?`, string(run.ID))
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	attempt, err := gormClaimScanRuntimeAttempt(transaction, `SELECT `+gormClaimRuntimeAttemptColumns+` FROM workflow.node_attempt WHERE id=?`, string(attemptID))
	if err != nil {
		return application.ClaimResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return application.ClaimResult{
		Disposition: application.ClaimDispositionClaimed, Definition: definition, Run: run, Node: node, Attempt: attempt,
		ObservedNodeKind: node.NodeType, DuplicateDelivery: leaseReclaimed, LeaseReclaimed: leaseReclaimed,
	}, nil
}

// Heartbeat extends an unexpired lease using the complete persisted fence.
func (repository *GORMRuntimeRepository) Heartbeat(ctx context.Context, command application.HeartbeatCommand) (application.HeartbeatResult, error) {
	var result application.HeartbeatResult
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		heartbeat, heartbeatErr := repository.gormClaimHeartbeatWithin(callbackCtx, transaction, scope, command)
		if heartbeatErr != nil {
			return heartbeatErr
		}
		result = heartbeat
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "WORKFLOW_HEARTBEAT_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = "WORKFLOW_HEARTBEAT_COMMIT_FAILED"
		}
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, code)
	}
	return result, nil
}

func (repository *GORMRuntimeRepository) gormClaimHeartbeatWithin(
	ctx context.Context,
	transaction *gorm.DB,
	scope foundation.TransactionScope,
	command application.HeartbeatCommand,
) (application.HeartbeatResult, error) {
	var runID string
	row, err := gormWorkflowRawRow(transaction, `SELECT run_id::text FROM workflow.node_run WHERE id=?`, string(command.NodeRunID))
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	if err := row.Scan(&runID); err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}

	var pauseRequestedAt, cancelRequestedAt sql.NullTime
	row, err = gormWorkflowRawRow(transaction, `SELECT pause_requested_at,cancel_requested_at FROM workflow.run WHERE id=? FOR UPDATE`, runID)
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if err := row.Scan(&pauseRequestedAt, &cancelRequestedAt); err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if cancelRequestedAt.Valid {
		safe, safetyErr := repository.gormClaimSafeToCancelWorkflowNode(ctx, scope, command.NodeRunID)
		if safetyErr != nil {
			return application.HeartbeatResult{}, safetyErr
		}
		if safe {
			return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CANCEL_REQUESTED", false, errors.New("workflow cancellation checkpoint requested"))
		}
	}
	if pauseRequestedAt.Valid {
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_PAUSE_REQUESTED", false, errors.New("workflow pause checkpoint requested"))
	}

	var locked int
	row, err = gormWorkflowRawRow(transaction, `SELECT 1 FROM workflow.node_run WHERE id=? FOR UPDATE`, string(command.NodeRunID))
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	if err := row.Scan(&locked); gormWorkflowNoRows(err) {
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	} else if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	row, err = gormWorkflowRawRow(transaction, `SELECT 1 FROM workflow.node_attempt WHERE node_run_id=? AND attempt_no=? FOR UPDATE`, string(command.NodeRunID), command.Fence.AttemptNo)
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	if err := row.Scan(&locked); gormWorkflowNoRows(err) {
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	} else if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}

	now, err := gormClaimDatabaseNow(ctx, transaction)
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	leaseUntil := now.Add(command.LeaseDuration)
	node, err := gormClaimScanRuntimeNode(transaction, `UPDATE workflow.node_run
		SET lease_until=?,updated_at=?,version=version+1
		WHERE id=? AND status='running' AND lease_owner=? AND version=? AND lease_until>?
		RETURNING `+gormClaimRuntimeNodeColumns,
		leaseUntil, now, string(command.NodeRunID), command.Fence.Owner, command.Fence.NodeVersion, now)
	if gormWorkflowNoRows(err) {
		var status domain.NodeStatus
		var owner sql.NullString
		var currentLeaseUntil sql.NullTime
		row, queryErr := gormWorkflowRawRow(transaction, `SELECT status,lease_owner,lease_until FROM workflow.node_run WHERE id=?`, string(command.NodeRunID))
		if queryErr == nil {
			queryErr = row.Scan(&status, &owner, &currentLeaseUntil)
		}
		if queryErr == nil && status == domain.NodeStatusRunning && owner.Valid && owner.String == command.Fence.Owner && currentLeaseUntil.Valid && !currentLeaseUntil.Time.After(now) {
			return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_LEASE_EXPIRED", true, err)
		}
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	}
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_HEARTBEAT_FAILED")
	}
	attempt, err := gormClaimScanRuntimeAttempt(transaction, `UPDATE workflow.node_attempt
		SET lease_until=?,heartbeat_at=?
		WHERE node_run_id=? AND attempt_no=? AND lease_owner=? AND status='running' AND lease_until>?
		RETURNING `+gormClaimRuntimeAttemptColumns,
		leaseUntil, now, string(command.NodeRunID), command.Fence.AttemptNo, command.Fence.Owner, now)
	if gormWorkflowNoRows(err) {
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	}
	if err != nil {
		return application.HeartbeatResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_HEARTBEAT_FAILED")
	}
	return application.HeartbeatResult{Node: node, Attempt: attempt}, nil
}

func (repository *GORMRuntimeRepository) gormClaimSelectModelRuntimeBinding(
	ctx context.Context,
	transaction *gorm.DB,
	owner *foundation.ID,
) (*int64, *foundation.ID, error) {
	if owner == nil {
		return nil, nil, nil
	}

	var statePhase string
	var activeRevision int64
	var previousActiveRevision sql.NullInt64
	row, err := gormWorkflowRawRow(transaction, `SELECT phase,active_revision,previous_active_revision
		FROM ops.model_settings_state WHERE singleton=true FOR SHARE`)
	if err != nil {
		return nil, nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_MODEL_RUNTIME_STATE_QUERY_FAILED")
	}
	if err := row.Scan(&statePhase, &activeRevision, &previousActiveRevision); err != nil {
		if gormWorkflowNoRows(err) {
			return nil, nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_MODEL_RUNTIME_STATE_INVALID", false, errors.New("model settings singleton state is missing"))
		}
		return nil, nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_MODEL_RUNTIME_STATE_QUERY_FAILED")
	}
	selectedRevision := activeRevision
	switch statePhase {
	case "idle", "failed":
	case "preparing":
		if !previousActiveRevision.Valid || previousActiveRevision.Int64 != activeRevision {
			return nil, nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_MODEL_RUNTIME_STATE_INVALID", false, errors.New("preparing model settings state does not preserve active revision"))
		}
		selectedRevision = previousActiveRevision.Int64
	case "arming", "activating":
		return nil, nil, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_MODEL_RUNTIME_CLAIM_BLOCKED", true, errors.New("model settings activation blocks new workflow claims"))
	default:
		return nil, nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_MODEL_RUNTIME_STATE_INVALID", false, errors.New("model settings activation phase is invalid"))
	}
	if selectedRevision < 0 {
		return nil, nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_MODEL_RUNTIME_STATE_INVALID", false, errors.New("model settings active revision is invalid"))
	}

	var instanceID string
	var appliedRevision int64
	var rolloutID sql.NullString
	var runtimePhase string
	var heartbeatAt time.Time
	row, err = gormWorkflowRawRow(transaction, `SELECT instance_id::text,applied_revision,rollout_id::text,phase,heartbeat_at
		FROM ops.model_settings_runtime WHERE role='worker' FOR SHARE`)
	if err != nil {
		return nil, nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_MODEL_RUNTIME_QUERY_FAILED")
	}
	if err := row.Scan(&instanceID, &appliedRevision, &rolloutID, &runtimePhase, &heartbeatAt); err != nil {
		if gormWorkflowNoRows(err) {
			return nil, nil, modelRuntimeOwnershipLost()
		}
		return nil, nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_MODEL_RUNTIME_QUERY_FAILED")
	}
	parsedInstanceID, err := foundation.ParseID(instanceID)
	if err != nil {
		return nil, nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_MODEL_RUNTIME_STATE_INVALID", false, errors.New("worker model runtime instance is invalid"))
	}
	freshnessNow, err := gormClaimDatabaseNow(ctx, transaction)
	if err != nil {
		return nil, nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	if parsedInstanceID != *owner || appliedRevision != selectedRevision || rolloutID.Valid || runtimePhase != "active" || heartbeatAt.Before(freshnessNow.Add(-repository.modelRuntimeFreshWithin)) {
		return nil, nil, modelRuntimeOwnershipLost()
	}
	return &selectedRevision, &parsedInstanceID, nil
}

func (repository *GORMRuntimeRepository) gormClaimSafeToCancelWorkflowNode(
	ctx context.Context,
	scope foundation.TransactionScope,
	nodeRunID foundation.ID,
) (bool, error) {
	if repository == nil || repository.cancellation == nil {
		return true, nil
	}
	if nilGORMWorkflowDependency(repository.cancellation) {
		return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE", true, errors.New("workflow scoped cancellation safety guard is unavailable"))
	}
	safe, err := repository.cancellation.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeRunID)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Kind == foundation.ErrorVersionConflict {
			return false, err
		}
		return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE", true, err)
	}
	return safe, nil
}

func gormClaimDatabaseNow(ctx context.Context, transaction *gorm.DB) (time.Time, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, err
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

func gormClaimFindAttemptByDelivery(
	ctx context.Context,
	transaction *gorm.DB,
	nodeID foundation.ID,
	dispatchNo int,
	deliveryID string,
) (domain.NodeAttempt, bool, error) {
	attempt, err := gormClaimScanRuntimeAttempt(transaction, `SELECT `+gormClaimRuntimeAttemptColumns+`
		FROM workflow.node_attempt
		WHERE node_run_id=? AND dispatch_no=? AND delivery_id=?
		ORDER BY attempt_no DESC
		LIMIT 1
		FOR UPDATE`, string(nodeID), dispatchNo, deliveryID)
	if gormWorkflowNoRows(err) {
		return domain.NodeAttempt{}, false, nil
	}
	if err != nil {
		return domain.NodeAttempt{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return attempt, true, nil
}

func gormClaimScanDefinition(transaction *gorm.DB, query string, arguments ...any) (domain.Definition, error) {
	row, err := gormWorkflowRawRow(transaction, query, arguments...)
	if err != nil {
		return domain.Definition{}, err
	}
	return scanClaimDefinition(row)
}

func gormClaimScanRuntimeRun(transaction *gorm.DB, query string, arguments ...any) (domain.Run, error) {
	row, err := gormWorkflowRawRow(transaction, query, arguments...)
	if err != nil {
		return domain.Run{}, err
	}
	return scanRuntimeRun(row)
}

func gormClaimScanRuntimeNode(transaction *gorm.DB, query string, arguments ...any) (domain.NodeRun, error) {
	row, err := gormWorkflowRawRow(transaction, query, arguments...)
	if err != nil {
		return domain.NodeRun{}, err
	}
	return scanRuntimeNode(row)
}

func gormClaimScanRuntimeAttempt(transaction *gorm.DB, query string, arguments ...any) (domain.NodeAttempt, error) {
	row, err := gormWorkflowRawRow(transaction, query, arguments...)
	if err != nil {
		return domain.NodeAttempt{}, err
	}
	return scanRuntimeAttempt(row)
}

var _ application.RuntimeStatePort = (*GORMRuntimeRepository)(nil)
