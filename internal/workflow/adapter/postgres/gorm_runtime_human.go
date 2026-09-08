package workflowpostgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// WaitForHuman releases one live delivery lease and creates its Human Task in
// the same scoped transaction.
func (repository *GORMRuntimeRepository) WaitForHuman(ctx context.Context, command application.HumanWaitTransition) (application.HumanTransitionResult, error) {
	var result application.HumanTransitionResult
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		result, err = repository.waitForHuman(callbackCtx, tx, scope, command)
		if err == nil {
			callbackSucceeded = true
		}
		return err
	})
	if err != nil {
		if callbackSucceeded {
			return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_COMMIT_FAILED")
		}
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_TRANSACTION_FAILED")
	}
	return result, nil
}

func (repository *GORMRuntimeRepository) waitForHuman(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	command application.HumanWaitTransition,
) (application.HumanTransitionResult, error) {
	run, err := gormHumanLockRun(ctx, tx, command.RunID)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	nodes, err := gormDeliveryLockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	node, ok := nodes[command.NodeRunID]
	if !ok {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, errors.New("human node not found"))
	}
	attempt, found, err := gormHumanFindAttemptByNo(ctx, tx, command.NodeRunID, command.Fence.AttemptNo)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	now, err := gormDeliveryDatabaseNow(ctx, tx)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	if !found || node.Status != domain.NodeStatusRunning || node.Version != command.Fence.NodeVersion ||
		node.LeaseOwner != command.Fence.Owner || node.LeaseUntil == nil || !node.LeaseUntil.After(now) ||
		attempt.Status != domain.AttemptStatusRunning || attempt.LeaseOwner != command.Fence.Owner {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("human task lease fence failed"))
	}
	if domain.IsTerminalRunStatus(run.Status) || run.CancelRequestedAt != nil {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow cannot wait for human"))
	}
	expiresAt := now.Add(command.ExpiresIn)
	row, err := gormWorkflowRawRow(tx, `UPDATE workflow.node_attempt
		SET status='waiting_for_human',lease_owner=NULL,lease_until=NULL,ended_at=?,heartbeat_at=?
		WHERE id=?::uuid RETURNING `+runtimeAttemptColumns, now, now, string(attempt.ID))
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_ATTEMPT_UPDATE_FAILED")
	}
	updatedAttempt, err := scanRuntimeAttempt(row)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_ATTEMPT_UPDATE_FAILED")
	}
	row, err = gormWorkflowRawRow(tx, `UPDATE workflow.node_run
		SET status='waiting_for_human',lease_owner=NULL,lease_until=NULL,updated_at=?,version=version+1
		WHERE id=?::uuid RETURNING `+runtimeNodeColumns, now, string(node.ID))
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_NODE_UPDATE_FAILED")
	}
	updatedNode, err := scanRuntimeNode(row)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_NODE_UPDATE_FAILED")
	}
	nodes[node.ID] = updatedNode
	status, err := gormDeliveryReduceRunStatus(run, nodes)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	run, err = gormDeliveryPersistRunReduction(ctx, tx, run, status, now)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	row, err = gormWorkflowRawRow(tx, `INSERT INTO workflow.human_task(
		id,run_id,node_run_id,status,expected_input_schema,target_version,expires_at,created_at
	) VALUES(?::uuid,?::uuid,?::uuid,'pending',?::jsonb,?,?,?) RETURNING `+humanColumns,
		string(command.TaskID), string(run.ID), string(node.ID), workflowJSONB(command.ExpectedInputSchema),
		command.TargetVersion, expiresAt, now)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_CREATE_FAILED")
	}
	task, err := scanHuman(row)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_CREATE_FAILED")
	}
	if err := gormDeliveryInsertRuntimeEvent(ctx, tx, run, updatedNode, updatedAttempt, "workflow.human.requested"); err != nil {
		return application.HumanTransitionResult{}, err
	}
	run, err = gormHumanReadRun(ctx, tx, run.ID)
	if err != nil {
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	_ = scope // The Human wait path owns no cross-owner hook or River insert.
	return application.HumanTransitionResult{Task: task, Run: run, Node: updatedNode, Attempt: updatedAttempt}, nil
}

