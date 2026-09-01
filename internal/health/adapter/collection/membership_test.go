package collection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestMembershipLoadsDurablePagesAndRevalidatesBinding(t *testing.T) {
	binding := collectionMembershipBinding()
	reader := &durableReaderFake{binding: binding, pages: []collectionapp.DurableScanPage{
		{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "CLAIM", ID: collectionMemberID(1)}}, Next: &collectionapp.DurableScanKey{ObjectType: "CLAIM", ID: collectionMemberID(1)}},
		{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "TOPIC", ID: collectionMemberID(2)}}, Complete: true},
	}}
	membership, err := NewMembership(reader)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := membership.Plan(context.Background(), binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := membership.Load(context.Background(), planned, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Members) != 2 || loaded.Members[0].Type != domain.ObjectTypeClaim || loaded.Members[1].Type != domain.ObjectTypeTopic || reader.planCalls != 2 || reader.pageCalls != 2 {
		t.Fatalf("loaded=%#v reader=%#v", loaded, reader)
	}
}

func TestMembershipFailsClosedWhenFinalBindingChanges(t *testing.T) {
	binding := collectionMembershipBinding()
	reader := &durableReaderFake{binding: binding, pages: []collectionapp.DurableScanPage{{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "CLAIM", ID: collectionMemberID(1)}, {ObjectType: "TOPIC", ID: collectionMemberID(2)}}, Complete: true}}}
	reader.finalBinding = binding
	reader.finalBinding.ReadModelRevision = strings.Repeat("c", 64)
	membership, _ := NewMembership(reader)
	planned, _ := membership.Plan(context.Background(), binding.WorkspaceID, binding.CollectionID)
	_, err := membership.Load(context.Background(), planned, 10)
	if !hasMembershipCode(err, domain.ErrorCodeScanScopeStale) {
		t.Fatalf("stale load error = %v", err)
	}
}

func TestMembershipRejectsUnboundedOrInvalidMembers(t *testing.T) {
	binding := collectionMembershipBinding()
	membership, _ := NewMembership(&durableReaderFake{binding: binding})
	healthBinding := toHealthBinding(binding)
	if _, err := membership.Load(context.Background(), healthBinding, 1); !hasMembershipCode(err, domain.ErrorCodeScanScopeUnavailable) {
		t.Fatalf("unbounded load error = %v", err)
	}
	reader := &durableReaderFake{binding: binding, pages: []collectionapp.DurableScanPage{{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "INDEX_VERSION", ID: collectionMemberID(1)}, {ObjectType: "TOPIC", ID: collectionMemberID(2)}}, Complete: true}}}
	membership, _ = NewMembership(reader)
	if _, err := membership.Load(context.Background(), healthBinding, 10); !hasMembershipCode(err, domain.ErrorCodeScanInvalid) {
		t.Fatalf("invalid member error = %v", err)
	}
}

func TestMembershipLoadForScopeReusesDurableSnapshotAcrossDetectorPages(t *testing.T) {
	binding := collectionMembershipBinding()
	reader := &durableReaderFake{binding: binding, pages: []collectionapp.DurableScanPage{
		{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "CLAIM", ID: collectionMemberID(1)}}, Next: &collectionapp.DurableScanKey{ObjectType: "CLAIM", ID: collectionMemberID(1)}},
		{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "TOPIC", ID: collectionMemberID(2)}}, Complete: true},
	}}
	membership, err := NewMembership(reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.ScanScope{Type: domain.ScanScopeTypeSmartCollection, Ref: binding.CollectionID, Version: binding.CollectionVersion, SchemaVersion: "health-scope/smart-collection/v1", Hash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount}
	for i := 0; i < 3; i++ {
		loaded, loadErr := membership.LoadForScope(context.Background(), binding.WorkspaceID, scope)
		if loadErr != nil || len(loaded.Members) != 2 {
			t.Fatalf("iteration=%d loaded=%#v err=%v", i, loaded, loadErr)
		}
		if verifyErr := membership.VerifyBinding(context.Background(), toHealthBinding(binding)); verifyErr != nil {
			t.Fatalf("iteration=%d verify err=%v", i, verifyErr)
		}
	}
	if reader.pageCalls != 2 || reader.planCalls != 2 || reader.revisionCalls != 5 {
		t.Fatalf("reader=%#v; expected one linear load, one final load check, two cache checks and three explicit binding checks", reader)
	}
}

func TestMembershipLoadForScopeHonorsCancellationDuringRevisionCheck(t *testing.T) {
	binding := collectionMembershipBinding()
	reader := &durableReaderFake{binding: binding, pages: []collectionapp.DurableScanPage{{Binding: binding, Items: []collectionapp.CollectionItem{{ObjectType: "CLAIM", ID: collectionMemberID(1)}, {ObjectType: "TOPIC", ID: collectionMemberID(2)}}, Complete: true}}}
	membership, _ := NewMembership(reader)
	scope := domain.ScanScope{Type: domain.ScanScopeTypeSmartCollection, Ref: binding.CollectionID, Version: binding.CollectionVersion, SchemaVersion: "health-scope/smart-collection/v1", Hash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount}
	if _, err := membership.LoadForScope(context.Background(), binding.WorkspaceID, scope); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := membership.VerifyBinding(ctx, toHealthBinding(binding)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

type durableReaderFake struct {
	binding       collectionapp.DurableScanBinding
	finalBinding  collectionapp.DurableScanBinding
	pages         []collectionapp.DurableScanPage
	planCalls     int
	pageCalls     int
	revisionCalls int
}

func (reader *durableReaderFake) PlanDurableScan(context.Context, foundation.ID, foundation.ID) (collectionapp.DurableScanBinding, error) {
	reader.planCalls++
	if reader.planCalls > 1 && reader.finalBinding.CollectionID != "" {
		return reader.finalBinding, nil
	}
	return reader.binding, nil
}

func (reader *durableReaderFake) ReadDurableScanPage(_ context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	if reader.pageCalls >= len(reader.pages) {
		return collectionapp.DurableScanPage{}, errors.New("unexpected durable page call")
	}
	page := reader.pages[reader.pageCalls]
	reader.pageCalls++
	return page, nil
}

func (reader *durableReaderFake) VerifyDurableScanRevision(ctx context.Context, binding collectionapp.DurableScanBinding) error {
	reader.revisionCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if binding != reader.binding {
		return foundation.NewError(foundation.ErrorVersionConflict, "COLLECTION_CURSOR_STALE", false, errors.New("binding changed"))
	}
	return nil
}

func collectionMembershipBinding() collectionapp.DurableScanBinding {
	return collectionapp.DurableScanBinding{
		WorkspaceID: collectionMemberID(10), CollectionID: collectionMemberID(11), CollectionVersion: 2,
		QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 2,
	}
}

func collectionMemberID(suffix int) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-%012d", suffix))
}

func hasMembershipCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
