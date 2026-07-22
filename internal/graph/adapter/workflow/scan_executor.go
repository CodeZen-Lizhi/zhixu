package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const maxSemanticLinkScanPagesPerDelivery = 10_000

// SemanticLinkScanLookup 通过 Workflow Run 恢复唯一 Scan 业务事实。
type SemanticLinkScanLookup interface {
	GetByWorkflowRun(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error)
}

// SemanticLinkScanExecutor 在一个 River delivery 内按页推进 durable Scan checkpoint。
type SemanticLinkScanExecutor struct {
	lookup SemanticLinkScanLookup
	scans  *graphapp.SemanticLinkScanService
	pages  *graphapp.SemanticLinkTopicScanExecutor
	clock  foundation.Clock
}

var _ workflowapplication.Executor = (*SemanticLinkScanExecutor)(nil)

// NewSemanticLinkScanExecutor 创建 Topic scan Runtime Executor。
func NewSemanticLinkScanExecutor(lookup SemanticLinkScanLookup, scans *graphapp.SemanticLinkScanService, pages *graphapp.SemanticLinkTopicScanExecutor, clock foundation.Clock) (*SemanticLinkScanExecutor, error) {
	if lookup == nil || scans == nil || pages == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "SEMANTIC_LINK_SCAN_EXECUTOR_DEPENDENCY_MISSING", false, errors.New("semantic link scan executor dependencies are missing"))
	}
	return &SemanticLinkScanExecutor{lookup: lookup, scans: scans, pages: pages, clock: clock}, nil
}

// Execute 恢复 Scan checkpoint，逐页发现/持久化 Candidate，并写入成功或最终失败终态。
func (executor *SemanticLinkScanExecutor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	if executor == nil || executor.lookup == nil || executor.scans == nil || executor.pages == nil || executor.clock == nil {
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "SEMANTIC_LINK_SCAN_EXECUTOR_UNAVAILABLE", false, errors.New("semantic link scan executor is unavailable"))
	}
	if ctx == nil || execution.NodeKind != graphapp.SemanticLinkScanNodeKind || execution.NodeKey != graphapp.SemanticLinkScanNodeKey ||
		(execution.InputSchemaVersion != graphapp.SemanticLinkScanInputSchemaVersion && execution.InputSchemaVersion != graphapp.SemanticLinkSmartCollectionScanInputSchemaVersion) {
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorInvalidInput, "SEMANTIC_LINK_SCAN_EXECUTION_INVALID", false, errors.New("semantic link scan execution binding is invalid"))
	}
	input, err := decodeSemanticLinkScanExecutionInput(execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if input.workspaceID != execution.WorkspaceID || execution.DefinitionVersion != input.definitionVersion || execution.DefinitionHash != input.definitionHash {
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SEMANTIC_LINK_SCAN_EXECUTION_INVALID", false, errors.New("semantic link scan workflow identity drifted"))
	}
	scan, err := executor.lookup.GetByWorkflowRun(ctx, execution.WorkspaceID, execution.RunID)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateExecutionScanBinding(scan, input, execution); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if scan.Status == graphdomain.SemanticLinkScanStatusSucceeded {
		return semanticLinkScanExecutionResult(scan)
	}
	if scan.Status == graphdomain.SemanticLinkScanStatusFailed || scan.Status == graphdomain.SemanticLinkScanStatusCancelled {
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "SEMANTIC_LINK_SCAN_ALREADY_TERMINAL", false, errors.New("semantic link scan is already terminal"))
	}

	for pageNo := 0; pageNo < maxSemanticLinkScanPagesPerDelivery; pageNo++ {
		pageRequest := graphapp.SemanticLinkTopicScanPageRequest{
			WorkspaceID: execution.WorkspaceID, ScanID: scan.ID,
			Scope: input.scope, Generation: input.generation, Cursor: scan.Checkpoint.Cursor,
			Limit: graphapp.MaxSemanticLinkScanPageNodes,
		}
		if input.scope.Type == graphdomain.SemanticLinkScanScopeSmartCollection {
			pageRequest.Cursor = ""
			pageRequest.LastNode = scan.Checkpoint.LastNode
			pageRequest.TotalNodes = scan.TotalNodes
		}
		page, pageErr := executor.pages.ProcessPage(ctx, pageRequest)
		if pageErr != nil {
			return workflowapplication.ExecutionResult{}, executor.failIfTerminalRetry(ctx, execution, scan, pageErr)
		}
		scan, err = executor.scans.AdvancePage(ctx, graphdomain.SemanticLinkScanProgress{
			ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version,
			Checkpoint: graphdomain.SemanticLinkScanCheckpoint{
				Cursor: page.NextCursor, ProcessedPage: scan.Checkpoint.ProcessedPage + 1, LastNode: page.LastNode,
			},
			ProcessedDelta:  page.ProcessedNodes,
			CandidateDelta:  page.Candidates.Created + page.Candidates.Reopened,
			SuppressedDelta: page.Candidates.Suppressed, ReopenedDelta: page.Candidates.Reopened,
			FailedDelta: page.Candidates.Failed,
		})
		if err != nil {
			return workflowapplication.ExecutionResult{}, executor.failIfTerminalRetry(ctx, execution, scan, err)
		}
		if page.Complete {
			finishedAt := executor.clock.Now().UTC()
			if !finishedAt.After(scan.UpdatedAt) {
				finishedAt = scan.UpdatedAt.Add(time.Nanosecond)
			}
			scan, err = executor.scans.Finish(ctx, graphdomain.SemanticLinkScanTerminal{
				ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version,
				Status: graphdomain.SemanticLinkScanStatusSucceeded, At: finishedAt,
			})
			if err != nil {
				return workflowapplication.ExecutionResult{}, err
			}
			return semanticLinkScanExecutionResult(scan)
		}
	}
	return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorManualRecoveryRequired, "SEMANTIC_LINK_SCAN_PAGE_LIMIT_EXCEEDED", false, errors.New("semantic link scan exceeded the bounded page limit"))
}

