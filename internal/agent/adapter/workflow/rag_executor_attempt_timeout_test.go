package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
)

func TestRAGWorkflowExecutorV2AttemptDeadlineCancelsRunningSchedulerAndAbortsDraft(t *testing.T) {
	const modelCallTimeout = 25 * time.Millisecond
	attemptTimeout := RAGAttemptTimeout(modelCallTimeout)

	executionContext := validRAGExecutionContext(t)
	execution, _ := ragWorkflowExecution(t, executionContext)
	definition := conversationworkflow.RegisteredDefinitionV2()
	execution.DefinitionVersion = definition.Version
	execution.DefinitionHash = definition.GraphHash

	planInput, answerInput, err := buildRAGModelInputs(executionContext, emptyNonEvidenceContext())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewRuntimeCatalog(CatalogOptions{
		Model: testModelRef(), Timeout: modelCallTimeout, MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	provider := &attemptDeadlineChatModel{}
	scheduler := &attemptDeadlineScheduler{providerStartDelay: attemptTimeout - modelCallTimeout/2}
	drafts := &attemptDeadlineDraftStore{draftStreamStoreFake: newDraftStreamStoreFake()}
	retrieval := ragRetrievalFake{}
	executor, err := NewRAGWorkflowExecutor(RAGWorkflowExecutorDependencies{
		Model: provider, Scheduler: newTrackingEinoStructuredScheduler(t), RAGScheduler: scheduler,
		Catalog: catalog, Repository: &workflowRepository{}, Snapshots: &ragSnapshotRepositoryFake{},
		Memory: &ragMemoryLoaderFake{}, MemoryOwner: testRAGMemoryOwner(), Context: &ragContextLoaderFake{result: executionContext},
		Search: retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{}, Topics: ragTopicsFake{},
		Finalizer: &ragFinalizerFake{}, Progress: &ragProgressFake{},
		IDs: &workflowIDs{values: []foundation.ID{testModelRunID, testCallID}}, Clock: &workflowClock{next: time.Unix(1, 0).UTC()},
		Budget: agentapplication.DefaultRunBudget(), AgentRuntime: &ragAgentGenerationAgentFake{},
		AnswerStream: &ragAgentGenerationStreamFake{}, ToolContracts: contracts,
		ToolExecution: &ragAgentToolExecutionFake{}, DraftStreams: drafts,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := executor.prepareRAGModelRun(execution, agentdomain.RAGMemoryContextRef{})
	if err != nil {
		t.Fatal(err)
	}

	parentContext, cancelParent := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelParent()
	startedAt := time.Now()
	proposal, draft, err, unknown := executor.executeRunV2(
		parentContext, execution, run, executionContext, planInput, answerInput,
	)
	elapsed := time.Since(startedAt)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeRunV2() error = %v, want context deadline exceeded", err)
	}
	if unknown || proposal != (agentapplication.RAGTerminalProposal{}) || draft != nil {
		t.Fatalf("unknown=%t proposal=%+v draft=%+v", unknown, proposal, draft)
	}
	if !errors.Is(scheduler.contextErr, context.DeadlineExceeded) || !errors.Is(scheduler.runErr, context.DeadlineExceeded) || !scheduler.hasDeadline {
		t.Fatalf("scheduler context_err=%v run_err=%v has_deadline=%t", scheduler.contextErr, scheduler.runErr, scheduler.hasDeadline)
	}
	if scheduler.deadlineRemaining < attemptTimeout/2 || scheduler.deadlineRemaining > attemptTimeout {
		t.Fatalf("scheduler deadline remaining=%s, want within (%s,%s]", scheduler.deadlineRemaining, attemptTimeout/2, attemptTimeout)
	}
	if elapsed < attemptTimeout/2 || elapsed >= time.Second {
		t.Fatalf("executeRunV2() elapsed=%s, want Attempt deadline near %s and before parent timeout", elapsed, attemptTimeout)
	}
	if parentContext.Err() != nil {
		t.Fatalf("Attempt deadline cancelled parent context: %v", parentContext.Err())
	}
	if provider.calls != 1 || !errors.Is(provider.contextErr, context.DeadlineExceeded) || !provider.hasDeadline {
		t.Fatalf("provider calls=%d context_err=%v has_deadline=%t", provider.calls, provider.contextErr, provider.hasDeadline)
	}
	if delta := provider.deadline.Sub(scheduler.deadline); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("provider deadline=%s scheduler deadline=%s delta=%s", provider.deadline, scheduler.deadline, delta)
	}
	if drafts.abortContextErr != nil || !drafts.abortHasDeadline || drafts.abortDeadlineRemaining <= 0 || drafts.abortDeadlineRemaining > ragFinalizeTimeout {
		t.Fatalf("abort context_err=%v has_deadline=%t remaining=%s", drafts.abortContextErr, drafts.abortHasDeadline, drafts.abortDeadlineRemaining)
	}
	if drafts.status() != agentapplication.DraftStreamAborted || drafts.abortCount() != 1 {
		t.Fatalf("draft status=%s aborts=%d", drafts.status(), drafts.abortCount())
	}
}

type attemptDeadlineScheduler struct {
	providerStartDelay time.Duration
	hasDeadline        bool
	deadline           time.Time
	deadlineRemaining  time.Duration
	contextErr         error
	runErr             error
}

func (scheduler *attemptDeadlineScheduler) Schedule(ctx context.Context, run *agentapplication.RAGPhaseRun) error {
	deadline, hasDeadline := ctx.Deadline()
	scheduler.hasDeadline = hasDeadline
	if hasDeadline {
		scheduler.deadline = deadline
		scheduler.deadlineRemaining = time.Until(deadline)
	}
	time.Sleep(scheduler.providerStartDelay)
	_, scheduler.runErr = run.Plan(ctx)
	scheduler.contextErr = ctx.Err()
	return scheduler.runErr
}

type attemptDeadlineChatModel struct {
	calls       int
	hasDeadline bool
	deadline    time.Time
	contextErr  error
}

func (model *attemptDeadlineChatModel) Chat(ctx context.Context, _ agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	model.calls++
	model.deadline, model.hasDeadline = ctx.Deadline()
	<-ctx.Done()
	model.contextErr = ctx.Err()
	return agentapplication.ChatResponse{}, ctx.Err()
}

type attemptDeadlineDraftStore struct {
	*draftStreamStoreFake
	abortContextErr        error
	abortHasDeadline       bool
	abortDeadlineRemaining time.Duration
}

func (store *attemptDeadlineDraftStore) AbortDraftStream(
	ctx context.Context,
	command agentapplication.DraftStreamTransitionCommand,
) (agentapplication.DraftStreamSession, error) {
	store.abortContextErr = ctx.Err()
	deadline, hasDeadline := ctx.Deadline()
	store.abortHasDeadline = hasDeadline
	if hasDeadline {
		store.abortDeadlineRemaining = time.Until(deadline)
	}
	return store.draftStreamStoreFake.AbortDraftStream(ctx, command)
}

var _ agentapplication.RAGExecutionScheduler = (*attemptDeadlineScheduler)(nil)
var _ agentapplication.ChatModel = (*attemptDeadlineChatModel)(nil)
var _ agentapplication.DraftStreamStore = (*attemptDeadlineDraftStore)(nil)
