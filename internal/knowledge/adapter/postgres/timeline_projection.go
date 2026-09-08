package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const timelineProjectionColumns = `id::text,event_id::text,workspace_id::text,event_type,aggregate_type,
aggregate_id::text,source_event_ref,source_ref,event_version,schema_version,summary,correlation::text,
operator_type,operator_id::text,owner_binding::text,occurred_at,created_at,version`

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

func shouldPoisonTimelineProjection(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && (classified.Kind == foundation.ErrorInvalidInput ||
		classified.Kind == foundation.ErrorVersionConflict || classified.Kind == foundation.ErrorConsistencyViolation)
}

func decodeTimelineCorrelation(raw string, target *domain.EventCorrelation) error {
	return decodeTimelineJSON(raw, target, "timeline correlation")
}
