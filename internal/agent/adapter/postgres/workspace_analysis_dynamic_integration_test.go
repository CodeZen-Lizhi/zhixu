//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceAnalysisDynamicDecisionCommitRecoveryIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicDecisionCommitRecovery(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicDecisionCommitRecovery(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	seed := seedWorkspaceAnalysisDecisionIntegration(t, ctx, platform.DB(), 0)
	port, loseCommit := openGORMWorkspaceAnalysisModelRepositoryWithCommitLossIntegration(t, platform)
	repository := port.(*GORMWorkspaceAnalysisRepository)
	authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	if err != nil || authorized.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("decision authorization: %v", err)
	}
	changed := seed.command
	changed.RequestDocument = []byte(`{"prompt":"changed"}`)
	changed.Call.RequestHash, changed.Call.RequestBytes = workspaceAnalysisModelIntegrationHash(changed.RequestDocument), int64(len(changed.RequestDocument))
	if _, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, changed); err == nil {
		t.Fatal("same logical decision accepted another request")
	}
	decision := domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish}
	command := application.FinalizeWorkspaceAnalysisDecisionCommand{Identity: seed.command.Identity, OperationKey: seed.command.OperationKey,
		OperationID: authorized.OperationID, ExpectedCallVersion: 1, ReceiptID: workspaceAnalysisModelIntegrationID(71), Status: domain.ModelCallSucceeded,
		Decision: &decision, Usage: domain.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}, LatencyMillis: 4}
	loseCommit()
	completed, err := repository.FinalizeWorkspaceAnalysisDecision(ctx, command)
	if err != nil || !completed.Replayed || completed.Run.Status != domain.ModelRunSucceeded || completed.Decision == nil || completed.Decision.ID != command.ReceiptID {
		t.Fatalf("decision completion lost durable receipt: %v", err)
	}
	replayed, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	if err != nil || replayed.Disposition != application.WorkspaceAnalysisModelAuthorizationReuseResult || replayed.Call.ID != authorized.Call.ID {
		t.Fatalf("decision replay: %v", err)
	}
	journal, err := repository.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{WorkspaceID: seed.command.Identity.WorkspaceID,
		WorkflowRunID: seed.command.Identity.WorkflowRunID, AnalysisRunID: seed.command.OperationKey.AnalysisRunID})
	if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Decision == nil || journal.Run.Reserved.ModelCalls != 0 ||
		journal.Run.Settled.ModelCalls != 1 || journal.Run.Settled.InputTokens != 100 || journal.Run.Settled.OutputTokens != 20 {
		t.Fatalf("decision journal or actual usage changed: %v", err)
	}
	if _, err := platform.DB().Exec(ctx, `DELETE FROM agent.workspace_analysis_decision WHERE id=$1`, string(command.ReceiptID)); platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("decision receipt deletion: %v", err)
	}
	if _, err := platform.DB().Exec(ctx, `UPDATE agent.workspace_analysis_journal SET sequence=sequence+1 WHERE analysis_run_id=$1`, string(seed.command.OperationKey.AnalysisRunID)); platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("journal rewrite: %v", err)
	}
	command.Usage = domain.TokenUsage{InputTokens: 99, OutputTokens: 20, TotalTokens: 119}
	if _, err := repository.FinalizeWorkspaceAnalysisDecision(ctx, command); err == nil {
		t.Fatal("terminal replay rewrote actual usage")
	}
}

