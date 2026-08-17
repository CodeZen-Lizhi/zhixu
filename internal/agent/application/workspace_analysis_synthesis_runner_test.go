package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisSynthesisRunnerAuthorizesOnceAndPersistsCanonicalCandidate(t *testing.T) {
	events := []string{}
	repository := &workspaceAnalysisSynthesisRepository{events: &events}
	drafts := &workspaceAnalysisSynthesisDraftCoordinator{}
	runtime := &workspaceAnalysisSynthesisRuntime{
		events: &events,
		response: ChatResponse{
			Model:   workspaceAnalysisPlanModelRef(),
			Content: workspaceAnalysisSynthesisProviderDocument(t, []string{"E1"}),
			Usage:   domain.TokenUsage{InputTokens: 41, OutputTokens: 13, TotalTokens: 54},
		},
	}
	runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, drafts)
	request := workspaceAnalysisSynthesisRequest()

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"authorize", "provider", "finalize_candidate"}) {
		t.Fatalf("events = %#v", events)
	}
	if runtime.CallCount() != 1 || repository.authorizeCalls != 1 || repository.finalizeCandidateCalls != 1 ||
		repository.finalizeCallCalls != 0 || repository.loadCandidateCalls != 0 || result.Replayed {
		t.Fatalf("calls/result = provider:%d authorize:%d candidate:%d failure:%d load:%d replayed:%t",
			runtime.CallCount(), repository.authorizeCalls, repository.finalizeCandidateCalls,
			repository.finalizeCallCalls, repository.loadCandidateCalls, result.Replayed)
	}
	call := runtime.Calls()[0]
	if call.Phase != domain.ModelCallAnswer || call.SchemaRef != (domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "1"}) ||
		call.MaxOutputTokens != int(WorkspaceAnalysisV1SynthesisMaxOutputTokens) ||
		bytes.Contains(runtime.sink.Bytes(), []byte("model_run_ref")) || !bytes.Equal(runtime.sink.Bytes(), runtime.response.Content) {
		t.Fatalf("provider call/sink = %#v %s", call, runtime.sink.Bytes())
	}
	if result.CandidateResult.ModelRunRef != result.Run.ID || result.Candidate.SynthesisModelRunID != result.Run.ID ||
		result.Candidate.AnswerID != request.AnswerID || result.Candidate.DocumentHash != result.Call.ResponseHash ||
		result.Usage != runtime.response.Usage || !bytes.Contains(result.Candidate.Document, []byte("model_run_ref")) {
		t.Fatalf("result = %#v candidate=%s", result, result.Candidate.Document)
	}
	finalized := repository.finalizeCandidateCommands[0]
	if finalized.Run.FinalResultType != domain.ResultTypeWorkspaceAnalysisAnswer ||
		finalized.Call.MaxOutputTokens != int(WorkspaceAnalysisV1SynthesisMaxOutputTokens) ||
		finalized.Candidate.DocumentHash != workspaceAnalysisSHA256(finalized.Candidate.Document) {
		t.Fatalf("finalized = %#v", finalized)
	}
}

func TestWorkspaceAnalysisSynthesisRunnerSettlesCallWhenCancellationWinsCandidatePublication(t *testing.T) {
	repository := &workspaceAnalysisSynthesisRepository{
		candidateFinalizeErr: foundation.NewError(
			foundation.ErrorVersionConflict,
			ErrorCodeWorkspaceAnalysisModelCancellationConflict,
			false,
			errors.New("cancel requested before candidate publication"),
		),
	}
	runtime := &workspaceAnalysisSynthesisRuntime{
		response: ChatResponse{
			Model:   workspaceAnalysisPlanModelRef(),
			Content: workspaceAnalysisSynthesisProviderDocument(t, []string{"E1"}),
			Usage:   domain.TokenUsage{InputTokens: 41, OutputTokens: 13, TotalTokens: 54},
		},
	}
	runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, &workspaceAnalysisSynthesisDraftCoordinator{})

	result, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
	if err == nil {
		t.Fatal("Run unexpectedly published a candidate after cancellation")
	}
	if !reflect.DeepEqual(result, WorkspaceAnalysisSynthesisResult{}) {
		t.Fatalf("Run result = %#v", result)
	}
	if runtime.CallCount() != 1 || repository.finalizeCandidateCalls != 1 || repository.finalizeCallCalls != 1 {
		t.Fatalf("calls = provider:%d candidate:%d failure:%d", runtime.CallCount(), repository.finalizeCandidateCalls, repository.finalizeCallCalls)
	}
	if repository.finalizeResultCalls != 0 || repository.persistedCandidate != nil {
		t.Fatalf("cancelled publication persisted result=%d candidate=%#v", repository.finalizeResultCalls, repository.persistedCandidate)
	}
	evidence, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err)
	if !found || evidence.CallStatus != domain.ModelCallFailed || evidence.RunStatus != domain.ModelRunFailed ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("Run error did not retain settled model terminal evidence: %v", err)
	}
	if command := repository.finalizeCallCommands[0]; command.Call.Status != domain.ModelCallFailed ||
		command.Run.Status != domain.ModelRunFailed || command.Call.ErrorCode != ErrorCodeOperationCancelled ||
		command.Call.Usage != runtime.response.Usage || command.Call.ResponseHash == "" || command.Validate() != nil {
		t.Fatalf("settlement command = %#v", command)
	}
}

func TestWorkspaceAnalysisSynthesisRunnerDoesNotSettleUnprovenCandidateCancellationConflict(t *testing.T) {
	repository := &workspaceAnalysisSynthesisRepository{
		candidateFinalizeErr: foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelCancellationConflict,
			true,
			errors.New("unproven cancellation marker"),
		),
	}
	runtime := &workspaceAnalysisSynthesisRuntime{
		response: ChatResponse{
			Model:   workspaceAnalysisPlanModelRef(),
			Content: workspaceAnalysisSynthesisProviderDocument(t, []string{"E1"}),
			Usage:   domain.TokenUsage{InputTokens: 41, OutputTokens: 13, TotalTokens: 54},
		},
	}
	runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, &workspaceAnalysisSynthesisDraftCoordinator{})

	result, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
	if err == nil {
		t.Fatal("Run accepted an unproven cancellation conflict")
	}
	if !reflect.DeepEqual(result, WorkspaceAnalysisSynthesisResult{}) || runtime.CallCount() != 1 ||
		repository.finalizeCandidateCalls != 1 || repository.finalizeCallCalls != 0 ||
		repository.finalizeResultCalls != 0 || repository.persistedCandidate != nil {
		t.Fatalf("result/calls = %#v provider:%d candidate:%d call:%d result:%d persisted:%#v",
			result, runtime.CallCount(), repository.finalizeCandidateCalls, repository.finalizeCallCalls,
			repository.finalizeResultCalls, repository.persistedCandidate)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified == nil ||
		classified.Kind != foundation.ErrorManualRecoveryRequired ||
		classified.Code != ErrorCodeModelCallPersistenceUnknown || classified.Retryable ||
		errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %#v", err)
	}
	if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
		t.Fatalf("unproven finalization exposed terminal evidence: %v", err)
	}
}

func TestWorkspaceAnalysisSynthesisRunnerReplaysCandidateWithoutProvider(t *testing.T) {
	repository := &workspaceAnalysisSynthesisRepository{}
	drafts := &workspaceAnalysisSynthesisDraftCoordinator{}
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		authorized, candidate := workspaceAnalysisSynthesisSuccessfulReplay(t, command, workspaceAnalysisSynthesisRequest().AnswerID)
		repository.loadedCandidate = candidate
		drafts.loaded = workspaceAnalysisSynthesisDraftSession(candidate.WorkspaceID, candidate.AnswerID, candidate.NodeAttemptID)
		return authorized, nil
	}
	runtime := &workspaceAnalysisSynthesisRuntime{}
	runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, drafts)

	result, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Replayed || runtime.CallCount() != 0 || repository.loadCandidateCalls != 1 ||
		repository.finalizeCandidateCalls != 0 || repository.finalizeCallCalls != 0 ||
		result.Candidate.ID != repository.loadedCandidate.ID || result.CandidateResult.ModelRunRef != result.Run.ID {
		t.Fatalf("result/calls = %#v provider:%d load:%d", result, runtime.CallCount(), repository.loadCandidateCalls)
	}
}

