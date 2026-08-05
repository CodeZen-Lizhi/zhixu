package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const dispatchRecoveryTimeout = 5 * time.Second

// StartInput is the only Workflow input persisted by the Organizing dispatcher.
// All other state is reloaded from owner repositories by the executor.
type StartInput struct {
	SnapshotID foundation.ID `json:"snapshot_id"`
}

// WorkflowStarter starts or idempotently replays one registered Workflow Run.
type WorkflowStarter interface {
	Start(context.Context, workflowapp.StartCommand) (workflowdomain.Run, error)
}

// DispatcherDependencies are the durable Organizing start-outbox dependencies.
type DispatcherDependencies struct {
	Repository    organizingapp.StartRepository
	Workflows     WorkflowStarter
	IDs           foundation.IDGenerator
	Clock         foundation.Clock
	Owner         string
	LeaseDuration time.Duration
	RetryBase     time.Duration
	MaxAttempts   int
}

// DispatchBatchResult reports one bounded outbox pass.
type DispatchBatchResult struct {
	Claimed  int
	Started  int
	Replayed int
	Retried  int
	Poisoned int
}

// Dispatcher converts immutable Snapshot start events into registered Workflow Runs.
type Dispatcher struct{ dependencies DispatcherDependencies }

// NewDispatcher constructs a bounded, fenced outbox dispatcher.
func NewDispatcher(dependencies DispatcherDependencies) (*Dispatcher, error) {
	dependencies.Owner = strings.TrimSpace(dependencies.Owner)
	if dependencies.Repository == nil || dependencies.Workflows == nil || dependencies.IDs == nil || dependencies.Clock == nil ||
		dependencies.Owner == "" || dependencies.LeaseDuration <= 0 || dependencies.RetryBase <= 0 ||
		dependencies.MaxAttempts < 1 || dependencies.MaxAttempts > 1000 {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_DISPATCHER_UNAVAILABLE", true, "organizing dispatcher dependencies are incomplete")
	}
	return &Dispatcher{dependencies: dependencies}, nil
}

// DispatchBatch starts at most limit due Organizing workflows.
func (dispatcher *Dispatcher) DispatchBatch(ctx context.Context, limit int) (DispatchBatchResult, error) {
	if dispatcher == nil || ctx == nil || limit < 1 || limit > 100 {
		return DispatchBatchResult{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_DISPATCH_BATCH_INVALID", false, "organizing dispatch batch is invalid")
	}
	var result DispatchBatchResult
	for result.Claimed < limit {
		lease, found, err := dispatcher.dependencies.Repository.ClaimStart(ctx, dispatcher.dependencies.Owner, dispatcher.dependencies.LeaseDuration)
		if err != nil {
			return result, err
		}
		if !found {
			return result, nil
		}
		result.Claimed++
		definitionKey, definitionVersion, err := definitionForKind(lease.TemplateKind)
		if err != nil {
			if poisonErr := dispatcher.poison(ctx, lease, "ORGANIZING_TEMPLATE_KIND_UNREGISTERED"); poisonErr != nil {
				return result, errors.Join(err, poisonErr)
			}
			result.Poisoned++
			continue
		}
		input, err := json.Marshal(StartInput{SnapshotID: lease.SnapshotID})
		if err != nil {
			return result, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_WORKFLOW_INPUT_ENCODING_FAILED", false, "organizing workflow input could not be encoded")
		}
		run, startErr := dispatcher.dependencies.Workflows.Start(ctx, workflowapp.StartCommand{
			WorkspaceID: lease.WorkspaceID, DefinitionKey: definitionKey, DefinitionVersion: definitionVersion,
			Input: input, IdempotencyKey: organizingapp.StartIdempotencyKey(lease.SnapshotID),
		})
		if startErr != nil {
			if err := dispatcher.recoverStartFailure(ctx, lease, startErr, &result); err != nil {
				return result, err
			}
			continue
		}
		bindingID, err := dispatcher.dependencies.IDs.New()
		if err != nil {
			return result, err
		}
		startedAt := canonicalTime(dispatcher.dependencies.Clock.Now())
		_, replayed, err := dispatcher.dependencies.Repository.CompleteStart(ctx, organizingapp.CompleteStartRecord{
			Lease: lease, BindingID: bindingID, WorkflowRunID: run.ID,
			DefinitionKey: definitionKey, DefinitionVersion: definitionVersion, StartedAt: startedAt,
		})
		if err != nil {
			// The Workflow Start may already be committed. Leave the fenced lease to
			// expire so the next pass can replay the same Start idempotency key.
			return result, err
		}
		if replayed {
			result.Replayed++
		} else {
			result.Started++
		}
	}
	return result, nil
}

func (dispatcher *Dispatcher) recoverStartFailure(ctx context.Context, lease organizingapp.StartOutboxLease, startErr error, result *DispatchBatchResult) error {
	code, retryable := stableFailure(startErr, "ORGANIZING_WORKFLOW_START_FAILED")
	if retryable && lease.AttemptCount < dispatcher.dependencies.MaxAttempts {
		recoveryCtx, cancel := dispatchRecoveryContext(ctx)
		err := dispatcher.dependencies.Repository.RetryStart(recoveryCtx, organizingapp.RetryStartRecord{
			Lease: lease, ErrorCode: code, Delay: boundedBackoff(dispatcher.dependencies.RetryBase, lease.AttemptCount),
		})
		cancel()
		if err != nil {
			return errors.Join(startErr, err)
		}
		result.Retried++
		return nil
	}
	if err := dispatcher.poison(ctx, lease, code); err != nil {
		return errors.Join(startErr, err)
	}
	result.Poisoned++
	return nil
}

func (dispatcher *Dispatcher) poison(ctx context.Context, lease organizingapp.StartOutboxLease, code string) error {
	recoveryCtx, cancel := dispatchRecoveryContext(ctx)
	defer cancel()
	return dispatcher.dependencies.Repository.PoisonStart(recoveryCtx, organizingapp.PoisonStartRecord{Lease: lease, ErrorCode: code})
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

func definitionForKind(kind organizingdomain.TemplateKind) (string, int64, error) {
	switch kind {
	case organizingdomain.TemplateTopicArticle:
		return TopicArticleDefinitionKey, DefinitionVersion, nil
	case organizingdomain.TemplateMergeDocuments:
		return MergeDocumentsDefinitionKey, DefinitionVersion, nil
	case organizingdomain.TemplateKnowledgeReport:
		return KnowledgeReportDefinitionKey, DefinitionVersion, nil
	case organizingdomain.TemplateInterviewReview:
		return InterviewReviewDefinitionKey, DefinitionVersion, nil
	default:
		return "", 0, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_TEMPLATE_KIND_UNREGISTERED", false, "organizing template kind is not registered")
	}
}

func canonicalTime(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }
