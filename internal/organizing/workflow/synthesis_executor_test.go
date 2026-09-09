package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestSynthesisDefinitionMatchesItsRegisteredCanonicalHash(t *testing.T) {
	catalog, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range SynthesisExecutorNodeKinds() {
		if err := executors.Register(kind, 1, &SynthesisExecutor{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapp.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	definition := SynthesisRegisteredDefinitions()[0]
	expected, err := workflowapp.ComputeCanonicalGraphHash(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	registered, err := registry.Resolve(SynthesisDefinitionKey, SynthesisDefinitionVersion)
	if err != nil || registered.GraphHash != expected {
		t.Fatalf("fixed graph drifted after canonicalization: %v", err)
	}
	previous := map[string]string{SynthesisPrepareNodeKind: "", SynthesisGenerateNodeKind: SynthesisPrepareNodeKind, SynthesisValidateNodeKind: SynthesisGenerateNodeKind, SynthesisApplyNodeKind: SynthesisValidateNodeKind}
	for _, node := range registered.Graph.Nodes {
		if prior := previous[node.Kind]; prior == "" && len(node.Dependencies) != 0 || prior != "" && (len(node.Dependencies) != 1 || node.Dependencies[0] != prior) {
			t.Fatalf("node dependency changed: %+v", node)
		}
		if (node.Kind == SynthesisGenerateNodeKind || node.Kind == SynthesisValidateNodeKind) && node.RetryPolicy.MaxRetries != 0 {
			t.Fatal("model nodes gained automatic paid retries")
		}
	}
}

func TestSynthesisApplyRecoversCommittedCandidateBeforeOpeningStaleSources(t *testing.T) {
	executor, execution, store, candidates, sources := synthesisExecutorFixture(t)
	candidates.applied = organizingapp.SynthesisApplyResult{ProcessingID: store.value.Processing.ID, Changed: true, RevisionIDs: []foundation.ID{synthesisWorkflowTestID(30)}}
	candidates.found = true
	result, err := executor.Execute(t.Context(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if candidates.recoveries != 1 || candidates.applications != 0 || sources.reads != 0 || store.prepares != 1 || store.records != 1 {
		t.Fatalf("recovery repeated original work: candidate=%+v source reads=%d", candidates, sources.reads)
	}
	var receipt SynthesisNodeReceipt
	if err := json.Unmarshal(result.Output, &receipt); err != nil || receipt.ProcessingID != store.value.Processing.ID || len(receipt.RevisionIDs) != 1 || receipt.RequestHash != store.value.Input.RequestHash {
		t.Fatalf("recovery receipt=%+v err=%v", receipt, err)
	}
}

func TestSynthesisPublicationRecoveryDoesNotRequireAReopenedModelInput(t *testing.T) {
	executor, execution, store, candidates, sources := synthesisExecutorFixture(t)
	store.value.ApplyRecovery = true
	store.value.Input = nil
	store.value.Semantic = nil
	setSynthesisRunInput(t, executor, store.value.Processing.ID, true)
	candidates.applied = organizingapp.SynthesisApplyResult{ProcessingID: store.value.Processing.ID, RevisionIDs: []foundation.ID{}}
	candidates.found = true
	result, err := executor.Execute(t.Context(), execution)
	if err != nil || sources.reads != 0 || candidates.applications != 0 || store.records != 1 {
		t.Fatalf("publication-only recovery=%s err=%v", result.Output, err)
	}
	var receipt SynthesisNodeReceipt
	if err := json.Unmarshal(result.Output, &receipt); err != nil || receipt.RequestHash != "" {
		t.Fatalf("publication-only recovery added a model input: %v", err)
	}
}

func TestSynthesisPaidStepReplayAndPersistedDeadline(t *testing.T) {
	executor, execution, store, _, sources := synthesisExecutorFixture(t)
	execution.NodeKind, execution.NodeKey = SynthesisGenerateNodeKind, SynthesisGenerateNodeKind
	store.value.Generation = &organizingapp.SynthesisModelStepRecord{Status: organizingapp.SynthesisModelStepReady, InputRequestHash: store.value.Input.RequestHash}
	if _, err := executor.Execute(t.Context(), execution); err != nil || sources.reads != 0 {
		t.Fatalf("accepted generation replay opened source: %v", err)
	}
	store.value.CreatedAt = store.value.CreatedAt.Add(-SynthesisMaxExecutionAge - time.Second)
	_, err := executor.Execute(t.Context(), execution)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeSynthesisExecutionBudget {
		t.Fatalf("expired execution result=%v", err)
	}
}

func TestSynthesisRejectsChangedQueueIdentityAndMissingRequiredFields(t *testing.T) {
	executor, execution, store, _, sources := synthesisExecutorFixture(t)
	execution.WorkspaceID = synthesisWorkflowTestID(88)
	if _, err := executor.Execute(t.Context(), execution); err == nil || sources.reads != 0 {
		t.Fatalf("foreign Workspace accepted: %v", err)
	}
	for _, raw := range []string{
		`{"processing_id":"` + string(store.value.Processing.ID) + `","execution_no":1}`,
		`{"processing_id":"` + string(store.value.Processing.ID) + `","execution_no":1,"apply_recovery":false,"text":"private"}`,
		`{"processing_id":"` + string(store.value.Processing.ID) + `","execution_no":1,"apply_recovery":null}`,
	} {
		if _, err := DecodeSynthesisStartInput([]byte(raw)); err == nil {
			t.Fatalf("invalid queue input accepted: %s", raw)
		}
	}
}

func synthesisExecutorFixture(t *testing.T) (*SynthesisExecutor, workflowapp.ExecutionContext, *synthesisExecutionFixtureStore, *synthesisCandidateFixture, *synthesisSourceFixture) {
	t.Helper()
	at := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	source := organizingdomain.SynthesisSourceVersion{WorkspaceID: synthesisWorkflowTestID(1), SourceID: synthesisWorkflowTestID(2), SourceVersionID: synthesisWorkflowTestID(3), ContentArtifactID: synthesisWorkflowTestID(4), ParseProjectionID: synthesisWorkflowTestID(5), ContentHash: strings.Repeat("a", 64)}
	event := organizingdomain.SynthesisSourceReady{ID: synthesisWorkflowTestID(6), Source: source, IngestionAttemptID: synthesisWorkflowTestID(7), ProcessorVersion: organizingdomain.SynthesisProcessorVersion, CreatedAt: at}
	processingID, runID := synthesisWorkflowTestID(8), synthesisWorkflowTestID(9)
	frozen := SynthesisFrozenInput{ProcessingID: processingID, WorkflowRunID: runID, SourceEvent: event, Notes: []SynthesisFrozenNote{}, Sources: []organizingdomain.SynthesisSourceRef{{Source: source, SourceSpanID: synthesisWorkflowTestID(10), ExcerptHash: strings.Repeat("b", 64), Title: "source"}}}
	frozen.RequestHash, _ = frozen.ComputeHash()
	if err := frozen.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &synthesisExecutionFixtureStore{value: SynthesisExecution{Processing: organizingapp.SynthesisProcessing{ID: processingID, SourceEvent: event}, WorkflowRunID: runID, ExecutionNo: 1, Input: &frozen, CreatedAt: at, Semantic: &organizingapp.SynthesisModelStepRecord{Status: organizingapp.SynthesisModelStepReady, Semantic: &organizingapp.SynthesisSemanticReceipt{Accepted: true}}}}
	run := &synthesisRunFixture{value: workflowdomain.Run{ID: runID, WorkspaceID: source.WorkspaceID, DefinitionID: synthesisWorkflowTestID(11), IdempotencyKey: SynthesisStartIdempotencyKey(processingID, 1)}}
	candidates, sources := &synthesisCandidateFixture{}, &synthesisSourceFixture{}
	executor, err := NewSynthesisExecutor(SynthesisExecutorDependencies{Runs: run, Store: store, Candidates: candidates, Sources: sources, Model: &synthesisUnexpectedModel{}, Clock: foundation.FixedClock{Value: at}})
	if err != nil {
		t.Fatal(err)
	}
	setSynthesisRunInput(t, executor, processingID, false)
	execution := workflowapp.ExecutionContext{WorkspaceID: source.WorkspaceID, RunID: runID, DefinitionID: run.value.DefinitionID, DefinitionHash: strings.Repeat("c", 64), DefinitionVersion: 1, NodeRunID: synthesisWorkflowTestID(12), NodeAttemptID: synthesisWorkflowTestID(13), NodeKey: SynthesisApplyNodeKind, NodeKind: SynthesisApplyNodeKind, InputSchemaVersion: 1}
	return executor, execution, store, candidates, sources
}

func setSynthesisRunInput(t *testing.T, executor *SynthesisExecutor, processingID foundation.ID, recovery bool) {
	t.Helper()
	raw, err := json.Marshal(SynthesisStartInput{ProcessingID: processingID, ExecutionNo: 1, ApplyRecovery: recovery})
	if err != nil {
		t.Fatal(err)
	}
	executor.dependencies.Runs.(*synthesisRunFixture).value.Input = raw
}

type synthesisExecutionFixtureStore struct {
	SynthesisProcessingStore
	value             SynthesisExecution
	prepares, records int
}

func (s *synthesisExecutionFixtureStore) LoadSynthesisExecution(context.Context, foundation.ID, foundation.ID, foundation.ID) (SynthesisExecution, error) {
	return s.value, nil
}
func (s *synthesisExecutionFixtureStore) PrepareSynthesisApplication(context.Context, workflowapp.ExecutionContext, foundation.ID) error {
	s.prepares++
	return nil
}
func (s *synthesisExecutionFixtureStore) RecordSynthesisApplication(context.Context, workflowapp.ExecutionContext, organizingapp.SynthesisApplyResult) error {
	s.records++
	return nil
}

type synthesisRunFixture struct{ value workflowdomain.Run }

func (s *synthesisRunFixture) GetRun(context.Context, foundation.ID) (workflowdomain.Run, error) {
	return s.value, nil
}

type synthesisCandidateFixture struct {
	SynthesisCandidateOwner
	applied                  organizingapp.SynthesisApplyResult
	found                    bool
	recoveries, applications int
}

func (s *synthesisCandidateFixture) RecoverAppliedGeneration(context.Context, foundation.ID, foundation.ID) (organizingapp.SynthesisApplyResult, bool, error) {
	s.recoveries++
	return s.applied, s.found, nil
}
func (s *synthesisCandidateFixture) ApplyGeneration(context.Context, organizingapp.SynthesisGenerationInput, organizingapp.SynthesisGenerationResult) (organizingapp.SynthesisApplyResult, error) {
	s.applications++
	return organizingapp.SynthesisApplyResult{}, errors.New("unexpected fresh application")
}

type synthesisSourceFixture struct {
	organizingapp.SynthesisSourceReader
	reads int
}

func (s *synthesisSourceFixture) ReadSynthesisSource(context.Context, organizingdomain.SynthesisSourceVersion) ([]organizingapp.SynthesisSourceExcerpt, error) {
	s.reads++
	return nil, errors.New("source is stale")
}

type synthesisUnexpectedModel struct{ SynthesisExecutionModel }

func synthesisWorkflowTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("7d000000-0000-4000-8000-%012d", value))
}
