package postgres

import (
	"errors"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"strings"
	"time"
)

type workspaceAnalysisCancellationRuntime struct {
	WorkflowStatus            workflowdomain.RunStatus
	WorkflowCancelRequestedAt *time.Time
	WorkflowCompletedAt       *time.Time
	NodeStatus                workflowdomain.NodeStatus
	NodeAttemptNo             int
	NodeLeaseOwner            *string
	NodeLeaseUntil            *time.Time
	NodeCompletedAt           *time.Time
}

type workspaceAnalysisCancellationRun struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
	WorkflowRunID  foundation.ID
	Status         agentdomain.WorkspaceAnalysisRunStatus
	Reason         *agentdomain.WorkspaceAnalysisRunTerminationReason
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func workspaceAnalysisCancellationNodeKey(kind string) (string, bool) {
	for _, node := range conversationworkflow.RegisteredWorkspaceAnalysisDefinition().Graph.Nodes {
		if node.Kind == kind {
			return node.Key, true
		}
	}
	return "", false
}

func validateWorkspaceAnalysisCancellationEvent(event workflowapplication.WorkflowNodeTerminalEvent) error {
	values := []string{string(event.WorkspaceID), string(event.WorkflowRunID), string(event.NodeRunID)}
	if event.NodeAttemptID != "" {
		values = append(values, string(event.NodeAttemptID))
	}
	if _, err := parseWorkspaceAnalysisIDs(values...); err != nil || event.NodeKind == "" ||
		event.Outcome != workflowapplication.WorkflowTerminalOutcomeCancelled ||
		event.FailureClass != workflowdomain.FailureClassCancelled ||
		event.FailureCode != "WORKFLOW_CANCELLED" || event.FailureSummary != "WORKFLOW_CANCELLED" ||
		event.TerminalOutput != "" || event.TerminalAt.IsZero() ||
		!event.TerminalAt.Equal(event.TerminalAt.UTC().Truncate(time.Microsecond)) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation terminal event is invalid"),
		)
	}
	return nil
}

func validateWorkspaceAnalysisRuntimeFailureEvent(event workflowapplication.WorkflowNodeTerminalEvent) error {
	values := []string{
		string(event.WorkspaceID), string(event.WorkflowRunID),
		string(event.NodeRunID), string(event.NodeAttemptID),
	}
	if _, err := parseWorkspaceAnalysisIDs(values...); err != nil || event.NodeKind == "" ||
		event.Outcome != workflowapplication.WorkflowTerminalOutcomeFailed ||
		event.FailureClass == "" || event.FailureClass == workflowdomain.FailureClassCancelled ||
		strings.TrimSpace(event.FailureCode) == "" || event.FailureCode != strings.TrimSpace(event.FailureCode) ||
		strings.TrimSpace(event.FailureSummary) == "" || event.FailureSummary != strings.TrimSpace(event.FailureSummary) ||
		event.TerminalOutput != "" || event.TerminalAt.IsZero() ||
		!event.TerminalAt.Equal(event.TerminalAt.UTC().Truncate(time.Microsecond)) {
		return consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis runtime failure terminal event is invalid"),
		)
	}
	return nil
}
