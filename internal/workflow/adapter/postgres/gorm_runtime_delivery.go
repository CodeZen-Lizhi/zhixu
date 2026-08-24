package workflowpostgres

import (
	"context"
	"crypto/sha256"
	encodinghex "encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

type gormDeliveryReduction struct {
	result     application.DeliveryTransitionResult
	commitCode string
}

// TransitionDelivery atomically reduces one claimed delivery and creates any
// successor generation, Outbox event and River job in the same scoped UoW.
func (repository *GORMRuntimeRepository) TransitionDelivery(ctx context.Context, command application.DeliveryTransition) (application.DeliveryTransitionResult, error) {
	var reduction gormDeliveryReduction
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(transactionCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		transitioned, transitionErr := repository.gormDeliveryTransition(transactionCtx, transaction, scope, command)
		if transitionErr != nil {
			return transitionErr
		}
		reduction = transitioned
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "WORKFLOW_DELIVERY_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = reduction.commitCode
			if code == "" {
				code = "WORKFLOW_DELIVERY_COMMIT_FAILED"
			}
		}
		return application.DeliveryTransitionResult{}, classifyGORMWorkflow(ctx, err, code)
	}
	return reduction.result, nil
}

func (repository *GORMRuntimeRepository) gormDeliveryTransition(ctx context.Context, transaction *gorm.DB, scope foundation.TransactionScope, command application.DeliveryTransition) (gormDeliveryReduction, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT run_id::text FROM workflow.node_run WHERE id=?`, string(command.Binding.NodeRunID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	var runID string
	if err := row.Scan(&runID); err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}

	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=? FOR UPDATE`, runID)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	run, err := scanRuntimeRun(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	nodes, err := gormDeliveryLockRuntimeNodes(ctx, transaction, run.ID)
	if err != nil {
		return gormDeliveryReduction{}, err
	}
	node, ok := nodes[command.Binding.NodeRunID]
	if !ok {
		return gormDeliveryReduction{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, errors.New("workflow node not found"))
	}
	attempt, found, err := gormDeliveryFindAttemptByDelivery(ctx, transaction, command.Binding.NodeRunID, command.Binding.DispatchNo, command.Binding.DeliveryID)
	if err != nil {
		return gormDeliveryReduction{}, err
	}
	if !found {
		return gormDeliveryReduction{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_ATTEMPT_NOT_FOUND", false, errors.New("workflow delivery attempt not found"))
	}
	if attempt.Status != domain.AttemptStatusRunning {
		if attempt.Status == domain.AttemptStatusCancelled && (attempt.ErrorCode == "WORKFLOW_PAUSED" || attempt.ErrorCode == "WORKFLOW_CANCELLED") {
			return gormDeliveryReduction{
				result:     application.DeliveryTransitionResult{Run: run, Node: node, Attempt: attempt, Replayed: true},
				commitCode: "WORKFLOW_DELIVERY_COMMIT_FAILED",
			}, nil
		}
		if gormDeliverySameAttemptResult(attempt, command.Result) {
			return gormDeliveryReduction{
				result:     application.DeliveryTransitionResult{Run: run, Node: node, Attempt: attempt, Replayed: true},
				commitCode: "WORKFLOW_DELIVERY_COMMIT_FAILED",
			}, nil
		}
		return gormDeliveryReduction{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_COMPLETION_CONFLICT", false, errors.New("delivery already reduced with a different result"))
	}

	now, err := gormDeliveryDatabaseNow(ctx, transaction)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	if node.Status != domain.NodeStatusRunning || node.Version != command.Binding.Fence.NodeVersion || node.LeaseOwner != command.Binding.Fence.Owner || node.LeaseUntil == nil || !node.LeaseUntil.After(now) || attempt.AttemptNo != command.Binding.Fence.AttemptNo || attempt.LeaseOwner != command.Binding.Fence.Owner {
		return gormDeliveryReduction{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("delivery lease fence failed"))
	}
	if err := domain.ValidateAttemptResult(command.Result); err != nil {
		return gormDeliveryReduction{}, err
	}
	if run.CancelRequestedAt != nil {
		safe, safetyErr := repository.gormDeliverySafeToCancelWorkflowNode(ctx, scope, node.ID)
		if safetyErr != nil {
			return gormDeliveryReduction{}, safetyErr
		}
		if safe {
			return repository.gormDeliveryControlCheckpoint(ctx, transaction, scope, run, nodes, node, attempt, domain.NodeStatusCancelled, "WORKFLOW_CANCELLED", now)
		}
		return gormDeliveryReduction{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_CANCELLATION_DEFERRED", true, errors.New("workflow cancellation waits for side-effect recovery checkpoint"))
	}
	if run.PauseRequestedAt != nil {
		return repository.gormDeliveryControlCheckpoint(ctx, transaction, scope, run, nodes, node, attempt, domain.NodeStatusPaused, "WORKFLOW_PAUSED", now)
	}
	if command.Result.Failure == nil {
		return repository.gormDeliveryComplete(ctx, transaction, scope, run, nodes, node, attempt, command.Result, now)
	}
	return repository.gormDeliveryFail(ctx, transaction, scope, run, nodes, node, attempt, *command.Result.Failure, now)
}

func gormDeliveryDatabaseNow(ctx context.Context, transaction *gorm.DB) (time.Time, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, err
	}
	var now time.Time
	err = row.Scan(&now)
	return now, err
}

func gormDeliveryLockRuntimeNodes(ctx context.Context, transaction *gorm.DB, runID foundation.ID) (map[foundation.ID]domain.NodeRun, error) {
	rows, err := gormWorkflowRawRows(transaction.WithContext(ctx), `SELECT `+runtimeNodeColumns+` FROM workflow.node_run WHERE run_id=? ORDER BY node_key,id FOR UPDATE`, string(runID))
	if err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	defer func() { _ = rows.Close() }()

	result := make(map[foundation.ID]domain.NodeRun)
	for rows.Next() {
		node, scanErr := scanRuntimeNode(rows)
		if scanErr != nil {
			return nil, classifyGORMWorkflow(ctx, joinGORMRowsError(rows, scanErr), "WORKFLOW_NODE_QUERY_FAILED")
		}
		result[node.ID] = node
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	if err := rows.Close(); err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	return result, nil
}

func gormDeliveryFindAttemptByDelivery(ctx context.Context, transaction *gorm.DB, nodeID foundation.ID, dispatchNo int, deliveryID string) (domain.NodeAttempt, bool, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT `+runtimeAttemptColumns+` FROM workflow.node_attempt WHERE node_run_id=? AND dispatch_no=? AND delivery_id=? ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, string(nodeID), dispatchNo, deliveryID)
	if err != nil {
		return domain.NodeAttempt{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	attempt, err := scanRuntimeAttempt(row)
	if gormWorkflowNoRows(err) {
		return domain.NodeAttempt{}, false, nil
	}
	if err != nil {
		return domain.NodeAttempt{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return attempt, true, nil
}

func gormDeliverySameAttemptResult(attempt domain.NodeAttempt, result domain.AttemptResult) bool {
	if result.Failure == nil {
		hash := sha256.Sum256(result.Output)
		return attempt.Status == domain.AttemptStatusSucceeded && attempt.OutputSchemaVersion == result.OutputSchemaVersion && strings.EqualFold(attempt.OutputHash, encodinghex.EncodeToString(hash[:]))
	}
	return attempt.FailureClass == result.Failure.Class && attempt.ErrorCode == result.Failure.Code
}

func (repository *GORMRuntimeRepository) gormDeliveryComplete(ctx context.Context, transaction *gorm.DB, scope foundation.TransactionScope, run domain.Run, nodes map[foundation.ID]domain.NodeRun, node domain.NodeRun, attempt domain.NodeAttempt, result domain.AttemptResult, now time.Time) (gormDeliveryReduction, error) {
	hash := sha256.Sum256(result.Output)
	outputHash := encodinghex.EncodeToString(hash[:])
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_attempt SET status='succeeded',output_schema_version=?,output_hash=?,lease_owner=NULL,lease_until=NULL,ended_at=?,heartbeat_at=? WHERE id=? RETURNING `+runtimeAttemptColumns, result.OutputSchemaVersion, outputHash, now, now, string(attempt.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_COMPLETE_FAILED")
	}
	updatedAttempt, err := scanRuntimeAttempt(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_COMPLETE_FAILED")
	}

	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_run SET status='succeeded',output=?::jsonb,lease_owner=NULL,lease_until=NULL,next_attempt_at=NULL,completed_at=?,updated_at=?,version=version+1 WHERE id=? RETURNING `+runtimeNodeColumns, workflowJSONB(result.Output), now, now, string(node.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_COMPLETE_FAILED")
	}
	updatedNode, err := scanRuntimeNode(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_COMPLETE_FAILED")
	}
	nodes[node.ID] = updatedNode
	if run.PauseRequestedAt == nil && run.CancelRequestedAt == nil {
		if err := repository.gormDeliveryActivateSuccessors(ctx, transaction, scope, run, nodes, updatedNode, now); err != nil {
			return gormDeliveryReduction{}, err
		}
	}
	status, err := gormDeliveryReduceRunStatus(run, nodes)
	if err != nil {
		return gormDeliveryReduction{}, err
	}
	run, err = gormDeliveryPersistRunReduction(ctx, transaction, run, status, now)
	if err != nil {
		return gormDeliveryReduction{}, err
	}
	if err := gormDeliveryInsertRuntimeEvent(ctx, transaction, run, updatedNode, updatedAttempt, "workflow.node.succeeded"); err != nil {
		return gormDeliveryReduction{}, err
	}
	if err := repository.gormDeliveryNotifyWorkflowNodeTerminal(ctx, scope, application.WorkflowNodeTerminalEvent{
		WorkspaceID:    run.WorkspaceID,
		WorkflowRunID:  run.ID,
		NodeRunID:      updatedNode.ID,
		NodeKind:       updatedNode.NodeType,
		NodeAttemptID:  updatedAttempt.ID,
		Outcome:        application.WorkflowTerminalOutcomeSucceeded,
		TerminalOutput: string(updatedNode.Output),
		TerminalAt:     now,
	}); err != nil {
		return gormDeliveryReduction{}, err
	}
	return gormDeliveryReduction{
		result:     application.DeliveryTransitionResult{Run: run, Node: updatedNode, Attempt: updatedAttempt},
		commitCode: "WORKFLOW_COMPLETE_COMMIT_FAILED",
	}, nil
}

func (repository *GORMRuntimeRepository) gormDeliveryNotifyWorkflowNodeTerminal(ctx context.Context, scope foundation.TransactionScope, event application.WorkflowNodeTerminalEvent) error {
	if repository == nil || repository.terminal == nil {
		return nil
	}
	if err := repository.terminal.OnWorkflowNodeTerminalScoped(ctx, scope, event); err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			return err
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_TERMINAL_HOOK_FAILED", true, err)
	}
	return nil
}

func (repository *GORMRuntimeRepository) gormDeliveryFail(ctx context.Context, transaction *gorm.DB, scope foundation.TransactionScope, run domain.Run, nodes map[foundation.ID]domain.NodeRun, node domain.NodeRun, attempt domain.NodeAttempt, failure domain.FailureEnvelope, now time.Time) (gormDeliveryReduction, error) {
	attemptFailure := failure
	if run.CancelRequestedAt != nil {
		return repository.gormDeliveryControlCheckpoint(ctx, transaction, scope, run, nodes, node, attempt, domain.NodeStatusCancelled, "WORKFLOW_CANCELLED", now)
	}
	if run.PauseRequestedAt != nil {
		return repository.gormDeliveryControlCheckpoint(ctx, transaction, scope, run, nodes, node, attempt, domain.NodeStatusPaused, "WORKFLOW_PAUSED", now)
	}
	if failure.Class == domain.FailureClassRetryable {
		policy, err := gormDeliveryRetryPolicyForNode(ctx, transaction, run.DefinitionID, node.NodeKey)
		if err != nil {
			return gormDeliveryReduction{}, err
		}
		schedule, err := domain.CalculateRetry(policy, domain.RetryIdentity{RunID: run.ID, NodeKey: node.NodeKey}, node.RetryNo, failure.RetryAfter)
		if err != nil {
			return gormDeliveryReduction{}, err
		}
		if !schedule.Exhausted {
			dispatchNo := node.DispatchNo + 1
			nextAt := now.Add(schedule.Delay)
			row, updateErr := gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_attempt SET status='retry_scheduled',failure_class=?,error_kind=?,error_code=?,error_summary=?,next_attempt_at=?,lease_owner=NULL,lease_until=NULL,ended_at=?,heartbeat_at=? WHERE id=? RETURNING `+runtimeAttemptColumns, string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, nextAt, now, now, string(attempt.ID))
			if updateErr != nil {
				return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, updateErr, "WORKFLOW_ATTEMPT_RETRY_FAILED")
			}
			updatedAttempt, updateErr := scanRuntimeAttempt(row)
			if updateErr != nil {
				return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, updateErr, "WORKFLOW_ATTEMPT_RETRY_FAILED")
			}

			row, updateErr = gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_run SET status='retry_wait',retry_no=?,dispatch_no=?,next_attempt_at=?,failure_class=?,error_kind=?,error_code=?,error_summary=?,lease_owner=NULL,lease_until=NULL,updated_at=?,version=version+1 WHERE id=? RETURNING `+runtimeNodeColumns, schedule.NextRetryNo, dispatchNo, nextAt, string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, now, string(node.ID))
			if updateErr != nil {
				return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, updateErr, "WORKFLOW_NODE_RETRY_FAILED")
			}
			updatedNode, updateErr := scanRuntimeNode(row)
			if updateErr != nil {
				return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, updateErr, "WORKFLOW_NODE_RETRY_FAILED")
			}
			nodes[node.ID] = updatedNode
			if err := gormDeliveryInsertRuntimeEvent(ctx, transaction, run, updatedNode, updatedAttempt, "workflow.node.retry_scheduled"); err != nil {
				return gormDeliveryReduction{}, err
			}
			args, argsErr := riveradapter.NewNodeJobArgs(updatedNode.ID, dispatchNo)
			if argsErr != nil {
				return gormDeliveryReduction{}, argsErr
			}
			if _, insertErr := repository.jobs.InsertTx(ctx, scope, args, riveradapter.InsertOptions{ScheduledAt: nextAt}); insertErr != nil {
				return gormDeliveryReduction{}, insertErr
			}
			status, reduceErr := gormDeliveryReduceRunStatus(run, nodes)
			if reduceErr != nil {
				return gormDeliveryReduction{}, reduceErr
			}
			run, reduceErr = gormDeliveryPersistRunReduction(ctx, transaction, run, status, now)
			if reduceErr != nil {
				return gormDeliveryReduction{}, reduceErr
			}
			return gormDeliveryReduction{
				result:     application.DeliveryTransitionResult{Run: run, Node: updatedNode, Attempt: updatedAttempt},
				commitCode: "WORKFLOW_RETRY_COMMIT_FAILED",
			}, nil
		}
		failure = domain.FailureEnvelope{Class: domain.FailureClassNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure, Code: "WORKFLOW_RETRY_EXHAUSTED", Summary: "retry limit exhausted"}
	}

	attemptStatus := domain.AttemptStatusFailed
	nodeStatus := domain.NodeStatusFailed
	if failure.Class == domain.FailureClassLeaseLost {
		attemptStatus = domain.AttemptStatusLeaseLost
	}
	if failure.Class == domain.FailureClassManualRecovery {
		attemptStatus = domain.AttemptStatusManualRecovery
	}
	if failure.Class == domain.FailureClassCancelled {
		attemptStatus = domain.AttemptStatusCancelled
		nodeStatus = domain.NodeStatusCancelled
	}
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_attempt SET status=?,failure_class=?,error_kind=?,error_code=?,error_summary=?,lease_owner=NULL,lease_until=NULL,ended_at=?,heartbeat_at=? WHERE id=? RETURNING `+runtimeAttemptColumns, string(attemptStatus), string(attemptFailure.Class), string(attemptFailure.ErrorKind), attemptFailure.Code, attemptFailure.Summary, now, now, string(attempt.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_FAIL_FAILED")
	}
	updatedAttempt, err := scanRuntimeAttempt(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_ATTEMPT_FAIL_FAILED")
	}
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_run SET status=?,failure_class=?,error_kind=?,error_code=?,error_summary=?,lease_owner=NULL,lease_until=NULL,completed_at=?,updated_at=?,version=version+1 WHERE id=? RETURNING `+runtimeNodeColumns, string(nodeStatus), string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, now, now, string(node.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_FAIL_FAILED")
	}
	updatedNode, err := scanRuntimeNode(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_FAIL_FAILED")
	}
	nodes[node.ID] = updatedNode
	status, err := gormDeliveryReduceRunStatus(run, nodes)
	if err != nil {
		return gormDeliveryReduction{}, err
	}
	run, err = gormDeliveryPersistRunReduction(ctx, transaction, run, status, now)
	if err != nil {
		return gormDeliveryReduction{}, err
	}
	if err := gormDeliveryInsertRuntimeEvent(ctx, transaction, run, updatedNode, updatedAttempt, "workflow.node.failed"); err != nil {
		return gormDeliveryReduction{}, err
	}
	outcome := application.WorkflowTerminalOutcomeFailed
	if nodeStatus == domain.NodeStatusCancelled {
		outcome = application.WorkflowTerminalOutcomeCancelled
	}
	if err := repository.gormDeliveryNotifyWorkflowNodeTerminal(ctx, scope, application.WorkflowNodeTerminalEvent{
		WorkspaceID:    run.WorkspaceID,
		WorkflowRunID:  run.ID,
		NodeRunID:      updatedNode.ID,
		NodeKind:       updatedNode.NodeType,
		NodeAttemptID:  updatedAttempt.ID,
		Outcome:        outcome,
		FailureClass:   failure.Class,
		FailureCode:    failure.Code,
		FailureSummary: failure.Summary,
		TerminalAt:     now,
	}); err != nil {
		return gormDeliveryReduction{}, err
	}
	return gormDeliveryReduction{
		result:     application.DeliveryTransitionResult{Run: run, Node: updatedNode, Attempt: updatedAttempt},
		commitCode: "WORKFLOW_FAIL_COMMIT_FAILED",
	}, nil
}

func gormDeliveryRetryPolicyForNode(ctx context.Context, transaction *gorm.DB, definitionID foundation.ID, nodeKey string) (domain.RetryPolicy, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT graph::text FROM workflow.definition WHERE id=?`, string(definitionID))
	if err != nil {
		return domain.RetryPolicy{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	var graphJSON workflowJSONB
	if err := row.Scan(&graphJSON); err != nil {
		return domain.RetryPolicy{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	canonical, err := application.DecodeCanonicalGraph(graphJSON)
	if err != nil {
		return domain.RetryPolicy{}, err
	}
	for _, node := range canonical.Nodes {
		if node.Key == nodeKey {
			return node.RetryPolicy, nil
		}
	}
	return domain.RetryPolicy{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_DEFINITION_NODE_NOT_FOUND", false, errors.New("node definition not found"))
}

func (repository *GORMRuntimeRepository) gormDeliveryActivateSuccessors(ctx context.Context, transaction *gorm.DB, scope foundation.TransactionScope, run domain.Run, nodes map[foundation.ID]domain.NodeRun, completed domain.NodeRun, now time.Time) error {
	if run.PauseRequestedAt != nil || run.CancelRequestedAt != nil {
		return nil
	}
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT graph::text FROM workflow.definition WHERE id=?`, string(run.DefinitionID))
	if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	var graphJSON workflowJSONB
	if err := row.Scan(&graphJSON); err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	graph, err := application.DecodeCanonicalGraph(graphJSON)
	if err != nil {
		return err
	}
	statusByKey := make(map[string]domain.NodeStatus)
	for _, node := range nodes {
		statusByKey[node.NodeKey] = node.Status
	}
	for _, successor := range graph.Nodes {
		depends := false
		for _, dependency := range successor.Dependencies {
			if dependency == completed.NodeKey {
				depends = true
				break
			}
		}
		if !depends {
			continue
		}
		allSucceeded := true
		for _, dependency := range successor.Dependencies {
			if statusByKey[dependency] != domain.NodeStatusSucceeded {
				allSucceeded = false
				break
			}
		}
		if !allSucceeded {
			continue
		}

		row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT id::text FROM workflow.node_run WHERE run_id=? AND node_key=? FOR UPDATE`, string(run.ID), successor.Key)
		if err != nil {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
		}
		var existingID string
		err = row.Scan(&existingID)
		if err == nil {
			row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT `+runtimeNodeColumns+` FROM workflow.node_run WHERE id=?`, existingID)
			if err != nil {
				return classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
			}
			existing, scanErr := scanRuntimeNode(row)
			if scanErr != nil {
				return classifyGORMWorkflow(ctx, scanErr, "WORKFLOW_NODE_QUERY_FAILED")
			}
			if existing.Status != domain.NodeStatusPending || existing.DispatchNo < 1 {
				continue
			}
			args, argsErr := riveradapter.NewNodeJobArgs(existing.ID, existing.DispatchNo)
			if argsErr != nil {
				return argsErr
			}
			if _, insertErr := repository.jobs.InsertTx(ctx, scope, args, riveradapter.InsertOptions{}); insertErr != nil {
				return insertErr
			}
			continue
		}
		if !gormWorkflowNoRows(err) {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
		}

		id, idErr := foundation.NewUUIDGenerator(nil).New()
		if idErr != nil {
			return idErr
		}
		idempotency := "node:" + string(run.ID) + ":" + successor.Key
		row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,idempotency_key,input_schema_version,output_schema_version,dispatch_no,retry_no)
