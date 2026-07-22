//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const collectionReferenceTopicCount = 512

func TestCollectionQueryExecutesEveryRegisteredFieldOperator(t *testing.T) {
	ctx, pool, fixture := collectionQueryFixture(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	aliasID := collectionQueryID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic_alias(id,workspace_id,topic_id,alias,normalized_alias,created_at) VALUES($1,$2,$3,$4,$5,$6)`,
		string(aliasID), string(fixture.WorkspaceID), string(fixture.PrimaryTopicID), `Literal%_\Alias`, `literal%_\alias`, collectionFixtureTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.source SET original_location='docs/literal%_path.md' WHERE workspace_id=$1`, string(fixture.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	insertCollectionHealthIssue(t, ctx, tx, fixture.WorkspaceID, fixture.FirstClaimID)

	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	service := newCollectionQueryService(t, repository)
	all := []foundation.ID{fixture.PrimaryTopicID, fixture.SecondaryTopicID, fixture.FirstClaimID, fixture.SecondClaimID}
	topics := []foundation.ID{fixture.PrimaryTopicID, fixture.SecondaryTopicID}
	claims := []foundation.ID{fixture.FirstClaimID, fixture.SecondClaimID}
	base := collectionFixtureTime()

	tests := []struct {
		name   string
		clause domain.Clause
		want   []foundation.ID
	}{
		{"object_type/EQ", collectionSingle("object_type", domain.OperatorEQ, "TOPIC"), topics},
		{"object_type/IN", collectionIn("object_type", "TOPIC", "CLAIM"), all},
		{"topic_id/EQ", collectionSingle("topic_id", domain.OperatorEQ, string(fixture.SecondaryTopicID)), []foundation.ID{fixture.SecondaryTopicID, fixture.SecondClaimID}},
		{"topic_id/IN", collectionIn("topic_id", string(fixture.PrimaryTopicID), string(fixture.SecondaryTopicID)), all},
		{"status/EQ", collectionSingle("status", domain.OperatorEQ, "CONFIRMED"), claims},
		{"status/IN", collectionIn("status", "ACTIVE", "CONFIRMED"), all},
		{"created_at/GTE", collectionSingle("created_at", domain.OperatorGTE, base.Add(2*time.Second).Format(time.RFC3339Nano)), claims},
		{"created_at/LTE", collectionSingle("created_at", domain.OperatorLTE, base.Add(time.Second).Format(time.RFC3339Nano)), topics},
		{"created_at/BETWEEN", collectionBetween("created_at", base.Add(time.Second).Format(time.RFC3339Nano), base.Add(2*time.Second).Format(time.RFC3339Nano)), []foundation.ID{fixture.SecondaryTopicID, fixture.FirstClaimID}},
		{"updated_at/GTE", collectionSingle("updated_at", domain.OperatorGTE, base.Add(2*time.Second).Format(time.RFC3339Nano)), claims},
		{"updated_at/LTE", collectionSingle("updated_at", domain.OperatorLTE, base.Add(time.Second).Format(time.RFC3339Nano)), topics},
		{"updated_at/BETWEEN", collectionBetween("updated_at", base.Add(time.Second).Format(time.RFC3339Nano), base.Add(2*time.Second+time.Millisecond).Format(time.RFC3339Nano)), []foundation.ID{fixture.SecondaryTopicID, fixture.FirstClaimID}},
		{"confidence/GTE", collectionSingle("confidence", domain.OperatorGTE, 0.9), []foundation.ID{fixture.FirstClaimID}},
		{"confidence/LTE", collectionSingle("confidence", domain.OperatorLTE, 0.9), []foundation.ID{fixture.SecondClaimID}},
		{"confidence/BETWEEN", collectionBetween("confidence", 0.86, 0.94), claims},
		{"confidence/IS_NULL", collectionIsNull("confidence"), topics},
		{"relation_type/EQ", collectionSingle("relation_type", domain.OperatorEQ, "SUPPORTS"), claims},
		{"relation_type/IN", collectionIn("relation_type", "SUPPORTS", "BELONGS_TO"), all},
		{"health_issue_type/EQ", collectionSingle("health_issue_type", domain.OperatorEQ, "ORPHAN"), []foundation.ID{fixture.FirstClaimID}},
		{"health_issue_type/IN", collectionIn("health_issue_type", "ORPHAN", "STALE"), []foundation.ID{fixture.FirstClaimID}},
		{"source_type/EQ", collectionSingle("source_type", domain.OperatorEQ, "text"), claims},
		{"source_type/IN", collectionIn("source_type", "text", "pdf"), claims},
		{"source_type/PREFIX", collectionSingle("source_type", domain.OperatorPrefix, "te"), claims},
		{"file_path/EQ", collectionSingle("file_path", domain.OperatorEQ, "docs/literal%_path.md"), claims},
		{"file_path/IN", collectionIn("file_path", "docs/literal%_path.md", "docs/other.md"), claims},
		{"file_path/PREFIX", collectionSingle("file_path", domain.OperatorPrefix, "docs/literal%_"), claims},
		{"text/CONTAINS", collectionSingle("text", domain.OperatorContains, "graph"), []foundation.ID{fixture.SecondaryTopicID, fixture.SecondClaimID}},
		{"text/PREFIX", collectionSingle("text", domain.OperatorPrefix, "channels"), []foundation.ID{fixture.FirstClaimID}},
		{"text/alias-literal-CONTAINS", collectionSingle("text", domain.OperatorContains, `%_\`), []foundation.ID{fixture.PrimaryTopicID}},
		{"text/alias-literal-PREFIX", collectionSingle("text", domain.OperatorPrefix, `literal%_\`), []foundation.ID{fixture.PrimaryTopicID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := service.Preview(ctx, collectionapp.PreviewQuery{
				WorkspaceID: fixture.WorkspaceID,
				Query:       collectionQuery(test.clause),
				Limit:       100,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertCollectionIDs(t, page, test.want)
		})
	}
}

func TestCollectionNullableConfidenceKeysetTraversesNullTail(t *testing.T) {
	ctx, pool, fixture := collectionQueryFixture(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	service := newCollectionQueryService(t, repository)

	for _, direction := range []string{"ASC", "DESC"} {
		t.Run(direction, func(t *testing.T) {
			query := collectionQuery(collectionIn("object_type", "TOPIC", "CLAIM"))
			query.Sort = []domain.SortTerm{{Field: "confidence", Direction: direction}}
			seen := make(map[foundation.ID]struct{}, 4)
			cursor := ""
			nullTail := false
			for pageNo := 0; pageNo < 6; pageNo++ {
				page, err := service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 1, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				if page.ExactCount != 4 || len(page.Items) != 1 {
					t.Fatalf("page %d = %+v", pageNo+1, page)
				}
				item := page.Items[0]
				if _, duplicate := seen[item.ID]; duplicate {
					t.Fatalf("duplicate item %s", item.ID)
				}
				seen[item.ID] = struct{}{}
				if item.Confidence == nil {
					nullTail = true
				} else if nullTail {
					t.Fatalf("non-null confidence appeared after NULLS LAST tail: %+v", item)
				}
				cursor = page.NextCursor
				if cursor == "" {
					break
				}
			}
			if len(seen) != 4 || !nullTail {
				t.Fatalf("seen=%v null_tail=%v", seen, nullTail)
			}
		})
	}
}

func TestCollectionPreviewCursorBindingAndRevisionMutations(t *testing.T) {
	ctx, pool, fixture := collectionQueryFixture(t)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newCollectionQueryService(t, repository)
	query := collectionQuery(collectionSingle("object_type", domain.OperatorEQ, "CLAIM"))
	query.Sort = []domain.SortTerm{{Field: "text", Direction: "ASC"}}
	freshCursor := func() string {
		t.Helper()
		page, queryErr := service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 1})
		if queryErr != nil || page.NextCursor == "" {
			t.Fatalf("first page=%+v err=%v", page, queryErr)
		}
		return page.NextCursor
	}
	assertPreviewError := func(request collectionapp.PreviewQuery, code string) {
		t.Helper()
		page, previewErr := service.Preview(ctx, request)
		assertCollectionErrorCode(t, page, previewErr, code)
	}

	cursor := freshCursor()
	otherQuery := collectionQuery(collectionSingle("object_type", domain.OperatorEQ, "TOPIC"))
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: otherQuery, Limit: 1, Cursor: cursor}, "COLLECTION_CURSOR_INVALID")

	otherWorkspaceID := collectionQueryID(t)
	otherRoot := "/tmp/collection-cursor-" + string(otherWorkspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'collection-cursor-binding',$2,$2,$3,'test',1,$3,$3)`, string(otherWorkspaceID), otherRoot, collectionFixtureTime()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM core.workspace WHERE id=$1`, string(otherWorkspaceID)); cleanupErr != nil {
			t.Errorf("cleanup cursor workspace: %v", cleanupErr)
		}
	})
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: otherWorkspaceID, Query: query, Limit: 1, Cursor: cursor}, "COLLECTION_CURSOR_INVALID")

	aliasID := collectionQueryID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic_alias(id,workspace_id,topic_id,alias,normalized_alias,created_at) VALUES($1,$2,$3,'Cursor Alias','cursor alias',$4)`, string(aliasID), string(fixture.WorkspaceID), string(fixture.PrimaryTopicID), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 1, Cursor: cursor}, "COLLECTION_CURSOR_STALE")

	cursor = freshCursor()
	if _, err := pool.Exec(ctx, `UPDATE core.source SET original_location='cursor-mutated-source.txt' WHERE workspace_id=$1`, string(fixture.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 1, Cursor: cursor}, "COLLECTION_CURSOR_STALE")

	cursor = freshCursor()
	insertCollectionSourceVersion(t, ctx, pool, fixture.WorkspaceID)
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 1, Cursor: cursor}, "COLLECTION_CURSOR_STALE")

	// Results hydrate Health 摘要，即使过滤和排序没有引用 health_issue_type，
	// 期间的 Health 事实变化也必须让后续页失效，避免跨页返回混合投影。
	cursor = freshCursor()
	insertCollectionHealthIssueVariant(t, ctx, pool, fixture.WorkspaceID, fixture.FirstClaimID, "health.detector.collection-query-plain", "c", "d")
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 1, Cursor: cursor}, "COLLECTION_CURSOR_STALE")

	healthSortQuery := collectionQuery(collectionSingle("object_type", domain.OperatorEQ, "CLAIM"))
	healthSortQuery.Sort = []domain.SortTerm{{Field: "health_issue_type", Direction: "ASC"}}
	healthPage, err := service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: healthSortQuery, Limit: 1})
	if err != nil || healthPage.NextCursor == "" {
		t.Fatalf("health-sort first page=%+v err=%v", healthPage, err)
	}
	insertCollectionHealthIssue(t, ctx, pool, fixture.WorkspaceID, fixture.FirstClaimID)
	assertPreviewError(collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: healthSortQuery, Limit: 1, Cursor: healthPage.NextCursor}, "COLLECTION_CURSOR_STALE")
}

