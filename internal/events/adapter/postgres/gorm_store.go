package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMStore persists and replays Server Events through the shared GORM connection.
type GORMStore struct {
	database *gorm.DB
}

// NewGORMStore derives its read root from one shared platform pool.
func NewGORMStore(pool *platformpostgres.Pool) (*GORMStore, error) {
	if pool == nil {
		return nil, eventStoreUnavailable(errors.New("SSE PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil || !validEventGORMDatabase(database) {
		return nil, eventStoreUnavailable(errors.New("SSE GORM database is unavailable"))
	}
	return &GORMStore{database: database}, nil
}

// CurrentWatermark returns the highest physical sequence for one Workspace,
// including rows just outside the logical retention window.
func (store *GORMStore) CurrentWatermark(ctx context.Context, workspaceID foundation.ID) (int64, error) {
	if err := store.validateQuery(ctx, workspaceID); err != nil {
		return 0, err
	}
	row, err := gormEventRawRow(store.database.WithContext(ctx), gormEventWatermarkSQL, string(workspaceID))
	if err != nil {
		return 0, classifyGORMEventFailure(ctx, err)
	}
	var watermark int64
	if err := row.Scan(&watermark); err != nil {
		return 0, classifyGORMEventFailure(ctx, err)
	}
	if watermark < 0 {
		return 0, corruptEvent(errors.New("SSE watermark is negative"))
	}
	return watermark, nil
}

// EarliestRetained returns the first sequence retained by PostgreSQL time.
func (store *GORMStore) EarliestRetained(ctx context.Context, workspaceID foundation.ID) (*int64, error) {
	if err := store.validateQuery(ctx, workspaceID); err != nil {
		return nil, err
	}
	row, err := gormEventRawRow(store.database.WithContext(ctx), gormEventEarliestRetainedSQL, string(workspaceID))
	if err != nil {
		return nil, classifyGORMEventFailure(ctx, err)
	}
	var value sql.NullInt64
	if err := row.Scan(&value); err != nil {
		return nil, classifyGORMEventFailure(ctx, err)
	}
	if !value.Valid {
		return nil, nil
	}
	if value.Int64 <= 0 {
		return nil, corruptEvent(errors.New("SSE earliest retained sequence is invalid"))
	}
	earliest := value.Int64
	return &earliest, nil
}

// ListAfter returns one retained, ascending, Workspace-scoped replay page.
func (store *GORMStore) ListAfter(ctx context.Context, workspaceID foundation.ID, afterSeq int64, limit int) ([]domain.ServerEvent, error) {
	if err := store.validateQuery(ctx, workspaceID); err != nil {
		return nil, err
	}
	if afterSeq < 0 || limit <= 0 || limit > domain.MaxReplayPageSize {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeReplayQueryInvalid, false, errors.New("SSE replay query boundary is invalid"))
	}
	rows, err := gormEventRawRows(store.database.WithContext(ctx), gormEventListAfterSQL, string(workspaceID), afterSeq, limit)
	if err != nil {
		return nil, classifyGORMEventFailure(ctx, err)
	}
	defer rows.Close()

	var events []domain.ServerEvent
	lastSequence := afterSeq
	for rows.Next() {
		event, scanErr := scanGORMEvent(ctx, rows)
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
		return nil, classifyGORMEventFailure(ctx, err)
	}
	return events, nil
}

// AppendScoped appends or exactly replays an event in the caller's opaque
// transaction. It never commits or rolls back that transaction.
func (store *GORMStore) AppendScoped(ctx context.Context, scope foundation.TransactionScope, request domain.AppendRequest) (domain.ServerEvent, bool, error) {
	if err := store.validateAppend(ctx); err != nil {
		return domain.ServerEvent{}, false, err
	}
	normalized, err := normalizeAppendRequest(request)
	if err != nil {
		return domain.ServerEvent{}, false, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeAppendTransactionUnavailable, true, errors.New("SSE append transaction is unavailable"))
	}
	summary, err := json.Marshal(normalized.PayloadSummary)
	if err != nil {
		return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendInvalid, false, errors.New("SSE payload summary cannot be encoded"))
	}
	row, err := gormEventRawRow(tx.WithContext(ctx), gormAppendEventSQL,
		string(normalized.WorkspaceID), optionalIDValue(normalized.ConversationID), optionalIDValue(normalized.WorkflowRunID),
		normalized.Type, normalized.ResourceRef, normalized.ResourceVersion, eventJSONB(summary), normalized.SchemaVersion,
		normalized.SourceEventRef, normalized.OccurredAt, normalized.OccurredAt,
	)
	if err != nil {
		return domain.ServerEvent{}, false, classifyGORMAppendFailure(ctx, err)
	}
	event, err := scanEventRaw(row)
	if err == nil {
		if !sameAppendBinding(event, normalized) {
			return domain.ServerEvent{}, false, appendConflict()
		}
		return event, false, nil
	}
	if !eventNoRows(err) {
		return domain.ServerEvent{}, false, classifyGORMAppendFailure(ctx, err)
	}

	existingRow, err := gormEventRawRow(tx.WithContext(ctx), gormEventBySourceSQL,
		string(normalized.WorkspaceID), normalized.SourceEventRef)
	if err != nil {
		return domain.ServerEvent{}, false, classifyGORMAppendFailure(ctx, err)
	}
	existing, err := scanEventRaw(existingRow)
	if err != nil {
		if eventNoRows(err) {
			return domain.ServerEvent{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendConflict, false, errors.New("SSE append conflict has no persisted event"))
		}
		return domain.ServerEvent{}, false, classifyGORMAppendFailure(ctx, err)
	}
	if !sameAppendBinding(existing, normalized) {
		return domain.ServerEvent{}, false, appendConflict()
	}
	return existing, true, nil
}

