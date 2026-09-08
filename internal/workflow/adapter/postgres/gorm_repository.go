package workflowpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

var errGORMWorkflowCompletionReplay = errors.New("workflow completion replay requires rollback")

// Start atomically persists a definition, run, first node, and outbox event.
func (repository *GORMRepository) Start(ctx context.Context, request domain.StartRequest) (domain.Run, error) {
	var persisted domain.Run
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		if validationErr := validateGORMWorkflowJSONB(request.Definition.Graph); validationErr != nil {
			return validationErr
		}
		row, rowErr := gormWorkflowRawRow(transaction, `
			INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES(?,?,?,?,?::jsonb,?)
			ON CONFLICT(workspace_id,key,version) DO NOTHING
			RETURNING id::text,graph::text`,
			string(request.Definition.ID), string(request.Definition.WorkspaceID), request.Definition.Key,
			request.Definition.Version, gormWorkflowJSONBArgument(request.Definition.Graph), request.Definition.CreatedAt.UTC(),
		)
		if rowErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowErr, "WORKFLOW_DEFINITION_CREATE_FAILED")
		}
		var definitionID string
		var graph workflowJSONB
		rowErr = row.Scan(&definitionID, &graph)
		if gormWorkflowNoRows(rowErr) {
			row, rowErr = gormWorkflowRawRow(transaction, `
				SELECT id::text,graph::text
				FROM workflow.definition
				WHERE workspace_id=? AND key=? AND version=?`,
				string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version,
			)
			if rowErr == nil {
				rowErr = row.Scan(&definitionID, &graph)
			}
		}
		if rowErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowErr, "WORKFLOW_DEFINITION_CREATE_FAILED")
		}
		if !jsonEqual(graph, request.Definition.Graph) {
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_VERSION_CONFLICT", false, errors.New("definition version has different graph"))
		}

		request.Run.DefinitionID = foundation.ID(definitionID)
		persisted, rowErr = insertGORMRun(transaction, request.Run)
		if rowErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowErr, "WORKFLOW_RUN_CREATE_FAILED")
		}
		if validationErr := validateGORMWorkflowJSONB(request.FirstNode.Input); validationErr != nil {
			return validationErr
		}
		statement := transaction.Exec(`
			INSERT INTO workflow.node_run(
				id,run_id,node_key,node_type,status,attempt,input,
				idempotency_key,input_schema_version,output_schema_version,dispatch_no,
				version,created_at,updated_at
			) VALUES(?,?,?,?,?,?,?::jsonb,?,?,?,?,?,?,?)`,
			string(request.FirstNode.ID), string(persisted.ID), request.FirstNode.NodeKey, request.FirstNode.NodeType,
			string(request.FirstNode.Status), request.FirstNode.Attempt, gormWorkflowJSONBArgument(request.FirstNode.Input),
			nullableGORMWorkflowString(request.FirstNode.IdempotencyKey), nullableGORMWorkflowInt(request.FirstNode.InputSchemaVersion),
			nullableGORMWorkflowInt(request.FirstNode.OutputSchemaVersion), nullableGORMWorkflowInt(request.FirstNode.DispatchNo),
			request.FirstNode.Version, request.FirstNode.CreatedAt.UTC(), request.FirstNode.UpdatedAt.UTC(),
		)
		if statement.Error != nil {
			return classifyGORMWorkflow(callbackCtx, statement.Error, "WORKFLOW_NODE_CREATE_FAILED")
		}
		if scopeErr := validateGORMEventScope(callbackCtx, transaction, request.Event, persisted.ID); scopeErr != nil {
			return scopeErr
		}
		if insertErr := insertGORMEvent(transaction, request.Event); insertErr != nil {
			return classifyGORMWorkflow(callbackCtx, insertErr, "WORKFLOW_OUTBOX_CREATE_FAILED")
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return persisted, nil
	}
	code := "WORKFLOW_START_TRANSACTION_FAILED"
	if callbackSucceeded {
		code = "WORKFLOW_START_COMMIT_FAILED"
	}
	return domain.Run{}, classifyGORMWorkflow(ctx, err, code)
}

// GetRun returns one durable workflow run.
func (repository *GORMRepository) GetRun(ctx context.Context, id foundation.ID) (domain.Run, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Run{}, err
	}
	run, err := scanGORMRun(repository.database.WithContext(ctx), gormRunSelect+` WHERE id=?`, string(id))
	if gormWorkflowNoRows(err) {
		return domain.Run{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUN_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Run{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	return run, nil
}

// GetPendingHumanTask returns the unique actionable Human Task for a Run.
func (repository *GORMRepository) GetPendingHumanTask(ctx context.Context, runID foundation.ID) (domain.HumanTask, bool, error) {
	var task domain.HumanTask
	count := 0
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		rows, rowsErr := gormWorkflowRawRows(transaction, `
			SELECT `+gormHumanColumns+`
			FROM workflow.human_task
			WHERE run_id=? AND status='pending'
			ORDER BY created_at,id
			LIMIT 2`, string(runID))
		if rowsErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowsErr, "WORKFLOW_HUMAN_TASK_QUERY_FAILED")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			current, scanErr := scanGORMHumanRow(rows)
			if scanErr != nil {
				return classifyGORMWorkflow(callbackCtx, joinGORMRowsError(rows, scanErr), "WORKFLOW_HUMAN_TASK_QUERY_FAILED")
			}
			task = current
			count++
		}
		if rowsErr = rows.Err(); rowsErr != nil {
			return classifyGORMWorkflow(callbackCtx, joinGORMRowsError(rows, rowsErr), "WORKFLOW_HUMAN_TASK_QUERY_FAILED")
		}
		if rowsErr = rows.Close(); rowsErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowsErr, "WORKFLOW_HUMAN_TASK_QUERY_FAILED")
		}
		if count > 1 {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_HUMAN_TASK_MULTIPLE_PENDING", false, errors.New("workflow run has multiple pending human tasks"))
		}
		return nil
	})
	if err != nil {
		return domain.HumanTask{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_HUMAN_TASK_QUERY_FAILED")
	}
	return task, count == 1, nil
}

// ListRuns returns one Workspace-bound, stably ordered page of Workflow Runs.
func (repository *GORMRepository) ListRuns(ctx context.Context, request domain.RunListQuery) ([]domain.RunListItem, bool, error) {
	if request.WorkspaceID == "" || request.Limit < 1 || request.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_INVALID", false, errors.New("invalid workflow list scope"))
	}

	items := make([]domain.RunListItem, 0, request.Limit+1)
	hasMore := false
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		query, arguments := buildGORMRunListQuery(request)
		rows, rowsErr := gormWorkflowRawRows(transaction, query, arguments...)
		if rowsErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowsErr, "WORKFLOW_LIST_QUERY_FAILED")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, workspaceID, definitionKey, status string
			var definitionVersion, version int64
			var createdAt, updatedAt time.Time
			var completedAt, pauseRequestedAt, cancelRequestedAt *time.Time
			var waitingForHuman bool
			if scanErr := rows.Scan(
				&id, &workspaceID, &definitionKey, &definitionVersion, &status, &version,
				&createdAt, &updatedAt, &completedAt, &pauseRequestedAt, &cancelRequestedAt, &waitingForHuman,
			); scanErr != nil {
				return classifyGORMWorkflow(callbackCtx, joinGORMRowsError(rows, scanErr), "WORKFLOW_LIST_SCAN_FAILED")
			}
			items = append(items, domain.RunListItem{
				Run: domain.Run{
					ID: foundation.ID(id), WorkspaceID: foundation.ID(workspaceID), Status: domain.RunStatus(status),
					Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt, CompletedAt: completedAt,
					PauseRequestedAt: pauseRequestedAt, CancelRequestedAt: cancelRequestedAt,
				},
				DefinitionKey: definitionKey, DefinitionVersion: definitionVersion, WaitingForHuman: waitingForHuman,
			})
		}
		if rowsErr = rows.Err(); rowsErr != nil {
			return classifyGORMWorkflow(callbackCtx, joinGORMRowsError(rows, rowsErr), "WORKFLOW_LIST_ROWS_FAILED")
		}
		if rowsErr = rows.Close(); rowsErr != nil {
			return classifyGORMWorkflow(callbackCtx, rowsErr, "WORKFLOW_LIST_ROWS_FAILED")
		}
		hasMore = len(items) > request.Limit
		if hasMore {
			items = items[:request.Limit]
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "WORKFLOW_LIST_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = "WORKFLOW_LIST_COMMIT_FAILED"
		}
		return nil, false, classifyGORMWorkflow(ctx, err, code)
	}
	return items, hasMore, nil
}

// ClaimNode leases a pending node or reclaims a node after its lease expired.
func (repository *GORMRepository) ClaimNode(ctx context.Context, id foundation.ID, owner string, now, until time.Time) (domain.NodeRun, error) {
	var node domain.NodeRun
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		var scanErr error
		node, scanErr = scanGORMNode(transaction, gormNodeSelect+`
			WHERE id=? AND (status='pending' OR (status='running' AND lease_until <= ?))
			FOR UPDATE`, string(id), now.UTC())
		if gormWorkflowNoRows(scanErr) {
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_NODE_NOT_CLAIMABLE", false, scanErr)
		}
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "WORKFLOW_NODE_CLAIM_FAILED")
		}
		if transitionErr := domain.ValidateTransition(node.Status, domain.StatusRunning); transitionErr != nil && node.Status != domain.StatusRunning {
			return transitionErr
		}
		node, scanErr = scanGORMNode(transaction, gormNodeUpdateReturning+` WHERE id=? RETURNING `+gormNodeColumns,
			string(domain.StatusRunning), node.Attempt+1, owner, until.UTC(), now.UTC(), node.Version+1, string(id),
		)
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "WORKFLOW_NODE_CLAIM_FAILED")
		}
		statement := transaction.Exec(`
			UPDATE workflow.run
			SET status='running',version=version+1,updated_at=?
			WHERE id=? AND status IN ('pending','running')`, now.UTC(), string(node.RunID))
		if statement.Error != nil {
			return classifyGORMWorkflow(callbackCtx, statement.Error, "WORKFLOW_RUN_START_FAILED")
		}
		if statement.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_RUN_NOT_RUNNABLE", false, errors.New("workflow run is not pending or running"))
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return node, nil
	}
	code := "WORKFLOW_CLAIM_TRANSACTION_FAILED"
	if callbackSucceeded {
		code = "WORKFLOW_CLAIM_COMMIT_FAILED"
	}
	return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, code)
}

// HeartbeatNode extends an owned, unexpired lease.
func (repository *GORMRepository) HeartbeatNode(ctx context.Context, id foundation.ID, owner string, now, until time.Time) (domain.NodeRun, error) {
	var node domain.NodeRun
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		var scanErr error
		node, scanErr = scanGORMNode(transaction, `
			UPDATE workflow.node_run
			SET lease_until=?,updated_at=?,version=version+1
			WHERE id=? AND status='running' AND lease_owner=? AND lease_until>?
			RETURNING `+gormNodeColumns,
			until.UTC(), now.UTC(), string(id), owner, now.UTC(),
		)
		if gormWorkflowNoRows(scanErr) {
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, scanErr)
		}
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "WORKFLOW_HEARTBEAT_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_HEARTBEAT_FAILED")
	}
	return node, nil
}

