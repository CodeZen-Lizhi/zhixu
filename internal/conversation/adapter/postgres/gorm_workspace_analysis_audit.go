package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedWorkspaceAnalysisAuditRecorder 将审计追加到调用方持有的事务 scope。
type ScopedWorkspaceAnalysisAuditRecorder interface {
	RecordScoped(context.Context, foundation.TransactionScope, auditdomain.Event) (auditdomain.Event, bool, error)
}

func appendScopedWorkspaceAnalysisRunStartedAudit(
	ctx context.Context,
	transaction foundation.TransactionScope,
	recorder ScopedWorkspaceAnalysisAuditRecorder,
	run agentdomain.WorkspaceAnalysisRun,
) error {
	if isNilInterface(recorder) {
		return nil
	}
	correlation, err := json.Marshal(struct {
		AnalysisRunID  string `json:"analysis_run_id"`
		WorkflowRunID  string `json:"workflow_run_id"`
		ConversationID string `json:"conversation_id"`
		QuestionID     string `json:"question_id"`
		AnswerID       string `json:"answer_id"`
	}{
		AnalysisRunID: string(run.ID), WorkflowRunID: string(run.WorkflowRunID),
		ConversationID: string(run.ConversationID), QuestionID: string(run.QuestionID), AnswerID: string(run.AnswerID),
	})
	if err != nil {
		return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("workspace analysis started audit correlation cannot be encoded"))
	}
	metadata, err := json.Marshal(struct {
		DefinitionKey     string `json:"definition_key"`
		DefinitionVersion int64  `json:"definition_version"`
		PolicyVersion     int    `json:"policy_version"`
		ConfigRevision    int64  `json:"config_revision"`
		Status            string `json:"status"`
	}{
		DefinitionKey: run.DefinitionKey, DefinitionVersion: run.DefinitionVersion,
		PolicyVersion: run.PolicyVersion, ConfigRevision: run.ConfigRevision, Status: string(run.Status),
	})
	if err != nil {
		return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("workspace analysis started audit metadata cannot be encoded"))
	}
	event, err := newWorkspaceAnalysisAuditEvent(workspaceAnalysisAuditEventInput{
		WorkspaceID: run.WorkspaceID, AnalysisRunID: run.ID, ActorType: auditdomain.ActorAgent,
		ActorRef: workspaceAnalysisAuditAgentRef, Action: workspaceAnalysisRunStartedAuditAction,
		Outcome: auditdomain.OutcomeSucceeded, IdempotencyKey: "workspace-analysis:run-started:" + string(run.ID) + ":v1",
		Correlation: correlation, Metadata: metadata, OccurredAt: run.CreatedAt,
	})
	if err != nil {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	return recordFreshScopedWorkspaceAnalysisAudit(ctx, transaction, recorder, event, ErrorCodeQuestionDispatchCorrupt)
}

func appendScopedWorkspaceAnalysisTerminalAudit(
	ctx context.Context,
	transaction foundation.TransactionScope,
	recorder ScopedWorkspaceAnalysisAuditRecorder,
	workerActorRef string,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
	answer conversationdomain.Answer,
	citationCount int64,
	now time.Time,
) error {
	if isNilInterface(recorder) {
		return nil
	}
	outcome, errorCode, err := workspaceAnalysisTerminalAuditOutcome(runStatus, reason)
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	correlation, err := json.Marshal(struct {
		AnalysisRunID  string `json:"analysis_run_id"`
		WorkflowRunID  string `json:"workflow_run_id"`
		ConversationID string `json:"conversation_id"`
		QuestionID     string `json:"question_id"`
		AnswerID       string `json:"answer_id"`
	}{
		AnalysisRunID: string(analysisRunID), WorkflowRunID: string(answer.WorkflowRunID),
		ConversationID: string(answer.ConversationID), QuestionID: string(answer.QuestionID), AnswerID: string(answer.ID),
	})
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis terminal audit correlation cannot be encoded"))
	}
	metadata, err := json.Marshal(struct {
		Status            string `json:"status"`
		TerminationReason string `json:"termination_reason"`
		PublicationStatus string `json:"publication_status"`
		ResultType        string `json:"result_type"`
		CitationCount     int64  `json:"citation_count"`
		AnswerVersion     int64  `json:"answer_version"`
	}{
		Status: string(runStatus), TerminationReason: string(reason), PublicationStatus: string(answer.PublicationStatus),
		ResultType: string(answer.ResultType), CitationCount: citationCount, AnswerVersion: answer.Version,
	})
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis terminal audit metadata cannot be encoded"))
	}
	event, err := newWorkspaceAnalysisAuditEvent(workspaceAnalysisAuditEventInput{
		WorkspaceID: answer.WorkspaceID, AnalysisRunID: analysisRunID, ActorType: auditdomain.ActorWorker,
		ActorRef: workerActorRef, Action: workspaceAnalysisTerminatedAuditAction, Outcome: outcome, ErrorCode: errorCode,
		IdempotencyKey: "workspace-analysis:terminated:" + string(analysisRunID) + ":v1",
		Correlation:    correlation, Metadata: metadata, OccurredAt: now,
	})
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return recordFreshScopedWorkspaceAnalysisAudit(ctx, transaction, recorder, event, ErrorCodeWorkspaceAnalysisFinalizeCorrupt)
}

func recordFreshScopedWorkspaceAnalysisAudit(
	ctx context.Context,
	transaction foundation.TransactionScope,
	recorder ScopedWorkspaceAnalysisAuditRecorder,
	event auditdomain.Event,
	errorCode string,
) error {
	_, replayed, err := recorder.RecordScoped(ctx, transaction, event)
	if err != nil {
		return err
	}
	if replayed {
		return consistency(errorCode, errors.New("new workspace analysis fact reused an existing audit event"))
	}
	return nil
}
