package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

const timelineProjectionColumns = `id::text,event_id::text,workspace_id::text,event_type,aggregate_type,
aggregate_id::text,source_event_ref,source_ref,event_version,schema_version,summary,correlation::text,
operator_type,operator_id::text,owner_binding::text,occurred_at,created_at,version`

var _ knowledgeapp.TimelineProjectionPort = (*Repository)(nil)

type timelineProjectionSource struct {
	ID              foundation.ID
	EventID         foundation.ID
	Event           domain.KnowledgeEvent
	CorrelationRaw  string
	OperatorTypeRaw *string
	OperatorIDRaw   *string
	OwnerBindingRaw *string
	Version         int64
}

// ProjectNext 领取一条可信投影源并在同一短事务中追加 Knowledge Event。
// 正式状态变化只负责写入 Outbox；这里失败不会回滚源领域事务。
func (repository *Repository) ProjectNext(ctx context.Context) (knowledgeapp.TimelineProjectionResult, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return knowledgeapp.TimelineProjectionResult{}, false, timelineUnavailable(errors.New("timeline projection repository is unavailable"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return knowledgeapp.TimelineProjectionResult{}, false, timelineStorage(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	source, found, err := claimTimelineProjection(ctx, tx)
	if err != nil || !found {
		return knowledgeapp.TimelineProjectionResult{}, found, err
	}
	result := knowledgeapp.TimelineProjectionResult{SourceID: source.ID, EventID: source.EventID}
	operator, ownerBinding, err := decodeTimelineEventExtensions(source.OperatorTypeRaw, source.OperatorIDRaw, source.OwnerBindingRaw)
	if err != nil {
		result.Outcome = knowledgeapp.TimelineProjectionPoisoned
		return poisonTimelineProjection(ctx, tx, source, result, err)
	}
	source.Event.Operator, source.Event.OwnerBinding = operator, ownerBinding
	if err := decodeTimelineCorrelation(source.CorrelationRaw, &source.Event.Correlation); err != nil {
		result.Outcome = knowledgeapp.TimelineProjectionPoisoned
		return poisonTimelineProjection(ctx, tx, source, result, err)
	}
	if err := source.Event.Validate(); err != nil {
		result.Outcome = knowledgeapp.TimelineProjectionPoisoned
		return poisonTimelineProjection(ctx, tx, source, result, err)
	}
	persisted, replayed, err := appendTimelineEventTx(ctx, tx, source.Event)
	if err != nil {
		if shouldPoisonTimelineProjection(err) {
			result.Outcome = knowledgeapp.TimelineProjectionPoisoned
			return poisonTimelineProjection(ctx, tx, source, result, err)
		}
		return knowledgeapp.TimelineProjectionResult{}, true, err
	}
	if persisted.ID != source.EventID || persisted.WorkspaceID != source.Event.WorkspaceID || persisted.SourceEventRef != source.Event.SourceEventRef {
		result.Outcome = knowledgeapp.TimelineProjectionPoisoned
		return poisonTimelineProjection(ctx, tx, source, result, errors.New("timeline projection returned a mismatched event"))
	}
	command, err := tx.Exec(ctx, `UPDATE ops.timeline_projection_outbox
SET status='PROJECTED',error_code=NULL,
    projected_at=GREATEST(CURRENT_TIMESTAMP,created_at),
    updated_at=GREATEST(CURRENT_TIMESTAMP,created_at),version=version+1
WHERE id=$1 AND status='PENDING' AND version=$2`, string(source.ID), source.Version)
	if err != nil {
		return knowledgeapp.TimelineProjectionResult{}, true, timelineStorage(err)
	}
	if command.RowsAffected() != 1 {
		return knowledgeapp.TimelineProjectionResult{}, true, timelineCorrupt(errors.New("timeline projection completion CAS did not match"))
	}
	if replayed {
		result.Outcome = knowledgeapp.TimelineProjectionReplayed
	} else {
		result.Outcome = knowledgeapp.TimelineProjectionProjected
	}
	if err := tx.Commit(ctx); err != nil {
		return knowledgeapp.TimelineProjectionResult{}, true, timelineStorage(err)
	}
	return result, true, nil
}

func claimTimelineProjection(ctx context.Context, tx pgx.Tx) (timelineProjectionSource, bool, error) {
	row := tx.QueryRow(ctx, `SELECT `+timelineProjectionColumns+`
FROM ops.timeline_projection_outbox
WHERE status='PENDING'
ORDER BY occurred_at,id
FOR UPDATE SKIP LOCKED
LIMIT 1`)
	var source timelineProjectionSource
	var sourceID, eventID, workspaceID, eventType, aggregateType string
	var aggregateID *string
	if err := row.Scan(
		&sourceID, &eventID, &workspaceID, &eventType, &aggregateType, &aggregateID,
		&source.Event.SourceEventRef, &source.Event.SourceRef, &source.Event.EventVersion,
		&source.Event.SchemaVersion, &source.Event.Summary, &source.CorrelationRaw,
		&source.OperatorTypeRaw, &source.OperatorIDRaw, &source.OwnerBindingRaw,
		&source.Event.OccurredAt, &source.Event.CreatedAt, &source.Version,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return timelineProjectionSource{}, false, nil
		}
		return timelineProjectionSource{}, false, timelineStorage(err)
	}
	source.ID, source.EventID = foundation.ID(sourceID), foundation.ID(eventID)
	source.Event.ID, source.Event.WorkspaceID = source.EventID, foundation.ID(workspaceID)
	source.Event.EventType, source.Event.AggregateType = domain.EventType(eventType), domain.TimelineAggregateType(aggregateType)
	if aggregateID != nil {
		value := foundation.ID(*aggregateID)
		source.Event.AggregateID = &value
	}
	source.Event.Payload = json.RawMessage(`{}`)
	source.Event.OccurredAt = domain.CanonicalTimelineTime(source.Event.OccurredAt)
	source.Event.CreatedAt = domain.CanonicalTimelineTime(source.Event.CreatedAt)
	return source, true, nil
}

func shouldPoisonTimelineProjection(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && (classified.Kind == foundation.ErrorInvalidInput ||
		classified.Kind == foundation.ErrorVersionConflict || classified.Kind == foundation.ErrorConsistencyViolation)
}

func poisonTimelineProjection(
	ctx context.Context,
	tx pgx.Tx,
	source timelineProjectionSource,
	result knowledgeapp.TimelineProjectionResult,
	cause error,
) (knowledgeapp.TimelineProjectionResult, bool, error) {
	command, err := tx.Exec(ctx, `UPDATE ops.timeline_projection_outbox
SET status='POISONED',error_code=$1,projected_at=NULL,
    updated_at=GREATEST(CURRENT_TIMESTAMP,created_at),version=version+1
WHERE id=$2 AND status='PENDING' AND version=$3`, domain.ErrorCodeTimelineProjectionPoisoned, string(source.ID), source.Version)
	if err != nil {
		return knowledgeapp.TimelineProjectionResult{}, true, timelineStorage(err)
	}
	if command.RowsAffected() != 1 {
		return knowledgeapp.TimelineProjectionResult{}, true, timelineCorrupt(errors.New("timeline projection poison CAS did not match"))
	}
	if err := tx.Commit(ctx); err != nil {
		return knowledgeapp.TimelineProjectionResult{}, true, timelineStorage(err)
	}
	return result, true, foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeTimelineProjectionPoisoned, false, cause)
}

func decodeTimelineCorrelation(raw string, target *domain.EventCorrelation) error {
	return decodeTimelineJSON(raw, target, "timeline correlation")
}