// CompleteNode completes a valid lease once; the same output is idempotent.
func (repository *GORMRepository) CompleteNode(ctx context.Context, completion domain.Completion) (domain.NodeRun, error) {
	var node domain.NodeRun
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		var scanErr error
		node, scanErr = scanGORMNode(transaction, gormNodeSelect+` WHERE id=? FOR UPDATE`, string(completion.NodeID))
		if gormWorkflowNoRows(scanErr) {
			return foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, scanErr)
		}
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "WORKFLOW_NODE_COMPLETE_FAILED")
		}
		if node.Status == domain.StatusSucceeded {
			if !jsonEqual(node.Output, completion.Output) {
				return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_NODE_ALREADY_COMPLETED", false, errors.New("completed output differs"))
			}
			return errGORMWorkflowCompletionReplay
		}
		if node.Status != domain.StatusRunning || node.LeaseOwner != completion.LeaseOwner || node.LeaseUntil == nil || !node.LeaseUntil.After(completion.At) {
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("node lease is not valid"))
		}
		if transitionErr := domain.ValidateTransition(node.Status, domain.StatusSucceeded); transitionErr != nil {
			return transitionErr
		}
		if scopeErr := validateGORMEventScope(callbackCtx, transaction, completion.Event, node.RunID); scopeErr != nil {
			return scopeErr
		}
		if validationErr := validateGORMWorkflowJSONB(completion.Output); validationErr != nil {
			return validationErr
		}
		node, scanErr = scanGORMNode(transaction, `
			UPDATE workflow.node_run
			SET status='succeeded',output=?::jsonb,lease_owner=NULL,lease_until=NULL,
				completed_at=?,updated_at=?,version=version+1
			WHERE id=?
			RETURNING `+gormNodeColumns,
			gormWorkflowJSONBArgument(completion.Output), completion.At.UTC(), completion.At.UTC(), string(completion.NodeID),
		)
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "WORKFLOW_NODE_COMPLETE_FAILED")
		}
		if insertErr := insertGORMEvent(transaction, completion.Event); insertErr != nil {
			return classifyGORMWorkflow(callbackCtx, insertErr, "WORKFLOW_OUTBOX_CREATE_FAILED")
		}
		callbackSucceeded = true
		return nil
	})
	if errors.Is(err, errGORMWorkflowCompletionReplay) {
		return node, nil
	}
	if err == nil {
		return node, nil
	}
	code := "WORKFLOW_COMPLETE_TRANSACTION_FAILED"
	if callbackSucceeded {
		code = "WORKFLOW_COMPLETE_COMMIT_FAILED"
	}
	return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, code)
}

// CreateHumanTask atomically waits the node and run for a human decision.
func (repository *GORMRepository) CreateHumanTask(ctx context.Context, task domain.HumanTask, event domain.OutboxEvent, now time.Time) (domain.HumanTask, error) {
	var persisted domain.HumanTask
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		if scopeErr := validateGORMEventScope(callbackCtx, transaction, event, task.RunID); scopeErr != nil {
			return scopeErr
		}
		statement := transaction.Exec(`
			UPDATE workflow.node_run
			SET status='waiting_for_human',lease_owner=NULL,lease_until=NULL,updated_at=?,version=version+1
			WHERE id=? AND run_id=? AND status='running'`, now.UTC(), string(task.NodeRunID), string(task.RunID))
		if statement.Error != nil {
			return classifyGORMWorkflow(callbackCtx, statement.Error, "HUMAN_TASK_NODE_UPDATE_FAILED")
		}
		if statement.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_NODE_NOT_RUNNING", false, errors.New("node is not running"))
		}
		statement = transaction.Exec(`
			UPDATE workflow.run
			SET status='waiting_for_human',updated_at=?,version=version+1
			WHERE id=? AND status='running'`, now.UTC(), string(task.RunID))
		if statement.Error != nil {
			return classifyGORMWorkflow(callbackCtx, statement.Error, "HUMAN_TASK_RUN_UPDATE_FAILED")
		}
		if statement.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_RUN_NOT_RUNNING", false, errors.New("run is not running"))
		}
		if validationErr := validateGORMWorkflowJSONB(task.ExpectedInputSchema); validationErr != nil {
			return validationErr
		}
		var scanErr error
		persisted, scanErr = scanGORMHuman(transaction, `
			INSERT INTO workflow.human_task(
				id,run_id,node_run_id,status,expected_input_schema,target_version,expires_at,created_at
			) VALUES(?,?,?,?,?::jsonb,?,?,?)
			RETURNING `+gormHumanColumns,
			string(task.ID), string(task.RunID), string(task.NodeRunID), string(task.Status),
			gormWorkflowJSONBArgument(task.ExpectedInputSchema), task.TargetVersion, task.ExpiresAt, task.CreatedAt.UTC(),
		)
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "HUMAN_TASK_CREATE_FAILED")
		}
		if insertErr := insertGORMEvent(transaction, event); insertErr != nil {
			return classifyGORMWorkflow(callbackCtx, insertErr, "WORKFLOW_OUTBOX_CREATE_FAILED")
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return persisted, nil
	}
	code := "HUMAN_TASK_TRANSACTION_FAILED"
	if callbackSucceeded {
		code = "HUMAN_TASK_COMMIT_FAILED"
	}
	return domain.HumanTask{}, classifyGORMWorkflow(ctx, err, code)
}

