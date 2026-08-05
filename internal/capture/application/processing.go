package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	// ProcessingDefinitionKey is the only registered Workflow started by the Capture outbox.
	ProcessingDefinitionKey = "capture-processing"
	// ProcessingDefinitionVersion freezes the first Capture processing workflow contract.
	ProcessingDefinitionVersion int64 = 1
	// ProcessingInputSchemaVersion freezes the minimal Capture workflow input.
	ProcessingInputSchemaVersion = 1
	// ProcessingOutputSchemaVersion freezes the redacted completion receipt.
	ProcessingOutputSchemaVersion = 1
	// ProcessingNodeKey is the stable logical key of the single Capture node.
	ProcessingNodeKey = "capture.process"
	// ProcessingNodeKind is the Worker executor registry key.
	ProcessingNodeKind = "capture.process"
	// dispatchRecoveryTimeout bounds outbox recovery after the caller is cancelled.
	dispatchRecoveryTimeout = 5 * time.Second
)

// ProcessingInput is the minimal persisted Workflow input; all mutable state is reloaded.
type ProcessingInput struct {
	SchemaVersion int           `json:"schema_version"`
	WorkspaceID   foundation.ID `json:"workspace_id"`
	CaptureID     foundation.ID `json:"capture_id"`
}

// OutboxLease is a short-lived claim on one due Capture processing event.
type OutboxLease struct {
	ID           foundation.ID
	WorkspaceID  foundation.ID
	CaptureID    foundation.ID
	Owner        string
	AttemptCount int
	Version      int64
	LeaseUntil   time.Time
}

// OutboxStore owns DB-time Capture event claims and terminal delivery facts.
type OutboxStore interface {
	ClaimNext(context.Context, string, time.Duration) (OutboxLease, bool, error)
	MarkPublished(context.Context, OutboxLease, foundation.ID) error
	Reschedule(context.Context, OutboxLease, string, time.Duration) error
	Poison(context.Context, OutboxLease, string) error
}

// WorkflowStartPort avoids requiring Workflow domain values to implement helper methods.
type WorkflowStartPort interface {
	StartCaptureWorkflow(context.Context, workflowapp.StartCommand) (foundation.ID, error)
}

// DispatcherDependencies are the durable Capture outbox dependencies.
type DispatcherDependencies struct {
	Outbox        OutboxStore
	Workflows     WorkflowStartPort
	Owner         string
	LeaseDuration time.Duration
	RetryBase     time.Duration
	MaxAttempts   int
}

// DispatchBatchResult reports bounded Capture workflow starts.
type DispatchBatchResult struct {
	Claimed  int
	Started  int
	Retried  int
	Poisoned int
}

// OutboxDispatcher reliably converts Capture events into registered Workflow runs.
type OutboxDispatcher struct{ dependencies DispatcherDependencies }

// NewOutboxDispatcher creates a bounded DB-backed Capture dispatcher.
func NewOutboxDispatcher(dependencies DispatcherDependencies) (*OutboxDispatcher, error) {
	dependencies.Owner = strings.TrimSpace(dependencies.Owner)
	if dependencies.Outbox == nil || dependencies.Workflows == nil || dependencies.Owner == "" ||
		dependencies.LeaseDuration <= 0 || dependencies.RetryBase <= 0 || dependencies.MaxAttempts < 1 || dependencies.MaxAttempts > 1000 {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_DISPATCHER_UNAVAILABLE", true, errors.New("capture dispatcher dependencies are incomplete"))
	}
	return &OutboxDispatcher{dependencies: dependencies}, nil
}

// DispatchBatch starts at most limit due Capture workflows.
func (dispatcher *OutboxDispatcher) DispatchBatch(ctx context.Context, limit int) (DispatchBatchResult, error) {
	if dispatcher == nil || limit < 1 || limit > 100 {
		return DispatchBatchResult{}, invalid("CAPTURE_DISPATCH_BATCH_INVALID", "capture dispatch batch is invalid")
	}
	var result DispatchBatchResult
	for result.Claimed < limit {
		lease, found, err := dispatcher.dependencies.Outbox.ClaimNext(ctx, dispatcher.dependencies.Owner, dispatcher.dependencies.LeaseDuration)
		if err != nil {
			return result, err
		}
		if !found {
			return result, nil
		}
		result.Claimed++
		input, err := json.Marshal(ProcessingInput{SchemaVersion: ProcessingInputSchemaVersion, WorkspaceID: lease.WorkspaceID, CaptureID: lease.CaptureID})
		if err != nil {
			return result, inconsistent("CAPTURE_WORKFLOW_INPUT_ENCODING_FAILED", "capture workflow input could not be encoded")
		}
		runID, startErr := dispatcher.dependencies.Workflows.StartCaptureWorkflow(ctx, workflowapp.StartCommand{
			WorkspaceID: lease.WorkspaceID, DefinitionKey: ProcessingDefinitionKey,
			DefinitionVersion: ProcessingDefinitionVersion, Input: input,
			IdempotencyKey: "capture-process:" + string(lease.CaptureID) + ":" + string(lease.ID),
		})
		if startErr == nil {
			if err := dispatcher.dependencies.Outbox.MarkPublished(ctx, lease, runID); err != nil {
				return result, err
			}
			result.Started++
			continue
		}
		code, retryable := stableFailure(startErr, "CAPTURE_WORKFLOW_START_FAILED")
		if retryable && lease.AttemptCount < dispatcher.dependencies.MaxAttempts {
			delay := boundedBackoff(dispatcher.dependencies.RetryBase, lease.AttemptCount)
			recoveryCtx, cancel := dispatchRecoveryContext(ctx)
			err := dispatcher.dependencies.Outbox.Reschedule(recoveryCtx, lease, code, delay)
			cancel()
			if err != nil {
				return result, errors.Join(startErr, err)
			}
			result.Retried++
			continue
		}
		recoveryCtx, cancel := dispatchRecoveryContext(ctx)
		err = dispatcher.dependencies.Outbox.Poison(recoveryCtx, lease, code)
		cancel()
		if err != nil {
			return result, errors.Join(startErr, err)
		}
		result.Poisoned++
	}
	return result, nil
}

func dispatchRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), dispatchRecoveryTimeout)
}

func boundedBackoff(base time.Duration, attempt int) time.Duration {
	const maximum = 30 * time.Minute
	delay := base
	for index := 1; index < attempt && delay < maximum/2; index++ {
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func stableFailure(err error, fallback string) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		code := strings.TrimSpace(classified.Code)
		if code == "" {
			code = fallback
		}
		return code, classified.Retryable
	}
	return fallback, false
}

// BeginAttemptRequest binds a Capture checkpoint row to a Workflow attempt.
type BeginAttemptRequest struct {
	ID            foundation.ID
	WorkspaceID   foundation.ID
	CaptureID     foundation.ID
	WorkflowRunID foundation.ID
	AttemptNumber int
	StartedAt     time.Time
}

// MaterializeURLRequest atomically appends a fetched Source Version and advances Capture state.
type MaterializeURLRequest struct {
	Attempt                 domain.ProcessingAttempt
	ExpectedCaptureVersion  int64
	ArtifactID              foundation.ID
	SourceVersionID         foundation.ID
	ContentHash             string
	ByteSize                int64
	MediaType               string
	OriginalContentLocation string
	ManagedLocation         string
	CapturedAt              time.Time
}

// RefreshCheckpoint freezes the successful ingestion and retrieval outputs.
type RefreshCheckpoint struct {
	Attempt                domain.ProcessingAttempt
	ExpectedCaptureVersion int64
	IngestionAttemptID     foundation.ID
	ParseProjectionID      foundation.ID
	IndexVersionID         foundation.ID
	VectorDegraded         bool
	UpdatedAt              time.Time
}

// AttemptFailure is a stable terminal or retryable Capture stage failure.
type AttemptFailure struct {
	Attempt                domain.ProcessingAttempt
	ExpectedCaptureVersion int64
	Stage                  domain.AttemptStage
	Code                   string
	Retryable              bool
	FailedAt               time.Time
}

// DegradedCompletion records a safe original and the exact unavailable derived layers.
type DegradedCompletion struct {
	Attempt                domain.ProcessingAttempt
	ExpectedCaptureVersion int64
	ProfileID              foundation.ID
	IngestionStatus        domain.StageStatus
	IndexStatus            domain.StageStatus
	ProfileStatus          domain.ProfileStatus
	Code                   string
	Retryable              bool
	CompletedAt            time.Time
}

// ReadyCompletion completes a searchable Capture with a validated Profile revision.
type ReadyCompletion struct {
	Attempt                domain.ProcessingAttempt
	ExpectedCaptureVersion int64
	ProfileID              foundation.ID
	VectorDegraded         bool
	CompletedAt            time.Time
}

// ProcessingRepository owns Capture stage checkpoints and URL materialization transactions.
type ProcessingRepository interface {
	BeginAttempt(context.Context, BeginAttemptRequest) (domain.Capture, domain.ProcessingAttempt, error)
	MaterializeURL(context.Context, MaterializeURLRequest) (domain.Capture, domain.ProcessingAttempt, error)
	MarkRefreshRunning(context.Context, domain.ProcessingAttempt, int64, time.Time) (domain.Capture, domain.ProcessingAttempt, error)
	MarkRefreshReady(context.Context, RefreshCheckpoint) (domain.Capture, domain.ProcessingAttempt, error)
	MarkProfileRunning(context.Context, domain.ProcessingAttempt, int64, time.Time) (domain.Capture, domain.ProcessingAttempt, error)
	CompleteDegraded(context.Context, DegradedCompletion) (domain.Capture, domain.ProcessingAttempt, error)
	CompleteAttempt(context.Context, ReadyCompletion) (domain.Capture, domain.ProcessingAttempt, error)
	FailAttempt(context.Context, AttemptFailure) (domain.Capture, domain.ProcessingAttempt, error)
}
