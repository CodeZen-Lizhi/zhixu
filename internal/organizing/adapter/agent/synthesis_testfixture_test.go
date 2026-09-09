package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const synthesisFactOutput = `{"notes":[{"note":"","topic_key":"cache lifetime","title":"Cache lifetime","aliases":[],"operations":[{"op":"ADD_FACT","statement":{"text":"Entries expire after five minutes.","applicability":"","sources":["S001"]}}]}]}`
const synthesisFactReview = `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`

var synthesisFixtureModelRef = agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "synthesis-fixture", ModelVersion: "v1"}
var synthesisFixtureTime = time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)

type synthesisTestIDs struct{ count atomic.Int64 }

func (ids *synthesisTestIDs) New() (foundation.ID, error) {
	return synthesisTestID(10000 + int(ids.count.Add(1))), nil
}

type synthesisTrackingScheduler struct {
	delegate agentapp.StructuredPhaseScheduler
	calls    atomic.Int64
}

func (scheduler *synthesisTrackingScheduler) Schedule(ctx context.Context, run *agentapp.StructuredPhaseRun) error {
	scheduler.calls.Add(1)
	return scheduler.delegate.Schedule(ctx, run)
}

type synthesisHarness struct {
	model     *SynthesisModel
	provider  *agentapp.DeterministicChatModel
	store     *synthesisMemoryStore
	ids       *synthesisTestIDs
	scheduler *synthesisTrackingScheduler
}

func newSynthesisHarness(t *testing.T, outputs ...string) synthesisHarness {
	t.Helper()
	steps := make([]agentapp.DeterministicChatStep, len(outputs))
	for index, output := range outputs {
		steps[index] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: synthesisFixtureModelRef, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}}
	}
	return newSynthesisHarnessSteps(t, steps...)
}

func newSynthesisHarnessSteps(t *testing.T, steps ...agentapp.DeterministicChatStep) synthesisHarness {
	t.Helper()
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterSynthesisRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "synthesis-test", Version: "v1"}, Model: synthesisFixtureModelRef, Timeout: time.Second, MaxOutputTokens: 2048}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	delegate, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &synthesisTrackingScheduler{delegate: delegate}
	provider := agentapp.NewDeterministicChatModel(steps...)
	store := &synthesisMemoryStore{runs: make(map[foundation.ID]agentdomain.ModelRun), calls: make(map[foundation.ID][]agentdomain.ModelCall), steps: make(map[foundation.ID]organizingapp.SynthesisModelStepRecord)}
	ids := &synthesisTestIDs{}
	model, err := NewSynthesisModel(SynthesisModelDependencies{Model: provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: store, Store: store, ProfileRef: profile.Ref, IDs: ids, Clock: foundation.FixedClock{Value: synthesisFixtureTime}})
	if err != nil {
		t.Fatal(err)
	}
	return synthesisHarness{model: model, provider: provider, store: store, ids: ids, scheduler: scheduler}
}

func synthesisFixtureInput(t *testing.T) (organizingapp.SynthesisGenerationInput, workflowapp.ExecutionContext, workflowapp.ExecutionContext) {
	t.Helper()
	source := synthesisFixtureSource(100, "RAW-EXCERPT-ONLY: Entries expire after five minutes. This source contains no sizing guidance.")
	input := organizingapp.SynthesisGenerationInput{
		ProcessingID: synthesisTestID(7), WorkflowRunID: synthesisTestID(8), NodeRunID: synthesisTestID(9), NodeAttemptID: synthesisTestID(10),
		SourceEvent: domain.SynthesisSourceReady{ID: synthesisTestID(11), Source: source.Reference.Source, IngestionAttemptID: synthesisTestID(12), ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: synthesisFixtureTime},
		RequestHash: synthesisHash([]byte("frozen synthesis request")), Notes: []organizingapp.SynthesisGenerationNote{}, Sources: []organizingapp.SynthesisSourceExcerpt{source},
	}
	generation := workflowapp.ExecutionContext{WorkspaceID: source.Reference.Source.WorkspaceID, RunID: input.WorkflowRunID, NodeRunID: input.NodeRunID, NodeAttemptID: input.NodeAttemptID}
	validation := generation
	validation.NodeRunID, validation.NodeAttemptID = synthesisTestID(13), synthesisTestID(14)
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	return input, generation, validation
}

