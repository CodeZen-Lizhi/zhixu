package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

var workflowTestNow = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

func TestCaptureWorkflowContractStrictInputAndOutput(t *testing.T) {
	input, err := DecodeInput(json.RawMessage(`{"schema_version":1,"workspace_id":"90000000-0000-4000-8000-000000000001","capture_id":"90000000-0000-4000-8000-000000000002"}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.WorkspaceID != workflowTestID(1) || input.CaptureID != workflowTestID(2) {
		t.Fatalf("input = %#v", input)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"schema_version":1,"workspace_id":"90000000-0000-4000-8000-000000000001","capture_id":"90000000-0000-4000-8000-000000000002","extra":true}`),
		json.RawMessage(`{"schema_version":1,"workspace_id":"not-a-uuid","capture_id":"90000000-0000-4000-8000-000000000002"}`),
		json.RawMessage(`{"schema_version":1,"workspace_id":"90000000-0000-4000-8000-000000000001","capture_id":"90000000-0000-4000-8000-000000000002"} {}`),
		json.RawMessage(`{"schema_version":1,"workspace_id":"90000000-0000-4000-8000-000000000001","capture_id":"90000000-0000-4000-8000-000000000002"} trailing`),
	} {
		if _, err := DecodeInput(raw); workflowErrorCode(err) != "CAPTURE_WORKFLOW_INPUT_INVALID" {
			t.Fatalf("DecodeInput(%s) error = %#v", raw, err)
		}
	}
	definition, err := RegisteredDefinition()
	if err != nil {
		t.Fatal(err)
	}
	permissions := definition.Graph.Nodes[0].RequiredPermissions
	if len(permissions) != 2 || permissions[0] != workflowdomain.PermissionReadLocal || permissions[1] != workflowdomain.PermissionWriteProposal {
		t.Fatalf("required permissions = %#v", permissions)
	}

	encoded, err := EncodeOutput(OutputReceipt{
		SchemaVersion: captureapp.ProcessingOutputSchemaVersion, WorkspaceID: workflowTestID(1), CaptureID: workflowTestID(2),
		Status: domain.StatusReady, ProfileStatus: domain.StageReady, ProfileID: workflowTestID(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt OutputReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt != (OutputReceipt{SchemaVersion: 1, WorkspaceID: workflowTestID(1), CaptureID: workflowTestID(2), Status: domain.StatusReady, ProfileStatus: domain.StageReady, ProfileID: workflowTestID(3)}) {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, err := EncodeOutput(OutputReceipt{SchemaVersion: 1, WorkspaceID: workflowTestID(1), CaptureID: workflowTestID(2), Status: domain.StatusReady}); workflowErrorCode(err) != "CAPTURE_WORKFLOW_OUTPUT_INVALID" {
		t.Fatalf("EncodeOutput() error = %#v", err)
	}
}

func TestExecutorURLFetchRefreshAndProfileReady(t *testing.T) {
	capture := workflowCapture(domain.KindURL)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageFetch))
	fetcher := &workflowFetcher{result: captureapp.URLFetchResult{Content: []byte("<h1>Java AI</h1>"), MediaType: "text/html", FinalURL: "https://example.com/java-ai"}}
	content := &workflowContent{}
	refresher := &workflowRefresher{result: workflowRefreshResult()}
	profiles := &workflowProfiles{revisionID: workflowTestID(31)}
	executor := newWorkflowExecutor(t, capture, runtime, content, fetcher, refresher, profiles, workflowTestIDs(20, 21, 22, 23))

	execution := workflowExecution(capture.WorkspaceID, capture.ID)
	execution.AttemptNo = 3
	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	receipt := decodeWorkflowResult(t, result)
	if receipt.Status != domain.StatusReady || receipt.ProfileStatus != domain.StageReady || receipt.ProfileID != workflowTestID(23) {
		t.Fatalf("receipt = %#v", receipt)
	}
	if fetcher.calls != 1 || content.calls != 1 || content.publishCalls != 1 || refresher.calls != 1 || profiles.calls != 1 || runtime.completeCalls != 1 {
		t.Fatalf("fetch=%d stage=%d publish=%d refresh=%d profile=%d complete=%d", fetcher.calls, content.calls, content.publishCalls, refresher.calls, profiles.calls, runtime.completeCalls)
	}
	if !strings.HasSuffix(content.sourceRef, "/"+string(workflowTestID(22))) {
		t.Fatalf("stage source ref %q is not bound to proposed source version", content.sourceRef)
	}
	if runtime.materialized.MediaType != "text/html" || runtime.materialized.OriginalContentLocation != fetcher.result.FinalURL || runtime.materialized.SourceVersionID != workflowTestID(22) {
		t.Fatalf("materialized = %#v", runtime.materialized)
	}
	if refresher.request.RequestID != workflowTestID(11) || refresher.request.AttemptNumber != 1 ||
		refresher.request.SourceVersionID != workflowTestID(22) || profiles.request.ParseProjectionID != workflowTestID(41) || profiles.request.IndexVersionID != workflowTestID(42) {
		t.Fatalf("refresh=%#v profile=%#v", refresher.request, profiles.request)
	}
}

func TestExecutorURLMaterializationUnknownCommitPublishesWithoutMarkingFetchFailed(t *testing.T) {
	capture := workflowCapture(domain.KindURL)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageFetch))
	runtime.materializeErr = foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_URL_MATERIALIZATION_FAILED", true, errors.New("commit response lost"))
	runtime.commitOnMaterializeError = true
	content := &workflowContent{}
	executor := newWorkflowExecutor(t, capture, runtime, content,
		&workflowFetcher{result: captureapp.URLFetchResult{Content: []byte("<p>committed</p>"), MediaType: "text/html", FinalURL: "https://example.com/final"}},
		&workflowRefresher{}, nil, workflowTestIDs(20, 21, 22))

	_, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if workflowErrorCode(err) != errorCodeContentUnknown {
		t.Fatalf("Execute() error=%#v", err)
	}
	if runtime.failCalls != 0 || content.calls != 1 || content.publishCalls != 1 || content.discardCalls != 0 {
		t.Fatalf("fail=%d stage=%d publish=%d discard=%d", runtime.failCalls, content.calls, content.publishCalls, content.discardCalls)
	}
}

