package collection

import (
	"context"
	"strings"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	snapshotWorkspaceID  foundation.ID = "11111111-1111-4111-8111-111111111111"
	snapshotCollectionID foundation.ID = "22222222-2222-4222-8222-222222222222"
	snapshotItemOneID    foundation.ID = "33333333-3333-4333-8333-333333333333"
	snapshotItemTwoID    foundation.ID = "44444444-4444-4444-8444-444444444444"
)

type collectionServiceFake struct {
	collection collectionapp.Collection
	binding    collectionapp.DurableScanBinding
	pages      []collectionapp.DurableScanPage
	requests   []collectionapp.DurableScanPageRequest
}

func (service *collectionServiceFake) Get(context.Context, foundation.ID, foundation.ID) (collectionapp.Collection, error) {
	return service.collection, nil
}

func (service *collectionServiceFake) PlanDurableScan(context.Context, foundation.ID, foundation.ID) (collectionapp.DurableScanBinding, error) {
	return service.binding, nil
}

func (service *collectionServiceFake) ReadDurableScanPage(_ context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	service.requests = append(service.requests, request)
	index := len(service.requests) - 1
	return service.pages[index], nil
}

func TestSnapshotReaderUsesFrozenDurablePagesWithoutLocalFiltering(t *testing.T) {
	now := time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC)
	binding := collectionapp.DurableScanBinding{
		WorkspaceID: snapshotWorkspaceID, CollectionID: snapshotCollectionID, CollectionVersion: 7,
		QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 2,
	}
	firstKey := collectionapp.DurableScanKey{ObjectType: "CLAIM", ID: snapshotItemOneID}
	service := &collectionServiceFake{
		collection: collectionapp.Collection{ID: snapshotCollectionID, WorkspaceID: snapshotWorkspaceID, Name: "Export Me", Version: 7, QueryHash: binding.QueryHash},
		binding:    binding,
		pages: []collectionapp.DurableScanPage{
			{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "CLAIM", ID: snapshotItemOneID, Title: "Claim", Summary: "Summary", Status: "CONFIRMED", SourceSummaries: []collectionapp.CollectionSourceSummary{{SourceType: "MARKDOWN", FilePath: "/workspace/private.md", SupportType: "SUPPORTS", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}}, Next: &firstKey},
			{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "TOPIC", ID: snapshotItemTwoID, Title: "Topic", Status: "ACTIVE", RelationTypes: []string{"SUPPORTS"}, CreatedAt: now, UpdatedAt: now}}, Complete: true},
		},
	}
	reader, err := NewSnapshotReader(service)
	if err != nil {
		t.Fatal(err)
	}
	version := int64(7)
	snapshot, err := reader.ReadCollection(context.Background(), snapshotWorkspaceID, domain.Scope{Kind: domain.ScopeCollection, CollectionID: pointerID(snapshotCollectionID), CollectionVersion: &version, QueryHash: binding.QueryHash}, 10)
	if err != nil {
		t.Fatalf("ReadCollection() error=%v", err)
	}
	if snapshot.Name != "Export Me" || snapshot.ExactCount != 2 || snapshot.ReadModelRevision != binding.ReadModelRevision || len(snapshot.Items) != 2 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if len(service.requests) != 2 || service.requests[0].After != nil || service.requests[1].After == nil || *service.requests[1].After != firstKey {
		t.Fatalf("durable requests=%#v", service.requests)
	}
	if got := snapshot.Items[0].Sources[0].Path; got != "/workspace/private.md" {
		t.Fatalf("source path=%q", got)
	}
}

func TestSnapshotReaderRejectsBindingDriftAndOversizedCollection(t *testing.T) {
	binding := collectionapp.DurableScanBinding{WorkspaceID: snapshotWorkspaceID, CollectionID: snapshotCollectionID, CollectionVersion: 2, QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 11}
	service := &collectionServiceFake{collection: collectionapp.Collection{ID: snapshotCollectionID, WorkspaceID: snapshotWorkspaceID, Version: 2, QueryHash: binding.QueryHash}, binding: binding}
	reader, err := NewSnapshotReader(service)
	if err != nil {
		t.Fatal(err)
	}
	version := int64(2)
	if _, err := reader.ReadCollection(context.Background(), snapshotWorkspaceID, domain.Scope{Kind: domain.ScopeCollection, CollectionID: pointerID(snapshotCollectionID), CollectionVersion: &version, QueryHash: binding.QueryHash}, 10); err == nil {
		t.Fatal("ReadCollection() accepted a collection beyond maxItems")
	}
	service.binding.ExactCount = 0
	service.binding.QueryHash = strings.Repeat("c", 64)
	if _, err := reader.ReadCollection(context.Background(), snapshotWorkspaceID, domain.Scope{Kind: domain.ScopeCollection, CollectionID: pointerID(snapshotCollectionID), CollectionVersion: &version, QueryHash: binding.QueryHash}, 10); err == nil {
		t.Fatal("ReadCollection() accepted query hash drift")
	}
}

func pointerID(value foundation.ID) *foundation.ID { return &value }
