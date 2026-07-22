//go:build integration

package workspacepostgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryListSourceVersionsWithPostgres(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	workspaceID := sourceVersionListID(1)
	otherWorkspaceID := sourceVersionListID(2)
	insertSourceVersionListWorkspace(t, ctx, tx, workspaceID, "source-list", now)
	insertSourceVersionListWorkspace(t, ctx, tx, otherWorkspaceID, "source-list-other", now)
	definitionID := sourceVersionListID(3)
	otherDefinitionID := sourceVersionListID(4)
	insertSourceVersionListDefinition(t, ctx, tx, definitionID, workspaceID, now)
	insertSourceVersionListDefinition(t, ctx, tx, otherDefinitionID, otherWorkspaceID, now)
	runningRunID := sourceVersionListID(5)
	waitingRunID := sourceVersionListID(6)
	otherRunID := sourceVersionListID(7)
	insertSourceVersionListRun(t, ctx, tx, runningRunID, workspaceID, definitionID, "running", now)
	insertSourceVersionListRun(t, ctx, tx, waitingRunID, workspaceID, definitionID, "waiting_for_human", now)
	insertSourceVersionListRun(t, ctx, tx, otherRunID, otherWorkspaceID, otherDefinitionID, "running", now)

	sourceOneID := sourceVersionListID(11)
	sourceTwoID := sourceVersionListID(12)
	sourceThreeID := sourceVersionListID(13)
	otherSourceID := sourceVersionListID(14)
	firstID := sourceVersionListID(21)
	secondID := sourceVersionListID(22)
	thirdID := sourceVersionListID(23)
	fourthID := sourceVersionListID(24)
	otherID := sourceVersionListID(25)
	insertSourceVersionFixture(t, ctx, tx, sourceVersionFixture{SourceID: sourceOneID, VersionID: firstID, WorkspaceID: workspaceID, Path: "docs/current.md", MimeType: "text/markdown", SecurityStatus: "passed", CapturedAt: now.Add(8 * time.Hour), HashSeed: "1"})
	insertSourceVersionFixture(t, ctx, tx, sourceVersionFixture{SourceID: sourceOneID, VersionID: secondID, WorkspaceID: workspaceID, Path: "docs/current.md", MimeType: "text/markdown", SecurityStatus: "passed", CapturedAt: now.Add(7 * time.Hour), HashSeed: "2"})
	// The immutable Source Version capture status stays passed; Inbox must expose
	// the later quarantined Ingestion Attempt as the current security fact.
	insertSourceVersionFixture(t, ctx, tx, sourceVersionFixture{SourceID: sourceTwoID, VersionID: thirdID, WorkspaceID: workspaceID, Path: "docs/excluded.pdf", MimeType: "application/pdf", SecurityStatus: "passed", CapturedAt: now.Add(6 * time.Hour), HashSeed: "3"})
	insertSourceVersionFixture(t, ctx, tx, sourceVersionFixture{SourceID: sourceThreeID, VersionID: fourthID, WorkspaceID: workspaceID, Path: "notes/unprocessed.txt", MimeType: "text/plain", SecurityStatus: "pending", CapturedAt: now.Add(5 * time.Hour), HashSeed: "4"})
	insertSourceVersionFixture(t, ctx, tx, sourceVersionFixture{SourceID: otherSourceID, VersionID: otherID, WorkspaceID: otherWorkspaceID, Path: "other/hidden.md", MimeType: "text/markdown", SecurityStatus: "passed", CapturedAt: now.Add(10 * time.Hour), HashSeed: "5"})

	projectionID := sourceVersionListID(42)
	insertSourceVersionProjection(t, ctx, tx, projectionID, workspaceID, secondID, now)
	insertSourceVersionAttempt(t, ctx, tx, sourceVersionListID(31), workspaceID, firstID, nil, nil, "validating", "pending", now.Add(time.Hour), false)
	insertSourceVersionAttempt(t, ctx, tx, sourceVersionListID(32), workspaceID, firstID, &runningRunID, nil, "parse_failed", "passed", now.Add(9*time.Hour), true)
	insertSourceVersionAttempt(t, ctx, tx, sourceVersionListID(33), workspaceID, secondID, nil, &projectionID, "chunked", "passed", now.Add(8*time.Hour), true)
	insertSourceVersionAttempt(t, ctx, tx, sourceVersionListID(34), workspaceID, thirdID, &waitingRunID, nil, "validating", "quarantined", now.Add(7*time.Hour), true)
	insertSourceVersionAttempt(t, ctx, tx, sourceVersionListID(35), otherWorkspaceID, otherID, &otherRunID, nil, "parse_failed", "passed", now.Add(11*time.Hour), true)

	indexID := sourceVersionListID(41)
	insertSourceVersionBuildingIndex(t, ctx, tx, indexID, workspaceID, now)
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at) VALUES($1,$2,$3,$4,$5,'included',$6)`, string(indexID), string(workspaceID), string(sourceOneID), string(secondID), string(projectionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,selection_status,exclusion_code,created_at) VALUES($1,$2,$3,'excluded','NO_CURRENT_SUCCESSFUL_PROJECTION',$4)`, string(indexID), string(workspaceID), string(sourceTwoID), now); err != nil {
		t.Fatal(err)
	}
	activateSourceVersionIndex(t, ctx, tx, indexID, workspaceID, now)
	seedSourceVersionListPlanCardinality(t, ctx, tx, otherWorkspaceID, otherSourceID, now.Add(48*time.Hour))
	assertSourceVersionListPlans(t, ctx, tx, workspaceID, secondID, now.Add(7*time.Hour))

	firstPage, hasMore, err := repository.ListSourceVersions(ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(firstPage) != 2 || firstPage[0].ID != firstID || firstPage[1].ID != secondID || firstPage[0].IngestionStatus != "parse_failed" || firstPage[0].WorkflowStatus != "running" || firstPage[0].IndexStatus != "" || firstPage[1].IndexStatus != "included" {
		t.Fatalf("first page=%#v hasMore=%v", firstPage, hasMore)
	}
	cursorTime := firstPage[1].CapturedAt
	secondPage, hasMore, err := repository.ListSourceVersions(ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, CursorTime: &cursorTime, CursorID: firstPage[1].ID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(secondPage) != 2 || secondPage[0].ID != thirdID || secondPage[1].ID != fourthID || secondPage[0].SecurityStatus != "quarantined" || secondPage[0].IndexStatus != "excluded" {
		t.Fatalf("second page=%#v hasMore=%v", secondPage, hasMore)
	}

	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, SecurityStatus: "quarantined", Limit: 10}, thirdID)
	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, IngestionStatus: "parse_failed", Limit: 10}, firstID)
	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, WorkflowStatus: "waiting_for_human", Limit: 10}, thirdID)
	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, IndexStatus: "included", Limit: 10}, secondID)
	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, IndexStatus: "excluded", Limit: 10}, thirdID)
	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, MimeType: "text/plain", Limit: 10}, fourthID)
	assertSourceVersionListIDs(t, repository, ctx, domain.SourceVersionListQuery{WorkspaceID: workspaceID, IngestionStatus: "parsed", Limit: 10})
}