func TestExecutorURLMaterializationUnknownWithoutVisibleVersionKeepsStage(t *testing.T) {
	capture := workflowCapture(domain.KindURL)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageFetch))
	runtime.materializeErr = foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_URL_MATERIALIZATION_FAILED", true, errors.New("commit outcome unknown"))
	content := &workflowContent{}
	executor := newWorkflowExecutor(t, capture, runtime, content,
		&workflowFetcher{result: captureapp.URLFetchResult{Content: []byte("<p>unknown</p>"), MediaType: "text/html", FinalURL: "https://example.com/final"}},
		&workflowRefresher{}, nil, workflowTestIDs(20, 21, 22))

	_, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if workflowErrorCode(err) != errorCodeContentUnknown {
		t.Fatalf("Execute() error=%#v", err)
	}
	if runtime.failCalls != 0 || content.calls != 1 || content.publishCalls != 0 || content.discardCalls != 0 {
		t.Fatalf("fail=%d stage=%d publish=%d discard=%d", runtime.failCalls, content.calls, content.publishCalls, content.discardCalls)
	}
}

func TestExecutorURLMaterializationRollbackDiscardsItsUniqueStage(t *testing.T) {
	capture := workflowCapture(domain.KindURL)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageFetch))
	runtime.materializeErr = foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_URL_SOURCE_BINDING_INVALID", false, errors.New("rolled back before commit"))
	content := &workflowContent{}
	executor := newWorkflowExecutor(t, capture, runtime, content,
		&workflowFetcher{result: captureapp.URLFetchResult{Content: []byte("<p>rollback</p>"), MediaType: "text/html", FinalURL: "https://example.com/final"}},
		&workflowRefresher{}, nil, workflowTestIDs(20, 21, 22))

	_, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if workflowErrorCode(err) != "CAPTURE_URL_SOURCE_BINDING_INVALID" {
		t.Fatalf("Execute() error=%#v", err)
	}
	if runtime.failCalls != 1 || content.calls != 1 || content.publishCalls != 0 || content.discardCalls != 1 {
		t.Fatalf("fail=%d stage=%d publish=%d discard=%d", runtime.failCalls, content.calls, content.publishCalls, content.discardCalls)
	}
}

