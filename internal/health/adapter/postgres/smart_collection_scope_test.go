package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestLoadSmartCollectionScopeUsesBindingAwareSnapshotWithoutPlanReload(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	collectionID := foundation.ID("20000000-0000-4000-8000-000000000001")
	binding := healthapp.SmartCollectionBinding{WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 2, QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 2}
	port := &snapshotMembershipFake{snapshot: healthapp.SmartCollectionMembership{Binding: binding, Members: []domain.ObjectRef{{Type: domain.ObjectTypeClaim, ID: foundation.ID("30000000-0000-4000-8000-000000000001")}, {Type: domain.ObjectTypeTopic, ID: foundation.ID("40000000-0000-4000-8000-000000000001")}}}}
	scope := domain.ScanScope{Type: domain.ScanScopeTypeSmartCollection, Ref: collectionID, Version: binding.CollectionVersion, SchemaVersion: "health-scope/smart-collection/v1", Hash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount}
	members, got, err := loadSmartCollectionScope(context.Background(), port, workspaceID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if got != binding || len(members.TopicIDs) != 1 || len(members.ClaimIDs) != 1 || port.snapshotCalls != 1 || port.planCalls != 0 {
		t.Fatalf("members=%#v binding=%#v port=%#v", members, got, port)
	}
}

type snapshotMembershipFake struct {
	snapshot      healthapp.SmartCollectionMembership
	snapshotCalls int
	planCalls     int
}

func (fake *snapshotMembershipFake) Plan(context.Context, foundation.ID, foundation.ID) (healthapp.SmartCollectionBinding, error) {
	fake.planCalls++
	return fake.snapshot.Binding, nil
}

func (fake *snapshotMembershipFake) Load(context.Context, healthapp.SmartCollectionBinding, int) (healthapp.SmartCollectionMembership, error) {
	return fake.snapshot, nil
}

func (fake *snapshotMembershipFake) LoadForScope(ctx context.Context, _ foundation.ID, _ domain.ScanScope) (healthapp.SmartCollectionMembership, error) {
	fake.snapshotCalls++
	if err := ctx.Err(); err != nil {
		return healthapp.SmartCollectionMembership{}, err
	}
	return fake.snapshot, nil
}

func TestLoadSmartCollectionScopePreservesCancellationFromSnapshotPort(t *testing.T) {
	port := &snapshotMembershipFake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := loadSmartCollectionScope(ctx, port, foundation.ID("10000000-0000-4000-8000-000000000001"), domain.ScanScope{Type: domain.ScanScopeTypeSmartCollection, Ref: foundation.ID("20000000-0000-4000-8000-000000000001"), Version: 1, SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}
