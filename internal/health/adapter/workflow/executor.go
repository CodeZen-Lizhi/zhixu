// Package workflow adapts Health Scan detector execution to the durable Workflow runtime.
package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	healthworkflow "github.com/CodeZen-Lizhi/zhixu/internal/health/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const maxHealthScanPagesPerDelivery = 10_000

// HealthScanExecutor 逐 detector 分页执行并在每页后提交 checkpoint/Issue facts。
// Detector/Issue 事实不持有长事务；River 只负责调用本 Executor。
type HealthScanExecutor struct {
	registry *healthapp.Registry
	scans    *healthapp.ScanService
	issues   healthapp.DetectorPageStore
	clock    foundation.Clock
}

var _ workflowapp.Executor = (*HealthScanExecutor)(nil)

// NewHealthScanExecutor 构造严格绑定 detector registry、scan state 与 Issue store 的 executor。
func NewHealthScanExecutor(registry *healthapp.Registry, scans *healthapp.ScanService, issues healthapp.DetectorPageStore, clock foundation.Clock) (*HealthScanExecutor, error) {
	if registry == nil || scans == nil || issues == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_SCAN_EXECUTOR_DEPENDENCY_MISSING", false, errors.New("health scan executor dependencies are missing"))
	}
	return &HealthScanExecutor{registry: registry, scans: scans, issues: issues, clock: clock}, nil
}

// Execute 恢复 durable Scan checkpoint，按页执行 detector，最终保存明确成功/部分/失败终态。
func (executor *HealthScanExecutor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if executor == nil || executor.registry == nil || executor.scans == nil || executor.issues == nil || executor.clock == nil {
		return workflowapp.ExecutionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_SCAN_EXECUTOR_UNAVAILABLE", true, errors.New("health scan executor is unavailable"))
	}
	if ctx == nil || execution.NodeKind != healthapp.HealthScanNodeKind || execution.NodeKey != healthapp.HealthScanNodeKey || execution.InputSchemaVersion != healthapp.HealthScanInputSchemaVersion {
		return workflowapp.ExecutionResult{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeScanInvalid, false, errors.New("health scan execution binding is invalid"))
	}
	input, err := healthworkflowDecode(execution.Input)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	definition, err := healthworkflow.RegisteredDefinition()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].Key != healthapp.HealthScanNodeKey || definition.Graph.Nodes[0].RetryPolicy.MaxRetries < 0 {
		return workflowapp.ExecutionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, errors.New("health scan workflow retry policy is invalid"))
	}
	retryLimit := definition.Graph.Nodes[0].RetryPolicy.MaxRetries
	scan, err := executor.scans.GetByWorkflowRun(ctx, execution.WorkspaceID, execution.RunID)
	if err != nil {
		return workflowapp.ExecutionResult{}, operationalFailure("HEALTH_SCAN_LOOKUP_FAILED", err)
	}
	if scan.WorkspaceID != execution.WorkspaceID || scan.WorkflowRunID != execution.RunID || scan.Scope != input.Scope || scan.Fingerprint != input.Fingerprint || scan.RequestHash != input.RequestHash || scan.IdempotencyKey != input.IdempotencyKey || scan.MaxItems != input.MaxItems || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash {
		return workflowapp.ExecutionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, errors.New("health scan execution binding drifted"))
	}
	if scan.Status == domain.ScanStatusSucceeded || scan.Status == domain.ScanStatusPartial {
		return result(scan)
	}
	if scan.Status == domain.ScanStatusFailed || scan.Status == domain.ScanStatusCancelled {
		return workflowapp.ExecutionResult{}, terminalScanError(scan)
	}
	for pageNo := 0; pageNo < maxHealthScanPagesPerDelivery; pageNo++ {
		coverage, found := nextCoverage(scan.Coverage)
		if !found {
			return executor.finish(ctx, scan, execution.RetryNo, retryLimit)
		}
		descriptor, descriptorFound := executor.registry.Descriptor(coverage.DetectorID)
		if !descriptorFound {
			cause := foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_DETECTOR_UNAVAILABLE", false, healthapp.ErrDetectorUnavailable)
			return executor.failDetector(ctx, scan, coverage, "registry", "HEALTH_DETECTOR_UNAVAILABLE", cause, execution.RetryNo, retryLimit)
		}
		if coverage.DetectorVersion != descriptor.Version {
			cause := foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_DETECTOR_VERSION_DRIFT", false, errors.New("health detector registry version does not match scan coverage"))
			return executor.failDetector(ctx, scan, coverage, "registry", "HEALTH_DETECTOR_VERSION_DRIFT", cause, execution.RetryNo, retryLimit)
		}
		// Registry coverage is the single capability source. A stale/manual start
		// must be made explicit in durable coverage instead of executing an
		// unsupported detector or pretending it returned an empty page.
		if !descriptor.Available || !descriptor.SupportsScope(scan.Scope.Type) {
			if coverage.Status == domain.DetectorCoverageStatusPending {
				reason := descriptor.UnavailableReason
				if reason == "" {
					reason = "detector does not support this scan scope"
				}
				scan, err = executor.scans.Advance(ctx, healthapp.ScanProgress{
					ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version,
					DetectorID: coverage.DetectorID, Status: domain.DetectorCoverageStatusUnavailable,
					Checkpoint: coverage.Checkpoint, UnavailableReason: reason,
				})
				if err != nil {
					return executor.failDetector(ctx, scan, coverage, "checkpoint", "HEALTH_SCAN_CHECKPOINT_FAILED", err, execution.RetryNo, retryLimit)
				}
				continue
			}
			cause := foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_DETECTOR_UNAVAILABLE", false, healthapp.ErrDetectorUnavailable)
			return executor.failDetector(ctx, scan, coverage, "registry", "HEALTH_DETECTOR_UNAVAILABLE", cause, execution.RetryNo, retryLimit)
		}
		detectorImpl, err := executor.registry.Get(coverage.DetectorID)
		if err != nil {
			cause := foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_DETECTOR_UNAVAILABLE", false, err)
			return executor.failDetector(ctx, scan, coverage, "registry", "HEALTH_DETECTOR_UNAVAILABLE", cause, execution.RetryNo, retryLimit)
		}
		if coverage.Status == domain.DetectorCoverageStatusPending {
			scan, err = executor.scans.Advance(ctx, healthapp.ScanProgress{
				ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version,
				DetectorID: coverage.DetectorID, Status: domain.DetectorCoverageStatusRunning,
				Checkpoint: coverage.Checkpoint,
			})
			if err != nil {
				return executor.failDetector(ctx, scan, coverage, "checkpoint", "HEALTH_SCAN_CHECKPOINT_FAILED", err, execution.RetryNo, retryLimit)
			}
			started, ok := nextCoverage(scan.Coverage)
			if !ok || started.DetectorID != coverage.DetectorID || started.Status != domain.DetectorCoverageStatusRunning {
				cause := foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, errors.New("health detector running checkpoint is inconsistent"))
				return executor.failScan(ctx, scan, "checkpoint", "HEALTH_SCAN_CHECKPOINT_INVALID", cause, execution.RetryNo, retryLimit)
			}
			coverage = started
		}
		page, err := detectorImpl.ScanPage(ctx, healthapp.PageRequest{
			Scope: healthapp.Scope{WorkspaceID: scan.WorkspaceID, Type: scan.Scope.Type, Ref: scan.Scope.Ref, Version: scan.Scope.Version,
				Hash: scan.Scope.Hash, ReadModelRevision: scan.Scope.ReadModelRevision, ExactCount: scan.Scope.ExactCount},
			Cursor: coverage.Checkpoint.Cursor, Page: coverage.Checkpoint.Page, BatchSize: domain.DefaultDetectorBatchSize,
			Descriptor: descriptor,
		})
		if err != nil {
			return executor.failDetector(ctx, scan, coverage, "detector", "HEALTH_DETECTOR_PAGE_FAILED", err, execution.RetryNo, retryLimit)
		}
		if page.Processed < 0 || page.Processed != int64(len(page.Findings)) || page.Processed > int64(domain.MaxDetectorBatchSize) ||
			scan.Counters.Processed+page.Processed > scan.MaxItems ||
			(!page.Complete && (page.NextCursor == "" || page.NextCursor == coverage.Checkpoint.Cursor)) ||
			(page.Complete && page.NextCursor != "") {
			cause := foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_DETECTOR_PAGE_INVALID", false, errors.New("health detector page exceeded bounds or did not advance its cursor"))
			return executor.failDetector(ctx, scan, coverage, "detector", "HEALTH_DETECTOR_PAGE_INVALID", cause, execution.RetryNo, retryLimit)
		}
		observations := make([]domain.IssueObservation, 0, len(page.Findings))
		for _, finding := range page.Findings {
			observation, convertErr := detector.FindingToObservation(descriptor, scan.WorkspaceID, finding)
			if convertErr != nil {
				cause := foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_OBSERVATION_INVALID", false, convertErr)
				return executor.failDetector(ctx, scan, coverage, "observation", "HEALTH_OBSERVATION_INVALID", cause, execution.RetryNo, retryLimit)
			}
			observations = append(observations, observation)
		}
		reconciled, err := executor.issues.ReconcileDetectorPage(ctx, healthapp.DetectorPageReconcileRequest{WorkspaceID: scan.WorkspaceID, ScanID: scan.ID, DetectorID: coverage.DetectorID, Observations: observations, ObservedAt: executor.clock.Now().UTC()})
		if err != nil {
			return executor.failDetector(ctx, scan, coverage, "reconcile", "HEALTH_ISSUE_RECONCILE_FAILED", err, execution.RetryNo, retryLimit)
		}
		if err := healthapp.ValidateDetectorPageResult(reconciled, len(observations)); err != nil {
			return executor.failDetector(ctx, scan, coverage, "reconcile", "HEALTH_ISSUE_RECONCILE_INVALID", err, execution.RetryNo, retryLimit)
		}
		next := domain.ScanCheckpoint{Cursor: page.NextCursor, Page: coverage.Checkpoint.Page + 1, LastItem: coverage.Checkpoint.LastItem}
		if len(page.Findings) > 0 {
			last := page.Findings[len(page.Findings)-1].Target
			next.LastItem = &last
		}
		status := domain.DetectorCoverageStatusRunning
		if page.Complete {
			status = domain.DetectorCoverageStatusSucceeded
		}
		scan, err = executor.scans.Advance(ctx, healthapp.ScanProgress{ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version, DetectorID: coverage.DetectorID, Status: status, Checkpoint: next, CountersDelta: domain.ScanCounters{Processed: page.Processed, Created: reconciled.Created, Reopened: reconciled.Reopened, Unchanged: reconciled.Unchanged, Failed: reconciled.Failed}})
		if err != nil {
			return executor.failDetector(ctx, scan, coverage, "checkpoint", "HEALTH_SCAN_CHECKPOINT_FAILED", err, execution.RetryNo, retryLimit)
		}
		if page.Complete {
			continue
		}
	}
	coverage, _ := nextCoverage(scan.Coverage)
	cause := foundation.NewError(foundation.ErrorManualRecoveryRequired, "HEALTH_SCAN_PAGE_LIMIT_EXCEEDED", false, errors.New("health scan exceeded the bounded page limit"))
	return executor.failDetector(ctx, scan, coverage, "detector", "HEALTH_SCAN_PAGE_LIMIT_EXCEEDED", cause, execution.RetryNo, retryLimit)
}