func TestExecutorFetchFailurePersistsFetchStage(t *testing.T) {
	capture := workflowCapture(domain.KindURL)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageFetch))
	fetcher := &workflowFetcher{err: foundation.NewError(foundation.ErrorPermissionDenied, "CAPTURE_FETCH_DENIED", false, errors.New("blocked"))}
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, fetcher, &workflowRefresher{}, nil, workflowTestIDs(20))

	_, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if workflowErrorCode(err) != "CAPTURE_FETCH_DENIED" {
		t.Fatalf("Execute() error = %#v", err)
	}
	if runtime.failCalls != 1 || runtime.failure.Stage != domain.AttemptStageFetch || runtime.failure.Code != "CAPTURE_FETCH_DENIED" || runtime.failure.Retryable {
		t.Fatalf("failure = %#v calls=%d", runtime.failure, runtime.failCalls)
	}
	if !runtime.terminalContextHasDeadline || runtime.terminalContextCanceled {
		t.Fatalf("terminal context deadline=%t canceled=%t", runtime.terminalContextHasDeadline, runtime.terminalContextCanceled)
	}
}

func TestExecutorTerminalPersistenceSurvivesCallerCancellation(t *testing.T) {
	capture := workflowCapture(domain.KindURL)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageFetch))
	fetcher := &workflowFetcher{err: foundation.NewError(foundation.ErrorRetryableFailure, "CAPTURE_FETCH_FAILED", true, errors.New("network"))}
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, fetcher, &workflowRefresher{}, nil, workflowTestIDs(20))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = executor.Execute(ctx, workflowExecution(capture.WorkspaceID, capture.ID))

	if runtime.failCalls != 1 || !runtime.terminalContextHasDeadline || runtime.terminalContextCanceled {
		t.Fatalf("fail=%d deadline=%t canceled=%t", runtime.failCalls, runtime.terminalContextHasDeadline, runtime.terminalContextCanceled)
	}
}

func TestExecutorImageCompletesWithExplicitOCRUnavailable(t *testing.T) {
	capture := workflowCapture(domain.KindImage)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageIngestion))
	fetcher := &workflowFetcher{}
	refresher := &workflowRefresher{}
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, fetcher, refresher, nil, workflowTestIDs(20, 21))

	result, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if err != nil {
		t.Fatal(err)
	}
	receipt := decodeWorkflowResult(t, result)
	if receipt.Status != domain.StatusReadyDegraded || receipt.ProfileStatus != domain.StageCapabilityUnavailable || runtime.degraded.Code != errorCodeOCRUnavailable {
		t.Fatalf("receipt=%#v degraded=%#v", receipt, runtime.degraded)
	}
	if fetcher.calls != 0 || refresher.calls != 0 || runtime.degraded.IngestionStatus != domain.StageCapabilityUnavailable || runtime.degraded.IndexStatus != domain.StageCapabilityUnavailable {
		t.Fatalf("fetch=%d refresh=%d degraded=%#v", fetcher.calls, refresher.calls, runtime.degraded)
	}
}

func TestExecutorRefreshReadyWithoutModelKeepsRetrievalAndDegradesProfile(t *testing.T) {
	capture := workflowCapture(domain.KindText)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageIngestion))
	refresher := &workflowRefresher{result: workflowRefreshResult()}
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, &workflowFetcher{}, refresher, nil, workflowTestIDs(20, 21))

	result, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if err != nil {
		t.Fatal(err)
	}
	receipt := decodeWorkflowResult(t, result)
	if receipt.Status != domain.StatusReadyDegraded || receipt.ProfileStatus != domain.StageCapabilityUnavailable || runtime.degraded.Code != errorCodeProfileUnavailable {
		t.Fatalf("receipt=%#v degraded=%#v", receipt, runtime.degraded)
	}
	if refresher.calls != 1 || runtime.refreshReadyCalls != 1 || runtime.degraded.IngestionStatus != domain.StageReady || runtime.degraded.IndexStatus != domain.StageReady {
		t.Fatalf("refresh=%d ready=%d degraded=%#v", refresher.calls, runtime.refreshReadyCalls, runtime.degraded)
	}
}

