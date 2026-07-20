package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestServiceSubmitFeedbackCanonicalizesAndValidatesRepositoryResult(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	answer := feedbackTestAnswer(t, now)
	repository := &feedbackServiceRepository{recordingRepository: &recordingRepository{}, answer: AnswerView{
		Answer: answer, Workflow: WorkflowRunView{RunID: answer.WorkflowRunID, Status: workflowdomain.RunStatusSucceeded, Version: 2, UpdatedAt: *answer.PublishedAt},
		AssistantText: "No approved evidence was found.", Citations: []agentdomain.Citation{},
	}}
	feedbacks := &recordingFeedbackRepository{}
	service, err := NewService(Dependencies{
		Repository: repository, FeedbackRepository: feedbacks, IDs: fixedIDGenerator{id: conversationApplicationID(95)},
		Clock: foundation.FixedClock{Value: now.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	comment := "  Needs another source.  "
	feedbacks.result = SubmitFeedbackResult{Feedback: conversationdomain.AnswerFeedback{
		ID: conversationApplicationID(95),
		Request: conversationdomain.FeedbackRequest{
			WorkspaceID: answer.WorkspaceID, AnswerID: answer.ID, Type: conversationdomain.FeedbackMissingSource,
			Comment: stringPointer("Needs another source."),
		},
		CreatedAt: now.Add(time.Minute),
	}}
	feedbacks.result.Feedback.RequestHash, err = conversationdomain.ComputeFeedbackRequestHash(feedbacks.result.Feedback.Request, answer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitFeedback(context.Background(), SubmitFeedbackCommand{
		Request: conversationdomain.FeedbackRequest{
			WorkspaceID: answer.WorkspaceID, AnswerID: answer.ID, Type: conversationdomain.FeedbackMissingSource, Comment: &comment,
		},
		IdempotencyKey: "  feedback-command-1  ",
	})
	if err != nil || result.Feedback.Request.Comment == nil || *result.Feedback.Request.Comment != "Needs another source." {
		t.Fatalf("SubmitFeedback() = %#v, %v", result, err)
	}
	if feedbacks.record.IdempotencyKey != "feedback-command-1" || feedbacks.record.Feedback.RequestHash == "" {
		t.Fatalf("record = %#v", feedbacks.record)
	}
}

type feedbackServiceRepository struct {
	*recordingRepository
	answer AnswerView
}

func (repository *feedbackServiceRepository) GetAnswer(context.Context, foundation.ID, foundation.ID) (AnswerView, error) {
	return repository.answer, nil
}

type recordingFeedbackRepository struct {
	record RecordFeedbackRecord
	result SubmitFeedbackResult
	err    error
}

func (repository *recordingFeedbackRepository) RecordFeedback(_ context.Context, record RecordFeedbackRecord) (SubmitFeedbackResult, error) {
	repository.record = record
	return repository.result, repository.err
}

func feedbackTestAnswer(t *testing.T, now time.Time) conversationdomain.Answer {
	t.Helper()
	workspaceID := conversationApplicationID(90)
	modelRunID := conversationApplicationID(94)
	raw, err := json.Marshal(agentdomain.RefusalResult{
		ResultType: agentdomain.ResultTypeRefusal, SchemaID: agentdomain.RefusalSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunID,
		Payload: agentdomain.RefusalPayload{
			ReasonCode: agentdomain.RefusalNoRelevantEvidence, Summary: "No approved evidence was found.",
			RetrievalScope: "approved knowledge", MissingRequirements: []string{"approved evidence"}, SuggestedActions: []string{"add a source"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := conversationdomain.CanonicalizePublishedResult(conversationdomain.AnswerResultRefusal, raw)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := conversationdomain.CanonicalizeRetrievalSummary(workspaceID, conversationdomain.RetrievalSummary{
		Rewrites: []string{}, RequestedMode: retrievaldomain.SearchModeHybrid, EffectiveMode: retrievaldomain.SearchModeHybrid,
		Scope:        conversationdomain.RetrievalScopeSummary{SourceIDs: []foundation.ID{}, SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{}},
		Degradations: []conversationdomain.RetrievalDegradation{},
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := now.Add(time.Second)
	return conversationdomain.Answer{
		ID: conversationApplicationID(91), WorkspaceID: workspaceID, ConversationID: conversationApplicationID(92),
		QuestionID: conversationApplicationID(93), WorkflowRunID: conversationApplicationID(96), ModelRunID: &modelRunID,
		PublicationStatus: conversationdomain.AnswerPublicationRefused, ResultType: result.Type, Result: result.Document,
		ResultHash: result.Hash, RetrievalSummary: &summary, Version: 2, CreatedAt: now, UpdatedAt: publishedAt, PublishedAt: &publishedAt,
	}
}
