package workflowpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

const gormStartRunColumns = `id::text,workspace_id::text,definition_id::text,status,input::text,output::text,idempotency_key,request_hash,version,created_at,updated_at,completed_at,pause_requested_at,cancel_requested_at`
const gormStartRunSelect = `SELECT ` + gormStartRunColumns + ` FROM workflow.run`
const gormStartNodeColumns = `id::text,run_id::text,node_key,node_type,status,attempt,input::text,output::text,idempotency_key,input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at,completed_at`
const gormStartNodeSelect = `SELECT ` + gormStartNodeColumns + ` FROM workflow.node_run`

type gormRuntimeStartScanner interface {
	Scan(...any) error
}

// Start 在一个平台 UoW 中原子创建或重放 Definition、Run、根 Node、Outbox 和 River Job。
func (repository *GORMRuntimeRepository) Start(ctx context.Context, request application.RuntimeStartRequest) (application.RuntimeStartResult, error) {
	if err := validateGORMRuntimeStartRequest(request); err != nil {
		return application.RuntimeStartResult{}, err
	}
	if err := repository.ready(ctx); err != nil {
		return application.RuntimeStartResult{}, err
	}

	var result application.RuntimeStartResult
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		var startErr error
		result, startErr = repository.startGORMScoped(callbackCtx, transaction, scope, request)
		if startErr != nil {
			return startErr
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if !callbackSucceeded {
		return application.RuntimeStartResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_TRANSACTION_FAILED")
	}

	event := request.Event
	event.RunID = &result.Run.ID
	recovered, found, recoveryErr := repository.recoverCommittedGORMStart(ctx, result.Run, result.FirstNode, event, result.Job, result.Replayed)
	commitErr := classifyGORMWorkflow(ctx, err, "WORKFLOW_START_COMMIT_FAILED")
	if recoveryErr != nil {
		return application.RuntimeStartResult{}, errors.Join(commitErr, recoveryErr)
	}
	if found {
		return recovered, nil
	}
	return application.RuntimeStartResult{}, commitErr
}

// StartScoped 在 caller-owned live scope 中创建或重放 Start 事实，不提交或回滚事务。
func (repository *GORMRuntimeRepository) StartScoped(ctx context.Context, scope foundation.TransactionScope, request application.RuntimeStartRequest) (application.RuntimeStartResult, error) {
	if err := validateGORMRuntimeStartRequest(request); err != nil {
		return application.RuntimeStartResult{}, err
	}
	if err := repository.ready(ctx); err != nil {
		return application.RuntimeStartResult{}, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return application.RuntimeStartResult{}, gormWorkflowUnavailable("WORKFLOW_START_TRANSACTION_UNAVAILABLE", err)
	}
	return repository.startGORMScoped(ctx, transaction.WithContext(ctx), scope, request)
}

func validateGORMRuntimeStartRequest(request application.RuntimeStartRequest) error {
	if err := validateRuntimeStartRequest(request); err != nil {
		return err
	}
	if len(request.Run.Input) == 0 || !json.Valid(request.Run.Input) ||
		len(request.FirstNode.Input) == 0 || !json.Valid(request.FirstNode.Input) ||
		!objectBytes(request.Event.Payload) {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DATA_INVALID", false, errors.New("workflow start JSON facts are invalid"))
	}
	return nil
}

func (repository *GORMRuntimeRepository) startGORMScoped(ctx context.Context, transaction *gorm.DB, scope foundation.TransactionScope, request application.RuntimeStartRequest) (application.RuntimeStartResult, error) {
	// All initial Runtime facts share one database-owned timestamp.
	databaseTimestamp, err := gormRuntimeStartDatabaseNow(ctx, transaction)
	if err != nil {
		return application.RuntimeStartResult{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	request.Definition.CreatedAt = databaseTimestamp
	request.Run.CreatedAt = databaseTimestamp
	request.Run.UpdatedAt = databaseTimestamp
	request.FirstNode.CreatedAt = databaseTimestamp
	request.FirstNode.UpdatedAt = databaseTimestamp
	request.Event.OccurredAt = databaseTimestamp

	definitionID, err := upsertGORMRuntimeDefinition(ctx, transaction, request)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	if err := rejectGORMActiveLegacyRun(ctx, transaction, request.Run.WorkspaceID, definitionID); err != nil {
		return application.RuntimeStartResult{}, err
	}

	runCandidate := request.Run
	runCandidate.DefinitionID = definitionID
	run, replayed, err := insertOrReplayGORMRuntimeRun(ctx, transaction, runCandidate, request.RequestHash)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	nodeCandidate := request.FirstNode
	nodeCandidate.RunID = run.ID
	node, err := insertOrReplayGORMRuntimeNode(ctx, transaction, nodeCandidate, replayed)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	eventCandidate := request.Event
	eventCandidate.RunID = &run.ID
	if err := insertOrReplayGORMRuntimeEvent(ctx, transaction, eventCandidate, replayed); err != nil {
		return application.RuntimeStartResult{}, err
	}

	args, err := riveradapter.NewNodeJobArgs(node.ID, node.DispatchNo)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	receipt, err := repository.jobs.InsertTx(ctx, scope, args, riveradapter.InsertOptions{})
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	if replayed && !receipt.Duplicate {
		return application.RuntimeStartResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_JOB_MISSING", false, errors.New("replayed workflow facts had no existing River job"))
	}
	if !replayed && receipt.Duplicate {
		return application.RuntimeStartResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_JOB_BINDING_CONFLICT", false, errors.New("new workflow facts resolved to an existing River job"))
	}
	return application.RuntimeStartResult{Run: run, FirstNode: node, Job: receipt, Replayed: replayed}, nil
}

func gormRuntimeStartDatabaseNow(ctx context.Context, transaction *gorm.DB) (time.Time, error) {
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

func upsertGORMRuntimeDefinition(ctx context.Context, transaction *gorm.DB, request application.RuntimeStartRequest) (foundation.ID, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES(?,?,?,?,?::jsonb,?)
ON CONFLICT(workspace_id,key,version) DO NOTHING
RETURNING id::text,graph::text`, string(request.Definition.ID), string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version, workflowJSONB(request.Definition.Graph), request.Definition.CreatedAt.UTC())
	if err != nil {
		return "", classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_CREATE_FAILED")
	}
	var id string
	var graph workflowJSONB
	inserted := true
	err = row.Scan(&id, &graph)
	if gormWorkflowNoRows(err) {
		inserted = false
		row, err = gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT id::text,graph::text
FROM workflow.definition WHERE workspace_id=? AND key=? AND version=? FOR UPDATE`, string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version)
		if err == nil {
			err = row.Scan(&id, &graph)
		}
	}
	if err != nil {
		return "", classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_CREATE_FAILED")
	}
	if inserted && foundation.ID(id) != request.Definition.ID {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_BINDING_INVALID", false, errors.New("created definition identity differs"))
	}
	if !jsonEqual(graph, request.Definition.Graph) {
		return "", foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_VERSION_CONFLICT", false, errors.New("registered definition graph differs"))
	}
	return foundation.ID(id), nil
}

func rejectGORMActiveLegacyRun(ctx context.Context, transaction *gorm.DB, workspaceID, definitionID foundation.ID) error {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT id::text FROM workflow.run
WHERE workspace_id=? AND definition_id=?
  AND status NOT IN ('succeeded','failed','cancelled')
  AND (idempotency_key IS NULL OR request_hash IS NULL)
ORDER BY created_at,id LIMIT 1 FOR UPDATE`, string(workspaceID), string(definitionID))
	if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_LEGACY_RUNTIME_QUERY_FAILED")
	}
	var legacyID string
	if err := row.Scan(&legacyID); gormWorkflowNoRows(err) {
		return nil
	} else if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_LEGACY_RUNTIME_QUERY_FAILED")
	}
	return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED", false, errors.New("active legacy workflow run lacks runtime identity"))
}

func insertOrReplayGORMRuntimeRun(ctx context.Context, transaction *gorm.DB, candidate domain.Run, requestHash string) (domain.Run, bool, error) {
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash)
VALUES(?,?,?,?,?::jsonb,?,?,?,?,?)
ON CONFLICT(workspace_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING `+gormStartRunColumns, string(candidate.ID), string(candidate.WorkspaceID), string(candidate.DefinitionID), string(candidate.Status), workflowJSONB(candidate.Input), candidate.Version, candidate.CreatedAt.UTC(), candidate.UpdatedAt.UTC(), candidate.IdempotencyKey, candidate.RequestHash)
	if err != nil {
		return domain.Run{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_CREATE_FAILED")
	}
	run, scanErr := scanGORMStartRun(row)
	replayed := false
	if gormWorkflowNoRows(scanErr) {
		replayed = true
		row, err = gormWorkflowRawRow(transaction.WithContext(ctx), gormStartRunSelect+` WHERE workspace_id=? AND idempotency_key=? FOR UPDATE`, string(candidate.WorkspaceID), candidate.IdempotencyKey)
		if err == nil {
			run, scanErr = scanGORMStartRun(row)
		}
	}
	if err != nil {
		return domain.Run{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_CREATE_FAILED")
	}
	if scanErr != nil {
		return domain.Run{}, false, classifyGORMWorkflow(ctx, scanErr, "WORKFLOW_RUN_CREATE_FAILED")
	}
	if (!replayed && run.ID != candidate.ID) || run.WorkspaceID != candidate.WorkspaceID || run.DefinitionID != candidate.DefinitionID ||
		run.IdempotencyKey != candidate.IdempotencyKey || !strings.EqualFold(run.RequestHash, requestHash) || !jsonEqual(run.Input, candidate.Input) {
		return domain.Run{}, false, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_START_IDEMPOTENCY_CONFLICT", false, errors.New("workflow start idempotency binding differs"))
	}
	return run, replayed, nil
}

func insertOrReplayGORMRuntimeNode(ctx context.Context, transaction *gorm.DB, candidate domain.NodeRun, replayed bool) (domain.NodeRun, error) {
	if replayed {
		row, err := gormWorkflowRawRow(transaction.WithContext(ctx), gormStartNodeSelect+` WHERE run_id=? AND idempotency_key=? FOR UPDATE`, string(candidate.RunID), candidate.IdempotencyKey)
		if err != nil {
			return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_QUERY_FAILED")
		}
		node, scanErr := scanGORMStartNode(row)
		if gormWorkflowNoRows(scanErr) {
			return domain.NodeRun{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_NODE_MISSING", false, scanErr)
		}
		if scanErr != nil {
			return domain.NodeRun{}, classifyGORMWorkflow(ctx, scanErr, "WORKFLOW_NODE_QUERY_FAILED")
		}
		if err := validateRuntimeNodeReplay(node, candidate); err != nil {
			return domain.NodeRun{}, err
		}
		return node, nil
	}

	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,idempotency_key,input_schema_version,output_schema_version,dispatch_no)
VALUES(?,?,?,?,?,?,?::jsonb,?,?,?,?,?,?,?)
RETURNING `+gormStartNodeColumns, string(candidate.ID), string(candidate.RunID), candidate.NodeKey, candidate.NodeType, string(candidate.Status), candidate.Attempt, workflowJSONB(candidate.Input), candidate.Version, candidate.CreatedAt.UTC(), candidate.UpdatedAt.UTC(), candidate.IdempotencyKey, candidate.InputSchemaVersion, candidate.OutputSchemaVersion, candidate.DispatchNo)
	if err != nil {
		return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_CREATE_FAILED")
	}
	node, err := scanGORMStartNode(row)
	if err != nil {
		return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_NODE_CREATE_FAILED")
	}
	if node.ID != candidate.ID {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_NODE_BINDING_CONFLICT", false, errors.New("created root node identity differs"))
	}
	if err := validateRuntimeNodeReplay(node, candidate); err != nil {
		return domain.NodeRun{}, err
	}
	return node, nil
}

func insertOrReplayGORMRuntimeEvent(ctx context.Context, transaction *gorm.DB, event domain.OutboxEvent, replayed bool) error {
	if replayed {
		row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT id::text,run_id::text,event_type,idempotency_key,payload::text,schema_version,event_version
FROM workflow.outbox_event WHERE workspace_id=? AND event_key=? AND event_version=? FOR UPDATE`, string(event.WorkspaceID), event.EventKey, event.EventVersion)
		if err != nil {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_OUTBOX_QUERY_FAILED")
		}
		var id, eventType, idempotencyKey string
		var runID sql.NullString
		var payload workflowJSONB
		var schemaVersion int
		var eventVersion int64
		if err := row.Scan(&id, &runID, &eventType, &idempotencyKey, &payload, &schemaVersion, &eventVersion); gormWorkflowNoRows(err) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_OUTBOX_MISSING", false, err)
		} else if err != nil {
			return classifyGORMWorkflow(ctx, err, "WORKFLOW_OUTBOX_QUERY_FAILED")
		}
		if event.RunID == nil || !runID.Valid || foundation.ID(runID.String) != *event.RunID || eventType != event.Type ||
			idempotencyKey != event.IdempotencyKey || schemaVersion != event.SchemaVersion || eventVersion != event.EventVersion || !jsonEqual(payload, event.Payload) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_OUTBOX_BINDING_CONFLICT", false, errors.New("replayed outbox binding differs"))
		}
		return nil
	}

	if err := validateGORMRuntimeEventScope(ctx, transaction, event, *event.RunID); err != nil {
		return err
	}
	statement := transaction.WithContext(ctx).Exec(`INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at)
VALUES(?,?,?,?,?,?,?,?,?::jsonb,?)`, string(event.ID), string(event.WorkspaceID), string(*event.RunID), event.Type, event.IdempotencyKey, event.EventKey, event.SchemaVersion, event.EventVersion, workflowJSONB(event.Payload), event.OccurredAt.UTC())
	if statement.Error != nil {
		return classifyGORMWorkflow(ctx, statement.Error, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	if statement.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_OUTBOX_CREATE_FAILED", false, errors.New("workflow outbox insert affected an unexpected number of rows"))
	}
	return nil
}

