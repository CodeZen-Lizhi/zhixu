package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
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

func TestServiceSubmitQuestionCanonicalizesRequestAndValidatesResult(t *testing.T) {
	t.Parallel()

	workspaceID := conversationApplicationID(10)
	conversationID := conversationApplicationID(11)
	questionID := conversationApplicationID(12)
	answerID := conversationApplicationID(13)
	runID := conversationApplicationID(14)
	nodeID := conversationApplicationID(15)
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	contextHash, _, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: "What changed?",
		Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(canonical)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &recordingQuestionDispatcher{result: SubmitQuestionResult{
		Question: conversationdomain.Question{
			ID: questionID, Request: canonical, Ordinal: 1, ContextThroughOrdinal: 0,
			ContextHash: contextHash, RequestHash: requestHash, CreatedAt: now,
		},
		Answer: conversationdomain.Answer{
			ID: answerID, WorkspaceID: workspaceID, ConversationID: conversationID, QuestionID: questionID,
			WorkflowRunID: runID, PublicationStatus: conversationdomain.AnswerPublicationPending,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Workflow:  WorkflowRunView{RunID: runID, Status: workflowdomain.RunStatusPending, Version: 1, UpdatedAt: now},
		NodeRunID: nodeID, JobID: 42,
	}}
	service, err := NewService(Dependencies{
		Repository:         &recordingRepository{},
		QuestionDispatcher: dispatcher,
		IDs:                fixedIDGenerator{id: conversationApplicationID(16)},
		Clock:              foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.SubmitQuestion(context.Background(), SubmitQuestionCommand{
		Request: conversationdomain.QuestionRequest{
			WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: "  What changed?  ",
		},
		IdempotencyKey: "  question-command-1  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer.ID != answerID || result.JobID != 42 || result.Replayed {
		t.Fatalf("result = %#v", result)
	}
	if dispatcher.record.Request.QuestionText != "What changed?" || dispatcher.record.Request.Scope.RetrievalMode != retrievaldomain.SearchModeHybrid ||
		dispatcher.record.Request.AnswerDepth != conversationdomain.AnswerDepthStandard || dispatcher.record.Request.OutputFormat != conversationdomain.OutputFormatMarkdown ||
		dispatcher.record.IdempotencyKey != "question-command-1" || len(dispatcher.record.RequestHash) != 64 {
		t.Fatalf("dispatch record = %#v", dispatcher.record)
	}
	if strings.Contains(dispatcher.record.RequestHash, "What changed?") {
		t.Fatalf("request hash leaked question text: %q", dispatcher.record.RequestHash)
	}
	dispatcher.result.NodeRunID = workspaceID
	_, err = service.SubmitQuestion(context.Background(), SubmitQuestionCommand{
		Request: canonical, IdempotencyKey: "question-command-1",
	})
	requireConversationApplicationError(t, err, foundation.ErrorConsistencyViolation, errorCodeResultInconsistent)
	dispatcher.result.NodeRunID = nodeID
	dispatcher.result.Replayed = true
	dispatcher.result.Workflow.Status = workflowdomain.RunStatusFailed
	replayed, err := service.SubmitQuestion(context.Background(), SubmitQuestionCommand{
		Request: canonical, IdempotencyKey: "question-command-1",
	})
	if err != nil || !replayed.Replayed || replayed.Workflow.Status != workflowdomain.RunStatusFailed ||
		replayed.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		t.Fatalf("SubmitQuestion(terminal workflow replay) = %#v, %v", replayed, err)
	}
}

func TestServiceSubmitQuestionPropagatesFailureAndRejectsResultDrift(t *testing.T) {
	t.Parallel()
	workspaceID := conversationApplicationID(30)
	conversationID := conversationApplicationID(31)
	now := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: "Which facts are bound?",
		Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := validSubmitQuestionResult(t, request, now)
	injected := foundation.NewError(foundation.ErrorRetryableFailure, "TEST_QUESTION_DISPATCH_RETRY", true, errors.New("retry dispatch"))
	tests := []struct {
		name      string
		configure func(*recordingQuestionDispatcher)
		wantCause error
	}{
		{
			name: "dispatcher failure is preserved",
			configure: func(dispatcher *recordingQuestionDispatcher) {
				dispatcher.err = injected
			},
			wantCause: injected,
		},
		{
			name: "answer question binding drift",
			configure: func(dispatcher *recordingQuestionDispatcher) {
				dispatcher.result.Answer.QuestionID = conversationApplicationID(40)
			},
		},
		{
			name: "workflow binding drift",
			configure: func(dispatcher *recordingQuestionDispatcher) {
				dispatcher.result.Workflow.RunID = conversationApplicationID(41)
			},
		},
		{
			name: "new dispatch terminal workflow",
			configure: func(dispatcher *recordingQuestionDispatcher) {
				dispatcher.result.Workflow.Status = workflowdomain.RunStatusFailed
			},
		},
		{
			name: "missing job receipt",
			configure: func(dispatcher *recordingQuestionDispatcher) {
				dispatcher.result.JobID = 0
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &recordingQuestionDispatcher{result: base}
			test.configure(dispatcher)
			service, err := NewService(Dependencies{
				Repository: &recordingRepository{}, QuestionDispatcher: dispatcher,
				IDs: fixedIDGenerator{id: conversationApplicationID(42)}, Clock: foundation.FixedClock{Value: now},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.SubmitQuestion(context.Background(), SubmitQuestionCommand{
				Request: request, IdempotencyKey: "question-result-drift",
			})
			if test.wantCause != nil {
				if !errors.Is(err, test.wantCause) {
					t.Fatalf("SubmitQuestion() error = %#v", err)
				}
				return
			}
			requireConversationApplicationError(t, err, foundation.ErrorConsistencyViolation, errorCodeResultInconsistent)
		})
	}
}

func TestServiceSubmitQuestionFailsClosedWithoutDispatcher(t *testing.T) {
	t.Parallel()
	service, err := NewService(Dependencies{
		Repository: &recordingRepository{}, IDs: fixedIDGenerator{id: conversationApplicationID(20)}, Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitQuestion(context.Background(), SubmitQuestionCommand{})
	requireConversationApplicationError(t, err, foundation.ErrorDependencyUnavailable, errorCodeSubmissionUnavailable)
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

type recordingQuestionDispatcher struct {
	record SubmitQuestionRecord
	result SubmitQuestionResult
	err    error
}

func (dispatcher *recordingQuestionDispatcher) SubmitQuestion(_ context.Context, record SubmitQuestionRecord) (SubmitQuestionResult, error) {
	dispatcher.record = record
	return dispatcher.result, dispatcher.err
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

func validSubmitQuestionResult(t *testing.T, request conversationdomain.QuestionRequest, now time.Time) SubmitQuestionResult {
	t.Helper()
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	contextHash, _, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	questionID := conversationApplicationID(32)
	answerID := conversationApplicationID(33)
	runID := conversationApplicationID(34)
	return SubmitQuestionResult{
		Question: conversationdomain.Question{
			ID: questionID, Request: request, Ordinal: 1, ContextThroughOrdinal: 0,
			ContextHash: contextHash, RequestHash: requestHash, CreatedAt: now,
		},
		Answer: conversationdomain.Answer{
			ID: answerID, WorkspaceID: request.WorkspaceID, ConversationID: request.ConversationID,
			QuestionID: questionID, WorkflowRunID: runID, PublicationStatus: conversationdomain.AnswerPublicationPending,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Workflow: WorkflowRunView{
			RunID: runID, Status: workflowdomain.RunStatusPending, Version: 1, UpdatedAt: now,
		},
		NodeRunID: conversationApplicationID(35), JobID: 43,
	}
}

func requireConversationApplicationError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("application error = %#v, want kind=%s code=%s", err, kind, code)
	}
}
