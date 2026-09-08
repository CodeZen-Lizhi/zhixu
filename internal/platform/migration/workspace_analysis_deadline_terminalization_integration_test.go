//go:build integration

package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	workspaceAnalysisDeadlineWorkspaceID    foundation.ID = "83000000-0000-4000-8000-000000000001"
	workspaceAnalysisDeadlineConversationID foundation.ID = "83000000-0000-4000-8000-000000000002"
	workspaceAnalysisDeadlineQuestionID     foundation.ID = "83000000-0000-4000-8000-000000000010"
	workspaceAnalysisDeadlineWorkflowRunID  foundation.ID = "83000000-0000-4000-8000-000000000012"
	workspaceAnalysisDeadlineAnswerID       foundation.ID = "83000000-0000-4000-8000-000000000013"
	workspaceAnalysisDeadlineAnalysisRunID  foundation.ID = "83000000-0000-4000-8000-000000000014"
	workspaceAnalysisDeadlineInspectNodeID  foundation.ID = "83000000-0000-4000-8000-000000000020"
	workspaceAnalysisDeadlineInspectTryID   foundation.ID = "83000000-0000-4000-8000-000000000021"
	workspaceAnalysisDeadlineRetrieveNodeID foundation.ID = "83000000-0000-4000-8000-000000000030"
	workspaceAnalysisDeadlineRetrieveTryID  foundation.ID = "83000000-0000-4000-8000-000000000031"
	workspaceAnalysisDeadlineProofID        foundation.ID = "83000000-0000-4000-8000-000000000201"
	workspaceAnalysisDeadlineDraftID        foundation.ID = "83000000-0000-4000-8000-000000000202"
)

func TestWorkspaceAnalysisPreoperationDeadlineFinalizerInitialGitCommitsBundleAndReplays(t *testing.T) {
	ctx := context.Background()
	pool := newWorkspaceAnalysisDeadlineFixture(
		t, ctx,
		"now()-interval '13 minutes 49 seconds'",
		"now()+interval '1 second'",
	)
	insertWorkspaceAnalysisDeadlineNode(
		t, ctx, pool,
		workspaceAnalysisDeadlineInspectNodeID,
		workspaceAnalysisDeadlineInspectTryID,
		"inspect_workspace",
		"workspace_analysis.inspect",
	)
	insertWorkspaceAnalysisDeadlineDraft(t, ctx, pool)

	runtime := openWorkspaceAnalysisDeadlineRuntime(t, ctx, pool)
	t.Cleanup(runtime.Close)
	pool = runtime.DB()
	armMigrationCommitResponseLoss(t, runtime)
	ids := &workspaceAnalysisDeadlineOneShotIDs{}
	finalizer := newWorkspaceAnalysisDeadlineFinalizer(t, runtime, ids)
	command := workspaceAnalysisDeadlineCommand(
		workspaceAnalysisDeadlineInspectNodeID,
		workspaceAnalysisDeadlineInspectTryID,
	)
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.AnswerID != workspaceAnalysisDeadlineAnswerID ||
		output.ProofID != workspaceAnalysisDeadlineProofID ||
		output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed ||
		output.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisTermination || output.ModelRunID != nil {
		t.Fatalf("FinalizeTermination(response loss)=%#v replayed=%t err=%s", output, replayed, workspaceAnalysisDeadlineErrorChain(err))
	}
	replayedOutput, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || replayedOutput != output || ids.calls != 1 {
		t.Fatalf("FinalizeTermination(replay)=%#v replayed=%t ids=%d err=%s", replayedOutput, replayed, ids.calls, workspaceAnalysisDeadlineErrorChain(err))
	}

	var operationID *string
	var deadlineKind, answerStatus, runStatus, runReason, draftStatus string
	var deadlineOrdinal int
	var proofCount, eventCount int64
	if err := pool.QueryRow(ctx, `SELECT
		p.operation_id::text,p.deadline_operation_kind,p.deadline_operation_ordinal,
		a.publication_status,r.status,r.termination_reason,d.status,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof WHERE analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e
		 WHERE e.workspace_id=a.workspace_id
		   AND e.resource_ref='answer:'||a.id::text
		   AND e.event_type='answer.failed')
		FROM agent.workspace_analysis_termination_proof p
		JOIN agent.workspace_analysis_run r ON r.id=p.analysis_run_id
		JOIN agent.answer a ON a.id=p.answer_id
		JOIN agent.answer_draft_session d ON d.answer_id=a.id
		WHERE p.id=$1`, string(workspaceAnalysisDeadlineProofID)).Scan(
		&operationID, &deadlineKind, &deadlineOrdinal,
		&answerStatus, &runStatus, &runReason, &draftStatus,
		&proofCount, &eventCount,
	); err != nil {
		t.Fatal(err)
	}
	if operationID != nil || deadlineKind != "GIT_STATUS" || deadlineOrdinal != 1 ||
		answerStatus != "failed" || runStatus != "failed" ||
		runReason != string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded) ||
		draftStatus != "ABORTED" || proofCount != 1 || eventCount != 1 {
		t.Fatalf("operation=%v deadline=%s/%d answer=%s run=%s/%s draft=%s proof=%d event=%d",
			operationID, deadlineKind, deadlineOrdinal, answerStatus, runStatus, runReason,
			draftStatus, proofCount, eventCount)
	}
}

