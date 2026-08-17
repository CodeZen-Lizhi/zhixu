//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const workspaceAnalysisMigrationTable = "goose_workspace_analysis_dependency_version"

const workspaceAnalysisRetrievalPlanDocument = `{"result_type":"workspace_analysis_plan","schema_id":"agent.workspace-analysis-plan","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000034","payload":{"intent":"inspect","requires_clarification":false,"rewrites":["analyze workspace"],"clarification_reason":"","clarification_question":"","suggested_scopes":[]}}`

const workspaceAnalysisClarificationPlanDocument = `{"result_type":"workspace_analysis_plan","schema_id":"agent.workspace-analysis-plan","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000034","payload":{"intent":"clarify environment","requires_clarification":true,"rewrites":[],"clarification_reason":"Target environment is ambiguous.","clarification_question":"Which environment should be analyzed?","suggested_scopes":["production","staging"]}}`

const workspaceAnalysisClarificationDocument = `{"result_type":"clarification","schema_id":"conversation.clarification","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000034","payload":{"reason":"Target environment is ambiguous.","question":"Which environment should be analyzed?","suggested_scopes":["production","staging"]}}`

const workspaceAnalysisMismatchedClarificationDocument = `{"result_type":"clarification","schema_id":"conversation.clarification","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000034","payload":{"reason":"Target environment is ambiguous.","question":"Which branch should be analyzed?","suggested_scopes":["production","staging"]}}`

func TestWorkspaceAnalysisPersistenceMigrationBackfillEmptyDownAndGuard(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	assertWorkspaceAnalysisMigrationShape(t, ctx, pool)
	var mode string
	if err := pool.QueryRow(ctx, `SELECT mode FROM agent.question WHERE id='83000000-0000-4000-8000-000000000003'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "rag" {
		t.Fatalf("legacy question mode=%q", mode)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent.question(
			id,workspace_id,conversation_id,ordinal,question_text,scope,answer_depth,output_format,
			context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
		) VALUES (
			'83000000-0000-4000-8000-000000000004','83000000-0000-4000-8000-000000000001',
			'83000000-0000-4000-8000-000000000002',99,'rolling legacy question','{}','standard','markdown',
			1,repeat('d',64),'wa-rolling-legacy-question',repeat('e',64),now()
		) RETURNING mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "rag" {
		t.Fatalf("old-binary-compatible question mode=%q", mode)
	}
	if results, err := provider.Up(ctx); err != nil || len(results) != 0 {
		t.Fatalf("repeat Up results=%d error=%v", len(results), err)
	}

	if _, err := provider.ApplyVersion(ctx, 85, false); err != nil {
		t.Fatalf("empty 00085 Down: %v", err)
	}
	var modeColumn int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='agent' AND table_name='question' AND column_name='mode'`).Scan(&modeColumn); err != nil {
		t.Fatal(err)
	}
	if modeColumn != 0 {
		t.Fatalf("question mode column survived empty Down: %d", modeColumn)
	}
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("00085 Up after empty Down: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT mode FROM agent.question WHERE id='83000000-0000-4000-8000-000000000003'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "rag" {
		t.Fatalf("legacy question mode after reapply=%q", mode)
	}

	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	_, err := provider.ApplyVersion(ctx, 85, false)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("guarded Down error=%v", err)
	}
	assertWorkspaceAnalysisMigrationShape(t, ctx, pool)
	var applied bool
	if err := pool.QueryRow(ctx, `SELECT is_applied FROM `+workspaceAnalysisMigrationTable+`
		WHERE version_id=85 ORDER BY id DESC LIMIT 1`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("guarded Down cleared the applied version")
	}
}

func TestWorkspaceAnalysisPersistenceMigrationReceiptAndBudgetTransaction(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		t.Fatal(err)
	}
	authorizeWorkspaceAnalysisGitOperation(t, ctx, pool)

	driftTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driftTx.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET reserved_tool_calls=0,settled_tool_calls=1,version=4,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		_ = driftTx.Rollback(ctx)
		t.Fatal(err)
	}
	err = driftTx.Commit(ctx)
	assertPostgresCode(t, err, "55000")

	completeWorkspaceAnalysisGitOperation(t, ctx, pool)
	var reservedToolCalls, settledToolCalls int
	if err := pool.QueryRow(ctx, `SELECT reserved_tool_calls,settled_tool_calls
		FROM agent.workspace_analysis_run
		WHERE id='83000000-0000-4000-8000-000000000014'`).Scan(&reservedToolCalls, &settledToolCalls); err != nil {
		t.Fatal(err)
	}
	if reservedToolCalls != 0 || settledToolCalls != 1 {
		t.Fatalf("tool budget reserved=%d settled=%d", reservedToolCalls, settledToolCalls)
	}
	terminalTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminalTx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status='cancelled',termination_reason='WORKSPACE_ANALYSIS_CANCELLED',
		version=5,updated_at=terminal.at,completed_at=terminal.at
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		_ = terminalTx.Rollback(ctx)
		t.Fatal(err)
	}
	err = terminalTx.Commit(ctx)
	assertPostgresCode(t, err, "55000")

	unsupportedTerminalTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unsupportedTerminalTx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status='refused',termination_reason='WORKSPACE_ANALYSIS_CITATION_INVALID',
		version=5,updated_at=terminal.at,completed_at=terminal.at
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000014';
	UPDATE agent.answer SET
		publication_status='refused',result_type='workspace_analysis_refusal',
		result='{"result_type":"workspace_analysis_refusal","schema_id":"conversation.workspace_analysis_refusal","schema_version":"v1","model_run_ref":null,"payload":{"reason_code":"WORKSPACE_ANALYSIS_CITATION_INVALID","summary":"Citation validation failed."}}',
		result_hash=repeat('a',64),version=2,updated_at=published.at,published_at=published.at
		FROM (SELECT clock_timestamp() AS at) AS published
		WHERE id='83000000-0000-4000-8000-000000000013'`)
	if err != nil {
		_ = unsupportedTerminalTx.Rollback(ctx)
		assertPostgresCode(t, err, "55000")
	} else {
		err = unsupportedTerminalTx.Commit(ctx)
		assertPostgresCode(t, err, "55000")
	}

	_, err = pool.Exec(ctx, `UPDATE workflow.tool_result_receipt
		SET output_hash=repeat('f',64)
		WHERE id='83000000-0000-4000-8000-000000000025'`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM workflow.tool_result_receipt
		WHERE id='83000000-0000-4000-8000-000000000025'`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE agent.workspace_analysis_operation
		SET request_hash=repeat('f',64),version=4,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000022'`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `TRUNCATE agent.workspace_analysis_budget_reservation`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status='cancelled',termination_reason='WORKSPACE_ANALYSIS_TOOL_FAILED',
		version=5,updated_at=clock_timestamp(),completed_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014'`)
	assertPostgresCode(t, err, "23514")

	var candidateRunConstraint string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid='agent.workspace_analysis_candidate'::regclass
		  AND conname='fk_workspace_analysis_candidate_run'`).Scan(&candidateRunConstraint); err != nil {
		t.Fatal(err)
	}
	if candidateRunConstraint != "FOREIGN KEY (analysis_run_id, workspace_id, answer_id) REFERENCES agent.workspace_analysis_run(id, workspace_id, answer_id) ON DELETE RESTRICT" {
		t.Fatalf("candidate run constraint=%q", candidateRunConstraint)
	}
}

func TestWorkspaceAnalysisPersistenceMigrationModelResultReplayReceipt(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		t.Fatal(err)
	}
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)

	var resultKind string
	if err := pool.QueryRow(ctx, `SELECT result_kind
		FROM agent.workspace_analysis_operation
		WHERE id='83000000-0000-4000-8000-000000000032'`).Scan(&resultKind); err != nil {
		t.Fatal(err)
	}
	if resultKind != "MODEL_RESULT_RECEIPT" {
		t.Fatalf("planner result kind=%q", resultKind)
	}
	_, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_model_result
		SET document_hash=repeat('f',64)
		WHERE id='83000000-0000-4000-8000-000000000036'`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM agent.workspace_analysis_model_result
		WHERE id='83000000-0000-4000-8000-000000000036'`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `TRUNCATE agent.workspace_analysis_publication_proof, agent.workspace_analysis_model_result`)
	assertPostgresCode(t, err, "55000")
}

func TestWorkspaceAnalysisPersistenceMigrationClarificationBindsPlannerReceipt(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		t.Fatal(err)
	}
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisClarificationPlanDocument)

	err := publishWorkspaceAnalysisClarification(ctx, pool, workspaceAnalysisMismatchedClarificationDocument)
	assertPostgresCode(t, err, "55000")
	if err := publishWorkspaceAnalysisClarification(ctx, pool, workspaceAnalysisClarificationDocument); err != nil {
		t.Fatalf("publish planner clarification: %v", err)
	}
	var runStatus, answerStatus string
	if err := pool.QueryRow(ctx, `SELECT analysis.status,answer.publication_status
		FROM agent.workspace_analysis_run AS analysis
		JOIN agent.answer AS answer ON answer.id=analysis.answer_id
		WHERE analysis.id='83000000-0000-4000-8000-000000000014'`).Scan(&runStatus, &answerStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "clarification_required" || answerStatus != "clarification_required" {
		t.Fatalf("clarification states run=%q answer=%q", runStatus, answerStatus)
	}
}

func TestWorkspaceAnalysisPersistenceMigrationRejectsOutOfOrderAuthorization(t *testing.T) {
	ctx := context.Background()
	t.Run("plan requires git", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixture(t, ctx, pool)
		startWorkspaceAnalysisRun(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		assertPostgresCode(t, prepareWorkspaceAnalysisPlanOperationError(ctx, tx), "55000")
	})

	t.Run("clarification plan blocks search", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixture(t, ctx, pool)
		startWorkspaceAnalysisRun(t, ctx, pool)
		completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
		authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
		completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisClarificationPlanDocument)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		assertPostgresCode(t, authorizeWorkspaceAnalysisSearchOperationError(ctx, tx), "55000")
	})
}

func TestWorkspaceAnalysisPersistenceMigrationRejectsPartialModelCompletion(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)

	_, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		status='SUCCEEDED',response_hash=encode(sha256(convert_to($1,'UTF8')),'hex'),
		response_bytes=octet_length(convert_to($1,'UTF8')),input_tokens=10,output_tokens=5,
		latency_ms=10,version=2,completed_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000035'`, workspaceAnalysisRetrievalPlanDocument)
	assertPostgresCode(t, err, "55000")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	completeWorkspaceAnalysisPlanCallAndResult(t, ctx, tx, workspaceAnalysisRetrievalPlanDocument)
	assertPostgresCode(t, tx.Commit(ctx), "55000")
}