func TestWorkspaceAnalysisDynamicDecisionReplacementChargesUnknownIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicDecisionReplacementChargesUnknown(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicDecisionReplacementChargesUnknown(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) application.AuthorizeWorkspaceAnalysisModelCallCommand {
	seed := seedWorkspaceAnalysisDecisionIntegration(t, ctx, platform.DB(), 0)
	port, loseCommit := openGORMWorkspaceAnalysisModelRepositoryWithCommitLossIntegration(t, platform)
	repository := port.(*GORMWorkspaceAnalysisRepository)
	loseCommit()
	authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	if err != nil || authorized.Disposition != application.WorkspaceAnalysisModelAuthorizationReconcile {
		t.Fatalf("ambiguous authorization allowed a new call: %v", err)
	}
	again, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	if err != nil || again.Disposition != application.WorkspaceAnalysisModelAuthorizationReconcile || again.Call.ID != authorized.Call.ID {
		t.Fatalf("same attempt uncertainty: %v", err)
	}
	advanceWorkspaceAnalysisModelAttemptIntegration(t, ctx, platform.DB())
	replacement := seed.command
	replacement.Identity.NodeAttemptID, replacement.Identity.LeaseFence = workspaceAnalysisModelIntegrationID(90), 2
	replacement.Run.ID, replacement.Run.NodeAttemptID = workspaceAnalysisModelIntegrationID(91), replacement.Identity.NodeAttemptID
	replacement.Call.ID, replacement.Call.ModelRunID = workspaceAnalysisModelIntegrationID(92), replacement.Run.ID
	reduced, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, replacement)
	if err != nil || reduced.Disposition != application.WorkspaceAnalysisModelAuthorizationTerminateUnknown || reduced.Call.ID != authorized.Call.ID || reduced.Call.Status != domain.ModelCallUnknown {
		t.Fatalf("replacement repeated uncertain decision: %v", err)
	}
	journal, err := repository.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{WorkspaceID: seed.command.Identity.WorkspaceID,
		WorkflowRunID: seed.command.Identity.WorkflowRunID, AnalysisRunID: seed.command.OperationKey.AnalysisRunID})
	if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Decision != nil || journal.Run.Reserved.ModelCalls != 0 || journal.Run.Settled.ModelCalls != 1 ||
		journal.Run.Settled.InputTokens != 65536 || journal.Run.Settled.OutputTokens != 512 {
		t.Fatalf("unknown reservation did not settle completely: %v", err)
	}
	if _, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command); err == nil {
		t.Fatal("previous attempt retained authority")
	}
	return replacement
}

func TestWorkspaceAnalysisDynamicDecisionDeadlineDenialIsDurableIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicDecisionDeadlineDenial(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicDecisionDeadlineDenial(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) (application.AuthorizeWorkspaceAnalysisModelCallCommand, application.WorkspaceAnalysisAdmissionDenial) {
	// The Run still has time, but cannot fit one model timeout plus its durable
	// completion margin. No Call may be created for the denied decision.
	seed := seedWorkspaceAnalysisDecisionIntegration(t, ctx, platform.DB(), 36*time.Minute)
	port, loseCommit := openGORMWorkspaceAnalysisModelRepositoryWithCommitLossIntegration(t, platform)
	repository := port.(*GORMWorkspaceAnalysisRepository)
	loseCommit()
	_, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	denial, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || denial.OperationID != seed.command.OperationID || denial.Reason != domain.WorkspaceAnalysisRunDeadlineExceeded {
		t.Fatalf("deadline denial lost exact pending fact: %v", err)
	}
	journal, err := repository.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{WorkspaceID: seed.command.Identity.WorkspaceID,
		WorkflowRunID: seed.command.Identity.WorkflowRunID, AnalysisRunID: seed.command.OperationKey.AnalysisRunID})
	if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Operation.Status != domain.WorkspaceAnalysisOperationPending ||
		journal.Entries[0].ModelCall != nil || journal.Entries[0].ModelRun != nil || journal.Run.Reserved.ModelCalls != 0 || journal.Run.Settled.ModelCalls != 0 {
		t.Fatalf("denied decision allocated a model or reservation: %v", err)
	}
	_, err = repository.AuthorizeWorkspaceAnalysisModelCall(ctx, seed.command)
	var repeated *application.WorkspaceAnalysisAdmissionDenial
	if !errors.As(err, &repeated) || repeated.OperationID != denial.OperationID {
		t.Fatalf("pending decision denial did not replay: %v", err)
	}
	return seed.command, denial
}