func TestCollectionQuerySnapshotCountAndReferenceP95(t *testing.T) {
	databaseURL := collectionDatabaseURL(t)
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse collection test database configuration")
	}
	tracer := &collectionQueryTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	registerCollectionQueryCleanup(t, pool, fixture.WorkspaceID)
	seedCollectionReferenceTopics(t, ctx, pool, fixture.WorkspaceID)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newCollectionQueryService(t, repository)
	query := collectionQuery(collectionIn("object_type", "TOPIC", "CLAIM"))
	query.Sort = []domain.SortTerm{{Field: "updated_at", Direction: "DESC"}}

	tracer.reset()
	preview, err := service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionReferencePage(t, preview)
	assertCollectionTrace(t, tracer.snapshot(), false)

	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: fixture.WorkspaceID, Name: "query count fixture", Query: query,
		ViewType: domain.ViewTypeList, IdempotencyKey: "collection-query-count-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	tracer.reset()
	saved, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: created.Collection.ID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionReferencePage(t, saved)
	assertCollectionTrace(t, tracer.snapshot(), true)

	for warmup := 0; warmup < 3; warmup++ {
		tracer.reset()
		if _, err := service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 25}); err != nil {
			t.Fatalf("warmup %d: %v", warmup+1, err)
		}
		assertCollectionTrace(t, tracer.snapshot(), false)
	}
	durations := make([]time.Duration, 0, 25)
	for sample := 0; sample < 25; sample++ {
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		tracer.reset()
		started := time.Now()
		page, queryErr := service.Preview(queryCtx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 25})
		duration := time.Since(started)
		cancel()
		if queryErr != nil || page.ExactCount != collectionReferenceTopicCount+4 || len(page.Items) != 25 || page.NextCursor == "" {
			t.Fatalf("sample %d page=%+v err=%v", sample+1, page, queryErr)
		}
		assertCollectionTrace(t, tracer.snapshot(), false)
		durations = append(durations, duration)
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	p95 := durations[(95*len(durations)+99)/100-1]
	if p95 > 2*time.Second {
		t.Fatalf("collection reference fixture p95=%s exceeds 2s", p95)
	}
	t.Logf("collection reference fixture samples=%d p95=%s", len(durations), p95)
}

