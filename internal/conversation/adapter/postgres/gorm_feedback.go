package postgres

import (
	"context"
	"database/sql"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordFeedback 在锁定的 Answer 范围内追加或精确重放 Feedback。
func (repository *GORMRepository) RecordFeedback(ctx context.Context, record conversationapplication.RecordFeedbackRecord) (conversationapplication.SubmitFeedbackResult, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return conversationapplication.SubmitFeedbackResult{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if ctx == nil || !canonicalIdempotencyKey(record.IdempotencyKey) {
		return conversationapplication.SubmitFeedbackResult{}, invalid(ErrorCodeFeedbackPersistenceInvalid, errors.New("feedback context or idempotency key is invalid"))
	}
	var result conversationapplication.SubmitFeedbackResult
	err := withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		answer, err := scanAnswerView(gormScanRow(tx.Raw(`SELECT `+answerViewColumns+`
			FROM agent.answer a JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
			WHERE a.workspace_id=? AND a.id=? FOR UPDATE OF a`, string(record.Feedback.Request.WorkspaceID), string(record.Feedback.Request.AnswerID))))
		if errors.Is(err, sql.ErrNoRows) {
			return notFound(ErrorCodeAnswerNotFound, err)
		}
		if err != nil {
			return err
		}
		if err := conversationdomain.ValidateAnswerFeedback(record.Feedback, answer.Answer); err != nil {
			return invalid(ErrorCodeFeedbackPersistenceInvalid, err)
		}
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?,0))`, string(record.Feedback.Request.AnswerID), record.IdempotencyKey).Error; err != nil {
			return classify(err, ErrorCodeDatabaseUnavailable)
		}
		existing, err := scanFeedbackRecord(gormScanRow(tx.Model(&feedbackModel{}).Select(feedbackColumns).
			Where("answer_id=? AND idempotency_key=?", string(record.Feedback.Request.AnswerID), record.IdempotencyKey).
			Clauses(clause.Locking{Strength: "UPDATE"})), answer.Answer)
		if err == nil {
			if existing.Feedback.RequestHash != record.Feedback.RequestHash || !sameFeedbackRequest(existing.Feedback.Request, record.Feedback.Request) {
				return conflict(ErrorCodeFeedbackIdempotencyConflict, errors.New("feedback idempotency key is bound to a different request"))
			}
			result = conversationapplication.SubmitFeedbackResult{Feedback: existing.Feedback, Replayed: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		model := feedbackModel{
			ID: string(record.Feedback.ID), WorkspaceID: string(record.Feedback.Request.WorkspaceID), AnswerID: string(record.Feedback.Request.AnswerID),
			FeedbackType: string(record.Feedback.Request.Type), CitationID: record.Feedback.Request.CitationID, Comment: record.Feedback.Request.Comment,
			IdempotencyKey: record.IdempotencyKey, RequestHash: record.Feedback.RequestHash, CreatedAt: record.Feedback.CreatedAt.UTC(),
		}
		if err := tx.Create(&model).Error; err != nil {
			return classify(err, ErrorCodeDatabaseUnavailable)
		}
		persisted, err := scanFeedbackRecord(gormScanRow(tx.Model(&feedbackModel{}).Select(feedbackColumns).
			Where("workspace_id=? AND id=?", model.WorkspaceID, model.ID)), answer.Answer)
		if err != nil {
			return err
		}
		if persisted.IdempotencyKey != record.IdempotencyKey || persisted.Feedback.RequestHash != record.Feedback.RequestHash || !sameFeedbackRequest(persisted.Feedback.Request, record.Feedback.Request) {
			return consistency(ErrorCodeFeedbackPersistenceCorrupt, errors.New("created feedback readback differs from request"))
		}
		result = conversationapplication.SubmitFeedbackResult{Feedback: persisted.Feedback}
		return nil
	})
	if err != nil {
		return conversationapplication.SubmitFeedbackResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return result, nil
}