VALUES(?,?,?,?,'pending',0,?::jsonb,1,?,?,?,?,?,1,0) RETURNING `+runtimeNodeColumns,
			string(id), string(run.ID), successor.Key, successor.Kind, workflowJSONB(completed.Output), now, now,
			idempotency, successor.InputSchemaVersion, successor.OutputSchemaVersion)
		if err != nil {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_CREATE_FAILED")
		}
		created, createErr := scanRuntimeNode(row)
		if createErr != nil {
			return classifyGORMWorkflow(ctx, createErr, "WORKFLOW_NODE_CREATE_FAILED")
		}
		nodes[created.ID] = created
		statusByKey[created.NodeKey] = created.Status
		args, argsErr := riveradapter.NewNodeJobArgs(created.ID, created.DispatchNo)
		if argsErr != nil {
			return argsErr
		}
		if _, insertErr := repository.jobs.InsertTx(ctx, scope, args, riveradapter.InsertOptions{}); insertErr != nil {
			return insertErr
		}
	}
	return nil
}

func gormDeliveryReduceRunStatus(run domain.Run, nodes map[foundation.ID]domain.NodeRun) (domain.RunStatus, error) {
	if domain.IsTerminalRunStatus(run.Status) {
		return run.Status, nil
	}
	allSucceeded := len(nodes) > 0
	hasFailed, hasCancelled, hasWaiting, hasRetry, hasRunning, hasPending := false, false, false, false, false, false
	for _, node := range nodes {
		switch node.Status {
		case domain.NodeStatusSucceeded:
		case domain.NodeStatusFailed:
			hasFailed = true
			allSucceeded = false
		case domain.NodeStatusCancelled:
			hasCancelled = true
			allSucceeded = false
		case domain.NodeStatusWaitingForHuman:
			hasWaiting = true
			allSucceeded = false
		case domain.NodeStatusRetryWait:
			hasRetry = true
			allSucceeded = false
		case domain.NodeStatusRunning:
			hasRunning = true
			allSucceeded = false
		case domain.NodeStatusPending, domain.NodeStatusPaused:
			hasPending = true
			allSucceeded = false
		default:
			return domain.RunStatusFailed, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_NODE_STATUS_INVALID", false, errors.New("unknown node status"))
		}
	}
	if allSucceeded {
		return domain.RunStatusSucceeded, nil
	}
	if hasCancelled && !hasRunning {
		return domain.RunStatusCancelled, nil
	}
	if hasFailed && !hasRunning && !hasWaiting && !hasRetry && !hasPending {
		return domain.RunStatusFailed, nil
	}
	if run.PauseRequestedAt != nil && !hasRunning {
		return domain.RunStatusPaused, nil
	}
	if hasWaiting && !hasRunning && !hasPending {
		return domain.RunStatusWaitingForHuman, nil
	}
	if hasRetry && !hasRunning && !hasPending {
		return domain.RunStatusRetryWait, nil
	}
	return domain.RunStatusRunning, nil
}

func gormDeliveryPersistRunReduction(ctx context.Context, transaction *gorm.DB, run domain.Run, status domain.RunStatus, now time.Time) (domain.Run, error) {
	if status == run.Status {
		return run, nil
	}
	var completedAt any
	if domain.IsTerminalRunStatus(status) {
		completedAt = now
	}
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.run SET status=?,completed_at=?::timestamptz,pause_requested_at=CASE WHEN ?::timestamptz IS NOT NULL THEN NULL ELSE pause_requested_at END,cancel_requested_at=CASE WHEN ?::timestamptz IS NOT NULL THEN NULL ELSE cancel_requested_at END,updated_at=?,version=version+1 WHERE id=? RETURNING `+runtimeRunColumns,
		string(status), completedAt, completedAt, completedAt, now, string(run.ID))
	if err != nil {
		return domain.Run{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_REDUCE_FAILED")
	}
	updated, err := scanRuntimeRun(row)
	if err != nil {
		return domain.Run{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_REDUCE_FAILED")
	}
	return updated, nil
}

func (repository *GORMRuntimeRepository) gormDeliveryControlCheckpoint(ctx context.Context, transaction *gorm.DB, scope foundation.TransactionScope, run domain.Run, nodes map[foundation.ID]domain.NodeRun, node domain.NodeRun, attempt domain.NodeAttempt, nodeStatus domain.NodeStatus, code string, now time.Time) (gormDeliveryReduction, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_attempt SET status='cancelled',failure_class='cancelled',error_kind=?,error_code=?,error_summary=?,lease_owner=NULL,lease_until=NULL,ended_at=?,heartbeat_at=? WHERE id=? RETURNING `+runtimeAttemptColumns, string(foundation.ErrorVersionConflict), code, code, now, now, string(attempt.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	updatedAttempt, err := scanRuntimeAttempt(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	var nodeCompletedAt any
	if nodeStatus == domain.NodeStatusCancelled {
		nodeCompletedAt = now
	}
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.node_run SET status=?,lease_owner=NULL,lease_until=NULL,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=?,error_summary=?,completed_at=?::timestamptz,updated_at=?,version=version+1 WHERE id=? RETURNING `+runtimeNodeColumns, string(nodeStatus), code, code, nodeCompletedAt, now, string(node.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	updatedNode, err := scanRuntimeNode(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	nodes[node.ID] = updatedNode
	var completedAt any
	status := run.Status
	if !gormDeliveryHasRunningNode(nodes) {
		status = domain.RunStatusPaused
	}
	if nodeStatus == domain.NodeStatusCancelled && !gormDeliveryHasRunningNode(nodes) {
		completedAt = now
		status = domain.RunStatusCancelled
	}
	row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `UPDATE workflow.run SET status=?,completed_at=?::timestamptz,pause_requested_at=CASE WHEN ?='cancelled' THEN NULL ELSE pause_requested_at END,updated_at=?,version=version+1 WHERE id=? RETURNING `+runtimeRunColumns, string(status), completedAt, string(status), now, string(run.ID))
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	updatedRun, err := scanRuntimeRun(row)
	if err != nil {
		return gormDeliveryReduction{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	if err := gormDeliveryInsertRuntimeEvent(ctx, transaction, updatedRun, updatedNode, updatedAttempt, "workflow.node."+strings.ToLower(code)); err != nil {
		return gormDeliveryReduction{}, err
	}
	if nodeStatus == domain.NodeStatusCancelled {
		if err := repository.gormDeliveryNotifyWorkflowNodeTerminal(ctx, scope, application.WorkflowNodeTerminalEvent{
			WorkspaceID:    updatedRun.WorkspaceID,
			WorkflowRunID:  updatedRun.ID,
			NodeRunID:      updatedNode.ID,
			NodeKind:       updatedNode.NodeType,
			NodeAttemptID:  updatedAttempt.ID,
			Outcome:        application.WorkflowTerminalOutcomeCancelled,
			FailureClass:   domain.FailureClassCancelled,
			FailureCode:    code,
			FailureSummary: code,
			TerminalAt:     now,
		}); err != nil {
			return gormDeliveryReduction{}, err
		}
	}
	return gormDeliveryReduction{
		result:     application.DeliveryTransitionResult{Run: updatedRun, Node: updatedNode, Attempt: updatedAttempt},
		commitCode: "WORKFLOW_CONTROL_CHECKPOINT_COMMIT_FAILED",
	}, nil
}

func gormDeliveryInsertRuntimeEvent(ctx context.Context, transaction *gorm.DB, run domain.Run, node domain.NodeRun, attempt domain.NodeAttempt, eventType string) error {
	eventKey := eventType + ":" + string(node.ID) + ":" + strconv.Itoa(attempt.AttemptNo)
	eventID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return err
	}
	occurredAt := attempt.StartedAt
	if attempt.EndedAt != nil {
		occurredAt = *attempt.EndedAt
	} else if !attempt.HeartbeatAt.IsZero() {
		occurredAt = attempt.HeartbeatAt
	}
	if err := transaction.WithContext(ctx).Exec(`INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at)
VALUES(?,?,?,?,?,?,?,1,'{}'::jsonb,?)
ON CONFLICT(workspace_id,event_key,event_version) WHERE event_key IS NOT NULL DO NOTHING`, string(eventID), string(run.WorkspaceID), string(run.ID), eventType, eventKey, eventKey, runtimeEventSchemaVersion, occurredAt).Error; err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	return nil
}

func gormDeliveryHasRunningNode(nodes map[foundation.ID]domain.NodeRun) bool {
	for _, node := range nodes {
		if node.Status == domain.NodeStatusRunning {
			return true
		}
	}
	return false
}

func (repository *GORMRuntimeRepository) gormDeliverySafeToCancelWorkflowNode(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	if repository == nil || repository.cancellation == nil {
		return true, nil
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
