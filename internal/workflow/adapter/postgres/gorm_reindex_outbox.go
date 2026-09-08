package workflowpostgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"gorm.io/gorm"
)

// GORMReindexOutbox 在 Retrieval 持有的事务内领取和发布 Workflow Outbox。
type GORMReindexOutbox struct {
	database *gorm.DB
}

// NewGORMReindexOutbox 从共享 Pool 构造参与者；方法只使用调用方的 live scope。
func NewGORMReindexOutbox(pool *platformpostgres.Pool) (*GORMReindexOutbox, error) {
	if pool == nil {
		return nil, gormWorkflowUnavailable("REINDEX_OUTBOX_UNAVAILABLE", errors.New("workflow outbox pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormWorkflowUnavailable("REINDEX_OUTBOX_UNAVAILABLE", err)
	}
	if !validGORMWorkflowDatabase(database) {
		return nil, gormWorkflowUnavailable("REINDEX_OUTBOX_UNAVAILABLE", errors.New("workflow outbox database is unavailable"))
	}
	return &GORMReindexOutbox{database: database}, nil
}

// ClaimReindexOutboxScoped 保留 first-dispatch 的 FIFO 与 SKIP LOCKED，不提交事务。
func (outbox *GORMReindexOutbox) ClaimReindexOutboxScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	request retrievalapplication.ReindexOutboxClaimRequest,
) (retrievalapplication.ReindexOutboxFact, bool, error) {
	consumerName := strings.TrimSpace(request.ConsumerName)
	if consumerName == "" || !utf8.ValidString(consumerName) || utf8.RuneCountInString(consumerName) > 128 || strings.ContainsRune(consumerName, 0) {
		return retrievalapplication.ReindexOutboxFact{}, false, foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_OUTBOX_CLAIM_INVALID", false, errors.New("reindex consumer identity is invalid"))
	}
	transaction, err := outbox.scopedTransaction(ctx, scope)
	if err != nil {
		return retrievalapplication.ReindexOutboxFact{}, false, err
	}
	row, err := gormWorkflowRawRow(transaction, `SELECT
		event.id::text,event.workspace_id::text,event.run_id::text,event.payload::text
		FROM workflow.outbox_event AS event
		WHERE event.event_type=? AND event.published_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM retrieval.reindex_delivery AS blocker
			WHERE blocker.workspace_id=event.workspace_id
			  AND blocker.status IN ('pending','dispatched','processing','retry_wait','manual_recovery')
			  AND NOT (blocker.consumer_name=? AND blocker.outbox_event_id=event.id)
		  )
		ORDER BY event.occurred_at,event.id
		FOR UPDATE OF event SKIP LOCKED LIMIT 1`, reindexcontract.EventTypeReindexRequested, consumerName)
	if err != nil {
		return retrievalapplication.ReindexOutboxFact{}, false, classifyGORMReindexOutbox(ctx, err, "REINDEX_OUTBOX_SELECT_FAILED")
	}
	var fact retrievalapplication.ReindexOutboxFact
	var runID sql.NullString
	if err := row.Scan(&fact.EventID, &fact.WorkspaceID, &runID, &fact.Payload); err != nil {
		if gormWorkflowNoRows(err) {
			return retrievalapplication.ReindexOutboxFact{}, false, nil
		}
		return retrievalapplication.ReindexOutboxFact{}, false, classifyGORMReindexOutbox(ctx, err, "REINDEX_OUTBOX_SELECT_FAILED")
	}
	if runID.Valid {
		id := foundation.ID(runID.String)
		fact.WorkflowRunID = &id
	}
	return fact, true, nil
}

// PublishReindexOutboxScoped 与 Delivery、River Job 共用 scope，重复发布仍返回 CAS 失败。
func (outbox *GORMReindexOutbox) PublishReindexOutboxScoped(ctx context.Context, scope foundation.TransactionScope, eventID foundation.ID) error {
	if !validGORMWorkflowID(eventID) {
		return foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_OUTBOX_PUBLISH_INVALID", false, errors.New("reindex outbox identity is invalid"))
	}
	transaction, err := outbox.scopedTransaction(ctx, scope)
	if err != nil {
		return err
	}
	row, err := gormWorkflowRawRow(transaction, `UPDATE workflow.outbox_event
		SET published_at=CURRENT_TIMESTAMP
		WHERE id=?::uuid AND event_type=? AND published_at IS NULL
		RETURNING id::text`, string(eventID), reindexcontract.EventTypeReindexRequested)
	if err != nil {
		return classifyGORMReindexOutbox(ctx, err, "REINDEX_OUTBOX_PUBLISH_FAILED")
	}
	var published string
	return classifyGORMReindexOutbox(ctx, row.Scan(&published), "REINDEX_OUTBOX_PUBLISH_FAILED")
}

func (outbox *GORMReindexOutbox) scopedTransaction(ctx context.Context, scope foundation.TransactionScope) (*gorm.DB, error) {
	if outbox == nil || !validGORMWorkflowDatabase(outbox.database) {
		return nil, gormWorkflowUnavailable("REINDEX_OUTBOX_UNAVAILABLE", errors.New("workflow outbox is unavailable"))
	}
	if ctx == nil {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_OUTBOX_CONTEXT_INVALID", false, errors.New("workflow outbox context is nil"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, gormWorkflowUnavailable("REINDEX_OUTBOX_TRANSACTION_UNAVAILABLE", err)
	}
	return transaction.WithContext(ctx), nil
}

// Outbox 原由 Retrieval Dispatcher 访问，迁移 owner 后保留其错误类别和稳定码。
func classifyGORMReindexOutbox(ctx context.Context, cause error, code string) error {
	if cause == nil {
		return nil
	}
	if contextCause := gormWorkflowContextCause(ctx, cause); contextCause != nil {
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, contextCause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, contextCause)
	}
	if gormWorkflowNoRows(cause) {
		return foundation.NewError(foundation.ErrorNotFound, code, false, cause)
	}
	switch platformpostgres.SQLState(cause) {
	case "40001", "40P01", "55P03":
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
	case "23505":
		return foundation.NewError(foundation.ErrorVersionConflict, code, false, cause)
	case "23503", "23514", "55000":
		return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, cause)
	}
	return gormWorkflowUnavailable(code, cause)
}

var _ retrievalapplication.ScopedReindexOutbox = (*GORMReindexOutbox)(nil)