// SubmitHumanTask accepts an unexpired target version exactly once.
func (repository *GORMRepository) SubmitHumanTask(ctx context.Context, id foundation.ID, targetVersion int64, decision json.RawMessage, now time.Time, event domain.OutboxEvent) (domain.HumanTask, error) {
	var persisted domain.HumanTask
	callbackSucceeded := false
	expired := false
	commitCode := "HUMAN_SUBMIT_COMMIT_FAILED"
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		current, scanErr := scanGORMHuman(transaction, `
			SELECT `+gormHumanColumns+`
			FROM workflow.human_task
			WHERE id=?
			FOR UPDATE`, string(id))
		if gormWorkflowNoRows(scanErr) {
			return foundation.NewError(foundation.ErrorNotFound, "HUMAN_TASK_NOT_FOUND", false, scanErr)
		}
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "HUMAN_TASK_QUERY_FAILED")
		}
		if current.Status != domain.HumanTaskPending {
			return foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_ALREADY_RESOLVED", false, errors.New("human task is not pending"))
		}
		if current.TargetVersion != targetVersion {
			return foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_VERSION_CONFLICT", false, errors.New("target version differs"))
		}
		if current.ExpiresAt != nil && !current.ExpiresAt.After(now) {
			statement := transaction.Exec(`UPDATE workflow.human_task SET status='expired' WHERE id=?`, string(id))
			if statement.Error != nil {
				return classifyGORMWorkflow(callbackCtx, statement.Error, "HUMAN_TASK_EXPIRE_FAILED")
			}
			expired = true
			commitCode = "HUMAN_TASK_EXPIRE_FAILED"
			callbackSucceeded = true
			return nil
		}
		if scopeErr := validateGORMEventScope(callbackCtx, transaction, event, current.RunID); scopeErr != nil {
			return scopeErr
		}
		if validationErr := validateGORMWorkflowJSONB(decision); validationErr != nil {
			return validationErr
		}
		persisted, scanErr = scanGORMHuman(transaction, `
			UPDATE workflow.human_task
			SET status='submitted',decision=?::jsonb,submitted_at=?
			WHERE id=?
			RETURNING `+gormHumanColumns,
			gormWorkflowJSONBArgument(decision), now.UTC(), string(id),
		)
		if scanErr != nil {
			return classifyGORMWorkflow(callbackCtx, scanErr, "HUMAN_TASK_SUBMIT_FAILED")
		}
		statement := transaction.Exec(`
			UPDATE workflow.node_run
			SET status='pending',updated_at=?,version=version+1
			WHERE id=? AND status='waiting_for_human'`, now.UTC(), string(current.NodeRunID))
		if statement.Error != nil {
			return classifyGORMWorkflow(callbackCtx, statement.Error, "HUMAN_TASK_NODE_RESUME_FAILED")
		}
		if statement.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_NODE_STATE_INVALID", false, errors.New("human node is not waiting"))
		}
		statement = transaction.Exec(`
			UPDATE workflow.run
			SET status='running',updated_at=?,version=version+1
			WHERE id=? AND status='waiting_for_human'`, now.UTC(), string(current.RunID))
		if statement.Error != nil {
			return classifyGORMWorkflow(callbackCtx, statement.Error, "HUMAN_TASK_RUN_RESUME_FAILED")
		}
		if statement.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_RUN_STATE_INVALID", false, errors.New("human run is not waiting"))
		}
		if insertErr := insertGORMEvent(transaction, event); insertErr != nil {
			return classifyGORMWorkflow(callbackCtx, insertErr, "WORKFLOW_OUTBOX_CREATE_FAILED")
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "HUMAN_SUBMIT_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = commitCode
		}
		return domain.HumanTask{}, classifyGORMWorkflow(ctx, err, code)
	}
	if expired {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_EXPIRED", false, errors.New("human task expired"))
	}
	return persisted, nil
}

