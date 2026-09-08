package postgres

import (
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func normalizeAppendRequest(request domain.AppendRequest) (domain.AppendRequest, error) {
	request.OccurredAt = request.OccurredAt.UTC().Truncate(time.Microsecond)
	if err := request.Validate(); err != nil {
		return domain.AppendRequest{}, err
	}
	return request, nil
}

func sameAppendBinding(event domain.ServerEvent, request domain.AppendRequest) bool {
	return event.WorkspaceID == request.WorkspaceID && optionalIDsEqual(event.ConversationID, request.ConversationID) &&
		optionalIDsEqual(event.WorkflowRunID, request.WorkflowRunID) && event.Type == request.Type &&
		event.ResourceRef == request.ResourceRef && event.ResourceVersion == request.ResourceVersion &&
		reflect.DeepEqual(event.PayloadSummary, request.PayloadSummary) && event.SchemaVersion == request.SchemaVersion &&
		event.SourceEventRef == request.SourceEventRef && event.OccurredAt.Equal(request.OccurredAt) &&
		event.ExpiresAt.Equal(request.OccurredAt.Add(domain.RetentionWindow))
}

func optionalIDsEqual(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalIDValue(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func classifyAppendFailure(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code == domain.ErrorCodeEventCorrupt {
		return err
	}
	switch platformpostgres.SQLState(err) {
	case "23503", "23514":
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendBindingInvalid, false, err)
	case "23505":
		return appendConflict()
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeStoreUnavailable, true, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, err)
}

func appendConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeAppendConflict, false, errors.New("SSE source event reference is bound to a different event"))
}