func TestCollectionQueryPlanUsesCanonicalIndexes(t *testing.T) {
	ctx, pool, fixture := collectionQueryFixture(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	insertCollectionHealthIssue(t, ctx, tx, fixture.WorkspaceID, fixture.FirstClaimID)
	seedCollectionPlanCardinality(t, ctx, tx, fixture)
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{
		"core.idx_knowledge_relation_workspace_source_status_type",
		"core.idx_knowledge_relation_workspace_target_status_type",
		"ops.idx_ops_health_issue_workspace_target_status_type",
	} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("required migrated index %s exists=%v err=%v", index, exists, err)
		}
	}

	found := make(map[string]bool)
	queries := []domain.Query{
		collectionQuery(collectionIn("status", "ACTIVE", "CONFIRMED")),
		collectionQuery(collectionSingle("relation_type", domain.OperatorEQ, "SUPPORTS")),
		collectionQuery(collectionSingle("health_issue_type", domain.OperatorEQ, "ORPHAN")),
	}
	for _, query := range queries {
		for index := range explainCollectionPage(t, ctx, tx, fixture.WorkspaceID, query) {
			found[index] = true
		}
	}
	assertCollectionPlanUsesAny(t, found, "Topic root", "idx_knowledge_topic_workspace_status", "uq_knowledge_topic_id_workspace", "uq_knowledge_topic_workspace_name")
	assertCollectionPlanUsesAny(t, found, "Claim root", "idx_knowledge_claim_workspace_status", "uq_knowledge_claim_id_workspace", "uq_knowledge_claim_workspace_fingerprint")
	assertCollectionPlanUsesAny(t, found, "Relation source", "idx_knowledge_relation_workspace_source_status_type", "idx_knowledge_relation_source")
	assertCollectionPlanUsesAny(t, found, "Relation target", "idx_knowledge_relation_workspace_target_status_type", "idx_knowledge_relation_target")
	assertCollectionPlanUsesAny(t, found, "Health target", "idx_ops_health_issue_workspace_target_status_type")
}

