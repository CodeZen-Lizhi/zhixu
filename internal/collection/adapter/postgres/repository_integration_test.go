//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectionRepositoryLifecycleIdempotencyCASAndWorkspaceIsolation(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, func(t *testing.T, testCase collectionIntegrationCase) {
		ctx := testCase.context
		pool := testCase.pool
		repository := testCase.repository
		now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
		workspaceID := integrationID(t, "10000000-0000-4000-8000-000000000101")
		otherWorkspaceID := integrationID(t, "10000000-0000-4000-8000-000000000102")
		seedCollectionWorkspace(t, ctx, pool, workspaceID, now)
		seedCollectionWorkspace(t, ctx, pool, otherWorkspaceID, now)
		collectionID := integrationID(t, "20000000-0000-4000-8000-000000000101")
		rejectedCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000104")
		secondCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000105")
		thirdCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000106")
		fourthCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000107")
		service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: repository, IDs: &sequenceCollectionIDs{values: []foundation.ID{
			collectionID,
			integrationID(t, "20000000-0000-4000-8000-000000000102"),
			integrationID(t, "20000000-0000-4000-8000-000000000103"),
			rejectedCollectionID,
			secondCollectionID,
			thirdCollectionID,
			fourthCollectionID,
		}}, Clock: foundation.FixedClock{Value: now}})
		if err != nil {
			t.Fatal(err)
		}
		command := collectionapp.CreateCommand{WorkspaceID: workspaceID, Name: "  Integration Collection ", Description: "first", Query: integrationQuery(), ViewType: domain.ViewTypeList, IdempotencyKey: "collection-create-1"}
		created, err := service.Create(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		if created.Collection.ID != collectionID || created.Collection.Version != 1 || created.Replayed {
			t.Fatalf("created=%+v", created)
		}
		replayed, err := service.Create(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		if !replayed.Replayed || replayed.Collection.ID != created.Collection.ID || replayed.CommandVersion != created.CommandVersion || replayed.Collection.Version != created.Collection.Version || replayed.Collection.Name != created.Collection.Name || replayed.Collection.Description != created.Collection.Description {
			t.Fatalf("replayed=%+v", replayed)
		}
		reopenedService, err := collectionapp.NewService(collectionapp.Dependencies{
			Repository: testCase.reopen(t), IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now},
		})
		if err != nil {
			t.Fatal(err)
		}
		persistedReplay, err := reopenedService.Create(ctx, command)
		if err != nil || !persistedReplay.Replayed || !reflect.DeepEqual(persistedReplay.Collection, created.Collection) {
			t.Fatalf("replay after repository reopen=%+v err=%v", persistedReplay, err)
		}
		searchItems, err := service.SearchCollections(ctx, collectionapp.CollectionSearchQuery{WorkspaceID: workspaceID, Query: "Integration", Limit: 5})
		if err != nil || len(searchItems) != 1 || searchItems[0].ID != collectionID {
			t.Fatalf("collection search=%+v err=%v", searchItems, err)
		}
		otherWorkspaceItems, err := service.SearchCollections(ctx, collectionapp.CollectionSearchQuery{WorkspaceID: otherWorkspaceID, Query: "Integration", Limit: 5})
		if err != nil || len(otherWorkspaceItems) != 0 {
			t.Fatalf("cross-workspace collection search=%+v err=%v", otherWorkspaceItems, err)
		}
		conflict := command
		conflict.Description = "different payload"
		if _, err := service.Create(ctx, conflict); !hasCollectionCode(err, collectionapp.ErrorCodeIdempotencyConflict) {
			t.Fatalf("idempotency conflict err=%v", err)
		}
		if _, err := service.Create(ctx, collectionapp.CreateCommand{
			WorkspaceID: workspaceID, Name: "Integration Collection", Query: command.Query,
			ViewType: domain.ViewTypeList, IdempotencyKey: "collection-create-name-conflict",
		}); !hasCollectionCode(err, "COLLECTION_NAME_CONFLICT") {
			t.Fatalf("active name SQLSTATE conflict err=%v", err)
		}
		var rejectedAggregateCount, rejectedReceiptCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.smart_collection WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(rejectedCollectionID)).Scan(&rejectedAggregateCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.smart_collection_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), "collection-create-name-conflict").Scan(&rejectedReceiptCount); err != nil {
			t.Fatal(err)
		}
		if rejectedAggregateCount != 0 || rejectedReceiptCount != 0 {
			t.Fatalf("failed create left transaction residue aggregate=%d receipt=%d", rejectedAggregateCount, rejectedReceiptCount)
		}

		updatedCommand := collectionapp.UpdateCommand{WorkspaceID: workspaceID, CollectionID: collectionID, ExpectedVersion: 1, Name: "Integration Collection Updated", Description: "updated", Query: command.Query, ViewType: domain.ViewTypeTable, ViewConfig: domain.ViewConfig{Columns: []string{"title"}}, IdempotencyKey: "collection-update-1"}
		commandConflict := updatedCommand
		commandConflict.IdempotencyKey = command.IdempotencyKey
		if _, err := service.Update(ctx, commandConflict); !hasCollectionCode(err, collectionapp.ErrorCodeIdempotencyConflict) {
			t.Fatalf("idempotency command conflict err=%v", err)
		}
		updated, err := service.Update(ctx, updatedCommand)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Collection.Version != 2 || updated.Collection.ViewType != domain.ViewTypeTable {
			t.Fatalf("updated=%+v", updated)
		}
		if _, err := service.Update(ctx, updatedCommand); err != nil {
			t.Fatalf("update replay: %v", err)
		}
		stale := updatedCommand
		stale.IdempotencyKey = "collection-update-stale"
		stale.ExpectedVersion = 1
		if _, err := service.Update(ctx, stale); !hasCollectionCode(err, collectionapp.ErrorCodeVersionConflict) {
			t.Fatalf("stale update err=%v", err)
		}

		archived, err := service.Archive(ctx, collectionapp.ArchiveCommand{WorkspaceID: workspaceID, CollectionID: collectionID, ExpectedVersion: 2, IdempotencyKey: "collection-archive-1"})
		if err != nil {
			t.Fatal(err)
		}
		if archived.Collection.Status != collectionapp.CollectionStatusArchived || archived.Collection.Version != 3 {
			t.Fatalf("archived=%+v", archived)
		}
		archiveReplay, err := service.Archive(ctx, collectionapp.ArchiveCommand{WorkspaceID: workspaceID, CollectionID: collectionID, ExpectedVersion: 2, IdempotencyKey: "collection-archive-1"})
		if err != nil || !archiveReplay.Replayed {
			t.Fatalf("archive replay=%+v err=%v", archiveReplay, err)
		}
		updateReplay, err := service.Update(ctx, updatedCommand)
		if err != nil || !updateReplay.Replayed {
			t.Fatalf("update replay after archive=%+v err=%v", updateReplay, err)
		}
		if updateReplay.Collection.Version != updated.Collection.Version || updateReplay.Collection.Status != collectionapp.CollectionStatusActive || updateReplay.Collection.Name != updated.Collection.Name || updateReplay.Collection.ViewType != updated.Collection.ViewType {
			t.Fatalf("update replay did not return the original snapshot: replay=%+v original=%+v", updateReplay.Collection, updated.Collection)
		}
		if _, err := service.Update(ctx, collectionapp.UpdateCommand{WorkspaceID: workspaceID, CollectionID: collectionID, ExpectedVersion: 3, Name: "should fail", Query: command.Query, ViewType: domain.ViewTypeList, IdempotencyKey: "collection-update-archived"}); !hasCollectionCode(err, collectionapp.ErrorCodeArchivedImmutable) {
			t.Fatalf("archived update err=%v", err)
		}
		triggerErr := executeCollectionIntegrationStatement(ctx, testCase, `UPDATE learning.smart_collection
			SET description='trigger mutation',version=version+1,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(collectionID), now.Add(time.Second))
		var postgresError *pgconn.PgError
		if !hasCollectionCode(triggerErr, collectionapp.ErrorCodeArchivedImmutable) || !errors.As(triggerErr, &postgresError) || postgresError.Code != "55000" {
			t.Fatalf("archived trigger SQLSTATE err=%v postgres=%+v", triggerErr, postgresError)
		}

		if _, err := service.Get(ctx, otherWorkspaceID, collectionID); !hasCollectionCode(err, collectionapp.ErrorCodeNotFound) {
			t.Fatalf("cross workspace get err=%v", err)
		}
		if _, err := service.Archive(ctx, collectionapp.ArchiveCommand{WorkspaceID: otherWorkspaceID, CollectionID: collectionID, ExpectedVersion: 3, IdempotencyKey: "cross-archive"}); !hasCollectionCode(err, collectionapp.ErrorCodeNotFound) {
			t.Fatalf("cross workspace archive err=%v", err)
		}
		archivedPage, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Statuses: []collectionapp.CollectionStatus{collectionapp.CollectionStatusArchived}, Limit: 10})
		if err != nil || len(archivedPage.Items) != 1 || archivedPage.Items[0].ID != collectionID {
			t.Fatalf("archived list=%+v err=%v", archivedPage, err)
		}
		second, err := service.Create(ctx, collectionapp.CreateCommand{WorkspaceID: workspaceID, Name: "Second Collection", Query: command.Query, ViewType: domain.ViewTypeList, IdempotencyKey: "collection-create-2"})
		if err != nil || second.Collection.ID != secondCollectionID {
			t.Fatalf("second collection=%+v err=%v", second, err)
		}
		third, err := service.Create(ctx, collectionapp.CreateCommand{WorkspaceID: workspaceID, Name: "Third Collection", Query: command.Query, ViewType: domain.ViewTypeList, IdempotencyKey: "collection-create-3"})
		if err != nil || third.Collection.ID != thirdCollectionID {
			t.Fatalf("third collection=%+v err=%v", third, err)
		}
		firstPage, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 1})
		if err != nil || len(firstPage.Items) != 1 || firstPage.Items[0].ID != thirdCollectionID || firstPage.NextCursor == "" {
			t.Fatalf("first active page=%+v err=%v", firstPage, err)
		}
		secondPage, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 1, Cursor: firstPage.NextCursor})
		if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID != secondCollectionID || secondPage.NextCursor != "" {
			t.Fatalf("second active page=%+v err=%v", secondPage, err)
		}
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 2, Cursor: firstPage.NextCursor}); !hasCollectionCode(err, collectionapp.ErrorCodeCursorInvalid) {
			t.Fatalf("cross-limit list cursor err=%v", err)
		}
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Statuses: []collectionapp.CollectionStatus{collectionapp.CollectionStatusArchived}, Limit: 1, Cursor: firstPage.NextCursor}); !hasCollectionCode(err, collectionapp.ErrorCodeCursorInvalid) {
			t.Fatalf("cross-status list cursor err=%v", err)
		}
		if _, err := service.Update(ctx, collectionapp.UpdateCommand{WorkspaceID: workspaceID, CollectionID: secondCollectionID, ExpectedVersion: 1, Name: "Second Collection Updated", Query: command.Query, ViewType: domain.ViewTypeList, IdempotencyKey: "collection-update-2"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 1, Cursor: firstPage.NextCursor}); !hasCollectionCode(err, "COLLECTION_CURSOR_STALE") {
			t.Fatalf("stale list cursor err=%v", err)
		}
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: otherWorkspaceID, Limit: 1, Cursor: firstPage.NextCursor}); !hasCollectionCode(err, collectionapp.ErrorCodeCursorInvalid) {
			t.Fatalf("cross workspace list cursor err=%v", err)
		}
		parts := strings.Split(firstPage.NextCursor, ".")
		if len(parts) != 2 || parts[1] == "" {
			t.Fatalf("invalid fixture cursor")
		}
		replacement := byte('A')
		if parts[1][0] == replacement {
			replacement = 'B'
		}
		tampered := parts[0] + "." + string(replacement) + parts[1][1:]
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 1, Cursor: tampered}); !hasCollectionCode(err, collectionapp.ErrorCodeCursorInvalid) {
			t.Fatalf("tampered list cursor err=%v", err)
		}
		fourth, err := service.Create(ctx, collectionapp.CreateCommand{WorkspaceID: workspaceID, Name: "Fourth Collection", Query: command.Query, ViewType: domain.ViewTypeList, IdempotencyKey: "collection-create-4"})
		if err != nil || fourth.Collection.ID != fourthCollectionID {
			t.Fatalf("fourth collection=%+v err=%v", fourth, err)
		}
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 1, Cursor: firstPage.NextCursor}); !hasCollectionCode(err, "COLLECTION_CURSOR_STALE") {
			t.Fatalf("create stale list cursor err=%v", err)
		}
		if _, err := service.Archive(ctx, collectionapp.ArchiveCommand{WorkspaceID: workspaceID, CollectionID: secondCollectionID, ExpectedVersion: 2, IdempotencyKey: "collection-archive-2"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 1, Cursor: firstPage.NextCursor}); !hasCollectionCode(err, "COLLECTION_CURSOR_STALE") {
			t.Fatalf("archive stale list cursor err=%v", err)
		}

		if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection
			SET description='valid predecessor',version=version+1,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(thirdCollectionID), now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection
			SET view_config='{"density":"ROOMY"}'::jsonb,version=version+1,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(fourthCollectionID), now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		corruptPage, corruptErr := service.List(ctx, collectionapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
		if !hasCollectionCode(corruptErr, collectionapp.ErrorCodeResultInconsistent) || len(corruptPage.Items) != 0 || corruptPage.NextCursor != "" {
			t.Fatalf("corrupt row returned partial page=%+v err=%v", corruptPage, corruptErr)
		}
		var one int
		if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
			t.Fatalf("connection pool unusable after corrupt-row rollback: value=%d err=%v", one, err)
		}
	})
}

type sequenceCollectionIDs struct {
	values []foundation.ID
	index  int
}

func (g *sequenceCollectionIDs) New() (foundation.ID, error) {
	value := g.values[g.index]
	g.index++
	return value, nil
}

func integrationID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedCollectionWorkspace(t *testing.T, ctx context.Context, database DB, id foundation.ID, now time.Time) {
	t.Helper()
	_, err := database.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(id), "collection-test", "/tmp/collection-"+string(id), now)
	if err != nil {
		t.Fatal(err)
	}
}

func integrationQuery() domain.Query {
	return domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKind("group"), Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKind("predicate"), Field: "object_type", Operator: "EQ", Value: []byte(`"TOPIC"`)}}}}
}

func hasCollectionCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func executeCollectionIntegrationStatement(ctx context.Context, testCase collectionIntegrationCase, query string, args ...any) error {
	if repository, ok := testCase.repository.(*GORMRepository); ok {
		database, err := newGORMDB(repository.database.WithContext(ctx))
		if err != nil {
			return err
		}
		_, err = database.Exec(ctx, query, args...)
		return err
	}
	_, err := testCase.pool.Exec(ctx, query, args...)
	return classify(err)
}

type collectionIntegrationRepository interface {
	collectionapp.Repository
	collectionapp.CollectionSearchRepository
	collectionapp.QueryRepository
	collectionapp.PreviewRepository
	collectionapp.DurableScanRepository
	collectionapp.DurableScanRevisionVerifier
}

type collectionIntegrationCase struct {
	name       string
	context    context.Context
	platform   *platformpostgres.Pool
	pool       *pgxpool.Pool
	repository collectionIntegrationRepository
	reopen     func(*testing.T) collectionIntegrationRepository
}

type collectionIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool) collectionIntegrationRepository
}

// runCollectionRepositoryIntegrationCases provisions one disposable database
// per implementation. Both constructors still originate from the same shared
// Pool, while committed seed data makes each implementation own its writes.
func runCollectionRepositoryIntegrationCases(t *testing.T, test func(*testing.T, collectionIntegrationCase)) {
	t.Helper()
	variants := []collectionIntegrationVariant{
		{name: "legacy", open: openLegacyCollectionIntegrationRepository},
		{name: "gorm", open: openGORMCollectionIntegrationRepository},
	}
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			fixture := requireCollectionIntegrationDatabase(t, 8)
			platformPool := fixture.Pool()
			if platformPool == nil || platformPool.DB() == nil {
				t.Fatal("Testcontainers fixture did not provide a shared platform pool")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			reopen := func(t *testing.T) collectionIntegrationRepository {
				t.Helper()
				return variant.open(t, platformPool)
			}
			test(t, collectionIntegrationCase{
				name:       variant.name,
				context:    ctx,
				platform:   platformPool,
				pool:       platformPool.DB(),
				repository: reopen(t),
				reopen:     reopen,
			})
		})
	}
}

func requireCollectionIntegrationDatabase(t *testing.T, maxConns int32) *testdb.Fixture {
	t.Helper()
	return testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         maxConns,
	})
}

func openLegacyCollectionIntegrationRepository(t *testing.T, platform *platformpostgres.Pool) collectionIntegrationRepository {
	t.Helper()
	repository, err := NewRepository(platform.DB())
	if err != nil {
		t.Fatalf("construct legacy Collection repository: %v", err)
	}
	return repository
}

func openGORMCollectionIntegrationRepository(t *testing.T, platform *platformpostgres.Pool) collectionIntegrationRepository {
	t.Helper()
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatalf("construct GORM Collection repository: %v", err)
	}
	return repository
}
