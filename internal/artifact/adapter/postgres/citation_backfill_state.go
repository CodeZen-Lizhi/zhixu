package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

type citationBackfillState struct {
	workspaceID                foundation.ID
	highWaterCreatedAt         *time.Time
	highWaterRevisionID        *foundation.ID
	cursorCreatedAt            *time.Time
	cursorRevisionID           *foundation.ID
	expectedRevisionCount      int64
	processedRevisionCount     int64
	expectedSelectorCount      int64
	processedSelectorCount     int64
	validationCursorCreatedAt  *time.Time
	validationCursorRevisionID *foundation.ID
	validatedRevisionCount     int64
	validatedSelectorCount     int64
	status                     string
	version                    int64
}

type citationBackfillRevision struct {
	revision domain.Revision
}

func optionalFoundationID(value *string) *foundation.ID {
	if value == nil {
		return nil
	}
	id := foundation.ID(*value)
	return &id
}

func validateCitationBackfillState(state citationBackfillState) error {
	if !validID(state.workspaceID) || state.expectedRevisionCount < 1 || state.highWaterCreatedAt == nil || state.highWaterRevisionID == nil ||
		state.processedRevisionCount < 0 || state.processedRevisionCount > state.expectedRevisionCount ||
		state.processedSelectorCount < 0 || state.processedSelectorCount > state.expectedSelectorCount ||
		state.validatedRevisionCount < 0 || state.validatedRevisionCount > state.expectedRevisionCount ||
		state.validatedSelectorCount < 0 || state.validatedSelectorCount > state.expectedSelectorCount || state.version < 1 {
		return errors.New("artifact citation backfill marker is invalid")
	}
	if state.status != "PENDING" && state.status != "RUNNING" && state.status != "FAILED" {
		return errors.New("artifact citation backfill marker status is invalid")
	}
	if state.processedRevisionCount == 0 && (state.cursorCreatedAt != nil || state.cursorRevisionID != nil) ||
		state.processedRevisionCount > 0 && (state.cursorCreatedAt == nil || state.cursorRevisionID == nil) ||
		state.validatedRevisionCount == 0 && (state.validationCursorCreatedAt != nil || state.validationCursorRevisionID != nil) ||
		state.validatedRevisionCount > 0 && (state.validationCursorCreatedAt == nil || state.validationCursorRevisionID == nil) {
		return errors.New("artifact citation backfill marker cursor is invalid")
	}
	return nil
}

func citationBackfillDataError(err error) bool {
	switch platformpostgres.SQLState(err) {
	case "23502", "23503", "23514", "22P02":
		return true
	default:
		return false
	}
}

func shouldPersistCitationBackfillFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return true
	}
	switch classified.Kind {
	case foundation.ErrorRetryableFailure, foundation.ErrorDependencyUnavailable:
		return false
	default:
		return true
	}
}
