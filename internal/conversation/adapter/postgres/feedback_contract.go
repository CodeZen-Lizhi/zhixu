package postgres

import (
	"errors"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
)

const feedbackColumns = `
	id::text,workspace_id::text,answer_id::text,feedback_type,citation_id,comment,idempotency_key,request_hash,created_at`

type feedbackRecord struct {
	Feedback       conversationdomain.AnswerFeedback
	IdempotencyKey string
}

func scanFeedbackRecord(row scanner, answer conversationdomain.Answer) (feedbackRecord, error) {
	var record feedbackRecord
	var id, workspaceID, answerID, feedbackType string
	if err := row.Scan(&id, &workspaceID, &answerID, &feedbackType, &record.Feedback.Request.CitationID,
		&record.Feedback.Request.Comment, &record.IdempotencyKey, &record.Feedback.RequestHash, &record.Feedback.CreatedAt); err != nil {
		return feedbackRecord{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	parsedID, err := parseCanonicalID(id)
	if err != nil {
		return feedbackRecord{}, consistency(ErrorCodeFeedbackPersistenceCorrupt, err)
	}
	parsedWorkspaceID, err := parseCanonicalID(workspaceID)
	if err != nil {
		return feedbackRecord{}, consistency(ErrorCodeFeedbackPersistenceCorrupt, err)
	}
	parsedAnswerID, err := parseCanonicalID(answerID)
	if err != nil {
		return feedbackRecord{}, consistency(ErrorCodeFeedbackPersistenceCorrupt, err)
	}
	record.Feedback.ID = parsedID
	record.Feedback.Request.WorkspaceID = parsedWorkspaceID
	record.Feedback.Request.AnswerID = parsedAnswerID
	record.Feedback.Request.Type = conversationdomain.FeedbackType(feedbackType)
	if !canonicalIdempotencyKey(record.IdempotencyKey) {
		return feedbackRecord{}, consistency(ErrorCodeFeedbackPersistenceCorrupt, errors.New("persisted feedback idempotency key is invalid"))
	}
	if err := conversationdomain.ValidateAnswerFeedback(record.Feedback, answer); err != nil {
		return feedbackRecord{}, consistency(ErrorCodeFeedbackPersistenceCorrupt, err)
	}
	return record, nil
}

func sameFeedbackRequest(left, right conversationdomain.FeedbackRequest) bool {
	return left.WorkspaceID == right.WorkspaceID && left.AnswerID == right.AnswerID && left.Type == right.Type &&
		sameOptionalString(left.CitationID, right.CitationID) && sameOptionalString(left.Comment, right.Comment)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
