package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMStore persists append-only Audit facts through the shared GORM connection.
type GORMStore struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMStore constructs an Audit Store from one shared platform pool so its
// read root and Store-owned transaction boundary cannot use different databases.
func NewGORMStore(pool *platformpostgres.Pool) (*GORMStore, error) {
	if pool == nil {
		return nil, domainErrorUnavailable(errors.New("audit PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, domainErrorUnavailable(errors.New("audit GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, domainErrorUnavailable(errors.New("audit GORM unit of work is unavailable"))
	}
	if !validAuditGORMDatabase(database) || nilAuditUnitOfWork(unitOfWork) {
		return nil, domainErrorUnavailable(errors.New("audit GORM dependencies are unavailable"))
	}
	return &GORMStore{database: database, unitOfWork: unitOfWork}, nil
}

// Append appends an event in a Store-owned shared GORM transaction.
func (store *GORMStore) Append(ctx context.Context, event domain.Event) (persisted domain.Event, replayed bool, err error) {
	if err = store.validateAppend(ctx); err != nil {
		return domain.Event{}, false, err
	}
	err = store.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		var appendErr error
		persisted, replayed, appendErr = store.AppendScoped(callbackCtx, scope, event)
		return appendErr
	})
	if err != nil {
		return domain.Event{}, false, classifyGORMStoreError(ctx, err)
	}
	return persisted, replayed, nil
}

// AppendScoped appends or exactly replays an event in the caller's opaque
// transaction. It never commits or rolls back that transaction.
func (store *GORMStore) AppendScoped(ctx context.Context, scope foundation.TransactionScope, event domain.Event) (domain.Event, bool, error) {
	if err := store.validateAppend(ctx); err != nil {
		return domain.Event{}, false, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable, true, errors.New("audit scoped transaction is unavailable"))
	}
	return appendGORMAudit(ctx, tx, event)
}

// Get loads one safely decoded Audit event by ID.
func (store *GORMStore) Get(ctx context.Context, id foundation.ID) (domain.Event, error) {
	if err := store.validateQuery(ctx); err != nil {
		return domain.Event{}, err
	}
	return getGORMAudit(ctx, store.database, id)
}

// GetScoped loads one immutable Audit event from the caller's opaque
// transaction. It never commits or rolls back that transaction.
func (store *GORMStore) GetScoped(ctx context.Context, scope foundation.TransactionScope, id foundation.ID) (domain.Event, error) {
	if err := store.validateQuery(ctx); err != nil {
		return domain.Event{}, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.Event{}, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable, true, errors.New("audit scoped transaction is unavailable"))
	}
	return getGORMAudit(ctx, tx, id)
}

func getGORMAudit(ctx context.Context, database *gorm.DB, id foundation.ID) (domain.Event, error) {
	parsed, err := foundation.ParseID(string(id))
	if err != nil || parsed != id {
		return domain.Event{}, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit id is invalid"))
	}
	row, err := gormAuditRawRow(database.WithContext(ctx), gormAuditGetSQL, string(id))
	if err != nil {
		return domain.Event{}, classifyGORMStoreError(ctx, err)
	}
	event, err := scanGORMAuditEvent(ctx, row)
	if auditNoRows(err) {
		return domain.Event{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, errors.New("audit event was not found"))
	}
	return event, err
}

