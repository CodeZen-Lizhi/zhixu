package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestAppendRequestAcceptsCanonicalConversationCreatedEvent(t *testing.T) {
	conversationID := foundation.ID("7a100000-0000-4000-8000-000000000002")
	request := AppendRequest{
		WorkspaceID:     foundation.ID("7a100000-0000-4000-8000-000000000001"),
		ConversationID:  &conversationID,
		Type:            "conversation.created",
		ResourceRef:     "conversation:" + string(conversationID),
		ResourceVersion: 1,
		PayloadSummary:  PayloadSummary{Status: "open"},
		SchemaVersion:   1,
		SourceEventRef:  "conversation.created:" + string(conversationID) + ":v1",
		OccurredAt:      time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate conversation event: %v", err)
	}
	summary, err := json.Marshal(request.PayloadSummary)
	if err != nil || string(summary) != `{"status":"open"}` {
		t.Fatalf("canonical conversation summary=%s err=%v", summary, err)
	}
}

func TestAppendRequestRejectsMismatchedSummaryBinding(t *testing.T) {
	conversationID := foundation.ID("7a100000-0000-4000-8000-000000000002")
	otherConversationID := foundation.ID("7a100000-0000-4000-8000-000000000003")
	request := AppendRequest{
		WorkspaceID:     foundation.ID("7a100000-0000-4000-8000-000000000001"),
		ConversationID:  &conversationID,
		Type:            "conversation.created",
		ResourceRef:     "conversation:" + string(conversationID),
		ResourceVersion: 1,
		PayloadSummary:  PayloadSummary{ConversationID: &otherConversationID, Status: "open"},
		SchemaVersion:   1,
		SourceEventRef:  "conversation.created:" + string(conversationID) + ":v1",
		OccurredAt:      time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC),
	}
	if err := request.Validate(); errorCode(err) != ErrorCodeAppendInvalid {
		t.Fatalf("mismatched append binding code=%q err=%v", errorCode(err), err)
	}
}
