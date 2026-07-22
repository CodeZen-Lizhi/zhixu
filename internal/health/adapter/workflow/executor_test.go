package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	healthworkflow "github.com/CodeZen-Lizhi/zhixu/internal/health/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestHealthScanExecutorReconcilesPageAndResolvesOnlySucceededScan(t *testing.T) {
	workspaceID := workflowTestID(1)
	scan := workflowScan(workspaceID, domain.DetectorCoverageStatusPending)
	state := &workflowScanState{scan: scan}
	scans, err := healthapp.NewScanStateService(state)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := healthapp.Descriptor{ID: "health.detector.test", Version: "detector/v1", IssueType: domain.IssueTypeLowConfidence, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeClaim}, DefaultSeverity: domain.SeverityLow}
	registry, err := healthapp.NewRegistry([]healthapp.Detector{workflowDetector{descriptor: descriptor, page: healthapp.Page{Findings: []healthapp.FindingFact{{Target: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: workflowTestID(5)}, TargetVersion: 1, Summary: "claim confidence is low"}}, Complete: true, Processed: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &workflowPageStore{}
	executor, err := NewHealthScanExecutor(registry, scans, store, foundation.FixedClock{Value: time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := healthworkflow.RegisteredDefinition()
	input, err := healthworkflow.EncodeScanInput(workflowStartRequest(scan))
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), workflowapp.ExecutionContext{WorkspaceID: workspaceID, RunID: scan.WorkflowRunID, NodeKind: healthapp.HealthScanNodeKind, NodeKey: healthapp.HealthScanNodeKey, InputSchemaVersion: healthapp.HealthScanInputSchemaVersion, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, Input: input})
	if err != nil || len(result.Output) == 0 || store.reconciles != 1 || store.resolves != 1 || state.advanceCalls != 2 || state.scan.Status != domain.ScanStatusSucceeded || state.scan.Counters.Resolved != 1 {
		t.Fatalf("result=%s scan=%#v reconciles=%d resolves=%d err=%v", result.Output, state.scan, store.reconciles, store.resolves, err)
	}
	if checkpoint := state.scan.Coverage[0].Checkpoint; checkpoint.LastItem == nil || checkpoint.LastItem.ID != workflowTestID(5) {
		t.Fatalf("checkpoint=%#v", checkpoint)
	}
}

func TestHealthScanExecutorUnavailableCoverageFinishesPartialWithoutResolve(t *testing.T) {
	workspaceID := workflowTestID(2)
	scan := workflowScan(workspaceID, domain.DetectorCoverageStatusUnavailable)
	scan.Coverage[0].UnavailableReason = "owner is unavailable"
	state := &workflowScanState{scan: scan}
	scans, _ := healthapp.NewScanStateService(state)
	registry, _ := healthapp.NewRegistry(nil, []healthapp.Descriptor{{ID: "health.detector.test", Version: "detector/v1", IssueType: domain.IssueTypeReviewInvalidated, UnavailableReason: "owner is unavailable"}})
	store := &workflowPageStore{}
	executor, _ := NewHealthScanExecutor(registry, scans, store, foundation.FixedClock{Value: time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)})
	definition, _ := healthworkflow.RegisteredDefinition()
	request := workflowStartRequest(scan)
	request.Coverage[0].UnavailableReason = "owner is unavailable"
	input, _ := healthworkflow.EncodeScanInput(request)
	_, err := executor.Execute(context.Background(), workflowapp.ExecutionContext{WorkspaceID: workspaceID, RunID: scan.WorkflowRunID, NodeKind: healthapp.HealthScanNodeKind, NodeKey: healthapp.HealthScanNodeKey, InputSchemaVersion: 1, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, Input: input})
	if err != nil || state.scan.Status != domain.ScanStatusPartial || store.resolves != 0 {
		t.Fatalf("scan=%#v resolves=%d err=%v", state.scan, store.resolves, err)
	}
}

func TestHealthScanExecutorRejectsInconsistentPageCounters(t *testing.T) {
	workspaceID := workflowTestID(6)
	scan := workflowScan(workspaceID, domain.DetectorCoverageStatusPending)
	state := &workflowScanState{scan: scan}
	scans, _ := healthapp.NewScanStateService(state)
	descriptor := healthapp.Descriptor{ID: "health.detector.test", Version: "detector/v1", IssueType: domain.IssueTypeLowConfidence, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeClaim}, DefaultSeverity: domain.SeverityLow}
	registry, _ := healthapp.NewRegistry([]healthapp.Detector{workflowDetector{descriptor: descriptor, page: healthapp.Page{Findings: []healthapp.FindingFact{{Target: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: workflowTestID(7)}, TargetVersion: 1, Summary: "claim confidence is low"}}, Complete: true, Processed: 2}}}, nil)
	executor, _ := NewHealthScanExecutor(registry, scans, &workflowPageStore{}, foundation.FixedClock{Value: time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)})
	definition, _ := healthworkflow.RegisteredDefinition()
	input, _ := healthworkflow.EncodeScanInput(workflowStartRequest(scan))
	result, err := executor.Execute(context.Background(), workflowapp.ExecutionContext{WorkspaceID: workspaceID, RunID: scan.WorkflowRunID, NodeKind: healthapp.HealthScanNodeKind, NodeKey: healthapp.HealthScanNodeKey, InputSchemaVersion: 1, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, Input: input})
	if err == nil {
		t.Fatal("expected page counter validation error")
	}
	if len(result.Output) != 0 || state.scan.Status != domain.ScanStatusFailed || state.scan.LastError == nil || state.scan.LastError.Code != "HEALTH_DETECTOR_PAGE_INVALID" {
		t.Fatalf("result=%s scan=%#v err=%v", result.Output, state.scan, err)
	}
}