const gormRunColumns = `id::text,workspace_id::text,definition_id::text,status,input::text,output::text,idempotency_key,request_hash,version,created_at,updated_at,completed_at,pause_requested_at,cancel_requested_at`
const gormRunSelect = `SELECT ` + gormRunColumns + ` FROM workflow.run`
const gormNodeColumns = `id::text,run_id::text,node_key,node_type,status,attempt,input::text,output::text,idempotency_key,input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at,completed_at`
const gormNodeSelect = `SELECT ` + gormNodeColumns + ` FROM workflow.node_run`
const gormNodeUpdateReturning = `UPDATE workflow.node_run SET status=?,attempt=?,lease_owner=?,lease_until=?,updated_at=?,version=?`
const gormHumanColumns = `id::text,run_id::text,node_run_id::text,status,expected_input_schema::text,target_version,decision::text,expires_at,submitted_at,created_at`

func buildGORMRunListQuery(request domain.RunListQuery) (string, []any) {
	query := `SELECT r.id::text,r.workspace_id::text,d.key,d.version,r.status,r.version,r.created_at,r.updated_at,r.completed_at,r.pause_requested_at,r.cancel_requested_at,EXISTS(SELECT 1 FROM workflow.human_task h WHERE h.run_id=r.id AND h.status='pending') FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id WHERE r.workspace_id=?`
	arguments := []any{string(request.WorkspaceID)}
	if request.Status != "" {
		query += ` AND r.status=?`
		arguments = append(arguments, string(request.Status))
	}
	if request.CursorTime != nil {
		query += ` AND (r.updated_at,r.id)<(?,?)`
		arguments = append(arguments, request.CursorTime.UTC(), string(request.CursorID))
	}
	query += ` ORDER BY r.updated_at DESC,r.id DESC LIMIT ?`
	arguments = append(arguments, request.Limit+1)
	return query, arguments
}

func insertGORMRun(database *gorm.DB, run domain.Run) (domain.Run, error) {
	if err := validateGORMWorkflowJSONB(run.Input); err != nil {
		return domain.Run{}, err
	}
	return scanGORMRun(database, `
		INSERT INTO workflow.run(
			id,workspace_id,definition_id,status,input,idempotency_key,request_hash,version,created_at,updated_at
		)
		VALUES(?,?,?,?,?::jsonb,?,?,?,?,?)
		RETURNING `+gormRunColumns,
		string(run.ID), string(run.WorkspaceID), string(run.DefinitionID), string(run.Status),
		gormWorkflowJSONBArgument(run.Input), nullableGORMWorkflowString(run.IdempotencyKey),
		nullableGORMWorkflowString(run.RequestHash), run.Version, run.CreatedAt.UTC(), run.UpdatedAt.UTC(),
	)
}

func insertGORMEvent(database *gorm.DB, event domain.OutboxEvent) error {
	if err := validateGORMWorkflowJSONB(event.Payload); err != nil {
		return err
	}
	var runID any
	if event.RunID != nil {
		runID = string(*event.RunID)
	}
	return database.Exec(`
		INSERT INTO workflow.outbox_event(
			id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at
		) VALUES(?,?,?,?,?,?,?,?,?::jsonb,?)`,
		string(event.ID), string(event.WorkspaceID), runID, event.Type, event.IdempotencyKey,
		nullableGORMWorkflowString(event.EventKey), nullableGORMWorkflowInt(event.SchemaVersion),
		nullableGORMWorkflowInt64(event.EventVersion), gormWorkflowJSONBArgument(event.Payload), event.OccurredAt.UTC(),
	).Error
}