func TestWorkspaceAnalysisDynamicDecisionCompletedPrefixReplacementIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicDecisionCompletedPrefixReplacement(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicDecisionCompletedPrefixReplacement(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	h.execute(t, ctx, 1, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionGitStatus}, "ReadGitStatus", `{}`)
	oldRun := h.command.Run
	if _, err := platform.DB().Exec(ctx, `SELECT agent.close_workspace_analysis_v2_decision_model($1,clock_timestamp())`, string(oldRun.ID)); platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("live decision prefix closed without replacement or termination proof: %v", err)
	}
	advanceWorkspaceAnalysisModelAttemptIntegration(t, ctx, platform.DB())
	command := h.command
	command.Identity.NodeAttemptID, command.Identity.LeaseFence = workspaceAnalysisModelIntegrationID(90), 2
	command.Run.ID, command.Run.NodeAttemptID = workspaceAnalysisModelIntegrationID(91), command.Identity.NodeAttemptID
	command.OperationKey.Ordinal = 2
	command.OperationID, command.ReservationID = workspaceAnalysisModelIntegrationID(80), workspaceAnalysisModelIntegrationID(81)
	command.Call.ID, command.Call.ModelRunID, command.Call.CallNo = workspaceAnalysisModelIntegrationID(92), command.Run.ID, 1
	created, err := h.model.AuthorizeWorkspaceAnalysisModelCall(ctx, command)
	if err != nil || created.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated || created.Call.CallNo != 1 || created.Run.ID == oldRun.ID {
		t.Fatalf("replacement did not start a new model run at local call one: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	decision := domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish}
	completed, err := h.model.FinalizeWorkspaceAnalysisDecision(ctx, application.FinalizeWorkspaceAnalysisDecisionCommand{
		Identity: command.Identity, OperationKey: command.OperationKey, OperationID: created.OperationID, ExpectedCallVersion: created.Call.Version,
		ReceiptID: workspaceAnalysisModelIntegrationID(93), Status: domain.ModelCallSucceeded, Decision: &decision,
		Usage: domain.TokenUsage{InputTokens: 16, OutputTokens: 16, TotalTokens: 32}, LatencyMillis: 1,
	})
	if err != nil || completed.Run.Status != domain.ModelRunSucceeded {
		t.Fatalf("replacement finish: %v", err)
	}
	journal, err := h.model.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{
		WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID, AnalysisRunID: command.OperationKey.AnalysisRunID,
	})
	if err != nil || len(journal.Entries) != 3 {
		t.Fatalf("replacement journal: %v", err)
	}
	first, last := journal.Entries[0], journal.Entries[2]
	if first.ModelRun == nil || first.ModelRun.ID != oldRun.ID || first.ModelRun.Status != domain.ModelRunSucceeded ||
		first.ModelRun.FinalResultType != domain.ResultTypeWorkspaceAnalysisDecision || first.ModelCall == nil || first.ModelCall.CallNo != 1 ||
		last.ModelRun == nil || last.ModelRun.ID != command.Run.ID || last.ModelRun.NodeAttemptID != command.Identity.NodeAttemptID ||
		last.ModelCall == nil || last.ModelCall.CallNo != 1 || last.Operation.Ordinal != 2 || last.Sequence != 3 {
		t.Fatal("replacement lost its closed prefix or confused local call number with global decision ordinal")
	}
	var loopComplete bool
	if err := platform.DB().QueryRow(ctx, `SELECT agent.workspace_analysis_v2_decisions_complete($1)`, string(command.OperationKey.AnalysisRunID)).Scan(&loopComplete); err != nil || !loopComplete {
		t.Fatalf("completed replacement loop has no durable closure: %v", err)
	}
	h.assertCounts(t, ctx, platform, 2, 1, 0, 0, 0)
	if h.counts["ReadGitStatus"].Load() != 1 {
		t.Fatal("replacement repeated the completed tool")
	}
}

func TestWorkspaceAnalysisDynamicDecisionBudgetDenialIsDurableIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	testWorkspaceAnalysisDynamicDecisionBudgetDenial(t, platform, ctx)
}