type collectionQueryTracer struct {
	mu  sync.Mutex
	sql []string
}

func (tracer *collectionQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer.mu.Lock()
	tracer.sql = append(tracer.sql, strings.TrimSpace(data.SQL))
	tracer.mu.Unlock()
	return ctx
}

func (*collectionQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tracer *collectionQueryTracer) reset() {
	tracer.mu.Lock()
	tracer.sql = nil
	tracer.mu.Unlock()
}

func (tracer *collectionQueryTracer) snapshot() []string {
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	return append([]string(nil), tracer.sql...)
}

func collectionQueryFixture(t *testing.T) (context.Context, *pgxpool.Pool, graphfixture.Fixture) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, collectionDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	registerCollectionQueryCleanup(t, pool, fixture.WorkspaceID)
	return ctx, pool, fixture
}

func collectionDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	return databaseURL
}

func registerCollectionQueryCleanup(t *testing.T, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Errorf("begin collection query cleanup: %v", err)
			return
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(context.Background())
			}
		}()
		if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM learning.smart_collection_command WHERE workspace_id=$1`, string(workspaceID))
		}
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM learning.smart_collection WHERE workspace_id=$1`, string(workspaceID))
		}
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM ops.health_issue WHERE workspace_id=$1`, string(workspaceID))
		}
		if err == nil {
			err = tx.Commit(ctx)
			committed = err == nil
		}
		if err != nil {
			t.Errorf("cleanup collection query facts: %v", err)
			return
		}
		if err := graphfixture.Cleanup(ctx, pool, workspaceID); err != nil {
			t.Errorf("cleanup collection query fixture: %v", err)
		}
	})
}

func newCollectionQueryService(t *testing.T, repository *Repository) *collectionapp.Service {
	t.Helper()
	service, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: repository,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.FixedClock{Value: collectionFixtureTime()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func collectionFixtureTime() time.Time {
	return time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
}

func collectionQueryID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertCollectionHealthIssue(t *testing.T, ctx context.Context, db DB, workspaceID, claimID foundation.ID) {
	t.Helper()
	insertCollectionHealthIssueVariant(t, ctx, db, workspaceID, claimID, "health.detector.collection-query", "a", "b")
}

func insertCollectionHealthIssueVariant(t *testing.T, ctx context.Context, db DB, workspaceID, claimID foundation.ID, detectorID, identityChar, fingerprintChar string) {
	t.Helper()
	now := collectionFixtureTime().Add(time.Minute)
	if _, err := db.Exec(ctx, `INSERT INTO ops.health_issue(id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at) VALUES($1,$2,'ORPHAN','CLAIM',$3,$4,$5,'health-issue-fingerprint/v1',$6,'detector/v1','HIGH','fixture','OPEN',1,$7,$7,$7,$7,$7)`,
		string(collectionQueryID(t)), string(workspaceID), string(claimID), detectorID, strings.Repeat(identityChar, 64), strings.Repeat(fingerprintChar, 64), now); err != nil {
		t.Fatal(err)
	}
}

func insertCollectionSourceVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	artifactID, versionID := collectionQueryID(t), collectionQueryID(t)
	hash := strings.Repeat("c", 64)
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,1,$4,$5)`, string(artifactID), string(workspaceID), hash, ".knowledge/sources/"+hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) SELECT $1,s.id,$5,$2,$3,1,'text/plain','cursor-version-2.txt','pending',$4 FROM core.source s WHERE s.workspace_id=$5 ORDER BY s.id LIMIT 1`, string(versionID), string(artifactID), hash, now, string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedCollectionPlanCardinality(t *testing.T, ctx context.Context, tx pgx.Tx, fixture graphfixture.Fixture) {
	t.Helper()
	const topicCount = 384
	now := collectionFixtureTime().Add(2 * time.Hour)
	topics := make([]foundation.ID, topicCount)
	topicBatch := &pgx.Batch{}
	for index := range topics {
		topics[index] = collectionQueryID(t)
		name := fmt.Sprintf("Collection Plan Topic %03d", index)
		topicBatch.Queue(`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'plan fixture','ACTIVE',1,$5,$5)`, string(topics[index]), string(fixture.WorkspaceID), name, strings.ToLower(name), now)
	}
	if err := tx.SendBatch(ctx, topicBatch).Close(); err != nil {
		t.Fatal(err)
	}
	relationBatch := &pgx.Batch{}
	targets := []foundation.ID{fixture.PrimaryTopicID, fixture.SecondaryTopicID}
	relationNo := 0
	for _, sourceID := range topics {
		for _, targetID := range targets {
			relationNo++
			relationID := collectionQueryID(t)
			fingerprint := fmt.Sprintf("%064x", relationNo+1000)
			relationBatch.Queue(`INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,version,created_at,updated_at) VALUES($1,$2,'TOPIC',$3,'TOPIC',$4,'IMPACTS','SUGGESTED',0.9,$5,1,$6,$6)`,
				string(relationID), string(fixture.WorkspaceID), string(sourceID), string(targetID), fingerprint, now)
		}
	}
	if err := tx.SendBatch(ctx, relationBatch).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE core.topic; ANALYZE core.claim; ANALYZE core.relation; ANALYZE ops.health_issue`); err != nil {
		t.Fatal(err)
	}
}

