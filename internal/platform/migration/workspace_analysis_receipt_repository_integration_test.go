//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolspostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workspaceAnalysisGitReceiptOutput = `{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}`

const workspaceAnalysisSearchReceiptOutput = `{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}`

const workspaceAnalysisSearchReceiptPrivateBinding = `{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000038","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000037","source_span_id":"83000000-0000-4000-8000-000000000040","source_version_id":"83000000-0000-4000-8000-000000000039"}],"selected_refs":["E1"]}`

func TestWorkspaceAnalysisReceiptRepositoryAtomicCompletionReplayAndCommitRecovery(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if err := provider.UpTo(ctx, 85); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	definition := workspaceAnalysisGitReceiptDefinition(t)
	authorizeWorkspaceAnalysisGitReceiptOperation(t, ctx, pool, definition.DefinitionHash)

	repository, err := toolspostgres.NewRepository(&workspaceAnalysisReceiptCommitLossDB{pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	command := workspaceAnalysisGitReceiptCompletion(definition)
	result, err := repository.FinalizeCallWithReceipt(ctx, command)
	if err != nil {
		t.Fatalf("FinalizeCallWithReceipt after commit response loss: %v", err)
	}
	if !result.Replayed || result.Call.Status != toolsdomain.CallSucceeded || result.Receipt.ID != command.ReceiptID ||
		string(result.Receipt.Output) != workspaceAnalysisGitReceiptOutput {
		t.Fatalf("completion result=%+v receipt=%v", result.Call, result.Receipt)
	}

	loaded, err := repository.LoadResultReceipt(ctx, toolsapplication.LoadResultReceiptCommand{Call: result.Call, Definition: definition})
	if err != nil || loaded.ID != result.Receipt.ID || string(loaded.Output) != workspaceAnalysisGitReceiptOutput {
		t.Fatalf("LoadResultReceipt=%v err=%v", loaded, err)
	}
	replayed, err := repository.FinalizeCallWithReceipt(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Receipt.ID != result.Receipt.ID {
		t.Fatalf("receipt replay=%v err=%v", replayed.Receipt, err)
	}
	conflict := command
	conflict.ReceiptID = "83000000-0000-4000-8000-000000000026"
	if _, err := repository.FinalizeCallWithReceipt(ctx, conflict); receiptRepositoryErrorCode(err) != toolspostgres.ErrorCodeResultReceiptConflict {
		t.Fatalf("receipt identity conflict code=%s err=%v", receiptRepositoryErrorCode(err), err)
	}

	var reservationStatus, operationStatus, operationResultID string
	var reservedToolCalls, settledToolCalls int
	if err := pool.QueryRow(ctx, `SELECT reservation.status,operation.status,operation.result_id::text,
		analysis.reserved_tool_calls,analysis.settled_tool_calls
		FROM agent.workspace_analysis_budget_reservation AS reservation
		JOIN agent.workspace_analysis_operation AS operation ON operation.id=reservation.operation_id
		JOIN agent.workspace_analysis_run AS analysis ON analysis.id=reservation.analysis_run_id
		WHERE reservation.tool_call_id=$1`, string(command.Call.ID)).Scan(
		&reservationStatus, &operationStatus, &operationResultID, &reservedToolCalls, &settledToolCalls,
	); err != nil {
		t.Fatal(err)
	}
	if reservationStatus != "SETTLED" || operationStatus != "SUCCEEDED" || operationResultID != string(command.ReceiptID) ||
		reservedToolCalls != 0 || settledToolCalls != 1 {
		t.Fatalf("closure reservation=%s operation=%s result=%s reserved=%d settled=%d",
			reservationStatus, operationStatus, operationResultID, reservedToolCalls, settledToolCalls)
	}
}

func TestWorkspaceAnalysisReceiptFailureRepositoryAtomicClosureReplayAndCommitRecovery(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if err := provider.UpTo(ctx, 85); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	definition := workspaceAnalysisGitReceiptDefinition(t)
	authorizeWorkspaceAnalysisGitReceiptOperation(t, ctx, pool, definition.DefinitionHash)

	repository, err := toolspostgres.NewRepository(&workspaceAnalysisReceiptCommitLossDB{pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	command := workspaceAnalysisGitReceiptFailureCompletion(definition)
	result, err := repository.FinalizeCallWithReceiptFailure(ctx, command)
	if err != nil {
		t.Fatalf("FinalizeCallWithReceiptFailure after commit response loss: %v", err)
	}
	if !result.Replayed || result.Call.Status != toolsdomain.CallSucceeded ||
		result.OperationID != command.Failure.OperationID || result.Failure.ID != command.Failure.ID ||
		result.Failure.FailureCode != toolsdomain.ResultReceiptFailureContractInvalid {
		t.Fatalf("failure completion result=%+v failure=%s", result.Call, result.Failure.String())
	}

	replay := command
	replay.Failure.ID = "83000000-0000-4000-8000-000000000027"
	replayed, err := repository.FinalizeCallWithReceiptFailure(ctx, replay)
	if err != nil || !replayed.Replayed || replayed.Failure.ID != result.Failure.ID {
		t.Fatalf("receipt failure replay=%s err=%v", replayed.Failure.String(), err)
	}

	var callStatus, reservationStatus, operationStatus, operationError string
	var reservedToolCalls, settledToolCalls, receiptCount, failureCount int
	if err := pool.QueryRow(ctx, `SELECT call.status,reservation.status,operation.status,operation.error_code,
		analysis.reserved_tool_calls,analysis.settled_tool_calls,
		(SELECT count(*) FROM workflow.tool_result_receipt WHERE tool_call_id=call.id),
		(SELECT count(*) FROM workflow.tool_result_receipt_failure WHERE tool_call_id=call.id)
		FROM workflow.tool_call AS call
		JOIN agent.workspace_analysis_operation AS operation ON operation.tool_call_id=call.id
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.workspace_analysis_run AS analysis ON analysis.id=operation.analysis_run_id
		WHERE call.id=$1`, string(command.Call.ID)).Scan(
		&callStatus, &reservationStatus, &operationStatus, &operationError,
		&reservedToolCalls, &settledToolCalls, &receiptCount, &failureCount,
	); err != nil {
		t.Fatal(err)
	}
	if callStatus != "SUCCEEDED" || reservationStatus != "SETTLED" || operationStatus != "FAILED" ||
		operationError != "WORKSPACE_ANALYSIS_RECEIPT_INVALID" || reservedToolCalls != 0 || settledToolCalls != 1 ||
		receiptCount != 0 || failureCount != 1 {
		t.Fatalf("closure call=%s reservation=%s operation=%s error=%s reserved=%d settled=%d receipts=%d failures=%d",
			callStatus, reservationStatus, operationStatus, operationError,
			reservedToolCalls, settledToolCalls, receiptCount, failureCount)
	}
}

func TestWorkspaceAnalysisReceiptRepositorySearchPrivateBindingPersistenceAndRollback(t *testing.T) {
	ctx := context.Background()

	t.Run("success persists private binding and replays exact receipt", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()

		definition := prepareWorkspaceAnalysisSearchReceiptOperation(t, ctx, pool)
		repository, err := toolspostgres.NewRepository(pool)
		if err != nil {
			t.Fatal(err)
		}
		command := workspaceAnalysisSearchReceiptCompletion(definition)
		result, err := repository.FinalizeCallWithReceipt(ctx, command)
		if err != nil {
			t.Fatalf("FinalizeCallWithReceipt: %v", err)
		}
		if result.Replayed || result.Call.Status != toolsdomain.CallSucceeded || result.Receipt.ID != command.ReceiptID ||
			string(result.Receipt.Output) != workspaceAnalysisSearchReceiptOutput || result.Receipt.PrivateBinding == nil ||
			result.Receipt.PrivateBinding.Schema != command.PrivateBinding.Schema ||
			string(result.Receipt.PrivateBinding.Document) != workspaceAnalysisSearchReceiptPrivateBinding {
			t.Fatalf("search completion result=%+v receipt=%s", result.Call, result.Receipt.String())
		}

		loaded, err := repository.LoadResultReceipt(ctx, toolsapplication.LoadResultReceiptCommand{Call: result.Call, Definition: definition})
		if err != nil || loaded.PrivateBinding == nil ||
			string(loaded.PrivateBinding.Document) != workspaceAnalysisSearchReceiptPrivateBinding {
			t.Fatalf("LoadResultReceipt=%s err=%v", loaded.String(), err)
		}
		replayed, err := repository.FinalizeCallWithReceipt(ctx, command)
		if err != nil || !replayed.Replayed || replayed.Receipt.ID != result.Receipt.ID || replayed.Receipt.PrivateBinding == nil ||
			string(replayed.Receipt.PrivateBinding.Document) != workspaceAnalysisSearchReceiptPrivateBinding {
			t.Fatalf("search receipt replay=%s err=%v", replayed.Receipt.String(), err)
		}

		var persistedBinding []byte
		if err := pool.QueryRow(ctx, `SELECT server_binding_document
			FROM workflow.tool_result_receipt WHERE tool_call_id=$1`, string(command.Call.ID)).Scan(&persistedBinding); err != nil {
			t.Fatal(err)
		}
		if string(persistedBinding) != workspaceAnalysisSearchReceiptPrivateBinding {
			t.Fatalf("persisted private binding=%q", persistedBinding)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*toolsapplication.FinalizeCallWithReceiptCommand)
	}{
		{
			name: "missing private binding",
			mutate: func(command *toolsapplication.FinalizeCallWithReceiptCommand) {
				command.PrivateBinding = nil
			},
		},
		{
			name: "private binding does not match output",
			mutate: func(command *toolsapplication.FinalizeCallWithReceiptCommand) {
				command.PrivateBinding.Document = json.RawMessage(strings.Replace(
					workspaceAnalysisSearchReceiptPrivateBinding,
					`"evidence_ref":"E1"`,
					`"evidence_ref":"E2"`,
					1,
				))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, cleanup := newMigrationTestDatabase(t, ctx)
			defer cleanup()

			definition := prepareWorkspaceAnalysisSearchReceiptOperation(t, ctx, pool)
			repository, err := toolspostgres.NewRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			command := workspaceAnalysisSearchReceiptCompletion(definition)
			test.mutate(&command)
			if _, err := repository.FinalizeCallWithReceipt(ctx, command); err == nil {
				t.Fatal("invalid SearchKnowledge@2 private binding was accepted")
			}
			assertWorkspaceAnalysisSearchReceiptOperationStarted(t, ctx, pool, command.Call.ID)
		})
	}
}

func workspaceAnalysisGitReceiptDefinition(t *testing.T) toolsdomain.Definition {
	return workspaceAnalysisReceiptDefinition(t, toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2})
}

func workspaceAnalysisSearchReceiptDefinition(t *testing.T) toolsdomain.Definition {
	return workspaceAnalysisReceiptDefinition(t, toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2})
}

func workspaceAnalysisReceiptDefinition(t *testing.T, ref toolsdomain.ToolRef) toolsdomain.Definition {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref == ref {
			return contract.Definition
		}
	}
	t.Fatalf("%s@%d contract is missing", ref.Name, ref.Version)
	return toolsdomain.Definition{}
}

func authorizeWorkspaceAnalysisGitReceiptOperation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	definitionHash string,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000020','83000000-0000-4000-8000-000000000012',
    'inspect_workspace','workspace_analysis.inspect','running',1,'{}','wa-worker',now()+interval '5 minutes',1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000021','83000000-0000-4000-8000-000000000020',
    1,1,0,'wa-inspect-1','wa-worker',now()+interval '5 minutes','running',now()
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000022','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000020','inspect_workspace','GIT_STATUS',1,'TOOL',repeat('5',64),
    'PENDING',1,now(),now()
);
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '83000000-0000-4000-8000-000000000023','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000020',
    '83000000-0000-4000-8000-000000000021',1,'ReadGitStatus',2,$1,
    'tool.read_git_status.input',1,'tool.read_git_status.output',1,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('5',64),2,'{}','STARTED',false,now(),0,1
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',
    first_node_attempt_id='83000000-0000-4000-8000-000000000021',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000021',
    tool_call_id='83000000-0000-4000-8000-000000000023',
    version=2,started_at=now(),updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000022';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,
    reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000024','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000022',
    'TOOL','83000000-0000-4000-8000-000000000023','RESERVED',0,1,0,0,0,now()
);
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';`, pgx.QueryExecModeSimpleProtocol, definitionHash); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func prepareWorkspaceAnalysisSearchReceiptOperation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) toolsdomain.Definition {
	t.Helper()
	applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)
	definition := workspaceAnalysisSearchReceiptDefinition(t)
	authorizeWorkspaceAnalysisSearchReceiptOperation(t, ctx, pool, definition.DefinitionHash)
	return definition
}