type sourceVersionFixture struct {
	SourceID       foundation.ID
	VersionID      foundation.ID
	WorkspaceID    foundation.ID
	Path           string
	MimeType       string
	SecurityStatus string
	CapturedAt     time.Time
	HashSeed       string
}

func insertSourceVersionListWorkspace(t *testing.T, ctx context.Context, tx pgx.Tx, id foundation.ID, suffix string, now time.Time) {
	t.Helper()
	root := "/tmp/zhixu-m9-" + suffix
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(id), suffix, root, now); err != nil {
		t.Fatal(err)
	}
}

func insertSourceVersionListDefinition(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,'{}',$4)`, string(id), string(workspaceID), "source-list-"+string(id), now); err != nil {
		t.Fatal(err)
	}
}

func insertSourceVersionListRun(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID, definitionID foundation.ID, status string, now time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,$4,'{}',1,$5,$5)`, string(id), string(workspaceID), string(definitionID), status, now); err != nil {
		t.Fatal(err)
	}
}

func insertSourceVersionFixture(t *testing.T, ctx context.Context, tx pgx.Tx, fixture sourceVersionFixture) {
	t.Helper()
	artifactID := sourceVersionListID(100 + int(fixture.VersionID[len(fixture.VersionID)-1]-'0'))
	hash := strings.Repeat(fixture.HashSeed, 64)
	if _, err := tx.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'file',$3,$4,$5) ON CONFLICT(workspace_id,original_location) DO NOTHING`, string(fixture.SourceID), string(fixture.WorkspaceID), fixture.Path, fixture.Path, fixture.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,128,$4,$5)`, string(artifactID), string(fixture.WorkspaceID), hash, ".knowledge/sources/"+hash, fixture.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,128,$6,$7,$8,$9)`, string(fixture.VersionID), string(fixture.SourceID), string(fixture.WorkspaceID), string(artifactID), hash, fixture.MimeType, fixture.Path, fixture.SecurityStatus, fixture.CapturedAt); err != nil {
		t.Fatal(err)
	}
}

func insertSourceVersionAttempt(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID, versionID foundation.ID, runID, projectionID *foundation.ID, status, security string, startedAt time.Time, completed bool) {
	t.Helper()
	var completedAt *time.Time
	if completed {
		completedAt = &startedAt
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,workflow_run_id,parse_projection_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,'m9-parser','v1',$8,'chunks-v1','schema-v1',$9,1,$10,$11)`, string(id), string(workspaceID), string(versionID), runID, projectionID, status, security, strings.Repeat("a", 64), "attempt-"+string(id), startedAt, completedAt); err != nil {
		t.Fatal(err)
	}
}