func seedCollectionReferenceTopics(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	now := collectionFixtureTime().Add(3 * time.Hour)
	batch := &pgx.Batch{}
	for index := 0; index < collectionReferenceTopicCount; index++ {
		name := fmt.Sprintf("Collection Reference Topic %03d", index)
		at := now.Add(time.Duration(index) * time.Microsecond)
		batch.Queue(`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'reference fixture','ACTIVE',1,$5,$5)`, string(collectionQueryID(t)), string(workspaceID), name, strings.ToLower(name), at)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func collectionQuery(clause domain.Clause) domain.Query {
	return domain.Query{
		SchemaVersion: domain.QuerySchemaVersionV1,
		Root:          domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{clause}},
	}
}

func collectionSingle(field string, operator domain.Operator, value any) domain.Clause {
	encoded, _ := json.Marshal(value)
	return domain.Clause{Kind: domain.ClauseKindPredicate, Field: field, Operator: string(operator), Value: encoded}
}

func collectionIn(field string, values ...string) domain.Clause {
	encoded := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		item, _ := json.Marshal(value)
		encoded = append(encoded, item)
	}
	return domain.Clause{Kind: domain.ClauseKindPredicate, Field: field, Operator: string(domain.OperatorIN), Values: encoded}
}

func collectionBetween(field string, lower, upper any) domain.Clause {
	lowerJSON, _ := json.Marshal(lower)
	upperJSON, _ := json.Marshal(upper)
	return domain.Clause{Kind: domain.ClauseKindPredicate, Field: field, Operator: string(domain.OperatorBetween), Lower: lowerJSON, Upper: upperJSON}
}

