package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestSemanticLinkScanExecutorAdvancesMultiplePagesAndReplaysSuccess(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	scan, execution := scanExecutorFixture(t, now, 3)
	firstNode := knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: scanExecutorID(31)}
	lastNode := knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: scanExecutorID(32)}
	source := &scanExecutorPageSource{pages: []graphapp.SemanticLinkTopicScanPage{
		{WorkspaceID: scan.WorkspaceID, ScanID: scan.ID, ScopeVersion: scan.Scope.Version, Cursor: "", NextCursor: "page-2", ProcessedNodes: 2, LastNode: &firstNode},
		{WorkspaceID: scan.WorkspaceID, ScanID: scan.ID, ScopeVersion: scan.Scope.Version, Cursor: "page-2", Complete: true, ProcessedNodes: 1, LastNode: &lastNode},
	}}
	executor, state := newScanExecutorForTest(t, scan, source, now.Add(time.Hour))

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || len(state.progress) != 2 || state.scan.Status != graphdomain.SemanticLinkScanStatusSucceeded {
		t.Fatalf("calls=%d progress=%d scan=%+v", source.calls, len(state.progress), state.scan)
	}
	if state.scan.ProcessedNodes != 3 || state.scan.Checkpoint.ProcessedPage != 2 || state.scan.Checkpoint.Cursor != "" || state.scan.Checkpoint.LastNode == nil || *state.scan.Checkpoint.LastNode != lastNode {
		t.Fatalf("checkpoint=%+v processed=%d", state.scan.Checkpoint, state.scan.ProcessedNodes)
	}
	assertScanExecutionOutput(t, result.Output, scan.ID, graphdomain.SemanticLinkScanStatusSucceeded, 3)

	replayed, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || len(state.progress) != 2 || state.finishCalls != 1 {
		t.Fatalf("terminal replay performed work: calls=%d progress=%d finishes=%d", source.calls, len(state.progress), state.finishCalls)
	}
	if string(replayed.Output) != string(result.Output) {
		t.Fatalf("replay output=%s want=%s", replayed.Output, result.Output)
	}
}

func TestSemanticLinkScanExecutorCompletesEmptyTopic(t *testing.T) {
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	scan, execution := scanExecutorFixture(t, now, 0)
	source := &scanExecutorPageSource{pages: []graphapp.SemanticLinkTopicScanPage{{
		WorkspaceID: scan.WorkspaceID, ScanID: scan.ID, ScopeVersion: scan.Scope.Version, Complete: true,
	}}}
	executor, state := newScanExecutorForTest(t, scan, source, now.Add(time.Hour))

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if state.scan.Status != graphdomain.SemanticLinkScanStatusSucceeded || state.scan.ProcessedNodes != 0 || state.scan.Checkpoint.ProcessedPage != 1 {
		t.Fatalf("empty scan=%+v", state.scan)
	}
	assertScanExecutionOutput(t, result.Output, scan.ID, graphdomain.SemanticLinkScanStatusSucceeded, 0)
}

func TestSemanticLinkScanExecutorRetriesThenPersistsFinalFailure(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	scan, execution := scanExecutorFixture(t, now, 2)
	cause := foundation.NewError(foundation.ErrorDependencyUnavailable, "SEMANTIC_PROVIDER_TIMEOUT", true, errors.New("provider timed out"))
	source := &scanExecutorPageSource{err: cause}
	executor, state := newScanExecutorForTest(t, scan, source, now.Add(time.Hour))

	execution.RetryNo = 2
	if _, err := executor.Execute(context.Background(), execution); !errors.Is(err, cause) {
		t.Fatalf("retry error=%v", err)
	}
	if state.finishCalls != 0 || state.scan.Status != graphdomain.SemanticLinkScanStatusPending {
		t.Fatalf("retry prematurely finished scan=%+v finishes=%d", state.scan, state.finishCalls)
	}

	execution.RetryNo = 3
	if _, err := executor.Execute(context.Background(), execution); !errors.Is(err, cause) {
		t.Fatalf("final error=%v", err)
	}
	if state.finishCalls != 1 || state.scan.Status != graphdomain.SemanticLinkScanStatusFailed || state.scan.LastError == nil {
		t.Fatalf("failed scan=%+v finishes=%d", state.scan, state.finishCalls)
	}
	if state.scan.LastError.Code != "SEMANTIC_PROVIDER_TIMEOUT" || !state.scan.LastError.Retryable {
		t.Fatalf("failure summary=%+v", state.scan.LastError)
	}
}

