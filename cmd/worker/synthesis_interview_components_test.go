package main

import (
	"context"
	"errors"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestSynthesisInterviewCompositionRegistersDurableUnavailableExecutor(t *testing.T) {
	persistence, runs := synthesisInterviewCompositionFixture(t)
	components, err := newWorkerSynthesisInterviewComponents(persistence, runs, agentWorkflowComponents{})
	if err != nil || components.capability.available || components.executor == nil {
		t.Fatalf("unconfigured interview composition: %v", err)
	}
	_, err = components.model.GenerateNoteInterview(t.Context(), workflowapplication.ExecutionContext{}, interviewapplication.NotePreparation{})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != interviewapplication.ErrorCodeNotePlanUnavailable {
		t.Fatalf("unconfigured model must return its explicit capability error: %v", err)
	}
	executors, definitions := synthesisInterviewRegistries(t, components)
	resolved, err := executors.Resolve(interviewapplication.NotePreparationNodeKind, interviewapplication.NotePreparationSchemaVersion)
	if err != nil || resolved != components.executor {
		t.Fatalf("the preparation must retain its real executor: %v", err)
	}
	if _, err := definitions.Resolve(interviewapplication.NotePreparationDefinitionKey, interviewapplication.NotePreparationDefinitionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := newWorkerSynthesisInterviewComponents(persistence, runs, agentWorkflowComponents{capability: agentCapabilityStatus{available: true}}); err == nil {
		t.Fatal("configured capability without its model was accepted")
	}
}

func TestSynthesisInterviewCompositionRebuildsFrozenModelProfile(t *testing.T) {
	persistence, runs := synthesisInterviewCompositionFixture(t)
	scheduler, err := newStructuredPhaseScheduler()
	if err != nil {
		t.Fatal(err)
	}
	agent := agentWorkflowComponents{
		model: &synthesisInterviewCompositionModel{}, organizingScheduler: scheduler,
		contract: platformmodels.ChatContract{
			Model:   agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "1", ModelID: "interview-model", ModelVersion: "first"},
			Timeout: time.Second, MaxRequestBytes: 256 << 10, MaxResponseBytes: 256 << 10,
		},
	}
	first, err := newWorkerSynthesisInterviewComponents(persistence, runs, agent)
	if err != nil {
		t.Fatal(err)
	}
	agent.contract.Model.ModelVersion = "second"
	agent.model = &synthesisInterviewCompositionModel{}
	second, err := newWorkerSynthesisInterviewComponents(persistence, runs, agent)
	if err != nil {
		t.Fatal(err)
	}
	if first.executor == second.executor || first.model == second.model || first.catalog == second.catalog {
		t.Fatal("model generations reused a bound interview component")
	}
	for _, test := range []struct {
		components workerSynthesisInterviewComponents
		version    string
	}{{first, "first"}, {second, "second"}} {
		snapshot, err := test.components.catalog.Snapshot(interviewapplication.NoteModelPromptRef(), interviewapplication.NoteModelSchemaRef(), interviewapplication.NoteModelSchemaRef(), agentworkflow.DefaultProfileRef())
		if err != nil || snapshot.Profile.Model.ModelVersion != test.version {
			t.Fatalf("interview model generation lost its frozen profile: %v", err)
		}
		synthesisInterviewRegistries(t, test.components)
	}
	agent.organizingScheduler = nil
	if _, err := newWorkerSynthesisInterviewComponents(persistence, runs, agent); err == nil {
		t.Fatal("configured interview model accepted a missing Eino scheduler")
	}
}

// This fixture intentionally uses an unreachable address: constructors must not
// connect, migrate, start a worker, or invoke a Provider. It is not a DB test.
func synthesisInterviewCompositionFixture(t *testing.T) (*workerSynthesisInterviewPersistence, *workflowpostgres.GORMRepository) {
	t.Helper()
	pool, err := platformpostgres.Open(t.Context(), "postgres://postgres@127.0.0.1:1/interview_constructor?sslmode=disable", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	modelRuns, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := newWorkerSynthesisInterviewPersistence(pool, modelRuns)
	if err != nil {
		t.Fatal(err)
	}
	if terminal, err := persistence.terminalHook(); err != nil || terminal == nil {
		t.Fatalf("terminal hook must be available before runtime: %v", err)
	}
	runs, err := workflowpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	return persistence, runs
}

func synthesisInterviewRegistries(t *testing.T, components workerSynthesisInterviewComponents) (*workflowapplication.ExecutorRegistry, *workflowapplication.DefinitionRegistry) {
	t.Helper()
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorkerSynthesisInterviewExecutor(executors, components); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorkerSynthesisInterviewDefinition(definitions); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	return executors, definitions
}

type synthesisInterviewCompositionModel struct{}

func (*synthesisInterviewCompositionModel) Chat(context.Context, agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	return agentapplication.ChatResponse{}, errors.New("constructor test must not call a model")
}