func TestWorkspaceAnalysisPersistenceMigrationRejectsMismatchedModelReservation(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)

	for _, test := range []struct {
		name         string
		inputTokens  int
		outputTokens int
		cost         any
		code         string
	}{
		{name: "plan output", inputTokens: 65536, outputTokens: 255, code: "55000"},
		{name: "model input", inputTokens: 65535, outputTokens: 256, code: "55000"},
		{name: "untrusted cost", inputTokens: 65536, outputTokens: 256, cost: int64(1), code: "55000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			prepareWorkspaceAnalysisPlanOperation(t, ctx, tx)
			_, err = tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_budget_reservation(
				id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,
				reserved_model_calls,reserved_tool_calls,reserved_source_reads,
				reserved_input_tokens,reserved_output_tokens,reserved_cost_microunits,created_at
			) VALUES (
				'83000000-0000-4000-8000-000000000033','83000000-0000-4000-8000-000000000001',
				'83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000032',
				'MODEL','83000000-0000-4000-8000-000000000035','RESERVED',1,0,0,$1,$2,$3,now()
			)`, test.inputTokens, test.outputTokens, test.cost)
			assertPostgresCode(t, err, test.code)
		})
	}
}

func TestWorkspaceAnalysisPersistenceMigrationAllowsFirstReviewCallAndGuardsDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertIndependentReviewFirstCallFixture(t, ctx, pool)

	var phase string
	if err := pool.QueryRow(ctx, `SELECT phase FROM agent.model_call
		WHERE id='83000000-0000-4000-8000-000000000046'`).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "REVIEW" {
		t.Fatalf("first review call phase=%q", phase)
	}
	_, err := provider.ApplyVersion(ctx, 85, false)
	assertPostgresCode(t, err, "55000")
}

func TestWorkspaceAnalysisPersistenceMigrationBudgetExhaustionProofRequiresCausalFixedOverage(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixtureWithLimits(t, ctx, pool, "now()", "now()+interval '13 minutes 50 seconds'", 4096)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	insertPendingWorkspaceAnalysisPlanOperation(t, ctx, pool)

	assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", operationID: "83000000-0000-4000-8000-000000000032",
		requestedModelCalls: 1, requestedInputTokens: 65536, requestedOutputTokens: 255,
	}, "55000")
	assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", operationID: "83000000-0000-4000-8000-000000000032",
		requestedModelCalls: 1, requestedInputTokens: 1, requestedOutputTokens: 256,
	}, "55000")
	assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", operationID: "83000000-0000-4000-8000-000000000032",
		requestedModelCalls: 1, requestedInputTokens: 65536, requestedOutputTokens: 256,
	}, "55000")
	costOnly := int64(1)
	assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", operationID: "83000000-0000-4000-8000-000000000032",
		requestedModelCalls: 1, requestedInputTokens: 65536, requestedOutputTokens: 256,
		requestedCostMicrounits: &costOnly,
	}, "55000")

	t.Run("prior failed operation is not budget exhaustion", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()

		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixtureWithLimits(t, ctx, pool, "now()", "now()+interval '13 minutes 50 seconds'", 4096)
		startWorkspaceAnalysisRun(t, ctx, pool)
		authorizeWorkspaceAnalysisGitOperation(t, ctx, pool)
		failWorkspaceAnalysisGitReceipt(t, ctx, pool)
		insertPendingWorkspaceAnalysisPlanOperation(t, ctx, pool)

		assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", operationID: "83000000-0000-4000-8000-000000000032",
			requestedModelCalls: 1, requestedInputTokens: 65536, requestedOutputTokens: 256,
		}, "55000")
	})

}

func TestWorkspaceAnalysisPersistenceMigrationDeadlineAndReceiptProofsRejectInvalidEvidence(t *testing.T) {
	ctx := context.Background()
	t.Run("deadline before limit", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixture(t, ctx, pool)
		startWorkspaceAnalysisRun(t, ctx, pool)
		completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
		insertPendingWorkspaceAnalysisPlanOperation(t, ctx, pool)
		assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", operationID: "83000000-0000-4000-8000-000000000032",
		}, "55000")
	})

	t.Run("expired deadline closes with pending operation", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixtureWithLimits(t, ctx, pool, "now()-interval '15 minutes'", "now()-interval '70 seconds'", 5376)
		startWorkspaceAnalysisRun(t, ctx, pool)
		insertPendingWorkspaceAnalysisGitOperation(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		insertWorkspaceAnalysisTerminationProof(t, ctx, tx, workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", operationID: "83000000-0000-4000-8000-000000000022",
			terminalNodeRunID: "83000000-0000-4000-8000-000000000020", terminalNodeAttemptID: "83000000-0000-4000-8000-000000000021",
		})
		publishWorkspaceAnalysisFailure(t, ctx, tx, "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("close deadline termination: %v", err)
		}
	})

	t.Run("future deadline without durable completion margin closes", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixtureWithLimits(t, ctx, pool, "now()-interval '12 minutes'", "now()+interval '110 seconds'", 5376)
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
			t.Fatalf("close pre-invocation deadline termination: %v", err)
		}
	})

	t.Run("future deadline without durable completion margin refuses authorization", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixtureWithLimits(t, ctx, pool, "now()-interval '12 minutes'", "now()+interval '110 seconds'", 5376)
		startWorkspaceAnalysisRun(t, ctx, pool)
		completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		prepareWorkspaceAnalysisPlanOperation(t, ctx, tx)
		_, err = tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_budget_reservation(
			id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,
			reserved_model_calls,reserved_tool_calls,reserved_source_reads,
			reserved_input_tokens,reserved_output_tokens,created_at
		) VALUES (
			'83000000-0000-4000-8000-000000000033','83000000-0000-4000-8000-000000000001',
			'83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000032',
			'MODEL','83000000-0000-4000-8000-000000000035','RESERVED',1,0,0,65536,256,now()
		)`)
		assertPostgresCode(t, err, "55000")
	})

	t.Run("successful call crosses deadline before durable finalization", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixtureWithTimeouts(
			t, ctx, pool,
			"statement_timestamp()-interval '64.009 seconds'",
			"statement_timestamp()+interval '6 seconds'",
			5376, 1, 1, 1, 1, 1, 1, 1,
		)
		startWorkspaceAnalysisRun(t, ctx, pool)
		completeWorkspaceAnalysisGitPrefix(t, ctx, pool)

		proof := workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", operationID: "83000000-0000-4000-8000-000000000022",
			terminalNodeRunID: "83000000-0000-4000-8000-000000000020", terminalNodeAttemptID: "83000000-0000-4000-8000-000000000021",
		}
		assertTerminationProofPostgresCode(t, ctx, pool, proof, "55000")
		if _, err := pool.Exec(ctx, `SELECT pg_sleep(GREATEST(
			EXTRACT(EPOCH FROM deadline_at-clock_timestamp())+0.1, 0
		)) FROM agent.workspace_analysis_run
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
			t.Fatal(err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		insertWorkspaceAnalysisTerminationProof(t, ctx, tx, proof)
		publishWorkspaceAnalysisFailure(t, ctx, tx, "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("close post-call deadline termination: %v", err)
		}
	})

	t.Run("receipt expected hash and failure code", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixture(t, ctx, pool)
		startWorkspaceAnalysisRun(t, ctx, pool)
		authorizeWorkspaceAnalysisGitOperation(t, ctx, pool)
		failWorkspaceAnalysisGitReceipt(t, ctx, pool)
		var responseHash string
		if err := pool.QueryRow(ctx, `SELECT response_hash FROM workflow.tool_call
				WHERE id='83000000-0000-4000-8000-000000000023'`).Scan(&responseHash); err != nil {
			t.Fatal(err)
		}
		assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_RECEIPT_INVALID", operationID: "83000000-0000-4000-8000-000000000022",
			receiptFailureCode: "HASH_MISMATCH", receiptFailureID: "83000000-0000-4000-8000-000000000026",
			expectedHash: strings.Repeat("a", 64), actualHash: strings.Repeat("b", 64),
			terminalNodeRunID: "83000000-0000-4000-8000-000000000020", terminalNodeAttemptID: "83000000-0000-4000-8000-000000000021",
		}, "55000")
		assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_RECEIPT_INVALID", operationID: "83000000-0000-4000-8000-000000000022",
			receiptFailureCode: "MISSING", receiptFailureID: "83000000-0000-4000-8000-000000000026",
			expectedHash: responseHash, actualHash: strings.Repeat("d", 64),
			terminalNodeRunID: "83000000-0000-4000-8000-000000000020", terminalNodeAttemptID: "83000000-0000-4000-8000-000000000021",
		}, "55000")

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		insertWorkspaceAnalysisTerminationProof(t, ctx, tx, workspaceAnalysisTerminationProof{
			reason: "WORKSPACE_ANALYSIS_RECEIPT_INVALID", operationID: "83000000-0000-4000-8000-000000000022",
			receiptFailureCode: "MISSING", receiptFailureID: "83000000-0000-4000-8000-000000000026",
			expectedHash:      responseHash,
			terminalNodeRunID: "83000000-0000-4000-8000-000000000020", terminalNodeAttemptID: "83000000-0000-4000-8000-000000000021",
		})
		publishWorkspaceAnalysisFailure(t, ctx, tx, "WORKSPACE_ANALYSIS_RECEIPT_INVALID")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("close receipt failure termination: %v", err)
		}

		_, err = pool.Exec(ctx, `INSERT INTO workflow.tool_result_receipt(
				id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
				output_schema_id,output_schema_version,definition_hash,persistence_policy,
				max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,created_at
			) SELECT
				'83000000-0000-4000-8000-000000000025',id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
				requested_tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,'PERSIST_CANONICAL',
				4096,1024,convert_to('{"branch":"main","head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","clean":true,"staged_count":0,"unstaged_count":0,"untracked_count":0,"conflict_count":0}','UTF8'),
				response_hash,response_bytes,clock_timestamp()
			FROM workflow.tool_call WHERE id='83000000-0000-4000-8000-000000000023'`)
		assertPostgresCode(t, err, "55000")
	})
}

func TestWorkspaceAnalysisPersistenceMigrationDeterministicRefusalsUseExactUpstreamProofs(t *testing.T) {
	ctx := context.Background()

	t.Run("empty search is finalized by the current read node", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()

		provider := workspaceAnalysisMigrationProvider(t, pool)
		insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
		if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
			t.Fatalf("apply 00085: %v", err)
		}
		insertWorkspaceAnalysisRunFixture(t, ctx, pool)
		startWorkspaceAnalysisRun(t, ctx, pool)
		completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
		authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
		completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)
		completeWorkspaceAnalysisEmptySearchAndStartRead(t, ctx, pool)

		searchHash := workspaceAnalysisToolReceiptHash(
			t, ctx, pool, "83000000-0000-4000-8000-000000000045",
		)
		proof := workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000052",
			reason:                "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT",
			operationID:           "83000000-0000-4000-8000-000000000042",
			artifactID:            "83000000-0000-4000-8000-000000000045",
			artifactHash:          searchHash,
			terminalNodeRunID:     "83000000-0000-4000-8000-000000000050",
			terminalNodeAttemptID: "83000000-0000-4000-8000-000000000051",
		}

		assertWorkspaceAnalysisDeterministicRefusalInsertError(t, ctx, pool, workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000053",
			reason:                proof.reason,
			operationID:           proof.operationID,
			artifactID:            proof.artifactID,
			artifactHash:          proof.artifactHash,
			terminalNodeRunID:     "83000000-0000-4000-8000-000000000030",
			terminalNodeAttemptID: "83000000-0000-4000-8000-000000000031",
		}, "workspace analysis termination operation binding is invalid")

		assertWorkspaceAnalysisDeterministicRefusalInsertError(t, ctx, pool, workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000054",
			reason:                proof.reason,
			operationID:           proof.operationID,
			artifactID:            proof.artifactID,
			artifactHash:          strings.Repeat("f", 64),
			terminalNodeRunID:     proof.terminalNodeRunID,
			terminalNodeAttemptID: proof.terminalNodeAttemptID,
		}, "workspace analysis termination receipt binding is invalid")

		expiredTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := expiredTx.Exec(ctx, `UPDATE workflow.node_attempt
			SET lease_until=clock_timestamp()-interval '1 second'
			WHERE id='83000000-0000-4000-8000-000000000051';
			UPDATE workflow.node_run
			SET lease_until=clock_timestamp()-interval '1 second',updated_at=clock_timestamp()
			WHERE id='83000000-0000-4000-8000-000000000050'`); err != nil {
			_ = expiredTx.Rollback(ctx)
			t.Fatal(err)
		}
		err = insertWorkspaceAnalysisDeterministicRefusalProof(ctx, expiredTx, workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000055",
			reason:                proof.reason,
			operationID:           proof.operationID,
			artifactID:            proof.artifactID,
			artifactHash:          proof.artifactHash,
			terminalNodeRunID:     proof.terminalNodeRunID,
			terminalNodeAttemptID: proof.terminalNodeAttemptID,
		})
		assertPostgresCode(t, err, "55000")
		assertPostgresMessageContains(t, err, "workspace analysis termination fence is invalid")
		_ = expiredTx.Rollback(ctx)

		publishWorkspaceAnalysisDeterministicRefusal(t, ctx, pool, proof)
		assertWorkspaceAnalysisDeterministicRefusal(t, ctx, pool, proof, "refused")
	})

	t.Run("invalid citations are finalized by the current review node", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()

		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		insertWorkspaceAnalysisInvalidCitationFixture(t, ctx, pool)
		validationHash := workspaceAnalysisToolReceiptHash(
			t, ctx, pool, "83000000-0000-4000-8000-000000000175",
		)
		proof := workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000192",
			reason:                "WORKSPACE_ANALYSIS_CITATION_INVALID",
			operationID:           "83000000-0000-4000-8000-000000000172",
			artifactID:            "83000000-0000-4000-8000-000000000175",
			artifactHash:          validationHash,
			terminalNodeRunID:     "83000000-0000-4000-8000-000000000180",
			terminalNodeAttemptID: "83000000-0000-4000-8000-000000000181",
		}

		assertWorkspaceAnalysisDeterministicRefusalInsertError(t, ctx, pool, workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000193",
			reason:                proof.reason,
			operationID:           proof.operationID,
			artifactID:            proof.artifactID,
			artifactHash:          proof.artifactHash,
			terminalNodeRunID:     "83000000-0000-4000-8000-000000000170",
			terminalNodeAttemptID: "83000000-0000-4000-8000-000000000171",
		}, "workspace analysis termination operation binding is invalid")

		assertWorkspaceAnalysisDeterministicRefusalInsertError(t, ctx, pool, workspaceAnalysisDeterministicRefusalProof{
			id:                    "83000000-0000-4000-8000-000000000194",
			reason:                proof.reason,
			operationID:           proof.operationID,
			artifactID:            proof.artifactID,
			artifactHash:          strings.Repeat("f", 64),
			terminalNodeRunID:     proof.terminalNodeRunID,
			terminalNodeAttemptID: proof.terminalNodeAttemptID,
		}, "workspace analysis termination receipt binding is invalid")

		publishWorkspaceAnalysisDeterministicRefusal(t, ctx, pool, proof)
		assertWorkspaceAnalysisDeterministicRefusal(t, ctx, pool, proof, "refused")
	})
}

func TestWorkspaceAnalysisPersistenceMigrationTerminalOperationRecoveryAttemptReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisRetrievalPlanDocument)
	advanceWorkspaceAnalysisPlanAttempt(t, ctx, pool)

	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		latest_node_attempt_id='83000000-0000-4000-8000-000000000090',version=4,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000032'`); err != nil {
		t.Fatalf("replay terminal operation at replacement attempt: %v", err)
	}
	_, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		result_hash=repeat('f',64),version=5,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000032'`)
	assertPostgresCode(t, err, "55000")
}