func TestSemanticLinkScanExecutorRejectsFrozenBindingDrift(t *testing.T) {
	now := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	scan, execution := scanExecutorFixture(t, now, 1)
	source := &scanExecutorPageSource{}
	executor, state := newScanExecutorForTest(t, scan, source, now.Add(time.Hour))

	execution.DefinitionHash = strings.Repeat("f", 64)
	_, err := executor.Execute(context.Background(), execution)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "SEMANTIC_LINK_SCAN_EXECUTION_INVALID" {
		t.Fatalf("binding drift error=%v", err)
	}
	if source.calls != 0 || len(state.progress) != 0 || state.finishCalls != 0 {
		t.Fatalf("binding drift mutated scan: calls=%d progress=%d finishes=%d", source.calls, len(state.progress), state.finishCalls)
	}
}

type scanExecutorState struct {
	scan        graphdomain.SemanticLinkScan
	progress    []graphdomain.SemanticLinkScanProgress
	finishCalls int
}

func (state *scanExecutorState) StartOrReplay(context.Context, graphapp.SemanticLinkScanStartRequest) (graphapp.SemanticLinkScanStartResult, error) {
	return graphapp.SemanticLinkScanStartResult{}, errors.New("start must not be called by worker executor")
}

func (state *scanExecutorState) Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error) {
	return state.scan, nil
}

func (state *scanExecutorState) GetByWorkflowRun(_ context.Context, workspaceID, runID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if state.scan.WorkspaceID != workspaceID || state.scan.WorkflowRunID != runID {
		return graphdomain.SemanticLinkScan{}, errors.New("scan workflow binding not found")
	}
	return state.scan, nil
}

func (state *scanExecutorState) AdvancePage(_ context.Context, progress graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error) {
	if progress.ExpectedVersion != state.scan.Version {
		return graphdomain.SemanticLinkScan{}, errors.New("unexpected scan version")
	}
	state.progress = append(state.progress, progress)
	state.scan.Status = graphdomain.SemanticLinkScanStatusRunning
	state.scan.ProcessedNodes += progress.ProcessedDelta
	state.scan.CandidateCount += progress.CandidateDelta
	state.scan.SuppressedCount += progress.SuppressedDelta
	state.scan.ReopenedCount += progress.ReopenedDelta
	state.scan.FailedCount += progress.FailedDelta
	state.scan.Checkpoint = cloneScanExecutorCheckpoint(progress.Checkpoint)
	state.scan.Version++
	state.scan.UpdatedAt = state.scan.UpdatedAt.Add(time.Second)
	return state.scan, nil
}

func (state *scanExecutorState) Finish(_ context.Context, terminal graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error) {
	if terminal.ExpectedVersion != state.scan.Version {
		return graphdomain.SemanticLinkScan{}, errors.New("unexpected terminal version")
	}
	state.finishCalls++
	state.scan.Status = terminal.Status
	state.scan.LastError = terminal.Error
	state.scan.Version++
	state.scan.UpdatedAt = terminal.At.UTC()
	completedAt := terminal.At.UTC()
	state.scan.CompletedAt = &completedAt
	return state.scan, nil
}

type scanExecutorPageSource struct {
	pages []graphapp.SemanticLinkTopicScanPage
	err   error
	calls int
}

func (source *scanExecutorPageSource) LoadPage(_ context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	source.calls++
	if source.err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, source.err
	}
	if source.calls > len(source.pages) {
		return graphapp.SemanticLinkTopicScanPage{}, errors.New("unexpected page request")
	}
	page := source.pages[source.calls-1]
	if page.Cursor != request.Cursor {
		return graphapp.SemanticLinkTopicScanPage{}, fmt.Errorf("cursor=%q want=%q", request.Cursor, page.Cursor)
	}
	return page, nil
}