func synthesisFixtureSource(base int, text string) organizingapp.SynthesisSourceExcerpt {
	return organizingapp.SynthesisSourceExcerpt{Reference: domain.SynthesisSourceRef{
		Source:       domain.SynthesisSourceVersion{WorkspaceID: synthesisTestID(1), SourceID: synthesisTestID(base + 1), SourceVersionID: synthesisTestID(base + 2), ContentArtifactID: synthesisTestID(base + 3), ParseProjectionID: synthesisTestID(base + 4), ContentHash: synthesisHash([]byte(text))},
		SourceSpanID: synthesisTestID(base + 5), ExcerptHash: synthesisHash([]byte(text)), Title: "Original source",
	}, Text: text}
}

func synthesisFixtureWithNote(t *testing.T) (organizingapp.SynthesisGenerationInput, workflowapp.ExecutionContext, workflowapp.ExecutionContext) {
	t.Helper()
	input, generation, validation := synthesisFixtureInput(t)
	reference := input.Sources[0].Reference
	items := []domain.SynthesisItem{
		{ID: synthesisTestID(31), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "Entries expire after five minutes.", Sources: []domain.SynthesisSourceRef{reference}}},
		{ID: synthesisTestID(32), Kind: domain.SynthesisConflictItem, Conflict: &domain.SynthesisConflictContent{Subject: "Cache duration", Alternatives: []domain.SynthesisStatement{{Text: "Use five minutes.", Applicability: "Stable values", Sources: []domain.SynthesisSourceRef{reference}}, {Text: "Use one minute.", Applicability: "Changing values", Sources: []domain.SynthesisSourceRef{reference}}}}},
		{ID: synthesisTestID(33), Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "How is memory bounded?", Context: "", Sources: []domain.SynthesisSourceRef{reference}}},
	}
	delta := domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &items[0]}, {Kind: domain.SynthesisAddConflict, Item: &items[1]}, {Kind: domain.SynthesisAddGap, Item: &items[2]}}}
	note := domain.SynthesisNote{ID: synthesisTestID(21), WorkspaceID: reference.Source.WorkspaceID, DocumentID: synthesisTestID(22), CurrentRevisionID: synthesisTestID(23), TopicKey: "cache lifetime", Title: "Cache lifetime", Aliases: []string{"cache"}, Version: 1, Status: domain.SynthesisPendingApproval, CreatedAt: synthesisFixtureTime, UpdatedAt: synthesisFixtureTime}
	body, err := domain.RenderSynthesisMarkdown(note.WorkspaceID, note.ID, note.Title, items)
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.SynthesisRevision{ID: note.CurrentRevisionID, WorkspaceID: note.WorkspaceID, NoteID: note.ID, DocumentID: note.DocumentID, ArticleRevisionID: synthesisTestID(24), RevisionNo: 1, ArticleRevisionNo: 1, Title: note.Title, RendererVersion: domain.SynthesisRendererVersion, ContentHash: synthesisHash([]byte(body)), Items: items, Delta: delta, SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: synthesisTestID(25), CreatedAt: synthesisFixtureTime}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	input.Notes = []organizingapp.SynthesisGenerationNote{{Note: note, Revision: revision}}
	incoming := synthesisFixtureSource(200, "RAW-INCOMING-ONLY: Stable values expire after five minutes; changing values after one minute. Keep at most 100 entries.")
	input.SourceEvent.Source = incoming.Reference.Source
	input.Sources = append(input.Sources, incoming)
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	return input, generation, validation
}

func synthesisTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("71000000-0000-4000-8000-%012d", value))
}

func synthesisTestCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

