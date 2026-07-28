package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
)

func TestLoaderMapsEffectiveMemoryUnderStableOwnerWorkspaceAndTaskScope(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	workspaceID, taskScopeID := loaderID(1), loaderID(2)
	repository := &effectiveRepositoryFake{items: []memorydomain.Memory{
		effectiveMemory(loaderID(11), workspaceID, memorydomain.TypePreference, json.RawMessage(`{"answer_style":"concise"}`), nil, now.Add(-time.Minute)),
		effectiveMemory(loaderID(12), workspaceID, memorydomain.TypeGoal, json.RawMessage(`{"goal":"practice-boundaries"}`), &taskScopeID, now.Add(-2*time.Minute)),
	}}
	service := newEffectiveMemoryService(t, repository, now)
	loader, err := NewLoader(service, memorydomain.SingleUserOwner())
	if err != nil {
		t.Fatal(err)
	}

	result, err := loader.Load(context.Background(), interviewapp.ContextQuery{
		WorkspaceID: workspaceID, TaskScopeID: taskScopeID, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	query := repository.query
	if query.Scope.WorkspaceID != workspaceID || query.Scope.Owner != memorydomain.SingleUserOwner() || query.Limit != 5 ||
		query.TaskScopeID == nil || *query.TaskScopeID != taskScopeID {
		t.Fatalf("effective query=%+v", query)
	}
	if len(result.Preferences) != 1 || string(result.Preferences[0]) != `{"answer_style":"concise"}` ||
		len(result.Context) != 1 || string(result.Context[0]) != `{"goal":"practice-boundaries"}` {
		t.Fatalf("personal context=%+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"citation", "evidence", "source_ref", string(loaderID(11)), string(loaderID(12))} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("memory context exposed forbidden %q: %s", forbidden, encoded)
		}
	}
}

func TestLoaderFailsClosedWhenMemoryServiceReturnsIneffectiveFacts(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	workspaceID, taskScopeID := loaderID(1), loaderID(2)
	otherTaskScopeID := loaderID(3)
	active := effectiveMemory(loaderID(21), workspaceID, memorydomain.TypePreference, json.RawMessage(`{"mode":"focused"}`), &taskScopeID, now.Add(-time.Minute))
	paused := active
	paused.Status = memorydomain.StatusPaused
	candidate := active
	candidate.Status, candidate.ConfirmedAt, candidate.ConfirmedBy = memorydomain.StatusCandidate, nil, nil
	expired := active
	expiresAt := now.Add(-time.Second)
	expired.ExpiresAt = &expiresAt
	taskMismatch := active
	taskMismatch.TaskScopeID = &otherTaskScopeID

	tests := []struct {
		name string
		item memorydomain.Memory
	}{
		{name: "paused", item: paused},
		{name: "candidate", item: candidate},
		{name: "expired", item: expired},
		{name: "task mismatch", item: taskMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &effectiveRepositoryFake{items: []memorydomain.Memory{test.item}}
			loader, err := NewLoader(newEffectiveMemoryService(t, repository, now), memorydomain.SingleUserOwner())
			if err != nil {
				t.Fatal(err)
			}
			result, err := loader.Load(context.Background(), interviewapp.ContextQuery{
				WorkspaceID: workspaceID, TaskScopeID: taskScopeID, Limit: 5,
			})
			if err == nil {
				t.Fatalf("ineffective memory was accepted: %+v", result)
			}
			if len(result.Preferences) != 0 || len(result.Context) != 0 {
				t.Fatalf("ineffective memory leaked into context: %+v", result)
			}
		})
	}
}

func TestNewLoaderRejectsCredentialOrAlternateOwner(t *testing.T) {
	service := newEffectiveMemoryService(t, &effectiveRepositoryFake{}, time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC))
	alternate := memorydomain.SingleUserOwner()
	alternate.ID = loaderID(99)
	if loader, err := NewLoader(service, alternate); err == nil || loader != nil {
		t.Fatalf("loader=%+v err=%v", loader, err)
	}
}

type effectiveRepositoryFake struct {
	items []memorydomain.Memory
	query memoryapp.EffectiveQuery
}

func (repository *effectiveRepositoryFake) LoadEffective(_ context.Context, query memoryapp.EffectiveQuery) ([]memorydomain.Memory, error) {
	repository.query = query
	return append([]memorydomain.Memory(nil), repository.items...), nil
}

func (*effectiveRepositoryFake) FindCommand(context.Context, memoryapp.CommandBinding) (memoryapp.CommandResult, bool, error) {
	return memoryapp.CommandResult{}, false, errors.New("not used")
}

func (*effectiveRepositoryFake) CreateCandidate(context.Context, memoryapp.CandidateRecord) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, errors.New("not used")
}

func (*effectiveRepositoryFake) Mutate(context.Context, memoryapp.MutationRecord) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, errors.New("not used")
}

func (*effectiveRepositoryFake) Get(context.Context, memoryapp.Scope, foundation.ID) (memorydomain.Memory, error) {
	return memorydomain.Memory{}, errors.New("not used")
}

func (*effectiveRepositoryFake) List(context.Context, memoryapp.ListQuery) (memoryapp.ListPage, error) {
	return memoryapp.ListPage{}, errors.New("not used")
}

func (*effectiveRepositoryFake) ExpireDue(context.Context, int) (int, error) {
	return 0, errors.New("not used")
}

type loaderIDs struct{}

func (loaderIDs) New() (foundation.ID, error) { return loaderID(100), nil }

func newEffectiveMemoryService(t *testing.T, repository memoryapp.Repository, now time.Time) *memoryapp.Service {
	t.Helper()
	service, err := memoryapp.NewService(memoryapp.Dependencies{
		Repository: repository,
		IDs:        loaderIDs{},
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func effectiveMemory(id, workspaceID foundation.ID, memoryType memorydomain.Type, content json.RawMessage, taskScopeID *foundation.ID, updatedAt time.Time) memorydomain.Memory {
	owner := memorydomain.SingleUserOwner()
	confirmedAt := updatedAt.Add(-time.Minute)
	createdAt := confirmedAt.Add(-time.Minute)
	return memorydomain.Memory{
		ID: id, WorkspaceID: workspaceID, Owner: owner, Type: memoryType, Content: content,
		Source: memorydomain.Source{Type: memorydomain.SourceUser, Ref: "user:confirmed"}, TaskScopeID: cloneLoaderID(taskScopeID),
		Status: memorydomain.StatusActive, ConfirmedAt: &confirmedAt, ConfirmedBy: &owner, Version: 2,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func cloneLoaderID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func loaderID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("30000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}