func TestExecutorProfileFailureDoesNotBlockReadyIndex(t *testing.T) {
	capture := workflowCapture(domain.KindText)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageIngestion))
	profiles := &workflowProfiles{err: foundation.NewError(foundation.ErrorDependencyUnavailable, "MODEL_TEMPORARILY_UNAVAILABLE", true, errors.New("model unavailable"))}
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, &workflowFetcher{}, &workflowRefresher{result: workflowRefreshResult()}, profiles, workflowTestIDs(20, 21))

	result, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if err != nil {
		t.Fatalf("profile failure must retain retrieval result: %v", err)
	}
	receipt := decodeWorkflowResult(t, result)
	if receipt.Status != domain.StatusReadyDegraded || receipt.ProfileStatus != domain.StageFailed || runtime.degraded.Code != "MODEL_TEMPORARILY_UNAVAILABLE" || !runtime.degraded.Retryable {
		t.Fatalf("receipt=%#v degraded=%#v", receipt, runtime.degraded)
	}
	if runtime.failCalls != 0 || runtime.refreshReadyCalls != 1 {
		t.Fatalf("fail=%d refresh-ready=%d", runtime.failCalls, runtime.refreshReadyCalls)
	}
}

func TestExecutorProfileCapabilityUnavailableKeepsProfileStateConsistent(t *testing.T) {
	capture := workflowCapture(domain.KindText)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageIngestion))
	profiles := &workflowProfiles{err: foundation.NewError(
		foundation.ErrorDependencyUnavailable, errorCodeProfileUnavailable, true, errors.New("profile model disabled"),
	)}
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, &workflowFetcher{},
		&workflowRefresher{result: workflowRefreshResult()}, profiles, workflowTestIDs(20, 21))

	result, err := executor.Execute(context.Background(), workflowExecution(capture.WorkspaceID, capture.ID))
	if err != nil {
		t.Fatalf("capability unavailable must retain retrieval result: %v", err)
	}
	receipt := decodeWorkflowResult(t, result)
	if receipt.Status != domain.StatusReadyDegraded || receipt.ProfileStatus != domain.StageCapabilityUnavailable ||
		runtime.degraded.ProfileStatus != domain.ProfileStatusCapabilityUnavailable ||
		runtime.degraded.Code != errorCodeProfileUnavailable || runtime.degraded.Retryable {
		t.Fatalf("receipt=%#v degraded=%#v", receipt, runtime.degraded)
	}
	if runtime.failCalls != 0 || runtime.refreshReadyCalls != 1 || runtime.degradedCalls != 1 {
		t.Fatalf("fail=%d refresh-ready=%d degraded=%d", runtime.failCalls, runtime.refreshReadyCalls, runtime.degradedCalls)
	}
}

