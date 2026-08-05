//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectionRepositoryLifecycleIdempotencyCASAndWorkspaceIsolation(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
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
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	workspaceID := integrationID(t, "10000000-0000-4000-8000-000000000101")
	otherWorkspaceID := integrationID(t, "10000000-0000-4000-8000-000000000102")
	seedCollectionWorkspace(t, ctx, tx, workspaceID, now)
	seedCollectionWorkspace(t, ctx, tx, otherWorkspaceID, now)
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	collectionID := integrationID(t, "20000000-0000-4000-8000-000000000101")
	secondCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000104")
	thirdCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000105")
	fourthCollectionID := integrationID(t, "20000000-0000-4000-8000-000000000106")
	service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: repository, IDs: &sequenceCollectionIDs{values: []foundation.ID{
		collectionID,
		integrationID(t, "20000000-0000-4000-8000-000000000102"),
		integrationID(t, "20000000-0000-4000-8000-000000000103"),
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
	if _, err := service.Create(ctx, collectionapp.CreateCommand{WorkspaceID: workspaceID, Name: "Fourth Collection", Query: command.Query, ViewType: domain.ViewTypeList, IdempotencyKey: "collection-create-4"}); err != nil {
		t.Fatal(err)
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

func seedCollectionWorkspace(t *testing.T, ctx context.Context, tx pgx.Tx, id foundation.ID, now time.Time) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(id), "collection-test", "/tmp/collection-"+string(id), now)
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