func TestWorkspaceAnalysisPreoperationDeadlineFinalizerDerivesPlanAfterGit(t *testing.T) {
	ctx := context.Background()
	pool := newWorkspaceAnalysisDeadlineFixture(
		t, ctx,
		"now()-interval '12 minutes 50 seconds'",
		"now()+interval '1 minute'",
	)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	insertWorkspaceAnalysisDeadlineNode(
		t, ctx, pool,
		workspaceAnalysisDeadlineRetrieveNodeID,
		workspaceAnalysisDeadlineRetrieveTryID,
		"retrieve_evidence",
		"workspace_analysis.retrieve",
	)

	runtime := openWorkspaceAnalysisDeadlineRuntime(t, ctx, pool)
	t.Cleanup(runtime.Close)
	pool = runtime.DB()
	finalizer := newWorkspaceAnalysisDeadlineFinalizer(
		t, runtime, foundation.NewUUIDGenerator(nil),
	)
	output, replayed, err := finalizer.FinalizeTermination(ctx, workspaceAnalysisDeadlineCommand(
		workspaceAnalysisDeadlineRetrieveNodeID,
		workspaceAnalysisDeadlineRetrieveTryID,
	))
	if err != nil || replayed || output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationFailed {
		t.Fatalf("FinalizeTermination(plan)=%#v replayed=%t err=%s", output, replayed, workspaceAnalysisDeadlineErrorChain(err))
	}
	var operationID *string
	var kind string
	var ordinal int
	if err := pool.QueryRow(ctx, `SELECT operation_id::text,deadline_operation_kind,deadline_operation_ordinal
		FROM agent.workspace_analysis_termination_proof
		WHERE analysis_run_id=$1`, string(workspaceAnalysisDeadlineAnalysisRunID)).Scan(
		&operationID, &kind, &ordinal,
	); err != nil {
		t.Fatal(err)
	}
	if operationID != nil || kind != "RETRIEVAL_PLAN" || ordinal != 1 {
		t.Fatalf("operation=%v deadline slot=%s/%d", operationID, kind, ordinal)
	}
}

func TestWorkspaceAnalysisDeadlineMigrationPreservesOperationProof(t *testing.T) {
	ctx := context.Background()
	pool := newWorkspaceAnalysisDeadlineFixture(
		t, ctx,
		"now()-interval '12 minutes'",
		"now()+interval '110 seconds'",
	)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	insertPendingWorkspaceAnalysisPlanOperation(t, ctx, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	insertWorkspaceAnalysisTerminationProof(t, ctx, tx, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", operationID: "83000000-0000-4000-8000-000000000032",
	})
	publishWorkspaceAnalysisFailure(t, ctx, tx, "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED")
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("close operation-backed deadline after 00086: %v", err)
	}

	var operationID string
	var deadlineKind *string
	var deadlineOrdinal *int
	if err := pool.QueryRow(ctx, `SELECT operation_id::text,deadline_operation_kind,deadline_operation_ordinal
		FROM agent.workspace_analysis_termination_proof
		WHERE analysis_run_id=$1`, string(workspaceAnalysisDeadlineAnalysisRunID)).Scan(
		&operationID, &deadlineKind, &deadlineOrdinal,
	); err != nil {
		t.Fatal(err)
	}
	if operationID != "83000000-0000-4000-8000-000000000032" || deadlineKind != nil || deadlineOrdinal != nil {
		t.Fatalf("operation=%s derived deadline slot=%v/%v", operationID, deadlineKind, deadlineOrdinal)
	}
}

func TestWorkspaceAnalysisDeadlineMigrationUpgradesExistingOperationProof(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	t.Cleanup(cleanup)
	if err := MigrateAtlasToVersion(ctx, pool, 85); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	insertWorkspaceAnalysisRunFixtureWithLimits(
		t, ctx, pool,
		"now()-interval '12 minutes'",
		"now()+interval '110 seconds'",
		5376,
	)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	insertPendingWorkspaceAnalysisPlanOperation(t, ctx, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	insertWorkspaceAnalysisTerminationProof(t, ctx, tx, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", operationID: "83000000-0000-4000-8000-000000000032",
	})
	publishWorkspaceAnalysisFailure(t, ctx, tx, "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED")
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("close 00085 operation-backed deadline: %v", err)
	}

	if err := MigrateAtlasToVersion(ctx, pool, 86); err != nil {
		t.Fatalf("upgrade existing operation-backed proof to 00086: %v", err)
	}
	var deadlineKind *string
	var deadlineOrdinal *int
	if err := pool.QueryRow(ctx, `SELECT deadline_operation_kind,deadline_operation_ordinal
		FROM agent.workspace_analysis_termination_proof
		WHERE analysis_run_id=$1`, string(workspaceAnalysisDeadlineAnalysisRunID)).Scan(
		&deadlineKind, &deadlineOrdinal,
	); err != nil {
		t.Fatal(err)
	}
	if deadlineKind != nil || deadlineOrdinal != nil {
		t.Fatalf("upgrade rewrote operation-backed proof slot=%v/%v", deadlineKind, deadlineOrdinal)
	}
}