func TestWorkspaceAnalysisSynthesisRunnerRejectsCitationOutsideOpenedSetWithoutRetry(t *testing.T) {
	repository := &workspaceAnalysisSynthesisRepository{}
	drafts := &workspaceAnalysisSynthesisDraftCoordinator{}
	runtime := &workspaceAnalysisSynthesisRuntime{
		response: ChatResponse{
			Model:   workspaceAnalysisPlanModelRef(),
			Content: workspaceAnalysisSynthesisProviderDocument(t, []string{"E3"}),
			Usage:   domain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28},
		},
	}
	runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, drafts)
	request := workspaceAnalysisSynthesisRequest()
	request.AllowedEvidenceRefs = []string{"E1", "E2"}

	_, err := runner.Run(context.Background(), request)
	if err == nil {
		t.Fatal("Run accepted a citation outside the opened evidence set")
	}
	if runtime.CallCount() != 1 || repository.finalizeCallCalls != 1 || repository.finalizeCandidateCalls != 0 {
		t.Fatalf("calls = provider:%d failure:%d candidate:%d", runtime.CallCount(), repository.finalizeCallCalls, repository.finalizeCandidateCalls)
	}
	terminal := repository.finalizeCallCommands[0]
	if terminal.Call.Status != domain.ModelCallFailed || terminal.Run.Status != domain.ModelRunFailed || terminal.Call.Version != 2 {
		t.Fatalf("terminal = %#v", terminal)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified == nil {
		t.Fatalf("Run error = %#v", err)
	}
	assertWorkspaceAnalysisModelTerminalEvidence(t, err, terminal.OperationID, terminal.Call.Status, terminal.Run.Status, classified.Kind, classified.Code)
}

