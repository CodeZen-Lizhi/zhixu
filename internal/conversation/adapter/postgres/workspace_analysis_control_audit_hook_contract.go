package postgres

import (
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"time"
)

const workspaceAnalysisCancelRequestedAuditAction = "workspace_analysis.cancel_requested"

type workspaceAnalysisCancellationAuditRun struct {
	DefinitionVersion int64
	ID                foundation.ID
	WorkspaceID       foundation.ID
	ConversationID    foundation.ID
	QuestionID        foundation.ID
	AnswerID          foundation.ID
	WorkflowRunID     foundation.ID
}

func validateWorkspaceAnalysisCancellationControlEvent(event workflowapplication.WorkflowControlEvent) error {
	if event.Action != workflowapplication.ControlActionCancel || event.IdempotencyKey == "" ||
		event.ExpectedVersion < 1 || event.PersistedControl.WorkflowRunID != event.WorkflowRunID ||
		event.PersistedControl.Version <= event.ExpectedVersion || !event.PersistedControl.CancelRequested ||
		event.OccurredAt.IsZero() || !event.OccurredAt.Equal(event.OccurredAt.UTC().Truncate(time.Microsecond)) {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis cancellation control event is invalid"))
	}
	if _, err := parseWorkspaceAnalysisIDs(string(event.WorkspaceID), string(event.WorkflowRunID)); err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return nil
}
