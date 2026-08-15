package eventcontract

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestProposalStatusRequestUsesStableResourceAndSourceBinding(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	proposalID := foundation.ID("10000000-0000-4000-8000-000000000002")
	sourceID := foundation.ID("10000000-0000-4000-8000-000000000003")
	at := time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC)
	request := ProposalStatusRequest(workspaceID, proposalID, sourceID, ProposalAppliedEventType, "applied", 4, at)
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if request.ResourceRef != "proposal:"+string(proposalID) || request.ResourceVersion != 4 || request.SourceEventRef != ProposalAppliedEventType+":"+string(sourceID)+":v1" || !request.OccurredAt.Equal(at) {
		t.Fatalf("request=%#v", request)
	}
	if request.PayloadSummary.SourceEventID == nil || *request.PayloadSummary.SourceEventID != sourceID || request.PayloadSummary.Status != "applied" {
		t.Fatalf("payload=%#v", request.PayloadSummary)
	}
	if request.Type != ProposalAppliedEventType || request.SchemaVersion != 1 {
		t.Fatalf("event type/schema=%s/%d", request.Type, request.SchemaVersion)
	}
}

func TestProposalRevisedRequestContainsOnlyStableSummary(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	proposalID := foundation.ID("10000000-0000-4000-8000-000000000002")
	revisionID := foundation.ID("10000000-0000-4000-8000-000000000004")
	at := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	request := ProposalStatusRequest(workspaceID, proposalID, revisionID, ProposalRevisedEventType, "ready_for_review", 2, at)
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if request.Type != ProposalRevisedEventType || request.SourceEventRef != ProposalRevisedEventType+":"+string(revisionID)+":v1" || request.PayloadSummary.Status != "ready_for_review" {
		t.Fatalf("request=%#v", request)
	}
}
