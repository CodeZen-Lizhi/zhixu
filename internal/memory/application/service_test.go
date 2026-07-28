package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

func TestCreateConfirmAndEffectiveReadHonorConfirmationAndTaskScope(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	repository := newMemoryRepositoryFake()
	service := newMemoryService(t, repository, now)
	owner := testPrincipal(3)
	task := testID(4)
	created, err := service.CreateCandidate(context.Background(), CreateCandidateCommand{
		WorkspaceID: testID(1), Owner: owner, Type: domain.TypePreference,
		Content: json.RawMessage(`{"language":"zh"}`), Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:proposal-1"},
		TaskScopeID: &task, IdempotencyKey: "candidate-1",
	})
	if err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	if created.Memory.Status != domain.StatusCandidate {
		t.Fatalf("CreateCandidate() status = %s", created.Memory.Status)
	}
	if items, err := service.LoadEffective(context.Background(), EffectiveQuery{Scope: Scope{WorkspaceID: testID(1), Owner: owner}, TaskScopeID: &task, Limit: 10}); err != nil || len(items) != 0 {
		t.Fatalf("LoadEffective(candidate) = %#v, %v", items, err)
	}
	confirmed, err := service.Confirm(context.Background(), TransitionCommand{
		Scope: Scope{WorkspaceID: testID(1), Owner: owner}, MemoryID: created.Memory.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-1",
	})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmed.Memory.Status != domain.StatusActive {
		t.Fatalf("Confirm() status = %s", confirmed.Memory.Status)
	}
	items, err := service.LoadEffective(context.Background(), EffectiveQuery{Scope: Scope{WorkspaceID: testID(1), Owner: owner}, TaskScopeID: &task, Limit: 10})
	if err != nil || len(items) != 1 || items[0].ID != created.Memory.ID {
		t.Fatalf("LoadEffective(confirmed) = %#v, %v", items, err)
	}
}

func TestConfirmUsesReceiptReplayAndRejectsDifferentRequest(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	repository := newMemoryRepositoryFake()
	service := newMemoryService(t, repository, now)
	owner := testPrincipal(3)
	created := createCandidate(t, service, owner, "candidate-1")
	command := TransitionCommand{Scope: Scope{WorkspaceID: testID(1), Owner: owner}, MemoryID: created.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-1"}
	first, err := service.Confirm(context.Background(), command)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	replayed, err := service.Confirm(context.Background(), command)
	if err != nil || !replayed.Replayed || replayed.Memory.ID != first.Memory.ID {
		t.Fatalf("Confirm(replay) = %#v, %v", replayed, err)
	}
	_, err = service.Confirm(context.Background(), TransitionCommand{Scope: command.Scope, MemoryID: command.MemoryID, ExpectedVersion: 2, IdempotencyKey: command.IdempotencyKey})
	if !hasCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("Confirm(conflict) error = %v", err)
	}
}

func TestOwnerAndExpiryFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	repository := newMemoryRepositoryFake()
	service := newMemoryService(t, repository, now)
	owner := testPrincipal(3)
	created := createCandidate(t, service, owner, "candidate-1")
	_, err := service.Confirm(context.Background(), TransitionCommand{Scope: Scope{WorkspaceID: testID(1), Owner: testPrincipal(9)}, MemoryID: created.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-other"})
	if !hasCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("Confirm(other owner) error = %v", err)
	}
	expires := now.Add(time.Minute)
	episodic, err := service.CreateCandidate(context.Background(), CreateCandidateCommand{
		WorkspaceID: testID(1), Owner: owner, Type: domain.TypeEpisodic, Content: json.RawMessage(`{"note":"short"}`),
		Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:proposal-2"}, ExpiresAt: &expires, IdempotencyKey: "candidate-episodic",
	})
	if err != nil {
		t.Fatalf("CreateCandidate(episodic) error = %v", err)
	}
	service.dependencies.Clock = foundation.FixedClock{Value: now.Add(2 * time.Minute)}
	_, err = service.Confirm(context.Background(), TransitionCommand{Scope: Scope{WorkspaceID: testID(1), Owner: owner}, MemoryID: episodic.Memory.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-expired"})
	if !hasCode(err, domain.ErrorCodeExpired) {
		t.Fatalf("Confirm(expired) error = %v", err)
	}
}

func TestEditPreservesSourceProvenance(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	repository := newMemoryRepositoryFake()
	service := newMemoryService(t, repository, now)
	owner := testPrincipal(3)
	created := createCandidate(t, service, owner, "candidate-edit-source")

	result, err := service.Edit(context.Background(), EditCommand{
		Scope: Scope{WorkspaceID: testID(1), Owner: owner}, MemoryID: created.ID, ExpectedVersion: 1,
		Content: json.RawMessage(`{"mode":"detailed"}`), IdempotencyKey: "edit-source-1",
	})
	if err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	if result.Memory.Source != created.Source {
		t.Fatalf("Edit() source = %+v, want immutable %+v", result.Memory.Source, created.Source)
	}
}

func TestCreateCandidateRecoversInterviewSemanticReplayAfterResponseLoss(t *testing.T) {
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	owner := testPrincipal(3)
	taskScopeID := testID(4)
	source := domain.Source{Type: domain.SourceInterview, Ref: "interview:session:path:step"}
	content := json.RawMessage(`{"goal":"Practice cancellation","rationale":"Close the persisted gap"}`)
	existing, err := domain.NewCandidate(domain.CandidateInput{
		ID: testID(90), WorkspaceID: testID(1), Owner: owner, Type: domain.TypeGoal, Content: content,
		Source: source, TaskScopeID: &taskScopeID, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &responseLossCandidateRepository{memoryRepositoryFake: newMemoryRepositoryFake(), result: CommandResult{Memory: existing, Replayed: true}}
	service := newMemoryService(t, repository, now)
	result, err := service.CreateCandidate(context.Background(), CreateCandidateCommand{
		WorkspaceID: testID(1), Owner: owner, Type: domain.TypeGoal, Content: content, Source: source, TaskScopeID: &taskScopeID,
		IdempotencyKey: "interview-response-loss",
	})
	if err != nil || !result.Replayed || result.Memory.ID != existing.ID {
		t.Fatalf("response-loss semantic replay=%#v err=%v", result, err)
	}
	if repository.recoveryBinding.MemoryID != "" {
		t.Fatalf("response-loss recovery kept transient memory id=%s", repository.recoveryBinding.MemoryID)
	}
}

type memoryRepositoryFake struct {
	memories map[foundation.ID]domain.Memory
	commands map[string]commandReceipt
}

type responseLossCandidateRepository struct {
	*memoryRepositoryFake
	result          CommandResult
	recoveryBinding CommandBinding
	finds           int
}

func (fake *responseLossCandidateRepository) FindCommand(_ context.Context, binding CommandBinding) (CommandResult, bool, error) {
	fake.finds++
	if fake.finds == 1 {
		return CommandResult{}, false, nil
	}
	fake.recoveryBinding = binding
	return fake.result, true, nil
}

func (fake *responseLossCandidateRepository) CreateCandidate(context.Context, CandidateRecord) (CommandResult, error) {
	return CommandResult{}, errors.New("response lost after durable interview candidate write")
}

type commandReceipt struct {
	binding CommandBinding
	result  CommandResult
}

func newMemoryRepositoryFake() *memoryRepositoryFake {
	return &memoryRepositoryFake{memories: make(map[foundation.ID]domain.Memory), commands: make(map[string]commandReceipt)}
}

func (fake *memoryRepositoryFake) FindCommand(_ context.Context, binding CommandBinding) (CommandResult, bool, error) {
	receipt, found := fake.commands[binding.IdempotencyKey]
	if !found {
		return CommandResult{}, false, nil
	}
	if !sameBinding(receipt.binding, binding) {
		return CommandResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict, false, errors.New("different memory command"))
	}
	result := receipt.result
	result.Replayed = true
	return result, true, nil
}

func (fake *memoryRepositoryFake) CreateCandidate(ctx context.Context, record CandidateRecord) (CommandResult, error) {
	if result, found, err := fake.FindCommand(ctx, record.Binding); err != nil || found {
		return result, err
	}
	fake.memories[record.Memory.ID] = domain.CloneMemory(record.Memory)
	result := CommandResult{Memory: domain.CloneMemory(record.Memory)}
	fake.commands[record.Binding.IdempotencyKey] = commandReceipt{binding: record.Binding, result: result}
	return result, nil
}

func (fake *memoryRepositoryFake) Mutate(ctx context.Context, record MutationRecord) (CommandResult, error) {
	if result, found, err := fake.FindCommand(ctx, record.Binding); err != nil || found {
		return result, err
	}
	current, found := fake.memories[record.Binding.MemoryID]
	if !found || !samePrincipal(current.Owner, record.Binding.Owner) {
		return CommandResult{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, errors.New("not found"))
	}
	if current.Version != record.Binding.ExpectedVersion {
		return CommandResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, errors.New("stale"))
	}
	fake.memories[current.ID] = domain.CloneMemory(record.Next)
	result := CommandResult{Memory: domain.CloneMemory(record.Next)}
	fake.commands[record.Binding.IdempotencyKey] = commandReceipt{binding: record.Binding, result: result}
	return result, nil
}

func (fake *memoryRepositoryFake) Get(_ context.Context, scope Scope, id foundation.ID) (domain.Memory, error) {
	memory, found := fake.memories[id]
	if !found || memory.WorkspaceID != scope.WorkspaceID || !samePrincipal(memory.Owner, scope.Owner) {
		return domain.Memory{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, errors.New("not found"))
	}
	return domain.CloneMemory(memory), nil
}

func (fake *memoryRepositoryFake) List(_ context.Context, query ListQuery) (ListPage, error) {
	items := make([]domain.Memory, 0)
	for _, memory := range fake.memories {
		if memory.WorkspaceID == query.Scope.WorkspaceID && samePrincipal(memory.Owner, query.Scope.Owner) {
			items = append(items, domain.CloneMemory(memory))
		}
	}
	return ListPage{Items: items}, nil
}

func (fake *memoryRepositoryFake) LoadEffective(_ context.Context, query EffectiveQuery) ([]domain.Memory, error) {
	items := make([]domain.Memory, 0)
	for _, memory := range fake.memories {
		if memory.WorkspaceID == query.Scope.WorkspaceID && samePrincipal(memory.Owner, query.Scope.Owner) &&
			memory.Status == domain.StatusActive && memory.ConfirmedAt != nil && memory.ConfirmedBy != nil &&
			(memory.TaskScopeID == nil || query.TaskScopeID != nil && *memory.TaskScopeID == *query.TaskScopeID) {
			items = append(items, domain.CloneMemory(memory))
		}
	}
	return items, nil
}

func (fake *memoryRepositoryFake) ExpireDue(context.Context, int) (int, error) { return 0, nil }

func newMemoryService(t *testing.T, repository Repository, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{Repository: repository, IDs: &testIDs{}, Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

type testIDs struct{ next int }

func (ids *testIDs) New() (foundation.ID, error) {
	ids.next++
	return testID(byte(ids.next + 20)), nil
}

func createCandidate(t *testing.T, service *Service, owner domain.Principal, key string) domain.Memory {
	t.Helper()
	result, err := service.CreateCandidate(context.Background(), CreateCandidateCommand{
		WorkspaceID: testID(1), Owner: owner, Type: domain.TypePreference, Content: json.RawMessage(`{"mode":"concise"}`),
		Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:proposal-1"}, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	return result.Memory
}

func testPrincipal(last byte) domain.Principal {
	return domain.Principal{Kind: domain.PrincipalUser, ID: testID(last)}
}

func testID(last byte) foundation.ID {
	return foundation.ID(fmt.Sprintf("20000000-0000-4000-8000-%012d", last))
}

func sameBinding(left, right CommandBinding) bool {
	return left.WorkspaceID == right.WorkspaceID && samePrincipal(left.Owner, right.Owner) &&
		(left.MemoryID == "" || right.MemoryID == "" || left.MemoryID == right.MemoryID) &&
		left.IdempotencyKey == right.IdempotencyKey && left.RequestHash == right.RequestHash && left.CommandType == right.CommandType && left.ExpectedVersion == right.ExpectedVersion
}

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
