//go:build integration

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolspostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceAnalysisToolAuthorizationReconcileAndFailureSettlement(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	prepareWorkspaceAnalysisToolAuthorizationRuntime(t, ctx, pool)
	repository, err := toolspostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	definition := workspaceAnalysisGitReceiptDefinition(t)
	command := workspaceAnalysisGitAuthorizationCommand(
		definition,
		"83000000-0000-4000-8000-000000000120",
		"83000000-0000-4000-8000-000000000121",
		"83000000-0000-4000-8000-000000000122",
		"83000000-0000-4000-8000-000000000021",
		1,
	)

	authorized, err := repository.AuthorizeWorkspaceAnalysisToolCall(ctx, command)
	if err != nil {
		t.Fatalf("AuthorizeWorkspaceAnalysisToolCall: %v: %v", err, errors.Unwrap(err))
	}
	if authorized.Disposition != toolsapplication.WorkspaceAnalysisToolAuthorizationCreated ||
		authorized.OperationID != command.OperationID || authorized.ReservationID != command.ReservationID ||
		authorized.Call.ID != command.Call.ID || authorized.Call.Status != toolsdomain.CallStarted ||
		authorized.Call.NodeAttemptID != command.Identity.NodeAttemptID {
		t.Fatalf("authorization=%+v call=%+v", authorized, authorized.Call)
	}
	assertWorkspaceAnalysisToolAuthorizationState(
		t, ctx, pool, authorized.Call.ID,
		"STARTED", "RESERVED", "STARTED", "", 1, 0, 1,
	)

	replayCommand := workspaceAnalysisGitAuthorizationCommand(
		definition,
		"83000000-0000-4000-8000-000000000123",
		"83000000-0000-4000-8000-000000000124",
		"83000000-0000-4000-8000-000000000125",
		"83000000-0000-4000-8000-000000000021",
		1,
	)
	replayed, err := repository.AuthorizeWorkspaceAnalysisToolCall(ctx, replayCommand)
	if err != nil {
		t.Fatalf("reconcile authorization: %v: %v", err, errors.Unwrap(err))
	}
	if replayed.Disposition != toolsapplication.WorkspaceAnalysisToolAuthorizationReconcile ||
		replayed.Call.ID != authorized.Call.ID || replayed.OperationID != authorized.OperationID ||
		replayed.ReservationID != authorized.ReservationID {
		t.Fatalf("reconcile=%+v", replayed)
	}
	assertWorkspaceAnalysisToolAuthorizationState(
		t, ctx, pool, authorized.Call.ID,
		"STARTED", "RESERVED", "STARTED", "", 1, 0, 1,
	)

	failedCall := authorized.Call
	completedAt := time.Now().UTC()
	failedCall.Status = toolsdomain.CallFailed
	failedCall.ErrorCode = "WORKSPACE_ANALYSIS_GIT_STATUS_FAILED"
	failedCall.Version = authorized.Call.Version + 1
	failedCall.CompletedAt = &completedAt
	failedCall.DurationMillis = 1
	finalized, err := repository.FinalizeWorkspaceAnalysisToolCall(ctx, toolsapplication.FinalizeWorkspaceAnalysisToolCallCommand{
		ExpectedVersion: authorized.Call.Version,
		Identity:        command.Identity,
		Call:            failedCall,
	})
	if err != nil {
		t.Fatalf("FinalizeWorkspaceAnalysisToolCall: %v: %v", err, errors.Unwrap(err))
	}
	if finalized.Replayed || finalized.Call.Status != toolsdomain.CallFailed ||
		finalized.Call.ErrorCode != failedCall.ErrorCode || finalized.Call.Version != failedCall.Version {
		t.Fatalf("finalized=%+v", finalized)
	}
	assertWorkspaceAnalysisToolAuthorizationState(
		t, ctx, pool, authorized.Call.ID,
		"FAILED", "SETTLED", "FAILED", failedCall.ErrorCode, 0, 1, 1,
	)

	finalizationReplay, err := repository.FinalizeWorkspaceAnalysisToolCall(ctx, toolsapplication.FinalizeWorkspaceAnalysisToolCallCommand{
		ExpectedVersion: authorized.Call.Version,
		Identity:        command.Identity,
		Call:            failedCall,
	})
	if err != nil || !finalizationReplay.Replayed || finalizationReplay.Call.ID != finalized.Call.ID {
		t.Fatalf("failure finalization replay=%+v err=%v", finalizationReplay, err)
	}
}

