package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMRepository is the staged GORM implementation of Memory persistence.
// Production composition remains on Repository until the PostgreSQL gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository constructs a Memory repository from one shared platform
// GORM root and its matching Unit of Work.
func NewGORMRepository(database *gorm.DB, unitOfWork foundation.UnitOfWork) (*GORMRepository, error) {
	if !validMemoryGORMDatabase(database) {
		return nil, unavailable(errors.New("memory GORM database is unavailable"))
	}
	if isNilMemoryUnitOfWork(unitOfWork) {
		return nil, unavailable(errors.New("memory GORM unit of work is unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

// FindCommand loads an exact command replay while holding the command lock.
func (repository *GORMRepository) FindCommand(ctx context.Context, binding memoryapp.CommandBinding) (memoryapp.CommandResult, bool, error) {
	if err := repository.ready(); err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	if ctx == nil || validateBinding(binding) != nil {
		return memoryapp.CommandResult{}, false, invalid(errors.New("memory command binding is invalid"))
	}
	var result memoryapp.CommandResult
	found := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(callbackCtx, tx, binding.WorkspaceID, binding.IdempotencyKey); err != nil {
			return err
		}
		command, commandFound, err := gormLoadCommand(callbackCtx, tx, binding.WorkspaceID, binding.IdempotencyKey)
		if err != nil || !commandFound {
			return err
		}
		if !commandMatches(command, binding) {
			return idempotencyConflict(errors.New("memory idempotency key is bound to a different command"))
		}
		result, err = commandResult(command)
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	return result, found, nil
}

// CreateCandidate persists a candidate, audit fact, and command receipt in one
// shared GORM transaction.
func (repository *GORMRepository) CreateCandidate(ctx context.Context, record memoryapp.CandidateRecord) (memoryapp.CommandResult, error) {
	if err := repository.ready(); err != nil {
		return memoryapp.CommandResult{}, err
	}
	if ctx == nil || validateCandidateRecord(record) != nil {
		return memoryapp.CommandResult{}, invalid(errors.New("memory candidate record is invalid"))
	}
	var result memoryapp.CommandResult
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(callbackCtx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		replayed, found, err := gormReplayCommand(callbackCtx, tx, record.Binding)
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}
		if record.Memory.Source.Type == domain.SourceInterview {
			if err := gormLockInterviewProvenance(callbackCtx, tx, record.Memory); err != nil {
				return err
			}
			replayed, found, err = gormReplayInterviewCandidate(callbackCtx, tx, record)
			if err != nil {
				return err
			}
			if found {
				result = replayed
				return nil
			}
		}
		if err := gormLockWorkspace(callbackCtx, tx, record.Memory.WorkspaceID); err != nil {
			return err
		}
		persisted, err := gormInsertCandidate(callbackCtx, tx, record.Memory)
		if err != nil {
			return err
		}
		if !sameMemory(persisted, record.Memory) {
			return inconsistent(errors.New("created memory differs from candidate record"))
		}
		if err := gormInsertAudit(callbackCtx, tx, persisted, domain.AuditCandidateCreated, nil, nil, record.Binding); err != nil {
			return err
		}
		result = memoryapp.CommandResult{Memory: persisted}
		return gormInsertCommand(callbackCtx, tx, record.Binding, result)
	})
	if err != nil {
		return memoryapp.CommandResult{}, err
	}
	return result, nil
}

// Mutate applies one owner-scoped CAS update with its audit fact and receipt.
func (repository *GORMRepository) Mutate(ctx context.Context, record memoryapp.MutationRecord) (memoryapp.CommandResult, error) {
	if err := repository.ready(); err != nil {
		return memoryapp.CommandResult{}, err
	}
	if ctx == nil || validateMutationRecord(record) != nil {
		return memoryapp.CommandResult{}, invalid(errors.New("memory mutation record is invalid"))
	}
	var result memoryapp.CommandResult
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(callbackCtx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		replayed, found, err := gormReplayCommand(callbackCtx, tx, record.Binding)
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}
		persisted, err := gormLoadOwnedMemory(callbackCtx, tx, record.Binding.WorkspaceID, record.Binding.Owner, record.Binding.MemoryID, true)
		if err != nil {
			return err
		}
		if !sameMemory(persisted, record.Current) || persisted.Version != record.Binding.ExpectedVersion {
			return versionConflict(errors.New("memory compare-and-swap state is stale"))
		}
		if record.Next.Status == domain.StatusActive && record.Next.ExpiresAt != nil {
			now, err := gormDatabaseNow(callbackCtx, tx)
			if err != nil {
				return err
			}
			if !record.Next.ExpiresAt.After(now) {
				return domain.ExpiredError("memory expired before active transition committed")
			}
		}
		updated, err := gormUpdateMemory(callbackCtx, tx, record.Next, record.Binding.ExpectedVersion)
		if err != nil {
			return err
		}
		if !sameMemory(updated, record.Next) {
			return inconsistent(errors.New("updated memory differs from mutation record"))
		}
		if err := gormInsertAudit(callbackCtx, tx, updated, record.Action, &record.Current.Status, record.Actor, record.Binding); err != nil {
			return err
		}
		result = memoryapp.CommandResult{Memory: updated}
		semanticBinding := record.Binding
		semanticBinding.MemoryID = result.Memory.ID
		return gormInsertCommand(callbackCtx, tx, semanticBinding, result)
	})
	if err != nil {
		return memoryapp.CommandResult{}, err
	}
	return result, nil
}