func authorizeWorkspaceAnalysisSearchReceiptOperation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	definitionHash string,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000042','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000030','retrieve_evidence','KNOWLEDGE_SEARCH',1,'TOOL',repeat('6',64),
    'PENDING',1,now(),now()
);
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '83000000-0000-4000-8000-000000000043','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000030',
    '83000000-0000-4000-8000-000000000031',1,'SearchKnowledge',2,$1,
    'tool.search_knowledge.input',2,'tool.search_knowledge.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('6',64),2,'{}','STARTED',false,now(),0,1
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',
    first_node_attempt_id='83000000-0000-4000-8000-000000000031',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000031',
    tool_call_id='83000000-0000-4000-8000-000000000043',
    version=2,started_at=now(),updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000042';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,
    reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000044','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000042',
    'TOOL','83000000-0000-4000-8000-000000000043','RESERVED',0,1,0,0,0,now()
);
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';`, pgx.QueryExecModeSimpleProtocol, definitionHash); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func workspaceAnalysisGitReceiptCompletion(definition toolsdomain.Definition) toolsapplication.FinalizeCallWithReceiptCommand {
	tool := definition.Ref
	inputSchema := definition.InputSchema
	outputSchema := definition.OutputSchema
	startedAt := time.Now().UTC().Add(-time.Second)
	completedAt := time.Now().UTC()
	output := json.RawMessage(workspaceAnalysisGitReceiptOutput)
	return toolsapplication.FinalizeCallWithReceiptCommand{
		ExpectedVersion: 1,
		Identity: toolsdomain.TrustedExecutionIdentity{
			WorkspaceID: "83000000-0000-4000-8000-000000000001", DefinitionID: "83000000-0000-4000-8000-000000000011",
			DefinitionVersion: 1, DefinitionHash: strings.Repeat("3", 64), WorkflowRunID: "83000000-0000-4000-8000-000000000012",
			NodeKey: "inspect_workspace", NodeRunID: "83000000-0000-4000-8000-000000000020",
			NodeAttemptID: "83000000-0000-4000-8000-000000000021", LeaseOwner: "wa-worker", LeaseFence: 1,
		},
		ReceiptID:  "83000000-0000-4000-8000-000000000025",
		Definition: definition,
		Output:     output,
		Call: toolsdomain.ToolCall{
			ID: "83000000-0000-4000-8000-000000000023", WorkspaceID: "83000000-0000-4000-8000-000000000001",
			WorkflowRunID: "83000000-0000-4000-8000-000000000012", NodeRunID: "83000000-0000-4000-8000-000000000020",
			NodeAttemptID: "83000000-0000-4000-8000-000000000021", CallNo: 1, RequestedToolName: tool.Name,
			Tool: &tool, DefinitionHash: definition.DefinitionHash, InputSchema: &inputSchema, OutputSchema: &outputSchema,
			Capability: capability.ReadLocal, SideEffectLevel: toolsdomain.SideEffectNone,
			InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash:      strings.Repeat("5", 64), RequestBytes: 2, RequestSummary: json.RawMessage(`{}`),
			ResponseHash: receiptRepositoryHash(output), ResponseBytes: int64(len(output)),
			ResponseSummary: json.RawMessage(`{"clean":true}`), Status: toolsdomain.CallSucceeded,
			Version: 2, StartedAt: startedAt, CompletedAt: &completedAt, DurationMillis: 1000,
		},
	}
}

