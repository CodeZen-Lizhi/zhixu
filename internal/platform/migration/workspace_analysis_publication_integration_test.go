//go:build integration

package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolspostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsretrieval "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/retrieval"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workspaceAnalysisPublicationCandidateDocument = `{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","model_run_ref":"83000000-0000-4000-8000-000000000164","payload":{"answer_markdown":"Workspace is clean.","citation_refs":["E1"],"proposal_suggestion":null}}`

const workspaceAnalysisPublicationReviewDocument = `{"result_type":"faithfulness_review","schema_id":"agent.faithfulness-review","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000184","payload":{"passed":true,"items":[{"assertion_id":"@answer/conclusion","verdict":"SUPPORTED","citation_ids":["E1"],"reason":"Supported by E1."}],"summary":"Candidate is supported."}}`

const workspaceAnalysisPublicationDocument = `{"result_type":"workspace_analysis","schema_id":"conversation.workspace_analysis_answer","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000164","payload":{"answer_markdown":"Workspace is clean.","citations":[{"id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","workspace_id":"83000000-0000-4000-8000-000000000001","index_version_id":"83000000-0000-4000-8000-000000000137","chunk_id":"83000000-0000-4000-8000-000000000138","source_version_id":"83000000-0000-4000-8000-000000000139","source_span_id":"83000000-0000-4000-8000-000000000140"}],"git_status":{"branch":"main","head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","clean":true,"staged_count":0,"unstaged_count":0,"untracked_count":0,"conflict_count":0},"budget":{"model_calls":3,"tool_calls":4,"input_tokens":30,"output_tokens":15,"estimated_cost_microunits":null},"proposal_suggestion":null,"termination_reason":"COMPLETED"}}`

const workspaceAnalysisPublicationTamperedDocument = `{"result_type":"workspace_analysis","schema_id":"conversation.workspace_analysis_answer","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000164","payload":{"answer_markdown":"This text was not synthesized.","citations":[{"id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","workspace_id":"83000000-0000-4000-8000-000000000001","index_version_id":"83000000-0000-4000-8000-000000000137","chunk_id":"83000000-0000-4000-8000-000000000138","source_version_id":"83000000-0000-4000-8000-000000000139","source_span_id":"83000000-0000-4000-8000-000000000140"}],"git_status":{"branch":"main","head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","clean":true,"staged_count":0,"unstaged_count":0,"untracked_count":0,"conflict_count":0},"budget":{"model_calls":3,"tool_calls":4,"input_tokens":30,"output_tokens":15,"estimated_cost_microunits":null},"proposal_suggestion":null,"termination_reason":"COMPLETED"}}`

const workspaceAnalysisSearchPrivateBindingDocument = `{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000137","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}],"selected_refs":["E1"]}`

