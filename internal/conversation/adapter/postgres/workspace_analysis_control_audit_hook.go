package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
)

const workspaceAnalysisCancelRequestedAuditAction = "workspace_analysis.cancel_requested"

// WorkspaceAnalysisCancellationAuditHook 在首次 Workflow 取消命令的事务内记录工作区分析取消审计。
// 它自行验证 Analysis Run 与冻结 Definition，避免通用 Runtime 持有 Conversation 事实。
type WorkspaceAnalysisCancellationAuditHook struct {
	audit WorkspaceAnalysisAuditRecorder
}

// NewWorkspaceAnalysisCancellationAuditHook 构造供 Workflow Runtime 使用的事务内取消审计 Hook。
func NewWorkspaceAnalysisCancellationAuditHook(audit WorkspaceAnalysisAuditRecorder) (*WorkspaceAnalysisCancellationAuditHook, error) {
	if isNilInterface(audit) {
		return nil, dependency(
			ErrorCodeWorkspaceAnalysisFinalizeUnavailable,
			errors.New("workspace analysis cancellation audit dependency is invalid"),
		)
	}
	return &WorkspaceAnalysisCancellationAuditHook{audit: audit}, nil
}

// OnWorkflowControl 只处理 workspace-analysis@1 的首次取消命令；其他控制和 Workflow 保持 no-op。
func (hook *WorkspaceAnalysisCancellationAuditHook) OnWorkflowControl(
	ctx context.Context,
	transaction any,
	event workflowapplication.WorkflowControlEvent,
) error {
	if event.Action != workflowapplication.ControlActionCancel {
		return nil
	}
	if hook == nil || isNilInterface(hook.audit) {
		return dependency(
			ErrorCodeWorkspaceAnalysisFinalizeUnavailable,
			errors.New("workspace analysis cancellation audit hook is unavailable"),
		)
	}
	if ctx == nil {
		return invalid(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis cancellation audit context is nil"))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || isNilInterface(tx) {
		return invalid(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis cancellation audit transaction is invalid"))
	}
	if err := validateWorkspaceAnalysisCancellationControlEvent(event); err != nil {
		return err
	}
	run, found, err := lockWorkspaceAnalysisCancellationAuditRun(ctx, tx, event)
	if err != nil || !found {
		return err
	}
	return appendWorkspaceAnalysisCancelRequestedAudit(ctx, tx, hook.audit, run, event)
}

type workspaceAnalysisCancellationAuditRun struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
	WorkflowRunID  foundation.ID
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

func lockWorkspaceAnalysisCancellationAuditRun(
	ctx context.Context,
	tx pgx.Tx,
	event workflowapplication.WorkflowControlEvent,
) (workspaceAnalysisCancellationAuditRun, bool, error) {
	var (
		run                                                                  workspaceAnalysisCancellationAuditRun
		id, workspaceID, conversationID, questionID, answerID, workflowRunID string
		analysisDefinitionKey, workflowDefinitionKey                         string
		analysisDefinitionVersion, workflowDefinitionVersion                 int64
	)
	err := tx.QueryRow(ctx, `SELECT
		a.id::text,a.workspace_id::text,a.conversation_id::text,a.question_id::text,a.answer_id::text,a.workflow_run_id::text,
		a.definition_key,a.definition_version,d.key,d.version
		FROM agent.workspace_analysis_run a
		JOIN workflow.run r ON r.id=a.workflow_run_id AND r.workspace_id=a.workspace_id
		JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
		WHERE a.workspace_id=$1 AND a.workflow_run_id=$2
		FOR UPDATE OF a`, string(event.WorkspaceID), string(event.WorkflowRunID)).Scan(
		&id, &workspaceID, &conversationID, &questionID, &answerID, &workflowRunID,
		&analysisDefinitionKey, &analysisDefinitionVersion, &workflowDefinitionKey, &workflowDefinitionVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return workspaceAnalysisCancellationAuditRun{}, false, nil
	}
	if err != nil {
		return workspaceAnalysisCancellationAuditRun{}, false, classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	ids, err := parseWorkspaceAnalysisIDs(id, workspaceID, conversationID, questionID, answerID, workflowRunID)
	if err != nil {
		return workspaceAnalysisCancellationAuditRun{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	run.ID, run.WorkspaceID, run.ConversationID = ids[0], ids[1], ids[2]
	run.QuestionID, run.AnswerID, run.WorkflowRunID = ids[3], ids[4], ids[5]
	if run.WorkspaceID != event.WorkspaceID || run.WorkflowRunID != event.WorkflowRunID ||
		analysisDefinitionKey != "workspace-analysis" || analysisDefinitionVersion != 1 ||
		workflowDefinitionKey != "workspace-analysis" || workflowDefinitionVersion != 1 {
		return workspaceAnalysisCancellationAuditRun{}, false, consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation definition binding is invalid"),
		)
	}
	return run, true, nil
}

func appendWorkspaceAnalysisCancelRequestedAudit(
	ctx context.Context,
	transaction any,
	recorder WorkspaceAnalysisAuditRecorder,
	run workspaceAnalysisCancellationAuditRun,
	control workflowapplication.WorkflowControlEvent,
) error {
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
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis cancellation audit correlation cannot be encoded"))
	}
	metadata, err := json.Marshal(struct {
		DefinitionKey     string `json:"definition_key"`
		DefinitionVersion int64  `json:"definition_version"`
		WorkflowStatus    string `json:"workflow_status"`
		CancelRequested   bool   `json:"cancel_requested"`
	}{
		DefinitionKey: "workspace-analysis", DefinitionVersion: 1,
		WorkflowStatus: string(control.PersistedControl.Status), CancelRequested: control.PersistedControl.CancelRequested,
	})
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis cancellation audit metadata cannot be encoded"))
	}
	event, err := newWorkspaceAnalysisAuditEvent(workspaceAnalysisAuditEventInput{
		WorkspaceID: run.WorkspaceID, AnalysisRunID: run.ID, ActorType: auditdomain.ActorAgent,
		ActorRef: workspaceAnalysisAuditAgentRef, Action: workspaceAnalysisCancelRequestedAuditAction,
		Outcome: auditdomain.OutcomeSucceeded, IdempotencyKey: "workspace-analysis:cancel-requested:" + string(run.ID) + ":v1",
		Correlation: correlation, Metadata: metadata, OccurredAt: control.OccurredAt,
	})
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return recordFreshWorkspaceAnalysisAudit(ctx, transaction, recorder, event, ErrorCodeWorkspaceAnalysisFinalizeCorrupt)
}
