package application

import (
	"context"
	"testing"
	"time"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestServiceAPIVersionIsDispatchConstraintAndNeverQuestionIdentity(t *testing.T) {
	t.Parallel()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: conversationApplicationID(301), ConversationID: conversationApplicationID(302), QuestionText: "同一个问题",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	result := validSubmitQuestionResult(t, request, now)
	result.Workflow.DefinitionKey, result.Workflow.DefinitionVersion = conversationworkflow.DefinitionKey, 2
	dispatcher := &recordingQuestionDispatcher{result: result}
	service, err := NewService(Dependencies{Repository: &recordingRepository{}, QuestionDispatcher: dispatcher, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	var originalHash string
	for _, version := range []APIVersion{APIVersionV1, APIVersionV2} {
		got, err := service.SubmitQuestion(context.Background(), SubmitQuestionCommand{Request: request, IdempotencyKey: "shared-key", APIVersion: version})
		if err != nil || got.Question.ID != result.Question.ID || got.Workflow.RunID != result.Workflow.RunID ||
			dispatcher.record.APIVersion != version || dispatcher.record.IdempotencyKey != "shared-key" {
			t.Fatalf("dispatch lost version or identity: result=%#v record=%#v err=%v", got, dispatcher.record, err)
		}
		if originalHash != "" && dispatcher.record.RequestHash != originalHash {
			t.Fatal("HTTP representation version changed durable request hash")
		}
		originalHash = dispatcher.record.RequestHash
		dispatcher.result.Replayed = true
	}
	dispatcher.record = SubmitQuestionRecord{}
	_, err = service.SubmitQuestion(context.Background(), SubmitQuestionCommand{Request: request, IdempotencyKey: "shared-key", APIVersion: 3})
	requireConversationApplicationError(t, err, foundation.ErrorInvalidInput, errorCodeRequestInvalid)
	if dispatcher.record.IdempotencyKey != "" {
		t.Fatal("invalid public version reached dispatch")
	}
}

func TestServiceAPIVersionChecksRepositoryTurnProjectionAndLatestConstraint(t *testing.T) {
	t.Parallel()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: conversationApplicationID(303), ConversationID: conversationApplicationID(304), QuestionText: "动态分析", Mode: conversationdomain.QuestionModeWorkspaceAnalysis,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	result := validSubmitQuestionResult(t, request, now)
	result.Workflow.DefinitionKey, result.Workflow.DefinitionVersion = conversationworkflow.WorkspaceAnalysisDefinitionKey, 2
	repository := &apiVersionTurnRepository{page: TurnPage{Items: []TurnView{{Question: result.Question, Answer: &AnswerView{Answer: result.Answer, Workflow: result.Workflow}}}}}
	service, err := NewService(Dependencies{Repository: repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	for _, latest := range []bool{false, true} {
		query := ListTurnsQuery{WorkspaceID: request.WorkspaceID, ConversationID: request.ConversationID, Limit: 1, Latest: latest, APIVersion: APIVersionV1}
		_, err := service.ListTurns(context.Background(), query)
		requireConversationApplicationError(t, err, foundation.ErrorVersionConflict, ErrorCodeAPIVersionUnsupported)
		if repository.query.APIVersion != APIVersionV1 || repository.query.Latest != latest {
			t.Fatalf("repository did not receive the full visibility constraint: %#v", repository.query)
		}
		query.APIVersion = APIVersionV2
		page, err := service.ListTurns(context.Background(), query)
		if err != nil || len(page.Items) != 1 || page.Items[0].Answer.Answer.ID != result.Answer.ID {
			t.Fatalf("v2 lost the same dynamic answer: %#v err=%v", page, err)
		}
	}
}

type apiVersionTurnRepository struct {
	recordingRepository
	query ListTurnsQuery
	page  TurnPage
}

func (repository *apiVersionTurnRepository) ListTurns(_ context.Context, query ListTurnsQuery) (TurnPage, error) {
	repository.query = query
	return repository.page, nil
}
