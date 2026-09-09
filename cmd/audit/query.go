package main

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type auditPage struct {
	Schema      string         `json:"schema"`
	WorkspaceID *foundation.ID `json:"workspace_id"`
	Limit       int            `json:"limit"`
	Items       []auditSummary `json:"items"`
	NextCursor  *auditCursor   `json:"next_cursor"`
}

type auditCursor struct {
	Before   time.Time     `json:"before"`
	BeforeID foundation.ID `json:"before_id"`
}

// The terminal projection deliberately excludes ActorRef, ResourceRef,
// IdempotencyKey, Correlation and Metadata, which may contain free text.
type auditSummary struct {
	ID           foundation.ID    `json:"id"`
	OccurredAt   time.Time        `json:"occurred_at"`
	ActorType    domain.ActorType `json:"actor_type"`
	Action       string           `json:"action"`
	ResourceType string           `json:"resource_type,omitempty"`
	Outcome      domain.Outcome   `json:"outcome"`
	ErrorCode    string           `json:"error_code,omitempty"`
}

func readPage(ctx context.Context, reader auditReader, query domain.ListQuery) (auditPage, error) {
	events, err := reader.List(ctx, query)
	if err != nil {
		return auditPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return auditPage{}, err
	}
	if len(events) > query.Limit {
		return auditPage{}, corruptPage()
	}
	page := auditPage{Schema: "audit-query/v1", WorkspaceID: query.WorkspaceID, Limit: query.Limit, Items: make([]auditSummary, 0, len(events))}
	previous := auditCursor{Before: query.Before, BeforeID: query.BeforeID}
	for _, event := range events {
		// Validate retains the shared Audit secret/canonical JSON checks. Do not
		// re-redact a corrupt stored event and thereby hide a persistence leak.
		if event.Validate() != nil || !sameWorkspace(query.WorkspaceID, event.WorkspaceID) ||
			!summaryCode(event.Action) || event.ResourceType != "" && !summaryCode(event.ResourceType) ||
			event.ErrorCode != "" && !summaryCode(event.ErrorCode) {
			return auditPage{}, corruptPage()
		}
		if !previous.Before.IsZero() && !(event.OccurredAt.Before(previous.Before) || event.OccurredAt.Equal(previous.Before) && event.ID < previous.BeforeID) {
			return auditPage{}, corruptPage()
		}
		page.Items = append(page.Items, auditSummary{
			ID: event.ID, OccurredAt: event.OccurredAt, ActorType: event.ActorType,
			Action: event.Action, ResourceType: event.ResourceType, Outcome: event.Outcome, ErrorCode: event.ErrorCode,
		})
		previous = auditCursor{Before: event.OccurredAt, BeforeID: event.ID}
	}
	if len(events) == query.Limit {
		page.NextCursor = &previous
	}
	return page, nil
}

func sameWorkspace(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// Summary codes are identifiers, not arbitrary text, URLs or paths. Producers
// own their values; this is an output shape check, not a second action registry.
func summaryCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '.' || character == '-') {
			return false
		}
	}
	return true
}

func corruptPage() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("audit page is invalid"))
}
