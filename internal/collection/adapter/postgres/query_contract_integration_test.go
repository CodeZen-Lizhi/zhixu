//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const collectionReferenceTopicCount = 512

func TestCollectionQueryExecutesEveryRegisteredFieldOperator(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, testCollectionQueryExecutesEveryRegisteredFieldOperator)
}

func testCollectionQueryExecutesEveryRegisteredFieldOperator(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	fixture := seedCollectionQueryFixture(t, ctx, pool)

	aliasID := collectionQueryID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic_alias(id,workspace_id,topic_id,alias,normalized_alias,created_at) VALUES($1,$2,$3,$4,$5,$6)`,
		string(aliasID), string(fixture.WorkspaceID), string(fixture.PrimaryTopicID), `Literal%_\Alias`, `literal%_\alias`, collectionFixtureTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.source SET original_location='docs/literal%_path.md' WHERE workspace_id=$1`, string(fixture.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	insertCollectionHealthIssue(t, ctx, pool, fixture.WorkspaceID, fixture.FirstClaimID)

	service := newCollectionQueryService(t, testCase.repository)
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
	runCollectionRepositoryIntegrationCases(t, testCollectionNullableConfidenceKeysetTraversesNullTail)
}

func testCollectionNullableConfidenceKeysetTraversesNullTail(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	fixture := seedCollectionQueryFixture(t, ctx, testCase.pool)
	service := newCollectionQueryService(t, testCase.repository)

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
	runCollectionRepositoryIntegrationCases(t, testCollectionPreviewCursorBindingAndRevisionMutations)
}

func testCollectionPreviewCursorBindingAndRevisionMutations(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	fixture := seedCollectionQueryFixture(t, ctx, pool)
	service := newCollectionQueryService(t, testCase.repository)
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
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'collection-cursor-binding',$2,$2,$3,'inactive',1,$3,$3)`, string(otherWorkspaceID), otherRoot, collectionFixtureTime()); err != nil {
		t.Fatal(err)
	}
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
	runCollectionRepositoryIntegrationCases(t, func(t *testing.T, testCase collectionIntegrationCase) {
		ctx := testCase.context
		fixture := seedCollectionQueryFixture(t, ctx, testCase.pool)
		seedCollectionReferenceTopics(t, ctx, testCase.pool, fixture.WorkspaceID)
		repository := testCase.repository.(*GORMRepository)
		tracer := &collectionQueryTracer{Interface: logger.Discard}
		repository.database.Config.Logger = tracer
		repository.unitOfWork = &collectionTracingUnitOfWork{delegate: repository.unitOfWork, tracer: tracer}
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
	})
}

func TestCollectionQueryPlanUsesCanonicalIndexes(t *testing.T) {
	ctx, platform, fixture := collectionQueryFixture(t)
	seedTx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	insertCollectionHealthIssue(t, ctx, seedTx, fixture.WorkspaceID, fixture.FirstClaimID)
	seedCollectionPlanCardinality(t, ctx, seedTx, fixture)
	seedCollectionPlanCollections(t, ctx, seedTx, fixture.WorkspaceID)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		ctx := callbackCtx
		if err := tx.WithContext(ctx).Exec(`ANALYZE learning.smart_collection`).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`SET LOCAL enable_seqscan=off`).Error; err != nil {
			return err
		}

		for _, index := range []string{
			"core.idx_knowledge_relation_workspace_source_status_type",
			"core.idx_knowledge_relation_workspace_target_status_type",
			"ops.idx_ops_health_issue_workspace_target_status_type",
			"learning.idx_learning_smart_collection_workspace_status_updated",
		} {
			var exists bool
			if err := tx.WithContext(ctx).Raw(`SELECT to_regclass(?) IS NOT NULL`, index).Row().Scan(&exists); err != nil || !exists {
				t.Fatalf("required migrated index %s exists=%v err=%v", index, exists, err)
			}
		}

		boundedRelationIndexes := []string{
			"idx_knowledge_relation_workspace_source_status_type",
			"idx_knowledge_relation_workspace_target_status_type",
			"idx_knowledge_relation_source",
			"idx_knowledge_relation_target",
			"idx_knowledge_relation_workspace_status",
			"idx_knowledge_relation_semantic_scan_topic_claim",
		}
		relationPredicateIndexes := []string{
			"idx_knowledge_relation_workspace_source_status_type",
			"idx_knowledge_relation_workspace_target_status_type",
			"idx_knowledge_relation_source",
			"idx_knowledge_relation_target",
			"idx_knowledge_relation_workspace_status",
		}
		tests := []struct {
			name         string
			query        domain.Query
			targets      []collectionPlanTargetRequirement
			requirements []collectionPlanIndexRequirement
		}{
			{
				name:  "topic status root",
				query: collectionQueryAll(collectionSingle("object_type", domain.OperatorEQ, "TOPIC"), collectionSingle("status", domain.OperatorEQ, "ACTIVE")),
				targets: []collectionPlanTargetRequirement{{
					purpose: "Topic root", relation: "topic",
					indexes: []string{"idx_knowledge_topic_workspace_status_id", "idx_knowledge_topic_workspace_status", "uq_knowledge_topic_workspace_name"},
				}},
			},
			{
				name:  "claim status root",
				query: collectionQueryAll(collectionSingle("object_type", domain.OperatorEQ, "CLAIM"), collectionSingle("status", domain.OperatorEQ, "CONFIRMED")),
				targets: []collectionPlanTargetRequirement{{
					purpose: "Claim root", relation: "claim",
					indexes: []string{"idx_knowledge_claim_workspace_status_id", "idx_knowledge_claim_workspace_status", "uq_knowledge_claim_workspace_fingerprint"},
				}},
			},
			{
				name:    "topic membership predicate",
				query:   collectionQuery(collectionSingle("topic_id", domain.OperatorEQ, string(fixture.SecondaryTopicID))),
				targets: []collectionPlanTargetRequirement{{purpose: "Relation access", relation: "relation", indexes: boundedRelationIndexes}},
				requirements: []collectionPlanIndexRequirement{{
					purpose: "Claim Topic membership",
					indexes: []string{"idx_knowledge_relation_semantic_scan_topic_claim"},
				}},
			},
			{
				name:    "relation predicate",
				query:   collectionQuery(collectionSingle("relation_type", domain.OperatorEQ, "SUPPORTS")),
				targets: []collectionPlanTargetRequirement{{purpose: "Relation access", relation: "relation", indexes: boundedRelationIndexes}},
				requirements: []collectionPlanIndexRequirement{{
					purpose: "Relation predicate",
					indexes: relationPredicateIndexes,
				}},
			},
			{
				name:  "health predicate",
				query: collectionQuery(collectionSingle("health_issue_type", domain.OperatorEQ, "ORPHAN")),
				targets: []collectionPlanTargetRequirement{{
					purpose: "Health target", relation: "health_issue",
					indexes: []string{"idx_ops_health_issue_workspace_target_status_type"},
				}},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				explain := explainCollectionPage(t, ctx, tx, fixture.WorkspaceID, test.query)
				for _, target := range test.targets {
					assertCollectionTargetAccess(t, explain, target)
				}
				for _, requirement := range test.requirements {
					assertCollectionPlanUsesAny(t, explain.indexes, requirement.purpose, requirement.indexes...)
				}
			})
		}

		t.Run("collection list", func(t *testing.T) {
			statement := collectionListQuery(tx.WithContext(ctx).Session(&gorm.Session{DryRun: true}), fixture.WorkspaceID, []string{"ACTIVE"}).Limit(26).Find(&[]collectionModel{}).Statement
			explain := explainCollectionSQL(t, ctx, statement)
			assertCollectionTargetAccess(t, explain, collectionPlanTargetRequirement{
				purpose: "Collection list", relation: "smart_collection",
				indexes: []string{"idx_learning_smart_collection_workspace_status_updated", "uq_learning_smart_collection_active_name"},
			})
		})
		t.Run("collection search", func(t *testing.T) {
			statement := collectionSearchQuery(tx.WithContext(ctx).Session(&gorm.Session{DryRun: true}), collectionapp.CollectionSearchQuery{WorkspaceID: fixture.WorkspaceID, Query: "collection", Limit: 26}).Find(&[]collectionModel{}).Statement
			explain := explainCollectionSQL(t, ctx, statement)
			assertCollectionTargetAccess(t, explain, collectionPlanTargetRequirement{
				purpose: "Collection search", relation: "smart_collection",
				indexes: []string{"idx_learning_smart_collection_workspace_status_updated", "uq_learning_smart_collection_active_name"},
			})
		})
		t.Run("durable result page", func(t *testing.T) {
			query := collectionQueryAll(collectionSingle("object_type", domain.OperatorEQ, "TOPIC"), collectionSingle("status", domain.OperatorEQ, "ACTIVE"))
			plan, err := collectionapp.CompileQuery(query)
			if err != nil {
				t.Fatal(err)
			}
			args := collectionQueryArguments(fixture.WorkspaceID, plan.Args)
			args["limit"] = 26
			pageSQL := unifiedItemCTE + "SELECT " + collectionItemColumns + " FROM item WHERE " + plan.Where + " ORDER BY item.object_type,item.id LIMIT @limit"
			explain := explainCollectionSQL(t, ctx, tx.WithContext(ctx).Raw(pageSQL, args).Statement)
			assertCollectionTargetAccess(t, explain, collectionPlanTargetRequirement{
				purpose: "Durable Topic root", relation: "topic",
				indexes: []string{"idx_knowledge_topic_workspace_status_id", "idx_knowledge_topic_workspace_status", "uq_knowledge_topic_workspace_name"},
			})
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type collectionPlanTargetRequirement struct {
	purpose  string
	relation string
	indexes  []string
}

type collectionPlanIndexRequirement struct {
	purpose string
	indexes []string
}

type collectionQueryTracer struct {
	logger.Interface
	mu  sync.Mutex
	sql []string
}

func (tracer *collectionQueryTracer) record(statement string) {
	tracer.mu.Lock()
	tracer.sql = append(tracer.sql, strings.TrimSpace(statement))
	tracer.mu.Unlock()
}

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

func (tracer *collectionQueryTracer) LogMode(logger.LogLevel) logger.Interface { return tracer }

func (tracer *collectionQueryTracer) ParamsFilter(_ context.Context, statement string, _ ...any) (string, []any) {
	tracer.record(statement)
	return statement, nil
}

func (tracer *collectionQueryTracer) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	// ParamsFilter records the actual parameterized SQL before the dialect's
	// display-only Explain formatter changes PostgreSQL placeholder syntax.
	_, _ = query()
}

type collectionTracingUnitOfWork struct {
	delegate foundation.UnitOfWork
	tracer   *collectionQueryTracer
}

func (unit *collectionTracingUnitOfWork) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	err := unit.delegate.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		isolation := "default"
		switch options.Isolation {
		case foundation.TransactionIsolationReadCommitted:
			isolation = "read committed"
		case foundation.TransactionIsolationRepeatableRead:
			isolation = "repeatable read"
		case foundation.TransactionIsolationSerializable:
			isolation = "serializable"
		}
		access := "read write"
		if options.ReadOnly {
			access = "read only"
		}
		unit.tracer.record(fmt.Sprintf("BEGIN ISOLATION LEVEL %s %s", isolation, access))
		return work(callbackCtx, scope)
	})
	if err == nil {
		unit.tracer.record("COMMIT")
	} else {
		unit.tracer.record("ROLLBACK")
	}
	return err
}

func collectionQueryFixture(t *testing.T) (context.Context, *platformpostgres.Pool, graphfixture.Fixture) {
	t.Helper()
	database := requireCollectionIntegrationDatabase(t, 16)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	pool := database.Pool()
	return ctx, pool, seedCollectionQueryFixture(t, ctx, pool.DB())
}

func seedCollectionQueryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) graphfixture.Fixture {
	t.Helper()
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newCollectionQueryService(t *testing.T, repository collectionIntegrationRepository) *collectionapp.Service {
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
	const (
		topicCount           = 384
		deprecatedTopicCount = 2048
	)
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
	deprecatedBatch := &pgx.Batch{}
	for index := 0; index < deprecatedTopicCount; index++ {
		name := fmt.Sprintf("Collection Plan Deprecated Topic %04d", index)
		deprecatedBatch.Queue(`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'plan fixture','ACTIVE',1,$5,$5)`, string(collectionQueryID(t)), string(fixture.WorkspaceID), name, strings.ToLower(name), now)
	}
	if err := tx.SendBatch(ctx, deprecatedBatch).Close(); err != nil {
		t.Fatal(err)
	}
	retiredAt := now.Add(time.Microsecond)
	commandTag, err := tx.Exec(ctx, `UPDATE core.topic
		SET status='DEPRECATED',version=2,updated_at=$2
		WHERE workspace_id=$1 AND normalized_name LIKE 'collection plan deprecated topic %'`, string(fixture.WorkspaceID), retiredAt)
	if err != nil {
		t.Fatal(err)
	}
	if commandTag.RowsAffected() != deprecatedTopicCount {
		t.Fatalf("deprecated plan topics=%d want=%d", commandTag.RowsAffected(), deprecatedTopicCount)
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

func seedCollectionPlanCollections(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID) {
	t.Helper()
	batch := &pgx.Batch{}
	now := collectionFixtureTime().Add(4 * time.Hour)
	for index := 0; index < 512; index++ {
		name := fmt.Sprintf("Collection Plan %03d", index)
		batch.Queue(`INSERT INTO learning.smart_collection(
			id,workspace_id,name,normalized_name,description,query_schema_version,query_version,
			query_definition,query_hash,view_type,view_config,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'plan fixture','collection-query/v1',1,'{}'::jsonb,$5,'LIST','{}'::jsonb,'ACTIVE',1,$6,$6)`,
			string(collectionQueryID(t)), string(workspaceID), name, strings.ToLower(name), strings.Repeat("a", 64), now.Add(time.Duration(index)*time.Microsecond))
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
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
	return collectionQueryAll(clause)
}

func collectionQueryAll(clauses ...domain.Clause) domain.Query {
	return domain.Query{
		SchemaVersion: domain.QuerySchemaVersionV1,
		Root:          domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: clauses},
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
		normalized := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(statement, `"`, "")), " "))
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
		case strings.HasPrefix(normalized, "with requested as") && strings.Contains(normalized, "unnest(($1)::text[],($2)::uuid[])"):
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

