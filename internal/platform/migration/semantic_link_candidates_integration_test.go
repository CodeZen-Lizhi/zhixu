//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSemanticLinkCandidatesMigrationUpRepeatAndEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}

	assertSemanticLinkCandidatesMigrationShape(t, ctx, pool)

	provider := migrationProvider(t, pool)
	if _, err := provider.DownTo(ctx, 23); err != nil {
		t.Fatalf("00024 empty Down failed: %v", err)
	}

	var graphTables, proposalTypeCols, revisionTypedCols, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='graph' AND table_name IN (
			'semantic_link_candidate',
			'semantic_link_candidate_evidence',
			'semantic_link_candidate_decision',
			'semantic_link_scan'
		)`).Scan(&graphTables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='change_control'
		  AND table_name='proposal'
		  AND column_name='proposal_type'`).Scan(&proposalTypeCols); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='change_control'
		  AND table_name='proposal_revision'
		  AND column_name IN ('target_refs','base_versions','change_set','evidence_refs','schema_version')`).Scan(&revisionTypedCols); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='semantic_link_candidates'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if graphTables != 0 || proposalTypeCols != 0 || revisionTypedCols != 0 || schemaMeta != 0 {
		t.Fatalf("00024 Down graph_tables=%d proposal_type_cols=%d revision_typed_cols=%d schema_meta=%d",
			graphTables, proposalTypeCols, revisionTypedCols, schemaMeta)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("00024 Up after Down failed: %v", err)
	}
	assertSemanticLinkCandidatesMigrationShape(t, ctx, pool)
}

func TestSemanticLinkCandidatesMigrationTypedProposalCompatibilityAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSemanticLinkCandidatesTestDatabase(t, ctx, pool)

	const (
		workspaceID         = "d1000000-0000-4000-8000-000000000001"
		fileProposalID      = "d2000000-0000-4000-8000-000000000001"
		fileRevisionID      = "d2100000-0000-4000-8000-000000000001"
		knowledgeProposalID = "d2200000-0000-4000-8000-000000000001"
		knowledgeRevisionID = "d2300000-0000-4000-8000-000000000001"
	)

	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "typed-proposal")

	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,status,idempotency_key,request_hash,version,created_at,updated_at
	) VALUES($1,$2,'file_patch','ready_for_review','file-proposal',repeat('a',64),1,now(),now())`,
		fileProposalID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
	) VALUES(
		$1,$2,1,'docs/graph.md',repeat('b',64),'patched content','safe evidence summary',
		'low risk','rollback by revert',repeat('c',64),now()
	)`, fileRevisionID, fileProposalID); err != nil {
		t.Fatal(err)
	}

	var fileType string
	var fileTypedCols int
	if err := pool.QueryRow(ctx, `SELECT p.proposal_type,
		(r.target_refs IS NOT NULL)::int + (r.base_versions IS NOT NULL)::int +
		(r.change_set IS NOT NULL)::int + (r.evidence_refs IS NOT NULL)::int +
		(r.schema_version IS NOT NULL)::int
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id = p.id
		WHERE p.id = $1`, fileProposalID).Scan(&fileType, &fileTypedCols); err != nil {
		t.Fatal(err)
	}
	if fileType != "file_patch" || fileTypedCols != 0 {
		t.Fatalf("file proposal type=%s typed_cols=%d", fileType, fileTypedCols)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,status,idempotency_key,request_hash,version,created_at,updated_at
	) VALUES($1,$2,'knowledge_change','ready_for_review','knowledge-proposal',repeat('d',64),1,now(),now())`,
		knowledgeProposalID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,
		change_hash,target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
	) VALUES(
		$1,$2,1,NULL,NULL,NULL,NULL,'medium risk','open corrective proposal if approval fails',
		repeat('e',64),
		jsonb_build_array(jsonb_build_object(
			'type','RELATION_CANDIDATE',
			'id','d2400000-0000-4000-8000-000000000001',
			'fingerprint',repeat('f',64)
		)),
		'[{"node_type":"CLAIM","node_id":"d2500000-0000-4000-8000-000000000001","version":3}]'::jsonb,
		'{"operation":"CREATE_RELATION","relation_type":"COMPLEMENTS"}'::jsonb,
		jsonb_build_array(jsonb_build_object(
			'candidate_evidence_id','d2600000-0000-4000-8000-000000000001',
			'semantic_hash',repeat('9',64)
		)),
		'knowledge-relation-change/v1',now()
	)`, knowledgeRevisionID, knowledgeProposalID); err != nil {
		t.Fatal(err)
	}

	var knowledgeSchema string
	var legacyCols int
	if err := pool.QueryRow(ctx, `SELECT r.schema_version,
		(r.target_path IS NOT NULL)::int + (r.base_hash IS NOT NULL)::int +
		(r.content IS NOT NULL)::int + (r.evidence_summary IS NOT NULL)::int
		FROM change_control.proposal_revision r
		WHERE r.id = $1`, knowledgeRevisionID).Scan(&knowledgeSchema, &legacyCols); err != nil {
		t.Fatal(err)
	}
	if knowledgeSchema != "knowledge-relation-change/v1" || legacyCols != 0 {
		t.Fatalf("knowledge revision schema=%s legacy_cols=%d", knowledgeSchema, legacyCols)
	}

	_, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,
		change_hash,target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
	) VALUES(
		'd2700000-0000-4000-8000-000000000001',$1,2,'fake/path.md',repeat('1',64),'fake patch','fake evidence',
		'bad','rollback',repeat('2',64),
		jsonb_build_array(jsonb_build_object(
			'type','RELATION_CANDIDATE',
			'id','d2800000-0000-4000-8000-000000000001',
			'fingerprint',repeat('3',64)
		)),
		'[{"node_type":"CLAIM","node_id":"d2900000-0000-4000-8000-000000000001","version":4}]'::jsonb,
		'{"operation":"CREATE_RELATION","relation_type":"SUPPORTS"}'::jsonb,
		jsonb_build_array(jsonb_build_object(
			'candidate_evidence_id','da000000-0000-4000-8000-000000000001',
			'semantic_hash',repeat('4',64)
		)),
		'knowledge-relation-change/v1',now()
	)`, knowledgeProposalID)
	assertPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,
		change_hash,target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
	) VALUES(
		'db000000-0000-4000-8000-000000000001',$1,2,'docs/typed.md',repeat('5',64),'content','summary',
		'bad','rollback',repeat('6',64),
		jsonb_build_array(jsonb_build_object(
			'type','RELATION_CANDIDATE',
			'id','dc000000-0000-4000-8000-000000000001',
			'fingerprint',repeat('7',64)
		)),
		NULL,NULL,NULL,'knowledge-relation-change/v1',now()
	)`, fileProposalID)
	assertPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `UPDATE change_control.proposal
		SET proposal_type='file_patch',version=2,updated_at=now()
		WHERE id=$1`, knowledgeProposalID)
	assertPostgresCode(t, err, "23514")

	provider := migrationProvider(t, pool)
	_, err = provider.DownTo(ctx, 23)
	assertPostgresCode(t, err, "55000")
	if !strings.Contains(err.Error(), "cannot remove semantic link candidates with knowledge change proposals") {
		t.Fatalf("00024 guarded Down returned unexpected error: %v", err)
	}
	version, versionErr := provider.GetDBVersion(ctx)
	if versionErr != nil {
		t.Fatal(versionErr)
	}
	if version != 24 {
		t.Fatalf("00024 guarded Down left migration version=%d want=24", version)
	}
}

func TestSemanticLinkCandidatesMigrationCandidateDecisionScanAndConcurrency(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSemanticLinkCandidatesTestDatabase(t, ctx, pool)

	const (
		workspaceID   = "e1000000-0000-4000-8000-000000000001"
		runID         = "e2000000-0000-4000-8000-000000000001"
		definitionID  = "e2100000-0000-4000-8000-000000000001"
		candidateID   = "e3000000-0000-4000-8000-000000000001"
		evidenceID    = "e3100000-0000-4000-8000-000000000001"
		decisionID    = "e3200000-0000-4000-8000-000000000001"
		scanID        = "e3300000-0000-4000-8000-000000000001"
		sourceVersion = "e3400000-0000-4000-8000-000000000001"
		sourceSpan    = "e3500000-0000-4000-8000-000000000001"
		firstClaimID  = "e3600000-0000-4000-8000-000000000001"
		secondClaimID = "e3700000-0000-4000-8000-000000000001"
	)

	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "candidate-contract")
	insertSemanticLinkWorkflowRun(t, ctx, pool, workspaceID, definitionID, runID, "candidate-contract")
	insertSemanticLinkProvenanceFixture(t, ctx, pool, workspaceID, sourceVersion, sourceSpan, "candidate-contract")

	if _, err := pool.Exec(ctx, `INSERT INTO graph.semantic_link_candidate(
		id,workspace_id,source_node_type,source_node_id,source_node_version,
		target_node_type,target_node_id,target_node_version,
		relation_type,fingerprint_schema_version,fingerprint,status,reopened_reason,reopened_from_candidate_id,
		current_proposal_id,confidence_score,reason,source_summary,target_summary,discovery_methods,
		evidence_semantic_hashes,generation,version,created_at,updated_at
	) VALUES(
		$1,$2,'CLAIM',$3,3,'CLAIM',$4,5,
		'COMPLEMENTS','semantic-link-candidate/v1',repeat('a',64),'ACTIVE',NULL,NULL,
		NULL,0.85,'shared topic evidence','source summary','target summary',
		'["TITLE_ALIAS","COMMON_TOPIC"]'::jsonb,
		jsonb_build_array(repeat('b',64)),
		'{"kind":"rule","version":"v1"}'::jsonb,1,now(),now()
	)`, candidateID, workspaceID, firstClaimID, secondClaimID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `UPDATE graph.semantic_link_candidate
		SET status='DEFERRED',version=2,updated_at=now()
		WHERE id=$1`, candidateID); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `UPDATE graph.semantic_link_candidate
		SET status='ACTIVE',version=2,updated_at=now()
		WHERE id=$1`, candidateID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE graph.semantic_link_candidate
		SET status='ACTIVE',version=3,updated_at=now()
		WHERE id=$1`, candidateID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO graph.semantic_link_candidate_evidence(
		id,workspace_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at
	) VALUES(
		$1,$2,$3,$4,$5,1,repeat('c',64),'bounded evidence summary','bounded excerpt',now()
	)`, evidenceID, workspaceID, candidateID, sourceVersion, sourceSpan); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO graph.semantic_link_candidate_evidence(
		id,workspace_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at
	) VALUES(
		'e3800000-0000-4000-8000-000000000001',$1,$2,$3,$4,2,repeat('c',64),'dup summary','dup excerpt',now()
	)`, workspaceID, candidateID, sourceVersion, sourceSpan)
	assertPostgresCode(t, err, "23505")

	if _, err := pool.Exec(ctx, `INSERT INTO graph.semantic_link_candidate_decision(
		id,workspace_id,candidate_id,candidate_version,proposal_id,idempotency_key,request_hash,
		action,relation_type,reason,defer_until,created_at
	) VALUES(
		$1,$2,$3,3,NULL,'decision-ignore',repeat('d',64),
		'IGNORE',NULL,'already reviewed elsewhere',NULL,now()
	)`, decisionID, workspaceID, candidateID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE graph.semantic_link_candidate_decision
		SET reason='mutated'
		WHERE id=$1`, decisionID)
	assertPostgresCode(t, err, "55000")

	if _, err := pool.Exec(ctx, `INSERT INTO graph.semantic_link_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,
		idempotency_key,request_hash,workflow_run_id,status,total_nodes,processed_nodes,
		candidate_count,suppressed_count,reopened_count,failed_count,checkpoint,last_error,
		version,created_at,updated_at,completed_at
	) VALUES(
		$1,$2,'TOPIC',$4,3,'semantic-link-scan-scope/v1',repeat('e',64),
		'scan-topic',repeat('f',64),$3,'RUNNING',1,0,0,0,0,0,'{"page":1}'::jsonb,NULL,
		1,now(),now(),NULL
	)`, scanID, workspaceID, runID, "topic:"+firstClaimID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE graph.semantic_link_scan
		SET processed_nodes=1,candidate_count=1,status='SUCCEEDED',
		    completed_at=now(),version=2,updated_at=now()
		WHERE id=$1`, scanID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE graph.semantic_link_scan
		SET processed_nodes=0,version=3,updated_at=now()
		WHERE id=$1`, scanID)
	assertPostgresCode(t, err, "23514")

	concurrencyTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = concurrencyTx.Rollback(context.Background()) }()

	if _, err := concurrencyTx.Exec(ctx, `INSERT INTO graph.semantic_link_candidate(
		id,workspace_id,source_node_type,source_node_id,source_node_version,
		target_node_type,target_node_id,target_node_version,
		relation_type,fingerprint_schema_version,fingerprint,status,reopened_reason,reopened_from_candidate_id,
		current_proposal_id,confidence_score,reason,source_summary,target_summary,discovery_methods,
		evidence_semantic_hashes,generation,version,created_at,updated_at
	) VALUES(
		'e3900000-0000-4000-8000-000000000001',$1,'CLAIM','e3a00000-0000-4000-8000-000000000001',1,
		'CLAIM','e3b00000-0000-4000-8000-000000000001',1,
		'COMPLEMENTS','semantic-link-candidate/v1',repeat('9',64),'ACTIVE',NULL,NULL,
		NULL,NULL,'concurrent winner','left','right','["TERM_MATCH"]'::jsonb,
		jsonb_build_array(repeat('8',64)),'{"kind":"rule","version":"v1"}'::jsonb,
		1,now(),now()
	)`, workspaceID); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, execErr := pool.Exec(ctx, `INSERT INTO graph.semantic_link_candidate(
			id,workspace_id,source_node_type,source_node_id,source_node_version,
			target_node_type,target_node_id,target_node_version,
			relation_type,fingerprint_schema_version,fingerprint,status,reopened_reason,reopened_from_candidate_id,
			current_proposal_id,confidence_score,reason,source_summary,target_summary,discovery_methods,
			evidence_semantic_hashes,generation,version,created_at,updated_at
		) VALUES(
			'e3c00000-0000-4000-8000-000000000001',$1,'CLAIM','e3d00000-0000-4000-8000-000000000001',1,
			'CLAIM','e3e00000-0000-4000-8000-000000000001',1,
			'COMPLEMENTS','semantic-link-candidate/v1',repeat('9',64),'ACTIVE',NULL,NULL,
			NULL,NULL,'concurrent loser','left','right','["TERM_MATCH"]'::jsonb,
			jsonb_build_array(repeat('8',64)),'{"kind":"rule","version":"v1"}'::jsonb,
			1,now(),now()
		)`, workspaceID)
		errCh <- execErr
	}()

	time.Sleep(100 * time.Millisecond)
	if err := concurrencyTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertPostgresCode(t, <-errCh, "23505")

	var duplicateCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate
		WHERE workspace_id=$1 AND fingerprint=repeat('9',64)`, workspaceID).Scan(&duplicateCount); err != nil {
		t.Fatal(err)
	}
	if duplicateCount != 1 {
		t.Fatalf("concurrent fingerprint rows=%d", duplicateCount)
	}

	provider := migrationProvider(t, pool)
	_, err = provider.DownTo(ctx, 23)
	assertPostgresCode(t, err, "55000")
	if !strings.Contains(err.Error(), "cannot remove semantic link candidates with candidate data") {
		t.Fatalf("00024 guarded Down returned unexpected error: %v", err)
	}
	version, versionErr := provider.GetDBVersion(ctx)
	if versionErr != nil {
		t.Fatal(versionErr)
	}
	if version != 24 {
		t.Fatalf("00024 guarded Down left migration version=%d want=24", version)
	}
}

func assertSemanticLinkCandidatesMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var graphTables, graphTriggers, proposalTypeCols, revisionTypedCols, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='graph' AND table_name IN (
			'semantic_link_candidate',
			'semantic_link_candidate_evidence',
			'semantic_link_candidate_decision',
			'semantic_link_scan'
		)`).Scan(&graphTables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgname IN (
			'proposal_revision_validate_payload',
			'semantic_link_candidate_validate_write',
			'semantic_link_candidate_evidence_validate_insert',
			'semantic_link_candidate_evidence_reject_update_delete',
			'semantic_link_candidate_decision_validate_insert',
			'semantic_link_candidate_decision_reject_update_delete',
			'semantic_link_scan_validate_write'
		)`).Scan(&graphTriggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='change_control'
		  AND table_name='proposal'
		  AND column_name='proposal_type'`).Scan(&proposalTypeCols); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='change_control'
		  AND table_name='proposal_revision'
		  AND column_name IN ('target_refs','base_versions','change_set','evidence_refs','schema_version')`).Scan(&revisionTypedCols); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='semantic_link_candidates' AND value='m7-02'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if graphTables != 4 || graphTriggers != 7 || proposalTypeCols != 1 || revisionTypedCols != 5 || schemaMeta != 1 {
		t.Fatalf("shape graph_tables=%d graph_triggers=%d proposal_type_cols=%d revision_typed_cols=%d schema_meta=%d",
			graphTables, graphTriggers, proposalTypeCols, revisionTypedCols, schemaMeta)
	}
}

func migrateSemanticLinkCandidatesTestDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := migrationProvider(t, pool).UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
}

func insertSemanticLinkWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, label string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,$2,$3,$3,now(),'active',now(),now())`,
		workspaceID, label, "/tmp/"+label); err != nil {
		t.Fatal(err)
	}
}

func insertSemanticLinkWorkflowRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, definitionID, runID, key string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(
		id,workspace_id,key,version,graph,created_at
	) VALUES($1,$2,$3,1,'{"nodes":[]}',now())`, definitionID, workspaceID, key); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES($1,$2,$3,'running','{}',1,now(),now())`, runID, workspaceID, definitionID); err != nil {
		t.Fatal(err)
	}
}

func insertSemanticLinkProvenanceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceVersionID, sourceSpanID, label string) {
	t.Helper()
	artifactID := strings.Replace(sourceVersionID, "4000-8000", "4000-9000", 1)
	sourceID := strings.Replace(sourceVersionID, "4000-8000", "4000-a000", 1)
	projectionID := strings.Replace(sourceSpanID, "4000-8000", "4000-b000", 1)
	contentHash := strings.Repeat("1", 64)
	parserHash := strings.Repeat("2", 64)
	normalizedHash := strings.Repeat("3", 64)
	excerptHash := strings.Repeat("4", 64)

	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
			VALUES($1,$2,$3,4,$4,now())`, []any{artifactID, workspaceID, contentHash, ".knowledge/sources/" + contentHash}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
			VALUES($1,$2,'text',$3,$3,now())`, []any{sourceID, workspaceID, label + ".txt"}},
		{`INSERT INTO core.source_version(
			id,source_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		) VALUES($1,$2,$3,$4,4,'text/plain',$5,'pending',now())`,
			[]any{sourceVersionID, sourceID, artifactID, contentHash, label + ".txt"}},
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',now())`,
			[]any{projectionID, workspaceID, artifactID, parserHash, normalizedHash}},
		{`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,
			start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',$5,'v1','v1',now())`,
			[]any{sourceSpanID, workspaceID, artifactID, projectionID, excerptHash}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,now())`, []any{sourceVersionID, projectionID, workspaceID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