func TestWorkspaceAnalysisDeadlineNextSlotFollowsFrozenPlanBranch(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name         string
		planDocument string
		wantNode     string
		wantKind     string
		wantOrdinal  int
	}{
		{
			name:         "non clarification continues to search",
			planDocument: workspaceAnalysisRetrievalPlanDocument,
			wantNode:     "retrieve_evidence",
			wantKind:     "KNOWLEDGE_SEARCH",
			wantOrdinal:  1,
		},
		{
			name:         "clarification branch has no operation slot",
			planDocument: workspaceAnalysisClarificationPlanDocument,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := newWorkspaceAnalysisDeadlineFixture(
				t, ctx,
				"now()",
				"now()+interval '13 minutes 50 seconds'",
			)
			startWorkspaceAnalysisRun(t, ctx, pool)
			completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
			authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
			completeWorkspaceAnalysisPlanOperation(t, ctx, pool, test.planDocument)

			if test.wantKind == "" {
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*)
					FROM agent.workspace_analysis_deadline_next_slot($1)`,
					string(workspaceAnalysisDeadlineAnalysisRunID),
				).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("clarification branch derived %d operation slots", count)
				}
				return
			}
			var node, kind string
			var ordinal int
			if err := pool.QueryRow(ctx, `SELECT node_key,operation_kind,ordinal
				FROM agent.workspace_analysis_deadline_next_slot($1)`,
				string(workspaceAnalysisDeadlineAnalysisRunID),
			).Scan(&node, &kind, &ordinal); err != nil {
				t.Fatal(err)
			}
			if node != test.wantNode || kind != test.wantKind || ordinal != test.wantOrdinal {
				t.Fatalf("next slot=%s/%s/%d want=%s/%s/%d",
					node, kind, ordinal, test.wantNode, test.wantKind, test.wantOrdinal)
			}
		})
	}
}

func TestWorkspaceAnalysisDeadlineNextSlotDerivesFirstSourceFromSearchReceipt(t *testing.T) {
	ctx := context.Background()
	pool := newWorkspaceAnalysisDeadlineFixture(
		t, ctx,
		"now()",
		"now()+interval '13 minutes 50 seconds'",
	)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)

	definition := workspaceAnalysisSearchReceiptDefinition(t)
	authorizeWorkspaceAnalysisSearchReceiptOperation(t, ctx, pool, definition.DefinitionHash)
	runtime := openMigrationRuntimePool(t, ctx, pool)
	t.Cleanup(runtime.Close)
	pool = runtime.DB()
	repository := newWorkspaceAnalysisMigrationToolsRepository(t, runtime)
	if _, err := repository.FinalizeCallWithReceipt(ctx, workspaceAnalysisSearchReceiptCompletion(definition)); err != nil {
		t.Fatalf("complete SearchKnowledge receipt: %v", err)
	}

	var node, kind string
	var ordinal int
	if err := pool.QueryRow(ctx, `SELECT node_key,operation_kind,ordinal
		FROM agent.workspace_analysis_deadline_next_slot($1)`,
		string(workspaceAnalysisDeadlineAnalysisRunID),
	).Scan(&node, &kind, &ordinal); err != nil {
		t.Fatal(err)
	}
	if node != "read_evidence" || kind != "SOURCE_READ" || ordinal != 1 {
		t.Fatalf("next slot=%s/%s/%d want=read_evidence/SOURCE_READ/1", node, kind, ordinal)
	}
}

func TestWorkspaceAnalysisDeadlineNextSlotCoversRemainingSourceOrdinals(t *testing.T) {
	ctx := context.Background()
	pool := newWorkspaceAnalysisDeadlineFixture(
		t, ctx,
		"now()",
		"now()+interval '13 minutes 50 seconds'",
	)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)

	definition := workspaceAnalysisSearchReceiptDefinition(t)
	authorizeWorkspaceAnalysisSearchReceiptOperation(t, ctx, pool, definition.DefinitionHash)
	completion := workspaceAnalysisSearchReceiptCompletion(definition)
	completion.Output = json.RawMessage(workspaceAnalysisDeadlineSearchOutput)
	completion.Call.ResponseHash = receiptRepositoryHash(completion.Output)
	completion.Call.ResponseBytes = int64(len(completion.Output))
	completion.Call.ResponseSummary = json.RawMessage(`{"item_count":3}`)
	completion.PrivateBinding.Document = json.RawMessage(workspaceAnalysisDeadlineSelectedRefsBinding)
	runtime := openMigrationRuntimePool(t, ctx, pool)
	t.Cleanup(runtime.Close)
	pool = runtime.DB()
	repository := newWorkspaceAnalysisMigrationToolsRepository(t, runtime)
	if _, err := repository.FinalizeCallWithReceipt(ctx, completion); err != nil {
		t.Fatalf("complete three-item SearchKnowledge receipt: %v", err)
	}

	assertWorkspaceAnalysisDeadlineNextSlot(t, ctx, pool, "read_evidence", "SOURCE_READ", 1)
	completeWorkspaceAnalysisDeadlineSource(t, ctx, pool, 1, "E1")
	assertWorkspaceAnalysisDeadlineNextSlot(t, ctx, pool, "read_evidence", "SOURCE_READ", 2)
	completeWorkspaceAnalysisDeadlineSource(t, ctx, pool, 2, "E2")
	assertWorkspaceAnalysisDeadlineNextSlot(t, ctx, pool, "read_evidence", "SOURCE_READ", 3)
	completeWorkspaceAnalysisDeadlineSource(t, ctx, pool, 3, "E3")
	assertWorkspaceAnalysisDeadlineNextSlot(t, ctx, pool, "synthesize_answer", "ANSWER_SYNTHESIS", 1)
}

func TestWorkspaceAnalysisDeadlineNextSlotCoversCitationAndReview(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name            string
		stopBefore      string
		removeOperation []string
		wantNode        string
		wantOperation   string
	}{
		{
			name:            "synthesis candidate leads to citation validation",
			stopBefore:      workspaceAnalysisDeadlineValidationStart,
			removeOperation: []string{"83000000-0000-4000-8000-000000000172", "83000000-0000-4000-8000-000000000182"},
			wantNode:        "validate_citations",
			wantOperation:   "CITATION_VALIDATION",
		},
		{
			name:            "valid citation receipt leads to faithfulness review",
			stopBefore:      workspaceAnalysisDeadlineReviewStart,
			removeOperation: []string{"83000000-0000-4000-8000-000000000182"},
			wantNode:        "review_publish",
			wantOperation:   "FAITHFULNESS_REVIEW",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := newWorkspaceAnalysisDeadlineFixture(
				t, ctx,
				"now()",
				"now()+interval '13 minutes 50 seconds'",
			)
			startWorkspaceAnalysisRun(t, ctx, pool)
			completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
			insertWorkspaceAnalysisDeadlinePublicationPrefix(t, ctx, pool, test.stopBefore, test.removeOperation...)
			assertWorkspaceAnalysisDeadlineNextSlot(t, ctx, pool, test.wantNode, test.wantOperation, 1)
		})
	}
}

func TestWorkspaceAnalysisDeadlineNextSlotRejectsSelectedRefAndPrefixDrift(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name            string
		evidenceRef     string
		ordinal         int
		threeItemSearch bool
		wantInsertError bool
	}{
		{name: "selected reference drift", evidenceRef: "E2", ordinal: 1, wantInsertError: true},
		{name: "prefix drift", evidenceRef: "E3", ordinal: 2, threeItemSearch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := newWorkspaceAnalysisDeadlineFixture(
				t, ctx,
				"now()",
				"now()+interval '13 minutes 50 seconds'",
			)
			startWorkspaceAnalysisRun(t, ctx, pool)
			completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
			authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
			completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)
			definition := workspaceAnalysisSearchReceiptDefinition(t)
			authorizeWorkspaceAnalysisSearchReceiptOperation(t, ctx, pool, definition.DefinitionHash)
			runtime := openMigrationRuntimePool(t, ctx, pool)
			t.Cleanup(runtime.Close)
			pool = runtime.DB()
			repository := newWorkspaceAnalysisMigrationToolsRepository(t, runtime)
			completion := workspaceAnalysisSearchReceiptCompletion(definition)
			if test.threeItemSearch {
				completion.Output = json.RawMessage(workspaceAnalysisDeadlineSearchOutput)
				completion.Call.ResponseHash = receiptRepositoryHash(completion.Output)
				completion.Call.ResponseBytes = int64(len(completion.Output))
				completion.Call.ResponseSummary = json.RawMessage(`{"item_count":3}`)
				completion.PrivateBinding.Document = json.RawMessage(workspaceAnalysisDeadlineSelectedRefsBinding)
			}
			if _, err := repository.FinalizeCallWithReceipt(ctx, completion); err != nil {
				t.Fatalf("complete SearchKnowledge receipt: %v", err)
			}
			if test.ordinal == 2 {
				completeWorkspaceAnalysisDeadlineSource(t, ctx, pool, 1, "E1")
			}
			if test.wantInsertError {
				completeWorkspaceAnalysisDeadlineSourceExpectError(t, ctx, pool, test.ordinal, test.evidenceRef)
				assertWorkspaceAnalysisDeadlineNextSlot(t, ctx, pool, "read_evidence", "SOURCE_READ", test.ordinal)
				return
			}
			completeWorkspaceAnalysisDeadlineSource(t, ctx, pool, test.ordinal, test.evidenceRef)
			assertWorkspaceAnalysisDeadlineNoNextSlot(t, ctx, pool)
		})
	}
}

const workspaceAnalysisDeadlineSelectedRefsBinding = `{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000381","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000371","source_span_id":"83000000-0000-4000-8000-000000000391","source_version_id":"83000000-0000-4000-8000-000000000361"},{"chunk_id":"83000000-0000-4000-8000-000000000382","citation_id":"cite-9f8de041b3a60b404389373fc12a6f35e8f2bb79b18e1a44b2e5c90aa49d29fa","content_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","evidence_ref":"E2","index_version_id":"83000000-0000-4000-8000-000000000371","source_span_id":"83000000-0000-4000-8000-000000000392","source_version_id":"83000000-0000-4000-8000-000000000362"},{"chunk_id":"83000000-0000-4000-8000-000000000383","citation_id":"cite-454c22b810f30b6ceaaf8cd6e9148a9cb9d8c2be27eb176f2e6cc03f0e4d8e11","content_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","evidence_ref":"E3","index_version_id":"83000000-0000-4000-8000-000000000371","source_span_id":"83000000-0000-4000-8000-000000000393","source_version_id":"83000000-0000-4000-8000-000000000363"}],"selected_refs":["E1","E2","E3"]}`

const workspaceAnalysisDeadlineSearchOutput = `{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence 1"},{"evidence_ref":"E2","rank":2,"snippet":"Evidence 2"},{"evidence_ref":"E3","rank":3,"snippet":"Evidence 3"}]}`

const (
	workspaceAnalysisDeadlineValidationStart = "UPDATE agent.workspace_analysis_operation SET\nstatus='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000171'"
	workspaceAnalysisDeadlineReviewStart     = "UPDATE agent.workspace_analysis_operation SET\nstatus='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000181'"
)

func assertWorkspaceAnalysisDeadlineNextSlot(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantNode, wantOperation string,
	wantOrdinal int,
) {
	t.Helper()
	var node, operation string
	var ordinal int
	if err := pool.QueryRow(ctx, `SELECT node_key,operation_kind,ordinal
		FROM agent.workspace_analysis_deadline_next_slot($1)`,
		string(workspaceAnalysisDeadlineAnalysisRunID),
	).Scan(&node, &operation, &ordinal); err != nil {
		t.Fatal(err)
	}
	if node != wantNode || operation != wantOperation || ordinal != wantOrdinal {
		t.Fatalf("next slot=%s/%s/%d want=%s/%s/%d",
			node, operation, ordinal, wantNode, wantOperation, wantOrdinal)
	}
}

func assertWorkspaceAnalysisDeadlineNoNextSlot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.workspace_analysis_deadline_next_slot($1)`,
		string(workspaceAnalysisDeadlineAnalysisRunID),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deadline next slot count=%d, want none", count)
	}
}

func insertWorkspaceAnalysisDeadlinePublicationPrefix(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	stopBefore string,
	removeOperationIDs ...string,
) {
	t.Helper()
	fixture, _, found := strings.Cut(workspaceAnalysisPublicationReadyFixtureSQL, stopBefore)
	if !found {
		t.Fatalf("publication fixture marker %q is missing", stopBefore)
	}
	for _, operationID := range removeOperationIDs {
		fixture = removeWorkspaceAnalysisDeadlineFixtureRow(t, fixture, operationID)
	}
	if stopBefore == workspaceAnalysisDeadlineValidationStart {
		for _, rowID := range []string{
			"83000000-0000-4000-8000-000000000173",
			"83000000-0000-4000-8000-000000000185",
			"83000000-0000-4000-8000-000000000184",
		} {
			fixture = removeWorkspaceAnalysisDeadlineFixtureRow(t, fixture, rowID)
		}
	}
	if stopBefore == workspaceAnalysisDeadlineReviewStart {
		for _, rowID := range []string{
			"83000000-0000-4000-8000-000000000185",
			"83000000-0000-4000-8000-000000000184",
		} {
			fixture = removeWorkspaceAnalysisDeadlineFixtureRow(t, fixture, rowID)
		}
	}
	settledToolCalls := 3
	if stopBefore == workspaceAnalysisDeadlineReviewStart {
		settledToolCalls = 4
	}
	fixture += fmt.Sprintf(`
UPDATE agent.workspace_analysis_run SET
    reserved_model_calls=0,reserved_tool_calls=0,reserved_source_reads=0,
    reserved_input_tokens=0,reserved_output_tokens=0,
    settled_model_calls=2,settled_tool_calls=%d,settled_source_reads=1,
    settled_input_tokens=20,settled_output_tokens=10,
    version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';`, settledToolCalls)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fixture); err != nil {
		t.Fatalf("insert workspace analysis deadline publication prefix: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit workspace analysis deadline publication prefix: %v", err)
	}
}

func removeWorkspaceAnalysisDeadlineFixtureRow(t *testing.T, fixture, rowID string) string {
	t.Helper()
	lines := strings.Split(fixture, "\n")
	needle := "('" + rowID + "'"
	for index, line := range lines {
		if !strings.Contains(line, needle) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, ",") {
			lines = append(lines[:index], lines[index+1:]...)
			return strings.Join(lines, "\n")
		}
		if !strings.HasSuffix(trimmed, ");") || index == 0 {
			t.Fatalf("fixture row %s has an unexpected shape", rowID)
		}
		previous := strings.TrimRight(lines[index-1], " \t")
		if !strings.HasSuffix(previous, ",") {
			t.Fatalf("fixture row %s has no preceding value separator", rowID)
		}
		lines[index-1] = strings.TrimSuffix(previous, ",") + ";"
		lines = append(lines[:index], lines[index+1:]...)
		return strings.Join(lines, "\n")
	}
	t.Fatalf("fixture row %s is missing", rowID)
	return ""
}