func workspaceAnalysisGitReceiptFailureCompletion(
	definition toolsdomain.Definition,
) toolsapplication.FinalizeCallWithReceiptFailureCommand {
	completion := workspaceAnalysisGitReceiptCompletion(definition)
	invalidOutput := json.RawMessage(`{"branch":true}`)
	outputBytes := int64(len(invalidOutput))
	completedAt := *completion.Call.CompletedAt
	completion.Call.ResponseHash = receiptRepositoryHash(invalidOutput)
	completion.Call.ResponseBytes = outputBytes
	completion.Call.ResponseSummary = json.RawMessage(`{"output_bytes":15}`)
	return toolsapplication.FinalizeCallWithReceiptFailureCommand{
		ExpectedVersion: completion.ExpectedVersion,
		Identity:        completion.Identity,
		Call:            completion.Call,
		Definition:      definition,
		Failure: toolsdomain.ResultReceiptFailureDraft{
			ID:                  "83000000-0000-4000-8000-000000000026",
			OperationID:         "83000000-0000-4000-8000-000000000022",
			AnalysisRunID:       "83000000-0000-4000-8000-000000000014",
			FailureCode:         toolsdomain.ResultReceiptFailureContractInvalid,
			ObservedOutputHash:  completion.Call.ResponseHash,
			ObservedOutputBytes: &outputBytes,
			CheckedAt:           completedAt,
			CreatedAt:           completedAt,
		},
	}
}

