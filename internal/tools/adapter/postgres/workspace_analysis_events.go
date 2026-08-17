package postgres

import (
	"context"
	"errors"
	"time"

	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

const (
	workspaceAnalysisToolRequestedEvent = "workspace_analysis.tool_requested"
	workspaceAnalysisToolCompletedEvent = "workspace_analysis.tool_completed"
)

// appendWorkspaceAnalysisToolRequested records the durable tool admission from
// the database-assigned STARTED timestamp. A legacy repository has no event
// appender and intentionally retains its historical behavior.
func (repository *Repository) appendWorkspaceAnalysisToolRequested(
	ctx context.Context,
	tx pgx.Tx,
	operation receiptOperation,
	call domain.ToolCall,
	wantReplay bool,
) error {
	return repository.appendWorkspaceAnalysisToolEvent(
		ctx, tx, operation, call, workspaceAnalysisToolRequestedEvent, "started", call.StartedAt, wantReplay,
	)
}

// appendWorkspaceAnalysisToolCompleted records the persisted terminal operation
// closure. The caller supplies the operation status because receipt-invalid
// closures have a successful Tool Call but a failed logical operation.
func (repository *Repository) appendWorkspaceAnalysisToolCompleted(
	ctx context.Context,
	tx pgx.Tx,
	operation receiptOperation,
	call domain.ToolCall,
	status string,
	wantReplay bool,
) error {
	if call.CompletedAt == nil {
		return consistency(errors.New("workspace analysis terminal tool call has no completion time"))
	}
	return repository.appendWorkspaceAnalysisToolEvent(
		ctx, tx, operation, call, workspaceAnalysisToolCompletedEvent, status, *call.CompletedAt, wantReplay,
	)
}

func (repository *Repository) appendWorkspaceAnalysisToolEvent(
	ctx context.Context,
	tx pgx.Tx,
	operation receiptOperation,
	call domain.ToolCall,
	eventType string,
	status string,
	occurredAt time.Time,
	wantReplay bool,
) error {
	if repository == nil || isNilWorkspaceAnalysisEventAppender(repository.events) {
		return nil
	}
	if operation.id == "" || operation.analysisRunID == "" || operation.nodeKey == "" ||
		call.WorkspaceID == "" || call.WorkflowRunID == "" || occurredAt.IsZero() {
		return consistency(errors.New("workspace analysis tool event binding is incomplete"))
	}
	if !validWorkspaceAnalysisToolEventStatus(eventType, status) {
		return consistency(errors.New("workspace analysis tool event status is invalid"))
	}
	workflowRunID := call.WorkflowRunID
	sourceEventRef := eventType + ":" + string(operation.id) + ":v1"
	_, replayed, err := repository.events.AppendTx(ctx, tx, eventsdomain.AppendRequest{
		WorkspaceID: call.WorkspaceID, WorkflowRunID: &workflowRunID,
		Type: eventType, ResourceRef: "workspace_analysis:" + string(operation.analysisRunID), ResourceVersion: 1,
		PayloadSummary: eventsdomain.PayloadSummary{WorkflowRunID: &workflowRunID, Stage: operation.nodeKey, Status: status},
		SchemaVersion:  1, SourceEventRef: sourceEventRef, OccurredAt: occurredAt,
	})
	if err != nil {
		return err
	}
	if replayed != wantReplay {
		return consistency(errors.New("workspace analysis tool event replay differs"))
	}
	return nil
}

func validWorkspaceAnalysisToolEventStatus(eventType, status string) bool {
	switch eventType {
	case workspaceAnalysisToolRequestedEvent:
		return status == "started"
	case workspaceAnalysisToolCompletedEvent:
		return status == "succeeded" || status == "failed" || status == "unknown"
	default:
		return false
	}
}

func workspaceAnalysisToolTerminalEventStatus(call domain.ToolCall) (string, error) {
	switch call.Status {
	case domain.CallSucceeded:
		return "succeeded", nil
	case domain.CallFailed:
		return "failed", nil
	case domain.CallUnknown:
		return "unknown", nil
	default:
		return "", consistency(errors.New("workspace analysis terminal tool status is unsupported"))
	}
}