func testWorkspaceAnalysisDynamicDecisionBudgetDenial(t *testing.T, platform *platformpostgres.Pool, ctx context.Context) (application.AuthorizeWorkspaceAnalysisModelCallCommand, application.WorkspaceAnalysisAdmissionDenial) {
	t.Helper()
	h := newDynamicToolsIntegration(t, ctx, platform)
	for ordinal := 1; ordinal <= domain.WorkspaceAnalysisV2MaxDecisions; ordinal++ {
		h.execute(t, ctx, ordinal, domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionGitStatus}, "ReadGitStatus", `{}`)
	}
	command := h.command
	command.OperationKey.Ordinal = domain.WorkspaceAnalysisV2MaxDecisions + 1
	command.OperationID, command.ReservationID = workspaceAnalysisModelIntegrationID(800), workspaceAnalysisModelIntegrationID(801)
	command.Call.ID, command.Call.CallNo = workspaceAnalysisModelIntegrationID(802), command.OperationKey.Ordinal
	port, loseCommit := openGORMWorkspaceAnalysisModelRepositoryWithCommitLossIntegration(t, platform)
	repository := port.(*GORMWorkspaceAnalysisRepository)
	loseCommit()
	_, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, command)
	denial, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || denial.OperationID != command.OperationID || denial.Reason != domain.WorkspaceAnalysisRunBudgetExhausted {
		t.Fatalf("thirteenth decision did not preserve its budget denial after commit loss: %v", err)
	}
	_, err = repository.AuthorizeWorkspaceAnalysisModelCall(ctx, command)
	repeated, ok := application.WorkspaceAnalysisAdmissionDenialFromError(err)
	if !ok || repeated.OperationID != denial.OperationID || repeated.Requested != denial.Requested || repeated.Reason != denial.Reason {
		t.Fatalf("budget denial did not replay exactly: %v", err)
	}
	journal, err := repository.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{
		WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID, AnalysisRunID: command.OperationKey.AnalysisRunID,
	})
	if err != nil || len(journal.Entries) != 2*domain.WorkspaceAnalysisV2MaxDecisions+1 {
		t.Fatalf("exhausted decision journal: %v", err)
	}
	last := journal.Entries[len(journal.Entries)-1]
	if last.Operation.ID != command.OperationID || last.Operation.Status != domain.WorkspaceAnalysisOperationPending ||
		last.Operation.Ordinal != 13 || last.ModelCall != nil || last.ModelRun != nil || last.Decision != nil {
		t.Fatal("budget denial allocated a model or did not retain the exact pending slot")
	}
	h.assertCounts(t, ctx, platform, 12, 12, 0, 0, 1)
	if h.counts["ReadGitStatus"].Load() != 12 {
		t.Fatal("denial or replay invoked an additional tool")
	}
	return command, denial
}