func (executor *HealthScanExecutor) finish(ctx context.Context, scan domain.Scan, retryNo, retryLimit int) (workflowapp.ExecutionResult, error) {
	status := domain.ScanStatusSucceeded
	var failure *domain.FailureSummary
	for _, coverage := range scan.Coverage {
		if coverage.Status == domain.DetectorCoverageStatusFailed {
			return executor.failScan(ctx, scan, "detector", "HEALTH_DETECTOR_FAILED", terminalCoverageError(coverage), retryNo, retryLimit)
		}
		if coverage.Status != domain.DetectorCoverageStatusSucceeded {
			status = domain.ScanStatusPartial
			if coverage.Status == domain.DetectorCoverageStatusFailed || coverage.Status == domain.DetectorCoverageStatusPartial {
				failure = coverage.LastError
			}
		}
	}
	if status == domain.ScanStatusSucceeded {
		for _, coverage := range scan.Coverage {
			if coverage.Status == domain.DetectorCoverageStatusSucceeded {
				if _, err := executor.issues.ResolveMissingForCompleteScan(ctx, scan.WorkspaceID, scan.ID, coverage.DetectorID); err != nil {
					return executor.failScan(ctx, scan, "resolve", "HEALTH_ISSUE_RESOLVE_FAILED", err, retryNo, retryLimit)
				}
			}
		}
		refreshed, err := executor.scans.GetByWorkflowRun(ctx, scan.WorkspaceID, scan.WorkflowRunID)
		if err != nil {
			return executor.failScan(ctx, scan, "resolve", "HEALTH_SCAN_REFRESH_FAILED", err, retryNo, retryLimit)
		}
		scan = refreshed
	}
	finished, err := executor.scans.Finish(ctx, healthapp.ScanTerminal{ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version, Status: status, LastError: failure})
	if err != nil {
		return executor.failScan(ctx, scan, "finish", "HEALTH_SCAN_FINISH_FAILED", err, retryNo, retryLimit)
	}
	return result(finished)
}