// Get returns one owner-scoped Memory record.
func (repository *GORMRepository) Get(ctx context.Context, scope memoryapp.Scope, memoryID foundation.ID) (domain.Memory, error) {
	if err := repository.ready(); err != nil {
		return domain.Memory{}, err
	}
	if ctx == nil || validateScope(scope) != nil || !validID(memoryID) {
		return domain.Memory{}, invalid(errors.New("memory get scope or id is invalid"))
	}
	return gormLoadOwnedMemory(ctx, repository.database.WithContext(ctx), scope.WorkspaceID, scope.Owner, memoryID, false)
}

// List returns one stable keyset page within an exact owner scope.
func (repository *GORMRepository) List(ctx context.Context, query memoryapp.ListQuery) (memoryapp.ListPage, error) {
	if err := repository.ready(); err != nil {
		return memoryapp.ListPage{}, err
	}
	if ctx == nil || validateListQuery(query) != nil {
		return memoryapp.ListPage{}, invalid(errors.New("memory list query is invalid"))
	}
	types := pq.Array(stringsFromTypes(query.Types))
	statuses := pq.Array(stringsFromStatuses(query.Statuses))
	var cursorTime, cursorID any
	if query.After != nil {
		cursorTime = query.After.UpdatedAt.UTC().Truncate(time.Microsecond)
		cursorID = string(query.After.ID)
	}
	items, err := gormListMemories(ctx, repository.database.WithContext(ctx), gormListMemorySQL,
		string(query.Scope.WorkspaceID), string(query.Scope.Owner.Kind), string(query.Scope.Owner.ID),
		types, types, statuses, statuses, cursorTime, cursorTime, cursorID, query.Limit+1)
	if err != nil {
		return memoryapp.ListPage{}, err
	}
	page := memoryapp.ListPage{Items: items}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &memoryapp.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// LoadEffective returns confirmed active records visible to one task scope.
func (repository *GORMRepository) LoadEffective(ctx context.Context, query memoryapp.EffectiveQuery) ([]domain.Memory, error) {
	if err := repository.ready(); err != nil {
		return nil, err
	}
	if ctx == nil || validateEffectiveQuery(query) != nil {
		return nil, invalid(errors.New("effective memory query is invalid"))
	}
	return gormListMemories(ctx, repository.database.WithContext(ctx), gormLoadEffectiveMemorySQL,
		string(query.Scope.WorkspaceID), string(query.Scope.Owner.Kind), string(query.Scope.Owner.ID), optionalIDValue(query.TaskScopeID), query.Limit)
}

// ExpireDue transitions one bounded locked batch and appends its audit facts.
func (repository *GORMRepository) ExpireDue(ctx context.Context, limit int) (int, error) {
	if err := repository.ready(); err != nil {
		return 0, err
	}
	if ctx == nil || limit < 1 || limit > domain.MaxListLimit {
		return 0, invalid(errors.New("memory expiry limit is invalid"))
	}
	count := 0
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		now, err := gormDatabaseNow(callbackCtx, tx)
		if err != nil {
			return err
		}
		items, err := gormListMemories(callbackCtx, tx, gormExpireDueMemorySQL, now, limit)
		if err != nil {
			return err
		}
		for _, current := range items {
			next, err := domain.Expire(current, now)
			if err != nil {
				return err
			}
			updated, err := gormUpdateMemory(callbackCtx, tx, next, current.Version)
			if err != nil {
				return err
			}
			if err := gormInsertAudit(callbackCtx, tx, updated, domain.AuditExpired, &current.Status, nil, memoryapp.CommandBinding{}); err != nil {
				return err
			}
		}
		count = len(items)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (repository *GORMRepository) ready() error {
	if repository == nil || !validMemoryGORMDatabase(repository.database) || isNilMemoryUnitOfWork(repository.unitOfWork) {
		return unavailable(errors.New("memory GORM repository is unavailable"))
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, work func(context.Context, *gorm.DB) error) error {
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(callbackCtx, tx.WithContext(callbackCtx))
	})
	return gormMemoryClassify(ctx, err)
}

func gormInsertCandidate(ctx context.Context, tx *gorm.DB, memory domain.Memory) (domain.Memory, error) {
	content, err := jsonMemoryContent(memory)
	if err != nil {
		return domain.Memory{}, err
	}
	persisted, err := gormScanMemory(tx.WithContext(ctx), gormInsertCandidateSQL,
		string(memory.ID), string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), string(memory.Type), memoryJSONB(content),
		string(memory.Source.Type), memory.Source.Ref, optionalIDValue(memory.TaskScopeID), string(memory.Status), optionalTimeValue(memory.ExpiresAt),
		optionalTimeValue(memory.ConfirmedAt), optionalPrincipalKind(memory.ConfirmedBy), optionalPrincipalID(memory.ConfirmedBy), memory.Version,
		memory.CreatedAt.UTC().Truncate(time.Microsecond), memory.UpdatedAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		return domain.Memory{}, gormMemoryClassify(ctx, err)
	}
	return persisted, nil
}