func workspaceAnalysisSearchReceiptCompletion(definition toolsdomain.Definition) toolsapplication.FinalizeCallWithReceiptCommand {
	tool := definition.Ref
	inputSchema := definition.InputSchema
	outputSchema := definition.OutputSchema
	startedAt := time.Now().UTC().Add(-time.Second)
	completedAt := time.Now().UTC()
	output := json.RawMessage(workspaceAnalysisSearchReceiptOutput)
	return toolsapplication.FinalizeCallWithReceiptCommand{
		ExpectedVersion: 1,
		Identity: toolsdomain.TrustedExecutionIdentity{
			WorkspaceID: "83000000-0000-4000-8000-000000000001", DefinitionID: "83000000-0000-4000-8000-000000000011",
			DefinitionVersion: 1, DefinitionHash: strings.Repeat("3", 64), WorkflowRunID: "83000000-0000-4000-8000-000000000012",
			NodeKey: "retrieve_evidence", NodeRunID: "83000000-0000-4000-8000-000000000030",
			NodeAttemptID: "83000000-0000-4000-8000-000000000031", LeaseOwner: "wa-worker", LeaseFence: 1,
		},
		ReceiptID:  "83000000-0000-4000-8000-000000000045",
		Definition: definition,
		Output:     output,
		PrivateBinding: &toolsapplication.ExecutorPrivateBinding{
			Schema:   toolsdomain.SchemaRef{ID: "tool.search_knowledge.private_binding", Version: 1},
			Document: json.RawMessage(workspaceAnalysisSearchReceiptPrivateBinding),
		},
		Call: toolsdomain.ToolCall{
			ID: "83000000-0000-4000-8000-000000000043", WorkspaceID: "83000000-0000-4000-8000-000000000001",
			WorkflowRunID: "83000000-0000-4000-8000-000000000012", NodeRunID: "83000000-0000-4000-8000-000000000030",
			NodeAttemptID: "83000000-0000-4000-8000-000000000031", CallNo: 1, RequestedToolName: tool.Name,
			Tool: &tool, DefinitionHash: definition.DefinitionHash, InputSchema: &inputSchema, OutputSchema: &outputSchema,
			Capability: capability.ReadLocal, SideEffectLevel: toolsdomain.SideEffectNone,
			InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash:      strings.Repeat("6", 64), RequestBytes: 2, RequestSummary: json.RawMessage(`{}`),
			ResponseHash: receiptRepositoryHash(output), ResponseBytes: int64(len(output)),
			ResponseSummary: json.RawMessage(`{"item_count":1}`), Status: toolsdomain.CallSucceeded,
			Version: 2, StartedAt: startedAt, CompletedAt: &completedAt, DurationMillis: 1000,
		},
	}
}