func completeWorkspaceAnalysisDeadlineSource(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ordinal int,
	evidenceRef string,
) {
	t.Helper()
	if err := completeWorkspaceAnalysisDeadlineSourceTx(ctx, pool, ordinal, evidenceRef); err != nil {
		t.Fatalf("complete source ordinal %d: %v", ordinal, err)
	}
}

func completeWorkspaceAnalysisDeadlineSourceExpectError(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ordinal int,
	evidenceRef string,
) {
	t.Helper()
	err := completeWorkspaceAnalysisDeadlineSourceTx(ctx, pool, ordinal, evidenceRef)
	assertPostgresCode(t, err, "23514")
}

func completeWorkspaceAnalysisDeadlineSourceTx(
	ctx context.Context,
	pool *pgxpool.Pool,
	ordinal int,
	evidenceRef string,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if ordinal == 1 {
		if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(
			id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
			idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
		) VALUES (
			'83000000-0000-4000-8000-000000000300','83000000-0000-4000-8000-000000000012',
			'read_evidence','workspace_analysis.read','running',1,'{}','wa-worker',now()+interval '5 minutes',
			'wa-read-deadline',1,1,1,1,now(),now()
		);
		INSERT INTO workflow.node_attempt(
			id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
		) VALUES (
			'83000000-0000-4000-8000-000000000301','83000000-0000-4000-8000-000000000300',
			1,1,0,'wa-read-deadline-1','wa-worker',now()+interval '5 minutes','running',now()
		)`); err != nil {
			return err
		}
	}

	callID := fmt.Sprintf("83000000-0000-4000-8000-0000000003%02d", 10+ordinal)
	operationID := fmt.Sprintf("83000000-0000-4000-8000-0000000003%02d", 20+ordinal)
	reservationID := fmt.Sprintf("83000000-0000-4000-8000-0000000003%02d", 30+ordinal)
	receiptID := fmt.Sprintf("83000000-0000-4000-8000-0000000003%02d", 40+ordinal)
	output := fmt.Sprintf(`{"content_hash":"%s","evidence_ref":"%s","excerpt":"Evidence %s","truncated":false}`,
		strings.Repeat(string(rune('a'+ordinal-1)), 64), evidenceRef, evidenceRef)
	binding := workspaceAnalysisDeadlineSourceBinding(ordinal, evidenceRef)
	statement := fmt.Sprintf(`
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '%s','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000300',
    'read_evidence','SOURCE_READ',%d,'TOOL',repeat('%d',64),'PENDING',1,now(),now()
);
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '%s','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000300','83000000-0000-4000-8000-000000000301',%d,
    'ReadSource',3,'d41dabac535261b885e453b64e11fe5bb779838e31a255aea4798e27ead54d32',
    'tool.read_source.input',2,'tool.read_source.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('%d',64),2,'{}','STARTED',false,now(),0,1
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000301',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000301',tool_call_id='%s',
    version=2,started_at=authorized.at,updated_at=authorized.at
FROM (SELECT clock_timestamp() AS at) AS authorized
WHERE id='%s';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,
    reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '%s','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014',
    '%s','TOOL','%s','RESERVED',0,1,1,0,0,now()
);
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=reserved_tool_calls+1,reserved_source_reads=reserved_source_reads+1,
    version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE workflow.tool_call SET
    status='SUCCEEDED',response_hash=encode(sha256(convert_to($1,'UTF8')),'hex'),
    response_bytes=octet_length(convert_to($1,'UTF8')),response_summary='{}',
    completed_at=clock_timestamp(),duration_ms=10,version=2