func validateGORMRuntimeEventScope(ctx context.Context, transaction *gorm.DB, event domain.OutboxEvent, expectedRunID foundation.ID) error {
	if event.RunID == nil || *event.RunID != expectedRunID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_EVENT_SCOPE_INVALID", false, errors.New("outbox event run does not match workflow state"))
	}
	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT workspace_id::text FROM workflow.run WHERE id=?`, string(expectedRunID))
	if err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_EVENT_SCOPE_QUERY_FAILED")
	}
	var workspaceID string
	if err := row.Scan(&workspaceID); err != nil {
		return classifyGORMWorkflow(ctx, err, "WORKFLOW_EVENT_SCOPE_QUERY_FAILED")
	}
	if foundation.ID(workspaceID) != event.WorkspaceID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_EVENT_SCOPE_INVALID", false, errors.New("outbox event workspace does not match workflow state"))
	}
	return nil
}

func (repository *GORMRuntimeRepository) recoverCommittedGORMStart(ctx context.Context, expectedRun domain.Run, expectedNode domain.NodeRun, expectedEvent domain.OutboxEvent, receipt application.JobReceipt, replayed bool) (application.RuntimeStartResult, bool, error) {
	row, err := gormWorkflowRawRow(repository.database.WithContext(ctx), gormStartRunSelect+` WHERE workspace_id=? AND idempotency_key=?`, string(expectedRun.WorkspaceID), expectedRun.IdempotencyKey)
	if err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_QUERY_FAILED")
	}
	run, err := scanGORMStartRun(row)
	if gormWorkflowNoRows(err) {
		return application.RuntimeStartResult{}, false, nil
	}
	if err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_QUERY_FAILED")
	}
	if run.ID != expectedRun.ID || run.WorkspaceID != expectedRun.WorkspaceID || run.DefinitionID != expectedRun.DefinitionID ||
		run.IdempotencyKey != expectedRun.IdempotencyKey || run.RequestHash != expectedRun.RequestHash || !jsonEqual(run.Input, expectedRun.Input) {
		return application.RuntimeStartResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_RECOVERY_BINDING_CONFLICT", false, errors.New("recovered run binding differs"))
	}

	row, err = gormWorkflowRawRow(repository.database.WithContext(ctx), gormStartNodeSelect+` WHERE run_id=? AND idempotency_key=?`, string(run.ID), expectedNode.IdempotencyKey)
	if err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_NODE_FAILED")
	}
	node, err := scanGORMStartNode(row)
	if err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_NODE_FAILED")
	}
	if node.ID != expectedNode.ID {
		return application.RuntimeStartResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_RECOVERY_BINDING_CONFLICT", false, errors.New("recovered node identity differs"))
	}
	if err := validateRuntimeNodeReplay(node, expectedNode); err != nil {
		return application.RuntimeStartResult{}, false, err
	}

	row, err = gormWorkflowRawRow(repository.database.WithContext(ctx), `SELECT run_id::text,event_type,idempotency_key,payload::text,schema_version,event_version