func TestWorkspaceAnalysisToolAuthorizationReplacementAttemptChargesUnknown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	prepareWorkspaceAnalysisToolAuthorizationRuntime(t, ctx, pool)
	repository, err := toolspostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	definition := workspaceAnalysisGitReceiptDefinition(t)
	first := workspaceAnalysisGitAuthorizationCommand(
		definition,
		"83000000-0000-4000-8000-000000000130",
		"83000000-0000-4000-8000-000000000131",
		"83000000-0000-4000-8000-000000000132",
		"83000000-0000-4000-8000-000000000021",
		1,
	)
	authorized, err := repository.AuthorizeWorkspaceAnalysisToolCall(ctx, first)
	if err != nil || authorized.Disposition != toolsapplication.WorkspaceAnalysisToolAuthorizationCreated {
		t.Fatalf("first authorization=%+v err=%v: %v", authorized, err, errors.Unwrap(err))
	}

	advanceWorkspaceAnalysisGitAuthorizationAttempt(t, ctx, pool)
	replacement := workspaceAnalysisGitAuthorizationCommand(
		definition,
		"83000000-0000-4000-8000-000000000133",
		"83000000-0000-4000-8000-000000000134",
		"83000000-0000-4000-8000-000000000135",
		"83000000-0000-4000-8000-000000000136",
		2,
	)
	reduced, err := repository.AuthorizeWorkspaceAnalysisToolCall(ctx, replacement)
	if err != nil {
		t.Fatalf("replacement authorization: %v: %v", err, errors.Unwrap(err))
	}
	if reduced.Disposition != toolsapplication.WorkspaceAnalysisToolAuthorizationTerminateUnknown ||
		reduced.Call.ID != authorized.Call.ID || reduced.Call.NodeAttemptID != first.Identity.NodeAttemptID ||
		reduced.Call.Status != toolsdomain.CallUnknown || reduced.Call.ErrorCode != "WORKSPACE_ANALYSIS_RESULT_UNKNOWN" ||
		reduced.OperationID != authorized.OperationID || reduced.ReservationID != authorized.ReservationID {
		t.Fatalf("replacement reduction=%+v call=%+v", reduced, reduced.Call)
	}
	assertWorkspaceAnalysisToolAuthorizationState(
		t, ctx, pool, authorized.Call.ID,
		"UNKNOWN", "UNKNOWN_CHARGED", "UNKNOWN", "WORKSPACE_ANALYSIS_RESULT_UNKNOWN", 0, 1, 1,
	)

	var latestAttemptID string
	if err := pool.QueryRow(ctx, `SELECT latest_node_attempt_id::text
		FROM agent.workspace_analysis_operation WHERE id=$1`, string(authorized.OperationID)).Scan(&latestAttemptID); err != nil {
		t.Fatal(err)
	}
	if latestAttemptID != string(replacement.Identity.NodeAttemptID) {
		t.Fatalf("latest attempt=%s, want %s", latestAttemptID, replacement.Identity.NodeAttemptID)
	}
}

func TestWorkspaceAnalysisToolAuthorizationRejectsPreAuthorizationDeadlineWithoutFacts(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	prepareWorkspaceAnalysisToolAuthorizationRuntimeWithRunAge(t, ctx, pool, 13*time.Minute+30*time.Second)
	repository, err := toolspostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	command := workspaceAnalysisGitAuthorizationCommand(
		workspaceAnalysisGitReceiptDefinition(t),
		"83000000-0000-4000-8000-000000000126",
		"83000000-0000-4000-8000-000000000127",
		"83000000-0000-4000-8000-000000000128",
		"83000000-0000-4000-8000-000000000021",
		1,
	)

	_, authorizationErr := repository.AuthorizeWorkspaceAnalysisToolCall(ctx, command)
	var classified *foundation.Error
	if !errors.As(authorizationErr, &classified) ||
		classified.Kind != foundation.ErrorNonRetryableFailure ||
		classified.Code != agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline ||
		classified.Retryable || !errors.Is(authorizationErr, context.DeadlineExceeded) {
		t.Fatalf("authorization err=%#v", authorizationErr)
	}

	var status string
	var runtimeFacts, reservedToolCalls, settledToolCalls, reservedSourceReads, settledSourceReads int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.workspace_analysis_operation WHERE id=$1)+
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation WHERE id=$2)+
		(SELECT count(*) FROM workflow.tool_call WHERE id=$3),
		status,reserved_tool_calls,settled_tool_calls,reserved_source_reads,settled_source_reads
		FROM agent.workspace_analysis_run WHERE id='83000000-0000-4000-8000-000000000014'`,
		string(command.OperationID), string(command.ReservationID), string(command.Call.ID),
	).Scan(&runtimeFacts, &status, &reservedToolCalls, &settledToolCalls, &reservedSourceReads, &settledSourceReads); err != nil {
		t.Fatal(err)
	}
	if runtimeFacts != 0 || status != "running" || reservedToolCalls != 0 || settledToolCalls != 0 ||
		reservedSourceReads != 0 || settledSourceReads != 0 {
		t.Fatalf("rejected authorization facts=%d status=%s budgets=(%d,%d,%d,%d)",
			runtimeFacts, status, reservedToolCalls, settledToolCalls, reservedSourceReads, settledSourceReads)
	}
}

func prepareWorkspaceAnalysisToolAuthorizationRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	prepareWorkspaceAnalysisToolAuthorizationRuntimeWithRunAge(t, ctx, pool, 0)
}

func prepareWorkspaceAnalysisToolAuthorizationRuntimeWithRunAge(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runAge time.Duration,
) {
	t.Helper()
	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if err := provider.UpTo(ctx, 85); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	snapshot, err := catalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	insertWorkspaceAnalysisAuthorizationRunFixture(t, ctx, pool, definition, snapshot, runAge)
	startWorkspaceAnalysisRun(t, ctx, pool)
	inspectNodeKind := ""
	for _, node := range definition.Graph.Nodes {
		if node.Key == conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace {
			inspectNodeKind = node.Kind
			break
		}
	}
	if inspectNodeKind == "" {
		t.Fatal("workspace analysis inspect node is missing")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,idempotency_key,
    input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000020','83000000-0000-4000-8000-000000000012',
    'inspect_workspace',$1,'running',1,'{}','wa-inspect-authorization',
    1,1,1,'wa-worker',now()+interval '5 minutes',1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000021','83000000-0000-4000-8000-000000000020',
    1,1,0,'wa-inspect-authorization-1','wa-worker',now()+interval '5 minutes','running',now()
);`, pgx.QueryExecModeSimpleProtocol, inspectNodeKind); err != nil {
		t.Fatal(err)
	}
}