func validateGORMEventScope(ctx context.Context, database *gorm.DB, event domain.OutboxEvent, expectedRunID foundation.ID) error {
	if event.RunID == nil || *event.RunID != expectedRunID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_EVENT_SCOPE_INVALID", false, errors.New("outbox event run does not match workflow state"))
	}
	row, err := gormWorkflowRawRow(database, `SELECT workspace_id::text FROM workflow.run WHERE id=?`, string(expectedRunID))
	if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_EVENT_SCOPE_QUERY_FAILED")
	}
	var workspaceID string
	if err = row.Scan(&workspaceID); err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_EVENT_SCOPE_QUERY_FAILED")
	}
	if foundation.ID(workspaceID) != event.WorkspaceID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_EVENT_SCOPE_INVALID", false, errors.New("outbox event workspace does not match workflow state"))
	}
	return nil
}

func scanGORMRun(database *gorm.DB, query string, arguments ...any) (domain.Run, error) {
	row, err := gormWorkflowRawRow(database, query, arguments...)
	if err != nil {
		return domain.Run{}, err
	}
	return scanGORMStartRun(row)
}

func scanGORMNode(database *gorm.DB, query string, arguments ...any) (domain.NodeRun, error) {
	row, err := gormWorkflowRawRow(database, query, arguments...)
	if err != nil {
		return domain.NodeRun{}, err
	}
	return scanGORMStartNode(row)
}

func scanGORMHuman(database *gorm.DB, query string, arguments ...any) (domain.HumanTask, error) {
	row, err := gormWorkflowRawRow(database, query, arguments...)
	if err != nil {
		return domain.HumanTask{}, err
	}
	return scanGORMHumanRow(row)
}

func scanGORMHumanRow(row workflowRowScanner) (domain.HumanTask, error) {
	var task domain.HumanTask
	var id, runID, nodeRunID, status string
	var expectedInput workflowJSONB
	var decision sql.NullString
	var expiresAt, submittedAt sql.NullTime
	if err := row.Scan(
		&id, &runID, &nodeRunID, &status, &expectedInput, &task.TargetVersion,
		&decision, &expiresAt, &submittedAt, &task.CreatedAt,
	); err != nil {
		return domain.HumanTask{}, err
	}
	task.ID = foundation.ID(id)
	task.RunID = foundation.ID(runID)
	task.NodeRunID = foundation.ID(nodeRunID)
	task.Status = domain.HumanTaskStatus(status)
	task.ExpectedInputSchema = append(json.RawMessage(nil), expectedInput...)
	if decision.Valid {
		parsed, err := gormRuntimeStartJSON(decision.String, "WORKFLOW_HUMAN_TASK_ROW_INVALID")
		if err != nil {
			return domain.HumanTask{}, err
		}
		task.Decision = parsed
	}
	if expiresAt.Valid {
		value := expiresAt.Time.UTC()
		task.ExpiresAt = &value
	}
	if submittedAt.Valid {
		value := submittedAt.Time.UTC()
		task.SubmittedAt = &value
	}
	return task, nil
}

func gormWorkflowJSONBArgument(value json.RawMessage) any {
	if value == nil {
		return nil
	}
	return workflowJSONB(value)
}

func nullableGORMWorkflowString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableGORMWorkflowInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableGORMWorkflowInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func validateGORMWorkflowJSONB(value json.RawMessage) error {
	if value != nil && !json.Valid(value) {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DATA_INVALID", false, errors.New("workflow JSON value is invalid"))
	}
	return nil
}

func joinGORMRowsError(rows *sql.Rows, cause error) error {
	if rows == nil {
		return cause
	}
	if rowsErr := rows.Err(); rowsErr != nil && !errors.Is(cause, rowsErr) {
		cause = errors.Join(cause, rowsErr)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return errors.Join(cause, closeErr)
	}
	return cause
}

var _ domain.Repository = (*GORMRepository)(nil)
var _ domain.RunListRepository = (*GORMRepository)(nil)
var _ domain.PendingHumanTaskRepository = (*GORMRepository)(nil)