func scanGORMEvent(ctx context.Context, row scanner) (domain.ServerEvent, error) {
	event, err := scanEventRaw(row)
	if err == nil {
		return event, nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return domain.ServerEvent{}, err
	}
	return domain.ServerEvent{}, classifyGORMEventFailure(ctx, err)
}

func classifyGORMAppendFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if contextError := gormEventContextError(ctx, err); contextError != nil {
		return contextError
	}
	switch platformpostgres.SQLState(err) {
	case "23503", "23514":
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeAppendBindingInvalid, false, err)
	case "23505":
		return appendConflict()
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeStoreUnavailable, true, err)
	}
	return eventStoreUnavailable(fmt.Errorf("SSE database operation failed: %T", err))
}

func classifyGORMEventFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if contextError := gormEventContextError(ctx, err); contextError != nil {
		return contextError
	}
	return eventStoreUnavailable(fmt.Errorf("SSE database operation failed: %T", err))
}

func gormEventContextError(ctx context.Context, err error) error {
	var cause error
	if ctx != nil && ctx.Err() != nil {
		cause = context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		} else if !errors.Is(cause, ctx.Err()) {
			cause = errors.Join(ctx.Err(), cause)
		}
	} else if errors.Is(err, context.Canceled) {
		cause = context.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	} else if errors.Is(err, sql.ErrTxDone) {
		cause = err
	}
	if cause == nil {
		return nil
	}
	return eventStoreUnavailable(cause)
}

func eventNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func eventStoreUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, cause)
}

func (store *GORMStore) validateQuery(ctx context.Context, workspaceID foundation.ID) error {
	if store == nil || !validEventGORMDatabase(store.database) {
		return eventStoreUnavailable(errors.New("SSE GORM store is unavailable"))
	}
	return validateReplayQuery(ctx, workspaceID)
}

func (store *GORMStore) validateAppend(ctx context.Context) error {
	if store == nil || !validEventGORMDatabase(store.database) {
		return eventStoreUnavailable(errors.New("SSE GORM store is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeAppendInvalid, false, errors.New("SSE append context is nil"))
	}
	return nil
}

func validEventGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

var _ application.Store = (*GORMStore)(nil)
var _ application.ScopedStore = (*GORMStore)(nil)