func (executor *HealthScanExecutor) failDetector(ctx context.Context, scan domain.Scan, coverage domain.DetectorCoverage, stage, code string, cause error, retryNo, retryLimit int) (workflowapp.ExecutionResult, error) {
	cause = operationalFailure(code, cause)
	if ctx.Err() != nil && errors.Is(cause, ctx.Err()) {
		return workflowapp.ExecutionResult{}, cause
	}
	if retryNo < retryLimit && isRetryable(cause) {
		return workflowapp.ExecutionResult{}, cause
	}
	summary := failureSummary(stage, code, cause)
	persistCtx := context.WithoutCancel(ctx)
	failed, err := executor.scans.Advance(persistCtx, healthapp.ScanProgress{ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version, DetectorID: coverage.DetectorID, Status: domain.DetectorCoverageStatusFailed, Checkpoint: coverage.Checkpoint, LastError: summary})
	if err != nil {
		return workflowapp.ExecutionResult{}, errors.Join(cause, err)
	}
	_, err = executor.scans.Finish(persistCtx, healthapp.ScanTerminal{ScanID: failed.ID, WorkspaceID: failed.WorkspaceID, ExpectedVersion: failed.Version, Status: domain.ScanStatusFailed, LastError: summary})
	if err != nil {
		return workflowapp.ExecutionResult{}, errors.Join(cause, operationalFailure("HEALTH_SCAN_FINISH_FAILED", err))
	}
	return workflowapp.ExecutionResult{}, cause
}

func (executor *HealthScanExecutor) failScan(ctx context.Context, scan domain.Scan, stage, code string, cause error, retryNo, retryLimit int) (workflowapp.ExecutionResult, error) {
	cause = operationalFailure(code, cause)
	if ctx.Err() != nil && errors.Is(cause, ctx.Err()) {
		return workflowapp.ExecutionResult{}, cause
	}
	if retryNo < retryLimit && isRetryable(cause) {
		return workflowapp.ExecutionResult{}, cause
	}
	summary := failureSummary(stage, code, cause)
	_, err := executor.scans.Finish(context.WithoutCancel(ctx), healthapp.ScanTerminal{ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version, Status: domain.ScanStatusFailed, LastError: summary})
	return workflowapp.ExecutionResult{}, errors.Join(cause, operationalFailure("HEALTH_SCAN_FINISH_FAILED", err))
}