func TestExecutorRejectsWorkspaceMismatchAndReplaysSucceededAttempt(t *testing.T) {
	capture := workflowCapture(domain.KindText)
	runtime := newWorkflowRuntime(capture, workflowAttempt(capture, domain.AttemptStageIngestion))
	executor := newWorkflowExecutor(t, capture, runtime, &workflowContent{}, &workflowFetcher{}, &workflowRefresher{}, nil, workflowTestIDs(20))

	wrongWorkspace := workflowExecution(workflowTestID(99), capture.ID)
	wrongWorkspace.Input = mustWorkflowJSON(t, captureapp.ProcessingInput{SchemaVersion: 1, WorkspaceID: capture.WorkspaceID, CaptureID: capture.ID})
	if _, err := executor.Execute(context.Background(), wrongWorkspace); workflowErrorCode(err) != "CAPTURE_WORKFLOW_INPUT_INVALID" || runtime.beginCalls != 0 {
		t.Fatalf("workspace binding error = %#v, attempts = %d", err, runtime.beginCalls)
	}

	replayedCapture := capture
	replayedCapture.Status = domain.StatusReady
	replayedCapture.ProfileStatus = domain.StageReady
	replayed := workflowAttempt(replayedCapture, domain.AttemptStageComplete)
	replayed.Status = domain.AttemptStatusSucceeded
	completed := workflowTestNow
	replayed.CompletedAt = &completed
	replayed.ProfileID = workflowTestID(61)
	replayRuntime := newWorkflowRuntime(replayedCapture, replayed)
	replayExecutor := newWorkflowExecutor(t, replayedCapture, replayRuntime, &workflowContent{}, &workflowFetcher{}, &workflowRefresher{}, nil, workflowTestIDs(20))
	result, err := replayExecutor.Execute(context.Background(), workflowExecution(replayedCapture.WorkspaceID, replayedCapture.ID))
	if err != nil {
		t.Fatal(err)
	}
	receipt := decodeWorkflowResult(t, result)
	if receipt.ProfileID != replayed.ProfileID || replayRuntime.refreshRunningCalls != 0 || replayRuntime.completeCalls != 0 || replayRuntime.degradedCalls != 0 {
		t.Fatalf("receipt=%#v runtime=%#v", receipt, replayRuntime)
	}
}

type workflowCaptureRepository struct {
	capture  domain.Capture
	getCalls int
}

func (*workflowCaptureRepository) ReplayCreate(context.Context, captureapp.CommandBinding) (captureapp.CreateResult, bool, error) {
	return captureapp.CreateResult{}, false, errors.New("not implemented")
}

func (*workflowCaptureRepository) Create(context.Context, captureapp.CreateRecord) (captureapp.CreateResult, error) {
	return captureapp.CreateResult{}, errors.New("not implemented")
}

func (repository *workflowCaptureRepository) Get(_ context.Context, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	repository.getCalls++
	if workspaceID != repository.capture.WorkspaceID || captureID != repository.capture.ID {
		return domain.Capture{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_NOT_FOUND", false, errors.New("missing"))
	}
	return repository.capture, nil
}

func (*workflowCaptureRepository) List(context.Context, captureapp.ListQuery) (captureapp.Page, error) {
	return captureapp.Page{}, errors.New("not implemented")
}

type workflowRuntime struct {
	capture domain.Capture
	attempt domain.ProcessingAttempt

	beginCalls, refreshRunningCalls, refreshReadyCalls  int
	completeCalls, degradedCalls, failCalls             int
	materialized                                        captureapp.MaterializeURLRequest
	degraded                                            captureapp.DegradedCompletion
	failure                                             captureapp.AttemptFailure
	terminalContextHasDeadline, terminalContextCanceled bool
	materializeErr                                      error
	commitOnMaterializeError                            bool
	repository                                          *workflowCaptureRepository
}

func newWorkflowRuntime(capture domain.Capture, attempt domain.ProcessingAttempt) *workflowRuntime {
	return &workflowRuntime{capture: capture, attempt: attempt}
}

func (runtime *workflowRuntime) BeginAttempt(_ context.Context, request captureapp.BeginAttemptRequest) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.beginCalls++
	if request.WorkspaceID != runtime.capture.WorkspaceID || request.CaptureID != runtime.capture.ID {
		return domain.Capture{}, domain.ProcessingAttempt{}, errors.New("unexpected capture binding")
	}
	return runtime.capture, runtime.attempt, nil
}

func (runtime *workflowRuntime) MaterializeURL(_ context.Context, request captureapp.MaterializeURLRequest) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.materialized = request
	if runtime.materializeErr != nil && !runtime.commitOnMaterializeError {
		return domain.Capture{}, domain.ProcessingAttempt{}, runtime.materializeErr
	}
	runtime.capture.LatestSourceVersionID = request.SourceVersionID
	runtime.capture.OriginalInputHash = request.ContentHash
	runtime.capture.Version++
	runtime.attempt.SourceVersionID = request.SourceVersionID
	runtime.attempt.Stage = domain.AttemptStageIngestion
	runtime.attempt.Version++
	if runtime.repository != nil {
		runtime.repository.capture = runtime.capture
	}
	if runtime.materializeErr != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, runtime.materializeErr
	}
	return runtime.capture, runtime.attempt, nil
}

