//go:build integration

package postgres

import (
	"testing"
	"time"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRepositoryRecordsFeedbackWithExactReplayAndImmutableProductFacts(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(800)
	otherWorkspaceID := conversationTurnID(801)
	seedConversationWorkspaces(t, ctx, pool, workspaceID, otherWorkspaceID)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	conversationRecord := conversationCreateRecord(t, workspaceID, conversationTurnID(802), "Feedback", "feedback-conversation", now)
	if _, err := repository.CreateConversation(ctx, conversationRecord); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversationRecord.Conversation.ID, 9000, now)
	seedPublishedTurn(t, ctx, pool, fixture, 1, conversationdomain.AnswerPublicationCompleted, "Question", "Approved answer", now.Add(time.Minute))
	answerID := fixture.answerID(1)
	answerBefore, err := repository.GetAnswer(ctx, workspaceID, answerID)
	if err != nil {
		t.Fatal(err)
	}
	var knowledgeBefore int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.claim`).Scan(&knowledgeBefore); err != nil {
		t.Fatal(err)
	}

	citationID := "citation-1"
	comment := "  Citation cannot be opened.  "
	request, err := conversationdomain.CanonicalizeFeedbackRequest(conversationdomain.FeedbackRequest{
		WorkspaceID: workspaceID, AnswerID: answerID, Type: conversationdomain.FeedbackBrokenCitation,
		CitationID: &citationID, Comment: &comment,
	}, answerBefore.Answer)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := conversationdomain.ComputeFeedbackRequestHash(request, answerBefore.Answer)
	if err != nil {
		t.Fatal(err)
	}
	record := conversationapplication.RecordFeedbackRecord{
		Feedback: conversationdomain.AnswerFeedback{
			ID: conversationTurnID(899), Request: request, RequestHash: hash, CreatedAt: now.Add(2 * time.Minute),
		},
		IdempotencyKey: "feedback-1",
	}
	created, err := repository.RecordFeedback(ctx, record)
	if err != nil || created.Replayed || created.Feedback.ID != record.Feedback.ID || created.Feedback.Request.Comment == nil || *created.Feedback.Request.Comment != "Citation cannot be opened." {
		t.Fatalf("RecordFeedback() = %#v, %v", created, err)
	}
	record.Feedback.ID = conversationTurnID(898)
	replayed, err := repository.RecordFeedback(ctx, record)
	if err != nil || !replayed.Replayed || replayed.Feedback.ID != created.Feedback.ID {
		t.Fatalf("RecordFeedback(replay) = %#v, %v", replayed, err)
	}
	conflictRequest := request
	conflictRequest.Type = conversationdomain.FeedbackIrrelevantCitation
	conflictHash, err := conversationdomain.ComputeFeedbackRequestHash(conflictRequest, answerBefore.Answer)
	if err != nil {
		t.Fatal(err)
	}
	record.Feedback.Request = conflictRequest
	record.Feedback.RequestHash = conflictHash
	_, err = repository.RecordFeedback(ctx, record)
	requireConversationRepositoryError(t, err, foundation.ErrorVersionConflict, ErrorCodeFeedbackIdempotencyConflict)

	crossWorkspace := record
	crossWorkspace.IdempotencyKey = "feedback-cross-workspace"
	crossWorkspace.Feedback.Request.WorkspaceID = otherWorkspaceID
	_, err = repository.RecordFeedback(ctx, crossWorkspace)
	requireConversationRepositoryError(t, err, foundation.ErrorNotFound, ErrorCodeAnswerNotFound)

	answerAfter, err := repository.GetAnswer(ctx, workspaceID, answerID)
	if err != nil {
		t.Fatal(err)
	}
	var knowledgeAfter, feedbackCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.claim`).Scan(&knowledgeAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.answer_feedback WHERE answer_id=$1`, string(answerID)).Scan(&feedbackCount); err != nil {
		t.Fatal(err)
	}
	if feedbackCount != 1 || knowledgeAfter != knowledgeBefore || answerAfter.Answer.ResultHash != answerBefore.Answer.ResultHash ||
		answerAfter.Answer.Version != answerBefore.Answer.Version {
		t.Fatalf("mutation drift: feedback=%d knowledge=%d/%d answer=%#v/%#v", feedbackCount, knowledgeBefore, knowledgeAfter, answerBefore.Answer, answerAfter.Answer)
	}
}
