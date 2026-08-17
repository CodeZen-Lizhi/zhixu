package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
)

const (
	workspaceAnalysisRunStartedAuditAction = "workspace_analysis.run_started"
	workspaceAnalysisTerminatedAuditAction = "workspace_analysis.terminated"
	workspaceAnalysisAuditResourceType     = "workspace_analysis_run"
	workspaceAnalysisAuditAgentRef         = "workspace-analysis@1"
)

// WorkspaceAnalysisAuditRecorder 是 Conversation PostgreSQL 事务使用的窄审计边界。
type WorkspaceAnalysisAuditRecorder interface {
	RecordTx(context.Context, any, auditdomain.Event) (auditdomain.Event, bool, error)
}

// appendWorkspaceAnalysisRunStartedAudit 在新 Analysis Run 的创建事务中追加一次审计。
func appendWorkspaceAnalysisRunStartedAudit(
	ctx context.Context,
	transaction any,
	recorder WorkspaceAnalysisAuditRecorder,
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
	return recordFreshWorkspaceAnalysisAudit(ctx, transaction, recorder, event, ErrorCodeQuestionDispatchCorrupt)
}

// appendWorkspaceAnalysisTerminalAudit 在 Answer、Analysis Run 与终态事件的同一事务中追加一次审计。
func appendWorkspaceAnalysisTerminalAudit(
	ctx context.Context,
	transaction any,
	recorder WorkspaceAnalysisAuditRecorder,
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
	return recordFreshWorkspaceAnalysisAudit(ctx, transaction, recorder, event, ErrorCodeWorkspaceAnalysisFinalizeCorrupt)
}

type workspaceAnalysisAuditEventInput struct {
	WorkspaceID    foundation.ID
	AnalysisRunID  foundation.ID
	ActorType      auditdomain.ActorType
	ActorRef       string
	Action         string
	Outcome        auditdomain.Outcome
	ErrorCode      string
	IdempotencyKey string
	Correlation    json.RawMessage
	Metadata       json.RawMessage
	OccurredAt     time.Time
}

func newWorkspaceAnalysisAuditEvent(input workspaceAnalysisAuditEventInput) (auditdomain.Event, error) {
	eventID, err := workspaceAnalysisAuditEventID(input.Action, input.AnalysisRunID)
	if err != nil {
		return auditdomain.Event{}, err
	}
	workspaceID := input.WorkspaceID
	return auditdomain.NewEvent(auditdomain.Event{
		ID: eventID, WorkspaceID: &workspaceID, ActorType: input.ActorType, ActorRef: input.ActorRef,
		Action: input.Action, ResourceType: workspaceAnalysisAuditResourceType,
		ResourceRef: "workspace_analysis:" + string(input.AnalysisRunID), Outcome: input.Outcome,
		ErrorCode: input.ErrorCode, IdempotencyKey: input.IdempotencyKey,
		Correlation: input.Correlation, Metadata: input.Metadata, SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt: input.OccurredAt.UTC().Truncate(time.Microsecond),
	})
}

func recordFreshWorkspaceAnalysisAudit(
	ctx context.Context,
	transaction any,
	recorder WorkspaceAnalysisAuditRecorder,
	event auditdomain.Event,
	errorCode string,
) error {
	_, replayed, err := recorder.RecordTx(ctx, transaction, event)
	if err != nil {
		return err
	}
	if replayed {
		return consistency(errorCode, errors.New("new workspace analysis fact reused an existing audit event"))
	}
	return nil
}

func workspaceAnalysisTerminalAuditOutcome(
	status agentdomain.WorkspaceAnalysisRunStatus,
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
) (auditdomain.Outcome, string, error) {
	switch status {
	case agentdomain.WorkspaceAnalysisRunSucceeded:
		if reason == agentdomain.WorkspaceAnalysisRunCompleted {
			return auditdomain.OutcomeSucceeded, "", nil
		}
	case agentdomain.WorkspaceAnalysisRunRefused:
		switch reason {
		case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, agentdomain.WorkspaceAnalysisRunCitationInvalid,
			agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, agentdomain.WorkspaceAnalysisRunModelRefused:
			return auditdomain.OutcomeRejected, string(reason), nil
		}
	case agentdomain.WorkspaceAnalysisRunClarificationRequired:
		if reason == agentdomain.WorkspaceAnalysisRunNeedsClarification {
			return auditdomain.OutcomeRejected, string(reason), nil
		}
	case agentdomain.WorkspaceAnalysisRunFailed:
		switch reason {
		case agentdomain.WorkspaceAnalysisRunResultUnknown:
			return auditdomain.OutcomeUnknown, string(reason), nil
		case agentdomain.WorkspaceAnalysisRunBudgetExhausted, agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			agentdomain.WorkspaceAnalysisRunDeadlineExceeded, agentdomain.WorkspaceAnalysisRunModelFailed,
			agentdomain.WorkspaceAnalysisRunToolFailed, agentdomain.WorkspaceAnalysisRunRuntimeFailed:
			return auditdomain.OutcomeFailed, string(reason), nil
		}
	case agentdomain.WorkspaceAnalysisRunCancelled:
		if reason == agentdomain.WorkspaceAnalysisRunCancellation {
			return auditdomain.OutcomeRejected, string(reason), nil
		}
	}
	return "", "", errors.New("workspace analysis terminal audit outcome is inconsistent")
}

func workspaceAnalysisAuditEventID(action string, analysisRunID foundation.ID) (foundation.ID, error) {
	if parsed, err := foundation.ParseID(string(analysisRunID)); err != nil || parsed != analysisRunID {
		return "", errors.New("workspace analysis audit run identity is invalid")
	}
	digest := sha256.Sum256([]byte("workspace-analysis-audit/v1\x00" + action + "\x00" + string(analysisRunID)))
	raw := digest[:16]
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	return foundation.ParseID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32])
}

func validWorkspaceAnalysisAuditActorRef(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\r\n") || foundationredaction.ContainsSecret(value) ||
		foundationredaction.ContainsPII(value) || foundationredaction.ContainsAbsolutePath(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