func TestWorkspaceAnalysisSynthesisRunnerNeverStreamsForNonCreatedDisposition(t *testing.T) {
	tests := []struct {
		name        string
		disposition WorkspaceAnalysisModelAuthorizationDisposition
		callStatus  domain.ModelCallStatus
		runStatus   domain.ModelRunStatus
		code        string
		kind        foundation.ErrorKind
	}{
		{name: "reconcile", disposition: WorkspaceAnalysisModelAuthorizationReconcile, callStatus: domain.ModelCallStarted, runStatus: domain.ModelRunRunning},
		{name: "failed", disposition: WorkspaceAnalysisModelAuthorizationReplayFailure, callStatus: domain.ModelCallFailed, runStatus: domain.ModelRunFailed, code: "MODEL_PROVIDER_FAILED", kind: foundation.ErrorNonRetryableFailure},
		{name: "unknown", disposition: WorkspaceAnalysisModelAuthorizationTerminateUnknown, callStatus: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown, code: "MODEL_RESULT_UNKNOWN", kind: foundation.ErrorManualRecoveryRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &workspaceAnalysisSynthesisRepository{}
			drafts := &workspaceAnalysisSynthesisDraftCoordinator{}
			persistedOperationID := workspaceAnalysisPlanTestID(902)
			repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
				result := WorkspaceAnalysisModelAuthorizationResult{
					Run: command.Run, Call: command.Call, OperationID: persistedOperationID,
					ReservationID: command.ReservationID, Disposition: test.disposition,
				}
				if test.callStatus != domain.ModelCallStarted {
					completedAt := command.Call.StartedAt.Add(time.Second)
					result.Call.Status, result.Call.ErrorCode, result.Call.Version = test.callStatus, test.code, 2
					result.Call.CompletedAt, result.Call.LatencyMillis = &completedAt, 1000
					result.Run.Status, result.Run.FinalErrorCode, result.Run.Version = test.runStatus, test.code, 2
					result.Run.UpdatedAt, result.Run.CompletedAt = completedAt, &completedAt
				}
				return result, nil
			}
			runtime := &workspaceAnalysisSynthesisRuntime{}
			runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, drafts)
			_, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
			if err == nil {
				t.Fatal("Run accepted a non-created disposition")
			}
			if test.disposition == WorkspaceAnalysisModelAuthorizationReconcile {
				if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
					t.Fatal("reconciliation unexpectedly exposed terminal evidence")
				}
			} else {
				assertWorkspaceAnalysisModelTerminalEvidence(t, err, persistedOperationID, test.callStatus, test.runStatus, test.kind, test.code)
			}
			if runtime.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeCandidateCalls != 0 {
				t.Fatalf("calls = provider:%d failure:%d candidate:%d", runtime.CallCount(), repository.finalizeCallCalls, repository.finalizeCandidateCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisSynthesisDraftFinalizationIgnoresCancellationButKeepsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	draft := &workspaceAnalysisSynthesisDraft{}

	if err := abortWorkspaceAnalysisSynthesisDraft(ctx, draft); err != nil {
		t.Fatalf("abort draft: %v", err)
	}
	if _, err := completeWorkspaceAnalysisSynthesisDraft(ctx, draft); err != nil {
		t.Fatalf("complete draft: %v", err)
	}
	for name, observation := range map[string]workspaceAnalysisSynthesisDraftContextObservation{
		"abort": draft.abortContext, "complete": draft.completeContext,
	} {
		if !observation.active || !observation.hasDeadline || observation.remaining <= 0 ||
			observation.remaining > workspaceAnalysisPlanFinalizationTimeout {
			t.Fatalf("%s context = %#v", name, observation)
		}
	}
}

type workspaceAnalysisSynthesisRuntime struct {
	mu       sync.Mutex
	response ChatResponse
	err      error
	calls    []ChatRequest
	sink     workspaceAnalysisSynthesisSink
	events   *[]string
}

func (runtime *workspaceAnalysisSynthesisRuntime) Stream(
	ctx context.Context,
	request ChatRequest,
	sink WorkspaceAnalysisCandidateStreamSink,
) (ChatResponse, error) {
	runtime.mu.Lock()
	runtime.calls = append(runtime.calls, cloneChatRequest(request))
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "provider")
	}
	response, err := cloneChatResponse(runtime.response), runtime.err
	runtime.mu.Unlock()
	if err == nil && len(response.Content) > 0 {
		if appendErr := sink.Append(ctx, WorkspaceAnalysisCandidateStreamChunk{Content: string(response.Content)}); appendErr != nil {
			return ChatResponse{}, appendErr
		}
		runtime.mu.Lock()
		_, _ = runtime.sink.Write(response.Content)
		runtime.mu.Unlock()
	}
	return response, err
}

func (runtime *workspaceAnalysisSynthesisRuntime) CallCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.calls)
}

func (runtime *workspaceAnalysisSynthesisRuntime) Calls() []ChatRequest {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	result := make([]ChatRequest, len(runtime.calls))
	for index := range runtime.calls {
		result[index] = cloneChatRequest(runtime.calls[index])
	}
	return result
}

type workspaceAnalysisSynthesisSink struct{ bytes.Buffer }

func (sink *workspaceAnalysisSynthesisSink) Append(_ context.Context, chunk WorkspaceAnalysisCandidateStreamChunk) error {
	_, err := sink.WriteString(chunk.Content)
	return err
}

type workspaceAnalysisSynthesisDraft struct {
	session         DraftStreamSession
	sink            workspaceAnalysisSynthesisSink
	completeContext workspaceAnalysisSynthesisDraftContextObservation
	abortContext    workspaceAnalysisSynthesisDraftContextObservation
}

type workspaceAnalysisSynthesisDraftContextObservation struct {
	active      bool
	hasDeadline bool
	remaining   time.Duration
}

func observeWorkspaceAnalysisSynthesisDraftContext(ctx context.Context) workspaceAnalysisSynthesisDraftContextObservation {
	deadline, hasDeadline := ctx.Deadline()
	return workspaceAnalysisSynthesisDraftContextObservation{
		active: ctx.Err() == nil, hasDeadline: hasDeadline, remaining: time.Until(deadline),
	}
}