func gormUpdateMemory(ctx context.Context, tx *gorm.DB, memory domain.Memory, expectedVersion int64) (domain.Memory, error) {
	content, err := jsonMemoryContent(memory)
	if err != nil {
		return domain.Memory{}, err
	}
	persisted, err := gormScanMemory(tx.WithContext(ctx), gormUpdateMemorySQL,
		memoryJSONB(content), string(memory.Source.Type), memory.Source.Ref, optionalIDValue(memory.TaskScopeID), string(memory.Status), optionalTimeValue(memory.ExpiresAt),
		optionalTimeValue(memory.ConfirmedAt), optionalPrincipalKind(memory.ConfirmedBy), optionalPrincipalID(memory.ConfirmedBy), memory.Version,
		memory.UpdatedAt.UTC().Truncate(time.Microsecond), string(memory.WorkspaceID), string(memory.ID), string(memory.Owner.Kind), string(memory.Owner.ID), expectedVersion)
	if gormMemoryNoRows(err) {
		return domain.Memory{}, versionConflict(errors.New("memory compare-and-swap did not update a row"))
	}
	if err != nil {
		return domain.Memory{}, gormMemoryClassify(ctx, err)
	}
	return persisted, nil
}

func gormLoadOwnedMemory(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, owner domain.Principal, memoryID foundation.ID, lock bool) (domain.Memory, error) {
	query := gormGetOwnedMemorySQL
	if lock {
		query = gormLockOwnedMemorySQL
	}
	memory, err := gormScanMemory(database.WithContext(ctx), query, string(workspaceID), string(memoryID), string(owner.Kind), string(owner.ID))
	if gormMemoryNoRows(err) {
		return domain.Memory{}, notFound(errors.New("memory was not found in owner scope"))
	}
	if err != nil {
		return domain.Memory{}, gormMemoryClassify(ctx, err)
	}
	return memory, nil
}

