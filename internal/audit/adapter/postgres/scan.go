package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const auditSelect = `
	SELECT id::text,workspace_id::text,actor_type,actor_ref,action,resource_type,resource_ref,
	       idempotency_key,correlation::text,payload::text,occurred_at
	FROM ops.audit_event`

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (domain.Event, error) {
	event, err := scanEventRaw(row)
	if err == nil || auditNoRows(err) {
		return event, err
	}
	return domain.Event{}, classifyStoreError(err)
}

func scanEventRaw(row scanner) (domain.Event, error) {
	var (
		event                                            domain.Event
		id, actorType                                    string
		workspaceID, actorRef, resourceType, resourceRef *string
		idempotencyKey                                   *string
		correlationJSON, payloadJSON                     string
	)
	if err := row.Scan(
		&id, &workspaceID, &actorType, &actorRef, &event.Action, &resourceType, &resourceRef,
		&idempotencyKey, &correlationJSON, &payloadJSON, &event.OccurredAt,
	); err != nil {
		return domain.Event{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil || string(parsedID) != id {
		return domain.Event{}, corruptEvent(errors.New("audit id is corrupt"))
	}
	event.ID = parsedID
	event.ActorType = domain.ActorType(actorType)
	if workspaceID != nil {
		parsed, parseErr := foundation.ParseID(*workspaceID)
		if parseErr != nil || string(parsed) != *workspaceID {
			return domain.Event{}, corruptEvent(errors.New("audit workspace id is corrupt"))
		}
		event.WorkspaceID = &parsed
	}
	if actorRef != nil {
		event.ActorRef = *actorRef
	}
	if resourceType != nil {
		event.ResourceType = *resourceType
	}
	if resourceRef != nil {
		event.ResourceRef = *resourceRef
	}
	if idempotencyKey == nil {
		return domain.Event{}, corruptEvent(errors.New("audit idempotency key is missing"))
	}
	event.IdempotencyKey = *idempotencyKey
	event.OccurredAt = event.OccurredAt.UTC().Truncate(time.Microsecond)

	correlationRaw := json.RawMessage(correlationJSON)
	canonicalCorrelation, err := domain.CanonicalJSONObject(correlationRaw)
	if err != nil {
		return domain.Event{}, corruptEvent(err)
	}
	redactedCorrelation, err := domain.RedactJSONObject(correlationRaw)
	if err != nil || !bytes.Equal(canonicalCorrelation, redactedCorrelation) {
		return domain.Event{}, corruptEvent(errors.New("audit correlation contains plaintext sensitive data"))
	}
	event.Correlation = canonicalCorrelation

	payloadRaw := json.RawMessage(payloadJSON)
	canonicalPayload, err := domain.CanonicalJSONObject(payloadRaw)
	if err != nil {
		return domain.Event{}, corruptEvent(err)
	}
	redactedPayload, err := domain.RedactJSONObject(payloadRaw)
	if err != nil || !bytes.Equal(canonicalPayload, redactedPayload) {
		return domain.Event{}, corruptEvent(errors.New("audit metadata contains plaintext sensitive data"))
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(canonicalPayload, &payload); err != nil {
		return domain.Event{}, corruptEvent(errors.New("audit metadata envelope is invalid"))
	}
	if err := decodeReservedFields(&event, payload); err != nil {
		return domain.Event{}, corruptEvent(err)
	}
	metadata, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, corruptEvent(errors.New("audit metadata cannot be encoded"))
	}
	event.Metadata = metadata
	if err := event.Validate(); err != nil {
		return domain.Event{}, corruptEvent(err)
	}
	return event, nil
}

func decodeReservedFields(event *domain.Event, payload map[string]json.RawMessage) error {
	if event == nil {
		return errors.New("audit event is nil")
	}
	var outcome string
	if raw, found := payload[reservedOutcomeKey]; !found || json.Unmarshal(raw, &outcome) != nil {
		return errors.New("audit outcome is missing")
	}
	event.Outcome = domain.Outcome(outcome)
	delete(payload, reservedOutcomeKey)
	if raw, found := payload[reservedErrorCodeKey]; found {
		if err := json.Unmarshal(raw, &event.ErrorCode); err != nil {
			return errors.New("audit error code is invalid")
		}
		delete(payload, reservedErrorCodeKey)
	}
	var schemaVersion string
	if raw, found := payload[reservedSchemaVersionKey]; !found || json.Unmarshal(raw, &schemaVersion) != nil || schemaVersion != domain.SchemaVersion {
		return errors.New("audit schema version is invalid")
	}
	event.SchemaVersion = schemaVersion
	delete(payload, reservedSchemaVersionKey)
	return nil
}

func corruptEvent(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, cause)
}