func (draft *workspaceAnalysisSynthesisDraft) Append(ctx context.Context, chunk WorkspaceAnalysisCandidateStreamChunk) error {
	return draft.sink.Append(ctx, chunk)
}

func (draft *workspaceAnalysisSynthesisDraft) Session() DraftStreamSession { return draft.session }

func (draft *workspaceAnalysisSynthesisDraft) Complete(ctx context.Context) (DraftStreamSession, error) {
	draft.completeContext = observeWorkspaceAnalysisSynthesisDraftContext(ctx)
	draft.session.Status = DraftStreamCompleted
	return draft.session, nil
}

func (draft *workspaceAnalysisSynthesisDraft) Abort(ctx context.Context) error {
	draft.abortContext = observeWorkspaceAnalysisSynthesisDraftContext(ctx)
	draft.session.Status = DraftStreamAborted
	return nil
}

type workspaceAnalysisSynthesisDraftCoordinator struct {
	beginCalls int
	loadCalls  int
	loaded     DraftStreamSession
}

func (coordinator *workspaceAnalysisSynthesisDraftCoordinator) Begin(
	_ context.Context,
	binding DraftStreamBinding,
) (WorkspaceAnalysisCandidateDraft, error) {
	coordinator.beginCalls++
	return &workspaceAnalysisSynthesisDraft{session: DraftStreamSession{
		ID: workspaceAnalysisPlanTestID(950), Binding: binding, Generation: 1, Status: DraftStreamActive,
	}}, nil
}

func (coordinator *workspaceAnalysisSynthesisDraftCoordinator) Load(
	context.Context,
	WorkspaceAnalysisCandidateDraftQuery,
) (DraftStreamSession, error) {
	coordinator.loadCalls++
	return coordinator.loaded, nil
}

type workspaceAnalysisSynthesisRepository struct {
	authorize            func(AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error)
	events               *[]string
	candidateFinalizeErr error

	authorizeCalls            int
	finalizeCallCalls         int
	finalizeResultCalls       int
	finalizeCandidateCalls    int
	loadCandidateCalls        int
	authorizedCommands        []AuthorizeWorkspaceAnalysisModelCallCommand
	finalizeCallCommands      []FinalizeWorkspaceAnalysisModelCallCommand
	finalizeCandidateCommands []FinalizeWorkspaceAnalysisModelCandidateCommand
	loadedCandidate           domain.WorkspaceAnalysisCandidate
	persistedCandidate        *domain.WorkspaceAnalysisCandidate
}

func (repository *workspaceAnalysisSynthesisRepository) AuthorizeWorkspaceAnalysisModelCall(
	_ context.Context,
	command AuthorizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelAuthorizationResult, error) {
	repository.authorizeCalls++
	repository.authorizedCommands = append(repository.authorizedCommands, command)
	repository.record("authorize")
	if repository.authorize != nil {
		return repository.authorize(command)
	}
	return WorkspaceAnalysisModelAuthorizationResult{
		Run: command.Run, Call: command.Call, OperationID: command.OperationID,
		ReservationID: command.ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationCreated,
	}, nil
}

func (repository *workspaceAnalysisSynthesisRepository) FinalizeWorkspaceAnalysisModelCall(
	_ context.Context,
	command FinalizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeCallCalls++
	repository.finalizeCallCommands = append(repository.finalizeCallCommands, command)
	repository.record("finalize_call")
	return WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call,
		OperationID: command.OperationID, ReservationID: command.ReservationID,
	}, nil
}

func (repository *workspaceAnalysisSynthesisRepository) FinalizeWorkspaceAnalysisModelResult(
	context.Context,
	FinalizeWorkspaceAnalysisModelResultCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeResultCalls++
	return WorkspaceAnalysisModelMutationResult{}, errors.New("unexpected model result finalization")
}