type semanticLinkScanExecutionInput struct {
	workspaceID       foundation.ID
	scope             graphdomain.SemanticLinkScanScope
	generation        graphdomain.SemanticLinkScanGeneration
	fingerprint       string
	requestHash       string
	idempotencyKey    string
	definitionVersion int64
	definitionHash    string
}

func decodeSemanticLinkScanExecutionInput(execution workflowapplication.ExecutionContext) (semanticLinkScanExecutionInput, error) {
	switch execution.InputSchemaVersion {
	case graphapp.SemanticLinkScanInputSchemaVersion:
		input, err := graphapp.DecodeSemanticLinkScanWorkflowInput(execution.Input)
		if err != nil {
			return semanticLinkScanExecutionInput{}, err
		}
		definition, err := graphapp.RegisteredSemanticLinkScanDefinition()
		if err != nil {
			return semanticLinkScanExecutionInput{}, err
		}
		return semanticLinkScanExecutionInput{
			workspaceID: input.WorkspaceID, scope: input.Scope(), generation: input.Generation(),
			fingerprint: input.Fingerprint, requestHash: input.RequestHash, idempotencyKey: input.IdempotencyKey,
			definitionVersion: definition.Version, definitionHash: definition.GraphHash,
		}, nil
	case graphapp.SemanticLinkSmartCollectionScanInputSchemaVersion:
		input, err := graphapp.DecodeSemanticLinkSmartCollectionScanWorkflowInput(execution.Input)
		if err != nil {
			return semanticLinkScanExecutionInput{}, err
		}
		definition, err := graphapp.RegisteredSemanticLinkSmartCollectionScanDefinition()
		if err != nil {
			return semanticLinkScanExecutionInput{}, err
		}
		return semanticLinkScanExecutionInput{
			workspaceID: input.WorkspaceID, scope: input.Scope(), generation: input.Generation(),
			fingerprint: input.Fingerprint, requestHash: input.RequestHash, idempotencyKey: input.IdempotencyKey,
			definitionVersion: definition.Version, definitionHash: definition.GraphHash,
		}, nil
	default:
		return semanticLinkScanExecutionInput{}, foundation.NewError(foundation.ErrorInvalidInput, "SEMANTIC_LINK_SCAN_EXECUTION_INVALID", false, errors.New("semantic link scan input schema is unsupported"))
	}
}

func (executor *SemanticLinkScanExecutor) failIfTerminalRetry(ctx context.Context, execution workflowapplication.ExecutionContext, scan graphdomain.SemanticLinkScan, cause error) error {
	var classified *foundation.Error
	nonRetryable := errors.As(cause, &classified) && !classified.Retryable
	if !nonRetryable && execution.RetryNo < 3 {
		return cause
	}
	code := "SEMANTIC_LINK_SCAN_FAILED"
	retryable := false
	if classified != nil {
		code, retryable = classified.Code, classified.Retryable
	}
	failedAt := executor.clock.Now().UTC()
	if !failedAt.After(scan.UpdatedAt) {
		failedAt = scan.UpdatedAt.Add(time.Nanosecond)
	}
	_, finishErr := executor.scans.Finish(context.WithoutCancel(ctx), graphdomain.SemanticLinkScanTerminal{
		ScanID: scan.ID, WorkspaceID: scan.WorkspaceID, ExpectedVersion: scan.Version,
		Status: graphdomain.SemanticLinkScanStatusFailed,
		Error:  &graphdomain.SemanticLinkScanError{Stage: "discover", Code: code, Retryable: retryable}, At: failedAt,
	})
	return errors.Join(cause, finishErr)
}

func validateExecutionScanBinding(scan graphdomain.SemanticLinkScan, input semanticLinkScanExecutionInput, execution workflowapplication.ExecutionContext) error {
	if scan.WorkspaceID != execution.WorkspaceID || scan.WorkflowRunID != execution.RunID || scan.Scope != input.scope ||
		scan.Fingerprint != input.fingerprint || scan.RequestHash != input.requestHash || scan.IdempotencyKey != input.idempotencyKey {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "SEMANTIC_LINK_SCAN_EXECUTION_BINDING_INVALID", false, errors.New("semantic link scan does not match workflow input"))
	}
	return nil
}

func semanticLinkScanExecutionResult(scan graphdomain.SemanticLinkScan) (workflowapplication.ExecutionResult, error) {
	output, err := json.Marshal(struct {
		ScanID          foundation.ID                      `json:"scan_id"`
		Status          graphdomain.SemanticLinkScanStatus `json:"status"`
		ProcessedCount  int64                              `json:"processed_count"`
		CandidateCount  int64                              `json:"candidate_count"`
		SuppressedCount int64                              `json:"suppressed_count"`
		ReopenedCount   int64                              `json:"reopened_count"`
		FailedCount     int64                              `json:"failed_count"`
	}{scan.ID, scan.Status, scan.ProcessedNodes, scan.CandidateCount, scan.SuppressedCount, scan.ReopenedCount, scan.FailedCount})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}