func (runtime *workflowRuntime) MarkRefreshRunning(_ context.Context, attempt domain.ProcessingAttempt, expectedCaptureVersion int64, _ time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.refreshRunningCalls++
	if expectedCaptureVersion != runtime.capture.Version || attempt.ID != runtime.attempt.ID {
		return domain.Capture{}, domain.ProcessingAttempt{}, errors.New("refresh running version drift")
	}
	runtime.capture.Version++
	runtime.attempt.Stage = domain.AttemptStageIngestion
	runtime.attempt.Version++
	return runtime.capture, runtime.attempt, nil
}

func (runtime *workflowRuntime) MarkRefreshReady(_ context.Context, request captureapp.RefreshCheckpoint) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.refreshReadyCalls++
	if request.ExpectedCaptureVersion != runtime.capture.Version || request.Attempt.ID != runtime.attempt.ID {
		return domain.Capture{}, domain.ProcessingAttempt{}, errors.New("refresh ready version drift")
	}
	runtime.capture.Version++
	runtime.capture.IngestionStatus = domain.StageReady
	runtime.capture.IndexStatus = domain.StageReady
	runtime.capture.ProfileStatus = domain.StageRunning
	runtime.attempt.Stage = domain.AttemptStageProfile
	runtime.attempt.IngestionAttemptID = request.IngestionAttemptID
	runtime.attempt.IndexVersionID = request.IndexVersionID
	runtime.attempt.Version++
	return runtime.capture, runtime.attempt, nil
}

func (*workflowRuntime) MarkProfileRunning(context.Context, domain.ProcessingAttempt, int64, time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	return domain.Capture{}, domain.ProcessingAttempt{}, errors.New("unexpected profile running checkpoint")
}

func (runtime *workflowRuntime) CompleteDegraded(ctx context.Context, request captureapp.DegradedCompletion) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.degradedCalls++
	_, runtime.terminalContextHasDeadline = ctx.Deadline()
	runtime.terminalContextCanceled = ctx.Err() != nil
	runtime.degraded = request
	if request.ExpectedCaptureVersion != runtime.capture.Version {
		return domain.Capture{}, domain.ProcessingAttempt{}, errors.New("degraded version drift")
	}
	runtime.capture.Version++
	runtime.capture.Status = domain.StatusReadyDegraded
	runtime.capture.IngestionStatus = request.IngestionStatus
	runtime.capture.IndexStatus = request.IndexStatus
	runtime.capture.ProfileStatus = domain.StageCapabilityUnavailable
	if request.ProfileStatus == domain.ProfileStatusFailed {
		runtime.capture.ProfileStatus = domain.StageFailed
	}
	runtime.attempt.ProfileID = request.ProfileID
	runtime.attempt.Stage = domain.AttemptStageComplete
	runtime.attempt.Status = domain.AttemptStatusSucceeded
	completed := request.CompletedAt
	runtime.attempt.CompletedAt = &completed
	runtime.attempt.Version++
	return runtime.capture, runtime.attempt, nil
}

func (runtime *workflowRuntime) CompleteAttempt(_ context.Context, request captureapp.ReadyCompletion) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.completeCalls++
	if request.ExpectedCaptureVersion != runtime.capture.Version {
		return domain.Capture{}, domain.ProcessingAttempt{}, errors.New("completion version drift")
	}
	runtime.capture.Version++
	runtime.capture.Status = domain.StatusReady
	runtime.capture.ProfileStatus = domain.StageReady
	runtime.attempt.ProfileID = request.ProfileID
	runtime.attempt.Stage = domain.AttemptStageComplete
	runtime.attempt.Status = domain.AttemptStatusSucceeded
	completed := request.CompletedAt
	runtime.attempt.CompletedAt = &completed
	runtime.attempt.Version++
	return runtime.capture, runtime.attempt, nil
}

