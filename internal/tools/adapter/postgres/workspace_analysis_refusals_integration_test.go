//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceAnalysisToolRefusalAuditIsAtomicReplayableAndBudgetFree(t *testing.T) {
	testWorkspaceAnalysisToolRefusalVariants(t, false, testWorkspaceAnalysisToolRefusalAuditIsAtomicReplayableAndBudgetFree)
}

func testWorkspaceAnalysisToolRefusalAuditIsAtomicReplayableAndBudgetFree(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness workspaceAnalysisToolRefusalIntegrationHarness,
) {
	pool := platform.DB()
	fixture := seedWorkspaceAnalysisToolRefusalRuntime(t, ctx, pool)
	repository := harness.repository
	command := toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand{
		RefusalID: fixture.refusalID, Identity: fixture.identity,
		OperationKey: fixture.operationKey, ErrorCode: "TOOL_NOT_ALLOWED",
	}

	created, err := repository.RecordWorkspaceAnalysisToolRefusal(ctx, command)
	if err != nil || created.Replayed || created.RefusalID != command.RefusalID || created.ErrorCode != command.ErrorCode {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replayed, err := repository.RecordWorkspaceAnalysisToolRefusal(ctx, command)
	if err != nil || !replayed.Replayed || replayed.RefusalID != created.RefusalID || replayed.ErrorCode != created.ErrorCode {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}

	var refusalCount, operationCount, reservationCount, callCount int
	var reservedTools, settledTools int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.workspace_analysis_tool_refusal WHERE analysis_run_id=$1),
		(SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=$1),
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=$1),
		(SELECT count(*) FROM workflow.tool_call WHERE workflow_run_id=$2),
		reserved_tool_calls,settled_tool_calls
		FROM agent.workspace_analysis_run WHERE id=$1`, string(fixture.operationKey.AnalysisRunID), string(fixture.identity.WorkflowRunID),
	).Scan(&refusalCount, &operationCount, &reservationCount, &callCount, &reservedTools, &settledTools); err != nil {
		t.Fatal(err)
	}
	if refusalCount != 1 || operationCount != 0 || reservationCount != 0 || callCount != 0 || reservedTools != 0 || settledTools != 0 {
		t.Fatalf("facts refusal=%d operation=%d reservation=%d call=%d budget=(%d,%d)", refusalCount, operationCount, reservationCount, callCount, reservedTools, settledTools)
	}

	auditEvents, err := harness.audit.List(ctx, auditdomain.ListQuery{WorkspaceID: &fixture.identity.WorkspaceID, Limit: 10})
	if err != nil || len(auditEvents) != 1 {
		t.Fatalf("audit=%+v err=%v", auditEvents, err)
	}
	event := auditEvents[0]
	if event.ActorType != auditdomain.ActorAgent || event.ActorRef != workspaceAnalysisToolRefusalActorRef ||
		event.Action != workspaceAnalysisToolRefusalAuditAction || event.Outcome != auditdomain.OutcomeRejected ||
		event.ErrorCode != command.ErrorCode || event.ResourceType != "workspace_analysis_tool_refusal" {
		t.Fatalf("audit event=%+v", event)
	}
	serialized := strings.ToLower(string(event.Correlation) + string(event.Metadata))
	for _, forbidden := range []string{"request_hash", "arguments", `"reason":`, "body", "path", "receipt", "private_binding"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("audit leaked %q: %s", forbidden, serialized)
		}
	}

	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_tool_refusal SET error_code='TOOL_PERMISSION_DENIED' WHERE id=$1`, string(created.RefusalID)); postgresState(err) != "55000" {
		t.Fatalf("immutable update error=%v state=%s", err, postgresState(err))
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agent.workspace_analysis_tool_refusal WHERE id=$1`, string(created.RefusalID)); postgresState(err) != "55000" {
		t.Fatalf("immutable delete error=%v state=%s", err, postgresState(err))
	}
	if _, err := pool.Exec(ctx, `TRUNCATE agent.workspace_analysis_tool_refusal`); postgresState(err) != "55000" {
		t.Fatalf("immutable truncate error=%v state=%s", err, postgresState(err))
	}

	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(fixture.identity.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(fixture.identity.NodeAttemptID)); err != nil {
		t.Fatal(err)
	}
	afterLeaseLoss, err := repository.RecordWorkspaceAnalysisToolRefusal(ctx, command)
	if err != nil || !afterLeaseLoss.Replayed || afterLeaseLoss.RefusalID != command.RefusalID {
		t.Fatalf("response-loss replay after lease=%+v err=%v", afterLeaseLoss, err)
	}
}

func TestWorkspaceAnalysisToolRefusalAuditRecoversAfterCommitResponseLoss(t *testing.T) {
	testWorkspaceAnalysisToolRefusalVariants(t, true, testWorkspaceAnalysisToolRefusalAuditRecoversAfterCommitResponseLoss)
}

func testWorkspaceAnalysisToolRefusalAuditRecoversAfterCommitResponseLoss(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness workspaceAnalysisToolRefusalIntegrationHarness,
) {
	pool := platform.DB()
	fixture := seedWorkspaceAnalysisToolRefusalRuntime(t, ctx, pool)
	result, err := harness.repository.RecordWorkspaceAnalysisToolRefusal(ctx, toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand{
		RefusalID: fixture.refusalID, Identity: fixture.identity, OperationKey: fixture.operationKey, ErrorCode: "TOOL_NOT_ALLOWED",
	})
	if err != nil || !result.Replayed || result.RefusalID != fixture.refusalID || result.ErrorCode != "TOOL_NOT_ALLOWED" {
		t.Fatalf("response-loss result=%+v err=%v", result, err)
	}
	var facts, audits int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.workspace_analysis_tool_refusal WHERE analysis_run_id=$1),
		(SELECT count(*) FROM ops.audit_event WHERE workspace_id=$2 AND action=$3)`,
		string(fixture.operationKey.AnalysisRunID), string(fixture.identity.WorkspaceID), workspaceAnalysisToolRefusalAuditAction,
	).Scan(&facts, &audits); err != nil {
		t.Fatal(err)
	}
	if facts != 1 || audits != 1 {
		t.Fatalf("response-loss persisted facts=%d audits=%d", facts, audits)
	}
}

