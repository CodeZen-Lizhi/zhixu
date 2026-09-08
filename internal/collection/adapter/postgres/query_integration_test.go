//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func TestCollectionQueryUsesUnifiedReadModelAndCursor(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, testCollectionQueryUsesUnifiedReadModelAndCursor)
}

func testCollectionQueryUsesUnifiedReadModelAndCursor(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	now := time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	workspaceID := integrationID(t, "10000000-0000-4000-8000-000000000205")
	seedCollectionWorkspace(t, ctx, pool, workspaceID, now)
	for index, topicID := range []string{"30000000-0000-4000-8000-000000000205", "30000000-0000-4000-8000-000000000206"} {
		if _, err := pool.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,'summary','ACTIVE',1,$4,$4)`, topicID, string(workspaceID), "Topic "+string(rune('A'+index)), now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: testCase.repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, collectionapp.CreateCommand{WorkspaceID: workspaceID, Name: "Topic Results", Query: domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: "object_type", Operator: "EQ", Value: []byte(`"TOPIC"`)}}}}, ViewType: domain.ViewTypeList, IdempotencyKey: "query-create-201"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: workspaceID, CollectionID: created.Collection.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExactCount != 2 || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	second, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: workspaceID, CollectionID: created.Collection.ID, Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.ExactCount != 2 {
		t.Fatalf("second page=%+v", second)
	}
}

func TestCollectionPreviewUsesMultiValueFactsAndStalesAfterSourceMutation(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, testCollectionPreviewUsesMultiValueFactsAndStalesAfterSourceMutation)
}

func testCollectionPreviewUsesMultiValueFactsAndStalesAfterSourceMutation(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	fixture := seedCollectionQueryFixture(t, ctx, pool)
	service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: testCase.repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(query domain.Query, key string) collectionapp.Collection {
		t.Helper()
		result, createErr := service.Create(ctx, collectionapp.CreateCommand{WorkspaceID: fixture.WorkspaceID, Name: "preview-" + key, Query: query, ViewType: domain.ViewTypeList, IdempotencyKey: key})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return result.Collection
	}
	query := func(field, operator string, value string, sort ...domain.SortTerm) domain.Query {
		return domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: field, Operator: operator, Value: []byte(value)}}}, Sort: sort}
	}
	collection := create(query("topic_id", "EQ", `"`+string(fixture.SecondaryTopicID)+`"`), "multi-topic")
	page, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: collection.ID, Limit: 25})
	if err != nil || page.ExactCount != 2 {
		t.Fatalf("multi-topic page=%+v err=%v", page, err)
	}
	if !containsObject(page.Items, "CLAIM", fixture.SecondClaimID) || !containsObject(page.Items, "TOPIC", fixture.SecondaryTopicID) {
		t.Fatalf("topic membership results=%+v", page.Items)
	}

	collection = create(query("relation_type", "EQ", `"SUPPORTS"`), "relation-any")
	page, err = service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: collection.ID, Limit: 25})
	if err != nil || page.ExactCount != 2 {
		t.Fatalf("relation page=%+v err=%v", page, err)
	}

	issueID := "99000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue(id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at) VALUES($1,$2,'ORPHAN','CLAIM',$3,'health.detector.test',$4,'health-issue-fingerprint/v1',$5,'detector/v1','HIGH','fixture','OPEN',1,$6,$6,$6,$6,$6)`, issueID, string(fixture.WorkspaceID), string(fixture.FirstClaimID), strings.Repeat("1", 64), strings.Repeat("2", 64), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	collection = create(query("health_issue_type", "EQ", `"ORPHAN"`), "health-any")
	page, err = service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: collection.ID, Limit: 25})
	if err != nil || page.ExactCount != 1 || len(page.Items) != 1 || page.Items[0].ID != fixture.FirstClaimID {
		t.Fatalf("health page=%+v err=%v", page, err)
	}

	collection = create(query("object_type", "EQ", `"CLAIM"`, domain.SortTerm{Field: "text", Direction: "ASC"}), "text-sort")
	first, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: collection.ID, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("text first=%+v err=%v", first, err)
	}
	second, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: collection.ID, Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("text second=%+v err=%v", second, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.source SET original_location='mutated-source.md' WHERE workspace_id=$1`, string(fixture.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: collection.ID, Limit: 1, Cursor: first.NextCursor}); err == nil || !strings.Contains(err.Error(), "COLLECTION_CURSOR_STALE") {
		t.Fatalf("source mutation cursor error=%v", err)
	}
}

func TestCollectionItemHydrationReturnsBoundedTopicClaimSummaries(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, testCollectionItemHydrationReturnsBoundedTopicClaimSummaries)
}

func testCollectionItemHydrationReturnsBoundedTopicClaimSummaries(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	fixture := seedCollectionQueryFixture(t, ctx, pool)
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic_alias(id,workspace_id,topic_id,alias,normalized_alias,created_at) VALUES($1,$2,$3,$4,$5,$6)`,
		string(integrationID(t, "98000000-0000-4000-8000-000000000001")), string(fixture.WorkspaceID), string(fixture.PrimaryTopicID), "Primary Alias", "primary alias", now); err != nil {
		t.Fatal(err)
	}
	insertCollectionHealthIssue(t, ctx, pool, fixture.WorkspaceID, fixture.FirstClaimID)
	service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: testCase.repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: "object_type", Operator: "IN", Values: []json.RawMessage{json.RawMessage(`"TOPIC"`), json.RawMessage(`"CLAIM"`)}}}}}
	page, err := service.Preview(ctx, collectionapp.PreviewQuery{WorkspaceID: fixture.WorkspaceID, Query: query, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var topic, claim *collectionapp.CollectionItem
	for index := range page.Items {
		item := &page.Items[index]
		if item.ID == fixture.PrimaryTopicID {
			topic = item
		}
		if item.ID == fixture.FirstClaimID {
			claim = item
		}
	}
	if topic == nil || len(topic.Aliases) != 1 || topic.Aliases[0] != "Primary Alias" {
		t.Fatalf("topic hydration=%+v", topic)
	}
	if topic.HealthSummary != nil || len(topic.SourceSummaries) != 0 {
		t.Fatalf("topic claim-only hydration leaked=%+v", topic)
	}
	if claim == nil || len(claim.Applicability) == 0 || claim.ApplicabilitySchemaVersion == "" || claim.ApplicabilityHash == "" || len(claim.SourceSummaries) == 0 {
		t.Fatalf("claim hydration=%+v", claim)
	}
	if claim.HealthSummary == nil || claim.HealthSummary.Count != 1 || claim.HealthSummary.MaxSeverity == "" || len(claim.HealthSummary.IssueTypes) != 1 {
		t.Fatalf("health hydration=%+v", claim.HealthSummary)
	}
	if len(claim.RelationTypes) == 0 {
		t.Fatalf("relation hydration=%+v", claim)
	}
}

func TestCollectionQueryTimeoutClassificationAndConnectionReuse(t *testing.T) {
	fixture := requireCollectionIntegrationDatabase(t, 4)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	platform := fixture.Pool()
	pool := platform.DB()
	gormRepository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		statement bool
		wantCause error
	}{
		{name: "statement timeout", statement: true},
		{name: "context deadline", statement: false, wantCause: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			queryCtx := ctx
			cancel := func() {}
			if !test.statement {
				queryCtx, cancel = context.WithTimeout(ctx, time.Millisecond)
			}
			defer cancel()
			queryErr := gormRepository.within(queryCtx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB) error {
				if test.statement {
					if timeoutErr := configureCollectionStatementTimeout(callbackCtx, database, time.Millisecond); timeoutErr != nil {
						return timeoutErr
					}
				}
				var value int
				return database.WithContext(callbackCtx).Raw(`SELECT 1 FROM pg_catalog.pg_sleep(?)`, 0.05).Row().Scan(&value)
			})
			if queryErr == nil {
				t.Fatal("slow query unexpectedly succeeded")
			}
			classified := classify(queryErr)
			var foundationErr *foundation.Error
			if !errors.As(classified, &foundationErr) || foundationErr.Code != collectionapp.ErrorCodeQueryTimeout || !foundationErr.Retryable {
				t.Fatalf("queryErr=%v classified=%v", queryErr, classified)
			}
			if test.wantCause != nil && !errors.Is(classified, test.wantCause) {
				t.Fatalf("classified=%v does not preserve %v", classified, test.wantCause)
			}
		})
	}
	t.Run("gorm statement timeout", func(t *testing.T) {
		err := gormRepository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB) error {
			if timeoutErr := configureCollectionStatementTimeout(callbackCtx, database, time.Millisecond); timeoutErr != nil {
				return timeoutErr
			}
			var value int
			return database.WithContext(callbackCtx).Raw(`SELECT 1 FROM pg_catalog.pg_sleep(?)`, 0.05).Row().Scan(&value)
		})
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != collectionapp.ErrorCodeQueryTimeout || !classified.Retryable {
			t.Fatalf("GORM statement timeout err=%v", err)
		}
	})
	t.Run("gorm active cancellation cause", func(t *testing.T) {
		cause := errors.New("collection query caller stopped waiting")
		queryCtx, cancelCause := context.WithCancelCause(ctx)
		timer := time.AfterFunc(20*time.Millisecond, func() { cancelCause(cause) })
		err := gormRepository.within(queryCtx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB) error {
			var value int
			return database.WithContext(callbackCtx).Raw(`SELECT 1 FROM pg_catalog.pg_sleep(?)`, 0.5).Row().Scan(&value)
		})
		timer.Stop()
		cancelCause(nil)
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != collectionapp.ErrorCodeDependencyUnavailable || classified.Retryable ||
			!errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("GORM cancellation err=%v", err)
		}
	})
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("connection pool unusable after timeout: value=%d err=%v", one, err)
	}
}

func containsObject(items []collectionapp.CollectionItem, objectType string, id foundation.ID) bool {
	for _, item := range items {
		if item.ObjectType == objectType && item.ID == id {
			return true
		}
	}
	return false
}