// SubmitHuman resolves one task, completes its node, and activates successors
// in one scoped transaction. Expiry is committed before the conflict is
// returned.
func (repository *GORMRuntimeRepository) SubmitHuman(ctx context.Context, command application.HumanDecisionTransition) (application.HumanTransitionResult, error) {
	var result application.HumanTransitionResult
	callbackSucceeded := false
	expired := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		result, expired, err = repository.submitHuman(callbackCtx, tx, scope, command)
		if err == nil {
			callbackSucceeded = true
		}
		return err
	})
	if err != nil {
		if callbackSucceeded {
			code := "HUMAN_SUBMIT_COMMIT_FAILED"
			if expired {
				code = "HUMAN_TASK_EXPIRE_COMMIT_FAILED"
			}
			return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, code)
		}
		return application.HumanTransitionResult{}, classifyGORMWorkflow(ctx, err, "HUMAN_SUBMIT_TRANSACTION_FAILED")
	}
	if expired {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_EXPIRED", false, errors.New("human task expired"))
	}
	return result, nil
}

func (repository *GORMRuntimeRepository) submitHuman(
	ctx context.Context,
	tx *gorm.DB,
	scope foundation.TransactionScope,
	command application.HumanDecisionTransition,
) (application.HumanTransitionResult, bool, error) {
	run, err := gormHumanLockRun(ctx, tx, command.RunID)
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if err := gormHumanAuthorizeRun(ctx, tx, run, command.CallerCapabilities); err != nil {
		return application.HumanTransitionResult{}, false, err
	}
	nodes, err := gormDeliveryLockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.HumanTransitionResult{}, false, err
	}
	row, err := gormWorkflowRawRow(tx, `SELECT `+humanColumns+`
		FROM workflow.human_task WHERE id=?::uuid AND run_id=?::uuid FOR UPDATE`, string(command.TaskID), string(run.ID))
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_QUERY_FAILED")
	}
	task, err := scanHuman(row)
	if gormWorkflowNoRows(err) {
		return application.HumanTransitionResult{}, false, foundation.NewError(foundation.ErrorNotFound, "HUMAN_TASK_NOT_FOUND", false, err)
	}
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_QUERY_FAILED")
	}
	if task.TargetVersion != command.TargetVersion {
		return application.HumanTransitionResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_VERSION_CONFLICT", false, errors.New("target version differs"))
	}
	if task.Status == domain.HumanTaskSubmitted {
		if !jsonEqual(task.Decision, command.Decision) {
			return application.HumanTransitionResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_DECISION_CONFLICT", false, errors.New("submitted human decision differs"))
		}
		node := nodes[task.NodeRunID]
		attempt, _, queryErr := gormHumanFindLatestAttempt(ctx, tx, task.NodeRunID)
		if queryErr != nil {
			return application.HumanTransitionResult{}, false, queryErr
		}
		return application.HumanTransitionResult{Task: task, Run: run, Node: node, Attempt: attempt}, false, nil
	}
	if task.Status != domain.HumanTaskPending {
		return application.HumanTransitionResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_ALREADY_RESOLVED", false, errors.New("human task is not pending"))
	}
	now, err := gormDeliveryDatabaseNow(ctx, tx)
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	if task.ExpiresAt != nil && !task.ExpiresAt.After(now) {
		statement := tx.Exec(`UPDATE workflow.human_task SET status='expired' WHERE id=?::uuid`, string(task.ID))
		if statement.Error != nil {
			return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, statement.Error, "HUMAN_TASK_EXPIRE_FAILED")
		}
		return application.HumanTransitionResult{}, true, nil
	}
	if run.CancelRequestedAt != nil || run.Status == domain.RunStatusCancelled {
		return application.HumanTransitionResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("cancelled workflow rejects human decision"))
	}
	node, ok := nodes[task.NodeRunID]
	if !ok || node.Status != domain.NodeStatusWaitingForHuman {
		return application.HumanTransitionResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_NODE_STATE_INVALID", false, errors.New("human node is not waiting"))
	}
	if err := validateDecisionSchema(task.ExpectedInputSchema, command.Decision); err != nil {
		return application.HumanTransitionResult{}, false, err
	}
	row, err = gormWorkflowRawRow(tx, `UPDATE workflow.human_task
		SET status='submitted',decision=?::jsonb,submitted_at=?
		WHERE id=?::uuid RETURNING `+humanColumns, workflowJSONB(command.Decision), now, string(task.ID))
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_SUBMIT_FAILED")
	}
	updatedTask, err := scanHuman(row)
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_SUBMIT_FAILED")
	}
	row, err = gormWorkflowRawRow(tx, `UPDATE workflow.node_run
		SET status='succeeded',output=?::jsonb,completed_at=?,updated_at=?,version=version+1
		WHERE id=?::uuid RETURNING `+runtimeNodeColumns,
		workflowJSONB(command.Decision), now, now, string(node.ID))
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_NODE_COMPLETE_FAILED")
	}
	updatedNode, err := scanRuntimeNode(row)
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "HUMAN_TASK_NODE_COMPLETE_FAILED")
	}
	nodes[node.ID] = updatedNode
	if run.Status != domain.RunStatusPaused && run.PauseRequestedAt == nil {
		if err := repository.gormDeliveryActivateSuccessors(ctx, tx, scope, run, nodes, updatedNode, now); err != nil {
			return application.HumanTransitionResult{}, false, err
		}
		status, reduceErr := gormDeliveryReduceRunStatus(run, nodes)
		if reduceErr != nil {
			return application.HumanTransitionResult{}, false, reduceErr
		}
		run, err = gormDeliveryPersistRunReduction(ctx, tx, run, status, now)
		if err != nil {
			return application.HumanTransitionResult{}, false, err
		}
	}
	attempt, _, err := gormHumanFindLatestAttempt(ctx, tx, node.ID)
	if err != nil {
		return application.HumanTransitionResult{}, false, err
	}
	if err := gormDeliveryInsertRuntimeEvent(ctx, tx, run, updatedNode, attempt, "workflow.human.submitted"); err != nil {
		return application.HumanTransitionResult{}, false, err
	}
	run, err = gormHumanReadRun(ctx, tx, run.ID)
	if err != nil {
		return application.HumanTransitionResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	return application.HumanTransitionResult{Task: updatedTask, Run: run, Node: updatedNode, Attempt: attempt}, false, nil
}

