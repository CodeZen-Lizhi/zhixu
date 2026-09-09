package conversationhttp

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCursorCodecRoundTripsAndBindsScope(t *testing.T) {
	t.Parallel()
	codec := NewCursorCodec()
	workspaceID := foundation.ID("11111111-1111-4111-8111-111111111111")
	otherWorkspaceID := foundation.ID("22222222-2222-4222-8222-222222222222")
	boundary := conversationdomain.ConversationCursor{LastActivityAt: time.Date(2026, 7, 20, 1, 2, 3, 4, time.UTC), ID: foundation.ID("33333333-3333-4333-8333-333333333333")}

	raw, err := codec.encodeConversation(workspaceID, boundary)
	if err != nil {
		t.Fatalf("encode conversation cursor: %v", err)
	}
	decoded, err := codec.decodeConversation(raw, workspaceID)
	if err != nil || *decoded != boundary {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
	if _, err := codec.decodeConversation(raw, otherWorkspaceID); err == nil {
		t.Fatal("expected cross-workspace cursor rejection")
	}
	if _, err := codec.decodeConversation(raw+"x", workspaceID); err == nil {
		t.Fatal("expected tampered cursor rejection")
	}
}

func TestTurnCursorBindsConversation(t *testing.T) {
	t.Parallel()
	codec := NewCursorCodec()
	workspaceID := foundation.ID("11111111-1111-4111-8111-111111111111")
	conversationID := foundation.ID("22222222-2222-4222-8222-222222222222")
	otherConversationID := foundation.ID("33333333-3333-4333-8333-333333333333")
	boundary := conversationdomain.TurnCursor{Ordinal: 7, QuestionID: foundation.ID("44444444-4444-4444-8444-444444444444")}

	raw, err := codec.encodeTurn(workspaceID, conversationID, boundary, application.APIVersionV1)
	if err != nil {
		t.Fatalf("encode turn cursor: %v", err)
	}
	decoded, err := codec.decodeTurn(raw, workspaceID, conversationID, application.APIVersionV1)
	if err != nil || *decoded != boundary {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
	if _, err := codec.decodeTurn(raw, workspaceID, otherConversationID, application.APIVersionV1); err == nil {
		t.Fatal("expected cross-conversation cursor rejection")
	}
}