FROM workflow.outbox_event WHERE workspace_id=? AND event_key=? AND event_version=?`, string(expectedEvent.WorkspaceID), expectedEvent.EventKey, expectedEvent.EventVersion)
	if err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_OUTBOX_FAILED")
	}
	var runID sql.NullString
	var eventType, idempotencyKey string
	var payload workflowJSONB
	var schemaVersion int
	var eventVersion int64
	if err := row.Scan(&runID, &eventType, &idempotencyKey, &payload, &schemaVersion, &eventVersion); err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_OUTBOX_FAILED")
	}
	if expectedEvent.RunID == nil || !runID.Valid || foundation.ID(runID.String) != *expectedEvent.RunID || eventType != expectedEvent.Type ||
		idempotencyKey != expectedEvent.IdempotencyKey || schemaVersion != expectedEvent.SchemaVersion || eventVersion != expectedEvent.EventVersion || !jsonEqual(payload, expectedEvent.Payload) {
		return application.RuntimeStartResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_RECOVERY_BINDING_CONFLICT", false, errors.New("recovered outbox binding differs"))
	}

	row, err = gormWorkflowRawRow(repository.database.WithContext(ctx), `SELECT id FROM workflow.river_job
WHERE id=? AND kind=? AND args->>'node_run_id'=? AND (args->>'dispatch_no')::integer=?`, receipt.JobID, riveradapter.NodeJobKind, string(node.ID), node.DispatchNo)
	if err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_JOB_FAILED")
	}
	var jobID int64
	if err := row.Scan(&jobID); err != nil {
		return application.RuntimeStartResult{}, false, classifyGORMWorkflow(ctx, err, "WORKFLOW_START_RECOVERY_JOB_FAILED")
	}
	return application.RuntimeStartResult{
		Run: run, FirstNode: node,
		Job: application.JobReceipt{JobID: jobID, Duplicate: receipt.Duplicate}, Replayed: replayed,
	}, true, nil
}

func scanGORMStartRun(row gormRuntimeStartScanner) (domain.Run, error) {
	var run domain.Run
	var id, workspaceID, definitionID, status string
	var input workflowJSONB
	var output, idempotencyKey, requestHash sql.NullString
	var completedAt, pauseRequestedAt, cancelRequestedAt sql.NullTime
	if err := row.Scan(&id, &workspaceID, &definitionID, &status, &input, &output, &idempotencyKey, &requestHash,
		&run.Version, &run.CreatedAt, &run.UpdatedAt, &completedAt, &pauseRequestedAt, &cancelRequestedAt); err != nil {
		return domain.Run{}, err
	}
	run.ID, run.WorkspaceID, run.DefinitionID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(definitionID)
	run.Status = domain.RunStatus(status)
	run.Input = append(json.RawMessage(nil), input...)
	if output.Valid {
		parsed, err := gormRuntimeStartJSON(output.String, "WORKFLOW_RUN_ROW_INVALID")
		if err != nil {
			return domain.Run{}, err
		}
		run.Output = parsed
	}
	if idempotencyKey.Valid {
		run.IdempotencyKey = idempotencyKey.String
	}
	if requestHash.Valid {
		run.RequestHash = requestHash.String
	}
	if completedAt.Valid {
		value := completedAt.Time
		run.CompletedAt = &value
	}
	if pauseRequestedAt.Valid {
		value := pauseRequestedAt.Time
		run.PauseRequestedAt = &value
	}
	if cancelRequestedAt.Valid {
		value := cancelRequestedAt.Time
		run.CancelRequestedAt = &value
	}
	return run, nil
}

func scanGORMStartNode(row gormRuntimeStartScanner) (domain.NodeRun, error) {
	var node domain.NodeRun
	var id, runID, status string
	var input workflowJSONB
	var output, idempotencyKey, leaseOwner sql.NullString
	var inputSchemaVersion, outputSchemaVersion, dispatchNo sql.NullInt64
	var leaseUntil, completedAt sql.NullTime
	if err := row.Scan(&id, &runID, &node.NodeKey, &node.NodeType, &status, &node.Attempt, &input, &output,
		&idempotencyKey, &inputSchemaVersion, &outputSchemaVersion, &dispatchNo, &leaseOwner, &leaseUntil,
		&node.Version, &node.CreatedAt, &node.UpdatedAt, &completedAt); err != nil {
		return domain.NodeRun{}, err
	}
	node.ID, node.RunID = foundation.ID(id), foundation.ID(runID)
	node.Status = domain.NodeStatus(status)
	node.Input = append(json.RawMessage(nil), input...)
	if output.Valid {
		parsed, err := gormRuntimeStartJSON(output.String, "WORKFLOW_NODE_ROW_INVALID")
		if err != nil {
			return domain.NodeRun{}, err
		}
		node.Output = parsed
	}
	if idempotencyKey.Valid {
		node.IdempotencyKey = idempotencyKey.String
	}
	if inputSchemaVersion.Valid {
		node.InputSchemaVersion = int(inputSchemaVersion.Int64)
	}
	if outputSchemaVersion.Valid {
		node.OutputSchemaVersion = int(outputSchemaVersion.Int64)
	}
	if dispatchNo.Valid {
		node.DispatchNo = int(dispatchNo.Int64)
	}
	if leaseOwner.Valid {
		node.LeaseOwner = leaseOwner.String
	}
	if leaseUntil.Valid {
		value := leaseUntil.Time
		node.LeaseUntil = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		node.CompletedAt = &value
	}
	return node, nil
}

func gormRuntimeStartJSON(value, code string) (json.RawMessage, error) {
	raw := []byte(value)
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New("persisted workflow JSON is invalid"))
	}
	return append(json.RawMessage(nil), raw...), nil
}

var _ application.RuntimeStarter = (*GORMRuntimeRepository)(nil)
var _ application.ScopedRuntimeStarter = (*GORMRuntimeRepository)(nil)