func gormReplayCommand(ctx context.Context, tx *gorm.DB, binding memoryapp.CommandBinding) (memoryapp.CommandResult, bool, error) {
	command, found, err := gormLoadCommand(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return memoryapp.CommandResult{}, found, err
	}
	if !commandMatches(command, binding) {
		return memoryapp.CommandResult{}, false, idempotencyConflict(errors.New("memory idempotency key is bound to a different command"))
	}
	result, err := commandResult(command)
	return result, true, err
}

func gormLoadCommand(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) (persistedCommand, bool, error) {
	row, err := gormMemoryRawRow(tx.WithContext(ctx), gormMemoryCommandSQL, string(workspaceID), key)
	if err != nil {
		return persistedCommand{}, false, gormMemoryClassify(ctx, err)
	}
	command, err := scanPersistedCommand(row, workspaceID, "memory command row is invalid")
	if gormMemoryNoRows(err) {
		return persistedCommand{}, false, nil
	}
	if err != nil {
		return persistedCommand{}, false, gormMemoryClassify(ctx, err)
	}
	return command, true, nil
}

func gormReplayInterviewCandidate(ctx context.Context, tx *gorm.DB, record memoryapp.CandidateRecord) (memoryapp.CommandResult, bool, error) {
	command, found, err := gormLoadInterviewCandidateCommand(ctx, tx, record.Memory)
	if err != nil || !found {
		return memoryapp.CommandResult{}, found, err
	}
	if command.requestHash != record.Binding.RequestHash {
		return memoryapp.CommandResult{}, false, idempotencyConflict(errors.New("interview provenance is bound to a different memory candidate request"))
	}
	result, err := commandResult(command)
	if err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	if !sameInterviewCandidate(result.Memory, record.Memory) {
		return memoryapp.CommandResult{}, false, inconsistent(errors.New("interview candidate receipt does not match requested provenance"))
	}
	semanticBinding := record.Binding
	semanticBinding.MemoryID = result.Memory.ID
	if err := gormInsertCommand(ctx, tx, semanticBinding, result); err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func gormLoadInterviewCandidateCommand(ctx context.Context, tx *gorm.DB, memory domain.Memory) (persistedCommand, bool, error) {
	row, err := gormMemoryRawRow(tx.WithContext(ctx), gormInterviewCandidateCommandSQL,
		string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), memory.Source.Ref)
	if err != nil {
		return persistedCommand{}, false, gormMemoryClassify(ctx, err)
	}
	command, err := scanPersistedCommand(row, memory.WorkspaceID, "interview candidate command row is invalid")
	if gormMemoryNoRows(err) {
		return persistedCommand{}, false, nil
	}
	if err != nil {
		return persistedCommand{}, false, gormMemoryClassify(ctx, err)
	}
	return command, true, nil
}

func gormInsertCommand(ctx context.Context, tx *gorm.DB, binding memoryapp.CommandBinding, result memoryapp.CommandResult) error {
	snapshot, err := encodeMemorySnapshot(result.Memory)
	if err != nil {
		return err
	}
	err = tx.WithContext(ctx).Exec(gormInsertMemoryCommandSQL,
		string(binding.WorkspaceID), binding.IdempotencyKey, string(binding.Owner.Kind), string(binding.Owner.ID), binding.RequestHash,
		string(binding.CommandType), string(result.Memory.ID), binding.ExpectedVersion, result.Memory.Version, memoryJSONB(snapshot),
		result.Memory.UpdatedAt.UTC().Truncate(time.Microsecond)).Error
	return gormMemoryClassify(ctx, err)
}