WHERE id='%s';
INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,
    max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
    server_binding_schema_id,server_binding_schema_version,server_binding_document,
    server_binding_hash,server_binding_bytes,created_at
) SELECT
    '%s',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,call.node_attempt_id,
    call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,
    call.definition_hash,'PERSIST_CANONICAL',8192,4096,convert_to($1,'UTF8'),
    call.response_hash,call.response_bytes,'tool.read_source.private_binding',1,convert_to($2,'UTF8'),
    encode(sha256(convert_to($2,'UTF8')),'hex'),octet_length(convert_to($2,'UTF8')),call.completed_at+interval '1 millisecond'
FROM workflow.tool_call AS call WHERE call.id='%s';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_source_reads=1,settled_at=clock_timestamp()
WHERE id='%s';
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=reserved_tool_calls-1,reserved_source_reads=reserved_source_reads-1,
    settled_tool_calls=settled_tool_calls+1,settled_source_reads=settled_source_reads+1,
    version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id='%s',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='%s'),
    version=3,completed_at=completed.at,updated_at=completed.at
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='%s'`,
		operationID, ordinal, ordinal,
		callID, ordinal, ordinal,
		callID, operationID,
		reservationID, operationID, callID,
		callID,
		receiptID, callID,
		reservationID,
		receiptID, receiptID, operationID,
	)
	if _, err := tx.Exec(ctx, statement, pgx.QueryExecModeSimpleProtocol, output, binding); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func workspaceAnalysisDeadlineSourceBinding(ordinal int, evidenceRef string) string {
	contentHash := strings.Repeat(string(rune('a'+ordinal-1)), 64)
	identities := []struct {
		citationID      string
		chunkID         string
		sourceVersionID string
		sourceSpanID    string
	}{
		{"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23", "83000000-0000-4000-8000-000000000381", "83000000-0000-4000-8000-000000000361", "83000000-0000-4000-8000-000000000391"},
		{"cite-9f8de041b3a60b404389373fc12a6f35e8f2bb79b18e1a44b2e5c90aa49d29fa", "83000000-0000-4000-8000-000000000382", "83000000-0000-4000-8000-000000000362", "83000000-0000-4000-8000-000000000392"},
		{"cite-454c22b810f30b6ceaaf8cd6e9148a9cb9d8c2be27eb176f2e6cc03f0e4d8e11", "83000000-0000-4000-8000-000000000383", "83000000-0000-4000-8000-000000000363", "83000000-0000-4000-8000-000000000393"},
	}
	identity := identities[ordinal-1]
	return fmt.Sprintf(`{"chunk_id":"%s","citation_id":"%s","content_hash":"%s","evidence_ref":"%s","index_version_id":"83000000-0000-4000-8000-000000000371","search_receipt_hash":"%s","search_receipt_id":"83000000-0000-4000-8000-000000000045","source_span_id":"%s","source_version_id":"%s"}`,
		identity.chunkID, identity.citationID, contentHash, evidenceRef,
		receiptRepositoryHash(json.RawMessage(workspaceAnalysisDeadlineSearchOutput)),
		identity.sourceSpanID, identity.sourceVersionID)
}

func TestWorkspaceAnalysisPreoperationDeadlineFinalizerRejectsUnsafeAuthority(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name           string
		operationState string
	}{
		{name: "remaining time covers frozen timeout"},
		{name: "started operation exists", operationState: "STARTED"},
		{name: "failed operation exists", operationState: "FAILED"},
		{name: "unknown operation exists", operationState: "UNKNOWN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := newWorkspaceAnalysisDeadlineFixture(
				t, ctx,
				"now()",
				"now()+interval '13 minutes 50 seconds'",
			)
			if test.operationState != "" {
				startWorkspaceAnalysisRun(t, ctx, pool)
				authorizeWorkspaceAnalysisGitOperation(t, ctx, pool)
				switch test.operationState {
				case "FAILED":
					failWorkspaceAnalysisGitReceipt(t, ctx, pool)
				case "UNKNOWN":
					markWorkspaceAnalysisGitOperationUnknown(t, ctx, pool)
				}
			} else {
				insertWorkspaceAnalysisDeadlineNode(
					t, ctx, pool,
					workspaceAnalysisDeadlineInspectNodeID,
					workspaceAnalysisDeadlineInspectTryID,
					"inspect_workspace",
					"workspace_analysis.inspect",
				)
			}
			runtime := openWorkspaceAnalysisDeadlineRuntime(t, ctx, pool)
			t.Cleanup(runtime.Close)
			pool = runtime.DB()
			finalizer := newWorkspaceAnalysisDeadlineFinalizer(
				t, runtime, foundation.NewUUIDGenerator(nil),
			)
			_, replayed, err := finalizer.FinalizeTermination(ctx, workspaceAnalysisDeadlineCommand(
				workspaceAnalysisDeadlineInspectNodeID,
				workspaceAnalysisDeadlineInspectTryID,
			))
			if err == nil || replayed {
				t.Fatalf("unsafe deadline replayed=%t err=%v", replayed, err)
			}
			var proofCount int64
			var runStatus, answerStatus string
			if err := pool.QueryRow(ctx, `SELECT r.status,a.publication_status,
				(SELECT count(*) FROM agent.workspace_analysis_termination_proof p WHERE p.analysis_run_id=r.id)
				FROM agent.workspace_analysis_run r
				JOIN agent.answer a ON a.id=r.answer_id
				WHERE r.id=$1`, string(workspaceAnalysisDeadlineAnalysisRunID)).Scan(
				&runStatus, &answerStatus, &proofCount,
			); err != nil {
				t.Fatal(err)
			}
			wantRunStatus := "queued"
			if test.operationState != "" {
				wantRunStatus = "running"
			}
			if runStatus != wantRunStatus || answerStatus != "pending" || proofCount != 0 {
				t.Fatalf("run=%s answer=%s proof=%d", runStatus, answerStatus, proofCount)
			}
		})
	}
}

func markWorkspaceAnalysisGitOperationUnknown(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE workflow.tool_call SET
			status='UNKNOWN',error_code='WORKSPACE_ANALYSIS_RESULT_UNKNOWN',retryable=false,
			version=2,completed_at=terminal.at,
			duration_ms=GREATEST(0,floor(extract(epoch FROM (terminal.at-started_at))*1000)::bigint)
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000023';
		UPDATE agent.workspace_analysis_budget_reservation SET
			status='UNKNOWN_CHARGED',settled_tool_calls=1,
			settled_source_reads=reserved_source_reads,settled_input_tokens=0,settled_output_tokens=0,
			settled_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000024';
		UPDATE agent.workspace_analysis_run SET
			reserved_tool_calls=reserved_tool_calls-1,
			settled_tool_calls=settled_tool_calls+1,
			version=version+1,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014';
		UPDATE agent.workspace_analysis_operation SET
			status='UNKNOWN',error_code='WORKSPACE_ANALYSIS_RESULT_UNKNOWN',version=3,
			completed_at=terminal.at,updated_at=terminal.at
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000022'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceAnalysisDeadlineMigrationRepeatUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	t.Cleanup(cleanup)
	if err := MigrateAtlasToVersion(ctx, pool, 85); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if err := MigrateAtlasToVersion(ctx, pool, 86); err != nil {
		t.Fatalf("apply 00086: %v", err)
	}
	if err := MigrateAtlasToVersion(ctx, pool, 86); err != nil {
		t.Fatalf("repeat 00086 Up: %v", err)
	}
}

func newWorkspaceAnalysisDeadlineFixture(
	t *testing.T,
	ctx context.Context,
	runCreatedAtSQL string,
	deadlineSQL string,
) *pgxpool.Pool {
	t.Helper()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	t.Cleanup(cleanup)
	if err := MigrateAtlasToVersion(ctx, pool, 85); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if err := MigrateAtlasToVersion(ctx, pool, 86); err != nil {
		t.Fatalf("apply 00086: %v", err)
	}
	insertWorkspaceAnalysisRunFixtureWithLimits(
		t, ctx, pool, runCreatedAtSQL, deadlineSQL, 5376,
	)
	return pool
}

func insertWorkspaceAnalysisDeadlineNode(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	nodeRunID, nodeAttemptID foundation.ID,
	nodeKey, nodeType string,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
		idempotency_key,input_schema_version,output_schema_version,dispatch_no,
		version,created_at,updated_at
	) VALUES($1,$2,$3,$4,'running',1,'{}','wa-worker',clock_timestamp()+interval '5 minutes',
		$3||'-deadline',1,1,1,1,clock_timestamp(),clock_timestamp())`,
		string(nodeRunID), string(workspaceAnalysisDeadlineWorkflowRunID), nodeKey, nodeType,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,
		lease_owner,lease_until,status,started_at
	) SELECT $3,$1,1,1,0,$2||'-deadline-1','wa-worker',lease_until,'running',clock_timestamp()
	FROM workflow.node_run WHERE id=$1`,
		string(nodeRunID), nodeKey, string(nodeAttemptID),
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertWorkspaceAnalysisDeadlineDraft(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO agent.answer_draft_session(
		id,workspace_id,answer_id,workflow_run_id,node_run_id,node_attempt_id,
		attempt_no,lease_owner,generation,status,next_sequence,total_bytes,
		expires_at,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,1,'wa-worker',1,'ACTIVE',1,0,
		clock_timestamp()+interval '30 minutes',clock_timestamp(),clock_timestamp())`,
		string(workspaceAnalysisDeadlineDraftID), string(workspaceAnalysisDeadlineWorkspaceID),
		string(workspaceAnalysisDeadlineAnswerID), string(workspaceAnalysisDeadlineWorkflowRunID),
		string(workspaceAnalysisDeadlineInspectNodeID), string(workspaceAnalysisDeadlineInspectTryID),
	); err != nil {
		t.Fatal(err)
	}
}

// Runtime finalizers use the current persisted row shape. The migration-only
// scenarios retain their explicit 00085/00086 upgrade and replay boundaries.
func openWorkspaceAnalysisDeadlineRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *platformpostgres.Pool {
	t.Helper()
	if err := MigrateAtlasToVersion(ctx, pool, 0); err != nil {
		t.Fatalf("apply current WorkspaceAnalysis runtime schema: %v", err)
	}
	return openMigrationRuntimePool(t, ctx, pool)
}

func newWorkspaceAnalysisDeadlineFinalizer(
	t *testing.T,
	pool *platformpostgres.Pool,
	ids foundation.IDGenerator,
) *conversationpostgres.GORMWorkspaceAnalysisFinalizer {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := conversationpostgres.NewGORMWorkspaceAnalysisFinalizer(pool, events, ids)
	if err != nil {
		t.Fatal(err)
	}
	return finalizer
}

func workspaceAnalysisDeadlineCommand(
	nodeRunID, nodeAttemptID foundation.ID,
) conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand {
	return conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: conversationapplication.WorkspaceAnalysisPublicationLookup{
			AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
				WorkspaceID: workspaceAnalysisDeadlineWorkspaceID, WorkflowRunID: workspaceAnalysisDeadlineWorkflowRunID,
				NodeRunID: nodeRunID, NodeAttemptID: nodeAttemptID,
				ConversationID: workspaceAnalysisDeadlineConversationID, QuestionID: workspaceAnalysisDeadlineQuestionID,
				AnswerID: workspaceAnalysisDeadlineAnswerID,
			},
			AnalysisRunID:      workspaceAnalysisDeadlineAnalysisRunID,
			ExpectedLeaseOwner: "wa-worker",
			ExpectedLeaseFence: 1,
		},
		ExpectedAnswerVersion: 1,
		Reason:                agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
	}
}

