package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestFeedbackCanonicalizationValidatesCitationAndStableHash(t *testing.T) {
	answer := validCompletedAnswer(t)
	helpful := FeedbackRequest{
		WorkspaceID: testWorkspaceID, AnswerID: answer.ID, Type: FeedbackHelpful,
	}
	canonical, err := CanonicalizeFeedbackRequest(helpful, answer)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ComputeFeedbackRequestHash(helpful, answer)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "aa4a235032f4c157fbd1d4498e639c372a3bcd6b129815357535ac35bebf7cf8" || canonical.Type != FeedbackHelpful {
		t.Fatalf("canonical=%#v hash=%s", canonical, hash)
	}

	citationID := "citation-1"
	comment := "The cited span is unavailable."
	citationFeedback := FeedbackRequest{
		WorkspaceID: testWorkspaceID, AnswerID: answer.ID, Type: FeedbackBrokenCitation,
		CitationID: &citationID, Comment: &comment,
	}
	if _, err := CanonicalizeFeedbackRequest(citationFeedback, answer); err != nil {
		t.Fatalf("citation feedback: %v", err)
	}

	tests := []struct {
		name   string
		answer Answer
		value  FeedbackRequest
	}{
		{name: "missing citation", answer: answer, value: func() FeedbackRequest { value := citationFeedback; value.CitationID = nil; return value }()},
		{name: "unknown citation", answer: answer, value: func() FeedbackRequest {
			value := citationFeedback
			missing := "missing"
			value.CitationID = &missing
			return value
		}()},
		{name: "citation on helpful", answer: answer, value: func() FeedbackRequest { value := helpful; value.CitationID = &citationID; return value }()},
		{name: "pending answer", answer: func() Answer { value := answer; value = pendingAnswerFrom(value); return value }(), value: helpful},
		{name: "clarification answer", answer: validClarificationAnswer(t), value: helpful},
		{name: "nul comment", answer: answer, value: func() FeedbackRequest {
			value := helpful
			invalid := "bad\x00comment"
			value.Comment = &invalid
			return value
		}()},
		{name: "oversized comment", answer: answer, value: func() FeedbackRequest {
			value := helpful
			invalid := strings.Repeat("x", MaxFeedbackCommentBytes+1)
			value.Comment = &invalid
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CanonicalizeFeedbackRequest(test.value, test.answer); errorCode(err) != ErrorCodeFeedbackInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}

	feedback := AnswerFeedback{
		ID: "10000000-0000-4000-8000-000000000040", Request: canonical, RequestHash: hash,
		CreatedAt: time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC),
	}
	if err := ValidateAnswerFeedback(feedback, answer); err != nil {
		t.Fatal(err)
	}
	feedback.RequestHash = strings.Repeat("f", 64)
	if err := ValidateAnswerFeedback(feedback, answer); errorCode(err) != ErrorCodeFeedbackInvalid {
		t.Fatalf("wrong hash err=%v", err)
	}
}

func validClarificationAnswer(t *testing.T) Answer {
	t.Helper()
	result, err := CanonicalizePublishedResult(AnswerResultClarification, []byte(`{
		"result_type":"clarification","schema_id":"conversation.clarification","schema_version":"v1",
		"model_run_ref":"10000000-0000-4000-8000-000000000006",
		"payload":{"reason":"scope is ambiguous","question":"Which environment?","suggested_scopes":[]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := CanonicalizeRetrievalSummary(testWorkspaceID, RetrievalSummary{
		Rewrites: []string{}, RequestedMode: retrievaldomain.SearchModeHybrid, EffectiveMode: retrievaldomain.SearchModeHybrid,
		Scope:        RetrievalScopeSummary{SourceIDs: []foundation.ID{}, SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{}},
		Degradations: []RetrievalDegradation{},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	publishedAt := now.Add(time.Second)
	answer := Answer{
		ID: "10000000-0000-4000-8000-000000000030", WorkspaceID: testWorkspaceID,
		ConversationID: testConversationID, QuestionID: "10000000-0000-4000-8000-000000000010",
		WorkflowRunID: "10000000-0000-4000-8000-000000000031", ModelRunID: &result.ModelRunID,
		PublicationStatus: AnswerPublicationClarificationRequired, ResultType: result.Type,
		Result: result.Document, ResultHash: result.Hash, RetrievalSummary: &summary,
		Version: 2, CreatedAt: now, UpdatedAt: publishedAt, PublishedAt: &publishedAt,
	}
	if err := ValidateAnswer(answer); err != nil {
		t.Fatalf("clarification answer: %v", err)
	}
	return answer
}

func validCompletedAnswer(t *testing.T) Answer {
	t.Helper()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	result := validRAGPublishedResult(t)
	summary := validCompletedRetrievalSummary(t)
	publishedAt := now.Add(time.Second)
	return Answer{
		ID: "10000000-0000-4000-8000-000000000030", WorkspaceID: testWorkspaceID,
		ConversationID: testConversationID, QuestionID: "10000000-0000-4000-8000-000000000010",
		WorkflowRunID: "10000000-0000-4000-8000-000000000031", ModelRunID: &result.ModelRunID,
		PublicationStatus: AnswerPublicationCompleted, ResultType: result.Type, Result: result.Document, ResultHash: result.Hash,
		RetrievalSummary: &summary, Version: 2, CreatedAt: now, UpdatedAt: publishedAt, PublishedAt: &publishedAt,
	}
}

func pendingAnswerFrom(value Answer) Answer {
	value.ModelRunID = nil
	value.PublicationStatus = AnswerPublicationPending
	value.ResultType = ""
	value.Result = nil
	value.ResultHash = ""
	value.RetrievalSummary = nil
	value.Version = 1
	value.UpdatedAt = value.CreatedAt
	value.PublishedAt = nil
	return value
}
