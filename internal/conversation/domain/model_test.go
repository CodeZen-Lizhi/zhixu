package domain

import (
	"testing"
	"time"

	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestConversationLifecycleAndStableCursors(t *testing.T) {
	now := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	title := "Runtime recovery"
	conversation := Conversation{
		ID: testConversationID, WorkspaceID: testWorkspaceID, Status: ConversationStatusOpen,
		Title: &title, Version: 1, LastActivityAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidateConversation(conversation); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConversationTransition(ConversationStatusOpen, ConversationStatusArchived); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConversationTransition(ConversationStatusArchived, ConversationStatusOpen); errorCode(err) != ErrorCodeConversationTransitionInvalid {
		t.Fatalf("reverse transition err=%v", err)
	}

	invalidTitle := conversation
	invalidTitleValue := " title "
	invalidTitle.Title = &invalidTitleValue
	if err := ValidateConversation(invalidTitle); errorCode(err) != ErrorCodeConversationInvalid {
		t.Fatalf("invalid title err=%v", err)
	}
	archivedWithoutTime := conversation
	archivedWithoutTime.Status = ConversationStatusArchived
	if err := ValidateConversation(archivedWithoutTime); errorCode(err) != ErrorCodeConversationInvalid {
		t.Fatalf("archived without time err=%v", err)
	}
	archiveTime := now.Add(time.Minute)
	archived := conversation
	archived.Status = ConversationStatusArchived
	archived.Version = 2
	archived.UpdatedAt = archiveTime
	archived.ArchivedAt = &archiveTime
	if err := ValidateConversation(archived); err != nil {
		t.Fatalf("valid archived conversation: %v", err)
	}
	archived.Version = 1
	if err := ValidateConversation(archived); errorCode(err) != ErrorCodeConversationInvalid {
		t.Fatalf("version one archived conversation err=%v", err)
	}
	archived.Version = 2
	archived.UpdatedAt = now
	if err := ValidateConversation(archived); errorCode(err) != ErrorCodeConversationInvalid {
		t.Fatalf("archive time drift err=%v", err)
	}

	if err := (ConversationCursor{LastActivityAt: now, ID: testConversationID}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (TurnCursor{Ordinal: 2, QuestionID: "10000000-0000-4000-8000-000000000010"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ConversationCursor{ID: testConversationID}).Validate(); errorCode(err) != ErrorCodeCursorInvalid {
		t.Fatalf("zero time cursor err=%v", err)
	}
	if err := (TurnCursor{Ordinal: 0, QuestionID: "bad"}).Validate(); errorCode(err) != ErrorCodeCursorInvalid {
		t.Fatalf("invalid turn cursor err=%v", err)
	}
	for _, limit := range []int{1, MaxPageLimit} {
		if err := ValidatePageLimit(limit); err != nil {
			t.Fatalf("limit=%d err=%v", limit, err)
		}
	}
	for _, limit := range []int{0, MaxPageLimit + 1} {
		if err := ValidatePageLimit(limit); errorCode(err) != ErrorCodeCursorInvalid {
			t.Fatalf("limit=%d err=%v", limit, err)
		}
	}
}

func TestQuestionFreezesCanonicalRequestAndContextBounds(t *testing.T) {
	request, err := CanonicalizeQuestionRequest(QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID,
		QuestionText: "How does recovery work?", Scope: QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	contextHash, _, _, err := ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	question := Question{
		ID: "10000000-0000-4000-8000-000000000010", Request: request, Ordinal: 1,
		ContextThroughOrdinal: 0, ContextHash: contextHash, RequestHash: requestHash,
		CreatedAt: time.Date(2026, 7, 19, 9, 1, 0, 0, time.UTC),
	}
	if err := ValidateQuestion(question); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Question)
	}{
		{name: "non canonical request", mutate: func(value *Question) { value.Request.QuestionText = " question " }},
		{name: "wrong request hash", mutate: func(value *Question) { value.RequestHash = contextHash }},
		{name: "context reaches current question", mutate: func(value *Question) { value.ContextThroughOrdinal = 1 }},
		{name: "invalid context hash", mutate: func(value *Question) { value.ContextHash = "BAD" }},
		{name: "identity reused", mutate: func(value *Question) { value.ID = value.Request.ConversationID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := question
			test.mutate(&value)
			if err := ValidateQuestion(value); errorCode(err) != ErrorCodeQuestionInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