func (repository *workspaceAnalysisSynthesisRepository) FinalizeWorkspaceAnalysisModelCandidate(
	_ context.Context,
	command FinalizeWorkspaceAnalysisModelCandidateCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeCandidateCalls++
	repository.finalizeCandidateCommands = append(repository.finalizeCandidateCommands, command)
	repository.record("finalize_candidate")
	if repository.candidateFinalizeErr != nil {
		return WorkspaceAnalysisModelMutationResult{}, repository.candidateFinalizeErr
	}
	candidate := command.Candidate
	repository.persistedCandidate = &candidate
	return WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, Candidate: &candidate,
		OperationID: command.OperationID, ReservationID: command.ReservationID,
	}, nil
}

func (repository *workspaceAnalysisSynthesisRepository) LoadWorkspaceAnalysisModelResult(
	context.Context,
	WorkspaceAnalysisModelResultQuery,
) (domain.WorkspaceAnalysisModelResult, error) {
	return domain.WorkspaceAnalysisModelResult{}, errors.New("unexpected model result load")
}

func (repository *workspaceAnalysisSynthesisRepository) LoadWorkspaceAnalysisCandidate(
	_ context.Context,
	_ WorkspaceAnalysisCandidateQuery,
) (domain.WorkspaceAnalysisCandidate, error) {
	repository.loadCandidateCalls++
	repository.record("load_candidate")
	candidate := repository.loadedCandidate
	candidate.Document = append(json.RawMessage(nil), repository.loadedCandidate.Document...)
	return candidate, nil
}

func (repository *workspaceAnalysisSynthesisRepository) record(event string) {
	if repository.events != nil {
		*repository.events = append(*repository.events, event)
	}
}

