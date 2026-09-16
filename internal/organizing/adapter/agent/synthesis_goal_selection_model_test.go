package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestGoalSelectionModelRecordsActualRequestAndCompletes(t *testing.T) {
	harness := newGoalSelectionModelHarness(t, false)
	completed, err := harness.model.Select(t.Context(), harness.execution, harness.selection)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != app.GoalSelectionSucceeded || completed.ModelRunID == "" || string(completed.ModelOutput) != goalSelectionModelOutput {
		t.Fatalf("completed=%+v", completed)
	}
	if harness.store.claimedInputHash == "" || harness.provider.CallCount() != 1 {
		t.Fatalf("claim hash=%q calls=%d", harness.store.claimedInputHash, harness.provider.CallCount())
	}
	payload, err := app.BuildGoalSelectionPayload(harness.input)
	if err != nil {
		t.Fatal(err)
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.GoalSelectionSchemaID, Version: "v1"}
	expected, err := agentapp.InitialStructuredRequest(harness.catalog, agentapp.StructuredRunRequest{
		ProfileRef: harness.profile.Ref, PromptRef: agentdomain.PromptRef{ID: ref.ID, Version: ref.Version},
		SchemaRef: ref, ReducedSchemaRef: ref, Input: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	rawExpected, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if harness.store.claimedInputHash != synthesisHash(rawExpected) {
		t.Fatalf("claim hash=%q want=%q", harness.store.claimedInputHash, synthesisHash(rawExpected))
	}
	actual, err := json.Marshal(harness.provider.Calls()[0])
	if err != nil || string(actual) != string(rawExpected) {
		t.Fatalf("actual provider request drifted: err=%v", err)
	}
	record, err := harness.store.GetModelRun(t.Context(), harness.selection.WorkspaceID, completed.ModelRunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.FinalResultType != agentdomain.ResultTypeGoalSelection || len(record.Calls) != 1 || record.Calls[0].Phase != agentdomain.ModelCallInitial {
		t.Fatalf("record=%+v", record)
	}
	replayed, err := harness.model.Select(t.Context(), harness.execution, harness.selection)
	if err != nil || replayed.ID != completed.ID || replayed.Status != app.GoalSelectionSucceeded || harness.provider.CallCount() != 1 {
		t.Fatalf("replayed=%+v err=%v calls=%d", replayed, err, harness.provider.CallCount())
	}
}

func TestGoalSelectionModelUnknownCompletionRequiresRecoveryWithoutReplay(t *testing.T) {
	harness := newGoalSelectionModelHarness(t, true)
	if _, err := harness.model.Select(t.Context(), harness.execution, harness.selection); synthesisTestCode(err) != "SYNTHESIS_GOAL_SELECTION_RECOVERY_REQUIRED" {
		t.Fatalf("first selection error=%v", err)
	}
	current, err := harness.store.GetGoalSelection(t.Context(), harness.selection.WorkspaceID, harness.selection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != app.GoalSelectionRecoveryRequired || harness.provider.CallCount() != 1 {
		t.Fatalf("current=%+v calls=%d", current, harness.provider.CallCount())
	}
	if _, err := harness.model.Select(t.Context(), harness.execution, current); synthesisTestCode(err) != "SYNTHESIS_GOAL_SELECTION_CONFLICT" {
		t.Fatalf("repeated selection error=%v", err)
	}
	if harness.provider.CallCount() != 1 {
		t.Fatalf("repeated selection issued provider call: %d", harness.provider.CallCount())
	}
}

const goalSelectionModelOutput = `{"selections":[{"point":"P001","reason":"Redis expiration is relevant to database study."}],"explanation":"The supplied point directly supports the requested database goal."}`

type goalSelectionModelHarness struct {
	model     *GoalSelectionModel
	provider  *agentapp.DeterministicChatModel
	store     *goalSelectionMemoryStore
	catalog   *agentapp.RuntimeCatalog
	profile   agentapp.ModelProfile
	selection app.SynthesisGoalSelection
	input     app.SynthesisGoalSelectionInput
	execution workflowapp.ExecutionContext
}

func newGoalSelectionModelHarness(t *testing.T, completeFails bool) goalSelectionModelHarness {
	t.Helper()
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterGoalSelectionRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "goal-selection-test", Version: "v1"}, Model: synthesisFixtureModelRef, Timeout: time.Second, MaxOutputTokens: 512}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	provider := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: synthesisFixtureModelRef, Content: []byte(goalSelectionModelOutput), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}})
	input := goalSelectionModelInput(t)
	payload, err := app.BuildGoalSelectionPayload(input)
	if err != nil {
		t.Fatal(err)
	}
	selection := app.SynthesisGoalSelection{
		ID: synthesisTestID(810), WorkspaceID: input.Source.WorkspaceID, RequestID: input.RequestID, CatalogBatchID: input.CatalogBatchID,
		SourceOrdinal: input.SourceOrdinal, PointOffset: input.PointOffset, PointCount: len(input.Points), PayloadHash: synthesisHash(payload),
		Status: app.GoalSelectionPending, ScheduledWorkflowID: synthesisTestID(811), Version: 1, CreatedAt: synthesisFixtureTime, UpdatedAt: synthesisFixtureTime,
	}
	store := &goalSelectionMemoryStore{
		synthesisMemoryStore: &synthesisMemoryStore{runs: make(map[foundation.ID]agentdomain.ModelRun), calls: make(map[foundation.ID][]agentdomain.ModelCall), steps: make(map[foundation.ID]app.SynthesisModelStepRecord)},
		selection:            selection, input: input, completeFails: completeFails,
	}
	model, err := NewGoalSelectionModel(GoalSelectionModelDependencies{Model: provider, ModelRuns: store, Store: store, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: &synthesisTestIDs{}, Clock: foundation.FixedClock{Value: synthesisFixtureTime}})
	if err != nil {
		t.Fatal(err)
	}
	return goalSelectionModelHarness{model: model, provider: provider, store: store, catalog: catalog, profile: profile, selection: selection, input: input,
		execution: workflowapp.ExecutionContext{WorkspaceID: selection.WorkspaceID, RunID: synthesisTestID(812), NodeRunID: synthesisTestID(813), NodeAttemptID: synthesisTestID(814)}}
}

