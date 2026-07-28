package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

func TestLoaderMapsEffectiveMemoryUnderStableOwnerWorkspaceAndTaskScope(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	workspaceID, taskScopeID := agentMemoryLoaderID(1), agentMemoryLoaderID(2)
	repository := &agentMemoryRepositoryFake{items: []memorydomain.Memory{
		effectiveAgentMemory(agentMemoryLoaderID(11), workspaceID, memorydomain.TypePreference, json.RawMessage(`{"answer_style":"concise"}`), nil, now.Add(-time.Minute)),
		effectiveAgentMemory(agentMemoryLoaderID(12), workspaceID, memorydomain.TypeGoal, json.RawMessage(`{"goal":"finish-the-current-task"}`), &taskScopeID, now.Add(-2*time.Minute)),
	}}
	loader, err := NewLoader(newAgentMemoryService(t, repository, now), memorydomain.SingleUserOwner())
	if err != nil {
		t.Fatal(err)
	}
	owner := memorydomain.SingleUserOwner()
	result, err := loader.Load(context.Background(), agentapplication.EffectiveMemoryQuery{
		WorkspaceID: workspaceID,
		Owner:       agentapplication.MemoryOwnerRef{Kind: string(owner.Kind), ID: owner.ID},
		TaskScopeID: taskScopeID,
		Limit:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 || repository.query.Scope.WorkspaceID != workspaceID || repository.query.Scope.Owner != owner ||
		repository.query.TaskScopeID == nil || *repository.query.TaskScopeID != taskScopeID || repository.query.Limit != 5 {
		t.Fatalf("effective query=%+v calls=%d", repository.query, repository.calls)
	}
	if len(result) != 2 || result[0].ID != agentMemoryLoaderID(11) || result[0].Version != 2 ||
		result[0].Type != string(memorydomain.TypePreference) || result[0].Category != agentapplication.MemoryCategoryUserPreference ||
		string(result[0].Content) != `{"answer_style":"concise"}` || result[1].ID != agentMemoryLoaderID(12) ||
		result[1].Type != string(memorydomain.TypeGoal) || result[1].Category != agentapplication.MemoryCategoryTaskContext ||
		string(result[1].Content) != `{"goal":"finish-the-current-task"}` {
		t.Fatalf("effective items=%+v", result)
	}
	repository.items[0].Content[2] = 'x'
	if string(result[0].Content) != `{"answer_style":"concise"}` {
		t.Fatalf("loader retained repository-owned content: %s", result[0].Content)
	}
}

func TestLoaderFailsClosedForUnavailableBindingAndMemoryQueryFailure(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	workspaceID, taskScopeID := agentMemoryLoaderID(1), agentMemoryLoaderID(2)
	owner := memorydomain.SingleUserOwner()
	query := agentapplication.EffectiveMemoryQuery{
		WorkspaceID: workspaceID,
		Owner:       agentapplication.MemoryOwnerRef{Kind: string(owner.Kind), ID: owner.ID},
		TaskScopeID: taskScopeID,
		Limit:       5,
	}

	repository := &agentMemoryRepositoryFake{}
	loader, err := NewLoader(newAgentMemoryService(t, repository, now), owner)
	if err != nil {
		t.Fatal(err)
	}
	mismatched := query
	mismatched.Owner.ID = agentMemoryLoaderID(99)
	if result, loadErr := loader.Load(context.Background(), mismatched); loadErr == nil || result != nil {
		t.Fatalf("mismatched owner result=%+v err=%v", result, loadErr)
	} else {
		assertAgentMemoryUnavailable(t, loadErr)
	}
	if repository.calls != 0 {
		t.Fatalf("mismatched owner reached repository %d times", repository.calls)
	}

	dependencyErr := foundation.NewError(foundation.ErrorRetryableFailure, "MEMORY_QUERY_DOWN", true, errors.New("temporary"))
	repository.err = dependencyErr
	if result, loadErr := loader.Load(context.Background(), query); !errors.Is(loadErr, dependencyErr) || result != nil {
		t.Fatalf("query failure result=%+v err=%v", result, loadErr)
	}
	if repository.calls != 1 {
		t.Fatalf("query failure calls=%d want 1", repository.calls)
	}

	var nilLoader *Loader
	if result, loadErr := nilLoader.Load(context.Background(), query); loadErr == nil || result != nil {
		t.Fatalf("nil loader result=%+v err=%v", result, loadErr)
	} else {
		assertAgentMemoryUnavailable(t, loadErr)
	}
}

func TestLoaderRejectsAlternateOwnerAndIneffectiveServiceResults(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	workspaceID, taskScopeID := agentMemoryLoaderID(1), agentMemoryLoaderID(2)
	service := newAgentMemoryService(t, &agentMemoryRepositoryFake{}, now)
	alternateOwner := memorydomain.SingleUserOwner()
	alternateOwner.ID = agentMemoryLoaderID(99)
	for _, test := range []struct {
		name    string
		service *memoryapplication.Service
		owner   memorydomain.Principal
	}{
		{name: "nil service", owner: memorydomain.SingleUserOwner()},
		{name: "alternate owner", service: service, owner: alternateOwner},
	} {
		t.Run(test.name, func(t *testing.T) {
			loader, err := NewLoader(test.service, test.owner)
			if err == nil || loader != nil {
				t.Fatalf("loader=%+v err=%v", loader, err)
			}
			assertAgentMemoryUnavailable(t, err)
		})
	}

	active := effectiveAgentMemory(agentMemoryLoaderID(21), workspaceID, memorydomain.TypePreference, json.RawMessage(`{"mode":"focused"}`), &taskScopeID, now.Add(-time.Minute))
	paused := active
	paused.Status = memorydomain.StatusPaused
	otherScope := active
	otherTaskScopeID := agentMemoryLoaderID(3)
	otherScope.TaskScopeID = &otherTaskScopeID
	for _, test := range []struct {
		name string
		item memorydomain.Memory
	}{
		{name: "paused", item: paused},
		{name: "other task scope", item: otherScope},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &agentMemoryRepositoryFake{items: []memorydomain.Memory{test.item}}
			loader, err := NewLoader(newAgentMemoryService(t, repository, now), memorydomain.SingleUserOwner())
			if err != nil {
				t.Fatal(err)
			}
			owner := memorydomain.SingleUserOwner()
			result, err := loader.Load(context.Background(), agentapplication.EffectiveMemoryQuery{
				WorkspaceID: workspaceID,
				Owner:       agentapplication.MemoryOwnerRef{Kind: string(owner.Kind), ID: owner.ID},
				TaskScopeID: taskScopeID,
				Limit:       5,
			})
			if err == nil || result != nil {
				t.Fatalf("ineffective result=%+v err=%v", result, err)
			}
		})
	}
}