func insertSourceVersionBuildingIndex(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version) VALUES($1,$2,'unicode','v1',$3,'{}','m9-source-list',$4,0,$5,'building','["vector"]',1,$6,$6,$7,2,'m9-parser','v1',$8,'chunks-v1','schema-v1')`, string(id), string(workspaceID), strings.Repeat("b", 64), strings.Repeat("c", 64), "index-"+string(id), now, strings.Repeat("f", 64), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}

func activateSourceVersionIndex(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID foundation.ID, now time.Time) {
	t.Helper()
	builtAt := now.Add(time.Minute)
	if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='ready',version=2,updated_at=$2,built_at=$2 WHERE id=$1`, string(id), builtAt); err != nil {
		t.Fatal(err)
	}
	activationID := sourceVersionListID(43)
	activatedAt := now.Add(2 * time.Minute)
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at) VALUES($1,'activate',$2,$3,3,$4,'m9-list-test',$5)`, string(activationID), string(workspaceID), string(id), "activate-"+string(id), activatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='active',version=3,updated_at=$2,activated_at=$2 WHERE id=$1`, string(id), activatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
}

func insertSourceVersionProjection(t *testing.T, ctx context.Context, tx pgx.Tx, id, workspaceID, versionID foundation.ID, now time.Time) {
	t.Helper()
	var artifactID string
	if err := tx.QueryRow(ctx, `SELECT content_artifact_id::text FROM core.source_version WHERE id=$1`, string(versionID)).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'m9-parser','v1',$4,'schema-v1',$5,'[]',$6)`, string(id), string(workspaceID), artifactID, strings.Repeat("a", 64), strings.Repeat("e", 64), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, string(versionID), string(id), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
}

func assertSourceVersionListIDs(t *testing.T, repository *Repository, ctx context.Context, query domain.SourceVersionListQuery, want ...foundation.ID) {
	t.Helper()
	items, hasMore, err := repository.ListSourceVersions(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(items) != len(want) {
		t.Fatalf("items=%#v hasMore=%v want=%#v", items, hasMore, want)
	}
	for index := range want {
		if items[index].ID != want[index] || items[index].WorkspaceID != query.WorkspaceID {
			t.Fatalf("items[%d]=%#v want=%s", index, items[index], want[index])
		}
	}
}

func seedSourceVersionListPlanCardinality(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, sourceID foundation.ID, newest time.Time) {
	t.Helper()
	const cardinality = 4096
	if _, err := tx.Exec(ctx, `WITH fixture AS (
		SELECT value,
			('9b000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid AS version_id,
			('9c000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid AS artifact_id,
			md5('source-version-plan-' || value::text) || md5('source-version-plan-tail-' || value::text) AS content_hash,
			$2::timestamptz - value * interval '1 second' AS captured_at
		FROM generate_series(1,$3) AS value
	)
	INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
	SELECT artifact_id,$1,content_hash,128,'.knowledge/sources/' || content_hash,captured_at
	FROM fixture`, string(workspaceID), newest.UTC(), cardinality); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH fixture AS (
		SELECT value,
			('9b000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid AS version_id,
			('9c000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid AS artifact_id,
			md5('source-version-plan-' || value::text) || md5('source-version-plan-tail-' || value::text) AS content_hash,
			$3::timestamptz - value * interval '1 second' AS captured_at
		FROM generate_series(1,$4) AS value
	)
		INSERT INTO core.source_version(
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		)
		SELECT version_id,$1,$2,artifact_id,content_hash,128,'text/markdown',
			'other/plan-' || value::text || '.md','passed',captured_at
		FROM fixture`, string(sourceID), string(workspaceID), newest.UTC(), cardinality); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH fixture AS (
		SELECT value,
			('9b000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid AS version_id,
			('9d000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid AS attempt_id,
			$2::timestamptz - value * interval '1 second' AS started_at
		FROM generate_series(1,$3) AS value
	)
	INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,status,security_status,parser_id,parser_version,
		parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at
	)
	SELECT attempt_id,$1,version_id,'validating','pending','m9-parser','v1',
		repeat('a',64),'chunks-v1','schema-v1','source-list-plan-' || value::text,1,started_at
	FROM fixture`, string(workspaceID), newest.UTC(), cardinality); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE core.source_version, core.source, ingestion.attempt, retrieval.index_version, retrieval.index_manifest_source, workflow.run`); err != nil {
		t.Fatal(err)
	}
}

