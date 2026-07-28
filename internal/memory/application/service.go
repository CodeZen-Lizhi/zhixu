package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

// Service owns command preparation, lifecycle preflight, and result validation.
type Service struct{ dependencies Dependencies }

// NewService creates a fail-closed Memory service.
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, unavailable("memory repository, id generator, and clock are required")
	}
	return &Service{dependencies: dependencies}, nil
}

// CreateCandidate creates a non-effective candidate owned by the specified principal.
func (service *Service) CreateCandidate(ctx context.Context, command CreateCandidateCommand) (CommandResult, error) {
	if err := service.available(ctx); err != nil {
		return CommandResult{}, err
	}
	if !validID(command.WorkspaceID) || domain.ValidatePrincipal(command.Owner) != nil {
		return CommandResult{}, requestInvalid("memory candidate workspace or owner is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CommandResult{}, err
	}
	content, err := domain.CanonicalContent(command.Content)
	if err != nil {
		return CommandResult{}, err
	}
	if err := domain.ValidateSource(command.Source); err != nil {
		return CommandResult{}, err
	}
	if command.TaskScopeID != nil && !validID(*command.TaskScopeID) {
		return CommandResult{}, requestInvalid("memory candidate task scope is invalid")
	}
	hash, err := requestHash(CommandCreateCandidate, command.WorkspaceID, "", 0, candidateRequest{
		Owner: command.Owner, Type: command.Type, Content: content, Source: command.Source,
		TaskScopeID: cloneID(command.TaskScopeID), ExpiresAt: cloneTime(command.ExpiresAt),
	})
	if err != nil {
		return CommandResult{}, err
	}
	lookup := CommandBinding{
		WorkspaceID: command.WorkspaceID, Owner: command.Owner, IdempotencyKey: command.IdempotencyKey,
		RequestHash: hash, CommandType: CommandCreateCandidate,
	}
	if replay, found, err := service.replay(ctx, lookup); err != nil {
		return CommandResult{}, err
	} else if found {
		return replay, nil
	}
	id, err := service.dependencies.IDs.New()
	if err != nil {
		return CommandResult{}, err
	}
	now := service.dependencies.Clock.Now().UTC()
	memory, err := domain.NewCandidate(domain.CandidateInput{
		ID: id, WorkspaceID: command.WorkspaceID, Owner: command.Owner, Type: command.Type,
		Content: content, Source: command.Source, TaskScopeID: command.TaskScopeID, ExpiresAt: command.ExpiresAt, CreatedAt: now,
	})
	if err != nil {
		return CommandResult{}, err
	}
	lookup.MemoryID = memory.ID
	result, err := service.dependencies.Repository.CreateCandidate(ctx, CandidateRecord{Binding: lookup, Memory: memory})
	if err != nil {
		// A semantic INTERVIEW replay receipt binds its existing candidate ID,
		// while this attempt has a freshly generated ID. Command identity is
		// already fully defined by workspace, owner, key, hash, and type.
		recoveryBinding := lookup
		recoveryBinding.MemoryID = ""
		result, err = service.replayOrError(ctx, recoveryBinding, err)
		if err != nil {
			return CommandResult{}, err
		}
	}
	if err := validateCandidateResult(result, lookup, memory); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

// Confirm makes a candidate effective only after its owner explicitly confirms it.
func (service *Service) Confirm(ctx context.Context, command TransitionCommand) (CommandResult, error) {
	return service.transition(ctx, command, CommandConfirm, domain.AuditConfirmed, func(current domain.Memory, actor domain.Principal, at time.Time) (domain.Memory, error) {
		return domain.Confirm(current, actor, at)
	})
}

// Edit changes a candidate, active, or paused Memory record without changing ownership.
func (service *Service) Edit(ctx context.Context, command EditCommand) (CommandResult, error) {
	if err := service.available(ctx); err != nil {
		return CommandResult{}, err
	}
	content, err := domain.CanonicalContent(command.Content)
	if err != nil {
		return CommandResult{}, err
	}
	if command.TaskScopeID != nil && !validID(*command.TaskScopeID) {
		return CommandResult{}, requestInvalid("memory edit task scope is invalid")
	}
	binding, err := service.prepareBinding(CommandUpdate, command.Scope, command.MemoryID, command.ExpectedVersion, command.IdempotencyKey,
		editRequest{Content: content, TaskScopeID: cloneID(command.TaskScopeID), ExpiresAt: cloneTime(command.ExpiresAt)})
	if err != nil {
		return CommandResult{}, err
	}
	if replay, found, err := service.replay(ctx, binding); err != nil {
		return CommandResult{}, err
	} else if found {
		return replay, nil
	}
	current, err := service.dependencies.Repository.Get(ctx, command.Scope, command.MemoryID)
	if err != nil {
		return CommandResult{}, err
	}
	next, err := domain.Edit(current, command.Scope.Owner, domain.EditInput{
		Content: content, TaskScopeID: command.TaskScopeID, ExpiresAt: command.ExpiresAt,
	}, service.dependencies.Clock.Now())
	if err != nil {
		return CommandResult{}, err
	}
	result, err := service.dependencies.Repository.Mutate(ctx, MutationRecord{
		Binding: binding, Current: current, Next: next, Action: domain.AuditUpdated, Actor: &command.Scope.Owner,
	})
	if err != nil {
		return service.replayOrError(ctx, binding, err)
	}
	if err := validateResult(result, binding, command.ExpectedVersion+1, next.Status); err != nil {
		return CommandResult{}, err
	}
	if result.Memory.Source != current.Source {
		return CommandResult{}, resultInconsistent("memory edit changed immutable source provenance")
	}
	return result, nil
}

// Pause excludes a confirmed active record from future effective-context reads.
func (service *Service) Pause(ctx context.Context, command TransitionCommand) (CommandResult, error) {
	return service.transition(ctx, command, CommandPause, domain.AuditPaused, func(current domain.Memory, actor domain.Principal, at time.Time) (domain.Memory, error) {
		return domain.Pause(current, actor, at)
	})
}

// Resume re-enables a confirmed paused record when its expiry has not elapsed.
func (service *Service) Resume(ctx context.Context, command TransitionCommand) (CommandResult, error) {
	return service.transition(ctx, command, CommandResume, domain.AuditResumed, func(current domain.Memory, actor domain.Principal, at time.Time) (domain.Memory, error) {
		return domain.Resume(current, actor, at)
	})
}

// Delete writes a tombstone instead of physically removing Memory history.
func (service *Service) Delete(ctx context.Context, command TransitionCommand) (CommandResult, error) {
	return service.transition(ctx, command, CommandDelete, domain.AuditDeleted, func(current domain.Memory, actor domain.Principal, at time.Time) (domain.Memory, error) {
		return domain.Delete(current, actor, at)
	})
}

// Get returns one Memory record only when the caller is its exact owner in the Workspace.
func (service *Service) Get(ctx context.Context, scope Scope, memoryID foundation.ID) (domain.Memory, error) {
	if err := service.available(ctx); err != nil {
		return domain.Memory{}, err
	}
	if err := validateScope(scope); err != nil || !validID(memoryID) {
		return domain.Memory{}, requestInvalid("memory get scope or id is invalid")
	}
	memory, err := service.dependencies.Repository.Get(ctx, scope, memoryID)
	if err != nil {
		return domain.Memory{}, err
	}
	if err := domain.ValidateMemory(memory); err != nil || memory.WorkspaceID != scope.WorkspaceID || memory.ID != memoryID || !samePrincipal(memory.Owner, scope.Owner) {
		return domain.Memory{}, resultInconsistent("memory repository returned a record outside its requested scope")
	}
	return memory, nil
}

// List returns a bounded, stable page visible only to the owner principal.
func (service *Service) List(ctx context.Context, query ListQuery) (ListPage, error) {
	if err := service.available(ctx); err != nil {
		return ListPage{}, err
	}
	if err := validateListQuery(query); err != nil {
		return ListPage{}, err
	}
	page, err := service.dependencies.Repository.List(ctx, query)
	if err != nil {
		return ListPage{}, err
	}
	if err := validateListPage(query, page); err != nil {
		return ListPage{}, err
	}
	return page, nil
}

// LoadEffective returns only confirmed, active, unexpired records applicable to the requested task scope.
func (service *Service) LoadEffective(ctx context.Context, query EffectiveQuery) ([]domain.Memory, error) {
	if err := service.available(ctx); err != nil {
		return nil, err
	}
	if err := validateScope(query.Scope); err != nil || query.Limit < 1 || query.Limit > domain.MaxListLimit ||
		query.TaskScopeID != nil && !validID(*query.TaskScopeID) {
		return nil, requestInvalid("effective memory query is invalid")
	}
	items, err := service.dependencies.Repository.LoadEffective(ctx, query)
	if err != nil {
		return nil, err
	}
	now := service.dependencies.Clock.Now()
	if len(items) > query.Limit {
		return nil, resultInconsistent("effective memory query exceeded its limit")
	}
	for index, item := range items {
		if !samePrincipal(item.Owner, query.Scope.Owner) || item.WorkspaceID != query.Scope.WorkspaceID || !domain.IsEffective(item, query.TaskScopeID, now) {
			return nil, resultInconsistent("repository returned ineffective memory")
		}
		if index > 0 && (item.UpdatedAt.After(items[index-1].UpdatedAt) || item.UpdatedAt.Equal(items[index-1].UpdatedAt) && item.ID >= items[index-1].ID) {
			return nil, resultInconsistent("effective memory order is unstable")
		}
	}
	return items, nil
}

// ExpireDue performs bounded maintenance; context reads remain fail-closed even before this sweep runs.
func (service *Service) ExpireDue(ctx context.Context, limit int) (int, error) {
	if err := service.available(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > domain.MaxListLimit {
		return 0, requestInvalid("memory expiry limit is invalid")
	}
	return service.dependencies.Repository.ExpireDue(ctx, limit)
}

func (service *Service) transition(
	ctx context.Context,
	command TransitionCommand,
	commandType CommandType,
	action domain.AuditAction,
	apply func(domain.Memory, domain.Principal, time.Time) (domain.Memory, error),
) (CommandResult, error) {
	if err := service.available(ctx); err != nil {
		return CommandResult{}, err
	}
	binding, err := service.prepareBinding(commandType, command.Scope, command.MemoryID, command.ExpectedVersion, command.IdempotencyKey, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if replay, found, err := service.replay(ctx, binding); err != nil {
		return CommandResult{}, err
	} else if found {
		return replay, nil
	}
	current, err := service.dependencies.Repository.Get(ctx, command.Scope, command.MemoryID)
	if err != nil {
		return CommandResult{}, err
	}
	next, err := apply(current, command.Scope.Owner, service.dependencies.Clock.Now())
	if err != nil {
		return CommandResult{}, err
	}
	result, err := service.dependencies.Repository.Mutate(ctx, MutationRecord{
		Binding: binding, Current: current, Next: next, Action: action, Actor: &command.Scope.Owner,
	})
	if err != nil {
		return service.replayOrError(ctx, binding, err)
	}
	if err := validateResult(result, binding, command.ExpectedVersion+1, next.Status); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (service *Service) prepareBinding(commandType CommandType, scope Scope, memoryID foundation.ID, expectedVersion int64, key string, payload any) (CommandBinding, error) {
	if err := validateScope(scope); err != nil || !validID(memoryID) || expectedVersion < 1 {
		return CommandBinding{}, requestInvalid("memory command scope, identity, or version is invalid")
	}
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		return CommandBinding{}, err
	}
	hash, err := requestHash(commandType, scope.WorkspaceID, memoryID, expectedVersion, payload)
	if err != nil {
		return CommandBinding{}, err
	}
	return CommandBinding{WorkspaceID: scope.WorkspaceID, Owner: scope.Owner, MemoryID: memoryID, IdempotencyKey: key, RequestHash: hash, CommandType: commandType, ExpectedVersion: expectedVersion}, nil
}

func (service *Service) replay(ctx context.Context, binding CommandBinding) (CommandResult, bool, error) {
	result, found, err := service.dependencies.Repository.FindCommand(ctx, binding)
	if err != nil || !found {
		return CommandResult{}, found, err
	}
	result.Replayed = true
	version := binding.ExpectedVersion + 1
	if binding.CommandType == CommandCreateCandidate {
		version = 1
	}
	if err := validateResult(result, binding, version, ""); err != nil {
		return CommandResult{}, false, err
	}
	return result, true, nil
}

func (service *Service) replayOrError(ctx context.Context, binding CommandBinding, original error) (CommandResult, error) {
	result, found, err := service.replay(ctx, binding)
	if err == nil && found {
		return result, nil
	}
	return CommandResult{}, original
}

func (service *Service) available(ctx context.Context) error {
	if service == nil || service.dependencies.Repository == nil || service.dependencies.IDs == nil || service.dependencies.Clock == nil {
		return unavailable("memory service is unavailable")
	}
	if ctx == nil {
		return requestInvalid("memory context is nil")
	}
	return nil
}

func validateResult(result CommandResult, binding CommandBinding, version int64, status domain.Status) error {
	if err := domain.ValidateMemory(result.Memory); err != nil || result.Memory.WorkspaceID != binding.WorkspaceID ||
		!samePrincipal(result.Memory.Owner, binding.Owner) || result.Memory.Version != version ||
		binding.MemoryID != "" && result.Memory.ID != binding.MemoryID || status != "" && result.Memory.Status != status {
		return resultInconsistent("memory command result violates its durable binding")
	}
	return nil
}

// validateCandidateResult permits only an INTERVIEW provenance replay to bind a
// newly supplied command key to the already persisted candidate identity.
func validateCandidateResult(result CommandResult, binding CommandBinding, expected domain.Memory) error {
	resultBinding := binding
	if result.Memory.ID != expected.ID {
		if !result.Replayed || expected.Source.Type != domain.SourceInterview {
			return resultInconsistent("memory candidate result changed its durable identity")
		}
		resultBinding.MemoryID = ""
	}
	if err := validateResult(result, resultBinding, 1, domain.StatusCandidate); err != nil {
		return err
	}
	if result.Memory.Type != expected.Type || !bytes.Equal(result.Memory.Content, expected.Content) || result.Memory.Source != expected.Source ||
		!sameOptionalID(result.Memory.TaskScopeID, expected.TaskScopeID) || !sameOptionalTime(result.Memory.ExpiresAt, expected.ExpiresAt) ||
		result.Memory.ConfirmedAt != nil || result.Memory.ConfirmedBy != nil {
		return resultInconsistent("memory candidate replay does not match its initial snapshot")
	}
	return nil
}

func validateScope(scope Scope) error {
	if !validID(scope.WorkspaceID) || domain.ValidatePrincipal(scope.Owner) != nil {
		return requestInvalid("memory scope is invalid")
	}
	return nil
}

func validateListQuery(query ListQuery) error {
	if err := validateScope(query.Scope); err != nil || query.Limit < 1 || query.Limit > domain.MaxListLimit {
		return requestInvalid("memory list query is invalid")
	}
	if query.After != nil && (query.After.UpdatedAt.IsZero() || !validID(query.After.ID)) {
		return requestInvalid("memory list cursor is invalid")
	}
	for _, typ := range query.Types {
		if !validType(typ) {
			return requestInvalid("memory list type filter is invalid")
		}
	}
	for _, status := range query.Statuses {
		if !validStatus(status) {
			return requestInvalid("memory list status filter is invalid")
		}
	}
	return nil
}

func validateListPage(query ListQuery, page ListPage) error {
	if len(page.Items) > query.Limit || page.Next != nil && len(page.Items) == 0 {
		return resultInconsistent("memory list page shape is invalid")
	}
	for index, item := range page.Items {
		if err := domain.ValidateMemory(item); err != nil || item.WorkspaceID != query.Scope.WorkspaceID || !samePrincipal(item.Owner, query.Scope.Owner) {
			return resultInconsistent("memory list escaped its owner scope")
		}
		if index > 0 && (item.UpdatedAt.After(page.Items[index-1].UpdatedAt) || item.UpdatedAt.Equal(page.Items[index-1].UpdatedAt) && item.ID >= page.Items[index-1].ID) {
			return resultInconsistent("memory list ordering is unstable")
		}
	}
	return nil
}

type candidateRequest struct {
	Owner       domain.Principal `json:"owner"`
	Type        domain.Type      `json:"type"`
	Content     json.RawMessage  `json:"content"`
	Source      domain.Source    `json:"source"`
	TaskScopeID *foundation.ID   `json:"task_scope_id,omitempty"`
	ExpiresAt   *time.Time       `json:"expires_at,omitempty"`
}

type editRequest struct {
	Content     json.RawMessage `json:"content"`
	TaskScopeID *foundation.ID  `json:"task_scope_id,omitempty"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
}

func requestHash(commandType CommandType, workspaceID, memoryID foundation.ID, expectedVersion int64, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		Schema          string        `json:"schema"`
		CommandType     CommandType   `json:"command_type"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		MemoryID        foundation.ID `json:"memory_id,omitempty"`
		ExpectedVersion int64         `json:"expected_version"`
		Payload         any           `json:"payload"`
	}{"memory-command/v1", commandType, workspaceID, memoryID, expectedVersion, payload})
	if err != nil {
		return "", requestInvalid("memory command cannot be canonicalized")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validType(value domain.Type) bool {
	return value == domain.TypePreference || value == domain.TypeEpisodic || value == domain.TypeGoal || value == domain.TypeFeedback
}

func validStatus(value domain.Status) bool {
	return value == domain.StatusCandidate || value == domain.StatusActive || value == domain.StatusPaused || value == domain.StatusExpired || value == domain.StatusDeleted
}

func samePrincipal(left, right domain.Principal) bool {
	return left.Kind == right.Kind && left.ID == right.ID
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC().Truncate(time.Microsecond)
	return &copy
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