func TestWorkspaceAnalysisPublicationProofCompletesOnlyAuthoritativeProjection(t *testing.T) {
	ctx := context.Background()

	t.Run("completed publication", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		insertWorkspaceAnalysisPublicationReadyFixture(t, ctx, pool)
		publishWorkspaceAnalysisCompletedProof(t, ctx, pool, "83000000-0000-4000-8000-000000000190", workspaceAnalysisPublicationDocument)

		var status, answerStatus, answerResult string
		if err := pool.QueryRow(ctx, `SELECT status FROM agent.workspace_analysis_run
			WHERE id='83000000-0000-4000-8000-000000000014'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT publication_status,result_type FROM agent.answer
			WHERE id='83000000-0000-4000-8000-000000000013'`).Scan(&answerStatus, &answerResult); err != nil {
			t.Fatal(err)
		}
		if status != "succeeded" || answerStatus != "completed" || answerResult != "workspace_analysis" {
			t.Fatalf("publication state run=%q answer=%q result=%q", status, answerStatus, answerResult)
		}
	})

	t.Run("tampered published answer is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		insertWorkspaceAnalysisPublicationReadyFixture(t, ctx, pool)

		_, err := pool.Exec(ctx, workspaceAnalysisPublicationProofInsertSQL,
			"83000000-0000-4000-8000-000000000199", workspaceAnalysisPublicationTamperedDocument)
		assertPostgresCode(t, err, "55000")
	})

	t.Run("invalid citation blocks review", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		invalidCitationSQL := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			`"valid":true`,
			`"valid":false`,
		)
		_, err = tx.Exec(ctx, invalidCitationSQL)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("review must cover the exact candidate citation set", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		driftedReview := strings.Replace(
			workspaceAnalysisPublicationReviewDocument,
			`"citation_ids":["E1"]`,
			`"citation_ids":["E2"]`,
			1,
		)
		driftedFixture := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			workspaceAnalysisPublicationReviewDocument,
			driftedReview,
		)
		if driftedFixture == workspaceAnalysisPublicationReadyFixtureSQL {
			t.Fatal("review fixture was not replaced")
		}
		_, err = tx.Exec(ctx, driftedFixture)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("malformed exact search receipt is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		malformedReceiptSQL := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			`{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}`,
			`{"evidence_ref":"E1","rank":1}`,
		)
		_, err = tx.Exec(ctx, malformedReceiptSQL)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("drifted exact search input schema is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		driftedCallSQL := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			`'tool.search_knowledge.input',2,'tool.search_knowledge.output'`,
			`'tool.search_knowledge.input',1,'tool.search_knowledge.output'`,
		)
		_, err = tx.Exec(ctx, driftedCallSQL)
		assertPostgresCode(t, err, "55000")
	})

	t.Run("non-canonical exact search binding is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		nonCanonical := `{"selected_refs":["E1"],"items":` +
			strings.TrimSuffix(strings.TrimPrefix(workspaceAnalysisSearchPrivateBindingDocument, `{"items":`), `,"selected_refs":["E1"]}`) + `}`
		driftedReceiptSQL := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			workspaceAnalysisSearchPrivateBindingDocument,
			nonCanonical,
		)
		if driftedReceiptSQL == workspaceAnalysisPublicationReadyFixtureSQL {
			t.Fatal("search private binding fixture was not replaced")
		}
		_, err = tx.Exec(ctx, driftedReceiptSQL)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("duplicate exact search binding key is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		duplicateKey := strings.Replace(
			workspaceAnalysisSearchPrivateBindingDocument,
			`"selected_refs":["E1"]`,
			`"selected_refs":["E1"],"selected_refs":["E1"]`,
			1,
		)
		driftedReceiptSQL := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			workspaceAnalysisSearchPrivateBindingDocument,
			duplicateKey,
		)
		if driftedReceiptSQL == workspaceAnalysisPublicationReadyFixtureSQL {
			t.Fatal("search private binding fixture was not replaced")
		}
		_, err = tx.Exec(ctx, driftedReceiptSQL)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("unsafe citation identity is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		unsafeReceiptSQL := strings.ReplaceAll(
			workspaceAnalysisPublicationReadyFixtureSQL,
			"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23",
			"/tmp/private",
		)
		_, err = tx.Exec(ctx, unsafeReceiptSQL)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("authority readers replay only the bound workspace run", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		insertWorkspaceAnalysisPublicationReadyFixture(t, ctx, pool)

		repository, err := toolspostgres.NewRepository(pool)
		if err != nil {
			t.Fatal(err)
		}
		workspaceID := foundation.ID("83000000-0000-4000-8000-000000000001")
		workflowRunID := foundation.ID("83000000-0000-4000-8000-000000000012")
		searchReceipt, err := repository.LoadSearchKnowledgeV2Receipt(ctx, workspaceID, workflowRunID)
		if err != nil {
			t.Fatalf("load SearchKnowledge@2 receipt: %v: %v: %v", err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
		}
		if searchReceipt.ID != "83000000-0000-4000-8000-000000000145" ||
			searchReceipt.ToolCallID != "83000000-0000-4000-8000-000000000143" {
			t.Fatalf("search receipt id=%s call=%s", searchReceipt.ID, searchReceipt.ToolCallID)
		}

		authority, err := repository.LoadValidateCitationV3Authority(ctx, toolsapplication.ValidateCitationV3AuthorityQuery{
			WorkspaceID: workspaceID, WorkflowRunID: workflowRunID,
			CandidateID: "83000000-0000-4000-8000-000000000166",
		})
		if err != nil {
			t.Fatalf("load ValidateCitation@3 authority: %v", err)
		}
		if authority.AnalysisRunID != "83000000-0000-4000-8000-000000000014" ||
			authority.SearchReceipt.ID != searchReceipt.ID || len(authority.EvidenceRefs) != 1 ||
			authority.EvidenceRefs[0] != "E1" || len(authority.ReadSourceReceipts) != 1 ||
			authority.ReadSourceReceipts[0].ID != "83000000-0000-4000-8000-000000000155" {
			t.Fatalf("authority=%s", authority.String())
		}

		readResolver, err := toolsretrieval.NewReadSourceV3ReceiptResolver(repository)
		if err != nil {
			t.Fatal(err)
		}
		readResolution, err := readResolver.ResolveReadSourceV3(ctx, toolsretrieval.ReadSourceV3ResolveRequest{
			WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, EvidenceRef: "E1",
		})
		if err != nil {
			t.Fatalf("resolve ReadSource@3 authority: %v", err)
		}
		if readResolution.Identity.CitationID != "cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23" ||
			readResolution.SearchReceiptID != searchReceipt.ID {
			t.Fatalf("read resolution=%s", readResolution.Identity.String())
		}

		citationResolver, err := toolsretrieval.NewValidateCitationV3ReceiptResolver(repository)
		if err != nil {
			t.Fatal(err)
		}
		citationResolution, err := citationResolver.ResolveValidateCitationV3(ctx, toolsretrieval.ValidateCitationV3ResolveRequest{
			WorkspaceID: workspaceID, WorkflowRunID: workflowRunID,
			CandidateID: authority.CandidateID, CandidateHash: authority.CandidateHash, EvidenceRefs: []string{"E1"},
		})
		if err != nil {
			t.Fatalf("resolve ValidateCitation@3 authority: %v", err)
		}
		if len(citationResolution.Identities) != 1 || citationResolution.Identities[0] != readResolution.Identity {
			t.Fatalf("citation resolution=%s", citationResolution.String())
		}

		if _, err := repository.LoadSearchKnowledgeV2Receipt(
			ctx, "83000000-0000-4000-8000-000000000002", workflowRunID,
		); err == nil {
			t.Fatal("cross-workspace authority query was accepted")
		}

		otherWorkflowRunID := foundation.ID("83000000-0000-4000-8000-000000000198")
		if _, err := pool.Exec(ctx, `
INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
VALUES ($1,'83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000011','running','{}',1,now(),now())`, otherWorkflowRunID); err != nil {
			t.Fatalf("insert same-workspace workflow run: %v", err)
		}
		if _, err := repository.LoadSearchKnowledgeV2Receipt(ctx, workspaceID, otherWorkflowRunID); err == nil {
			t.Fatal("same-workspace cross-run Search authority query was accepted")
		}
		if _, err := repository.LoadValidateCitationV3Authority(ctx, toolsapplication.ValidateCitationV3AuthorityQuery{
			WorkspaceID: workspaceID, WorkflowRunID: otherWorkflowRunID,
			CandidateID: "83000000-0000-4000-8000-000000000166",
		}); err == nil {
			t.Fatal("same-workspace cross-run Citation authority query was accepted")
		}
	})

	t.Run("source ordinal beyond selected refs is rejected", func(t *testing.T) {
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
		insertWorkspaceAnalysisPublicationReadyFixture(t, ctx, pool)
		_, err := pool.Exec(ctx, `
INSERT INTO workflow.tool_call(id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version)
VALUES ('83000000-0000-4000-8000-000000000193','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000150','83000000-0000-4000-8000-000000000151',2,'ReadSource',3,repeat('8',64),'tool.read_source.input',1,'tool.read_source.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',repeat('8',64),2,'{}','STARTED',false,now(),0,1);
INSERT INTO agent.workspace_analysis_operation(id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,ordinal,call_kind,request_hash,status,version,created_at,updated_at)
VALUES ('83000000-0000-4000-8000-000000000192','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000150','read_evidence','SOURCE_READ',2,'TOOL',repeat('8',64),'PENDING',1,now(),now());
UPDATE agent.workspace_analysis_operation SET status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000151',latest_node_attempt_id='83000000-0000-4000-8000-000000000151',tool_call_id='83000000-0000-4000-8000-000000000193',version=2,started_at=now(),updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000192';`)
		assertPostgresCode(t, err, "55000")
	})
}

func applyWorkspaceAnalysisPublicationMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	provider := workspaceAnalysisMigrationProvider(t, pool)
	insertWorkspaceAnalysisLegacyQuestion(t, ctx, pool)
	if _, err := provider.ApplyVersion(ctx, 85, true); err != nil {
		t.Fatalf("apply 00085: %v", err)
	}
	insertWorkspaceAnalysisRunFixture(t, ctx, pool)
	startWorkspaceAnalysisRun(t, ctx, pool)
	authorizeWorkspaceAnalysisGitOperation(t, ctx, pool)
	completeWorkspaceAnalysisGitOperation(t, ctx, pool)
}

func insertWorkspaceAnalysisPublicationReadyFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, workspaceAnalysisPublicationReadyFixtureSQL); err != nil {
		t.Fatalf("insert completed publication prerequisites: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit completed publication prerequisites: %v", err)
	}
}

func publishWorkspaceAnalysisCompletedProof(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proofID, document string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, workspaceAnalysisPublicationProofInsertSQL, proofID, document); err != nil {
		t.Fatalf("insert publication proof: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		status='succeeded',termination_reason='COMPLETED',
		validation_receipt_id='83000000-0000-4000-8000-000000000175',
		review_model_run_id='83000000-0000-4000-8000-000000000184',
		version=version+1,updated_at=terminal.at,completed_at=terminal.at
		FROM (SELECT clock_timestamp() AS at) AS terminal
		WHERE id='83000000-0000-4000-8000-000000000014'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.answer SET
		model_run_id='83000000-0000-4000-8000-000000000164',
		publication_status='completed',result_type='workspace_analysis',result=($1::text)::jsonb,
		result_hash=encode(sha256(convert_to($1::text,'UTF8')),'hex'),version=2,
		updated_at=published.at,published_at=published.at
		FROM (SELECT clock_timestamp() AS at) AS published
		WHERE id='83000000-0000-4000-8000-000000000013'`, document); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit completed publication: %v", err)
	}
}

const workspaceAnalysisPublicationProofInsertSQL = `
INSERT INTO agent.workspace_analysis_publication_proof(
    id,workspace_id,analysis_run_id,answer_id,workflow_run_id,
    finalization_node_run_id,finalization_node_attempt_id,candidate_id,candidate_hash,
    git_receipt_id,git_receipt_hash,validation_receipt_id,validation_receipt_hash,
    review_model_result_id,review_document_hash,published_document,published_result_hash,published_bytes,created_at
) SELECT
    $1,'83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014',
    '83000000-0000-4000-8000-000000000013','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000180','83000000-0000-4000-8000-000000000181',
    candidate.id,candidate.document_hash,git_receipt.id,git_receipt.output_hash,
    validation_receipt.id,validation_receipt.output_hash,review.id,review.document_hash,
    convert_to($2,'UTF8'),encode(sha256(convert_to($2,'UTF8')),'hex'),octet_length(convert_to($2,'UTF8')),clock_timestamp()
FROM agent.workspace_analysis_candidate AS candidate
JOIN workflow.tool_result_receipt AS git_receipt
  ON git_receipt.id='83000000-0000-4000-8000-000000000025'
JOIN workflow.tool_result_receipt AS validation_receipt
  ON validation_receipt.id='83000000-0000-4000-8000-000000000175'
JOIN agent.workspace_analysis_model_result AS review
  ON review.id='83000000-0000-4000-8000-000000000186'
WHERE candidate.id='83000000-0000-4000-8000-000000000166'`

const workspaceAnalysisPublicationReadyFixtureSQL = `
INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at) VALUES
('83000000-0000-4000-8000-000000000130','83000000-0000-4000-8000-000000000012','retrieve_evidence','workspace_analysis.retrieve','running',1,'{}','wa-worker',now()+interval '5 minutes',1,now(),now()),
('83000000-0000-4000-8000-000000000150','83000000-0000-4000-8000-000000000012','read_evidence','workspace_analysis.read','running',1,'{}','wa-worker',now()+interval '5 minutes',1,now(),now()),
('83000000-0000-4000-8000-000000000160','83000000-0000-4000-8000-000000000012','synthesize_answer','workspace_analysis.synthesize','running',1,'{}','wa-worker',now()+interval '5 minutes',1,now(),now()),
('83000000-0000-4000-8000-000000000170','83000000-0000-4000-8000-000000000012','validate_citations','workspace_analysis.validate','running',1,'{}','wa-worker',now()+interval '5 minutes',1,now(),now()),
('83000000-0000-4000-8000-000000000180','83000000-0000-4000-8000-000000000012','review_publish','workspace_analysis.review','running',1,'{}','wa-worker',now()+interval '5 minutes',1,now(),now());
INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at) VALUES
('83000000-0000-4000-8000-000000000131','83000000-0000-4000-8000-000000000130',1,1,0,'publication-retrieve','wa-worker',now()+interval '5 minutes','running',now()),
('83000000-0000-4000-8000-000000000151','83000000-0000-4000-8000-000000000150',1,1,0,'publication-source','wa-worker',now()+interval '5 minutes','running',now()),
('83000000-0000-4000-8000-000000000161','83000000-0000-4000-8000-000000000160',1,1,0,'publication-synthesis','wa-worker',now()+interval '5 minutes','running',now()),
('83000000-0000-4000-8000-000000000171','83000000-0000-4000-8000-000000000170',1,1,0,'publication-validation','wa-worker',now()+interval '5 minutes','running',now()),
('83000000-0000-4000-8000-000000000181','83000000-0000-4000-8000-000000000180',1,1,0,'publication-review','wa-worker',now()+interval '5 minutes','running',now());
INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at)
VALUES ('83000000-0000-4000-8000-000000000137','83000000-0000-4000-8000-000000000001','simple','v1',repeat('a',64),'{}','publication:index',repeat('b',64),0,'publication-index','building','["vector"]',1,now(),now());
INSERT INTO agent.model_run(id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,reduced_schema_id,reduced_schema_version,retrieval_index_version_id,status,version,started_at,updated_at) VALUES
('83000000-0000-4000-8000-000000000134','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000130','83000000-0000-4000-8000-000000000131','openai-compatible','v1','model-test','2026-08-15','default','v1','workspace-analysis-plan','v1','agent.workspace-analysis-plan','v1','agent.workspace-analysis-plan','v1','83000000-0000-4000-8000-000000000137','RUNNING',1,now(),now()),
('83000000-0000-4000-8000-000000000164','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000160','83000000-0000-4000-8000-000000000161','openai-compatible','v1','model-test','2026-08-15','default','v1','workspace-analysis-answer','v1','agent.workspace-analysis-candidate','1','agent.workspace-analysis-candidate','1','83000000-0000-4000-8000-000000000137','RUNNING',1,now(),now()),
('83000000-0000-4000-8000-000000000184','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000180','83000000-0000-4000-8000-000000000181','openai-compatible','v1','model-test','2026-08-15','default','v1','faithfulness-review','v1','agent.faithfulness-review','v1','agent.faithfulness-review','v1','83000000-0000-4000-8000-000000000137','RUNNING',1,now(),now());
INSERT INTO agent.model_call(id,model_run_id,call_no,phase,adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,request_hash,request_bytes,status,version,started_at) VALUES
('83000000-0000-4000-8000-000000000135','83000000-0000-4000-8000-000000000134',1,'PLAN','openai-compatible','v1','model-test','2026-08-15','default','v1','workspace-analysis-plan','v1','agent.workspace-analysis-plan','v1',256,repeat('1',64),128,'STARTED',1,now()),
('83000000-0000-4000-8000-000000000165','83000000-0000-4000-8000-000000000164',1,'ANSWER','openai-compatible','v1','model-test','2026-08-15','default','v1','workspace-analysis-answer','v1','agent.workspace-analysis-candidate','1',4096,repeat('2',64),128,'STARTED',1,now()),
('83000000-0000-4000-8000-000000000185','83000000-0000-4000-8000-000000000184',1,'REVIEW','openai-compatible','v1','model-test','2026-08-15','default','v1','faithfulness-review','v1','agent.faithfulness-review','v1',1024,repeat('3',64),128,'STARTED',1,now());
INSERT INTO workflow.tool_call(id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version) VALUES
('83000000-0000-4000-8000-000000000143','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000130','83000000-0000-4000-8000-000000000131',1,'SearchKnowledge',2,'db7180086adb06a18a4be8d1eb80a208fa6385c70f1a71d6d67c4f263fbd1807','tool.search_knowledge.input',2,'tool.search_knowledge.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',repeat('5',64),2,'{}','STARTED',false,now(),0,1),
('83000000-0000-4000-8000-000000000153','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000150','83000000-0000-4000-8000-000000000151',1,'ReadSource',3,'d41dabac535261b885e453b64e11fe5bb779838e31a255aea4798e27ead54d32','tool.read_source.input',2,'tool.read_source.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',repeat('6',64),2,'{}','STARTED',false,now(),0,1),
('83000000-0000-4000-8000-000000000173','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000170','83000000-0000-4000-8000-000000000171',1,'ValidateCitation',3,'bf5e643c47d57478b46d258eca250dc30e810ee7a0693569f44edaab19037d54','tool.validate_citation.input',2,'tool.validate_citation.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',repeat('7',64),2,'{}','STARTED',false,now(),0,1);
INSERT INTO agent.workspace_analysis_operation(id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,ordinal,call_kind,request_hash,status,version,created_at,updated_at) VALUES
('83000000-0000-4000-8000-000000000132','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000130','retrieve_evidence','RETRIEVAL_PLAN',1,'MODEL',repeat('1',64),'PENDING',1,now(),now()),
('83000000-0000-4000-8000-000000000142','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000130','retrieve_evidence','KNOWLEDGE_SEARCH',1,'TOOL',repeat('5',64),'PENDING',1,now(),now()),
('83000000-0000-4000-8000-000000000152','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000150','read_evidence','SOURCE_READ',1,'TOOL',repeat('6',64),'PENDING',1,now(),now()),
('83000000-0000-4000-8000-000000000162','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000160','synthesize_answer','ANSWER_SYNTHESIS',1,'MODEL',repeat('2',64),'PENDING',1,now(),now()),
('83000000-0000-4000-8000-000000000172','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000170','validate_citations','CITATION_VALIDATION',1,'TOOL',repeat('7',64),'PENDING',1,now(),now()),
('83000000-0000-4000-8000-000000000182','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000180','review_publish','FAITHFULNESS_REVIEW',1,'MODEL',repeat('3',64),'PENDING',1,now(),now());
UPDATE agent.workspace_analysis_operation SET
status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000131',
latest_node_attempt_id='83000000-0000-4000-8000-000000000131',
model_call_id='83000000-0000-4000-8000-000000000135',version=2,
started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
WHERE id='83000000-0000-4000-8000-000000000132';
INSERT INTO agent.workspace_analysis_budget_reservation(id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at) VALUES
('83000000-0000-4000-8000-000000000133','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000132','MODEL','83000000-0000-4000-8000-000000000135','RESERVED',1,0,0,65536,256,now());
UPDATE agent.model_call SET status='SUCCEEDED',response_hash=encode(sha256(convert_to('{"result_type":"workspace_analysis_plan","schema_id":"agent.workspace-analysis-plan","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000134","payload":{"intent":"inspect","requires_clarification":false,"rewrites":["analyze workspace"],"clarification_reason":"","clarification_question":"","suggested_scopes":[]}}','UTF8')),'hex'),response_bytes=octet_length(convert_to('{"result_type":"workspace_analysis_plan","schema_id":"agent.workspace-analysis-plan","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000134","payload":{"intent":"inspect","requires_clarification":false,"rewrites":["analyze workspace"],"clarification_reason":"","clarification_question":"","suggested_scopes":[]}}','UTF8')),input_tokens=10,output_tokens=5,latency_ms=10,version=2,completed_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000135';
UPDATE agent.model_run SET status='SUCCEEDED',final_result_type='workspace_analysis_plan',version=2,updated_at=terminal.at,completed_at=terminal.at FROM (SELECT clock_timestamp() AS at) terminal WHERE id='83000000-0000-4000-8000-000000000134';
INSERT INTO agent.workspace_analysis_model_result(id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,operation_kind,schema_id,schema_version,subject_candidate_id,subject_candidate_hash,document,document_hash,document_bytes,created_at)
SELECT '83000000-0000-4000-8000-000000000136','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000132','83000000-0000-4000-8000-000000000131','83000000-0000-4000-8000-000000000134',call.id,'RETRIEVAL_PLAN','agent.workspace-analysis-plan','v1',NULL,NULL,convert_to('{"result_type":"workspace_analysis_plan","schema_id":"agent.workspace-analysis-plan","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000134","payload":{"intent":"inspect","requires_clarification":false,"rewrites":["analyze workspace"],"clarification_reason":"","clarification_question":"","suggested_scopes":[]}}','UTF8'),call.response_hash,call.response_bytes,GREATEST(call.completed_at,model.completed_at)+interval '1 millisecond'
FROM agent.model_call AS call JOIN agent.model_run AS model ON model.id=call.model_run_id
WHERE call.id='83000000-0000-4000-8000-000000000135';
UPDATE agent.workspace_analysis_budget_reservation SET status='SETTLED',settled_model_calls=1,settled_input_tokens=10,settled_output_tokens=5,settled_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000133';
UPDATE agent.workspace_analysis_operation SET status='SUCCEEDED',result_kind='MODEL_RESULT_RECEIPT',result_id='83000000-0000-4000-8000-000000000136',result_hash=(SELECT document_hash FROM agent.workspace_analysis_model_result WHERE id='83000000-0000-4000-8000-000000000136'),version=3,completed_at=completion.at,updated_at=completion.at FROM (SELECT clock_timestamp() AS at) AS completion WHERE id='83000000-0000-4000-8000-000000000132';
UPDATE agent.workspace_analysis_operation SET
status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000131',
latest_node_attempt_id='83000000-0000-4000-8000-000000000131',
tool_call_id='83000000-0000-4000-8000-000000000143',version=2,
started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
WHERE id='83000000-0000-4000-8000-000000000142';
INSERT INTO agent.workspace_analysis_budget_reservation(id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at)
VALUES ('83000000-0000-4000-8000-000000000144','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000142','TOOL','83000000-0000-4000-8000-000000000143','RESERVED',0,1,0,0,0,now());
UPDATE workflow.tool_call SET
status='SUCCEEDED',response_hash=encode(sha256(convert_to('{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}','UTF8')),'hex'),
response_bytes=octet_length(convert_to('{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}','UTF8')),
response_summary='{}',completed_at=terminal.at,duration_ms=10,version=2
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000143';
INSERT INTO workflow.tool_result_receipt(id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at)
SELECT '83000000-0000-4000-8000-000000000145',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,call.node_attempt_id,call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,call.definition_hash,'PERSIST_CANONICAL',32768,16384,convert_to('{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}','UTF8'),call.response_hash,call.response_bytes,'tool.search_knowledge.private_binding',1,convert_to('{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000137","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}],"selected_refs":["E1"]}','UTF8'),encode(sha256(convert_to('{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000137","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}],"selected_refs":["E1"]}','UTF8')),'hex'),octet_length(convert_to('{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000137","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}],"selected_refs":["E1"]}','UTF8')),call.completed_at+interval '1 millisecond' FROM workflow.tool_call call WHERE call.id='83000000-0000-4000-8000-000000000143';
UPDATE agent.workspace_analysis_budget_reservation SET
status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000144';
UPDATE agent.workspace_analysis_operation SET
status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',
result_id='83000000-0000-4000-8000-000000000145',
result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000145'),
version=3,completed_at=completion.at,updated_at=completion.at
FROM (SELECT clock_timestamp() AS at) AS completion
WHERE id='83000000-0000-4000-8000-000000000142';