func gormInsertAudit(ctx context.Context, tx *gorm.DB, memory domain.Memory, action domain.AuditAction, from *domain.Status, actor *domain.Principal, binding memoryapp.CommandBinding) error {
	actorKind, actorID := auditActor(memory, action, actor)
	var fromStatus any
	if from != nil {
		fromStatus = string(*from)
	}
	var key, hash any
	if binding.IdempotencyKey != "" {
		key, hash = binding.IdempotencyKey, binding.RequestHash
	}
	err := tx.WithContext(ctx).Exec(gormInsertMemoryAuditSQL,
		string(memory.WorkspaceID), string(memory.ID), string(memory.Owner.Kind), string(memory.Owner.ID), actorKind, actorID,
		string(action), fromStatus, string(memory.Status), memory.Version, key, hash, memory.UpdatedAt.UTC().Truncate(time.Microsecond)).Error
	return gormMemoryClassify(ctx, err)
}

func gormDatabaseNow(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	row, err := gormMemoryRawRow(tx.WithContext(ctx), gormDatabaseNowSQL)
	if err != nil {
		return time.Time{}, gormMemoryClassify(ctx, err)
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, gormMemoryClassify(ctx, err)
	}
	return now.UTC().Truncate(time.Microsecond), nil
}

func gormLockCommand(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) error {
	return gormMemoryClassify(ctx, tx.WithContext(ctx).Exec(gormLockCommandSQL, string(workspaceID), key).Error)
}

func gormLockInterviewProvenance(ctx context.Context, tx *gorm.DB, memory domain.Memory) error {
	return gormMemoryClassify(ctx, tx.WithContext(ctx).Exec(gormLockInterviewProvenanceSQL,
		string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), string(memory.Source.Type), memory.Source.Ref).Error)
}

func gormLockWorkspace(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID) error {
	row, err := gormMemoryRawRow(tx.WithContext(ctx), gormLockWorkspaceSQL, string(workspaceID))
	if err != nil {
		return gormMemoryClassify(ctx, err)
	}
	var persisted string
	if err := row.Scan(&persisted); gormMemoryNoRows(err) {
		return notFound(errors.New("memory workspace was not found"))
	} else if err != nil {
		return gormMemoryClassify(ctx, err)
	}
	return nil
}

func gormScanMemory(database *gorm.DB, query string, args ...any) (domain.Memory, error) {
	row, err := gormMemoryRawRow(database, query, args...)
	if err != nil {
		return domain.Memory{}, err
	}
	return scanMemory(row)
}

func gormListMemories(ctx context.Context, database *gorm.DB, query string, args ...any) ([]domain.Memory, error) {
	rows, err := gormMemoryRawRows(database, query, args...)
	if err != nil {
		return nil, gormMemoryClassify(ctx, err)
	}
	defer rows.Close()
	items := make([]domain.Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, gormMemoryClassify(ctx, err)
		}
		items = append(items, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, gormMemoryClassify(ctx, err)
	}
	return items, nil
}

func gormMemoryRawRow(database *gorm.DB, query string, args ...any) (scanner, error) {
	if database == nil {
		return nil, errors.New("memory GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("memory GORM query returned a nil row")
	}
	return row, nil
}

func gormMemoryRawRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if database == nil {
		return nil, errors.New("memory GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("memory GORM query returned nil rows")
	}
	return rows, nil
}

func gormMemoryNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormMemoryClassify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, domain.ErrorCodeUnavailable, false, ctx.Err())
		}
		return unavailable(ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, domain.ErrorCodeUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailable(err)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return unavailable(err)
	}
	if gormMemoryNoRows(err) {
		return notFound(err)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return idempotencyConflict(err)
		case "23503", "23514", "23502", "22P02", "55000":
			return inconsistent(err)
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01", "57014":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, err)
		}
	}
	return unavailable(err)
}

func validMemoryGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func isNilMemoryUnitOfWork(unitOfWork foundation.UnitOfWork) bool {
	if unitOfWork == nil {
		return true
	}
	reflected := reflect.ValueOf(unitOfWork)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ memoryapp.Repository = (*GORMRepository)(nil)