func TestWorkspaceAnalysisPersistenceMigrationClarificationProofUsesCurrentRecoveryAttempt(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	completeWorkspaceAnalysisGitPrefix(t, ctx, pool)
	authorizeWorkspaceAnalysisPlanOperation(t, ctx, pool)
	completeWorkspaceAnalysisPlanOperation(t, ctx, pool, workspaceAnalysisClarificationPlanDocument)
	advanceWorkspaceAnalysisPlanAttempt(t, ctx, pool)

	assertTerminationProofPostgresCode(t, ctx, pool, workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED", operationID: "83000000-0000-4000-8000-000000000032",
		artifactKind: "MODEL_RESULT", artifactID: "83000000-0000-4000-8000-000000000036",
		artifactHash: workspaceAnalysisModelResultHash(t, ctx, pool),
	}, "55000")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	proof := workspaceAnalysisTerminationProof{
		reason: "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED", operationID: "83000000-0000-4000-8000-000000000032",
		artifactKind: "MODEL_RESULT", artifactID: "83000000-0000-4000-8000-000000000036",
		artifactHash: workspaceAnalysisModelResultHash(t, ctx, pool), terminalNodeAttemptID: "83000000-0000-4000-8000-000000000090",
	}
	insertWorkspaceAnalysisTerminationProof(t, ctx, tx, proof)
	publishWorkspaceAnalysisClarificationInTransaction(t, ctx, tx, workspaceAnalysisClarificationDocument)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("close clarification with replacement attempt: %v", err)
	}
}