func TestWorkspaceAnalysisToolOperationParticipantAndAuthorityParityIntegration(t *testing.T) {
	testWorkspaceAnalysisToolRefusalVariants(t, false, func(
		t *testing.T,
		platform *platformpostgres.Pool,
		ctx context.Context,
		harness workspaceAnalysisToolRefusalIntegrationHarness,
	) {
		pool := platform.DB()
		fixture := seedWorkspaceAnalysisToolRefusalRuntime(t, ctx, pool)
		definition, err := workspaceAnalysisBuiltinDefinition(workspaceAnalysisGitStatusRef)
		if err != nil {
			t.Fatal(err)
		}
		command := workspaceAnalysisToolOperationIntegrationCommand(fixture, definition)
		authorized, err := harness.repository.AuthorizeWorkspaceAnalysisToolCall(ctx, command)
		if err != nil || authorized.Disposition != toolsapplication.WorkspaceAnalysisToolAuthorizationCreated ||
			authorized.OperationID != command.OperationID || authorized.ReservationID != command.ReservationID ||
			authorized.Call.ID != command.Call.ID || authorized.Call.Status != toolsdomain.CallStarted {
			t.Fatalf("authorized=%+v err=%v", authorized, err)
		}
		reconciled, err := harness.repository.AuthorizeWorkspaceAnalysisToolCall(ctx, command)
		if err != nil || reconciled.Disposition != toolsapplication.WorkspaceAnalysisToolAuthorizationReconcile ||
			reconciled.Call.ID != authorized.Call.ID || reconciled.OperationID != authorized.OperationID ||
			reconciled.ReservationID != authorized.ReservationID {
			t.Fatalf("reconciled=%+v err=%v", reconciled, err)
		}

		output := json.RawMessage(workspaceAnalysisToolOperationIntegrationOutput)
		digest := sha256.Sum256(output)
		completedAt := time.Now().UTC().Truncate(time.Microsecond)
		completed := authorized.Call
		completed.Status = toolsdomain.CallSucceeded
		completed.ResponseHash = fmt.Sprintf("%x", digest)
		completed.ResponseBytes = int64(len(output))
		completed.ResponseSummary = json.RawMessage(`{"clean":true}`)
		completed.Version = authorized.Call.Version + 1
		completed.CompletedAt = &completedAt
		completed.DurationMillis = max(0, completedAt.Sub(completed.StartedAt).Milliseconds())
		completion := toolsapplication.FinalizeCallWithReceiptCommand{
			ExpectedVersion: authorized.Call.Version,
			Identity:        command.Identity,
			Call:            completed,
			Definition:      definition,
			ReceiptID:       foundation.ID("92000000-0000-4000-8000-000000000014"),
			Output:          output,
		}
		finalized, err := harness.repository.FinalizeCallWithReceipt(ctx, completion)
		if err != nil || finalized.Replayed || finalized.Call.Status != toolsdomain.CallSucceeded ||
			finalized.Receipt.ID != completion.ReceiptID || finalized.OperationID != authorized.OperationID {
			t.Fatalf("finalized=%+v receipt=%+v err=%v", finalized, finalized.Receipt, err)
		}
		replayed, err := harness.repository.FinalizeCallWithReceipt(ctx, completion)
		if err != nil || !replayed.Replayed || replayed.Call.ID != finalized.Call.ID || replayed.Receipt.ID != finalized.Receipt.ID {
			t.Fatalf("completion replay=%+v err=%v", replayed, err)
		}
		loaded, err := harness.repository.LoadResultReceipt(ctx, toolsapplication.LoadResultReceiptCommand{
			Call: finalized.Call, Definition: definition,
		})
		if err != nil || loaded.ID != finalized.Receipt.ID || loaded.OutputHash != finalized.Receipt.OutputHash {
			t.Fatalf("loaded receipt=%+v err=%v", loaded, err)
		}

		var authority agentapplication.WorkspaceAnalysisSuccessfulToolAuthority
		var found bool
		err = harness.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			var loadErr error
			authority, found, loadErr = harness.authority.LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped(
				callbackCtx,
				scope,
				agentapplication.WorkspaceAnalysisSuccessfulToolAuthorityQuery{
					WorkspaceID: fixture.identity.WorkspaceID, WorkflowRunID: fixture.identity.WorkflowRunID,
					OperationKey: fixture.operationKey,
				},
			)
			return loadErr
		})
		if err != nil || !found || authority.Validate() != nil || authority.OperationID != authorized.OperationID ||
			authority.ReservationID != authorized.ReservationID || authority.ToolCallID != authorized.Call.ID ||
			authority.ResultID != finalized.Receipt.ID || authority.ResultHash != finalized.Receipt.OutputHash {
			t.Fatalf("authority=%+v found=%t err=%v", authority, found, err)
		}

		var callStatus, reservationStatus, operationStatus string
		var resultID string
		var reservedTools, settledTools, requestedEvents, completedEvents int
		if err := pool.QueryRow(ctx, `SELECT call.status,reservation.status,operation.status,operation.result_id::text,
			analysis.reserved_tool_calls,analysis.settled_tool_calls,
			(SELECT count(*) FROM ops.server_event WHERE source_event_ref=$2),
			(SELECT count(*) FROM ops.server_event WHERE source_event_ref=$3)
			FROM workflow.tool_call call
			JOIN agent.workspace_analysis_budget_reservation reservation ON reservation.tool_call_id=call.id
			JOIN agent.workspace_analysis_operation operation ON operation.id=reservation.operation_id
			JOIN agent.workspace_analysis_run analysis ON analysis.id=reservation.analysis_run_id
			WHERE call.id=$1`, string(authorized.Call.ID),
			"workspace_analysis.tool_requested:"+string(authorized.OperationID)+":v1",
			"workspace_analysis.tool_completed:"+string(authorized.OperationID)+":v1",
		).Scan(&callStatus, &reservationStatus, &operationStatus, &resultID, &reservedTools, &settledTools, &requestedEvents, &completedEvents); err != nil {
			t.Fatal(err)
		}
		if callStatus != "SUCCEEDED" || reservationStatus != "SETTLED" || operationStatus != "SUCCEEDED" ||
			resultID != string(finalized.Receipt.ID) || reservedTools != 0 || settledTools != 1 ||
			requestedEvents != 1 || completedEvents != 1 {
			t.Fatalf("closure call=%s reservation=%s operation=%s result=%s budget=(%d,%d) events=(%d,%d)",
				callStatus, reservationStatus, operationStatus, resultID, reservedTools, settledTools, requestedEvents, completedEvents)
		}
	})
}

