package postgres

import (
	"context"
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	gormWorkspaceAnalysisToolRequestedEvent = "workspace_analysis.tool_requested"
	gormWorkspaceAnalysisToolCompletedEvent = "workspace_analysis.tool_completed"
)

func (repository *GORMWorkspaceAnalysisRepository) appendRequested(ctx context.Context, scope foundation.TransactionScope, operation agentdomain.WorkspaceAnalysisOperation, call domain.ToolCall, replay bool) error {
	return repository.appendEvent(ctx, scope, operation, call, gormWorkspaceAnalysisToolRequestedEvent, "started", call.StartedAt, replay)
}

func (repository *GORMWorkspaceAnalysisRepository) appendCompleted(ctx context.Context, scope foundation.TransactionScope, operation agentdomain.WorkspaceAnalysisOperation, call domain.ToolCall, status string, replay bool) error {
	if call.CompletedAt == nil {
		return consistency(errors.New("Workspace Analysis Tool completion is missing its timestamp"))
	}
	return repository.appendEvent(ctx, scope, operation, call, gormWorkspaceAnalysisToolCompletedEvent, status, *call.CompletedAt, replay)
}

func (repository *GORMWorkspaceAnalysisRepository) appendEvent(ctx context.Context, scope foundation.TransactionScope, operation agentdomain.WorkspaceAnalysisOperation, call domain.ToolCall, eventType, status string, occurredAt time.Time, replay bool) error {
	if repository == nil || nilGORMToolsDependency(repository.events) || nilGORMToolsDependency(scope) {
		return gormToolsUnavailable(errors.New("Workspace Analysis Tool event scope or appender is unavailable"))
	}
	if operation.ID == "" || operation.AnalysisRunID == "" || operation.NodeKey == "" || call.WorkspaceID == "" || call.WorkflowRunID == "" || occurredAt.IsZero() {
		return consistency(errors.New("Workspace Analysis Tool event binding is incomplete"))
	}
	if (eventType == gormWorkspaceAnalysisToolRequestedEvent && status != "started") ||
		(eventType == gormWorkspaceAnalysisToolCompletedEvent && status != "succeeded" && status != "failed" && status != "unknown") {
		return consistency(errors.New("Workspace Analysis Tool event status is invalid"))
	}
	workflowRunID := call.WorkflowRunID
	_, replayed, err := repository.events.AppendScoped(ctx, scope, eventsdomain.AppendRequest{
		WorkspaceID: call.WorkspaceID, WorkflowRunID: &workflowRunID, Type: eventType,
		ResourceRef: "workspace_analysis:" + string(operation.AnalysisRunID), ResourceVersion: 1,
		PayloadSummary: eventsdomain.PayloadSummary{WorkflowRunID: &workflowRunID, Stage: string(operation.NodeKey), Status: status},
		SchemaVersion:  1, SourceEventRef: eventType + ":" + string(operation.ID) + ":v1", OccurredAt: occurredAt.UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		return err
	}
	if replayed != replay {
		return consistency(errors.New("Workspace Analysis Tool event replay differs"))
	}
	return nil
}

func gormWorkspaceAnalysisTerminalEventStatus(call domain.ToolCall) (string, error) {
	switch call.Status {
	case domain.CallSucceeded:
		return "succeeded", nil
	case domain.CallFailed:
		return "failed", nil
	case domain.CallUnknown:
		return "unknown", nil
	default:
		return "", consistency(errors.New("Workspace Analysis Tool terminal status is unsupported"))
	}
}