func goalSelectionModelInput(t *testing.T) app.SynthesisGoalSelectionInput {
	t.Helper()
	text := "Redis expiration limits cache lifetime."
	source := domain.SynthesisSourceVersion{WorkspaceID: synthesisTestID(800), SourceID: synthesisTestID(801), SourceVersionID: synthesisTestID(802), ContentArtifactID: synthesisTestID(803), ParseProjectionID: synthesisTestID(804), ContentHash: synthesisHash([]byte(text))}
	return app.SynthesisGoalSelectionInput{
		RequestID: synthesisTestID(805), CatalogBatchID: synthesisTestID(806), SourceOrdinal: 0, PointOffset: 0,
		Goal: "整理数据库知识，用于系统学习", Source: source, ProfileRevisionID: synthesisTestID(807), Title: "Redis 缓存笔记", Summary: "Redis 缓存过期策略。",
		Points: []app.KnowledgeDirectoryPoint{{Locator: app.KnowledgePointLocator{ProfileRevisionID: synthesisTestID(807), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}, Text: text, SourceSpanIDs: []foundation.ID{synthesisTestID(808)}}},
	}
}

type goalSelectionMemoryStore struct {
	*synthesisMemoryStore
	mu               sync.Mutex
	selection        app.SynthesisGoalSelection
	input            app.SynthesisGoalSelectionInput
	claimedInputHash string
	completeFails    bool
}

func (*goalSelectionMemoryStore) PrepareGoalSelections(context.Context, foundation.ID, foundation.ID, int64) ([]app.SynthesisGoalSelection, error) {
	return nil, errors.New("not used by goal selection model")
}

func (store *goalSelectionMemoryStore) GetGoalSelection(_ context.Context, workspaceID, selectionID foundation.ID) (app.SynthesisGoalSelection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.selection.WorkspaceID != workspaceID || store.selection.ID != selectionID {
		return app.SynthesisGoalSelection{}, errors.New("selection not found")
	}
	return cloneGoalSelection(store.selection), nil
}

func (store *goalSelectionMemoryStore) ReadGoalSelectionInput(_ context.Context, workspaceID, selectionID foundation.ID) (app.SynthesisGoalSelectionInput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.selection.WorkspaceID != workspaceID || store.selection.ID != selectionID {
		return app.SynthesisGoalSelectionInput{}, errors.New("selection not found")
	}
	return cloneGoalSelectionInput(store.input), nil
}

func (store *goalSelectionMemoryStore) ClaimGoalSelection(_ context.Context, command app.ClaimGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if command.WorkspaceID != store.selection.WorkspaceID || command.SelectionID != store.selection.ID || command.ExpectedVersion != store.selection.Version || store.selection.Status != app.GoalSelectionPending {
		return app.SynthesisGoalSelection{}, errors.New("selection claim conflict")
	}
	store.selection.Status, store.selection.WorkflowRunID, store.selection.NodeRunID, store.selection.NodeAttemptID = app.GoalSelectionRunning, command.WorkflowRunID, command.NodeRunID, command.NodeAttemptID
	store.selection.ModelInputHash, store.claimedInputHash = command.ModelInputHash, command.ModelInputHash
	store.selection.Version++
	return cloneGoalSelection(store.selection), nil
}

func (store *goalSelectionMemoryStore) CompleteGoalSelection(_ context.Context, command app.CompleteGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if command.WorkspaceID != store.selection.WorkspaceID || command.SelectionID != store.selection.ID || command.ExpectedVersion != store.selection.Version || store.selection.Status != app.GoalSelectionRunning {
		return app.SynthesisGoalSelection{}, errors.New("selection completion conflict")
	}
	if store.completeFails {
		return app.SynthesisGoalSelection{}, errors.New("completion response unavailable")
	}
	store.selection.Status, store.selection.ModelRunID, store.selection.ModelOutput = app.GoalSelectionSucceeded, command.ModelRunID, append([]byte(nil), command.ModelOutput...)
	store.selection.Version++
	return cloneGoalSelection(store.selection), nil
}

func (store *goalSelectionMemoryStore) FailGoalSelection(_ context.Context, command app.FailGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if command.WorkspaceID != store.selection.WorkspaceID || command.SelectionID != store.selection.ID || command.ExpectedVersion != store.selection.Version || store.selection.Status != app.GoalSelectionRunning {
		return app.SynthesisGoalSelection{}, errors.New("selection failure conflict")
	}
	store.selection.Status, store.selection.ErrorCode, store.selection.Retryable = command.Status, command.ErrorCode, command.Retryable
	store.selection.Version++
	return cloneGoalSelection(store.selection), nil
}

func cloneGoalSelection(value app.SynthesisGoalSelection) app.SynthesisGoalSelection {
	value.ModelOutput = append([]byte(nil), value.ModelOutput...)
	return value
}

func cloneGoalSelectionInput(value app.SynthesisGoalSelectionInput) app.SynthesisGoalSelectionInput {
	value.Points = append([]app.KnowledgeDirectoryPoint(nil), value.Points...)
	for index := range value.Points {
		value.Points[index].SourceSpanIDs = append([]foundation.ID(nil), value.Points[index].SourceSpanIDs...)
	}
	return value
}
