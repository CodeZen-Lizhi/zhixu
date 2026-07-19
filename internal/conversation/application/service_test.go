package application

import (
	"context"
	"testing"
	"time"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestServiceCreateConversationCanonicalizesCommand(t *testing.T) {
	t.Parallel()

	workspaceID := conversationApplicationID(1)
	conversationID := conversationApplicationID(2)
	now := time.Date(2026, 7, 19, 12, 0, 0, 123000, time.UTC)
	repository := &recordingRepository{}
	repository.createResult = CreateConversationResult{Conversation: conversationdomain.Conversation{
		ID: conversationID, WorkspaceID: workspaceID, Status: conversationdomain.ConversationStatusOpen,
		Title: stringPointer("Research notes"), Version: 1,
		LastActivityAt: now, CreatedAt: now, UpdatedAt: now,
	}}
	service, err := NewService(Dependencies{
		Repository: repository,
		IDs:        fixedIDGenerator{id: conversationID},
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.CreateConversation(context.Background(), CreateConversationCommand{
		WorkspaceID:    workspaceID,
		Title:          stringPointer("  Research notes  "),
		IdempotencyKey: "  conversation-create-1  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Conversation.ID != conversationID || result.Replayed {
		t.Fatalf("result = %#v", result)
	}
	if repository.createRecord.Conversation.Title == nil || *repository.createRecord.Conversation.Title != "Research notes" {
		t.Fatalf("canonical title = %#v", repository.createRecord.Conversation.Title)
	}
	if repository.createRecord.IdempotencyKey != "conversation-create-1" || len(repository.createRecord.RequestHash) != 64 {
		t.Fatalf("create metadata = %#v", repository.createRecord)
	}
	if repository.createRecord.Conversation.ID != conversationID || !repository.createRecord.Conversation.CreatedAt.Equal(now) {
		t.Fatalf("conversation candidate = %#v", repository.createRecord.Conversation)
	}
}

type fixedIDGenerator struct {
	id  foundation.ID
	err error
}

func (generator fixedIDGenerator) New() (foundation.ID, error) {
	return generator.id, generator.err
}

type recordingRepository struct {
	createRecord CreateConversationRecord
	createResult CreateConversationResult
	createErr    error
}

func (repository *recordingRepository) CreateConversation(_ context.Context, record CreateConversationRecord) (CreateConversationResult, error) {
	repository.createRecord = record
	return repository.createResult, repository.createErr
}

func (repository *recordingRepository) ListConversations(context.Context, ListConversationsQuery) (ConversationPage, error) {
	return ConversationPage{}, nil
}

func (repository *recordingRepository) GetConversation(context.Context, foundation.ID, foundation.ID) (conversationdomain.Conversation, error) {
	return conversationdomain.Conversation{}, nil
}

func (repository *recordingRepository) ListTurns(context.Context, ListTurnsQuery) (TurnPage, error) {
	return TurnPage{}, nil
}

func (repository *recordingRepository) GetAnswer(context.Context, foundation.ID, foundation.ID) (AnswerView, error) {
	return AnswerView{}, nil
}

func (repository *recordingRepository) LoadPublishedContext(context.Context, PublishedContextQuery) ([]conversationdomain.PublishedTurn, error) {
	return nil, nil
}

func conversationApplicationID(value int) foundation.ID {
	return foundation.ID("71000000-0000-4000-8000-" + leftPad12(value))
}

func leftPad12(value int) string {
	const digits = "000000000000"
	raw := ""
	for value > 0 {
		raw = string(rune('0'+value%10)) + raw
		value /= 10
	}
	return digits[:len(digits)-len(raw)] + raw
}

func stringPointer(value string) *string {
	return &value
}
