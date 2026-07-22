package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type fakeRepository struct {
	create        CreateRecord
	update        UpdateRecord
	archive       ArchiveRecord
	createResult  CommandResult
	updateResult  CommandResult
	archiveResult CommandResult
	results       ResultsQuery
	queryResult   ResultPage
	preview       PreviewQuery
	previewResult ResultPage
	list          ListQuery
	listResult    CollectionListPage
	durablePlan   DurableScanBinding
	durableRead   DurableScanPageRequest
	durablePage   DurableScanPage
}

func (f *fakeRepository) CreateCollection(_ context.Context, record CreateRecord) (CommandResult, error) {
	f.create = record
	if f.createResult.Collection.ID == "" {
		return CommandResult{Collection: record.Collection, CommandVersion: record.Collection.Version, RequestHash: record.RequestHash, CommandType: "CREATE"}, nil
	}
	return f.createResult, nil
}
func (f *fakeRepository) UpdateCollection(_ context.Context, record UpdateRecord) (CommandResult, error) {
	f.update = record
	return f.updateResult, nil
}
func (f *fakeRepository) ArchiveCollection(_ context.Context, record ArchiveRecord) (CommandResult, error) {
	f.archive = record
	return f.archiveResult, nil
}
func (f *fakeRepository) GetCollection(context.Context, foundation.ID, foundation.ID) (Collection, error) {
	return Collection{}, errors.New("unused")
}
func (f *fakeRepository) ListCollections(_ context.Context, query ListQuery) (CollectionListPage, error) {
	f.list = query
	return f.listResult, nil
}
func (f *fakeRepository) ExecutePreview(_ context.Context, query PreviewQuery) (ResultPage, error) {
	f.preview = query
	return f.previewResult, nil
}
func (f *fakeRepository) ExecuteQuery(_ context.Context, query ResultsQuery) (ResultPage, error) {
	f.results = query
	return f.queryResult, nil
}
func (f *fakeRepository) PlanDurableScan(context.Context, foundation.ID, foundation.ID) (DurableScanBinding, error) {
	return f.durablePlan, nil
}
func (f *fakeRepository) ReadDurableScanPage(_ context.Context, request DurableScanPageRequest) (DurableScanPage, error) {
	f.durableRead = request
	return f.durablePage, nil
}

type fixedIDs struct{ value foundation.ID }

func (g fixedIDs) New() (foundation.ID, error) { return g.value, nil }

func collectionTestIDs() (foundation.ID, foundation.ID) {
	return foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000001")
}

func collectionQueryFixture() domain.Query {
	return domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKind("group"), Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKind("predicate"), Field: "object_type", Operator: "EQ", Value: []byte(`"TOPIC"`)}}}}
}

func TestServiceCreateCanonicalizesDefinitionAndHash(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	repository := &fakeRepository{}
	service, err := NewService(Dependencies{Repository: repository, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	query := collectionQueryFixture()
	canonical, err := domain.CanonicalizeQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Create(context.Background(), CreateCommand{WorkspaceID: workspaceID, Name: "  My\tCollection ", Description: "  desc\nvalue ", Query: query, ViewType: domain.ViewTypeList, IdempotencyKey: " create-1 "})
	if err != nil {
		t.Fatal(err)
	}
	if repository.create.IdempotencyKey != "create-1" || repository.create.Collection.Name != "My Collection" || repository.create.Collection.NormalizedName != "my collection" || repository.create.Collection.Description != "desc value" {
		t.Fatalf("record=%+v", repository.create)
	}
	if repository.create.RequestHash == "" || result.CommandType != "CREATE" {
		t.Fatalf("result=%+v", result)
	}
	if repository.create.Collection.QueryHash != canonical.Hash {
		t.Fatalf("query hash=%s want=%s", repository.create.Collection.QueryHash, canonical.Hash)
	}
}

func TestServiceRejectsUnavailableRegistryField(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	service, err := NewService(Dependencies{Repository: &fakeRepository{}, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKind("group"), Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKind("predicate"), Field: "tag", Operator: "EQ", Value: []byte(`"x"`)}}}}
	_, err = service.Create(context.Background(), CreateCommand{WorkspaceID: workspaceID, Name: "collection", Query: query, ViewType: domain.ViewTypeList, IdempotencyKey: "tag-1"})
	assertCode(t, err, domain.ErrorCodeFieldUnavailable)
}

