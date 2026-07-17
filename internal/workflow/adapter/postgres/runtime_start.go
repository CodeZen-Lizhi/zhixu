package workflowpostgres

import (
	"context"
	encodinghex "encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

// RuntimeRepository owns the PostgreSQL + River transactional Start unit of work.
type RuntimeRepository struct {
	db   DB
	jobs riveradapter.JobInserter
}

// NewRuntimeRepository constructs the reusable registered Workflow Start adapter.
func NewRuntimeRepository(db DB, jobs riveradapter.JobInserter) (*RuntimeRepository, error) {
	if isNilDB(db) || isNilJobInserter(jobs) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_DATABASE_UNAVAILABLE", true, errors.New("runtime database or job inserter is nil"))
	}
	return &RuntimeRepository{db: db, jobs: jobs}, nil
}

func isNilJobInserter(inserter riveradapter.JobInserter) bool {
	if inserter == nil {
		return true
	}
	value := reflect.ValueOf(inserter)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// Start atomically creates or replays Definition, Run, root Node, Outbox and River Job.
func (r *RuntimeRepository) Start(ctx context.Context, request application.RuntimeStartRequest) (application.RuntimeStartResult, error) {
	if err := validateRuntimeStartRequest(request); err != nil {
		return application.RuntimeStartResult{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.RuntimeStartResult{}, classify(err, "WORKFLOW_START_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := r.startTx(ctx, tx, request)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		event := request.Event
		event.RunID = &result.Run.ID
		recovered, found, recoveryErr := r.recoverCommittedStart(ctx, result.Run, result.FirstNode, event, result.Job, result.Replayed)
		if recoveryErr != nil {
			return application.RuntimeStartResult{}, errors.Join(classify(err, "WORKFLOW_START_COMMIT_FAILED"), recoveryErr)
		}
		if found {
			return recovered, nil
		}
		return application.RuntimeStartResult{}, classify(err, "WORKFLOW_START_COMMIT_FAILED")
	}
	return result, nil
}

// StartTx creates or replays Definition, Run, root Node, Outbox and River Job
// in a caller-owned transaction. The caller exclusively owns commit/rollback.
func (r *RuntimeRepository) StartTx(ctx context.Context, tx pgx.Tx, request application.RuntimeStartRequest) (application.RuntimeStartResult, error) {
	if err := validateRuntimeStartRequest(request); err != nil {
		return application.RuntimeStartResult{}, err
	}
	if r == nil || isNilJobInserter(r.jobs) || isNilRuntimeTx(tx) {
		return application.RuntimeStartResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_START_TRANSACTION_UNAVAILABLE", true, errors.New("runtime repository, job inserter, or transaction is nil"))
	}
	return r.startTx(ctx, tx, request)
}

func isNilRuntimeTx(tx pgx.Tx) bool {
	if tx == nil {
		return true
	}
	value := reflect.ValueOf(tx)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (r *RuntimeRepository) startTx(ctx context.Context, tx pgx.Tx, request application.RuntimeStartRequest) (application.RuntimeStartResult, error) {
	// Runtime facts use one database-owned timestamp so a skewed caller clock
	// cannot make the first control/update violate created_at ordering.
	databaseTimestamp, err := databaseNow(ctx, tx)
	if err != nil {
		return application.RuntimeStartResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	request.Definition.CreatedAt = databaseTimestamp
	request.Run.CreatedAt = databaseTimestamp
	request.Run.UpdatedAt = databaseTimestamp
	request.FirstNode.CreatedAt = databaseTimestamp
	request.FirstNode.UpdatedAt = databaseTimestamp
	request.Event.OccurredAt = databaseTimestamp

	definitionID, err := upsertRuntimeDefinition(ctx, tx, request)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	if err := rejectActiveLegacyRun(ctx, tx, request.Run.WorkspaceID, definitionID); err != nil {
		return application.RuntimeStartResult{}, err
	}

	runCandidate := request.Run
	runCandidate.DefinitionID = definitionID
	run, replayed, err := insertOrReplayRuntimeRun(ctx, tx, runCandidate, request.RequestHash)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	nodeCandidate := request.FirstNode
	nodeCandidate.RunID = run.ID
	node, err := insertOrReplayRuntimeNode(ctx, tx, nodeCandidate, replayed)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	eventCandidate := request.Event
	eventCandidate.RunID = &run.ID
	if err := insertOrReplayRuntimeEvent(ctx, tx, eventCandidate, replayed); err != nil {
		return application.RuntimeStartResult{}, err
	}
	args, err := riveradapter.NewNodeJobArgs(node.ID, node.DispatchNo)
	if err != nil {
		return application.RuntimeStartResult{}, err
	}
	receipt, err := r.jobs.InsertTx(ctx, tx, args, riveradapter.InsertOptions{})
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

func (r *RuntimeRepository) recoverCommittedStart(ctx context.Context, expectedRun domain.Run, expectedNode domain.NodeRun, expectedEvent domain.OutboxEvent, receipt application.JobReceipt, replayed bool) (application.RuntimeStartResult, bool, error) {
	run, err := scanRun(r.db.QueryRow(ctx, runSelect+` WHERE workspace_id=$1 AND idempotency_key=$2`, string(expectedRun.WorkspaceID), expectedRun.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeStartResult{}, false, nil
	}
	if err != nil {
		return application.RuntimeStartResult{}, false, classify(err, "WORKFLOW_START_RECOVERY_QUERY_FAILED")
	}
	if run.ID != expectedRun.ID || run.DefinitionID != expectedRun.DefinitionID || run.RequestHash != expectedRun.RequestHash || !jsonEqual(run.Input, expectedRun.Input) {
		return application.RuntimeStartResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_RECOVERY_BINDING_CONFLICT", false, errors.New("recovered run binding differs"))
	}
	node, err := scanNode(r.db.QueryRow(ctx, nodeSelect+` WHERE run_id=$1 AND idempotency_key=$2`, string(run.ID), expectedNode.IdempotencyKey))
	if err != nil {
		return application.RuntimeStartResult{}, false, classify(err, "WORKFLOW_START_RECOVERY_NODE_FAILED")
	}
	if err := validateRuntimeNodeReplay(node, expectedNode); err != nil {
		return application.RuntimeStartResult{}, false, err
	}
	var eventType, idempotencyKey string
	var payload []byte
	var schemaVersion int
	var eventVersion int64
	err = r.db.QueryRow(ctx, `SELECT event_type,idempotency_key,payload,schema_version,event_version
FROM workflow.outbox_event WHERE workspace_id=$1 AND event_key=$2 AND event_version=$3`, string(expectedEvent.WorkspaceID), expectedEvent.EventKey, expectedEvent.EventVersion).Scan(&eventType, &idempotencyKey, &payload, &schemaVersion, &eventVersion)
	if err != nil {
		return application.RuntimeStartResult{}, false, classify(err, "WORKFLOW_START_RECOVERY_OUTBOX_FAILED")
	}
	if eventType != expectedEvent.Type || idempotencyKey != expectedEvent.IdempotencyKey || schemaVersion != expectedEvent.SchemaVersion || eventVersion != expectedEvent.EventVersion || !jsonEqual(payload, expectedEvent.Payload) {
		return application.RuntimeStartResult{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_RECOVERY_BINDING_CONFLICT", false, errors.New("recovered outbox binding differs"))
	}
	var jobID int64
	err = r.db.QueryRow(ctx, `SELECT id FROM workflow.river_job
WHERE id=$1 AND kind=$2 AND args->>'node_run_id'=$3 AND (args->>'dispatch_no')::integer=$4`, receipt.JobID, riveradapter.NodeJobKind, string(node.ID), node.DispatchNo).Scan(&jobID)
	if err != nil {
		return application.RuntimeStartResult{}, false, classify(err, "WORKFLOW_START_RECOVERY_JOB_FAILED")
	}
	return application.RuntimeStartResult{Run: run, FirstNode: node, Job: application.JobReceipt{JobID: jobID, Duplicate: receipt.Duplicate}, Replayed: replayed}, true, nil
}

func validateRuntimeStartRequest(request application.RuntimeStartRequest) error {
	if request.Definition.ID == "" || request.Definition.WorkspaceID == "" || strings.TrimSpace(request.Definition.Key) == "" || request.Definition.Version < 1 || request.DefinitionInputSchemaVersion < 1 || !objectBytes(request.Definition.Graph) || !validRuntimeHash(request.DefinitionGraphHash) {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_DEFINITION_INVALID", false, errors.New("runtime definition is invalid"))
	}
	if request.Run.ID == "" || request.Run.WorkspaceID != request.Definition.WorkspaceID || request.Run.IdempotencyKey == "" || request.Run.RequestHash == "" || request.RequestHash != request.Run.RequestHash || !validRuntimeHash(request.RequestHash) || request.Run.Status != domain.StatusPending {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_RUN_INVALID", false, errors.New("runtime run is invalid"))
	}
	if request.FirstNode.ID == "" || request.FirstNode.RunID != request.Run.ID || request.FirstNode.IdempotencyKey == "" || request.FirstNode.InputSchemaVersion < 1 || request.FirstNode.OutputSchemaVersion < 1 || request.FirstNode.DispatchNo != 1 || request.FirstNode.Status != domain.StatusPending {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_NODE_INVALID", false, errors.New("runtime node is invalid"))
	}
	if request.Event.ID == "" || request.Event.RunID == nil || *request.Event.RunID != request.Run.ID || request.Event.WorkspaceID != request.Run.WorkspaceID || request.Event.EventKey == "" || request.Event.SchemaVersion < 1 || request.Event.EventVersion < 1 {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_EVENT_INVALID", false, errors.New("runtime event is invalid"))
	}
	return nil
}

func upsertRuntimeDefinition(ctx context.Context, tx pgx.Tx, request application.RuntimeStartRequest) (foundation.ID, error) {
	var id string
	var graph []byte
	err := tx.QueryRow(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT(workspace_id,key,version) DO NOTHING
RETURNING id::text,graph`, string(request.Definition.ID), string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version, request.Definition.Graph, request.Definition.CreatedAt.UTC()).Scan(&id, &graph)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text,graph FROM workflow.definition WHERE workspace_id=$1 AND key=$2 AND version=$3 FOR UPDATE`, string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version).Scan(&id, &graph)
	}
	if err != nil {
		return "", classify(err, "WORKFLOW_DEFINITION_CREATE_FAILED")
	}
	if !jsonEqual(graph, request.Definition.Graph) {
		return "", foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_VERSION_CONFLICT", false, errors.New("registered definition graph differs"))
	}
	return foundation.ID(id), nil
}

func rejectActiveLegacyRun(ctx context.Context, tx pgx.Tx, workspaceID, definitionID foundation.ID) error {
	var legacyID *string
	err := tx.QueryRow(ctx, `SELECT id::text FROM workflow.run
WHERE workspace_id=$1 AND definition_id=$2
  AND status NOT IN ('succeeded','failed','cancelled')
  AND (idempotency_key IS NULL OR request_hash IS NULL)
ORDER BY created_at,id LIMIT 1 FOR UPDATE`, string(workspaceID), string(definitionID)).Scan(&legacyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return classify(err, "WORKFLOW_LEGACY_RUNTIME_QUERY_FAILED")
	}
	return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED", false, errors.New("active legacy workflow run lacks runtime identity"))
}

func insertOrReplayRuntimeRun(ctx context.Context, tx pgx.Tx, candidate domain.Run, requestHash string) (domain.Run, bool, error) {
	run, err := scanRun(tx.QueryRow(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT(workspace_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING `+runColumns, string(candidate.ID), string(candidate.WorkspaceID), string(candidate.DefinitionID), string(candidate.Status), candidate.Input, candidate.Version, candidate.CreatedAt.UTC(), candidate.UpdatedAt.UTC(), candidate.IdempotencyKey, candidate.RequestHash))
	replayed := false
	if errors.Is(err, pgx.ErrNoRows) {
		replayed = true
		run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(candidate.WorkspaceID), candidate.IdempotencyKey))
	}
	if err != nil {
		return domain.Run{}, false, classify(err, "WORKFLOW_RUN_CREATE_FAILED")
	}
	if run.WorkspaceID != candidate.WorkspaceID || run.DefinitionID != candidate.DefinitionID || run.IdempotencyKey != candidate.IdempotencyKey || !strings.EqualFold(run.RequestHash, requestHash) || !jsonEqual(run.Input, candidate.Input) {
		return domain.Run{}, false, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_START_IDEMPOTENCY_CONFLICT", false, errors.New("workflow start idempotency binding differs"))
	}
	return run, replayed, nil
}

func insertOrReplayRuntimeNode(ctx context.Context, tx pgx.Tx, candidate domain.NodeRun, replayed bool) (domain.NodeRun, error) {
	if replayed {
		node, err := scanNode(tx.QueryRow(ctx, nodeSelect+` WHERE run_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(candidate.RunID), candidate.IdempotencyKey))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NodeRun{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_NODE_MISSING", false, err)
		}
		if err != nil {
			return domain.NodeRun{}, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
		}
		if err := validateRuntimeNodeReplay(node, candidate); err != nil {
			return domain.NodeRun{}, err
		}
		return node, nil
	}
	node, err := scanNode(tx.QueryRow(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,idempotency_key,input_schema_version,output_schema_version,dispatch_no)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
RETURNING `+nodeColumns, string(candidate.ID), string(candidate.RunID), candidate.NodeKey, candidate.NodeType, string(candidate.Status), candidate.Attempt, candidate.Input, candidate.Version, candidate.CreatedAt.UTC(), candidate.UpdatedAt.UTC(), candidate.IdempotencyKey, candidate.InputSchemaVersion, candidate.OutputSchemaVersion, candidate.DispatchNo))
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_NODE_CREATE_FAILED")
	}
	return node, nil
}

func validateRuntimeNodeReplay(node, candidate domain.NodeRun) error {
	if node.RunID != candidate.RunID || node.NodeKey != candidate.NodeKey || node.NodeType != candidate.NodeType || node.IdempotencyKey != candidate.IdempotencyKey || node.InputSchemaVersion != candidate.InputSchemaVersion || node.OutputSchemaVersion != candidate.OutputSchemaVersion || node.DispatchNo != candidate.DispatchNo || !jsonEqual(node.Input, candidate.Input) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_NODE_BINDING_CONFLICT", false, errors.New("replayed root node binding differs"))
	}
	return nil
}

func insertOrReplayRuntimeEvent(ctx context.Context, tx pgx.Tx, event domain.OutboxEvent, replayed bool) error {
	if replayed {
		var id, eventType, idempotencyKey string
		var payload []byte
		var schemaVersion int
		var eventVersion int64
		err := tx.QueryRow(ctx, `SELECT id::text,event_type,idempotency_key,payload,schema_version,event_version
FROM workflow.outbox_event WHERE workspace_id=$1 AND event_key=$2 AND event_version=$3 FOR UPDATE`, string(event.WorkspaceID), event.EventKey, event.EventVersion).Scan(&id, &eventType, &idempotencyKey, &payload, &schemaVersion, &eventVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_OUTBOX_MISSING", false, err)
		}
		if err != nil {
			return classify(err, "WORKFLOW_OUTBOX_QUERY_FAILED")
		}
		if eventType != event.Type || idempotencyKey != event.IdempotencyKey || schemaVersion != event.SchemaVersion || eventVersion != event.EventVersion || !jsonEqual(payload, event.Payload) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_OUTBOX_BINDING_CONFLICT", false, errors.New("replayed outbox binding differs"))
		}
		return nil
	}
	if err := validateEventScope(ctx, tx, event, *event.RunID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(event.ID), string(event.WorkspaceID), string(*event.RunID), event.Type, event.IdempotencyKey, event.EventKey, event.SchemaVersion, event.EventVersion, event.Payload, event.OccurredAt.UTC())
	if err != nil {
		return classify(err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	return nil
}

func objectBytes(value []byte) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validRuntimeHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := encodinghex.DecodeString(value)
	return err == nil
}

var _ application.RuntimeStarter = (*RuntimeRepository)(nil)