func workspaceAnalysisMigrationProvider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		annotated,
		goose.WithTableName(workspaceAnalysisMigrationTable),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create workspace analysis dependency provider: %v", err)
	}
	if _, err := dependencies.UpTo(context.Background(), 84); err != nil {
		t.Fatalf("apply workspace analysis migration dependencies: %v", err)
	}

	content, err := fs.ReadFile(projectmigrations.FS, "00085_workspace_analysis_persistence.sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		fstest.MapFS{
			"00085_workspace_analysis_persistence.sql": &fstest.MapFile{Data: content},
		},
		goose.WithTableName(workspaceAnalysisMigrationTable),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create workspace analysis target provider: %v", err)
	}
	return provider
}

func insertWorkspaceAnalysisLegacyQuestion(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES ('83000000-0000-4000-8000-000000000001','wa-migration','/tmp/wa-migration','/tmp/wa-migration',now(),'active',now(),now());
INSERT INTO agent.conversation(
    id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash
) VALUES (
    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000001',
    'open','Workspace Analysis',1,now(),now(),now(),'wa-migration',repeat('a',64)
);
INSERT INTO agent.question(
    id,workspace_id,conversation_id,ordinal,question_text,scope,answer_depth,output_format,
    context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000003','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002',1,'legacy question','{}','standard','markdown',
    0,repeat('b',64),'wa-legacy-question',repeat('c',64),now()
);`); err != nil {
		t.Fatal(err)
	}
}

func insertIndependentReviewFirstCallFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES (
    '83000000-0000-4000-8000-000000000040','83000000-0000-4000-8000-000000000001',
    'independent-review',1,'{"nodes":[]}',now()
);
INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
VALUES (
    '83000000-0000-4000-8000-000000000041','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000040','running','{}',1,now(),now()
);
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000042','83000000-0000-4000-8000-000000000041',
    'review','independent.review','running',1,'{}','review-worker',now()+interval '5 minutes',1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000043','83000000-0000-4000-8000-000000000042',
    1,1,0,'independent-review-1','review-worker',now()+interval '5 minutes','running',now()
);
INSERT INTO retrieval.index_version(
    id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
    version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000044','83000000-0000-4000-8000-000000000001',
    'simple','v1',repeat('a',64),'{}','independent-review:index',repeat('b',64),0,
    'independent-review-index','building','["vector"]',1,now(),now()
);
INSERT INTO agent.model_run(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
    adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
    prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
    reduced_schema_id,reduced_schema_version,retrieval_index_version_id,
    status,version,started_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000045','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000041','83000000-0000-4000-8000-000000000042',
    '83000000-0000-4000-8000-000000000043','openai-compatible','v1','model-test','2026-08-15',
    'default','v1','faithfulness-review','v1','agent.faithfulness-review','v1',
    'agent.faithfulness-review','v1','83000000-0000-4000-8000-000000000044','RUNNING',1,now(),now()
);
INSERT INTO agent.model_call(
    id,model_run_id,call_no,phase,
    adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
    prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
    request_hash,request_bytes,status,version,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000046','83000000-0000-4000-8000-000000000045',1,'REVIEW',
    'openai-compatible','v1','model-test','2026-08-15','default','v1','faithfulness-review','v1',
    'agent.faithfulness-review','v1',1024,repeat('c',64),128,'STARTED',1,now()
);`); err != nil {
		t.Fatal(err)
	}
}

func insertWorkspaceAnalysisRunFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	insertWorkspaceAnalysisRunFixtureWithLimits(t, ctx, pool, "now()", "now()+interval '13 minutes 50 seconds'", 5376)
}

func insertWorkspaceAnalysisRunFixtureWithLimits(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runCreatedAtSQL, deadlineSQL string, maxOutputTokens int) {
	insertWorkspaceAnalysisRunFixtureWithTimeouts(
		t, ctx, pool, runCreatedAtSQL, deadlineSQL, maxOutputTokens,
		120000, 300000, 180000, 30000, 45000, 20000, 25000,
	)
}

func insertWorkspaceAnalysisRunFixtureWithTimeouts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runCreatedAtSQL string,
	deadlineSQL string,
	maxOutputTokens int,
	planModelTimeoutMS int,
	synthesisModelTimeoutMS int,
	reviewModelTimeoutMS int,
	gitToolTimeoutMS int,
	searchToolTimeoutMS int,
	sourceReadToolTimeoutMS int,
	validateCitationToolTimeoutMS int,
) {
	t.Helper()
	statement := fmt.Sprintf(`
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
    'workspace-analysis',1,'{"nodes":[]}',now()
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
INSERT INTO agent.workspace_analysis_run(
    id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
    definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
	    deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
	    git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,
	    validate_citation_tool_timeout_ms,durable_completion_margin_ms,
	    max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,
    max_input_tokens,max_output_tokens,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000010',
	    '83000000-0000-4000-8000-000000000013','83000000-0000-4000-8000-000000000012',
	    'workspace-analysis',1,repeat('3',64),repeat('4',64),1,7,%s,
	    %d,%d,%d,%d,%d,%d,%d,5000,
	    6,3,6,3,1,196608,%d,'queued',1,%s,now()
);`, deadlineSQL, planModelTimeoutMS, synthesisModelTimeoutMS, reviewModelTimeoutMS,
		gitToolTimeoutMS, searchToolTimeoutMS, sourceReadToolTimeoutMS, validateCitationToolTimeoutMS,
		maxOutputTokens, runCreatedAtSQL)
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatal(err)
	}
}

func startWorkspaceAnalysisRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=clock_timestamp()
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		t.Fatal(err)
	}
}

func completeWorkspaceAnalysisGitPrefix(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	authorizeWorkspaceAnalysisGitOperation(t, ctx, pool)
	completeWorkspaceAnalysisGitOperation(t, ctx, pool)
}

type workspaceAnalysisTerminationProof struct {
	reason                  string
	operationID             string
	artifactKind            string
	artifactID              string
	artifactHash            string
	requestedModelCalls     int
	requestedToolCalls      int
	requestedSourceReads    int
	requestedInputTokens    int
	requestedOutputTokens   int
	requestedCostMicrounits *int64
	receiptFailureCode      string
	receiptFailureID        string
	expectedHash            string
	actualHash              string
	terminalNodeRunID       string
	terminalNodeAttemptID   string
	publishedModelRunID     string
	publishedDocument       string
}

