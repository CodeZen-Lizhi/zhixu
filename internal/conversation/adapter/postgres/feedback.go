package postgres

import (
	"context"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/jackc/pgx/v5"
)

const feedbackColumns = `
	id::text,workspace_id::text,answer_id::text,feedback_type,citation_id,comment,idempotency_key,request_hash,created_at`

type feedbackRecord struct {
	Feedback       conversationdomain.AnswerFeedback
	IdempotencyKey string
}

// RecordFeedback 在 Answer 范围内追加或精确重放一个 Feedback 事实。
func (repository *Repository) RecordFeedback(ctx context.Context, record conversationapplication.RecordFeedbackRecord) (conversationapplication.SubmitFeedbackResult, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationapplication.SubmitFeedbackResult{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if ctx == nil || !canonicalIdempotencyKey(record.IdempotencyKey) {
		return conversationapplication.SubmitFeedbackResult{}, invalid(ErrorCodeFeedbackPersistenceInvalid, errors.New("feedback context or idempotency key is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return conversationapplication.SubmitFeedbackResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	answer, err := scanAnswerView(tx.QueryRow(ctx, `SELECT `+answerViewColumns+`
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.id=$2
		FOR UPDATE OF a`, string(record.Feedback.Request.WorkspaceID), string(record.Feedback.Request.AnswerID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.SubmitFeedbackResult{}, notFound(ErrorCodeAnswerNotFound, err)
	}
	if err != nil {
		return conversationapplication.SubmitFeedbackResult{}, err
	}
	if err := conversationdomain.ValidateAnswerFeedback(record.Feedback, answer.Answer); err != nil {
		return conversationapplication.SubmitFeedbackResult{}, invalid(ErrorCodeFeedbackPersistenceInvalid, err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`, string(record.Feedback.Request.AnswerID), record.IdempotencyKey); err != nil {
		return conversationapplication.SubmitFeedbackResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}

	existing, err := scanFeedbackRecord(tx.QueryRow(ctx, `SELECT `+feedbackColumns+`
		FROM agent.answer_feedback WHERE answer_id=$1 AND idempotency_key=$2 FOR UPDATE`,
		string(record.Feedback.Request.AnswerID), record.IdempotencyKey), answer.Answer)
	if err == nil {
		if existing.Feedback.RequestHash != record.Feedback.RequestHash || !sameFeedbackRequest(existing.Feedback.Request, record.Feedback.Request) {
			return conversationapplication.SubmitFeedbackResult{}, conflict(ErrorCodeFeedbackIdempotencyConflict, errors.New("feedback idempotency key is bound to a different request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return conversationapplication.SubmitFeedbackResult{}, classify(err, ErrorCodeDatabaseUnavailable)
		}
		return conversationapplication.SubmitFeedbackResult{Feedback: existing.Feedback, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.SubmitFeedbackResult{}, err
	}

	persisted, err := scanFeedbackRecord(tx.QueryRow(ctx, `INSERT INTO agent.answer_feedback(
		id,workspace_id,answer_id,feedback_type,citation_id,comment,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+feedbackColumns,
		string(record.Feedback.ID), string(record.Feedback.Request.WorkspaceID), string(record.Feedback.Request.AnswerID),
		string(record.Feedback.Request.Type), record.Feedback.Request.CitationID, record.Feedback.Request.Comment,
		record.IdempotencyKey, record.Feedback.RequestHash, record.Feedback.CreatedAt.UTC()), answer.Answer)
	if err != nil {
		return conversationapplication.SubmitFeedbackResult{}, err
	}
	if persisted.IdempotencyKey != record.IdempotencyKey || persisted.Feedback.RequestHash != record.Feedback.RequestHash ||
		!sameFeedbackRequest(persisted.Feedback.Request, record.Feedback.Request) {
		return conversationapplication.SubmitFeedbackResult{}, consistency(ErrorCodeFeedbackPersistenceCorrupt, errors.New("created feedback readback differs from request"))
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationapplication.SubmitFeedbackResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return conversationapplication.SubmitFeedbackResult{Feedback: persisted.Feedback}, nil
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
