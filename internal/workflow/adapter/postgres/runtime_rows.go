package workflowpostgres

import (
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// workflowRowScanner 只解码 database/sql Row 或 Rows 的当前行，不拥有查询或连接生命周期。
type workflowRowScanner interface {
	Scan(...any) error
}

const runColumns = `id::text,workspace_id::text,definition_id::text,status,input,output,idempotency_key,request_hash,version,created_at,updated_at,completed_at,pause_requested_at,cancel_requested_at`

const nodeColumns = `id::text,run_id::text,node_key,node_type,status,attempt,input,output,idempotency_key,input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at,completed_at`

const humanColumns = `id::text,run_id::text,node_run_id::text,status,expected_input_schema,target_version,decision,expires_at,submitted_at,created_at`

const runtimeRunColumns = runColumns

const runtimeNodeColumns = nodeColumns + `,retry_no,next_attempt_at,failure_class,error_kind,error_code,error_summary`

const runtimeAttemptColumns = `id::text,node_run_id::text,attempt_no,dispatch_no,retry_no,model_settings_revision,model_runtime_instance_id::text,river_job_id,river_job_attempt,delivery_id,lease_owner,lease_until,status,output_schema_version,output_hash,failure_class,error_kind,error_code,error_summary,next_attempt_at,started_at,heartbeat_at,ended_at`

func scanHuman(row workflowRowScanner) (domain.HumanTask, error) {
	var h domain.HumanTask
	var id, run, node string
	var decision []byte
	err := row.Scan(&id, &run, &node, &h.Status, &h.ExpectedInputSchema, &h.TargetVersion, &decision, &h.ExpiresAt, &h.SubmittedAt, &h.CreatedAt)
	if err != nil {
		return h, err
	}
	h.ID = foundation.ID(id)
	h.RunID = foundation.ID(run)
	h.NodeRunID = foundation.ID(node)
	h.Decision = decision
	return h, nil
}

func scanClaimDefinition(row workflowRowScanner) (domain.Definition, error) {
	var definition domain.Definition
	var id, workspaceID string
	var graphJSON []byte
	if err := row.Scan(&id, &workspaceID, &definition.Key, &definition.Version, &graphJSON, &definition.CreatedAt); err != nil {
		return domain.Definition{}, err
	}
	graph, err := application.DecodeCanonicalGraph(graphJSON)
	if err != nil {
		return domain.Definition{}, err
	}
	canonicalGraph, err := json.Marshal(graph)
	if err != nil {
		return domain.Definition{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", false, err)
	}
	graphHash, err := application.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return domain.Definition{}, err
	}
	definition.ID = foundation.ID(id)
	definition.WorkspaceID = foundation.ID(workspaceID)
	definition.Graph = canonicalGraph
	definition.GraphHash = graphHash
	return definition, nil
}

func scanRuntimeRun(row workflowRowScanner) (domain.Run, error) {
	var run domain.Run
	var id, workspace, definition string
	var output []byte
	var idempotencyKey, requestHash *string
	if err := row.Scan(&id, &workspace, &definition, &run.Status, &run.Input, &output, &idempotencyKey, &requestHash, &run.Version, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt, &run.PauseRequestedAt, &run.CancelRequestedAt); err != nil {
		return run, err
	}
	run.ID, run.WorkspaceID, run.DefinitionID, run.Output = foundation.ID(id), foundation.ID(workspace), foundation.ID(definition), output
	if idempotencyKey != nil {
		run.IdempotencyKey = *idempotencyKey
	}
	if requestHash != nil {
		run.RequestHash = *requestHash
	}
	return run, nil
}

func scanRuntimeNode(row workflowRowScanner) (domain.NodeRun, error) {
	var node domain.NodeRun
	var id, runID string
	var output []byte
	var idempotencyKey, leaseOwner, failureClass, errorKind, errorCode, errorSummary *string
	var inputSchema, outputSchema, dispatch *int
	if err := row.Scan(&id, &runID, &node.NodeKey, &node.NodeType, &node.Status, &node.Attempt, &node.Input, &output, &idempotencyKey, &inputSchema, &outputSchema, &dispatch, &leaseOwner, &node.LeaseUntil, &node.Version, &node.CreatedAt, &node.UpdatedAt, &node.CompletedAt, &node.RetryNo, &node.NextAttemptAt, &failureClass, &errorKind, &errorCode, &errorSummary); err != nil {
		return node, err
	}
	node.ID, node.RunID, node.Output = foundation.ID(id), foundation.ID(runID), output
	if idempotencyKey != nil {
		node.IdempotencyKey = *idempotencyKey
	}
	if inputSchema != nil {
		node.InputSchemaVersion = *inputSchema
	}
	if outputSchema != nil {
		node.OutputSchemaVersion = *outputSchema
	}
	if dispatch != nil {
		node.DispatchNo = *dispatch
	}
	if leaseOwner != nil {
		node.LeaseOwner = *leaseOwner
	}
	if failureClass != nil {
		node.FailureClass = domain.FailureClass(*failureClass)
	}
	if errorKind != nil {
		node.ErrorKind = foundation.ErrorKind(*errorKind)
	}
	if errorCode != nil {
		node.ErrorCode = *errorCode
	}
	if errorSummary != nil {
		node.ErrorSummary = *errorSummary
	}
	return node, nil
}

func scanRuntimeAttempt(row workflowRowScanner) (domain.NodeAttempt, error) {
	var attempt domain.NodeAttempt
	var id, nodeID string
	var modelSettingsRevision *int64
	var modelRuntimeInstanceID *string
	var riverJobAttempt *int
	var riverJobID64 *int64
	var leaseOwner, failureClass, errorKind, errorCode, errorSummary *string
	var outputSchemaVersion *int
	var outputHash *string
	var leaseUntil *time.Time
	if err := row.Scan(&id, &nodeID, &attempt.AttemptNo, &attempt.DispatchNo, &attempt.RetryNo, &modelSettingsRevision, &modelRuntimeInstanceID, &riverJobID64, &riverJobAttempt, &attempt.DeliveryID, &leaseOwner, &leaseUntil, &attempt.Status, &outputSchemaVersion, &outputHash, &failureClass, &errorKind, &errorCode, &errorSummary, &attempt.NextAttemptAt, &attempt.StartedAt, &attempt.HeartbeatAt, &attempt.EndedAt); err != nil {
		return attempt, err
	}
	attempt.ModelSettingsRevision = modelSettingsRevision
	if modelRuntimeInstanceID != nil {
		instanceID := foundation.ID(*modelRuntimeInstanceID)
		attempt.ModelRuntimeInstanceID = &instanceID
	}
	if riverJobID64 != nil {
		attempt.RiverJobID = *riverJobID64
	}
	if riverJobAttempt != nil {
		attempt.RiverJobAttempt = *riverJobAttempt
	}
	if outputSchemaVersion != nil {
		attempt.OutputSchemaVersion = *outputSchemaVersion
	}
	if outputHash != nil {
		attempt.OutputHash = *outputHash
	}
	if leaseUntil != nil {
		attempt.LeaseUntil = *leaseUntil
	}
	attempt.ID, attempt.NodeRunID = foundation.ID(id), foundation.ID(nodeID)
	if leaseOwner != nil {
		attempt.LeaseOwner = *leaseOwner
	}
	if failureClass != nil {
		attempt.FailureClass = domain.FailureClass(*failureClass)
	}
	if errorKind != nil {
		attempt.ErrorKind = foundation.ErrorKind(*errorKind)
	}
	if errorCode != nil {
		attempt.ErrorCode = *errorCode
	}
	if errorSummary != nil {
		attempt.ErrorSummary = *errorSummary
	}
	return attempt, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFoundationID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
