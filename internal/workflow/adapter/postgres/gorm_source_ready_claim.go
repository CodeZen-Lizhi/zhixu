package workflowpostgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ClaimSourceReadyExcludingScoped lets a bounded consumer pass skip Workspaces
// whose preceding source is still running. The excluded set is transient; the
// unpublished notifications remain the durable authority for future passes.
func (outbox *GORMSourceReadyOutbox) ClaimSourceReadyExcludingScoped(ctx context.Context, scope foundation.TransactionScope, excluded []foundation.ID) (application.SourceReadyOutboxFact, bool, error) {
	if len(excluded) == 0 {
		return outbox.ClaimSourceReadyScoped(ctx, scope)
	}
	if len(excluded) > 100 {
		return application.SourceReadyOutboxFact{}, false, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_INPUT_INVALID", false, errors.New("source-ready excluded Workspace limit exceeded"))
	}
	values := make([]string, len(excluded))
	for i, id := range excluded {
		if !validGORMWorkflowID(id) {
			return application.SourceReadyOutboxFact{}, false, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_INPUT_INVALID", false, errors.New("source-ready excluded Workspace is invalid"))
		}
		values[i] = string(id)
	}
	tx, err := outbox.sourceReadyTransaction(ctx, scope)
	if err != nil {
		return application.SourceReadyOutboxFact{}, false, err
	}
	var row sourceReadyOutboxGORMRecord
	err = tx.Select(sourceReadyOutboxColumns).Where("event_type=? AND published_at IS NULL AND workspace_id NOT IN ?", application.SourceReadyEventType, values).
		Order("occurred_at,id").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return application.SourceReadyOutboxFact{}, false, nil
	}
	if err != nil {
		return application.SourceReadyOutboxFact{}, false, classifyGORMWorkflow(ctx, err, "SOURCE_READY_OUTBOX_CLAIM_FAILED")
	}
	fact, err := row.fact()
	return fact, err == nil, err
}