func gormHumanLockRun(ctx context.Context, tx *gorm.DB, runID foundation.ID) (domain.Run, error) {
	row, err := gormWorkflowRawRow(tx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=?::uuid FOR UPDATE`, string(runID))
	if err != nil {
		return domain.Run{}, err
	}
	return scanRuntimeRun(row)
}

func gormHumanReadRun(ctx context.Context, tx *gorm.DB, runID foundation.ID) (domain.Run, error) {
	row, err := gormWorkflowRawRow(tx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=?::uuid`, string(runID))
	if err != nil {
		return domain.Run{}, err
	}
	return scanRuntimeRun(row)
}

func gormHumanFindAttemptByNo(ctx context.Context, tx *gorm.DB, nodeID foundation.ID, attemptNo int) (domain.NodeAttempt, bool, error) {
	row, err := gormWorkflowRawRow(tx, `SELECT `+runtimeAttemptColumns+`
		FROM workflow.node_attempt WHERE node_run_id=?::uuid AND attempt_no=? FOR UPDATE`, string(nodeID), attemptNo)
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

func gormHumanFindLatestAttempt(ctx context.Context, tx *gorm.DB, nodeID foundation.ID) (domain.NodeAttempt, bool, error) {
	row, err := gormWorkflowRawRow(tx, `SELECT `+runtimeAttemptColumns+`
		FROM workflow.node_attempt WHERE node_run_id=?::uuid ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, string(nodeID))
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

func gormHumanAuthorizeRun(ctx context.Context, tx *gorm.DB, run domain.Run, caller []capability.Capability) error {
	if caller == nil {
		return nil
	}
	row, err := gormWorkflowRawRow(tx, `SELECT graph::text FROM workflow.definition WHERE id=?::uuid AND workspace_id=?::uuid`, string(run.DefinitionID), string(run.WorkspaceID))
	if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	var graphText string
	if err := row.Scan(&graphText); gormWorkflowNoRows(err) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_BINDING_INVALID", false, errors.New("workflow run definition binding is missing"))
	} else if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	graph, err := application.DecodeCanonicalGraph([]byte(graphText))
	if err != nil {
		return err
	}
	return application.AuthorizeWorkflowDefinition(graph, caller)
}

var _ application.RuntimeHumanStatePort = (*GORMRuntimeRepository)(nil)
