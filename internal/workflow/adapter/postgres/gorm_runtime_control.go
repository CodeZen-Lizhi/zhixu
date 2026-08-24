package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// Control persists pause, resume, and cancel commands with an exact durable
// receipt. Every external hook and River insertion shares this transaction.
func (repository *GORMRuntimeRepository) Control(ctx context.Context, command application.ControlTransition) (application.ControlPersistenceResult, error) {
	var result application.ControlPersistenceResult
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		result, err = repository.controlWorkflow(callbackCtx, tx, scope, command)
		if err == nil {
			callbackSucceeded = true
		}
		return err
	})
	if err != nil {
		if callbackSucceeded {
			return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_COMMIT_FAILED")
		}
		return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_TRANSACTION_FAILED")
	}
	return result, nil
}

func (repository *GORMRuntimeRepository) controlWorkflow(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	command application.ControlTransition,
) (application.ControlPersistenceResult, error) {
	run, err := gormHumanLockRun(ctx, tx, command.WorkflowRunID)
	if gormWorkflowNoRows(err) {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUN_NOT_FOUND", false, err)
	}
	if err != nil {
		return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if err := gormHumanAuthorizeRun(ctx, tx, run, command.CallerCapabilities); err != nil {
		return application.ControlPersistenceResult{}, err
	}
	result, found, err := gormControlReplay(ctx, tx, run, command)
	if err != nil || found {
		return result, err
	}
	if run.Version != command.ExpectedVersion {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_VERSION_CONFLICT", false, errors.New("workflow version differs"))
	}
	if domain.IsTerminalRunStatus(run.Status) {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("terminal workflow cannot be controlled"))
	}
	nodes, err := gormDeliveryLockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.ControlPersistenceResult{}, err
	}
	now, err := gormDeliveryDatabaseNow(ctx, tx)
	if err != nil {
		return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	status := run.Status
	switch command.Action {
	case application.ControlActionPause:
		status, err = repository.gormControlPause(ctx, tx, run, nodes, now)
	case application.ControlActionResume:
		status, err = repository.gormControlResume(ctx, tx, scope, &run, nodes, now)
	case application.ControlActionCancel:
		status, err = repository.gormControlCancel(ctx, tx, scope, run, nodes, now)
	default:
		err = foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CONTROL_INVALID", false, errors.New("unknown workflow control action"))
	}
	if err != nil {
		return application.ControlPersistenceResult{}, err
	}
	result, err = gormControlCurrentResult(ctx, tx, run.ID, status)
	if err != nil {
		return application.ControlPersistenceResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKFLOW_CONTROL_RESULT_ENCODING_FAILED", false, err)
	}
	commandID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return application.ControlPersistenceResult{}, err
	}
	statement := tx.Exec(`INSERT INTO workflow.control_command(
		id,run_id,command,idempotency_key,request_hash,expected_version,result,created_at,completed_at
	) VALUES(?::uuid,?::uuid,?,?,?,?,?::jsonb,?,?)`,
		string(commandID), string(run.ID), string(command.Action), command.IdempotencyKey,
		command.RequestHash, command.ExpectedVersion, workflowJSONB(encoded), now, now)
	if statement.Error != nil {
		return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_CONTROL_CREATE_FAILED")
	}
	if repository.control != nil {
		event := application.WorkflowControlEvent{
			WorkspaceID: run.WorkspaceID, WorkflowRunID: run.ID, Action: command.Action,
			IdempotencyKey: command.IdempotencyKey, ExpectedVersion: command.ExpectedVersion,
			PersistedControl: result, OccurredAt: now,
		}
		if err := repository.control.OnWorkflowControlScoped(ctx, scope, event); err != nil {
			return application.ControlPersistenceResult{}, err
		}
	}
	return result, nil
}

func gormControlReplay(
	ctx context.Context,
	tx *gorm.DB,
	run domain.Run,
	command application.ControlTransition,
) (application.ControlPersistenceResult, bool, error) {
	row, err := gormWorkflowRawRow(tx, `SELECT request_hash,result::text,expected_version
		FROM workflow.control_command
		WHERE run_id=?::uuid AND command=? AND idempotency_key=? FOR UPDATE`,
		string(command.WorkflowRunID), string(command.Action), command.IdempotencyKey)
	if err != nil {
		return application.ControlPersistenceResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_QUERY_FAILED")
	}
	var storedHash, resultText string
	var storedExpected int64
	if err := row.Scan(&storedHash, &resultText, &storedExpected); gormWorkflowNoRows(err) {
		return application.ControlPersistenceResult{}, false, nil
	} else if err != nil {
		return application.ControlPersistenceResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_QUERY_FAILED")
	}
	if storedHash != command.RequestHash || storedExpected != command.ExpectedVersion {
		return application.ControlPersistenceResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("control idempotency binding differs"))
	}
	var persisted application.ControlPersistenceResult
	if err := json.Unmarshal([]byte(resultText), &persisted); err != nil {
		return application.ControlPersistenceResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_CONTROL_RESULT_INVALID", false, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resultText), &fields); err != nil {
		return application.ControlPersistenceResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_CONTROL_RESULT_INVALID", false, err)
	}
	_, pauseCamel := fields["PauseRequested"]
	_, pauseSnake := fields["pause_requested"]
	if !pauseCamel && !pauseSnake {
		persisted.PauseRequested = run.PauseRequestedAt != nil
	}
	_, cancelCamel := fields["CancelRequested"]
	_, cancelSnake := fields["cancel_requested"]
	if !cancelCamel && !cancelSnake {
		persisted.CancelRequested = run.CancelRequestedAt != nil
	}
	return persisted, true, nil
}