func (runtime *workflowRuntime) FailAttempt(ctx context.Context, request captureapp.AttemptFailure) (domain.Capture, domain.ProcessingAttempt, error) {
	runtime.failCalls++
	_, runtime.terminalContextHasDeadline = ctx.Deadline()
	runtime.terminalContextCanceled = ctx.Err() != nil
	runtime.failure = request
	runtime.capture.Version++
	runtime.capture.Status = domain.StatusProcessingFailed
	if request.Stage == domain.AttemptStageFetch {
		runtime.capture.Status = domain.StatusFetchFailed
	}
	runtime.attempt.Stage = request.Stage
	runtime.attempt.Status = domain.AttemptStatusFailed
	runtime.attempt.ErrorCode = request.Code
	runtime.attempt.Retryable = request.Retryable
	completed := request.FailedAt
	runtime.attempt.CompletedAt = &completed
	runtime.attempt.Version++
	return runtime.capture, runtime.attempt, nil
}

type workflowContent struct {
	calls, publishCalls, discardCalls int
	sourceRef                         string
}

func (content *workflowContent) StageManagedBytes(_ context.Context, _ foundation.ID, sourceRef string, bytes []byte, hash string) (workspacedomain.ManagedContentStage, error) {
	content.calls++
	content.sourceRef = sourceRef
	return workspacedomain.ManagedContentStage{
		ContentHash: hash, ByteSize: int64(len(bytes)), ManagedLocation: ".knowledge/sources/" + hash,
		StagingLocation: ".knowledge/sources/.staging/" + sourceRef,
	}, nil
}

func (content *workflowContent) PublishManagedBytes(_ context.Context, _ foundation.ID, stage workspacedomain.ManagedContentStage) (workspacedomain.ContentCapture, error) {
	content.publishCalls++
	return workspacedomain.ContentCapture{ContentHash: stage.ContentHash, ByteSize: stage.ByteSize, ManagedLocation: stage.ManagedLocation, Created: true}, nil
}

func (content *workflowContent) DiscardManagedBytes(_ context.Context, _ foundation.ID, _ workspacedomain.ManagedContentStage) error {
	content.discardCalls++
	return nil
}

type workflowFetcher struct {
	result captureapp.URLFetchResult
	err    error
	calls  int
}

func (fetcher *workflowFetcher) Fetch(context.Context, string) (captureapp.URLFetchResult, error) {
	fetcher.calls++
	return fetcher.result, fetcher.err
}

type workflowRefresher struct {
	result  retrievalapp.SourceRefreshResult
	err     error
	calls   int
	request retrievalapp.SourceRefreshRequest
}

func (refresher *workflowRefresher) Refresh(_ context.Context, request retrievalapp.SourceRefreshRequest) (retrievalapp.SourceRefreshResult, error) {
	refresher.calls++
	refresher.request = request
	return refresher.result, refresher.err
}

type workflowProfiles struct {
	revisionID foundation.ID
	err        error
	calls      int
	request    ProfileGenerationRequest
}

func (profiles *workflowProfiles) Generate(_ context.Context, request ProfileGenerationRequest) (ProfileGenerationResult, error) {
	profiles.calls++
	profiles.request = request
	if profiles.err != nil {
		return ProfileGenerationResult{}, profiles.err
	}
	return ProfileGenerationResult{ProfileID: request.ProfileID, RevisionID: profiles.revisionID}, nil
}

type workflowIDs struct{ values []foundation.ID }

func (ids *workflowIDs) New() (foundation.ID, error) {
	if len(ids.values) == 0 {
		return "", errors.New("no test ID remaining")
	}
	value := ids.values[0]
	ids.values = ids.values[1:]
	return value, nil
}