UPDATE agent.workspace_analysis_operation SET
status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000151',
latest_node_attempt_id='83000000-0000-4000-8000-000000000151',
tool_call_id='83000000-0000-4000-8000-000000000153',version=2,
started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
WHERE id='83000000-0000-4000-8000-000000000152';
INSERT INTO agent.workspace_analysis_budget_reservation(id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at)
VALUES ('83000000-0000-4000-8000-000000000154','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000152','TOOL','83000000-0000-4000-8000-000000000153','RESERVED',0,1,1,0,0,now());
UPDATE workflow.tool_call SET
status='SUCCEEDED',response_hash=encode(sha256(convert_to('{"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","excerpt":"Evidence","truncated":false}','UTF8')),'hex'),
response_bytes=octet_length(convert_to('{"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","excerpt":"Evidence","truncated":false}','UTF8')),
response_summary='{}',completed_at=terminal.at,duration_ms=10,version=2
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000153';
INSERT INTO workflow.tool_result_receipt(id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at)
SELECT '83000000-0000-4000-8000-000000000155',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,call.node_attempt_id,call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,call.definition_hash,'PERSIST_CANONICAL',8192,4096,convert_to('{"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","excerpt":"Evidence","truncated":false}','UTF8'),call.response_hash,call.response_bytes,'tool.read_source.private_binding',1,binding.document,encode(sha256(binding.document),'hex'),octet_length(binding.document),call.completed_at+interval '1 millisecond' FROM workflow.tool_call call CROSS JOIN LATERAL (SELECT convert_to('{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000137","search_receipt_hash":"' || (SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000145') || '","search_receipt_id":"83000000-0000-4000-8000-000000000145","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}','UTF8') document) binding WHERE call.id='83000000-0000-4000-8000-000000000153';
UPDATE agent.workspace_analysis_budget_reservation SET
status='SETTLED',settled_tool_calls=1,settled_source_reads=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000154';
UPDATE agent.workspace_analysis_operation SET
status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',
result_id='83000000-0000-4000-8000-000000000155',
result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000155'),
version=3,completed_at=completion.at,updated_at=completion.at
FROM (SELECT clock_timestamp() AS at) AS completion
WHERE id='83000000-0000-4000-8000-000000000152';
UPDATE agent.workspace_analysis_operation SET
status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000161',
latest_node_attempt_id='83000000-0000-4000-8000-000000000161',
model_call_id='83000000-0000-4000-8000-000000000165',version=2,
started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
WHERE id='83000000-0000-4000-8000-000000000162';
INSERT INTO agent.workspace_analysis_budget_reservation(id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at)
VALUES ('83000000-0000-4000-8000-000000000163','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000162','MODEL','83000000-0000-4000-8000-000000000165','RESERVED',1,0,0,65536,4096,now());
UPDATE agent.model_call SET status='SUCCEEDED',response_hash=encode(sha256(convert_to('` + workspaceAnalysisPublicationCandidateDocument + `','UTF8')),'hex'),response_bytes=octet_length(convert_to('` + workspaceAnalysisPublicationCandidateDocument + `','UTF8')),input_tokens=10,output_tokens=5,latency_ms=10,version=2,completed_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000165';
UPDATE agent.model_run SET status='SUCCEEDED',final_result_type='workspace_analysis_answer',version=2,updated_at=terminal.at,completed_at=terminal.at FROM (SELECT clock_timestamp() AS at) terminal WHERE id='83000000-0000-4000-8000-000000000164';
INSERT INTO agent.workspace_analysis_candidate(id,workspace_id,analysis_run_id,answer_id,synthesis_operation_id,node_attempt_id,synthesis_model_run_id,schema_id,schema_version,document,document_hash,document_bytes,created_at)
SELECT '83000000-0000-4000-8000-000000000166','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000013','83000000-0000-4000-8000-000000000162','83000000-0000-4000-8000-000000000161','83000000-0000-4000-8000-000000000164','agent.workspace-analysis-candidate',1,convert_to('` + workspaceAnalysisPublicationCandidateDocument + `','UTF8'),call.response_hash,call.response_bytes,GREATEST(call.completed_at,model.completed_at)+interval '1 millisecond'
FROM agent.model_call AS call JOIN agent.model_run AS model ON model.id=call.model_run_id
WHERE call.id='83000000-0000-4000-8000-000000000165';
UPDATE agent.workspace_analysis_budget_reservation SET status='SETTLED',settled_model_calls=1,settled_input_tokens=10,settled_output_tokens=5,settled_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000163';
UPDATE agent.workspace_analysis_operation SET status='SUCCEEDED',result_kind='SYNTHESIS_CANDIDATE',result_id='83000000-0000-4000-8000-000000000166',result_hash=(SELECT document_hash FROM agent.workspace_analysis_candidate WHERE id='83000000-0000-4000-8000-000000000166'),version=3,completed_at=completion.at,updated_at=completion.at FROM (SELECT clock_timestamp() AS at) AS completion WHERE id='83000000-0000-4000-8000-000000000162';
UPDATE agent.workspace_analysis_operation SET
status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000171',
latest_node_attempt_id='83000000-0000-4000-8000-000000000171',
tool_call_id='83000000-0000-4000-8000-000000000173',version=2,
started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
WHERE id='83000000-0000-4000-8000-000000000172';
INSERT INTO agent.workspace_analysis_budget_reservation(id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at)
VALUES ('83000000-0000-4000-8000-000000000174','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000172','TOOL','83000000-0000-4000-8000-000000000173','RESERVED',0,1,0,0,0,now());
UPDATE workflow.tool_call SET
status='SUCCEEDED',response_hash=encode(sha256(convert_to('{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}','UTF8')),'hex'),
response_bytes=octet_length(convert_to('{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}','UTF8')),
response_summary='{}',completed_at=terminal.at,duration_ms=10,version=2
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000173';
INSERT INTO workflow.tool_result_receipt(id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at)
SELECT '83000000-0000-4000-8000-000000000175',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,call.node_attempt_id,call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,call.definition_hash,'PERSIST_CANONICAL',16384,16384,convert_to('{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}','UTF8'),call.response_hash,call.response_bytes,'tool.validate_citation.private_binding',1,binding.document,encode(sha256(binding.document),'hex'),octet_length(binding.document),call.completed_at+interval '1 millisecond' FROM workflow.tool_call call CROSS JOIN LATERAL (SELECT convert_to('{"candidate_hash":"' || (SELECT document_hash FROM agent.workspace_analysis_candidate WHERE id='83000000-0000-4000-8000-000000000166') || '","candidate_id":"83000000-0000-4000-8000-000000000166","results":[{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000137","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}]}','UTF8') document) binding WHERE call.id='83000000-0000-4000-8000-000000000173';
UPDATE agent.workspace_analysis_budget_reservation SET
status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000174';
UPDATE agent.workspace_analysis_operation SET
status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',
result_id='83000000-0000-4000-8000-000000000175',
result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000175'),
version=3,completed_at=completion.at,updated_at=completion.at
FROM (SELECT clock_timestamp() AS at) AS completion
WHERE id='83000000-0000-4000-8000-000000000172';
UPDATE agent.workspace_analysis_operation SET
status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000181',
latest_node_attempt_id='83000000-0000-4000-8000-000000000181',
model_call_id='83000000-0000-4000-8000-000000000185',version=2,
started_at=auth_time.at,updated_at=auth_time.at
FROM (SELECT clock_timestamp() AS at) AS auth_time
WHERE id='83000000-0000-4000-8000-000000000182';
INSERT INTO agent.workspace_analysis_budget_reservation(id,workspace_id,analysis_run_id,operation_id,call_kind,model_call_id,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at)
VALUES ('83000000-0000-4000-8000-000000000183','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000182','MODEL','83000000-0000-4000-8000-000000000185','RESERVED',1,0,0,65536,1024,now());
UPDATE agent.model_call SET status='SUCCEEDED',response_hash=encode(sha256(convert_to('{"result_type":"faithfulness_review","schema_id":"agent.faithfulness-review","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000184","payload":{"passed":true,"items":[{"assertion_id":"@answer/conclusion","verdict":"SUPPORTED","citation_ids":["E1"],"reason":"Supported by E1."}],"summary":"Candidate is supported."}}','UTF8')),'hex'),response_bytes=octet_length(convert_to('{"result_type":"faithfulness_review","schema_id":"agent.faithfulness-review","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000184","payload":{"passed":true,"items":[{"assertion_id":"@answer/conclusion","verdict":"SUPPORTED","citation_ids":["E1"],"reason":"Supported by E1."}],"summary":"Candidate is supported."}}','UTF8')),input_tokens=10,output_tokens=5,latency_ms=10,version=2,completed_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000185';
UPDATE agent.model_run SET status='SUCCEEDED',final_result_type='faithfulness_review',version=2,updated_at=terminal.at,completed_at=terminal.at FROM (SELECT clock_timestamp() AS at) terminal WHERE id='83000000-0000-4000-8000-000000000184';
INSERT INTO agent.workspace_analysis_model_result(id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,operation_kind,schema_id,schema_version,subject_candidate_id,subject_candidate_hash,document,document_hash,document_bytes,created_at)
SELECT '83000000-0000-4000-8000-000000000186','83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000182','83000000-0000-4000-8000-000000000181','83000000-0000-4000-8000-000000000184',call.id,'FAITHFULNESS_REVIEW','agent.faithfulness-review','v1',candidate.id,candidate.document_hash,convert_to('{"result_type":"faithfulness_review","schema_id":"agent.faithfulness-review","schema_version":"v1","model_run_ref":"83000000-0000-4000-8000-000000000184","payload":{"passed":true,"items":[{"assertion_id":"@answer/conclusion","verdict":"SUPPORTED","citation_ids":["E1"],"reason":"Supported by E1."}],"summary":"Candidate is supported."}}','UTF8'),call.response_hash,call.response_bytes,GREATEST(call.completed_at,model.completed_at,candidate.created_at)+interval '1 millisecond'
FROM agent.model_call AS call
JOIN agent.model_run AS model ON model.id=call.model_run_id
JOIN agent.workspace_analysis_candidate AS candidate ON candidate.id='83000000-0000-4000-8000-000000000166'
WHERE call.id='83000000-0000-4000-8000-000000000185';
UPDATE agent.workspace_analysis_budget_reservation SET status='SETTLED',settled_model_calls=1,settled_input_tokens=10,settled_output_tokens=5,settled_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000183';
UPDATE agent.workspace_analysis_operation SET status='SUCCEEDED',result_kind='MODEL_RESULT_RECEIPT',result_id='83000000-0000-4000-8000-000000000186',result_hash=(SELECT document_hash FROM agent.workspace_analysis_model_result WHERE id='83000000-0000-4000-8000-000000000186'),version=3,completed_at=completion.at,updated_at=completion.at FROM (SELECT clock_timestamp() AS at) AS completion WHERE id='83000000-0000-4000-8000-000000000182';
UPDATE agent.workspace_analysis_run SET reserved_model_calls=0,reserved_tool_calls=0,reserved_source_reads=0,reserved_input_tokens=0,reserved_output_tokens=0,settled_model_calls=3,settled_tool_calls=4,settled_source_reads=1,settled_input_tokens=30,settled_output_tokens=15,version=version+1,updated_at=clock_timestamp() WHERE id='83000000-0000-4000-8000-000000000014';
`