func workspaceAnalysisTerminationPublication(proof workspaceAnalysisTerminationProof) (string, string) {
	if proof.publishedDocument != "" {
		return proof.publishedDocument, proof.publishedModelRunID
	}
	if proof.reason == "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED" {
		return workspaceAnalysisClarificationDocument, "83000000-0000-4000-8000-000000000034"
	}
	switch proof.reason {
	case "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT", "WORKSPACE_ANALYSIS_CITATION_INVALID", "WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED", "WORKSPACE_ANALYSIS_MODEL_REFUSED":
		return fmt.Sprintf(`{"result_type":"workspace_analysis_refusal","schema_id":"conversation.workspace_analysis_refusal","schema_version":"v1","model_run_ref":null,"payload":{"reason_code":"%s","summary":"Workspace analysis stopped."}}`, proof.reason), ""
	default:
		return fmt.Sprintf(`{"result_type":"workspace_analysis_termination","schema_id":"conversation.workspace_analysis_termination","schema_version":"v1","model_run_ref":null,"payload":{"termination_reason":"%s","summary":"Workspace analysis stopped."}}`, proof.reason), ""
	}
}

type workspaceAnalysisDeterministicRefusalProof struct {
	id                    string
	reason                string
	operationID           string
	artifactID            string
	artifactHash          string
	terminalNodeRunID     string
	terminalNodeAttemptID string
}

func insertWorkspaceAnalysisDeterministicRefusalProof(
	ctx context.Context,
	tx pgx.Tx,
	proof workspaceAnalysisDeterministicRefusalProof,
) error {
	document, _ := workspaceAnalysisTerminationPublication(workspaceAnalysisTerminationProof{reason: proof.reason})
	_, err := tx.Exec(ctx, `WITH proof_time AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_termination_proof(
    id,workspace_id,analysis_run_id,answer_id,workflow_run_id,
    terminal_node_run_id,terminal_node_attempt_id,reason,operation_id,
    artifact_kind,artifact_id,artifact_hash,published_document,published_result_hash,
    published_bytes,checked_at,created_at
) SELECT
    $1::uuid,'83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000013',
    '83000000-0000-4000-8000-000000000012',$6::uuid,$7::uuid,$2,$3::uuid,
    'TOOL_RECEIPT',$4::uuid,$5,convert_to($8,'UTF8'),
    encode(sha256(convert_to($8,'UTF8')),'hex'),octet_length(convert_to($8,'UTF8')),at,at
FROM proof_time`,
		proof.id,
		proof.reason,
		proof.operationID,
		proof.artifactID,
		proof.artifactHash,
		proof.terminalNodeRunID,
		proof.terminalNodeAttemptID,
		document,
	)
	return err
}

func assertWorkspaceAnalysisDeterministicRefusalInsertError(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proof workspaceAnalysisDeterministicRefusalProof,
	message string,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = insertWorkspaceAnalysisDeterministicRefusalProof(ctx, tx, proof)
	assertPostgresCode(t, err, "55000")
	assertPostgresMessageContains(t, err, message)
}

func assertPostgresMessageContains(t *testing.T, err error, message string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !strings.Contains(pgErr.Message, message) {
		t.Fatalf("postgres error=%v, want message containing %q", err, message)
	}
}

func publishWorkspaceAnalysisDeterministicRefusal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proof workspaceAnalysisDeterministicRefusalProof,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := insertWorkspaceAnalysisDeterministicRefusalProof(ctx, tx, proof); err != nil {
		t.Fatalf("insert %s proof: %v", proof.reason, err)
	}
	document, _ := workspaceAnalysisTerminationPublication(workspaceAnalysisTerminationProof{reason: proof.reason})
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
    status='refused',termination_reason=$1,version=version+1,
    updated_at=terminal.at,completed_at=terminal.at
    FROM (SELECT clock_timestamp() AS at) AS terminal
    WHERE id='83000000-0000-4000-8000-000000000014'`, proof.reason); err != nil {
		t.Fatalf("close %s analysis run: %v", proof.reason, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.answer SET
    publication_status='refused',result_type='workspace_analysis_refusal',result=($1::text)::jsonb,
    result_hash=encode(sha256(convert_to($1::text,'UTF8')),'hex'),version=2,
    updated_at=terminal.at,published_at=terminal.at
    FROM (SELECT clock_timestamp() AS at) AS terminal
	WHERE id='83000000-0000-4000-8000-000000000013'`, document); err != nil {
		t.Fatalf("publish %s refusal: %v", proof.reason, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit %s refusal: %v", proof.reason, err)
	}
}

func assertWorkspaceAnalysisDeterministicRefusal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proof workspaceAnalysisDeterministicRefusalProof,
	wantStatus string,
) {
	t.Helper()
	var runStatus, answerStatus, reason, terminalNodeRunID, authorityNodeRunID string
	if err := pool.QueryRow(ctx, `SELECT analysis.status,answer.publication_status,proof.reason,
       proof.terminal_node_run_id::text,operation.node_run_id::text
FROM agent.workspace_analysis_termination_proof AS proof
JOIN agent.workspace_analysis_run AS analysis ON analysis.id=proof.analysis_run_id
JOIN agent.answer AS answer ON answer.id=proof.answer_id
JOIN agent.workspace_analysis_operation AS operation ON operation.id=proof.operation_id
WHERE proof.id=$1`, proof.id).Scan(
		&runStatus,
		&answerStatus,
		&reason,
		&terminalNodeRunID,
		&authorityNodeRunID,
	); err != nil {
		t.Fatal(err)
	}
	if runStatus != wantStatus || answerStatus != wantStatus || reason != proof.reason {
		t.Fatalf("refusal closure run=%q answer=%q reason=%q", runStatus, answerStatus, reason)
	}
	if terminalNodeRunID == authorityNodeRunID {
		t.Fatalf("deterministic refusal did not bind an upstream operation: node=%s", terminalNodeRunID)
	}
}

func completeWorkspaceAnalysisEmptySearchAndStartRead(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '83000000-0000-4000-8000-000000000043','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000030',
    '83000000-0000-4000-8000-000000000031',1,'SearchKnowledge',2,
    'db7180086adb06a18a4be8d1eb80a208fa6385c70f1a71d6d67c4f263fbd1807',
    'tool.search_knowledge.input',2,'tool.search_knowledge.output',2,
    'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',repeat('b',64),2,'{}','STARTED',false,now(),0,1
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000042','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000030','retrieve_evidence','KNOWLEDGE_SEARCH',1,
    'TOOL',repeat('b',64),'PENDING',1,now(),now()
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000031',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000031',
    tool_call_id='83000000-0000-4000-8000-000000000043',version=2,
    started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
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
    reserved_tool_calls=reserved_tool_calls+1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"degradations":[],"effective_mode":"hybrid","items":[]}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"degradations":[],"effective_mode":"hybrid","items":[]}','UTF8')),
    response_summary='{}',completed_at=clock_timestamp(),duration_ms=10,version=2
WHERE id='83000000-0000-4000-8000-000000000043';
INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,
    max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
    server_binding_schema_id,server_binding_schema_version,server_binding_document,
    server_binding_hash,server_binding_bytes,created_at
) SELECT
    '83000000-0000-4000-8000-000000000045',call.id,call.workspace_id,call.workflow_run_id,
    call.node_run_id,call.node_attempt_id,call.requested_tool_name,call.tool_version,
    call.output_schema_id,call.output_schema_version,call.definition_hash,'PERSIST_CANONICAL',32768,16384,
    convert_to('{"degradations":[],"effective_mode":"hybrid","items":[]}','UTF8'),
    call.response_hash,call.response_bytes,'tool.search_knowledge.private_binding',1,
    convert_to('{"items":[],"selected_refs":[]}','UTF8'),
    encode(sha256(convert_to('{"items":[],"selected_refs":[]}','UTF8')),'hex'),
    octet_length(convert_to('{"items":[],"selected_refs":[]}','UTF8')),clock_timestamp()