// This in-memory fixture exercises the real RecordingChatModel and Eino
// scheduler. It is not PostgreSQL transaction or semantic-quality evidence.
type synthesisMemoryStore struct {
	mu                   sync.Mutex
	runs                 map[foundation.ID]agentdomain.ModelRun
	calls                map[foundation.ID][]agentdomain.ModelCall
	steps                map[foundation.ID]organizingapp.SynthesisModelStepRecord
	loseCompleteResponse bool
	dropComplete         bool
	failCallCompletion   bool
}

func (store *synthesisMemoryStore) CreateModelRun(_ context.Context, run agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return run, false, err
	}
	for _, existing := range store.runs {
		if existing.NodeAttemptID == run.NodeAttemptID {
			return existing, true, nil
		}
	}
	store.runs[run.ID] = run
	return run, false, nil
}

func (store *synthesisMemoryStore) GetModelRun(_ context.Context, workspaceID, modelRunID foundation.ID) (agentapp.ModelRunRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	run, found := store.runs[modelRunID]
	if !found || run.WorkspaceID != workspaceID {
		return agentapp.ModelRunRecord{}, errors.New("model run not found")
	}
	return agentapp.ModelRunRecord{Run: run, Calls: append([]agentdomain.ModelCall(nil), store.calls[modelRunID]...)}, nil
}

func (store *synthesisMemoryStore) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := agentdomain.ValidateModelCall(call); err != nil {
		return call, false, err
	}
	for _, existing := range store.calls[call.ModelRunID] {
		if existing.CallNo == call.CallNo {
			return existing, true, nil
		}
	}
	store.calls[call.ModelRunID] = append(store.calls[call.ModelRunID], call)
	return call, false, nil
}

func (store *synthesisMemoryStore) CompleteModelCall(_ context.Context, command agentapp.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failCallCompletion && command.Call.Status == agentdomain.ModelCallSucceeded {
		store.failCallCompletion = false
		return agentdomain.ModelCall{}, false, errors.New("lost call completion")
	}
	if err := agentdomain.ValidateModelCall(command.Call); err != nil {
		return command.Call, false, err
	}
	for index, call := range store.calls[command.Call.ModelRunID] {
		if call.ID != command.Call.ID {
			continue
		}
		if call.Version != command.ExpectedVersion {
			return call, false, errors.New("call version conflict")
		}
		store.calls[call.ModelRunID][index] = command.Call
		return command.Call, false, nil
	}
	return agentdomain.ModelCall{}, false, errors.New("call missing")
}

func (store *synthesisMemoryStore) FinalizeModelRun(_ context.Context, command agentapp.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := agentdomain.ValidateModelRun(command.Run); err != nil {
		return command.Run, false, err
	}
	store.runs[command.Run.ID] = command.Run
	return command.Run, false, nil
}

func (*synthesisMemoryStore) MarkStaleModelCallsUnknown(context.Context, agentapp.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, nil
}
func (*synthesisMemoryStore) MarkStaleModelRunsUnknown(context.Context, agentapp.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, nil
}

func (store *synthesisMemoryStore) LookupReady(_ context.Context, workspaceID, nodeID foundation.ID, stage organizingapp.SynthesisModelStage, hash string) (organizingapp.SynthesisModelStepRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.steps {
		if record.WorkspaceID == workspaceID && record.NodeRunID == nodeID && record.Stage == stage && record.RequestHash == hash && record.Status == organizingapp.SynthesisModelStepReady {
			return synthesisCloneStep(record), true, nil
		}
	}
	return organizingapp.SynthesisModelStepRecord{}, false, nil
}

func (store *synthesisMemoryStore) Prepare(_ context.Context, command organizingapp.PrepareSynthesisModelStepCommand) (organizingapp.SynthesisModelStepRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := command.Record.Validate(); err != nil {
		return command.Record, false, err
	}
	for _, existing := range store.steps {
		if existing.NodeRunID == command.Record.NodeRunID && existing.Stage == command.Record.Stage {
			if existing.RequestHash != command.Record.RequestHash {
				return existing, false, synthesisReplayUnsafe()
			}
			if existing.NodeAttemptID == command.Record.NodeAttemptID || existing.Status == organizingapp.SynthesisModelStepReady {
				return synthesisCloneStep(existing), true, nil
			}
			return existing, false, synthesisReplayUnsafe()
		}
	}
	store.steps[command.Record.ID] = synthesisCloneStep(command.Record)
	return command.Record, false, nil
}