func TestServicePreviewCanonicalizesAndDefaultsLimit(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	repository := &fakeRepository{previewResult: ResultPage{ExactCount: 3}}
	service, err := NewService(Dependencies{Repository: repository, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	query := collectionQueryFixture()
	canonical, err := domain.CanonicalizeQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Preview(context.Background(), PreviewQuery{WorkspaceID: workspaceID, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := domain.CanonicalizeQuery(repository.preview.Query)
	if err != nil || actual.Hash != canonical.Hash || repository.preview.Limit != defaultResultLimit || result.ExactCount != 3 {
		t.Fatalf("preview=%+v result=%+v err=%v", repository.preview, result, err)
	}
}

func TestServiceResultsDefaultsLimit(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	repository := &fakeRepository{queryResult: ResultPage{ExactCount: 2}}
	service, err := NewService(Dependencies{Repository: repository, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Results(context.Background(), ResultsQuery{WorkspaceID: workspaceID, CollectionID: collectionID})
	if err != nil || repository.results.Limit != defaultResultLimit || result.ExactCount != 2 {
		t.Fatalf("results=%+v result=%+v err=%v", repository.results, result, err)
	}
}

func TestServiceDurableScanValidatesBindingKeysetAndPairs(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	claimA := DurableScanKey{ObjectType: "CLAIM", ID: foundation.ID("30000000-0000-4000-8000-000000000001")}
	claimB := DurableScanKey{ObjectType: "CLAIM", ID: foundation.ID("30000000-0000-4000-8000-000000000002")}
	topic := DurableScanKey{ObjectType: "TOPIC", ID: foundation.ID("40000000-0000-4000-8000-000000000001")}
	binding := DurableScanBinding{
		WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 2,
		QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 3,
	}
	repository := &fakeRepository{durablePlan: binding}
	repository.durablePage = DurableScanPage{
		Binding: binding,
		Items: []CollectionItem{
			{ObjectType: claimA.ObjectType, ID: claimA.ID},
			{ObjectType: claimB.ObjectType, ID: claimB.ID},
		},
		Pairs: []DurableScanPair{
			{Source: claimA, Target: claimB},
			{Source: claimA, Target: topic},
			{Source: claimB, Target: topic},
		},
		Nodes: []DurableScanNode{
			{Key: claimA, Version: 1, Status: "CONFIRMED", Title: "claim a", Summary: "claim a"},
			{Key: claimB, Version: 1, Status: "CONFIRMED", Title: "claim b", Summary: "claim b"},
			{Key: topic, Version: 1, Status: "ACTIVE", Title: "topic", Summary: "topic"},
		},
		Next: &claimB,
	}
	service, err := NewService(Dependencies{Repository: repository, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanDurableScan(context.Background(), workspaceID, collectionID)
	if err != nil || plan != binding {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	page, err := service.ReadDurableScanPage(context.Background(), DurableScanPageRequest{Binding: binding, Limit: 2, PairTargetLimit: 2})
	if err != nil || page.Next == nil || *page.Next != claimB || len(page.Pairs) != 3 || repository.durableRead.Binding != binding {
		t.Fatalf("page=%+v request=%+v err=%v", page, repository.durableRead, err)
	}

	repository.durablePage.Pairs = append(repository.durablePage.Pairs, DurableScanPair{Source: claimA, Target: DurableScanKey{ObjectType: "TOPIC", ID: foundation.ID("40000000-0000-4000-8000-000000000002")}})
	_, err = service.ReadDurableScanPage(context.Background(), DurableScanPageRequest{Binding: binding, Limit: 2, PairTargetLimit: 3})
	assertCode(t, err, ErrorCodeResultInconsistent)
}

func TestServiceListDefaultsLimitAndPreservesCursor(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	repository := &fakeRepository{listResult: CollectionListPage{Items: []Collection{{ID: collectionID}}, NextCursor: "next"}}
	service, err := NewService(Dependencies{Repository: repository, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), ListQuery{WorkspaceID: workspaceID, Cursor: "current"})
	if err != nil || repository.list.Limit != defaultListLimit || repository.list.Cursor != "current" || len(page.Items) != 1 || page.NextCursor != "next" {
		t.Fatalf("query=%+v page=%+v err=%v", repository.list, page, err)
	}
}

func TestServiceValidatesReplayBinding(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := collectionTestIDs()
	now := time.Now().UTC()
	repository := &fakeRepository{}
	service, err := NewService(Dependencies{Repository: repository, IDs: fixedIDs{value: collectionID}, Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	query := collectionQueryFixture()
	command := CreateCommand{WorkspaceID: workspaceID, Name: "collection", Query: query, ViewType: domain.ViewTypeList, IdempotencyKey: "replay-1"}
	bad := Collection{ID: collectionID, WorkspaceID: workspaceID, Version: 1}
	repository.createResult = CommandResult{Collection: bad, CommandVersion: 1, RequestHash: "bad", CommandType: "CREATE", Replayed: true}
	if _, err := service.Create(context.Background(), command); err == nil {
		t.Fatal("invalid replay accepted")
	}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("err=%v code=%s want=%s", err, classifiedCode(classified), code)
	}
}

func classifiedCode(err *foundation.Error) string {
	if err == nil {
		return ""
	}
	return err.Code
}