func TestWorkspaceAnalysisToolOperationEventFailureRollsBackAllOwnersIntegration(t *testing.T) {
	testWorkspaceAnalysisToolRefusalVariants(t, false, func(
		t *testing.T,
		platform *platformpostgres.Pool,
		ctx context.Context,
		harness workspaceAnalysisToolRefusalIntegrationHarness,
	) {
		pool := platform.DB()
		fixture := seedWorkspaceAnalysisToolRefusalRuntime(t, ctx, pool)
		definition, err := workspaceAnalysisBuiltinDefinition(workspaceAnalysisGitStatusRef)
		if err != nil {
			t.Fatal(err)
		}
		failure := &workspaceAnalysisToolOperationFailingEventAppender{
			err: errors.New("workspace analysis requested event failed"),
		}
		switch repository := harness.repository.(type) {
		case *GORMWorkspaceAnalysisRepository:
			repository.events = failure
		default:
			t.Fatalf("unsupported repository %T", repository)
		}
		command := workspaceAnalysisToolOperationIntegrationCommand(fixture, definition)
		if _, err := harness.repository.AuthorizeWorkspaceAnalysisToolCall(ctx, command); err == nil {
			t.Fatal("authorization succeeded after requested Event failure")
		}

		var operationCount, reservationCount, callCount, eventCount, reservedTools, settledTools int
		if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=$1),
			(SELECT count(*) FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=$1),
			(SELECT count(*) FROM workflow.tool_call WHERE workflow_run_id=$2),
			(SELECT count(*) FROM ops.server_event WHERE resource_ref=$3),
			reserved_tool_calls,settled_tool_calls
			FROM agent.workspace_analysis_run WHERE id=$1`,
			string(fixture.operationKey.AnalysisRunID), string(fixture.identity.WorkflowRunID),
			"workspace_analysis:"+string(fixture.operationKey.AnalysisRunID),
		).Scan(&operationCount, &reservationCount, &callCount, &eventCount, &reservedTools, &settledTools); err != nil {
			t.Fatal(err)
		}
		if operationCount != 0 || reservationCount != 0 || callCount != 0 || eventCount != 0 || reservedTools != 0 || settledTools != 0 {
			t.Fatalf("partial rollback operation=%d reservation=%d call=%d event=%d budget=(%d,%d)",
				operationCount, reservationCount, callCount, eventCount, reservedTools, settledTools)
		}
	})
}

type workspaceAnalysisToolOperationFailingEventAppender struct{ err error }

func (appender *workspaceAnalysisToolOperationFailingEventAppender) AppendScoped(
	context.Context,
	foundation.TransactionScope,
	eventsdomain.AppendRequest,
) (eventsdomain.ServerEvent, bool, error) {
	return eventsdomain.ServerEvent{}, false, appender.err
}

const workspaceAnalysisToolOperationIntegrationOutput = `{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}`

func workspaceAnalysisToolOperationIntegrationCommand(
	fixture workspaceAnalysisToolRefusalFixture,
	definition toolsdomain.Definition,
) toolsapplication.AuthorizeWorkspaceAnalysisToolCallCommand {
	tool := definition.Ref
	inputSchema := definition.InputSchema
	outputSchema := definition.OutputSchema
	return toolsapplication.AuthorizeWorkspaceAnalysisToolCallCommand{
		Identity: fixture.identity, OperationKey: fixture.operationKey,
		OperationID:   foundation.ID("92000000-0000-4000-8000-000000000011"),
		ReservationID: foundation.ID("92000000-0000-4000-8000-000000000012"),
		Definition:    definition,
		Call: toolsdomain.ToolCall{
			ID: foundation.ID("92000000-0000-4000-8000-000000000013"), WorkspaceID: fixture.identity.WorkspaceID,
			WorkflowRunID: fixture.identity.WorkflowRunID, NodeRunID: fixture.identity.NodeRunID,
			NodeAttemptID: fixture.identity.NodeAttemptID, CallNo: 1, RequestedToolName: tool.Name,
			Tool: &tool, DefinitionHash: definition.DefinitionHash, InputSchema: &inputSchema, OutputSchema: &outputSchema,
			Capability: definition.RequiredCapability, SideEffectLevel: definition.SideEffectLevel,
			InvocationPolicy: definition.InvocationPolicy, RequestHash: strings.Repeat("5", 64), RequestBytes: 2,
			RequestSummary: json.RawMessage(`{}`), Status: toolsdomain.CallStarted, Version: 1,
			StartedAt: time.Now().UTC().Truncate(time.Microsecond),
		},
	}
}

type workspaceAnalysisToolRefusalAuditLister interface {
	List(context.Context, auditdomain.ListQuery) ([]auditdomain.Event, error)
}

type workspaceAnalysisToolIntegrationRepository interface {
	toolsapplication.WorkspaceAnalysisToolRefusalRepository
	toolsapplication.WorkspaceAnalysisToolOperationRepository
	toolsapplication.ResultReceiptRepository
}

type workspaceAnalysisToolRefusalIntegrationHarness struct {
	repository workspaceAnalysisToolIntegrationRepository
	audit      workspaceAnalysisToolRefusalAuditLister
	authority  agentapplication.ScopedWorkspaceAnalysisToolAuthorityReader
	unitOfWork foundation.UnitOfWork
}

type workspaceAnalysisToolRefusalIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool, bool) workspaceAnalysisToolRefusalIntegrationHarness
}

func testWorkspaceAnalysisToolRefusalVariants(
	t *testing.T,
	commitResponseLoss bool,
	test func(*testing.T, *platformpostgres.Pool, context.Context, workspaceAnalysisToolRefusalIntegrationHarness),
) {
	t.Helper()
	variants := []workspaceAnalysisToolRefusalIntegrationVariant{

		{name: "gorm", open: openGORMWorkspaceAnalysisToolRefusalIntegration},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newToolRepositoryIntegrationPlatform(t)
			test(t, platform, ctx, variant.open(t, platform, commitResponseLoss))
		})
	}
}

func openGORMWorkspaceAnalysisToolRefusalIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
	commitResponseLoss bool,
) workspaceAnalysisToolRefusalIntegrationHarness {
	t.Helper()
	executionFence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(platform)
	if err != nil {
		t.Fatal(err)
	}
	agentRepository, err := agentpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	auditStore, err := auditpostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	auditRecorder, err := auditapplication.NewRecorder(auditStore)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMWorkspaceAnalysisRepository(
		platform,
		executionFence,
		agentRepository,
		agentRepository,
		agentRepository,
		events,
		auditRecorder,
	)
	if err != nil {
		t.Fatal(err)
	}
	if commitResponseLoss {
		fault := &toolRepositoryPostCommitUnitOfWork{
			inner: repository.unitOfWork,
			err:   errors.New("workspace analysis refusal commit response lost"),
		}
		fault.skip.Store(1)
		fault.armed.Store(true)
		repository.unitOfWork = fault
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	return workspaceAnalysisToolRefusalIntegrationHarness{
		repository: repository, audit: auditStore, authority: agentRepository, unitOfWork: unitOfWork,
	}
}

type toolRepositoryPostCommitUnitOfWork struct {
	inner foundation.UnitOfWork
	err   error
	armed atomic.Bool
	lost  atomic.Bool
	skip  atomic.Int64
}

func (unitOfWork *toolRepositoryPostCommitUnitOfWork) Within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work foundation.TransactionFunc,
) error {
	if err := unitOfWork.inner.Within(ctx, options, work); err != nil {
		return err
	}
	if !unitOfWork.armed.Load() {
		return nil
	}
	if remaining := unitOfWork.skip.Load(); remaining > 0 && unitOfWork.skip.CompareAndSwap(remaining, remaining-1) {
		return nil
	}
	if unitOfWork.lost.CompareAndSwap(false, true) {
		return unitOfWork.err
	}
	return nil
}

type workspaceAnalysisToolRefusalFixture struct {
	identity     toolsdomain.TrustedExecutionIdentity
	operationKey domain.WorkspaceAnalysisOperationKey
	refusalID    foundation.ID
}

func seedWorkspaceAnalysisToolRefusalRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) workspaceAnalysisToolRefusalFixture {
	t.Helper()
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	fixture := workspaceAnalysisToolRefusalFixture{
		refusalID: foundation.ID("92000000-0000-4000-8000-000000000010"),
		operationKey: domain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: "92000000-0000-4000-8000-000000000007",
			NodeKey:       domain.WorkspaceAnalysisOperationNodeInspectWorkspace,
			Kind:          domain.WorkspaceAnalysisOperationGitStatus,
			Ordinal:       1,
		},
	}
	fixture.identity = toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: "92000000-0000-4000-8000-000000000001", DefinitionID: "92000000-0000-4000-8000-000000000004",
		DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
		WorkflowRunID: "92000000-0000-4000-8000-000000000005",
		NodeKey:       string(domain.WorkspaceAnalysisOperationNodeInspectWorkspace), NodeRunID: "92000000-0000-4000-8000-000000000008",
		NodeAttemptID: "92000000-0000-4000-8000-000000000009", LeaseOwner: "wa-refusal-worker", LeaseFence: 1,
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
	INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
	VALUES ('92000000-0000-4000-8000-000000000001','wa-refusal','/tmp/wa-refusal','/tmp/wa-refusal',clock_timestamp(),'active',clock_timestamp(),clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO agent.conversation(id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash)
VALUES ('92000000-0000-4000-8000-000000000002','92000000-0000-4000-8000-000000000001','open','Workspace refusal',1,statement_timestamp(),statement_timestamp(),statement_timestamp(),'wa-refusal-conversation',repeat('a',64))`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO agent.question(id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,context_through_ordinal,context_hash,idempotency_key,request_hash,created_at)
	VALUES ('92000000-0000-4000-8000-000000000003','92000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000002',1,'workspace_analysis','analyze workspace','{}','standard','markdown',0,repeat('1',64),'wa-refusal-question',repeat('2',64),clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
	VALUES ($1,$2,$3,$4,$5::jsonb,clock_timestamp())`,
		string(fixture.identity.DefinitionID), string(fixture.identity.WorkspaceID), definition.Key, definition.Version, string(graph),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
	VALUES ('92000000-0000-4000-8000-000000000005','92000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000004','running','{}',1,clock_timestamp(),clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO agent.answer(id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at)
	VALUES ('92000000-0000-4000-8000-000000000006','92000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000002','92000000-0000-4000-8000-000000000003','92000000-0000-4000-8000-000000000005','pending',1,clock_timestamp(),clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH timing AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_run(
    id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
    definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
    deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
    git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms,
    durable_completion_margin_ms,max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,
    max_input_tokens,max_output_tokens,status,version,created_at,updated_at
) SELECT $1,$2,'92000000-0000-4000-8000-000000000002','92000000-0000-4000-8000-000000000003','92000000-0000-4000-8000-000000000006',$3,
    $4,$5,$6,$7,1,1,
    at+($8+5000+120000+$9+15000+3*$10+10000+300000+15000+$11+10000+180000+15000)*interval '1 millisecond',
    120000,300000,180000,$8,$9,$10,$11,5000,
    6,3,6,3,1,196608,5376,'queued',1,at,at
FROM timing`,
		string(fixture.operationKey.AnalysisRunID), string(fixture.identity.WorkspaceID), string(fixture.identity.WorkflowRunID),
		definition.Key, definition.Version, definition.GraphHash, snapshot.Hash,
		snapshot.ReadGitStatusTimeout.Milliseconds(), snapshot.SearchKnowledgeTimeout.Milliseconds(), snapshot.ReadSourceTimeout.Milliseconds(), snapshot.ValidateCitationTimeout.Milliseconds(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=GREATEST(updated_at,clock_timestamp())
		WHERE id=$1 AND status='queued' AND version=1`, string(fixture.operationKey.AnalysisRunID)); err != nil {
		t.Fatal(err)
	}
	inspectKind := ""
	for _, node := range definition.Graph.Nodes {
		if node.Key == string(domain.WorkspaceAnalysisOperationNodeInspectWorkspace) {
			inspectKind = node.Kind
			break
		}
	}
	if inspectKind == "" {
		t.Fatal("workspace analysis inspect node is missing")
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at)
	VALUES ($1,$2,'inspect_workspace',$3,'running',1,'{}','wa-refusal-inspect',1,1,1,'wa-refusal-worker',clock_timestamp()+interval '5 minutes',1,clock_timestamp(),clock_timestamp())`,
		string(fixture.identity.NodeRunID), string(fixture.identity.WorkflowRunID), inspectKind,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
	SELECT $1,$2,1,1,0,'wa-refusal-attempt',lease_owner,lease_until,'running',clock_timestamp()
	FROM workflow.node_run WHERE id=$2`,
		string(fixture.identity.NodeAttemptID), string(fixture.identity.NodeRunID),
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func postgresState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
