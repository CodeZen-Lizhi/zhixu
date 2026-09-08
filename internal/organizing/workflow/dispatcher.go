package workflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const dispatchRecoveryTimeout = 5 * time.Second

// StartInput is the only Workflow input persisted by the Organizing dispatcher.
// All other state is reloaded from owner repositories by the executor.
type StartInput struct {
	SnapshotID foundation.ID `json:"snapshot_id"`
}

// DispatchBatchResult reports one bounded outbox pass.
type DispatchBatchResult struct {
	Claimed  int
	Started  int
	Replayed int
	Retried  int
	Poisoned int
}

func recoverStartFailure(ctx context.Context, repository organizingapp.StartRepository, retryBase time.Duration, maxAttempts int, lease organizingapp.StartOutboxLease, startErr error, result *DispatchBatchResult) error {
	code, retryable := stableFailure(startErr, "ORGANIZING_WORKFLOW_START_FAILED")
	if retryable && lease.AttemptCount < maxAttempts {
		recoveryCtx, cancel := dispatchRecoveryContext(ctx)
		err := repository.RetryStart(recoveryCtx, organizingapp.RetryStartRecord{
			Lease: lease, ErrorCode: code, Delay: boundedBackoff(retryBase, lease.AttemptCount),
		})
		cancel()
		if err != nil {
			return errors.Join(startErr, err)
		}
		result.Retried++
		return nil
	}
	if err := poisonStart(ctx, repository, lease, code); err != nil {
		return errors.Join(startErr, err)
	}
	result.Poisoned++
	return nil
}

func poisonStart(ctx context.Context, repository organizingapp.StartRepository, lease organizingapp.StartOutboxLease, code string) error {
	recoveryCtx, cancel := dispatchRecoveryContext(ctx)
	defer cancel()
	return repository.PoisonStart(recoveryCtx, organizingapp.PoisonStartRecord{Lease: lease, ErrorCode: code})
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