FROM workflow.tool_call AS call
WHERE call.id='83000000-0000-4000-8000-000000000043';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000044';
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=reserved_tool_calls-1,settled_tool_calls=settled_tool_calls+1,
    version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',
    result_id='83000000-0000-4000-8000-000000000045',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt
                 WHERE id='83000000-0000-4000-8000-000000000045'),
    version=3,completed_at=terminal.at,updated_at=terminal.at
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000042';
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000050','83000000-0000-4000-8000-000000000012',
    'read_evidence','workspace_analysis.read','running',1,'{}','wa-worker',now()+interval '5 minutes',
    'wa-read-empty',1,1,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000051','83000000-0000-4000-8000-000000000050',
    1,1,0,'wa-read-empty-1','wa-worker',now()+interval '5 minutes','running',now()
)`); err != nil {
		t.Fatalf("complete empty SearchKnowledge operation: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit empty SearchKnowledge fixture: %v", err)
	}
}

func insertWorkspaceAnalysisInvalidCitationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const validDocument = `{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}`
	const invalidDocument = `{"results":[{"evidence_ref":"E1","reason_code":"CITATION_UNRESOLVABLE","valid":false}]}`
	const reviewStart = "\nUPDATE agent.workspace_analysis_operation SET\nstatus='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000181'"
	if count := strings.Count(workspaceAnalysisPublicationReadyFixtureSQL, validDocument); count != 3 {
		t.Fatalf("validation fixture document occurrences=%d, want 3", count)
	}
	fixture, _, found := strings.Cut(workspaceAnalysisPublicationReadyFixtureSQL, reviewStart)
	if !found {
		t.Fatal("review operation marker is missing from publication fixture")
	}
	fixture = strings.ReplaceAll(fixture, validDocument, invalidDocument)
	fixture = removeWorkspaceAnalysisFixtureTerminalRows(t, fixture, []string{
		"('83000000-0000-4000-8000-000000000184'",
		"('83000000-0000-4000-8000-000000000185'",
		"('83000000-0000-4000-8000-000000000182'",
	})
	fixture += `
UPDATE agent.workspace_analysis_run SET
    reserved_model_calls=0,reserved_tool_calls=0,reserved_source_reads=0,
    reserved_input_tokens=0,reserved_output_tokens=0,
    settled_model_calls=2,settled_tool_calls=4,settled_source_reads=1,
    settled_input_tokens=20,settled_output_tokens=10,
    version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';`
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fixture); err != nil {
		t.Fatalf("insert invalid citation fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit invalid citation fixture: %v", err)
	}
}

func removeWorkspaceAnalysisFixtureTerminalRows(t *testing.T, fixture string, rowPrefixes []string) string {
	t.Helper()
	lines := strings.Split(fixture, "\n")
	removed := 0
	for _, prefix := range rowPrefixes {
		found := false
		for index, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), prefix) {
				continue
			}
			if index == 0 || !strings.HasSuffix(strings.TrimSpace(line), ");") ||
				!strings.HasSuffix(strings.TrimSpace(lines[index-1]), ",") {
				t.Fatalf("fixture terminal row %q has an unexpected shape", prefix)
			}
			lines[index-1] = strings.TrimSuffix(lines[index-1], ",") + ";"
			lines = append(lines[:index], lines[index+1:]...)
			removed++
			found = true
			break
		}
		if !found {
			t.Fatalf("fixture terminal row %q is missing", prefix)
		}
	}
	if removed != len(rowPrefixes) {
		t.Fatalf("fixture terminal rows removed=%d, want %d", removed, len(rowPrefixes))
	}
	return strings.Join(lines, "\n")
}

func workspaceAnalysisToolReceiptHash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, receiptID string) string {
	t.Helper()
	var hash string
	if err := pool.QueryRow(ctx, `SELECT output_hash FROM workflow.tool_result_receipt WHERE id=$1`, receiptID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

func insertPendingWorkspaceAnalysisGitOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
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
);`); err != nil {
		t.Fatal(err)
	}
}

func insertPendingWorkspaceAnalysisPlanOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000030','83000000-0000-4000-8000-000000000012',
    'retrieve_evidence','workspace_analysis.retrieve','running',1,'{}','wa-worker',now()+interval '5 minutes',
    'wa-retrieve',1,1,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000031','83000000-0000-4000-8000-000000000030',
    1,1,0,'wa-retrieve-1','wa-worker',now()+interval '5 minutes','running',now()
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000032','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000030','retrieve_evidence','RETRIEVAL_PLAN',1,'MODEL',
    repeat('7',64),'PENDING',1,now(),now()
);`); err != nil {
		t.Fatal(err)
	}
}

func insertWorkspaceAnalysisTerminationProof(t *testing.T, ctx context.Context, tx pgx.Tx, proof workspaceAnalysisTerminationProof) {
	t.Helper()
	publishedDocument, publishedModelRunID := workspaceAnalysisTerminationPublication(proof)
	terminalNodeRunID := proof.terminalNodeRunID
	if terminalNodeRunID == "" {
		terminalNodeRunID = "83000000-0000-4000-8000-000000000030"
	}
	terminalAttemptID := proof.terminalNodeAttemptID
	if terminalAttemptID == "" {
		terminalAttemptID = "83000000-0000-4000-8000-000000000031"
	}
	if _, err := tx.Exec(ctx, `WITH proof_time AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_termination_proof(
    id,workspace_id,analysis_run_id,answer_id,workflow_run_id,terminal_node_run_id,terminal_node_attempt_id,
    reason,operation_id,artifact_kind,artifact_id,artifact_hash,
	    requested_model_calls,requested_tool_calls,requested_source_reads,requested_input_tokens,requested_output_tokens,requested_cost_microunits,
	    receipt_failure_code,receipt_failure_id,expected_hash,actual_hash,
	    published_model_run_id,published_document,published_result_hash,published_bytes,checked_at,created_at
) SELECT
    '83000000-0000-4000-8000-000000000080','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000013',
	    '83000000-0000-4000-8000-000000000012',$20::uuid,
	    $16::uuid,
    $1,$2::uuid,NULLIF($3,''),NULLIF($4,'')::uuid,NULLIF($5,''),
	    CASE WHEN $15 THEN $6::integer ELSE NULL END,CASE WHEN $15 THEN $7::integer ELSE NULL END,
	    CASE WHEN $15 THEN $8::integer ELSE NULL END,CASE WHEN $15 THEN $9::bigint ELSE NULL END,CASE WHEN $15 THEN $10::bigint ELSE NULL END,CASE WHEN $15 THEN $11::bigint ELSE NULL END,
	    NULLIF($12,''),NULLIF($19,'')::uuid,NULLIF($13,''),NULLIF($14,''),NULLIF($17,'')::uuid,
	    convert_to($18,'UTF8'),encode(sha256(convert_to($18,'UTF8')),'hex'),octet_length(convert_to($18,'UTF8')),at,at
FROM proof_time`,
		proof.reason, proof.operationID, proof.artifactKind, proof.artifactID, proof.artifactHash,
		proof.requestedModelCalls, proof.requestedToolCalls, proof.requestedSourceReads,
		proof.requestedInputTokens, proof.requestedOutputTokens, proof.requestedCostMicrounits, proof.receiptFailureCode,
		proof.expectedHash, proof.actualHash, proof.reason == "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", terminalAttemptID,
		publishedModelRunID, publishedDocument, proof.receiptFailureID, terminalNodeRunID); err != nil {
		t.Fatal(err)
	}
}

func assertTerminationProofPostgresCode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proof workspaceAnalysisTerminationProof, code string) {
	t.Helper()
	publishedDocument, publishedModelRunID := workspaceAnalysisTerminationPublication(proof)
	terminalNodeRunID := proof.terminalNodeRunID
	if terminalNodeRunID == "" {
		terminalNodeRunID = "83000000-0000-4000-8000-000000000030"
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	terminalAttemptID := proof.terminalNodeAttemptID
	if terminalAttemptID == "" {
		terminalAttemptID = "83000000-0000-4000-8000-000000000031"
	}
	if _, err := tx.Exec(ctx, `WITH proof_time AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_termination_proof(
    id,workspace_id,analysis_run_id,answer_id,workflow_run_id,terminal_node_run_id,terminal_node_attempt_id,
    reason,operation_id,artifact_kind,artifact_id,artifact_hash,
	    requested_model_calls,requested_tool_calls,requested_source_reads,requested_input_tokens,requested_output_tokens,requested_cost_microunits,
	    receipt_failure_code,receipt_failure_id,expected_hash,actual_hash,
	    published_model_run_id,published_document,published_result_hash,published_bytes,checked_at,created_at
) SELECT
    '83000000-0000-4000-8000-000000000080','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000013',
	    '83000000-0000-4000-8000-000000000012',$20::uuid,
	    $16::uuid,
    $1,$2::uuid,NULLIF($3,''),NULLIF($4,'')::uuid,NULLIF($5,''),
	    CASE WHEN $15 THEN $6::integer ELSE NULL END,CASE WHEN $15 THEN $7::integer ELSE NULL END,
	    CASE WHEN $15 THEN $8::integer ELSE NULL END,CASE WHEN $15 THEN $9::bigint ELSE NULL END,CASE WHEN $15 THEN $10::bigint ELSE NULL END,CASE WHEN $15 THEN $11::bigint ELSE NULL END,
	    NULLIF($12,''),NULLIF($19,'')::uuid,NULLIF($13,''),NULLIF($14,''),NULLIF($17,'')::uuid,
	    convert_to($18,'UTF8'),encode(sha256(convert_to($18,'UTF8')),'hex'),octet_length(convert_to($18,'UTF8')),at,at
FROM proof_time`,
		proof.reason, proof.operationID, proof.artifactKind, proof.artifactID, proof.artifactHash,
		proof.requestedModelCalls, proof.requestedToolCalls, proof.requestedSourceReads,
		proof.requestedInputTokens, proof.requestedOutputTokens, proof.requestedCostMicrounits, proof.receiptFailureCode,
		proof.expectedHash, proof.actualHash, proof.reason == "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", terminalAttemptID,
		publishedModelRunID, publishedDocument, proof.receiptFailureID, terminalNodeRunID); err != nil {
		assertPostgresCode(t, err, code)
		return
	}
	assertPostgresCode(t, tx.Commit(ctx), code)
}

func publishWorkspaceAnalysisFailure(t *testing.T, ctx context.Context, tx pgx.Tx, reason string) {
	t.Helper()
	document, _ := workspaceAnalysisTerminationPublication(workspaceAnalysisTerminationProof{reason: reason})
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
    status='failed',termination_reason=$1,version=version+1,
    updated_at=terminal.at,completed_at=terminal.at
    FROM (SELECT clock_timestamp() AS at) AS terminal
    WHERE id='83000000-0000-4000-8000-000000000014'`, reason); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.answer SET
    publication_status='failed',result_type='workspace_analysis_termination',result=($1::text)::jsonb,
    result_hash=encode(sha256(convert_to($1::text,'UTF8')),'hex'),version=2,
    updated_at=terminal.at,published_at=terminal.at
    FROM (SELECT clock_timestamp() AS at) AS terminal
    WHERE id='83000000-0000-4000-8000-000000000013'`, document); err != nil {
		t.Fatal(err)
	}
}

func advanceWorkspaceAnalysisPlanAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET
    status='retry_scheduled',lease_owner=NULL,lease_until=NULL,failure_class='retryable',
    error_code='RECOVERY_REPLAY',ended_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000031';
UPDATE workflow.node_run SET
    attempt=2,lease_owner='wa-worker',lease_until=now()+interval '5 minutes',
    dispatch_no=2,version=2,updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000030';
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000090','83000000-0000-4000-8000-000000000030',
    2,2,1,'wa-retrieve-2','wa-worker',now()+interval '5 minutes','running',now()
);`); err != nil {
		t.Fatal(err)
	}
}

func workspaceAnalysisModelResultHash(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var hash string
	if err := pool.QueryRow(ctx, `SELECT document_hash FROM agent.workspace_analysis_model_result
		WHERE id='83000000-0000-4000-8000-000000000036'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

func publishWorkspaceAnalysisClarificationInTransaction(t *testing.T, ctx context.Context, tx pgx.Tx, document string) {
	t.Helper()
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
			status='clarification_required',termination_reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED',
			version=version+1,updated_at=terminal.at,completed_at=terminal.at
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.answer SET
		model_run_id='83000000-0000-4000-8000-000000000034',
		publication_status='clarification_required',result_type='clarification',result=($1::text)::jsonb,
		result_hash=encode(sha256(convert_to($1::text,'UTF8')),'hex'),version=2,
		updated_at=publication.at,published_at=publication.at
		FROM (SELECT clock_timestamp() AS at) AS publication
		WHERE id='83000000-0000-4000-8000-000000000013'`, document); err != nil {
		t.Fatal(err)
	}
}

func authorizeWorkspaceAnalysisGitOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
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
    '83000000-0000-4000-8000-000000000021',1,'ReadGitStatus',2,'b5dd1fcca72d5bb41fd9ad3f74006d3706e4184ad39e2a3409b565d1ff896bbd',
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
WHERE id='83000000-0000-4000-8000-000000000014';`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func authorizeWorkspaceAnalysisPlanOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	prepareWorkspaceAnalysisPlanOperation(t, ctx, tx)
	reserveWorkspaceAnalysisPlanOperation(t, ctx, tx, 256)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func prepareWorkspaceAnalysisPlanOperation(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	if err := prepareWorkspaceAnalysisPlanOperationError(ctx, tx); err != nil {
		t.Fatal(err)
	}
}

func prepareWorkspaceAnalysisPlanOperationError(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000030','83000000-0000-4000-8000-000000000012',
    'retrieve_evidence','workspace_analysis.retrieve','running',1,'{}','wa-worker',now()+interval '5 minutes',
    'wa-retrieve',1,1,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000031','83000000-0000-4000-8000-000000000030',
    1,1,0,'wa-retrieve-1','wa-worker',now()+interval '5 minutes','running',now()
);
INSERT INTO retrieval.index_version(
    id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
    version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000037','83000000-0000-4000-8000-000000000001',
    'simple','v1',repeat('8',64),'{}','wa-plan:index',repeat('9',64),0,
    'wa-plan-index','building','["vector"]',1,now(),now()
);
INSERT INTO agent.model_run(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
    adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
    prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
    reduced_schema_id,reduced_schema_version,retrieval_index_version_id,
    status,version,started_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000034','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000030',
    '83000000-0000-4000-8000-000000000031','openai-compatible','v1','model-test','2026-08-15',
    'default','v1','workspace-analysis-plan','v1','agent.workspace-analysis-plan','v1',
    'agent.workspace-analysis-plan','v1','83000000-0000-4000-8000-000000000037','RUNNING',1,now(),now()
);
INSERT INTO agent.model_call(
    id,model_run_id,call_no,phase,
    adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
    prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
    request_hash,request_bytes,status,version,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000035','83000000-0000-4000-8000-000000000034',1,'PLAN',
    'openai-compatible','v1','model-test','2026-08-15','default','v1','workspace-analysis-plan','v1',
    'agent.workspace-analysis-plan','v1',256,repeat('7',64),128,'STARTED',1,now()
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000032','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000030','retrieve_evidence','RETRIEVAL_PLAN',1,'MODEL',
    repeat('7',64),'PENDING',1,now(),now()
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',
    first_node_attempt_id='83000000-0000-4000-8000-000000000031',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000031',
    model_call_id='83000000-0000-4000-8000-000000000035',
    version=2,started_at=now(),updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000032';`)
	return err
}

func authorizeWorkspaceAnalysisSearchOperationError(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '83000000-0000-4000-8000-000000000043','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000030',
    '83000000-0000-4000-8000-000000000031',1,'SearchKnowledge',2,repeat('a',64),
    'tool.search_knowledge.input',1,'tool.search_knowledge.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('b',64),2,'{}','STARTED',false,now(),0,1
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000042','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000030','retrieve_evidence','KNOWLEDGE_SEARCH',1,'TOOL',
    repeat('b',64),'PENDING',1,now(),now()
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000031',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000031',
    tool_call_id='83000000-0000-4000-8000-000000000043',
    version=2,started_at=now(),updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000042';`)
	return err
}

func reserveWorkspaceAnalysisPlanOperation(t *testing.T, ctx context.Context, tx pgx.Tx, outputTokens int) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_budget_reservation(
		id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,
		reserved_input_tokens,reserved_output_tokens,created_at
	) VALUES (
		'83000000-0000-4000-8000-000000000033','83000000-0000-4000-8000-000000000001',
		'83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000032',
			'MODEL','83000000-0000-4000-8000-000000000035','RESERVED',1,0,0,65536,$1,now()
	)`, outputTokens); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
			reserved_model_calls=1,reserved_input_tokens=65536,reserved_output_tokens=$1,
			version=version+1,updated_at=clock_timestamp()
			WHERE id='83000000-0000-4000-8000-000000000014'`, outputTokens); err != nil {
		t.Fatal(err)
	}
}