func assertSourceVersionListPlans(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, cursorID foundation.ID, cursorTime time.Time) {
	t.Helper()
	performanceIndexesAvailable := sourceVersionListIndexesAvailable(t, ctx, tx)
	if !performanceIndexesAvailable {
		t.Log("source version list indexes are absent; validating the migration-29 compatibility plan separately from the M9 performance gate")
	}
	assertSourceVersionListPlan(t, ctx, tx, "first page", domain.SourceVersionListQuery{
		WorkspaceID: workspaceID,
		Limit:       2,
	}, performanceIndexesAvailable)
	assertSourceVersionListPlan(t, ctx, tx, "cursor page", domain.SourceVersionListQuery{
		WorkspaceID: workspaceID,
		CursorTime:  &cursorTime,
		CursorID:    cursorID,
		Limit:       2,
	}, performanceIndexesAvailable)
}

func sourceVersionListIndexesAvailable(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	for _, index := range []string{
		"core.idx_source_version_workspace_captured_id",
		"ingestion.idx_ingestion_attempt_source_started_id",
	} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			return false
		}
	}
	return true
}

func assertSourceVersionListPlan(t *testing.T, ctx context.Context, tx pgx.Tx, label string, request domain.SourceVersionListQuery, performanceIndexesAvailable bool) {
	t.Helper()
	query, args := buildSourceVersionListQuery(request)
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan sourceVersionListExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid source version list explain json: %v %s", err, raw)
	}
	if documents[0].Plan.NodeType != "Limit" {
		t.Fatalf("source version list %s plan is not bounded by Limit: %s", label, raw)
	}
	if performanceIndexesAvailable {
		assertSourceVersionListAccessPath(t, label, raw, documents[0].Plan, "Source Version root", "source_version", "sv", "idx_source_version_workspace_captured_id")
		assertSourceVersionListAccessPath(t, label, raw, documents[0].Plan, "latest Ingestion Attempt", "attempt", "ia", "idx_ingestion_attempt_source_started_id")
	} else if !sourceVersionListPlanHasRelation(documents[0].Plan, "source_version", "sv") {
		t.Fatalf("source version list %s compatibility plan has no source_version relation: %s", label, raw)
	}
	t.Logf("source version list %s plan: %s", label, raw)
}

func sourceVersionListPlanHasRelation(plan sourceVersionListExplainPlan, relation, alias string) bool {
	if plan.RelationName == relation && plan.Alias == alias {
		return true
	}
	for _, child := range plan.Plans {
		if sourceVersionListPlanHasRelation(child, relation, alias) {
			return true
		}
	}
	return false
}

func assertSourceVersionListAccessPath(t *testing.T, label string, raw []byte, plan sourceVersionListExplainPlan, purpose, relation, alias, indexName string) {
	t.Helper()
	paths := sourceVersionListAccessPaths(plan, relation, alias, nil)
	if len(paths) == 0 {
		t.Fatalf("source version list %s plan has no %s access path: %s", label, purpose, raw)
	}
	for _, path := range paths {
		access := path[len(path)-1]
		if access.NodeType != "Index Scan" && access.NodeType != "Index Only Scan" {
			t.Fatalf("source version list %s %s is not a direct index scan: %s", label, purpose, raw)
		}
		if access.IndexName != indexName {
			t.Fatalf("source version list %s %s uses index %q, want %q: %s", label, purpose, access.IndexName, indexName, raw)
		}
		for _, node := range path {
			if strings.Contains(node.NodeType, "Sort") {
				t.Fatalf("source version list %s %s path contains %s: %s", label, purpose, node.NodeType, raw)
			}
		}
	}
}

type sourceVersionListExplainPlan struct {
	NodeType     string                         `json:"Node Type"`
	RelationName string                         `json:"Relation Name"`
	Alias        string                         `json:"Alias"`
	IndexName    string                         `json:"Index Name"`
	Plans        []sourceVersionListExplainPlan `json:"Plans"`
}

func sourceVersionListAccessPaths(plan sourceVersionListExplainPlan, relation, alias string, path []sourceVersionListExplainPlan) [][]sourceVersionListExplainPlan {
	path = append(path, plan)
	paths := make([][]sourceVersionListExplainPlan, 0, 1)
	if plan.RelationName == relation && plan.Alias == alias {
		found := make([]sourceVersionListExplainPlan, len(path))
		copy(found, path)
		paths = append(paths, found)
	}
	for _, child := range plan.Plans {
		paths = append(paths, sourceVersionListAccessPaths(child, relation, alias, path)...)
	}
	return paths
}

func sourceVersionListID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("93000000-0000-4000-8000-%012d", n))
}