func newWorkspaceAnalysisSynthesisRunner(
	t *testing.T,
	runtime WorkspaceAnalysisCandidateStreamRuntime,
	repository WorkspaceAnalysisModelOperationRepository,
	drafts WorkspaceAnalysisCandidateDraftCoordinator,
) *WorkspaceAnalysisSynthesisRunner {
	t.Helper()
	runner, err := NewWorkspaceAnalysisSynthesisRunner(WorkspaceAnalysisSynthesisRunnerDependencies{
		Runtime: runtime, Catalog: workspaceAnalysisSynthesisCatalog(t), Repository: repository, Drafts: drafts,
		IDs: &workspaceAnalysisPlanIDGenerator{}, Clock: foundation.FixedClock{Value: workspaceAnalysisPlanNow()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func workspaceAnalysisSynthesisCatalog(t *testing.T) *RuntimeCatalog {
	t.Helper()
	prompt := PromptDefinition{
		Ref:    domain.PromptRef{ID: "workspace-analysis-synthesis", Version: "v1"},
		System: "produce a bounded identityless candidate", InitialInstruction: "return one strict candidate",
		RepairInstruction: "unused", ReducedInstruction: "unused",
	}
	schema := SchemaDefinition{
		Ref:        domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		JSONSchema: []byte(`{"type":"object","additionalProperties":false,"required":["result_type","schema_id","schema_version","payload"]}`),
		Decode: func(raw []byte) (json.RawMessage, error) {
			if _, err := domain.DecodeWorkspaceAnalysisCandidateProvider(raw, domain.DefaultDecodeLimits()); err != nil {
				return nil, err
			}
			return append(json.RawMessage(nil), raw...), nil
		},
	}
	profile := ModelProfile{
		Ref:   domain.ModelProfileRef{ID: "workspace-analysis-synthesis", Version: "v1"},
		Model: workspaceAnalysisPlanModelRef(), Timeout: time.Minute, MaxOutputTokens: 8192,
	}
	catalog := NewRuntimeCatalog()
	for _, register := range []func() error{
		func() error { return catalog.RegisterPrompt(prompt) },
		func() error { return catalog.RegisterSchema(schema) },
		func() error { return catalog.RegisterProfile(profile) },
		catalog.Freeze,
	} {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}
	return catalog
}

func workspaceAnalysisSynthesisRequest() WorkspaceAnalysisSynthesisRequest {
	return WorkspaceAnalysisSynthesisRequest{
		Identity: WorkspaceAnalysisModelExecutionIdentity{
			WorkspaceID: workspaceAnalysisPlanTestID(1), DefinitionID: workspaceAnalysisPlanTestID(2),
			DefinitionVersion: 1, DefinitionHash: hashHex('a'), WorkflowRunID: workspaceAnalysisPlanTestID(3),
			NodeKey: domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer, NodeRunID: workspaceAnalysisPlanTestID(4),
			NodeAttemptID: workspaceAnalysisPlanTestID(5), LeaseOwner: "worker-a", LeaseFence: 1,
		},
		AnalysisRunID: workspaceAnalysisPlanTestID(6), AnswerID: workspaceAnalysisPlanTestID(8), AttemptNo: 1,
		Retrieval:           domain.RetrievalRef{IndexVersionID: workspaceAnalysisPlanTestID(7)},
		ProfileRef:          domain.ModelProfileRef{ID: "workspace-analysis-synthesis", Version: "v1"},
		PromptRef:           domain.PromptRef{ID: "workspace-analysis-synthesis", Version: "v1"},
		Input:               []byte(`{"question":"what does the policy say?","git_status":{"clean":true},"evidence":[{"evidence_ref":"E1","excerpt":"bounded policy"}]}`),
		AllowedEvidenceRefs: []string{"E1"},
	}
}

func workspaceAnalysisSynthesisDraftSession(
	workspaceID foundation.ID,
	answerID foundation.ID,
	attemptID foundation.ID,
) DraftStreamSession {
	return DraftStreamSession{
		ID: workspaceAnalysisPlanTestID(950),
		Binding: DraftStreamBinding{
			WorkspaceID: workspaceID, AnswerID: answerID, WorkflowRunID: workspaceAnalysisPlanTestID(3),
			NodeRunID: workspaceAnalysisPlanTestID(4), NodeAttemptID: attemptID, AttemptNo: 1, LeaseOwner: "worker-a",
		},
		Generation: 1, Status: DraftStreamCompleted,
	}
}

func workspaceAnalysisSynthesisProviderDocument(t *testing.T, refs []string) []byte {
	t.Helper()
	document, err := json.Marshal(domain.WorkspaceAnalysisCandidateProviderResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisCandidate,
		SchemaID:   domain.WorkspaceAnalysisCandidateSchemaID, SchemaVersion: "1",
		Payload: domain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: "Grounded answer.", CitationRefs: refs, ProposalSuggestion: nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func workspaceAnalysisSynthesisSuccessfulReplay(
	t *testing.T,
	command AuthorizeWorkspaceAnalysisModelCallCommand,
	answerID foundation.ID,
) (WorkspaceAnalysisModelAuthorizationResult, domain.WorkspaceAnalysisCandidate) {
	t.Helper()
	provider, err := domain.DecodeWorkspaceAnalysisCandidateProvider(
		workspaceAnalysisSynthesisProviderDocument(t, []string{"E1"}), domain.DefaultDecodeLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Compose(command.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := command.Call.StartedAt.Add(time.Second)
	call := command.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(document), int64(len(document))
	call.Usage, call.LatencyMillis, call.Version, call.CompletedAt = domain.TokenUsage{InputTokens: 19, OutputTokens: 6, TotalTokens: 25}, 1000, 2, &completedAt
	run := command.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisAnswer, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	candidate := domain.WorkspaceAnalysisCandidate{
		ID: workspaceAnalysisPlanTestID(900), WorkspaceID: command.Identity.WorkspaceID,
		AnalysisRunID: command.OperationKey.AnalysisRunID, AnswerID: answerID,
		SynthesisOperationID: command.OperationID, NodeAttemptID: command.Run.NodeAttemptID,
		SynthesisModelRunID: command.Run.ID, SchemaID: command.Run.Schema.ID, SchemaVersion: 1,
		Document: document, DocumentHash: call.ResponseHash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	return WorkspaceAnalysisModelAuthorizationResult{
		Run: run, Call: call, OperationID: command.OperationID, ReservationID: command.ReservationID,
		Disposition: WorkspaceAnalysisModelAuthorizationReuseResult,
	}, candidate
}