func (store *synthesisMemoryStore) BindModelRun(_ context.Context, command organizingapp.BindSynthesisModelRunCommand) (organizingapp.SynthesisModelStepRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.steps[command.StepID]
	if record.WorkspaceID != command.WorkspaceID || record.Version != command.ExpectedVersion {
		return record, errors.New("step version conflict")
	}
	record.ModelRunID, record.Version = command.ModelRunID, record.Version+1
	store.steps[record.ID] = synthesisCloneStep(record)
	return record, nil
}

func (store *synthesisMemoryStore) Complete(_ context.Context, command organizingapp.CompleteSynthesisModelStepCommand) (organizingapp.SynthesisModelStepRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.dropComplete {
		return organizingapp.SynthesisModelStepRecord{}, false, errors.New("complete unavailable")
	}
	record := store.steps[command.StepID]
	run := store.runs[command.ModelRunID]
	if record.WorkspaceID != command.WorkspaceID || record.Version != command.ExpectedVersion || run.Version != command.ExpectedModelRunVersion || record.ModelRunID != run.ID {
		return record, false, errors.New("completion binding drift")
	}
	record.Status, record.Output, record.OutputHash = organizingapp.SynthesisModelStepReady, command.Output, command.OutputHash
	record.Generation, record.Semantic = command.Generation, command.Semantic
	record.UpdatedAt, record.CompletedAt, record.Version = command.CompletedAt, &command.CompletedAt, record.Version+1
	run.Status, run.FinalResultType, run.UpdatedAt, run.CompletedAt, run.Version = agentdomain.ModelRunSucceeded, command.ResultType, command.CompletedAt, &command.CompletedAt, run.Version+1
	if err := record.Validate(); err != nil {
		return record, false, err
	}
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return record, false, err
	}
	store.steps[record.ID], store.runs[run.ID] = synthesisCloneStep(record), run
	if store.loseCompleteResponse {
		store.loseCompleteResponse = false
		return organizingapp.SynthesisModelStepRecord{}, false, errors.New("commit response lost")
	}
	return synthesisCloneStep(record), false, nil
}

func (store *synthesisMemoryStore) Fail(_ context.Context, command organizingapp.FailSynthesisModelStepCommand) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.steps[command.StepID]
	run := store.runs[command.ModelRunID]
	if record.WorkspaceID != command.WorkspaceID || record.Version != command.ExpectedVersion || run.Version != command.ExpectedModelRunVersion {
		return errors.New("failure binding drift")
	}
	record.Status, record.ErrorCode, record.Retryable = organizingapp.SynthesisModelStepFailed, command.ErrorCode, command.Retryable
	if command.ModelRunStatus == agentdomain.ModelRunUnknown {
		record.Status = organizingapp.SynthesisModelStepRecoveryRequired
	}
	record.UpdatedAt, record.CompletedAt, record.Version = command.CompletedAt, &command.CompletedAt, record.Version+1
	run.Status, run.FinalErrorCode, run.UpdatedAt, run.CompletedAt, run.Version = command.ModelRunStatus, command.ErrorCode, command.CompletedAt, &command.CompletedAt, run.Version+1
	if err := record.Validate(); err != nil {
		return err
	}
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return err
	}
	store.steps[record.ID], store.runs[run.ID] = synthesisCloneStep(record), run
	return nil
}

func synthesisCloneStep(record organizingapp.SynthesisModelStepRecord) organizingapp.SynthesisModelStepRecord {
	raw, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	var cloned organizingapp.SynthesisModelStepRecord
	if err := json.Unmarshal(raw, &cloned); err != nil {
		panic(err)
	}
	cloned.Output = append(json.RawMessage(nil), record.Output...)
	return cloned
}