func newScanExecutorForTest(t *testing.T, scan graphdomain.SemanticLinkScan, source *scanExecutorPageSource, clock time.Time) (*SemanticLinkScanExecutor, *scanExecutorState) {
	t.Helper()
	state := &scanExecutorState{scan: scan}
	scans, err := graphapp.NewSemanticLinkScanStateService(state)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := graphapp.NewSemanticLinkTopicScanExecutor(source, graphapp.NewSemanticLinkDiscoveryService(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewSemanticLinkScanExecutor(state, scans, pages, foundation.FixedClock{Value: clock})
	if err != nil {
		t.Fatal(err)
	}
	return executor, state
}

func scanExecutorFixture(t *testing.T, now time.Time, totalNodes int64) (graphdomain.SemanticLinkScan, workflowapplication.ExecutionContext) {
	t.Helper()
	workspaceID := scanExecutorID(1)
	scanID := scanExecutorID(2)
	runID := scanExecutorID(3)
	topicID := scanExecutorID(4)
	fingerprint := strings.Repeat("a", 64)
	requestHash := strings.Repeat("b", 64)
	scope := graphdomain.SemanticLinkScanScope{
		Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(topicID), Version: 7,
		SchemaVersion: graphapp.SemanticLinkTopicScanScopeSchemaVersion,
	}
	request := graphapp.SemanticLinkScanStartRequest{
		WorkspaceID: workspaceID, Scope: scope, Generation: scanExecutorGeneration(), Fingerprint: fingerprint,
		RequestHash: requestHash, TotalNodes: totalNodes, IdempotencyKey: "scan-executor-test",
		WorkflowDefinitionKey:      graphapp.SemanticLinkScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion:  graphapp.SemanticLinkScanWorkflowDefinitionVersion,
		WorkflowInputSchemaVersion: graphapp.SemanticLinkScanInputSchemaVersion,
	}
	input, err := graphapp.EncodeSemanticLinkScanWorkflowInput(request)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := graphapp.RegisteredSemanticLinkScanDefinition()
	if err != nil {
		t.Fatal(err)
	}
	scan := graphdomain.SemanticLinkScan{
		ID: scanID, WorkspaceID: workspaceID, Scope: scope, Fingerprint: fingerprint,
		IdempotencyKey: request.IdempotencyKey, RequestHash: requestHash, WorkflowRunID: runID,
		Status: graphdomain.SemanticLinkScanStatusPending, TotalNodes: totalNodes, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	execution := workflowapplication.ExecutionContext{
		WorkspaceID: workspaceID, DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
		RunID: runID, NodeKey: graphapp.SemanticLinkScanNodeKey, NodeKind: graphapp.SemanticLinkScanNodeKind,
		InputSchemaVersion: graphapp.SemanticLinkScanInputSchemaVersion, Input: input,
	}
	return scan, execution
}

func scanExecutorGeneration() graphdomain.SemanticLinkScanGeneration {
	ruleID := graphapp.SemanticLinkScanRuleID
	return graphdomain.SemanticLinkScanGeneration{
		Rule:            graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: graphapp.SemanticLinkScanRuleVersion},
		WorkflowVersion: graphapp.SemanticLinkScanWorkflowGenerationVersion,
	}
}

func scanExecutorID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}

func cloneScanExecutorCheckpoint(checkpoint graphdomain.SemanticLinkScanCheckpoint) graphdomain.SemanticLinkScanCheckpoint {
	if checkpoint.LastNode != nil {
		lastNode := *checkpoint.LastNode
		checkpoint.LastNode = &lastNode
	}
	return checkpoint
}

func assertScanExecutionOutput(t *testing.T, raw json.RawMessage, scanID foundation.ID, status graphdomain.SemanticLinkScanStatus, processed int64) {
	t.Helper()
	var output struct {
		ScanID         foundation.ID                      `json:"scan_id"`
		Status         graphdomain.SemanticLinkScanStatus `json:"status"`
		ProcessedCount int64                              `json:"processed_count"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	if output.ScanID != scanID || output.Status != status || output.ProcessedCount != processed {
		t.Fatalf("output=%+v", output)
	}
}