func TestHealthScanExecutorClassifiesRawFailuresAndPersistsRetryExhaustion(t *testing.T) {
	workspaceID := workflowTestID(8)
	scan := workflowScan(workspaceID, domain.DetectorCoverageStatusPending)
	state := &workflowScanState{scan: scan}
	scans, _ := healthapp.NewScanStateService(state)
	descriptor := healthapp.Descriptor{ID: "health.detector.test", Version: "detector/v1", IssueType: domain.IssueTypeLowConfidence, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeClaim}, DefaultSeverity: domain.SeverityLow}
	registry, _ := healthapp.NewRegistry([]healthapp.Detector{workflowDetector{descriptor: descriptor, err: errors.New("raw postgres failure")}}, nil)
	executor, _ := NewHealthScanExecutor(registry, scans, &workflowPageStore{}, foundation.FixedClock{Value: time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)})
	definition, _ := healthworkflow.RegisteredDefinition()
	input, _ := healthworkflow.EncodeScanInput(workflowStartRequest(scan))
	execution := workflowapp.ExecutionContext{WorkspaceID: workspaceID, RunID: scan.WorkflowRunID, NodeKind: healthapp.HealthScanNodeKind, NodeKey: healthapp.HealthScanNodeKey, InputSchemaVersion: 1, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, Input: input}

	if _, err := executor.Execute(context.Background(), execution); !workflowError(err, foundation.ErrorRetryableFailure, "HEALTH_DETECTOR_PAGE_FAILED", true) {
		t.Fatalf("first failure=%v", err)
	}
	if state.scan.Status != domain.ScanStatusRunning || state.scan.Coverage[0].Status != domain.DetectorCoverageStatusRunning || state.advanceCalls != 1 || state.finishCalls != 0 {
		t.Fatalf("retryable failure mutated scan: %#v", state.scan)
	}

	execution.RetryNo = definition.Graph.Nodes[0].RetryPolicy.MaxRetries
	result, err := executor.Execute(context.Background(), execution)
	if !workflowError(err, foundation.ErrorRetryableFailure, "HEALTH_DETECTOR_PAGE_FAILED", true) {
		t.Fatalf("exhausted failure=%v", err)
	}
	if len(result.Output) != 0 || state.scan.Status != domain.ScanStatusFailed || state.scan.LastError == nil || !state.scan.LastError.Retryable || state.advanceCalls != 2 || state.finishCalls != 1 {
		t.Fatalf("result=%s scan=%#v advance=%d finish=%d", result.Output, state.scan, state.advanceCalls, state.finishCalls)
	}
}