func completeWorkspaceAnalysisPlanCallAndResult(t *testing.T, ctx context.Context, tx pgx.Tx, document string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `UPDATE agent.model_call SET
			status='SUCCEEDED',response_hash=encode(sha256(convert_to($1,'UTF8')),'hex'),
			response_bytes=octet_length(convert_to($1,'UTF8')),input_tokens=10,output_tokens=5,
			latency_ms=10,version=2,completed_at=clock_timestamp()
			WHERE id='83000000-0000-4000-8000-000000000035'`, args: []any{document}},
		{query: `UPDATE agent.model_run SET
			status='SUCCEEDED',final_result_type='workspace_analysis_plan',version=2,
			updated_at=terminal.at,completed_at=terminal.at
			FROM (SELECT clock_timestamp() AS at) AS terminal
			WHERE id='83000000-0000-4000-8000-000000000034'`},
		{query: `INSERT INTO agent.workspace_analysis_model_result(
			id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,
			operation_kind,schema_id,schema_version,document,document_hash,document_bytes,created_at
		) SELECT
			'83000000-0000-4000-8000-000000000036','83000000-0000-4000-8000-000000000001',
			'83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000032',
				'83000000-0000-4000-8000-000000000031','83000000-0000-4000-8000-000000000034',call.id,
				'RETRIEVAL_PLAN',call.output_schema_id,call.output_schema_version,convert_to($1,'UTF8'),
				call.response_hash,call.response_bytes,GREATEST(call.completed_at,model.completed_at)+interval '1 millisecond'
				FROM agent.model_call AS call JOIN agent.model_run AS model ON model.id=call.model_run_id
				WHERE call.id='83000000-0000-4000-8000-000000000035'`, args: []any{document}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func completeWorkspaceAnalysisPlanOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, document string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `UPDATE agent.model_call SET
			status='SUCCEEDED',response_hash=encode(sha256(convert_to($1,'UTF8')),'hex'),
			response_bytes=octet_length(convert_to($1,'UTF8')),input_tokens=10,output_tokens=5,
			latency_ms=10,version=2,completed_at=clock_timestamp()
			WHERE id='83000000-0000-4000-8000-000000000035'`, args: []any{document}},
		{query: `UPDATE agent.model_run SET
			status='SUCCEEDED',final_result_type='workspace_analysis_plan',version=2,
			updated_at=terminal.at,completed_at=terminal.at
			FROM (SELECT clock_timestamp() AS at) AS terminal
			WHERE id='83000000-0000-4000-8000-000000000034'`},
		{query: `INSERT INTO agent.workspace_analysis_model_result(
			id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,
			operation_kind,schema_id,schema_version,document,document_hash,document_bytes,created_at
		) SELECT
			'83000000-0000-4000-8000-000000000036','83000000-0000-4000-8000-000000000001',
			'83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000032',
				'83000000-0000-4000-8000-000000000031','83000000-0000-4000-8000-000000000034',call.id,
				'RETRIEVAL_PLAN',call.output_schema_id,call.output_schema_version,convert_to($1,'UTF8'),
				call.response_hash,call.response_bytes,GREATEST(call.completed_at,model.completed_at)+interval '1 millisecond'
				FROM agent.model_call AS call JOIN agent.model_run AS model ON model.id=call.model_run_id
				WHERE call.id='83000000-0000-4000-8000-000000000035'`, args: []any{document}},
		{query: `UPDATE agent.workspace_analysis_budget_reservation SET
			status='SETTLED',settled_model_calls=1,settled_input_tokens=10,settled_output_tokens=5,
			settled_at=clock_timestamp()
			WHERE id='83000000-0000-4000-8000-000000000033'`},
		{query: `UPDATE agent.workspace_analysis_run SET
				reserved_model_calls=0,reserved_input_tokens=0,reserved_output_tokens=0,
				settled_model_calls=1,settled_input_tokens=10,settled_output_tokens=5,
				version=version+1,updated_at=clock_timestamp()
				WHERE id='83000000-0000-4000-8000-000000000014'`},
		{query: `UPDATE agent.workspace_analysis_operation SET
			status='SUCCEEDED',result_kind='MODEL_RESULT_RECEIPT',
			result_id='83000000-0000-4000-8000-000000000036',
			result_hash=(SELECT document_hash FROM agent.workspace_analysis_model_result
			             WHERE id='83000000-0000-4000-8000-000000000036'),
			version=3,completed_at=terminal.at,updated_at=terminal.at
			FROM (SELECT clock_timestamp() AS at) AS terminal
			WHERE id='83000000-0000-4000-8000-000000000032'`},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func publishWorkspaceAnalysisClarification(ctx context.Context, pool *pgxpool.Pool, document string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO agent.workspace_analysis_termination_proof(
			id,workspace_id,analysis_run_id,answer_id,workflow_run_id,terminal_node_run_id,terminal_node_attempt_id,
			reason,operation_id,artifact_kind,artifact_id,artifact_hash,
			published_model_run_id,published_document,published_result_hash,published_bytes,checked_at,created_at
		) SELECT
		'83000000-0000-4000-8000-000000000038','83000000-0000-4000-8000-000000000001',
		'83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000013',
		'83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000030',
			'83000000-0000-4000-8000-000000000031','WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED',
			'83000000-0000-4000-8000-000000000032','MODEL_RESULT',id,document_hash,
			'83000000-0000-4000-8000-000000000034',convert_to($1,'UTF8'),
			encode(sha256(convert_to($1,'UTF8')),'hex'),octet_length(convert_to($1,'UTF8')),
			proof.at,proof.at
	FROM agent.workspace_analysis_model_result
	CROSS JOIN (SELECT clock_timestamp() AS at) AS proof
		WHERE id='83000000-0000-4000-8000-000000000036'`, document); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
			status='clarification_required',termination_reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED',
			version=version+1,updated_at=terminal.at,completed_at=terminal.at
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.answer SET
		model_run_id='83000000-0000-4000-8000-000000000034',
		publication_status='clarification_required',result_type='clarification',result=($1::text)::jsonb,
		result_hash=encode(sha256(convert_to($1::text,'UTF8')),'hex'),version=2,
		updated_at=publication.at,published_at=publication.at
		FROM (SELECT clock_timestamp() AS at) AS publication
		WHERE id='83000000-0000-4000-8000-000000000013'`, document); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func completeWorkspaceAnalysisGitOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8')),
    response_summary='{"clean":true}',completed_at=clock_timestamp(),duration_ms=10,version=2
WHERE id='83000000-0000-4000-8000-000000000023';
INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,
    max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,created_at
) SELECT
    '83000000-0000-4000-8000-000000000025',id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
    requested_tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,'PERSIST_CANONICAL',
    4096,1024,
    convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8'),
    response_hash,response_bytes,completed_at+interval '1 millisecond'
FROM workflow.tool_call
WHERE id='83000000-0000-4000-8000-000000000023';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000024';
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=0,settled_tool_calls=1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',
    result_id='83000000-0000-4000-8000-000000000025',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt
                 WHERE id='83000000-0000-4000-8000-000000000025'),
    version=3,completed_at=terminal.at,updated_at=terminal.at
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000022';`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func failWorkspaceAnalysisGitReceipt(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0,"conflict_count":0}','UTF8')),
    response_summary='{"clean":true}',completed_at=clock_timestamp(),duration_ms=10,version=2
WHERE id='83000000-0000-4000-8000-000000000023';
INSERT INTO workflow.tool_result_receipt_failure(
    id,tool_call_id,operation_id,analysis_run_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
    failure_code,expected_output_hash,validator_version,checked_at,created_at
) SELECT
    '83000000-0000-4000-8000-000000000026',call.id,
    '83000000-0000-4000-8000-000000000022','83000000-0000-4000-8000-000000000014',
    call.workspace_id,call.workflow_run_id,call.node_run_id,call.node_attempt_id,
    'MISSING',call.response_hash,1,failure.at,failure.at
FROM workflow.tool_call AS call
CROSS JOIN (SELECT clock_timestamp() AS at) AS failure
WHERE call.id='83000000-0000-4000-8000-000000000023';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000024';
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=0,settled_tool_calls=1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='FAILED',error_code='WORKSPACE_ANALYSIS_RECEIPT_INVALID',version=3,
    completed_at=terminal.at,updated_at=terminal.at
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000022';`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceAnalysisMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, modeColumn, meta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE (table_schema='agent' AND table_name IN (
			'workspace_analysis_run','workspace_analysis_operation',
			'workspace_analysis_budget_reservation','workspace_analysis_candidate',
			'workspace_analysis_model_result','workspace_analysis_publication_proof',
			'workspace_analysis_termination_proof'
		)) OR (table_schema='workflow' AND table_name IN ('tool_result_receipt','tool_result_receipt_failure'))`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='agent' AND table_name='question' AND column_name='mode'`).Scan(&modeColumn); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='workspace_analysis_persistence' AND value='workspace-analysis-persistence-v1'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if tables != 9 || modeColumn != 1 || meta != 1 {
		t.Fatalf("workspace analysis schema tables=%d mode_columns=%d meta=%d", tables, modeColumn, meta)
	}
}