type workspaceAnalysisDeadlineOneShotIDs struct {
	calls int
}

func (ids *workspaceAnalysisDeadlineOneShotIDs) New() (foundation.ID, error) {
	ids.calls++
	if ids.calls > 1 {
		return "", errors.New("deadline replay must not allocate another proof id")
	}
	return workspaceAnalysisDeadlineProofID, nil
}

// armMigrationCommitResponseLoss drops only the first successful commit response.
// The stdlib connector borrows the existing physical pool and preserves the
// *sql.Tx required by the production UnitOfWork and scoped participants.
func armMigrationCommitResponseLoss(t *testing.T, pool *platformpostgres.Pool) {
	t.Helper()
	database, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	connector := &migrationCommitResponseLossConnector{Connector: stdlib.GetPoolConnector(pool.DB())}
	injected := sql.OpenDB(connector)
	// Idle connections belong to pgxpool, matching stdlib.OpenDBFromPool.
	injected.SetMaxIdleConns(0)
	rootPool, statementPool := database.ConnPool, database.Statement.ConnPool
	database.ConnPool, database.Statement.ConnPool = injected, injected
	t.Cleanup(func() {
		database.ConnPool, database.Statement.ConnPool = rootPool, statementPool
		if err := injected.Close(); err != nil {
			t.Errorf("close migration response-loss SQL facade: %v", err)
		}
		if !connector.lost.Load() {
			t.Error("migration commit response-loss injection was not exercised")
		}
	})
}

