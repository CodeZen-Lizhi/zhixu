package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const eventSelect = `
	SELECT seq,workspace_id::text,conversation_id::text,workflow_run_id::text,
	       event_type,resource_ref,resource_version,payload_summary::text,
	       schema_version,source_event_ref,occurred_at,expires_at
	FROM ops.server_event`

// DB 是事件重放 Store 所需的最小 PostgreSQL 查询边界。
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Store 从 ops.server_event 提供按 Workspace 隔离的水位和有界重放。
type Store struct {
	db DB
}

// NewStore 创建 PostgreSQL Server Event Store。
func NewStore(db DB) (*Store, error) {
	if isNilDB(db) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, errors.New("SSE database is nil"))
	}
	return &Store{db: db}, nil
}

// CurrentWatermark 返回 Workspace 已分配的最高事件序号，包括刚越过逻辑保留期的记录。
func (store *Store) CurrentWatermark(ctx context.Context, workspaceID foundation.ID) (int64, error) {
	if err := store.validateQuery(ctx, workspaceID); err != nil {
		return 0, err
	}
	var watermark int64
	if err := store.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(seq),0)
		FROM ops.server_event
		WHERE workspace_id=$1`, string(workspaceID)).Scan(&watermark); err != nil {
		return 0, queryFailure(err)
	}
	if watermark < 0 {
		return 0, corruptEvent(errors.New("SSE watermark is negative"))
	}
	return watermark, nil
}

// EarliestRetained 返回数据库当前时间下仍可重放的最早 Workspace 事件序号。
func (store *Store) EarliestRetained(ctx context.Context, workspaceID foundation.ID) (*int64, error) {
	if err := store.validateQuery(ctx, workspaceID); err != nil {
		return nil, err
	}
	var earliest *int64
	if err := store.db.QueryRow(ctx, `
		SELECT MIN(seq)
		FROM ops.server_event
		WHERE workspace_id=$1 AND expires_at > CURRENT_TIMESTAMP`, string(workspaceID)).Scan(&earliest); err != nil {
		return nil, queryFailure(err)
	}
	if earliest != nil && *earliest <= 0 {
		return nil, corruptEvent(errors.New("SSE earliest retained sequence is invalid"))
	}
	return earliest, nil
}

// ListAfter 按 seq 升序读取游标之后仍在保留窗口内的一页事件。
func (store *Store) ListAfter(ctx context.Context, workspaceID foundation.ID, afterSeq int64, limit int) ([]domain.ServerEvent, error) {
	if err := store.validateQuery(ctx, workspaceID); err != nil {
		return nil, err
	}
	if afterSeq < 0 || limit <= 0 || limit > domain.MaxReplayPageSize {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeReplayQueryInvalid, false, errors.New("SSE replay query boundary is invalid"))
	}
	rows, err := store.db.Query(ctx, eventSelect+`
		WHERE workspace_id=$1 AND seq>$2 AND expires_at > CURRENT_TIMESTAMP
		ORDER BY seq ASC
		LIMIT $3`, string(workspaceID), afterSeq, limit)
	if err != nil {
		return nil, queryFailure(err)
	}
	defer rows.Close()

	var events []domain.ServerEvent
	lastSequence := afterSeq
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if event.WorkspaceID != workspaceID || event.Seq <= lastSequence {
			return nil, corruptEvent(errors.New("SSE replay row scope or order is inconsistent"))
		}
		events = append(events, event)
		lastSequence = event.Seq
	}
	if err := rows.Err(); err != nil {
		return nil, queryFailure(err)
	}
	return events, nil
}

func (store *Store) validateQuery(ctx context.Context, workspaceID foundation.ID) error {
	if store == nil || isNilDB(store.db) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, errors.New("SSE database is unavailable"))
	}
	return validateReplayQuery(ctx, workspaceID)
}

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

func isNilDB(db DB) bool {
	if db == nil {
		return true
	}
	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ application.Store = (*Store)(nil)