func explainCollectionPage(t *testing.T, ctx context.Context, db *gorm.DB, workspaceID foundation.ID, query domain.Query) collectionExplainResult {
	t.Helper()
	plan, err := collectionapp.CompileQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	args := collectionQueryArguments(workspaceID, plan.Args)
	pageSQL, args := buildCollectionPageQuery(plan, plan.Where, args, 26)
	return explainCollectionSQL(t, ctx, db.WithContext(ctx).Raw(pageSQL, args).Statement)
}

func explainCollectionSQL(t *testing.T, ctx context.Context, statement *gorm.Statement) collectionExplainResult {
	t.Helper()
	var raw []byte
	if err := statement.ConnPool.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON, COSTS OFF) "+statement.SQL.String(), statement.Vars...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan collectionExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid collection explain: %v %s", err, raw)
	}
	indexes := make(map[string]bool)
	visitCollectionPlan(documents[0].Plan, func(node collectionExplainPlan) {
		if node.IndexName != "" {
			indexes[node.IndexName] = true
		}
	})
	return collectionExplainResult{plan: documents[0].Plan, indexes: indexes, raw: raw}
}

type collectionExplainResult struct {
	plan    collectionExplainPlan
	indexes map[string]bool
	raw     []byte
}

type collectionExplainPlan struct {
	NodeType     string                  `json:"Node Type"`
	RelationName string                  `json:"Relation Name"`
	IndexName    string                  `json:"Index Name"`
	Plans        []collectionExplainPlan `json:"Plans"`
}