func newWorkflowExecutor(t *testing.T, capture domain.Capture, runtime *workflowRuntime, content *workflowContent, fetcher *workflowFetcher, refresher *workflowRefresher, profiles *workflowProfiles, ids []foundation.ID) *Executor {
	t.Helper()
	repository := &workflowCaptureRepository{capture: capture}
	runtime.repository = repository
	executor, err := NewExecutor(ExecutorDependencies{
		Captures: repository, Runtime: runtime, Content: content, Fetcher: fetcher,
		Refresher: refresher, Profiles: profiles, IDs: &workflowIDs{values: ids}, Clock: foundation.FixedClock{Value: workflowTestNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func workflowCapture(kind domain.Kind) domain.Capture {
	capture := domain.Capture{
		ID: workflowTestID(2), WorkspaceID: workflowTestID(1), Kind: kind, DisplayName: "Java AI", SourceID: workflowTestID(3),
		Status: domain.StatusSourceSaved, FetchStatus: domain.StageNotApplicable, IngestionStatus: domain.StagePending,
		IndexStatus: domain.StagePending, ProfileStatus: domain.StagePending, Version: 1, CapturedAt: workflowTestNow, UpdatedAt: workflowTestNow,
	}
	if kind == domain.KindURL {
		capture.OriginalLocation = "https://example.com/java-ai"
		capture.OriginalURL = capture.OriginalLocation
		capture.LatestSourceVersionID = ""
		capture.Status = domain.StatusReceived
		capture.FetchStatus = domain.StagePending
		return capture
	}
	capture.OriginalLocation = "captures/input"
	capture.OriginalInputHash = strings.Repeat("a", 64)
	capture.LatestSourceVersionID = workflowTestID(4)
	return capture
}

func workflowAttempt(capture domain.Capture, stage domain.AttemptStage) domain.ProcessingAttempt {
	return domain.ProcessingAttempt{
		ID: workflowTestID(5), WorkspaceID: capture.WorkspaceID, CaptureID: capture.ID, SourceVersionID: capture.LatestSourceVersionID,
		WorkflowRunID: workflowTestID(11), AttemptNumber: 1, Stage: stage, Status: domain.AttemptStatusRunning,
		StartedAt: workflowTestNow, Version: 1,
	}
}

func workflowRefreshResult() retrievalapp.SourceRefreshResult {
	return retrievalapp.SourceRefreshResult{IngestionAttemptID: workflowTestID(40), ParseProjectionID: workflowTestID(41), IndexVersionID: workflowTestID(42)}
}

func workflowExecution(workspaceID, captureID foundation.ID) workflowapp.ExecutionContext {
	definition, err := RegisteredDefinition()
	if err != nil {
		panic(err)
	}
	return workflowapp.ExecutionContext{
		WorkspaceID: workspaceID, DefinitionID: workflowTestID(10), DefinitionVersion: captureapp.ProcessingDefinitionVersion,
		DefinitionHash: definition.GraphHash, RunID: workflowTestID(11), NodeKey: captureapp.ProcessingNodeKey,
		NodeRunID: workflowTestID(12), NodeAttemptID: workflowTestID(13), NodeKind: captureapp.ProcessingNodeKind,
		NodeVersion: 1, InputSchemaVersion: captureapp.ProcessingInputSchemaVersion, AttemptNo: 1, DispatchNo: 1,
		LeaseOwner: "capture-test", Input: mustWorkflowJSON(nil, captureapp.ProcessingInput{SchemaVersion: 1, WorkspaceID: workspaceID, CaptureID: captureID}),
	}
}

func workflowTestIDs(values ...int) []foundation.ID {
	ids := make([]foundation.ID, len(values))
	for index, value := range values {
		ids[index] = workflowTestID(value)
	}
	return ids
}

func workflowTestID(value int) foundation.ID {
	id, err := foundation.ParseID(fmt.Sprintf("90000000-0000-4000-8000-000000000%03d", value))
	if err != nil {
		panic(err)
	}
	return id
}

func mustWorkflowJSON(t *testing.T, value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Helper()
			t.Fatal(err)
		}
		panic(err)
	}
	return encoded
}

func decodeWorkflowResult(t *testing.T, result workflowapp.ExecutionResult) OutputReceipt {
	t.Helper()
	var receipt OutputReceipt
	if err := json.Unmarshal(result.Output, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func workflowErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