func collectionIsNull(field string) domain.Clause {
	return domain.Clause{Kind: domain.ClauseKindPredicate, Field: field, Operator: string(domain.OperatorIsNull)}
}

func assertCollectionIDs(t *testing.T, page collectionapp.ResultPage, want []foundation.ID) {
	t.Helper()
	got := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		got = append(got, string(item.ID))
	}
	expected := make([]string, 0, len(want))
	for _, id := range want {
		expected = append(expected, string(id))
	}
	sort.Strings(got)
	sort.Strings(expected)
	if page.ExactCount != int64(len(expected)) || strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Fatalf("page count=%d ids=%v want=%v", page.ExactCount, got, expected)
	}
}

func assertCollectionReferencePage(t *testing.T, page collectionapp.ResultPage) {
	t.Helper()
	if page.ExactCount != collectionReferenceTopicCount+4 || len(page.Items) != 25 || page.NextCursor == "" {
		t.Fatalf("reference page count=%d items=%d next=%t", page.ExactCount, len(page.Items), page.NextCursor != "")
	}
}

func assertCollectionErrorCode(t *testing.T, _ collectionapp.ResultPage, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("error=%v want code %s", err, code)
	}
}

func assertCollectionTrace(t *testing.T, statements []string, saved bool) {
	t.Helper()
	// Every page performs one bounded hydration query, even when the page is
	// empty; this keeps query count independent of result cardinality.
	wantStatements := 7
	if saved {
		wantStatements = 8
	}
	if len(statements) != wantStatements {
		t.Fatalf("statements=%d want=%d sql=%q", len(statements), wantStatements, statements)
	}
	counts := map[string]int{}
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		switch {
		case strings.HasPrefix(normalized, "begin"):
			counts["begin"]++
			if !strings.Contains(normalized, "isolation level repeatable read") || !strings.Contains(normalized, "read only") {
				t.Fatalf("read snapshot options missing: %q", statement)
			}
		case strings.Contains(normalized, "pg_catalog.set_config('statement_timeout'"):
			counts["timeout"]++
		case strings.Contains(normalized, "select count(*) from (with item as"):
			counts["count"]++
		case strings.HasPrefix(normalized, "with item as") && strings.Contains(normalized, " order by "):
			counts["page"]++
		case strings.HasPrefix(normalized, "with requested as") && strings.Contains(normalized, "unnest($2::text[],$3::uuid[])"):
			counts["hydration"]++
		case strings.Contains(normalized, "from learning.smart_collection"):
			counts["collection"]++
		case strings.Contains(normalized, "from core.workspace workspace left join core.workspace_read_model_revision revision"):
			if strings.Contains(normalized, "count(") || strings.Contains(normalized, "string_agg(") {
				t.Fatalf("read-model revision must remain O(1): %q", statement)
			}
			counts["revision"]++
		case normalized == "commit":
			counts["commit"]++
		default:
			t.Fatalf("unexpected collection query statement: %q", statement)
		}
	}
	wantCollection := 0
	if saved {
		wantCollection = 1
	}
	for key, want := range map[string]int{"begin": 1, "timeout": 1, "revision": 1, "count": 1, "page": 1, "hydration": 1, "collection": wantCollection, "commit": 1} {
		if counts[key] != want {
			t.Fatalf("statement kind %s=%d want=%d all=%q", key, counts[key], want, statements)
		}
	}
}