type agentMemoryRepositoryFake struct {
	items []memorydomain.Memory
	query memoryapplication.EffectiveQuery
	err   error
	calls int
}

func (repository *agentMemoryRepositoryFake) LoadEffective(_ context.Context, query memoryapplication.EffectiveQuery) ([]memorydomain.Memory, error) {
	repository.calls++
	repository.query = query
	if repository.err != nil {
		return nil, repository.err
	}
	return append([]memorydomain.Memory(nil), repository.items...), nil
}

func (*agentMemoryRepositoryFake) FindCommand(context.Context, memoryapplication.CommandBinding) (memoryapplication.CommandResult, bool, error) {
	return memoryapplication.CommandResult{}, false, errors.New("not used")
}

func (*agentMemoryRepositoryFake) CreateCandidate(context.Context, memoryapplication.CandidateRecord) (memoryapplication.CommandResult, error) {
	return memoryapplication.CommandResult{}, errors.New("not used")
}

func (*agentMemoryRepositoryFake) Mutate(context.Context, memoryapplication.MutationRecord) (memoryapplication.CommandResult, error) {
	return memoryapplication.CommandResult{}, errors.New("not used")
}

func (*agentMemoryRepositoryFake) Get(context.Context, memoryapplication.Scope, foundation.ID) (memorydomain.Memory, error) {
	return memorydomain.Memory{}, errors.New("not used")
}

func (*agentMemoryRepositoryFake) List(context.Context, memoryapplication.ListQuery) (memoryapplication.ListPage, error) {
	return memoryapplication.ListPage{}, errors.New("not used")
}

func (*agentMemoryRepositoryFake) ExpireDue(context.Context, int) (int, error) {
	return 0, errors.New("not used")
}

type agentMemoryLoaderIDs struct{}

func (agentMemoryLoaderIDs) New() (foundation.ID, error) { return agentMemoryLoaderID(100), nil }

func newAgentMemoryService(t *testing.T, repository memoryapplication.Repository, now time.Time) *memoryapplication.Service {
	t.Helper()
	service, err := memoryapplication.NewService(memoryapplication.Dependencies{
		Repository: repository,
		IDs:        agentMemoryLoaderIDs{},
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func effectiveAgentMemory(id, workspaceID foundation.ID, memoryType memorydomain.Type, content json.RawMessage, taskScopeID *foundation.ID, updatedAt time.Time) memorydomain.Memory {
	owner := memorydomain.SingleUserOwner()
	confirmedAt := updatedAt.Add(-time.Minute)
	createdAt := confirmedAt.Add(-time.Minute)
	return memorydomain.Memory{
		ID: id, WorkspaceID: workspaceID, Owner: owner, Type: memoryType, Content: content,
		Source: memorydomain.Source{Type: memorydomain.SourceUser, Ref: "user:confirmed"}, TaskScopeID: cloneAgentMemoryLoaderID(taskScopeID),
		Status: memorydomain.StatusActive, ConfirmedAt: &confirmedAt, ConfirmedBy: &owner, Version: 2,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func cloneAgentMemoryLoaderID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func agentMemoryLoaderID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("72000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}

func assertAgentMemoryUnavailable(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != agentapplication.ErrorCodeMemoryContextUnavailable {
		t.Fatalf("error=%v want %s dependency unavailable", err, agentapplication.ErrorCodeMemoryContextUnavailable)
	}
}