// List returns one bounded, stably ordered Workspace or global Audit page.
func (store *GORMStore) List(ctx context.Context, query domain.ListQuery) ([]domain.Event, error) {
	if err := store.validateQuery(ctx); err != nil {
		return nil, err
	}
	workspace, beforeID, err := auditListArguments(query)
	if err != nil {
		return nil, err
	}
	before := nullableTime(query.Before)
	rows, err := gormAuditRawRows(store.database.WithContext(ctx), gormAuditListSQL,
		workspace, before, before, beforeID, query.Limit)
	if err != nil {
		return nil, classifyGORMStoreError(ctx, err)
	}
	result := make([]domain.Event, 0, query.Limit)
	for rows.Next() {
		event, scanErr := scanGORMAuditEvent(ctx, rows)
		if scanErr != nil {
			if closeErr := rows.Close(); closeErr != nil {
				return nil, errors.Join(scanErr, classifyGORMStoreError(ctx, closeErr))
			}
			return nil, scanErr
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		classified := classifyGORMStoreError(ctx, err)
		if closeErr := rows.Close(); closeErr != nil {
			return nil, errors.Join(classified, classifyGORMStoreError(ctx, closeErr))
		}
		return nil, classified
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, classifyGORMStoreError(ctx, closeErr)
	}
	return result, nil
}

func appendGORMAudit(ctx context.Context, tx *gorm.DB, event domain.Event) (domain.Event, bool, error) {
	redacted, err := event.Redacted()
	if err != nil {
		return domain.Event{}, false, err
	}
	correlationJSON, payloadJSON, err := encodeAuditJSON(redacted)
	if err != nil {
		return domain.Event{}, false, err
	}
	if result := tx.WithContext(ctx).Exec(gormAuditLockSQL, auditLockScope(redacted)); result.Error != nil {
		return domain.Event{}, false, classifyGORMStoreError(ctx, result.Error)
	}
	if existing, found, lookupErr := loadGORMByIdempotency(ctx, tx, redacted.WorkspaceID, redacted.IdempotencyKey); lookupErr != nil {
		return domain.Event{}, false, lookupErr
	} else if found {
		if !domain.EqualBinding(existing, redacted) {
			return domain.Event{}, false, appendConflict()
		}
		return existing, true, nil
	}

	row, err := gormAuditRawRow(tx.WithContext(ctx), gormAppendAuditSQL,
		string(redacted.ID), optionalWorkspaceValue(redacted.WorkspaceID), string(redacted.ActorType),
		optionalText(redacted.ActorRef), redacted.Action, optionalText(redacted.ResourceType),
		optionalText(redacted.ResourceRef), redacted.IdempotencyKey, auditJSONB(correlationJSON),
		auditJSONB(payloadJSON), redacted.OccurredAt)
	if err != nil {
		return domain.Event{}, false, classifyGORMAppendFailure(ctx, err)
	}
	created, err := scanGORMAuditEvent(ctx, row)
	if err == nil {
		if created.ID != redacted.ID {
			return domain.Event{}, false, corruptEvent(errors.New("audit insert returned a different event id"))
		}
		if !domain.EqualBinding(created, redacted) {
			return domain.Event{}, false, appendConflict()
		}
		return created, false, nil
	}
	if !auditNoRows(err) {
		return domain.Event{}, false, classifyGORMAppendFailure(ctx, err)
	}
	existing, found, lookupErr := loadGORMByIdempotency(ctx, tx, redacted.WorkspaceID, redacted.IdempotencyKey)
	if lookupErr != nil {
		return domain.Event{}, false, lookupErr
	}
	if !found || !domain.EqualBinding(existing, redacted) {
		return domain.Event{}, false, appendConflict()
	}
	return existing, true, nil
}

func loadGORMByIdempotency(ctx context.Context, tx *gorm.DB, workspaceID *foundation.ID, key string) (domain.Event, bool, error) {
	rows, err := gormAuditRawRows(tx.WithContext(ctx), gormAuditByIdempotencySQL, optionalWorkspaceValue(workspaceID), key)
	if err != nil {
		return domain.Event{}, false, classifyGORMStoreError(ctx, err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			classified := classifyGORMStoreError(ctx, err)
			if closeErr := rows.Close(); closeErr != nil {
				return domain.Event{}, false, errors.Join(classified, classifyGORMStoreError(ctx, closeErr))
			}
			return domain.Event{}, false, classified
		}
		if closeErr := rows.Close(); closeErr != nil {
			return domain.Event{}, false, classifyGORMStoreError(ctx, closeErr)
		}
		return domain.Event{}, false, nil
	}
	event, err := scanGORMAuditEvent(ctx, rows)
	if err != nil {
		if closeErr := rows.Close(); closeErr != nil {
			return domain.Event{}, false, errors.Join(err, classifyGORMStoreError(ctx, closeErr))
		}
		return domain.Event{}, false, err
	}
	if rows.Next() {
		duplicateErr := corruptEvent(errors.New("audit idempotency key has duplicate rows"))
		if closeErr := rows.Close(); closeErr != nil {
			return domain.Event{}, false, errors.Join(duplicateErr, classifyGORMStoreError(ctx, closeErr))
		}
		return domain.Event{}, false, duplicateErr
	}
	if err := rows.Err(); err != nil {
		classified := classifyGORMStoreError(ctx, err)
		if closeErr := rows.Close(); closeErr != nil {
			return domain.Event{}, false, errors.Join(classified, classifyGORMStoreError(ctx, closeErr))
		}
		return domain.Event{}, false, classified
	}
	if closeErr := rows.Close(); closeErr != nil {
		return domain.Event{}, false, classifyGORMStoreError(ctx, closeErr)
	}
	return event, true, nil
}

func scanGORMAuditEvent(ctx context.Context, row scanner) (domain.Event, error) {
	event, err := scanEventRaw(row)
	if err == nil || auditNoRows(err) {
		return event, err
	}
	return domain.Event{}, classifyGORMStoreError(ctx, err)
}

func classifyGORMAppendFailure(ctx context.Context, err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if contextError := gormAuditContextError(ctx, err); contextError != nil {
		return contextError
	}
	return classifyAppendFailure(err)
}

func classifyGORMStoreError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if contextError := gormAuditContextError(ctx, err); contextError != nil {
		return contextError
	}
	return classifyStoreError(err)
}

func gormAuditContextError(ctx context.Context, err error) error {
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
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, cause)
}

func (store *GORMStore) validateAppend(ctx context.Context) error {
	if store == nil || !validAuditGORMDatabase(store.database) || nilAuditUnitOfWork(store.unitOfWork) {
		return domainErrorUnavailable(errors.New("audit GORM store is unavailable"))
	}
	if ctx == nil {
		return domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit append context is nil"))
	}
	return nil
}

func (store *GORMStore) validateQuery(ctx context.Context) error {
	if store == nil || !validAuditGORMDatabase(store.database) || nilAuditUnitOfWork(store.unitOfWork) {
		return domainErrorUnavailable(errors.New("audit GORM database is unavailable"))
	}
	if ctx == nil {
		return domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit query context is nil"))
	}
	return nil
}

func validAuditGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilAuditUnitOfWork(unitOfWork foundation.UnitOfWork) bool {
	if unitOfWork == nil {
		return true
	}
	value := reflect.ValueOf(unitOfWork)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ application.Repository = (*GORMStore)(nil)
var _ application.ScopedStore = (*GORMStore)(nil)