func visitCollectionPlan(plan collectionExplainPlan, visit func(collectionExplainPlan)) {
	visit(plan)
	for _, child := range plan.Plans {
		visitCollectionPlan(child, visit)
	}
}

func assertCollectionTargetAccess(t *testing.T, explain collectionExplainResult, target collectionPlanTargetRequirement) {
	t.Helper()
	accesses := 0
	visitCollectionPlan(explain.plan, func(node collectionExplainPlan) {
		if node.RelationName != target.relation {
			return
		}
		accesses++
		switch node.NodeType {
		case "Index Scan", "Index Only Scan":
			if !collectionIndexAllowed(node.IndexName, target.indexes) {
				t.Fatalf("production page %s uses non-bounded index %q; allowed=%v plan=%s", target.purpose, node.IndexName, target.indexes, explain.raw)
			}
		case "Bitmap Heap Scan":
			if !collectionPlanUsesAnyIndex(node, target.indexes) {
				t.Fatalf("production page %s bitmap access misses bounded indexes %v: %s", target.purpose, target.indexes, explain.raw)
			}
		default:
			t.Fatalf("production page %s uses unsupported %s on %s: %s", target.purpose, node.NodeType, target.relation, explain.raw)
		}
	})
	if accesses == 0 {
		t.Fatalf("production page has no %s access for %s: %s", target.relation, target.purpose, explain.raw)
	}
}

func collectionPlanUsesAnyIndex(plan collectionExplainPlan, indexes []string) bool {
	if collectionIndexAllowed(plan.IndexName, indexes) {
		return true
	}
	for _, child := range plan.Plans {
		if collectionPlanUsesAnyIndex(child, indexes) {
			return true
		}
	}
	return false
}

func collectionIndexAllowed(index string, allowed []string) bool {
	for _, candidate := range allowed {
		if index == candidate {
			return true
		}
	}
	return false
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
