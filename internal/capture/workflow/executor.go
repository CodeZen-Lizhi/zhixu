package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	errorCodeOCRUnavailable     = "CAPTURE_OCR_CAPABILITY_UNAVAILABLE"
	errorCodeProfileUnavailable = "CAPTURE_PROFILE_CAPABILITY_UNAVAILABLE"
	errorCodeProcessingFailed   = "CAPTURE_PROCESSING_FAILED"
	errorCodeContentUnknown     = "CAPTURE_CONTENT_FINALIZATION_UNKNOWN"
	terminalPersistenceTimeout  = 5 * time.Second
)

// SourceRefresher runs ingestion and Workspace index activation for one immutable Source Version.
type SourceRefresher interface {
	Refresh(context.Context, retrievalapp.SourceRefreshRequest) (retrievalapp.SourceRefreshResult, error)
}

// ProfileGenerationRequest 是 Capture Application 画像生成请求的兼容别名。
type ProfileGenerationRequest = captureapp.ProfileGenerationRequest

// ProfileGenerationResult 是 Capture Application 画像生成结果的兼容别名。
type ProfileGenerationResult = captureapp.ProfileGenerationResult

// ProfileGenerator 是 Capture Application 画像生成端口的兼容别名。
type ProfileGenerator = captureapp.ProfileGenerator

// ExecutorDependencies are the controlled Capture processing ports.
type ExecutorDependencies struct {
	Captures  captureapp.Repository
	Runtime   captureapp.ProcessingRepository
	Content   captureapp.ManagedContentWriter
	Fetcher   captureapp.URLFetcher
	Refresher SourceRefresher
	Profiles  ProfileGenerator
	IDs       foundation.IDGenerator
	Clock     foundation.Clock
}

// Executor processes a Capture under the shared Workflow lease and durable Capture checkpoints.
type Executor struct{ dependencies ExecutorDependencies }

// NewExecutor creates a Capture processing executor. Profile generation may be nil to expose an explicit degraded state.
func NewExecutor(dependencies ExecutorDependencies) (*Executor, error) {
	if nilPort(dependencies.Captures) || nilPort(dependencies.Runtime) || nilPort(dependencies.Content) ||
		nilPort(dependencies.Fetcher) || nilPort(dependencies.Refresher) || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_WORKFLOW_UNAVAILABLE", true, errors.New("capture workflow dependencies are incomplete"))
	}
	return &Executor{dependencies: dependencies}, nil
}