func explainCollectionPage(t *testing.T, ctx context.Context, db DB, workspaceID foundation.ID, query domain.Query) map[string]bool {
	t.Helper()
	plan, err := collectionapp.CompileQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	where, args := shiftWhere(plan.Where, append([]any{string(workspaceID)}, plan.Args...))
	order := make([]string, 0, len(plan.Sort))
	for _, term := range plan.Sort {
		order = append(order, term.Column+" "+term.Direction+" NULLS LAST")
	}
	pageSQL := unifiedItemCTE + "SELECT * FROM item WHERE " + where + " ORDER BY " + strings.Join(order, ", ") + fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, 26)
	var raw []byte
	if err := db.QueryRow(ctx, "EXPLAIN (FORMAT JSON, COSTS OFF) "+pageSQL, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid collection explain: %v %s", err, raw)
	}
	found := make(map[string]bool)
	visitCollectionPlan(documents[0]["Plan"], func(node map[string]any) {
		if index, ok := node["Index Name"].(string); ok {
			found[index] = true
		}
		if node["Node Type"] == "Seq Scan" {
			switch node["Relation Name"] {
			case "topic", "claim", "relation", "health_issue":
				t.Fatalf("production collection page contains %s Seq Scan: %s", node["Relation Name"], raw)
			}
		}
	})
	return found
}

func visitCollectionPlan(value any, visit func(map[string]any)) {
	node, ok := value.(map[string]any)
	if !ok {
		return
	}
	visit(node)
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		visitCollectionPlan(child, visit)
	}
}

func assertCollectionPlanUsesAny(t *testing.T, found map[string]bool, purpose string, indexes ...string) {
	t.Helper()
	for _, index := range indexes {
		if found[index] {
			return
		}
	}
	t.Fatalf("production page plans miss %s indexes %v; found=%v", purpose, indexes, found)
}