type migrationCommitResponseLossConnector struct {
	driver.Connector
	lost atomic.Bool
}

func (connector *migrationCommitResponseLossConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := connector.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	stdlibConnection, ok := connection.(*stdlib.Conn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("migration response-loss connection is %T, want *stdlib.Conn", connection)
	}
	return &migrationCommitResponseLossConnection{Conn: stdlibConnection, lost: &connector.lost}, nil
}

type migrationCommitResponseLossConnection struct {
	*stdlib.Conn
	lost *atomic.Bool
}

func (connection *migrationCommitResponseLossConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *migrationCommitResponseLossConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	transaction, err := connection.Conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &migrationCommitResponseLossTransaction{Tx: transaction, lost: connection.lost}, nil
}

type migrationCommitResponseLossTransaction struct {
	driver.Tx
	lost *atomic.Bool
}

func (transaction *migrationCommitResponseLossTransaction) Commit() error {
	if err := transaction.Tx.Commit(); err != nil {
		return err
	}
	if !transaction.lost.Swap(true) {
		return errors.New("injected migration commit response loss")
	}
	return nil
}

func workspaceAnalysisDeadlineErrorChain(err error) string {
	parts := make([]string, 0, 4)
	for err != nil {
		parts = append(parts, err.Error())
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, ": ")
}