func insertWorkspaceAnalysisAuthorizationRunFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	definition workflowdomain.RegisteredDefinition,
	snapshot catalog.WorkspaceAnalysisToolCatalog,
	runAge time.Duration,
) {
	t.Helper()
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent.question(
    id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,
    context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000010','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002',2,'workspace_analysis','analyze workspace','{}','standard','markdown',
    1,repeat('1',64),'wa-question',repeat('2',64),now()
);
INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES (
    '83000000-0000-4000-8000-000000000011','83000000-0000-4000-8000-000000000001',
    $1,$2,$3::jsonb,now()
);
INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
VALUES (
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000011','running','{}',1,now(),now()
);
INSERT INTO agent.answer(
    id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000013','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000010',
    '83000000-0000-4000-8000-000000000012','pending',1,now(),now()
);
	WITH timing AS (SELECT clock_timestamp()-$17::double precision*interval '1 millisecond' AS at)
	INSERT INTO agent.workspace_analysis_run(
    id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
    definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
    deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
    git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,
    validate_citation_tool_timeout_ms,durable_completion_margin_ms,
    max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,
    max_input_tokens,max_output_tokens,status,version,created_at,updated_at
	) SELECT
	    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000001',
	    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000010',
	    '83000000-0000-4000-8000-000000000013','83000000-0000-4000-8000-000000000012',
	    $1,$2,$4,$5,1,7,
	    at+($6+5000+120000+$7+15000+3*$8+10000+300000+15000+$9+10000+180000+15000)*interval '1 millisecond',
	    120000,300000,180000,$6,$7,$8,$9,5000,
	    $10,$11,$12,$13,$14,$15,$16,'queued',1,at,at
	FROM timing;`,
		pgx.QueryExecModeSimpleProtocol,
		definition.Key,
		definition.Version,
		string(graph),
		definition.GraphHash,
		snapshot.Hash,
		snapshot.ReadGitStatusTimeout.Milliseconds(),
		snapshot.SearchKnowledgeTimeout.Milliseconds(),
		snapshot.ReadSourceTimeout.Milliseconds(),
		snapshot.ValidateCitationTimeout.Milliseconds(),
		agentdomain.WorkspaceAnalysisV1MaxNodes,
		agentdomain.WorkspaceAnalysisV1MaxModelCalls,
		agentdomain.WorkspaceAnalysisV1MaxToolCalls,
		agentdomain.WorkspaceAnalysisV1MaxSourceReads,
		agentdomain.WorkspaceAnalysisV1MaxToolConcurrency,
		agentdomain.WorkspaceAnalysisV1MaxRunInputTokens,
		agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens,
		runAge.Milliseconds(),
	); err != nil {
		t.Fatal(err)
	}
}

func workspaceAnalysisGitAuthorizationCommand(
	definition toolsdomain.Definition,
	operationID foundation.ID,
	reservationID foundation.ID,
	callID foundation.ID,
	attemptID foundation.ID,
	leaseFence int64,
) toolsapplication.AuthorizeWorkspaceAnalysisToolCallCommand {
	tool := definition.Ref
	inputSchema := definition.InputSchema
	outputSchema := definition.OutputSchema
	identity := toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: "83000000-0000-4000-8000-000000000001", DefinitionID: "83000000-0000-4000-8000-000000000011",
		DefinitionVersion: 1, DefinitionHash: conversationworkflow.RegisteredWorkspaceAnalysisDefinition().GraphHash,
		WorkflowRunID: "83000000-0000-4000-8000-000000000012",
		NodeKey:       "inspect_workspace", NodeRunID: "83000000-0000-4000-8000-000000000020",
		NodeAttemptID: attemptID, LeaseOwner: "wa-worker", LeaseFence: leaseFence,
	}
	return toolsapplication.AuthorizeWorkspaceAnalysisToolCallCommand{
		Identity: identity,
		OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: "83000000-0000-4000-8000-000000000014",
			NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace,
			Kind:          agentdomain.WorkspaceAnalysisOperationGitStatus,
			Ordinal:       1,
		},
		OperationID:   operationID,
		ReservationID: reservationID,
		Definition:    definition,
		Call: toolsdomain.ToolCall{
			ID: callID, WorkspaceID: identity.WorkspaceID, WorkflowRunID: identity.WorkflowRunID,
			NodeRunID: identity.NodeRunID, NodeAttemptID: identity.NodeAttemptID, CallNo: 1,
			RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: definition.DefinitionHash,
			InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: capability.ReadLocal,
			SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash: strings.Repeat("5", 64), RequestBytes: 2, RequestSummary: json.RawMessage(`{}`),
			Status: toolsdomain.CallStarted, Version: 1, StartedAt: time.Now().UTC(),
		},
	}
}

func advanceWorkspaceAnalysisGitAuthorizationAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET
    status='retry_scheduled',lease_owner=NULL,lease_until=NULL,failure_class='retryable',
    error_code='RECOVERY_REPLAY',ended_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000021';
UPDATE workflow.node_run SET
    attempt=2,lease_owner='wa-worker',lease_until=now()+interval '5 minutes',
    dispatch_no=2,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000020';
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000136','83000000-0000-4000-8000-000000000020',
    2,2,1,'wa-inspect-authorization-2','wa-worker',now()+interval '5 minutes','running',now()
);`); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceAnalysisToolAuthorizationState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	callID foundation.ID,
	wantCallStatus string,
	wantReservationStatus string,
	wantOperationStatus string,
	wantErrorCode string,
	wantReservedToolCalls int,
	wantSettledToolCalls int,
	wantCallCount int,
) {
	t.Helper()
	var callStatus, reservationStatus, operationStatus, errorCode string
	var reservedToolCalls, settledToolCalls, reservationSettledToolCalls, callCount int
	if err := pool.QueryRow(ctx, `SELECT call.status,reservation.status,operation.status,
		COALESCE(operation.error_code,''),analysis.reserved_tool_calls,analysis.settled_tool_calls,
		COALESCE(reservation.settled_tool_calls,0)
		FROM workflow.tool_call AS call
		JOIN agent.workspace_analysis_operation AS operation ON operation.tool_call_id=call.id
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.workspace_analysis_run AS analysis ON analysis.id=operation.analysis_run_id
		WHERE call.id=$1`, string(callID)).Scan(
		&callStatus, &reservationStatus, &operationStatus, &errorCode,
		&reservedToolCalls, &settledToolCalls, &reservationSettledToolCalls,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_call
		WHERE workflow_run_id='83000000-0000-4000-8000-000000000012'`).Scan(&callCount); err != nil {
		t.Fatal(err)
	}
	wantReservationSettledToolCalls := 0
	if wantReservationStatus != "RESERVED" {
		wantReservationSettledToolCalls = 1
	}
	if callStatus != wantCallStatus || reservationStatus != wantReservationStatus ||
		operationStatus != wantOperationStatus || errorCode != wantErrorCode ||
		reservedToolCalls != wantReservedToolCalls || settledToolCalls != wantSettledToolCalls ||
		reservationSettledToolCalls != wantReservationSettledToolCalls || callCount != wantCallCount {
		t.Fatalf("state call=%s reservation=%s operation=%s error=%s reserved=%d settled=%d reservation_settled=%d calls=%d",
			callStatus, reservationStatus, operationStatus, errorCode, reservedToolCalls,
			settledToolCalls, reservationSettledToolCalls, callCount)
	}
}
