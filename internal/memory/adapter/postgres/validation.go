package postgres

import (
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

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