// Execute resumes the first incomplete Capture stage and returns a redacted receipt.
func (executor *Executor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if executor == nil {
		return workflowapp.ExecutionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_WORKFLOW_UNAVAILABLE", true, errors.New("capture workflow executor is nil"))
	}
	if err := validateExecution(execution); err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	input, err := DecodeInput(execution.Input)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if input.WorkspaceID != execution.WorkspaceID {
		return workflowapp.ExecutionResult{}, inputError(errors.New("capture workflow workspace binding differs"))
	}
	capture, err := executor.dependencies.Captures.Get(ctx, input.WorkspaceID, input.CaptureID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	attemptID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	startedAt := executor.now()
	capture, attempt, err := executor.dependencies.Runtime.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: attemptID, WorkspaceID: input.WorkspaceID, CaptureID: input.CaptureID,
		WorkflowRunID: execution.RunID, AttemptNumber: execution.AttemptNo, StartedAt: startedAt,
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if attempt.Status == domain.AttemptStatusSucceeded {
		return captureResult(capture, attempt.ProfileID)
	}
	if attempt.Status != domain.AttemptStatusRunning {
		return workflowapp.ExecutionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_ATTEMPT_TERMINAL", false, errors.New("capture workflow attempt is already terminal"))
	}

	if capture.Kind == domain.KindURL && capture.LatestSourceVersionID == "" {
		capture, attempt, err = executor.materializeURL(ctx, capture, attempt)
		if err != nil {
			if stableCode(err, "") == errorCodeContentUnknown {
				return workflowapp.ExecutionResult{}, err
			}
			return executor.fail(ctx, capture, attempt, domain.AttemptStageFetch, err)
		}
	}
	if capture.Kind == domain.KindImage {
		return executor.completeUnavailable(ctx, capture, attempt, domain.StageCapabilityUnavailable,
			domain.StageCapabilityUnavailable, errorCodeOCRUnavailable, false)
	}

	if attempt.Stage != domain.AttemptStageIngestion {
		capture, attempt, err = executor.dependencies.Runtime.MarkRefreshRunning(ctx, attempt, capture.Version, executor.now())
		if err != nil {
			return workflowapp.ExecutionResult{}, err
		}
	}
	refresh, err := executor.dependencies.Refresher.Refresh(ctx, retrievalapp.SourceRefreshRequest{
		RequestID: execution.RunID, WorkspaceID: capture.WorkspaceID, SourceID: capture.SourceID,
		SourceVersionID: capture.LatestSourceVersionID, AttemptNumber: 1,
	})
	if err != nil {
		if stableCode(err, "") == "PARSER_MEDIA_TYPE_UNSUPPORTED" || stableCode(err, "") == "SOURCE_MEDIA_TYPE_UNSUPPORTED" {
			return executor.completeUnavailable(ctx, capture, attempt, domain.StageCapabilityUnavailable,
				domain.StageCapabilityUnavailable, "CAPTURE_PARSER_CAPABILITY_UNAVAILABLE", false)
		}
		return executor.fail(ctx, capture, attempt, classifyRefreshStage(err), err)
	}
	capture, attempt, err = executor.dependencies.Runtime.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: attempt, ExpectedCaptureVersion: capture.Version,
		IngestionAttemptID: refresh.IngestionAttemptID, ParseProjectionID: refresh.ParseProjectionID,
		IndexVersionID: refresh.IndexVersionID, VectorDegraded: refresh.VectorDegraded, UpdatedAt: executor.now(),
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}

	if nilPort(executor.dependencies.Profiles) {
		return executor.completeUnavailable(ctx, capture, attempt, domain.StageReady, domain.StageReady,
			errorCodeProfileUnavailable, false)
	}
	profileID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	generated, err := executor.dependencies.Profiles.Generate(ctx, captureapp.ProfileGenerationRequest{
		ProfileID: profileID, Capture: capture, Attempt: attempt, ParseProjectionID: refresh.ParseProjectionID,
		IndexVersionID: refresh.IndexVersionID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID,
		NodeAttemptID: execution.NodeAttemptID, ModelSettingsRevision: cloneRevision(execution.ModelSettingsRevision),
	})
	if err != nil {
		code := stableCode(err, "CAPTURE_PROFILE_GENERATION_FAILED")
		_, retryable := failureProperties(err)
		return executor.completeProfileFailure(ctx, capture, attempt, profileID, code, retryable, err)
	}
	if !validID(generated.ProfileID) || !validID(generated.RevisionID) {
		return executor.fail(ctx, capture, attempt, domain.AttemptStageProfile,
			outputError(errors.New("capture profile generator returned an invalid binding")))
	}
	capture, attempt, err = executor.dependencies.Runtime.CompleteAttempt(ctx, captureapp.ReadyCompletion{
		Attempt: attempt, ExpectedCaptureVersion: capture.Version, ProfileID: generated.ProfileID,
		VectorDegraded: refresh.VectorDegraded, CompletedAt: executor.now(),
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return captureResult(capture, attempt.ProfileID)
}

func (executor *Executor) materializeURL(ctx context.Context, capture domain.Capture, attempt domain.ProcessingAttempt) (domain.Capture, domain.ProcessingAttempt, error) {
	fetched, err := executor.dependencies.Fetcher.Fetch(ctx, capture.OriginalURL)
	if err != nil {
		return capture, attempt, err
	}
	if len(fetched.Content) == 0 || fetched.MediaType != "text/html" || strings.TrimSpace(fetched.FinalURL) == "" {
		return capture, attempt, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_FETCH_RESULT_INVALID", false, errors.New("capture fetch result is invalid"))
	}
	digest := sha256.Sum256(fetched.Content)
	contentHash := hex.EncodeToString(digest[:])
	artifactID, err := executor.dependencies.IDs.New()
	if err != nil {
		return capture, attempt, err
	}
	versionID, err := executor.dependencies.IDs.New()
	if err != nil {
		return capture, attempt, err
	}
	// Bind cleanup ownership to this proposed immutable Source Version. Multiple
	// deliveries may share one processing attempt, but they must never share a
	// discardable staging locator before one transaction wins.
	stageRef := capture.OriginalLocation + "/fetch/" + string(attempt.ID) + "/" + string(versionID)
	stage, err := executor.dependencies.Content.StageManagedBytes(ctx, capture.WorkspaceID, stageRef, fetched.Content, contentHash)
	if err != nil {
		return capture, attempt, err
	}
	if stage.ContentHash != contentHash || stage.ByteSize != int64(len(fetched.Content)) || strings.TrimSpace(stage.ManagedLocation) == "" {
		return capture, attempt, foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_CONTENT_BINDING_INVALID", false, errors.New("fetched content binding is invalid"))
	}
	materializedCapture, materializedAttempt, materializeErr := executor.dependencies.Runtime.MaterializeURL(ctx, captureapp.MaterializeURLRequest{
		Attempt: attempt, ExpectedCaptureVersion: capture.Version, ArtifactID: artifactID,
		SourceVersionID: versionID, ContentHash: contentHash, ByteSize: int64(len(fetched.Content)),
		MediaType: fetched.MediaType, OriginalContentLocation: fetched.FinalURL,
		ManagedLocation: stage.ManagedLocation, CapturedAt: executor.now(),
	})
	if materializeErr != nil {
		recoveryCtx, cancel := terminalPersistenceContext(ctx)
		current, recoveryErr := executor.dependencies.Captures.Get(recoveryCtx, capture.WorkspaceID, capture.ID)
		if recoveryErr != nil {
			cancel()
			return capture, attempt, contentFinalizationUnknown(errors.Join(materializeErr, recoveryErr))
		}
		if current.LatestSourceVersionID == "" {
			if stableCode(materializeErr, "") == "CAPTURE_URL_MATERIALIZATION_FAILED" {
				cancel()
				return capture, attempt, contentFinalizationUnknown(materializeErr)
			}
			discardErr := executor.dependencies.Content.DiscardManagedBytes(recoveryCtx, capture.WorkspaceID, stage)
			cancel()
			return capture, attempt, errors.Join(materializeErr, discardErr)
		}
		if current.LatestSourceVersionID == versionID {
			_, publishErr := executor.dependencies.Content.PublishManagedBytes(recoveryCtx, capture.WorkspaceID, stage)
			cancel()
			return capture, attempt, contentFinalizationUnknown(errors.Join(materializeErr, publishErr))
		}
		discardErr := executor.dependencies.Content.DiscardManagedBytes(recoveryCtx, capture.WorkspaceID, stage)
		cancel()
		return capture, attempt, contentFinalizationUnknown(errors.Join(materializeErr, discardErr))
	}
	recoveryCtx, cancel := terminalPersistenceContext(ctx)
	published, publishErr := executor.dependencies.Content.PublishManagedBytes(recoveryCtx, capture.WorkspaceID, stage)
	cancel()
	if publishErr != nil {
		return capture, attempt, contentFinalizationUnknown(publishErr)
	}
	if published.ContentHash != stage.ContentHash || published.ByteSize != stage.ByteSize || published.ManagedLocation != stage.ManagedLocation {
		return capture, attempt, contentFinalizationUnknown(errors.New("managed content publish returned a different binding"))
	}
	return materializedCapture, materializedAttempt, nil
}

func contentFinalizationUnknown(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeContentUnknown, true, cause)
}

func (executor *Executor) completeUnavailable(
	ctx context.Context,
	capture domain.Capture,
	attempt domain.ProcessingAttempt,
	ingestionStatus domain.StageStatus,
	indexStatus domain.StageStatus,
	code string,
	retryable bool,
) (workflowapp.ExecutionResult, error) {
	profileID, err := executor.dependencies.IDs.New()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	persistCtx, cancel := terminalPersistenceContext(ctx)
	capture, attempt, err = executor.dependencies.Runtime.CompleteDegraded(persistCtx, captureapp.DegradedCompletion{
		Attempt: attempt, ExpectedCaptureVersion: capture.Version, ProfileID: profileID,
		IngestionStatus: ingestionStatus, IndexStatus: indexStatus,
		ProfileStatus: domain.ProfileStatusCapabilityUnavailable, Code: code, Retryable: retryable,
		CompletedAt: executor.now(),
	})
	cancel()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return captureResult(capture, attempt.ProfileID)
}

func (executor *Executor) completeProfileFailure(
	ctx context.Context,
	capture domain.Capture,
	attempt domain.ProcessingAttempt,
	profileID foundation.ID,
	code string,
	retryable bool,
	cause error,
) (workflowapp.ExecutionResult, error) {
	profileStatus := domain.ProfileStatusFailed
	if code == errorCodeProfileUnavailable {
		profileStatus = domain.ProfileStatusCapabilityUnavailable
		retryable = false
	}
	persistCtx, cancel := terminalPersistenceContext(ctx)
	completedCapture, completedAttempt, err := executor.dependencies.Runtime.CompleteDegraded(persistCtx, captureapp.DegradedCompletion{
		Attempt: attempt, ExpectedCaptureVersion: capture.Version, ProfileID: profileID,
		IngestionStatus: domain.StageReady, IndexStatus: domain.StageReady,
		ProfileStatus: profileStatus, Code: code, Retryable: retryable, CompletedAt: executor.now(),
	})
	cancel()
	if err != nil {
		return workflowapp.ExecutionResult{}, errors.Join(cause, err)
	}
	// A profile failure does not invalidate parsing or retrieval. The Workflow
	// returns success so its runtime does not erase that usable degraded result.
	return captureResult(completedCapture, completedAttempt.ProfileID)
}

func (executor *Executor) fail(ctx context.Context, capture domain.Capture, attempt domain.ProcessingAttempt, stage domain.AttemptStage, cause error) (workflowapp.ExecutionResult, error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return workflowapp.ExecutionResult{}, cause
	}
	code, retryable := failureProperties(cause)
	if code == "" {
		code = errorCodeProcessingFailed
	}
	persistCtx, cancel := terminalPersistenceContext(ctx)
	_, _, persistErr := executor.dependencies.Runtime.FailAttempt(persistCtx, captureapp.AttemptFailure{
		Attempt: attempt, ExpectedCaptureVersion: capture.Version, Stage: stage,
		Code: code, Retryable: retryable, FailedAt: executor.now(),
	})
	cancel()
	return workflowapp.ExecutionResult{}, errors.Join(cause, persistErr)
}

func terminalPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalPersistenceTimeout)
}

func captureResult(capture domain.Capture, profileID foundation.ID) (workflowapp.ExecutionResult, error) {
	output, err := EncodeOutput(OutputReceipt{
		SchemaVersion: captureapp.ProcessingOutputSchemaVersion, WorkspaceID: capture.WorkspaceID,
		CaptureID: capture.ID, Status: capture.Status, ProfileStatus: capture.ProfileStatus, ProfileID: profileID,
	})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return workflowapp.ExecutionResult{Output: output}, nil
}

func (executor *Executor) now() time.Time { return executor.dependencies.Clock.Now().UTC() }

func classifyRefreshStage(err error) domain.AttemptStage {
	code := stableCode(err, "")
	if strings.HasPrefix(code, "INGESTION_") || strings.HasPrefix(code, "PARSER_") || strings.HasPrefix(code, "SOURCE_") {
		return domain.AttemptStageIngestion
	}
	return domain.AttemptStageIndex
}

func failureProperties(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return strings.TrimSpace(classified.Code), classified.Retryable
	}
	return "", false
}

func stableCode(err error, fallback string) string {
	code, _ := failureProperties(err)
	if code == "" {
		return fallback
	}
	return code
}

func cloneRevision(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func nilPort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ workflowapp.Executor = (*Executor)(nil)