func seedWorkspaceAnalysisDecisionIntegration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runAge time.Duration,
) workspaceAnalysisModelIntegrationFixture {
	t.Helper()
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	catalog, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES ('83000000-0000-4000-8000-000000000001','wa-model-uow','/tmp/wa-model-uow','/tmp/wa-model-uow',now(),'active',now(),now());
INSERT INTO agent.conversation(
    id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash
) VALUES (
    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000001',
    'open','Workspace Analysis Model UoW',1,now(),now(),now(),'wa-model-uow',repeat('a',64)
);
INSERT INTO agent.question(
    id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,
    context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000010','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002',1,'workspace_analysis','analyze workspace','{}','standard','markdown',
    0,repeat('1',64),'wa-model-question',repeat('2',64),now()
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,$4,$5::jsonb,clock_timestamp())`,
		string(workspaceAnalysisModelIntegrationID(11)), string(workspaceAnalysisModelIntegrationID(1)),
		definition.Key, definition.Version, string(graph),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
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
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH timing AS (SELECT clock_timestamp()-$10::interval AS at)
INSERT INTO agent.workspace_analysis_run(
    id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
    definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
    deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
    git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms,
    durable_completion_margin_ms,max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,
    max_input_tokens,max_output_tokens,status,version,created_at,updated_at
) SELECT
    $1,$2,$3,$4,$5,$6,$7,$8,$9,$11,2,7,
    at+interval '37 minutes 10 seconds',120000,300000,180000,10000,15000,15000,15000,
    5000,4,14,13,8,1,917504,11264,'queued',1,at,at
FROM timing`, string(workspaceAnalysisModelIntegrationID(14)), string(workspaceAnalysisModelIntegrationID(1)),
		string(workspaceAnalysisModelIntegrationID(2)), string(workspaceAnalysisModelIntegrationID(10)),
		string(workspaceAnalysisModelIntegrationID(13)), string(workspaceAnalysisModelIntegrationID(12)),
		definition.Key, definition.Version, definition.GraphHash,
		fmt.Sprintf("%d milliseconds", runAge.Milliseconds()), catalog.Hash,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=clock_timestamp()
		WHERE id=$1 AND status='queued' AND version=1`, string(workspaceAnalysisModelIntegrationID(14))); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000030','83000000-0000-4000-8000-000000000012',
    'decide_next','agent.workspace-analysis.v2.decide_next','running',1,'{}','wa-worker',now()+interval '30 minutes',
    'wa-model-plan',1,2,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at
) VALUES (
    '83000000-0000-4000-8000-000000000031','83000000-0000-4000-8000-000000000030',
    1,1,0,'wa-model-plan-1','wa-worker',now()+interval '30 minutes','running',now(),now()
);
INSERT INTO retrieval.index_version(
    id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
    version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000037','83000000-0000-4000-8000-000000000001',
    'simple','v1',repeat('8',64),'{}','wa-model-plan:index',repeat('9',64),0,
    'wa-model-plan-index','building','["vector"]',1,now(),now()
);`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	request := []byte(`{"prompt":"decide"}`)
	at := time.Now().UTC().Truncate(time.Microsecond)
	run := domain.ModelRun{
		ID: workspaceAnalysisModelIntegrationID(34), WorkspaceID: workspaceAnalysisModelIntegrationID(1),
		WorkflowRunID: workspaceAnalysisModelIntegrationID(12), NodeRunID: workspaceAnalysisModelIntegrationID(30),
		NodeAttemptID: workspaceAnalysisModelIntegrationID(31),
		Model:         domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "model-test", ModelVersion: "2026-08-16"},
		Profile:       domain.ModelProfileRef{ID: "workspace-analysis", Version: "v1"},
		Prompt:        domain.PromptRef{ID: "workspace-analysis-plan", Version: "v1"},
		Schema:        domain.SchemaRef{ID: domain.WorkspaceAnalysisDecisionSchemaID, Version: domain.WorkspaceAnalysisDecisionSchemaVersion},
		ReducedSchema: domain.SchemaRef{ID: domain.WorkspaceAnalysisDecisionSchemaID, Version: domain.WorkspaceAnalysisDecisionSchemaVersion},
		Retrieval:     domain.RetrievalRef{},
		Status:        domain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	call := domain.ModelCall{
		ID: workspaceAnalysisModelIntegrationID(35), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallAgent,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema,
		MaxOutputTokens: int(domain.WorkspaceAnalysisV2DecisionMaxOutputTokens), Status: domain.ModelCallStarted,
		RequestHash: workspaceAnalysisModelIntegrationHash(request), RequestBytes: int64(len(request)),
		Version: 1, StartedAt: at,
	}
	return workspaceAnalysisModelIntegrationFixture{command: application.AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: application.WorkspaceAnalysisModelExecutionIdentity{
			WorkspaceID: run.WorkspaceID, DefinitionID: workspaceAnalysisModelIntegrationID(11),
			DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
			WorkflowRunID: run.WorkflowRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext,
			NodeRunID: run.NodeRunID, NodeAttemptID: run.NodeAttemptID, LeaseOwner: "wa-worker", LeaseFence: 1,
		},
		OperationKey: domain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: workspaceAnalysisModelIntegrationID(14),
			NodeKey:       domain.WorkspaceAnalysisOperationNodeDecideNext,
			Kind:          domain.WorkspaceAnalysisOperationDecision, Ordinal: 1,
		},
		OperationID: workspaceAnalysisModelIntegrationID(32), ReservationID: workspaceAnalysisModelIntegrationID(33),
		Run: run, Call: call, RequestDocument: request,
	}}
}