func nextCoverage(values []domain.DetectorCoverage) (domain.DetectorCoverage, bool) {
	for _, coverage := range values {
		if coverage.Status == domain.DetectorCoverageStatusPending || coverage.Status == domain.DetectorCoverageStatusRunning {
			return coverage, true
		}
	}
	return domain.DetectorCoverage{}, false
}

func result(scan domain.Scan) (workflowapp.ExecutionResult, error) {
	if scan.Status != domain.ScanStatusSucceeded && scan.Status != domain.ScanStatusPartial {
		return workflowapp.ExecutionResult{}, terminalScanError(scan)
	}
	output, err := json.Marshal(struct {
		ScanID         foundation.ID     `json:"scan_id"`
		Status         domain.ScanStatus `json:"status"`
		ProcessedCount int64             `json:"processed_count"`
		CreatedCount   int64             `json:"created_count"`
		ReopenedCount  int64             `json:"reopened_count"`
		ResolvedCount  int64             `json:"resolved_count"`
		UnchangedCount int64             `json:"unchanged_count"`
		FailedCount    int64             `json:"failed_count"`
	}{scan.ID, scan.Status, scan.Counters.Processed, scan.Counters.Created, scan.Counters.Reopened, scan.Counters.Resolved, scan.Counters.Unchanged, scan.Counters.Failed})
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	return workflowapp.ExecutionResult{Output: output}, nil
}

func operationalFailure(code string, cause error) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) || errors.Is(cause, context.Canceled) {
		return cause
	}
	return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
}

func isRetryable(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Retryable
}

func failureSummary(stage, fallbackCode string, cause error) *domain.FailureSummary {
	summary := &domain.FailureSummary{Stage: stage, Code: fallbackCode}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		if classified.Code != "" {
			summary.Code = classified.Code
		}
		summary.Retryable = classified.Retryable
	}
	return summary
}

func terminalCoverageError(coverage domain.DetectorCoverage) error {
	if coverage.LastError == nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, errors.New("failed detector coverage has no failure summary"))
	}
	kind := foundation.ErrorNonRetryableFailure
	if coverage.LastError.Retryable {
		kind = foundation.ErrorRetryableFailure
	}
	return foundation.NewError(kind, coverage.LastError.Code, coverage.LastError.Retryable, errors.New("health detector is already failed"))
}

func terminalScanError(scan domain.Scan) error {
	if scan.Status == domain.ScanStatusCancelled {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, "HEALTH_SCAN_CANCELLED", false, context.Canceled)
	}
	if scan.LastError == nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, errors.New("failed health scan has no failure summary"))
	}
	kind := foundation.ErrorNonRetryableFailure
	if scan.LastError.Retryable {
		kind = foundation.ErrorRetryableFailure
	}
	return foundation.NewError(kind, scan.LastError.Code, scan.LastError.Retryable, errors.New("health scan is already failed"))
}

func healthworkflowDecode(raw json.RawMessage) (struct {
	WorkspaceID    foundation.ID
	Scope          domain.ScanScope
	Coverage       []domain.DetectorCoverage
	Fingerprint    string
	RequestHash    string
	MaxItems       int64
	IdempotencyKey string
}, error) {
	// Decode through the shared strict workflow contract without exposing transport types here.
	input, err := healthworkflow.DecodeScanInput(raw)
	if err != nil {
		return struct {
			WorkspaceID    foundation.ID
			Scope          domain.ScanScope
			Coverage       []domain.DetectorCoverage
			Fingerprint    string
			RequestHash    string
			MaxItems       int64
			IdempotencyKey string
		}{}, err
	}
	return struct {
		WorkspaceID    foundation.ID
		Scope          domain.ScanScope
		Coverage       []domain.DetectorCoverage
		Fingerprint    string
		RequestHash    string
		MaxItems       int64
		IdempotencyKey string
	}{input.WorkspaceID, input.Scope, input.Coverage, input.Fingerprint, input.RequestHash, input.MaxItems, input.IdempotencyKey}, nil
}
