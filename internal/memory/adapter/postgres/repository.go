// Package postgres provides the parameterized PostgreSQL Memory repository.
package postgres

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	"github.com/jackc/pgx/v5"
)

// DB is the minimal pgx boundary required by the Memory repository.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository persists Memory records, receipts, and append-only audit facts.
type Repository struct{ db DB }

// NewRepository creates a Memory repository without opening or migrating a database.
func NewRepository(db DB) (*Repository, error) {
	if isNilDB(db) {
		return nil, unavailable(errors.New("memory database is nil"))
	}
	return &Repository{db: db}, nil
}

// FindCommand loads an exact command replay before the caller reads mutable aggregate state.
func (repository *Repository) FindCommand(ctx context.Context, binding memoryapp.CommandBinding) (memoryapp.CommandResult, bool, error) {
	if repository == nil || isNilDB(repository.db) {
		return memoryapp.CommandResult{}, false, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || validateBinding(binding) != nil {
		return memoryapp.CommandResult{}, false, invalid(errors.New("memory command binding is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return memoryapp.CommandResult{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockCommand(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey); err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	command, found, err := loadCommand(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return memoryapp.CommandResult{}, false, classify(err)
		}
		return memoryapp.CommandResult{}, false, nil
	}
	if !commandMatches(command, binding) {
		return memoryapp.CommandResult{}, false, idempotencyConflict(errors.New("memory idempotency key is bound to a different command"))
	}
	result, err := commandResult(command)
	if err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memoryapp.CommandResult{}, false, classify(err)
	}
	return result, true, nil
}

// CreateCandidate persists a CANDIDATE plus receipt and audit fact atomically.
func (repository *Repository) CreateCandidate(ctx context.Context, record memoryapp.CandidateRecord) (memoryapp.CommandResult, error) {
	if repository == nil || isNilDB(repository.db) {
		return memoryapp.CommandResult{}, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || validateCandidateRecord(record) != nil {
		return memoryapp.CommandResult{}, invalid(errors.New("memory candidate record is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return memoryapp.CommandResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
		return memoryapp.CommandResult{}, err
	}
	if result, found, err := replayCommand(ctx, tx, record.Binding); err != nil || found {
		if err != nil {
			return memoryapp.CommandResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return memoryapp.CommandResult{}, classify(err)
		}
		return result, nil
	}
	if record.Memory.Source.Type == domain.SourceInterview {
		if err := lockInterviewProvenance(ctx, tx, record.Memory); err != nil {
			return memoryapp.CommandResult{}, err
		}
		if result, found, err := replayInterviewCandidate(ctx, tx, record); err != nil || found {
			if err != nil {
				return memoryapp.CommandResult{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return memoryapp.CommandResult{}, classify(err)
			}
			return result, nil
		}
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR KEY SHARE`, string(record.Memory.WorkspaceID)).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return memoryapp.CommandResult{}, notFound(errors.New("memory workspace was not found"))
		}
		return memoryapp.CommandResult{}, classify(err)
	}
	persisted, err := insertCandidate(ctx, tx, record.Memory)
	if err != nil {
		return memoryapp.CommandResult{}, err
	}
	if !sameMemory(persisted, record.Memory) {
		return memoryapp.CommandResult{}, inconsistent(errors.New("created memory differs from candidate record"))
	}
	if err := insertAudit(ctx, tx, persisted, domain.AuditCandidateCreated, nil, nil, record.Binding); err != nil {
		return memoryapp.CommandResult{}, err
	}
	result := memoryapp.CommandResult{Memory: persisted}
	if err := insertCommand(ctx, tx, record.Binding, result); err != nil {
		return memoryapp.CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memoryapp.CommandResult{}, classify(err)
	}
	return result, nil
}

// Mutate performs a versioned lifecycle update, append-only audit write, and receipt write in one transaction.
func (repository *Repository) Mutate(ctx context.Context, record memoryapp.MutationRecord) (memoryapp.CommandResult, error) {
	if repository == nil || isNilDB(repository.db) {
		return memoryapp.CommandResult{}, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || validateMutationRecord(record) != nil {
		return memoryapp.CommandResult{}, invalid(errors.New("memory mutation record is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return memoryapp.CommandResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
		return memoryapp.CommandResult{}, err
	}
	if result, found, err := replayCommand(ctx, tx, record.Binding); err != nil || found {
		if err != nil {
			return memoryapp.CommandResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return memoryapp.CommandResult{}, classify(err)
		}
		return result, nil
	}
	persisted, err := loadOwnedMemory(ctx, tx, record.Binding.WorkspaceID, record.Binding.Owner, record.Binding.MemoryID, true)
	if err != nil {
		return memoryapp.CommandResult{}, err
	}
	if !sameMemory(persisted, record.Current) || persisted.Version != record.Binding.ExpectedVersion {
		return memoryapp.CommandResult{}, versionConflict(errors.New("memory compare-and-swap state is stale"))
	}
	if record.Next.Status == domain.StatusActive && record.Next.ExpiresAt != nil {
		now, err := databaseNow(ctx, tx)
		if err != nil {
			return memoryapp.CommandResult{}, err
		}
		if !record.Next.ExpiresAt.After(now) {
			return memoryapp.CommandResult{}, domain.ExpiredError("memory expired before active transition committed")
		}
	}
	updated, err := updateMemory(ctx, tx, record.Next, record.Binding.ExpectedVersion)
	if err != nil {
		return memoryapp.CommandResult{}, err
	}
	if !sameMemory(updated, record.Next) {
		return memoryapp.CommandResult{}, inconsistent(errors.New("updated memory differs from mutation record"))
	}
	if err := insertAudit(ctx, tx, updated, record.Action, &record.Current.Status, record.Actor, record.Binding); err != nil {
		return memoryapp.CommandResult{}, err
	}
	result := memoryapp.CommandResult{Memory: updated}
	semanticBinding := record.Binding
	semanticBinding.MemoryID = result.Memory.ID
	if err := insertCommand(ctx, tx, semanticBinding, result); err != nil {
		return memoryapp.CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memoryapp.CommandResult{}, classify(err)
	}
	return result, nil
}

// Get returns a Memory record only under the exact workspace and owner principal scope.
func (repository *Repository) Get(ctx context.Context, scope memoryapp.Scope, memoryID foundation.ID) (domain.Memory, error) {
	if repository == nil || isNilDB(repository.db) {
		return domain.Memory{}, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || validateScope(scope) != nil || !validID(memoryID) {
		return domain.Memory{}, invalid(errors.New("memory get scope or id is invalid"))
	}
	memory, err := loadOwnedMemory(ctx, repository.db, scope.WorkspaceID, scope.Owner, memoryID, false)
	if err != nil {
		return domain.Memory{}, err
	}
	return memory, nil
}

// List returns one keyset page under an exact workspace and owner principal scope.
func (repository *Repository) List(ctx context.Context, query memoryapp.ListQuery) (memoryapp.ListPage, error) {
	if repository == nil || isNilDB(repository.db) {
		return memoryapp.ListPage{}, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || validateListQuery(query) != nil {
		return memoryapp.ListPage{}, invalid(errors.New("memory list query is invalid"))
	}
	types := stringsFromTypes(query.Types)
	statuses := stringsFromStatuses(query.Statuses)
	var cursorTime, cursorID any
	if query.After != nil {
		cursorTime = query.After.UpdatedAt.UTC().Truncate(time.Microsecond)
		cursorID = string(query.After.ID)
	}
	rows, err := repository.db.Query(ctx, `SELECT `+memoryColumns+` FROM learning.memory
		WHERE workspace_id=$1 AND owner_principal_kind=$2 AND owner_principal_id=$3
		  AND ($4::text[] IS NULL OR memory_type=ANY($4::text[]))
		  AND ($5::text[] IS NULL OR status=ANY($5::text[]))
		  AND ($6::timestamptz IS NULL OR (updated_at,id)<($6::timestamptz,$7::uuid))
		ORDER BY updated_at DESC,id DESC LIMIT $8`,
		string(query.Scope.WorkspaceID), string(query.Scope.Owner.Kind), string(query.Scope.Owner.ID), types, statuses, cursorTime, cursorID, query.Limit+1)
	if err != nil {
		return memoryapp.ListPage{}, classify(err)
	}
	defer rows.Close()
	items, err := scanMemoryRows(rows)
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

// LoadEffective returns only confirmed, active, unexpired records matching the requested task scope.
func (repository *Repository) LoadEffective(ctx context.Context, query memoryapp.EffectiveQuery) ([]domain.Memory, error) {
	if repository == nil || isNilDB(repository.db) {
		return nil, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || validateEffectiveQuery(query) != nil {
		return nil, invalid(errors.New("effective memory query is invalid"))
	}
	rows, err := repository.db.Query(ctx, `SELECT `+memoryColumns+` FROM learning.memory
		WHERE workspace_id=$1 AND owner_principal_kind=$2 AND owner_principal_id=$3
		  AND status='ACTIVE' AND confirmed_at IS NOT NULL AND confirmed_by_principal_kind IS NOT NULL AND confirmed_by_principal_id IS NOT NULL
		  AND (expires_at IS NULL OR expires_at>clock_timestamp())
		  AND (task_scope_id IS NULL OR task_scope_id=$4::uuid)
		ORDER BY updated_at DESC,id DESC LIMIT $5`,
		string(query.Scope.WorkspaceID), string(query.Scope.Owner.Kind), string(query.Scope.Owner.ID), optionalIDValue(query.TaskScopeID), query.Limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	return scanMemoryRows(rows)
}

// ExpireDue safely transitions a bounded batch with database time and append-only audit facts.
func (repository *Repository) ExpireDue(ctx context.Context, limit int) (int, error) {
	if repository == nil || isNilDB(repository.db) {
		return 0, unavailable(errors.New("memory repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > domain.MaxListLimit {
		return 0, invalid(errors.New("memory expiry limit is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return 0, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT `+memoryColumns+` FROM learning.memory
		WHERE status IN ('CANDIDATE','ACTIVE','PAUSED') AND expires_at IS NOT NULL AND expires_at<=$1
		ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit)
	if err != nil {
		return 0, classify(err)
	}
	items, err := scanMemoryRows(rows)
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, current := range items {
		next, err := domain.Expire(current, now)
		if err != nil {
			return 0, err
		}
		updated, err := updateMemory(ctx, tx, next, current.Version)
		if err != nil {
			return 0, err
		}
		if err := insertAudit(ctx, tx, updated, domain.AuditExpired, &current.Status, nil, memoryapp.CommandBinding{}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, classify(err)
	}
	return len(items), nil
}

func insertCandidate(ctx context.Context, tx pgx.Tx, memory domain.Memory) (domain.Memory, error) {
	content, err := jsonMemoryContent(memory)
	if err != nil {
		return domain.Memory{}, err
	}
	persisted, err := scanMemory(tx.QueryRow(ctx, `INSERT INTO learning.memory(
		id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,source_type,source_ref,task_scope_id,
		status,expires_at,confirmed_at,confirmed_by_principal_kind,confirmed_by_principal_id,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
	RETURNING `+memoryColumns,
		string(memory.ID), string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), string(memory.Type), content,
		string(memory.Source.Type), memory.Source.Ref, optionalIDValue(memory.TaskScopeID), string(memory.Status), optionalTimeValue(memory.ExpiresAt),
		optionalTimeValue(memory.ConfirmedAt), optionalPrincipalKind(memory.ConfirmedBy), optionalPrincipalID(memory.ConfirmedBy), memory.Version,
		memory.CreatedAt.UTC().Truncate(time.Microsecond), memory.UpdatedAt.UTC().Truncate(time.Microsecond)))
	if err != nil {
		return domain.Memory{}, classify(err)
	}
	return persisted, nil
}

func updateMemory(ctx context.Context, tx pgx.Tx, memory domain.Memory, expectedVersion int64) (domain.Memory, error) {
	content, err := jsonMemoryContent(memory)
	if err != nil {
		return domain.Memory{}, err
	}
	persisted, err := scanMemory(tx.QueryRow(ctx, `UPDATE learning.memory SET
		content=$1::jsonb,source_type=$2,source_ref=$3,task_scope_id=$4,status=$5,expires_at=$6,
		confirmed_at=$7,confirmed_by_principal_kind=$8,confirmed_by_principal_id=$9,version=$10,updated_at=$11
		WHERE workspace_id=$12 AND id=$13 AND owner_principal_kind=$14 AND owner_principal_id=$15 AND version=$16
		RETURNING `+memoryColumns,
		content, string(memory.Source.Type), memory.Source.Ref, optionalIDValue(memory.TaskScopeID), string(memory.Status), optionalTimeValue(memory.ExpiresAt),
		optionalTimeValue(memory.ConfirmedAt), optionalPrincipalKind(memory.ConfirmedBy), optionalPrincipalID(memory.ConfirmedBy), memory.Version,
		memory.UpdatedAt.UTC().Truncate(time.Microsecond), string(memory.WorkspaceID), string(memory.ID), string(memory.Owner.Kind), string(memory.Owner.ID), expectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Memory{}, versionConflict(errors.New("memory compare-and-swap did not update a row"))
	}
	if err != nil {
		return domain.Memory{}, classify(err)
	}
	return persisted, nil
}

func loadOwnedMemory(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, owner domain.Principal, memoryID foundation.ID, lock bool) (domain.Memory, error) {
	sql := `SELECT ` + memoryColumns + ` FROM learning.memory WHERE workspace_id=$1 AND id=$2 AND owner_principal_kind=$3 AND owner_principal_id=$4`
	if lock {
		sql += ` FOR UPDATE`
	}
	memory, err := scanMemory(db.QueryRow(ctx, sql, string(workspaceID), string(memoryID), string(owner.Kind), string(owner.ID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Memory{}, notFound(errors.New("memory was not found in owner scope"))
	}
	if err != nil {
		return domain.Memory{}, classify(err)
	}
	return memory, nil
}

func scanMemoryRows(rows pgx.Rows) ([]domain.Memory, error) {
	items := make([]domain.Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return items, nil
}

func replayCommand(ctx context.Context, tx pgx.Tx, binding memoryapp.CommandBinding) (memoryapp.CommandResult, bool, error) {
	command, found, err := loadCommand(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return memoryapp.CommandResult{}, found, err
	}
	if !commandMatches(command, binding) {
		return memoryapp.CommandResult{}, false, idempotencyConflict(errors.New("memory idempotency key is bound to a different command"))
	}
	result, err := commandResult(command)
	return result, true, err
}

func insertCommand(ctx context.Context, tx pgx.Tx, binding memoryapp.CommandBinding, result memoryapp.CommandResult) error {
	snapshot, err := encodeMemorySnapshot(result.Memory)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO learning.memory_command(
		workspace_id,idempotency_key,owner_principal_kind,owner_principal_id,request_hash,command_type,memory_id,expected_version,memory_version,response,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11)`,
		string(binding.WorkspaceID), binding.IdempotencyKey, string(binding.Owner.Kind), string(binding.Owner.ID), binding.RequestHash,
		string(binding.CommandType), string(result.Memory.ID), binding.ExpectedVersion, result.Memory.Version, snapshot, result.Memory.UpdatedAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		return classify(err)
	}
	return nil
}

func insertAudit(ctx context.Context, tx pgx.Tx, memory domain.Memory, action domain.AuditAction, from *domain.Status, actor *domain.Principal, binding memoryapp.CommandBinding) error {
	actorKind, actorID := auditActor(memory, action, actor)
	var fromStatus any
	if from != nil {
		fromStatus = string(*from)
	}
	var key, hash any
	if binding.IdempotencyKey != "" {
		key, hash = binding.IdempotencyKey, binding.RequestHash
	}
	_, err := tx.Exec(ctx, `INSERT INTO learning.memory_audit(
		workspace_id,memory_id,owner_principal_kind,owner_principal_id,actor_principal_kind,actor_principal_id,
		action,from_status,to_status,memory_version,idempotency_key,request_hash,occurred_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		string(memory.WorkspaceID), string(memory.ID), string(memory.Owner.Kind), string(memory.Owner.ID), actorKind, actorID,
		string(action), fromStatus, string(memory.Status), memory.Version, key, hash, memory.UpdatedAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		return classify(err)
	}
	return nil
}

func auditActor(memory domain.Memory, action domain.AuditAction, actor *domain.Principal) (string, any) {
	if actor != nil {
		return string(actor.Kind), string(actor.ID)
	}
	if action == domain.AuditCandidateCreated {
		switch memory.Source.Type {
		case domain.SourceAgent:
			return "AGENT", nil
		case domain.SourceInterview:
			return "SYSTEM", nil
		default:
			return string(memory.Owner.Kind), string(memory.Owner.ID)
		}
	}
	return "SYSTEM", nil
}

func databaseNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err)
	}
	return now.UTC().Truncate(time.Microsecond), nil
}

func lockCommand(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`, string(workspaceID), key); err != nil {
		return classify(err)
	}
	return nil
}

// lockInterviewProvenance serializes semantic Interview candidate creation after
// the client command key has already been checked. This order preserves the
// strict "same key, different request" conflict before any other side effect.
func lockInterviewProvenance(ctx context.Context, tx pgx.Tx, memory domain.Memory) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2 || chr(31) || $3 || chr(31) || $4 || chr(31) || $5,0))`,
		string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), string(memory.Source.Type), memory.Source.Ref); err != nil {
		return classify(err)
	}
	return nil
}

func replayInterviewCandidate(ctx context.Context, tx pgx.Tx, record memoryapp.CandidateRecord) (memoryapp.CommandResult, bool, error) {
	command, found, err := loadInterviewCandidateCommand(ctx, tx, record.Memory)
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
	if err := insertCommand(ctx, tx, semanticBinding, result); err != nil {
		return memoryapp.CommandResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func loadInterviewCandidateCommand(ctx context.Context, tx pgx.Tx, memory domain.Memory) (persistedCommand, bool, error) {
	var command persistedCommand
	var ownerKind string
	command.workspaceID = memory.WorkspaceID
	err := tx.QueryRow(ctx, `SELECT c.owner_principal_kind,c.owner_principal_id::text,c.request_hash,c.command_type,c.memory_id::text,c.expected_version,c.memory_version,c.response::text
		FROM learning.memory AS m
		JOIN learning.memory_command AS c ON c.workspace_id=m.workspace_id AND c.memory_id=m.id
		WHERE m.workspace_id=$1 AND m.owner_principal_kind=$2 AND m.owner_principal_id=$3
		  AND m.source_type='INTERVIEW' AND m.source_ref=$4 AND c.command_type='CREATE_CANDIDATE'
		ORDER BY c.created_at ASC,c.idempotency_key ASC LIMIT 1 FOR UPDATE OF m,c`,
		string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), memory.Source.Ref).Scan(
		&ownerKind, &command.owner.ID, &command.requestHash, &command.commandType, &command.memoryID, &command.expected, &command.version, &command.response,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedCommand{}, false, nil
	}
	if err != nil {
		return persistedCommand{}, false, classify(err)
	}
	command.owner.Kind = authdomain.PrincipalKind(ownerKind)
	if err := domain.ValidatePrincipal(command.owner); err != nil || !validHash(command.requestHash) || !validCommandType(command.commandType) ||
		!validID(command.memoryID) || !validCommandVersion(command.commandType, command.expected, command.version) {
		return persistedCommand{}, false, inconsistent(errors.New("interview candidate command row is invalid"))
	}
	return command, true, nil
}

func sameInterviewCandidate(left, right domain.Memory) bool {
	return left.WorkspaceID == right.WorkspaceID && samePrincipal(left.Owner, right.Owner) && left.Type == right.Type &&
		reflect.DeepEqual(left.Content, right.Content) && left.Source == right.Source && sameOptionalID(left.TaskScopeID, right.TaskScopeID) &&
		sameOptionalTime(left.ExpiresAt, right.ExpiresAt) && left.Status == domain.StatusCandidate && left.Version == 1 &&
		left.ConfirmedAt == nil && left.ConfirmedBy == nil
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func commandMatches(command persistedCommand, binding memoryapp.CommandBinding) bool {
	return samePrincipal(command.owner, binding.Owner) && command.requestHash == binding.RequestHash && command.commandType == binding.CommandType &&
		command.expected == binding.ExpectedVersion && (binding.MemoryID == "" || command.memoryID == binding.MemoryID)
}

func validateBinding(binding memoryapp.CommandBinding) error {
	if !validID(binding.WorkspaceID) || domain.ValidatePrincipal(binding.Owner) != nil || binding.IdempotencyKey == "" ||
		domain.ValidateIdempotencyKey(binding.IdempotencyKey) != nil || !validHash(binding.RequestHash) || !validCommandType(binding.CommandType) || binding.ExpectedVersion < 0 {
		return errors.New("memory command binding is invalid")
	}
	if binding.CommandType == memoryapp.CommandCreateCandidate {
		if binding.ExpectedVersion != 0 {
			return errors.New("memory candidate binding version is invalid")
		}
		return nil
	}
	if !validID(binding.MemoryID) || binding.ExpectedVersion < 1 {
		return errors.New("memory mutation binding is invalid")
	}
	return nil
}

func validateCandidateRecord(record memoryapp.CandidateRecord) error {
	if err := validateBinding(record.Binding); err != nil || record.Binding.CommandType != memoryapp.CommandCreateCandidate ||
		record.Binding.MemoryID != record.Memory.ID || domain.ValidateMemory(record.Memory) != nil ||
		record.Memory.Status != domain.StatusCandidate || record.Memory.Version != 1 || record.Memory.WorkspaceID != record.Binding.WorkspaceID || !samePrincipal(record.Memory.Owner, record.Binding.Owner) {
		return errors.New("memory candidate record is invalid")
	}
	return nil
}

func validateMutationRecord(record memoryapp.MutationRecord) error {
	if err := validateBinding(record.Binding); err != nil || record.Binding.CommandType == memoryapp.CommandCreateCandidate ||
		domain.ValidateMemory(record.Current) != nil || domain.ValidateMemory(record.Next) != nil ||
		record.Current.ID != record.Next.ID || record.Current.ID != record.Binding.MemoryID || record.Current.WorkspaceID != record.Binding.WorkspaceID ||
		record.Next.WorkspaceID != record.Binding.WorkspaceID || !samePrincipal(record.Current.Owner, record.Binding.Owner) || !samePrincipal(record.Next.Owner, record.Binding.Owner) ||
		record.Current.Version != record.Binding.ExpectedVersion || record.Next.Version != record.Current.Version+1 ||
		record.Actor == nil || domain.ValidatePrincipal(*record.Actor) != nil || !samePrincipal(*record.Actor, record.Binding.Owner) || !validAuditAction(record.Action) {
		return errors.New("memory mutation record is invalid")
	}
	if !matchesAuditAction(record.Action, record.Current.Status, record.Next.Status) {
		return errors.New("memory mutation action does not match lifecycle transition")
	}
	return nil
}

func validateScope(scope memoryapp.Scope) error {
	if !validID(scope.WorkspaceID) || domain.ValidatePrincipal(scope.Owner) != nil {
		return errors.New("memory scope is invalid")
	}
	return nil
}

func validateListQuery(query memoryapp.ListQuery) error {
	if validateScope(query.Scope) != nil || query.Limit < 1 || query.Limit > domain.MaxListLimit ||
		query.After != nil && (query.After.UpdatedAt.IsZero() || !validID(query.After.ID)) {
		return errors.New("memory list query is invalid")
	}
	for _, typ := range query.Types {
		if typ != domain.TypePreference && typ != domain.TypeEpisodic && typ != domain.TypeGoal && typ != domain.TypeFeedback {
			return errors.New("memory list type is invalid")
		}
	}
	for _, status := range query.Statuses {
		if status != domain.StatusCandidate && status != domain.StatusActive && status != domain.StatusPaused && status != domain.StatusExpired && status != domain.StatusDeleted {
			return errors.New("memory list status is invalid")
		}
	}
	return nil
}

func validateEffectiveQuery(query memoryapp.EffectiveQuery) error {
	if validateScope(query.Scope) != nil || query.Limit < 1 || query.Limit > domain.MaxListLimit ||
		query.TaskScopeID != nil && !validID(*query.TaskScopeID) {
		return errors.New("effective memory query is invalid")
	}
	return nil
}

func validAuditAction(value domain.AuditAction) bool {
	return value == domain.AuditCandidateCreated || value == domain.AuditConfirmed || value == domain.AuditUpdated ||
		value == domain.AuditPaused || value == domain.AuditResumed || value == domain.AuditExpired || value == domain.AuditDeleted
}

func matchesAuditAction(action domain.AuditAction, from, to domain.Status) bool {
	switch action {
	case domain.AuditConfirmed:
		return from == domain.StatusCandidate && to == domain.StatusActive
	case domain.AuditUpdated:
		return from == to && (from == domain.StatusCandidate || from == domain.StatusActive || from == domain.StatusPaused)
	case domain.AuditPaused:
		return from == domain.StatusActive && to == domain.StatusPaused
	case domain.AuditResumed:
		return from == domain.StatusPaused && to == domain.StatusActive
	case domain.AuditDeleted:
		return from != domain.StatusDeleted && to == domain.StatusDeleted
	default:
		return false
	}
}

func stringsFromTypes(values []domain.Type) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	sort.Strings(result)
	return result
}

func stringsFromStatuses(values []domain.Status) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	sort.Strings(result)
	return result
}

func jsonMemoryContent(memory domain.Memory) ([]byte, error) {
	content, err := domain.CanonicalContent(memory.Content)
	if err != nil {
		return nil, err
	}
	return content, nil
}

func isNilDB(value DB) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ memoryapp.Repository = (*Repository)(nil)