func (repository *GORMRuntimeRepository) gormControlPause(
	ctx context.Context,
	tx *gorm.DB,
	run domain.Run,
	nodes map[foundation.ID]domain.NodeRun,
	now time.Time,
) (domain.RunStatus, error) {
	if run.CancelRequestedAt != nil || run.PauseRequestedAt != nil {
		return "", foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow control request already exists"))
	}
	statement := tx.Exec(`UPDATE workflow.run SET pause_requested_at=?,version=version+1,updated_at=? WHERE id=?::uuid`, now, now, string(run.ID))
	if statement.Error != nil {
		return "", classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_CONTROL_UPDATE_FAILED")
	}
	status := run.Status
	if gormDeliveryHasRunningNode(nodes) {
		return status, nil
	}
	for id, node := range nodes {
		if node.Status != domain.NodeStatusPending && node.Status != domain.NodeStatusRetryWait {
			continue
		}
		row, err := gormWorkflowRawRow(tx, `UPDATE workflow.node_run
			SET status='paused',next_attempt_at=NULL,updated_at=?,version=version+1
			WHERE id=?::uuid RETURNING `+runtimeNodeColumns, now, string(node.ID))
		if err != nil {
			return "", classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_PAUSE_FAILED")
		}
		updated, err := scanRuntimeNode(row)
		if err != nil {
			return "", classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_PAUSE_FAILED")
		}
		nodes[id] = updated
	}
	statement = tx.Exec(`UPDATE workflow.run SET status='paused',version=version+1,updated_at=? WHERE id=?::uuid`, now, string(run.ID))
	if statement.Error != nil {
		return "", classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_CONTROL_UPDATE_FAILED")
	}
	return domain.RunStatusPaused, nil
}

func (repository *GORMRuntimeRepository) gormControlResume(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	run *domain.Run,
	nodes map[foundation.ID]domain.NodeRun,
	now time.Time,
) (domain.RunStatus, error) {
	if run.Status != domain.RunStatusPaused || run.PauseRequestedAt == nil {
		return "", foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow is not paused"))
	}
	if err := repository.gormControlResumeNodes(ctx, tx, scope, nodes, now); err != nil {
		return "", err
	}
	statement := tx.Exec(`UPDATE workflow.run
		SET pause_requested_at=NULL,status='running',version=version+1,updated_at=? WHERE id=?::uuid`, now, string(run.ID))
	if statement.Error != nil {
		return "", classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_CONTROL_UPDATE_FAILED")
	}
	run.PauseRequestedAt = nil
	run.Status = domain.RunStatusRunning
	for _, node := range nodes {
		if node.Status != domain.NodeStatusSucceeded {
			continue
		}
		if err := repository.gormDeliveryActivateSuccessors(ctx, tx, scope, *run, nodes, node, now); err != nil {
			return "", err
		}
	}
	status, err := gormDeliveryReduceRunStatus(*run, nodes)
	if err != nil {
		return "", err
	}
	if status != domain.RunStatusRunning {
		updated, err := gormDeliveryPersistRunReduction(ctx, tx, *run, status, now)
		if err != nil {
			return "", err
		}
		*run = updated
	}
	return status, nil
}