func TestHealthScanExecutorTerminalFailureReplayCannotSucceed(t *testing.T) {
	workspaceID := workflowTestID(9)
	scan := workflowScan(workspaceID, domain.DetectorCoverageStatusPending)
	request := workflowStartRequest(scan)
	request.Coverage = append([]domain.DetectorCoverage(nil), request.Coverage...)
	scan.Status = domain.ScanStatusFailed
	scan.Coverage[0].Status = domain.DetectorCoverageStatusFailed
	scan.Coverage[0].LastError = &domain.FailureSummary{Stage: "detector", Code: "HEALTH_DETECTOR_PAGE_FAILED", Retryable: true}
	scan.LastError = scan.Coverage[0].LastError
	completed := scan.UpdatedAt.Add(time.Minute)
	scan.CompletedAt = &completed
	state := &workflowScanState{scan: scan}
	scans, _ := healthapp.NewScanStateService(state)
	descriptor := healthapp.Descriptor{ID: "health.detector.test", Version: "detector/v1", IssueType: domain.IssueTypeLowConfidence, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeClaim}, DefaultSeverity: domain.SeverityLow}
	registry, _ := healthapp.NewRegistry([]healthapp.Detector{workflowDetector{descriptor: descriptor}}, nil)
	executor, _ := NewHealthScanExecutor(registry, scans, &workflowPageStore{}, foundation.FixedClock{Value: completed})
	definition, _ := healthworkflow.RegisteredDefinition()
	input, _ := healthworkflow.EncodeScanInput(request)

	result, err := executor.Execute(context.Background(), workflowapp.ExecutionContext{WorkspaceID: workspaceID, RunID: scan.WorkflowRunID, NodeKind: healthapp.HealthScanNodeKind, NodeKey: healthapp.HealthScanNodeKey, InputSchemaVersion: 1, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, Input: input, RetryNo: definition.Graph.Nodes[0].RetryPolicy.MaxRetries})
	if !workflowError(err, foundation.ErrorRetryableFailure, "HEALTH_DETECTOR_PAGE_FAILED", true) || len(result.Output) != 0 {
		t.Fatalf("result=%s err=%v", result.Output, err)
	}
}

type workflowDetector struct {
	descriptor healthapp.Descriptor
	page       healthapp.Page
	err        error
}

func (detector workflowDetector) Descriptor() healthapp.Descriptor { return detector.descriptor }
func (detector workflowDetector) ScanPage(context.Context, healthapp.PageRequest) (healthapp.Page, error) {
	return detector.page, detector.err
}

type workflowPageStore struct{ reconciles, resolves int }

func (store *workflowPageStore) ReconcileDetectorPage(_ context.Context, request healthapp.DetectorPageReconcileRequest) (healthapp.DetectorPageReconcileResult, error) {
	store.reconciles++
	return healthapp.DetectorPageReconcileResult{Created: int64(len(request.Observations))}, nil
}
func (store *workflowPageStore) ResolveMissingForCompleteScan(context.Context, foundation.ID, foundation.ID, string) (int64, error) {
	store.resolves++
	return 1, nil
}

type workflowScanState struct {
	scan                      domain.Scan
	advanceCalls, finishCalls int
}

