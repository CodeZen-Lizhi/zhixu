package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const eventSelect = `
	SELECT seq,workspace_id::text,conversation_id::text,workflow_run_id::text,
	       event_type,resource_ref,resource_version,payload_summary::text,
	       schema_version,source_event_ref,occurred_at,expires_at
	FROM ops.server_event`

func validateReplayQuery(ctx context.Context, workspaceID foundation.ID) error {
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeReplayQueryInvalid, false, errors.New("SSE query context is nil"))
	}
	parsed, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsed != workspaceID {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeReplayQueryInvalid, false, errors.New("SSE query workspace is invalid"))
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanEvent(row scanner) (domain.ServerEvent, error) {
	event, err := scanEventRaw(row)
	if err == nil {
		return event, nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return domain.ServerEvent{}, err
	}
	return domain.ServerEvent{}, queryFailure(err)
}

func scanEventRaw(row scanner) (domain.ServerEvent, error) {
	var (
		event                          domain.ServerEvent
		workspaceText                  string
		conversationText, workflowText *string
		summaryText                    string
	)
	if err := row.Scan(
		&event.Seq, &workspaceText, &conversationText, &workflowText,
		&event.Type, &event.ResourceRef, &event.ResourceVersion, &summaryText,
		&event.SchemaVersion, &event.SourceEventRef, &event.OccurredAt, &event.ExpiresAt,
	); err != nil {
		return domain.ServerEvent{}, err
	}
	workspaceID, err := foundation.ParseID(workspaceText)
	if err != nil || string(workspaceID) != workspaceText {
		return domain.ServerEvent{}, corruptEvent(err)
	}
	event.WorkspaceID = workspaceID
	event.ConversationID, err = parseOptionalID(conversationText)
	if err != nil {
		return domain.ServerEvent{}, corruptEvent(err)
	}
	event.WorkflowRunID, err = parseOptionalID(workflowText)
	if err != nil {
		return domain.ServerEvent{}, corruptEvent(err)
	}
	event.PayloadSummary, err = domain.DecodePayloadSummary([]byte(summaryText))
	if err != nil {
		return domain.ServerEvent{}, corruptEvent(err)
	}
	if err := event.Validate(); err != nil {
		return domain.ServerEvent{}, corruptEvent(err)
	}
	return event, nil
}

func parseOptionalID(value *string) (*foundation.ID, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := foundation.ParseID(*value)
	if err != nil || string(parsed) != *value {
		return nil, errors.New("SSE optional identity is invalid")
	}
	return &parsed, nil
}

func queryFailure(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, fmt.Errorf("query SSE store: %w", err))
}

func corruptEvent(err error) error {
	if err == nil {
		err = errors.New("SSE event is corrupt")
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventCorrupt, false, err)
}