func (repository *GORMRuntimeRepository) gormControlCancel(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	run domain.Run,
	nodes map[foundation.ID]domain.NodeRun,
	now time.Time,
) (domain.RunStatus, error) {
	if run.CancelRequestedAt != nil {
		return "", foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow cancellation already requested"))
	}
	statement := tx.Exec(`UPDATE workflow.run SET cancel_requested_at=?,version=version+1,updated_at=? WHERE id=?::uuid`, now, now, string(run.ID))
	if statement.Error != nil {
		return "", classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_CONTROL_UPDATE_FAILED")
	}
	deferred := false
	cancelled := make([]foundation.ID, 0, len(nodes))
	ids := make([]foundation.ID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	for _, id := range ids {
		node := nodes[id]
		safe, err := repository.gormControlSafeToCancel(ctx, scope, node.ID)
		if err != nil {
			return "", err
		}
		if !safe {
			deferred = true
			continue
		}
		if node.Status == domain.NodeStatusRunning || domain.IsTerminalNodeStatus(node.Status) {
			continue
		}
		row, err := gormWorkflowRawRow(tx, `UPDATE workflow.node_run
			SET status='cancelled',completed_at=?,updated_at=?,version=version+1,
				lease_owner=NULL,lease_until=NULL,next_attempt_at=NULL
			WHERE id=?::uuid RETURNING `+runtimeNodeColumns, now, now, string(node.ID))
		if err != nil {
			return "", classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_CANCEL_FAILED")
		}
		updated, err := scanRuntimeNode(row)
		if err != nil {
			return "", classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_CANCEL_FAILED")
		}
		statement := tx.Exec(`UPDATE workflow.human_task SET status='cancelled'
			WHERE node_run_id=?::uuid AND status='pending'`, string(node.ID))
		if statement.Error != nil {
			return "", classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_HUMAN_TASK_CANCEL_FAILED")
		}
		nodes[id] = updated
		cancelled = append(cancelled, updated.ID)
	}
	status := run.Status
	if !gormDeliveryHasRunningNode(nodes) && !deferred {
		status = domain.RunStatusCancelled
		statement := tx.Exec(`UPDATE workflow.run
			SET status='cancelled',completed_at=?,version=version+1,updated_at=? WHERE id=?::uuid`, now, now, string(run.ID))
		if statement.Error != nil {
			return "", classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_CONTROL_UPDATE_FAILED")
		}
	}
	for _, nodeID := range cancelled {
		if err := repository.gormControlNotifyCancelled(ctx, tx, scope, run, nodes[nodeID], now); err != nil {
			return "", err
		}
	}
	return status, nil
}

func (repository *GORMRuntimeRepository) gormControlResumeNodes(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	nodes map[foundation.ID]domain.NodeRun,
	now time.Time,
) error {
	ids := make([]foundation.ID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	for _, id := range ids {
		node := nodes[id]
		if node.Status != domain.NodeStatusPaused && node.Status != domain.NodeStatusPending && node.Status != domain.NodeStatusRetryWait {
			continue
		}
		dispatch := node.DispatchNo + 1
		row, err := gormWorkflowRawRow(tx, `UPDATE workflow.node_run
			SET status='pending',dispatch_no=?,next_attempt_at=NULL,updated_at=?,version=version+1
			WHERE id=?::uuid RETURNING `+runtimeNodeColumns, dispatch, now, string(node.ID))
		if err != nil {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_RESUME_FAILED")
		}
		updated, err := scanRuntimeNode(row)
		if err != nil {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_RESUME_FAILED")
		}
		nodes[id] = updated
		args, err := riveradapter.NewNodeJobArgs(updated.ID, updated.DispatchNo)
		if err != nil {
			return err
		}
		if _, err := repository.jobs.InsertTx(ctx, scope, args, riveradapter.InsertOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (repository *GORMRuntimeRepository) gormControlSafeToCancel(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	if repository.cancellation == nil {
		return true, nil
	}
	safe, err := repository.cancellation.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeRunID)
	if err == nil {
		return safe, nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Kind == foundation.ErrorVersionConflict {
		return false, err
	}
	return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE", true, err)
}

func (repository *GORMRuntimeRepository) gormControlNotifyCancelled(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	run domain.Run,
	node domain.NodeRun,
	terminalAt time.Time,
) error {
	if repository.terminal == nil {
		return nil
	}
	attempt, found, err := gormHumanFindLatestAttempt(ctx, tx, node.ID)
	if err != nil {
		return err
	}
	attemptID := foundation.ID("")
	if found {
		attemptID = attempt.ID
	}
	return repository.gormDeliveryNotifyWorkflowNodeTerminal(ctx, scope, application.WorkflowNodeTerminalEvent{
		WorkspaceID: run.WorkspaceID, WorkflowRunID: run.ID, NodeRunID: node.ID,
		NodeKind: node.NodeType, NodeAttemptID: attemptID,
		Outcome: application.WorkflowTerminalOutcomeCancelled, FailureClass: domain.FailureClassCancelled,
		FailureCode: "WORKFLOW_CANCELLED", FailureSummary: "WORKFLOW_CANCELLED", TerminalAt: terminalAt,
	})
}

func gormControlCurrentResult(ctx context.Context, tx *gorm.DB, runID foundation.ID, status domain.RunStatus) (application.ControlPersistenceResult, error) {
	row, err := gormWorkflowRawRow(tx, `SELECT version,pause_requested_at,cancel_requested_at FROM workflow.run WHERE id=?::uuid`, string(runID))
	if err != nil {
		return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_QUERY_FAILED")
	}
	var version int64
	var pauseRequestedAt, cancelRequestedAt *time.Time
	if err := row.Scan(&version, &pauseRequestedAt, &cancelRequestedAt); err != nil {
		return application.ControlPersistenceResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_QUERY_FAILED")
	}
	return application.ControlPersistenceResult{
		WorkflowRunID: runID, Status: status, Version: version,
		PauseRequested: pauseRequestedAt != nil, CancelRequested: cancelRequestedAt != nil,
	}, nil
}