func (state *workflowScanState) Get(context.Context, foundation.ID, foundation.ID) (domain.Scan, error) {
	return state.scan, nil
}
func (state *workflowScanState) GetByWorkflowRun(context.Context, foundation.ID, foundation.ID) (domain.Scan, error) {
	if state.scan.Status == domain.ScanStatusRunning && domain.CoverageComplete(state.scan.Coverage) && state.scan.Counters.Resolved == 0 {
		state.scan.Counters.Resolved = 1
		state.scan.Coverage[0].Counters.Resolved = 1
		state.scan.Version++
	}
	return state.scan, nil
}
func (state *workflowScanState) Advance(_ context.Context, progress healthapp.ScanProgress) (domain.Scan, error) {
	state.advanceCalls++
	state.scan.Status = domain.ScanStatusRunning
	state.scan.Version++
	state.scan.Checkpoint = progress.Checkpoint
	for index := range state.scan.Coverage {
		if state.scan.Coverage[index].DetectorID == progress.DetectorID {
			state.scan.Coverage[index].Status = progress.Status
			state.scan.Coverage[index].Checkpoint = progress.Checkpoint
			state.scan.Coverage[index].LastError = progress.LastError
			state.scan.Coverage[index].Counters.Processed += progress.CountersDelta.Processed
			state.scan.Coverage[index].Counters.Created += progress.CountersDelta.Created
			state.scan.Coverage[index].Counters.Reopened += progress.CountersDelta.Reopened
			state.scan.Coverage[index].Counters.Resolved += progress.CountersDelta.Resolved
			state.scan.Coverage[index].Counters.Unchanged += progress.CountersDelta.Unchanged
			state.scan.Coverage[index].Counters.Failed += progress.CountersDelta.Failed
		}
	}
	state.scan.Counters.Processed += progress.CountersDelta.Processed
	state.scan.Counters.Created += progress.CountersDelta.Created
	state.scan.Counters.Reopened += progress.CountersDelta.Reopened
	state.scan.Counters.Resolved += progress.CountersDelta.Resolved
	state.scan.Counters.Unchanged += progress.CountersDelta.Unchanged
	state.scan.Counters.Failed += progress.CountersDelta.Failed
	return state.scan, nil
}
func (state *workflowScanState) Finish(_ context.Context, terminal healthapp.ScanTerminal) (domain.Scan, error) {
	state.finishCalls++
	state.scan.Status, state.scan.LastError = terminal.Status, terminal.LastError
	state.scan.Version++
	completed := state.scan.UpdatedAt.Add(time.Minute)
	state.scan.CompletedAt = &completed
	return state.scan, nil
}

func workflowScan(workspaceID foundation.ID, coverageStatus domain.DetectorCoverageStatus) domain.Scan {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	return domain.Scan{ID: workflowTestID(3), WorkspaceID: workspaceID, WorkflowRunID: workflowTestID(4), Scope: domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"}, Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", IdempotencyKey: "scan", MaxItems: 100, Status: domain.ScanStatusPending, Coverage: []domain.DetectorCoverage{{DetectorID: "health.detector.test", DetectorVersion: "detector/v1", Status: coverageStatus}}, Version: 1, CreatedAt: now, UpdatedAt: now}
}

func workflowStartRequest(scan domain.Scan) healthapp.ScanStartRequest {
	return healthapp.ScanStartRequest{WorkspaceID: scan.WorkspaceID, Scope: scan.Scope, Coverage: scan.Coverage, Fingerprint: scan.Fingerprint, RequestHash: scan.RequestHash, MaxItems: scan.MaxItems, IdempotencyKey: scan.IdempotencyKey, WorkflowDefinitionKey: healthapp.HealthScanWorkflowDefinitionKey, WorkflowDefinitionVersion: 1, WorkflowInputSchemaVersion: 1}
}

func workflowTestID(value byte) foundation.ID {
	const digits = "0123456789abcdef"
	return foundation.ID("30000000-0000-4000-8000-0000000000" + string([]byte{digits[value>>4], digits[value&15]}))
}

func workflowError(err error, kind foundation.ErrorKind, code string, retryable bool) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code && classified.Retryable == retryable
}