func assertWorkspaceAnalysisSearchReceiptOperationStarted(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	callID foundation.ID,
) {
	t.Helper()
	var callStatus, operationStatus, reservationStatus string
	var reservedToolCalls, settledToolCalls int
	var responseHash *string
	if err := pool.QueryRow(ctx, `SELECT call.status,operation.status,reservation.status,
		analysis.reserved_tool_calls,analysis.settled_tool_calls,call.response_hash
		FROM workflow.tool_call AS call
		JOIN agent.workspace_analysis_operation AS operation ON operation.tool_call_id=call.id
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.workspace_analysis_run AS analysis ON analysis.id=reservation.analysis_run_id
		WHERE call.id=$1`, string(callID)).Scan(
		&callStatus, &operationStatus, &reservationStatus, &reservedToolCalls, &settledToolCalls, &responseHash,
	); err != nil {
		t.Fatal(err)
	}
	var receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_result_receipt WHERE tool_call_id=$1`, string(callID)).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if callStatus != "STARTED" || operationStatus != "STARTED" || reservationStatus != "RESERVED" ||
		reservedToolCalls != 1 || settledToolCalls != 1 || responseHash != nil || receiptCount != 0 {
		t.Fatalf("failed receipt completion mutated state: call=%s operation=%s reservation=%s reserved=%d settled=%d response_hash=%v receipts=%d",
			callStatus, operationStatus, reservationStatus, reservedToolCalls, settledToolCalls, responseHash, receiptCount)
	}
}

func receiptRepositoryHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func receiptRepositoryErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

type workspaceAnalysisReceiptCommitLossDB struct {
	pool *pgxpool.Pool
	lost atomic.Bool
}

func (database *workspaceAnalysisReceiptCommitLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if !database.lost.Swap(true) {
		return &workspaceAnalysisReceiptCommitLossTx{Tx: tx}, nil
	}
	return tx, nil
}

type workspaceAnalysisReceiptCommitLossTx struct{ pgx.Tx }

func (tx *workspaceAnalysisReceiptCommitLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("simulated workspace analysis receipt commit response loss")
}
