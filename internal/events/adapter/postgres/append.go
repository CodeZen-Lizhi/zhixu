package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const appendEventSQL = `
	INSERT INTO ops.server_event(
		workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,resource_version,
		payload_summary,schema_version,source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10::timestamptz,$10::timestamptz + INTERVAL '24 hours')
	ON CONFLICT (workspace_id,source_event_ref) DO NOTHING
	RETURNING seq,workspace_id::text,conversation_id::text,workflow_run_id::text,
	          event_type,resource_ref,resource_version,payload_summary::text,
	          schema_version,source_event_ref,occurred_at,expires_at`

// AppendTx 在调用方事务中追加事件；同一来源完整绑定只返回 exact replay。
func (store *Store) AppendTx(ctx context.Context, transaction any, request domain.AppendRequest) (domain.ServerEvent, bool, error) {
	if store == nil || isNilDB(store.db) {
		return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, errors.New("SSE database is unavailable"))
	}
	if ctx == nil {
		return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeAppendInvalid, false, errors.New("SSE append context is nil"))
	}
	request.OccurredAt = request.OccurredAt.UTC().Truncate(time.Microsecond)
	if err := request.Validate(); err != nil {
		return domain.ServerEvent{}, false, err
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || isNilTx(tx) {
		return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeAppendTransactionUnavailable, true, errors.New("SSE append transaction is unavailable"))
	}
	summary, err := json.Marshal(request.PayloadSummary)
	if err != nil {
		return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendInvalid, false, err)
	}
	event, err := scanEvent(tx.QueryRow(ctx, appendEventSQL,
		string(request.WorkspaceID), optionalIDValue(request.ConversationID), optionalIDValue(request.WorkflowRunID),
		request.Type, request.ResourceRef, request.ResourceVersion, summary, request.SchemaVersion,
		request.SourceEventRef, request.OccurredAt,
	))
	if err == nil {
		if !sameAppendBinding(event, request) {
			return domain.ServerEvent{}, false, appendConflict()
		}
		return event, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ServerEvent{}, false, classifyAppendFailure(err)
	}

	existing, err := scanEvent(tx.QueryRow(ctx, eventSelect+`
		WHERE workspace_id=$1 AND source_event_ref=$2`, string(request.WorkspaceID), request.SourceEventRef))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendConflict, false, errors.New("SSE append conflict has no persisted event"))
		}
		return domain.ServerEvent{}, false, err
	}
	if !sameAppendBinding(existing, request) {
		return domain.ServerEvent{}, false, appendConflict()
	}
	return existing, true, nil
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
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendBindingInvalid, false, err)
		case "23505":
			return appendConflict()
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeStoreUnavailable, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, err)
}

func appendConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeAppendConflict, false, errors.New("SSE source event reference is bound to a different event"))
}

func isNilTx(tx pgx.Tx) bool {
	if tx == nil {
		return true
	}
	value := reflect.ValueOf(tx)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

var _ application.Appender = (*Store)(nil)
