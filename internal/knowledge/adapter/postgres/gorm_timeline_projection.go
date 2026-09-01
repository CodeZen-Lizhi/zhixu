package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"gorm.io/gorm"
)

var _ knowledgeapp.TimelineProjectionPort = (*GORMRepository)(nil)

// ProjectNext claims one source per transaction to preserve SKIP LOCKED work sharing.
func (repository *GORMRepository) ProjectNext(ctx context.Context) (knowledgeapp.TimelineProjectionResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return knowledgeapp.TimelineProjectionResult{}, false, err
	}
	var result knowledgeapp.TimelineProjectionResult
	var found bool
	var poisonCause error
	err := repository.within(ctx, foundation.TransactionOptions{}, domain.ErrorCodeTimelineUnavailable, domain.ErrorCodeTimelineUnavailable, classifyGORMKnowledge,
		func(callbackCtx context.Context, _ foundation.TransactionScope, transaction *gorm.DB) error {
			source, sourceFound, claimErr := gormClaimTimelineProjection(callbackCtx, transaction)
			if claimErr != nil || !sourceFound {
				return claimErr
			}
			found = true
			result = knowledgeapp.TimelineProjectionResult{SourceID: source.ID, EventID: source.EventID}
			poison := func(cause error) error {
				result.Outcome = knowledgeapp.TimelineProjectionPoisoned
				poisonCause = cause
				return gormMarkTimelineProjectionPoisoned(callbackCtx, transaction, source)
			}
			operator, ownerBinding, decodeErr := decodeTimelineEventExtensions(source.OperatorTypeRaw, source.OperatorIDRaw, source.OwnerBindingRaw)
			if decodeErr != nil {
				return poison(decodeErr)
			}
			source.Event.Operator, source.Event.OwnerBinding = operator, ownerBinding
			if decodeErr := decodeTimelineCorrelation(source.CorrelationRaw, &source.Event.Correlation); decodeErr != nil {
				return poison(decodeErr)
			}
			if validateErr := source.Event.Validate(); validateErr != nil {
				return poison(validateErr)
			}
			persisted, replayed, appendErr := gormAppendTimelineEvent(callbackCtx, transaction, source.Event)
			if appendErr != nil {
				if shouldPoisonTimelineProjection(appendErr) {
					return poison(appendErr)
				}
				return appendErr
			}
			if persisted.ID != source.EventID || persisted.WorkspaceID != source.Event.WorkspaceID || persisted.SourceEventRef != source.Event.SourceEventRef {
				return poison(errors.New("timeline projection returned a mismatched event"))
			}
			affected, updateErr := gormKnowledgeExec(callbackCtx, transaction, `UPDATE ops.timeline_projection_outbox
SET status='PROJECTED',error_code=NULL,projected_at=GREATEST(CURRENT_TIMESTAMP,created_at),
updated_at=GREATEST(CURRENT_TIMESTAMP,created_at),version=version+1
WHERE id=? AND status='PENDING' AND version=?`, string(source.ID), source.Version)
			if updateErr != nil {
				return classifyGORMKnowledge(callbackCtx, updateErr, domain.ErrorCodeTimelineUnavailable)
			}
			if affected != 1 {
				return timelineCorrupt(errors.New("timeline projection completion CAS did not match"))
			}
			if replayed {
				result.Outcome = knowledgeapp.TimelineProjectionReplayed
			} else {
				result.Outcome = knowledgeapp.TimelineProjectionProjected
			}
			return nil
		})
	if err != nil {
		return knowledgeapp.TimelineProjectionResult{}, found, err
	}
	if !found {
		return knowledgeapp.TimelineProjectionResult{}, false, nil
	}
	if poisonCause != nil {
		return result, true, foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeTimelineProjectionPoisoned, false, poisonCause)
	}
	return result, true, nil
}

func gormClaimTimelineProjection(ctx context.Context, database *gorm.DB) (timelineProjectionSource, bool, error) {
	row, err := gormKnowledgeRawRow(ctx, database, `SELECT `+timelineProjectionColumns+`
FROM ops.timeline_projection_outbox WHERE status='PENDING'
ORDER BY occurred_at,id FOR UPDATE SKIP LOCKED LIMIT 1`)
	if err != nil {
		return timelineProjectionSource{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	var source timelineProjectionSource
	var sourceID, eventID, workspaceID, eventType, aggregateType string
	var aggregateID *string
	err = row.Scan(&sourceID, &eventID, &workspaceID, &eventType, &aggregateType, &aggregateID,
		&source.Event.SourceEventRef, &source.Event.SourceRef, &source.Event.EventVersion,
		&source.Event.SchemaVersion, &source.Event.Summary, &source.CorrelationRaw,
		&source.OperatorTypeRaw, &source.OperatorIDRaw, &source.OwnerBindingRaw,
		&source.Event.OccurredAt, &source.Event.CreatedAt, &source.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return timelineProjectionSource{}, false, nil
	}
	if err != nil {
		return timelineProjectionSource{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
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

func gormMarkTimelineProjectionPoisoned(ctx context.Context, database *gorm.DB, source timelineProjectionSource) error {
	affected, err := gormKnowledgeExec(ctx, database, `UPDATE ops.timeline_projection_outbox
SET status='POISONED',error_code=?,projected_at=NULL,
updated_at=GREATEST(CURRENT_TIMESTAMP,created_at),version=version+1
WHERE id=? AND status='PENDING' AND version=?`, domain.ErrorCodeTimelineProjectionPoisoned, string(source.ID), source.Version)
	if err != nil {
		return classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	if affected != 1 {
		return timelineCorrupt(errors.New("timeline projection poison CAS did not match"))
	}
	return nil
}
