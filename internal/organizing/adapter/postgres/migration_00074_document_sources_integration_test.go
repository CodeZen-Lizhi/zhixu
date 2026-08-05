//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration00074ProjectsVerifiedDocumentSourcesAndGuardsDeferredRetrieval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newOrganizingIntegrationDatabase(t, ctx)
	workspaceID := organizingIntegrationID(740)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-v2-document-source")
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	documentID := organizingIntegrationID(741)
	articleRevisionID := organizingIntegrationID(742)
	artifactID := organizingIntegrationID(743)
	artifactRevisionID := organizingIntegrationID(744)
	articleHash := organizingIntegrationHash("organizing-v2-article")
	sections := organizingV2DocumentSourceSections(t, documentID, articleRevisionID, articleHash, false)
	seedOrganizingV2ArtifactRevision(t, ctx, pool, workspaceID, documentID, articleRevisionID, artifactID,
		artifactRevisionID, articleHash, sections, now)

	var projectedDocumentID, projectedArticleID string
	var projectedRevisionNo int
	var projectedHash string
	if err := pool.QueryRow(ctx, `SELECT document_id,article_revision_id,revision_no,content_hash
		FROM learning.artifact_revision_document_source
		WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(artifactRevisionID)).Scan(
		&projectedDocumentID, &projectedArticleID, &projectedRevisionNo, &projectedHash,
	); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if projectedDocumentID != string(documentID) || projectedArticleID != string(articleRevisionID) ||
		projectedRevisionNo != 1 || projectedHash != articleHash {
		t.Fatalf("unexpected document source projection: document=%s article=%s revision=%d hash=%s",
			projectedDocumentID, projectedArticleID, projectedRevisionNo, projectedHash)
	}

	invalidSections := organizingV2DocumentSourceSections(t, documentID, articleRevisionID, articleHash, true)
	_, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,2,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(organizingIntegrationID(745)), string(artifactID), string(workspaceID), string(invalidSections),
		organizingIntegrationHash("organizing-v2-invalid-revision"), now,
	)
	organizingIntegrationPostgresCode(t, err, "23514")

	repeatedRevisionID := organizingIntegrationID(752)
	repeatedSections := organizingV2DocumentSourceSectionsForKeys(t, documentID, articleRevisionID, articleHash,
		false, "section-1", "section-2")
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,2,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(repeatedRevisionID), string(artifactID), string(workspaceID), string(repeatedSections),
		organizingIntegrationHash("organizing-v2-repeated-source"), now,
	); err != nil {
		organizingIntegrationFatal(t, err)
	}
	var repeatedProjectionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision_document_source
		WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(repeatedRevisionID)).Scan(&repeatedProjectionCount); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if repeatedProjectionCount != 1 {
		t.Fatalf("cross-section document source projection count=%d want=1", repeatedProjectionCount)
	}

	boundedSources := []any{organizingV2DocumentSourceValue(documentID, articleRevisionID, articleHash, false)}
	for index := 0; index < 63; index++ {
		boundedDocumentID := organizingIntegrationID(800 + index)
		boundedRevisionID := organizingIntegrationID(900 + index)
		boundedContent := fmt.Sprintf("organizing-v2-bounded-source-%d", index)
		boundedHash := organizingIntegrationHash(boundedContent)
		seedOrganizingArticleRevision(t, ctx, pool, workspaceID, boundedDocumentID, boundedRevisionID,
			boundedContent, boundedHash, fmt.Sprintf("organizing-v2-bounded-%d.md", index), now)
		boundedSources = append(boundedSources, organizingV2DocumentSourceValue(
			boundedDocumentID, boundedRevisionID, boundedHash, false,
		))
	}
	boundedRevisionID := organizingIntegrationID(780)
	boundedSections := organizingV2SectionsWithDocumentSources(t, boundedSources, "section-1")
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,3,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(boundedRevisionID), string(artifactID), string(workspaceID), string(boundedSections),
		organizingIntegrationHash("organizing-v2-bounded-revision"), now,
	); err != nil {
		organizingIntegrationFatal(t, err)
	}
	var boundedProjectionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision_document_source
		WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(boundedRevisionID)).Scan(&boundedProjectionCount); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if boundedProjectionCount != 64 {
		t.Fatalf("bounded document source projection count=%d want=64", boundedProjectionCount)
	}
	overflowSources := append(append([]any(nil), boundedSources...), boundedSources[0])
	overflowSections := organizingV2SectionsWithDocumentSources(t, overflowSources, "section-1")
	_, err = pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,4,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(organizingIntegrationID(781)), string(artifactID), string(workspaceID), string(overflowSections),
		organizingIntegrationHash("organizing-v2-overflow-revision"), now,
	)
	organizingIntegrationPostgresCode(t, err, "23514")

	duplicateSections := organizingV2DocumentSourceSectionsForKeys(t, documentID, articleRevisionID, articleHash,
		false, "section-1")
	var duplicatePayload []map[string]any
	if err := json.Unmarshal(duplicateSections, &duplicatePayload); err != nil {
		t.Fatal(err)
	}
	duplicatePayload[0]["DocumentSources"] = append(
		duplicatePayload[0]["DocumentSources"].([]any),
		duplicatePayload[0]["DocumentSources"].([]any)[0],
	)
	duplicateSections, err = json.Marshal(duplicatePayload)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,4,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(organizingIntegrationID(753)), string(artifactID), string(workspaceID), string(duplicateSections),
		organizingIntegrationHash("organizing-v2-duplicate-section-source"), now,
	)
	organizingIntegrationPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `UPDATE core.document
		SET lifecycle_status='DELETED',version=version+1,updated_at=$2 WHERE id=$1`, string(documentID), now.Add(time.Minute)); err != nil {
		organizingIntegrationFatal(t, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,4,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(organizingIntegrationID(754)), string(artifactID), string(workspaceID), string(sections),
		organizingIntegrationHash("organizing-v2-deleted-document-source"), now.Add(time.Minute),
	)
	organizingIntegrationPostgresCode(t, err, "23514")

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	templates, revisions, err := organizingdomain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := repository.ConfirmDraft(ctx, seedOrganizingConfirmRecord(
		t, ctx, repository, workspaceID, templates[0], revisions[0], &organizingIntegrationFence{}, 760, now,
	))
	if err != nil || confirmed.Replayed {
		t.Fatalf("confirm deferred-retrieval fixture=%#v err=%v", confirmed, err)
	}

	definitionID := organizingIntegrationID(770)
	workflowRunID := organizingIntegrationID(771)
	nodeRunID := organizingIntegrationID(772)
	nodeAttemptID := organizingIntegrationID(773)
	generationID := organizingIntegrationID(774)
	modelRunID := organizingIntegrationID(775)
	leaseUntil := now.Add(2 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'organizing.topic-article',1,'{}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash
	) VALUES($1,$2,$3,'running',jsonb_build_object('snapshot_id',$4::text),1,$5,$5,$6,$7)`,
		string(workflowRunID), string(workspaceID), string(definitionID), string(confirmed.Snapshot.ID), now,
		organizingapp.StartIdempotencyKey(confirmed.Snapshot.ID), organizingIntegrationHash("organizing-v2-workflow")); err != nil {
		organizingIntegrationFatal(t, err)
	}
	lease, claimed, err := repository.ClaimStart(ctx, "organizing-v2-worker", time.Minute)
	if err != nil || !claimed || lease.SnapshotID != confirmed.Snapshot.ID {
		t.Fatalf("claim deferred-retrieval start lease=%#v claimed=%v err=%v", lease, claimed, err)
	}
	if _, replayed, err := repository.CompleteStart(ctx, organizingapp.CompleteStartRecord{
		Lease: lease, BindingID: organizingIntegrationID(776), WorkflowRunID: workflowRunID,
		DefinitionKey: "organizing.topic-article", DefinitionVersion: 1, StartedAt: time.Now().UTC(),
	}); err != nil || replayed {
		t.Fatalf("complete deferred-retrieval start replayed=%v err=%v", replayed, err)
	}
	seedOrganizingRunningNodeAttempt(t, ctx, pool, workflowRunID, nodeRunID, nodeAttemptID,
		"organizing-v2-generation", leaseUntil, now)
	if _, err := pool.Exec(ctx, `INSERT INTO organizing.generation(
		id,workspace_id,snapshot_id,workflow_run_id,node_run_id,node_attempt_id,generation_kind,
		request_hash,status,retryable,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,'OUTLINE',$7,'RUNNING',false,1,$8,$8)`,
		string(generationID), string(workspaceID), string(confirmed.Snapshot.ID), string(workflowRunID),
		string(nodeRunID), string(nodeAttemptID), organizingIntegrationHash("organizing-v2-generation"), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if err := insertOrganizingDeferredModelRun(ctx, pool, workspaceID, workflowRunID, nodeRunID,
		nodeAttemptID, modelRunID, "organizing.outline-generation", now); err != nil {
		organizingIntegrationFatal(t, err)
	}

	mismatchNodeRunID := organizingIntegrationID(790)
	mismatchNodeAttemptID := organizingIntegrationID(791)
	seedOrganizingRunningNodeAttempt(t, ctx, pool, workflowRunID, mismatchNodeRunID, mismatchNodeAttemptID,
		"organizing-v2-kind-mismatch", leaseUntil, now)
	if _, err := pool.Exec(ctx, `INSERT INTO organizing.generation(
		id,workspace_id,snapshot_id,workflow_run_id,node_run_id,node_attempt_id,generation_kind,
		request_hash,status,retryable,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,'OUTLINE',$7,'RUNNING',false,1,$8,$8)`,
		string(organizingIntegrationID(792)), string(workspaceID), string(confirmed.Snapshot.ID), string(workflowRunID),
		string(mismatchNodeRunID), string(mismatchNodeAttemptID), organizingIntegrationHash("organizing-v2-kind-mismatch"), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	err = insertOrganizingDeferredModelRun(ctx, pool, workspaceID, workflowRunID, mismatchNodeRunID,
		mismatchNodeAttemptID, organizingIntegrationID(793), "organizing.document-generation", now)
	organizingIntegrationPostgresCode(t, err, "55000")

	unboundNodeRunID := organizingIntegrationID(777)
	unboundNodeAttemptID := organizingIntegrationID(778)
	seedOrganizingRunningNodeAttempt(t, ctx, pool, workflowRunID, unboundNodeRunID, unboundNodeAttemptID,
		"organizing-v2-unbound", leaseUntil, now)
	err = insertOrganizingDeferredModelRun(ctx, pool, workspaceID, workflowRunID, unboundNodeRunID,
		unboundNodeAttemptID, organizingIntegrationID(779), "organizing.outline-generation", now)
	organizingIntegrationPostgresCode(t, err, "55000")

	provider := organizingMigrationProvider(t, pool)
	_, err = provider.DownTo(ctx, 73)
	organizingIntegrationPostgresCode(t, err, "55000")
}

func organizingV2DocumentSourceSections(t *testing.T, documentID, articleRevisionID foundation.ID, contentHash string,
	includeUnexpectedField bool,
) []byte {
	t.Helper()
	return organizingV2DocumentSourceSectionsForKeys(t, documentID, articleRevisionID, contentHash,
		includeUnexpectedField, "section-1")
}

func organizingV2DocumentSourceSectionsForKeys(t *testing.T, documentID, articleRevisionID foundation.ID,
	contentHash string, includeUnexpectedField bool, sectionKeys ...string,
) []byte {
	t.Helper()
	if len(sectionKeys) == 0 {
		t.Fatal("document source fixture requires at least one section")
	}
	documentSource := organizingV2DocumentSourceValue(documentID, articleRevisionID, contentHash, includeUnexpectedField)
	return organizingV2SectionsWithDocumentSources(t, []any{documentSource}, sectionKeys...)
}

func organizingV2DocumentSourceValue(documentID, articleRevisionID foundation.ID, contentHash string,
	includeUnexpectedField bool,
) map[string]any {
	documentSource := map[string]any{
		"DocumentID":          string(documentID),
		"ArticleRevisionID":   string(articleRevisionID),
		"RevisionNo":          1,
		"VerifiedContentHash": contentHash,
		"Verified":            true,
	}
	if includeUnexpectedField {
		documentSource["Unexpected"] = "rejected"
	}
	return documentSource
}

func organizingV2SectionsWithDocumentSources(t *testing.T, documentSources []any, sectionKeys ...string) []byte {
	t.Helper()
	if len(sectionKeys) == 0 || len(documentSources) == 0 {
		t.Fatal("document source fixture requires sections and document sources")
	}
	values := make([]map[string]any, len(sectionKeys))
	for index, key := range sectionKeys {
		values[index] = map[string]any{
			"Key": key, "Title": "Document source", "Content": "verified content",
			"Citations": []any{}, "DocumentSources": documentSources,
		}
	}
	sections, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return sections
}

func insertOrganizingDeferredModelRun(ctx context.Context, pool *pgxpool.Pool,
	workspaceID, workflowRunID, nodeRunID, nodeAttemptID, modelRunID foundation.ID,
	outputSchemaID string, now time.Time,
) error {
	_, err := pool.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,retrieval_index_version_id,embedding_version_id,
		rerank_model_version,status,version,started_at,updated_at
	) VALUES(
		$1,$2,$3,$4,$5,'test-adapter','v1','test-model','v1','test-profile','v1',
		'test-prompt','v1',$6,'v1','test-reduced','v1',NULL,NULL,NULL,'RUNNING',1,$7,$7
	)`, string(modelRunID), string(workspaceID), string(workflowRunID), string(nodeRunID),
		string(nodeAttemptID), outputSchemaID, now)
	return err
}

func seedOrganizingArticleRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	workspaceID, documentID, articleRevisionID foundation.ID, content, contentHash, canonicalPath string, now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,version,created_at,updated_at
	) VALUES($1,$2,$3,'Bounded source','DRAFT',1,$4,$4)`,
		string(documentID), string(workspaceID), canonicalPath, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,created_by_type,created_at
	) VALUES($1,$2,$3,NULL,1,$4,$5,'DRAFT','NONE','USER',$6)`,
		string(articleRevisionID), string(workspaceID), string(documentID), content, contentHash, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

func seedOrganizingRunningNodeAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	workflowRunID, nodeRunID, nodeAttemptID foundation.ID, key string, leaseUntil, now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,
		output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,$3,'model.organizing','running',1,'{}',$3,1,1,1,'organizing-worker',$4,1,$5,$5)`,
		string(nodeRunID), string(workflowRunID), key, leaseUntil, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,
		started_at,heartbeat_at,created_at
	) VALUES($1,$2,1,1,0,$3,'organizing-worker',$4,'running',$5,$5,$5)`,
		string(nodeAttemptID), string(nodeRunID), key, leaseUntil, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

func seedOrganizingV2ArtifactRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	workspaceID, documentID, articleRevisionID, artifactID, artifactRevisionID foundation.ID,
	articleHash string, sections []byte, createdAt time.Time,
) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		organizingIntegrationFatal(t, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,version,created_at,updated_at
	) VALUES($1,$2,'organizing-v2.md','Organizing V2','DRAFT',1,$3,$3)`,
		string(documentID), string(workspaceID), createdAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,created_by_type,created_at
	) VALUES($1,$2,$3,NULL,1,'verified content',$4,'DRAFT','NONE','USER',$5)`,
		string(articleRevisionID), string(workspaceID), string(documentID), articleHash, createdAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO learning.artifact(
		id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,
		domain_schema_version,scope_definition,source_coverage,current_revision_id
	) VALUES($1,$2,'SUMMARY','Organizing V2','{}'::jsonb,'PLANNING',1,$3,$3,
		'artifact/v1','document-source projection','[]'::jsonb,$4)`,
		string(artifactID), string(workspaceID), createdAt, string(artifactRevisionID)); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO learning.artifact_revision(
		id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
		content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
	) VALUES($1,$2,$3,1,'SNAPSHOT','[]'::jsonb,$4::jsonb,'[]'::jsonb,'[]'::jsonb,'[]'::jsonb,
		'', '{}'::jsonb,'artifact-revision/v2',$5,'HUMAN',NULL,$6)`,
		string(artifactRevisionID), string(artifactID), string(workspaceID), string(sections),
		organizingIntegrationHash("organizing-v2-artifact-revision"), createdAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if err = tx.Commit(ctx); err != nil {
		organizingIntegrationFatal(t, err)
	}
}
