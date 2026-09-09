package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMWorkspaceAnalysisCancellationAuditHook 在首次取消命令的同一 scope 记录审计。
type GORMWorkspaceAnalysisCancellationAuditHook struct {
	audit ScopedWorkspaceAnalysisAuditRecorder
}

// NewGORMWorkspaceAnalysisCancellationAuditHook 构造取消命令审计 Hook。
func NewGORMWorkspaceAnalysisCancellationAuditHook(audit ScopedWorkspaceAnalysisAuditRecorder) (*GORMWorkspaceAnalysisCancellationAuditHook, error) {
	if isNilInterface(audit) {
		return nil, dependency(
			ErrorCodeWorkspaceAnalysisFinalizeUnavailable,
			errors.New("workspace analysis cancellation audit dependency is invalid"),
		)
	}
	return &GORMWorkspaceAnalysisCancellationAuditHook{audit: audit}, nil
}

// OnWorkflowControlScoped 只为首次工作区分析取消命令记录审计。
func (hook *GORMWorkspaceAnalysisCancellationAuditHook) OnWorkflowControlScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
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
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return invalid(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis cancellation audit transaction is invalid"))
	}
	if err := validateWorkspaceAnalysisCancellationControlEvent(event); err != nil {
		return err
	}
	run, found, err := gormLockWorkspaceAnalysisCancellationAuditRun(ctx, tx, event)
	if err != nil || !found {
		return err
	}
	return appendScopedWorkspaceAnalysisCancelRequestedAudit(ctx, scope, hook.audit, run, event)
}

func gormLockWorkspaceAnalysisCancellationAuditRun(
	ctx context.Context,
	tx *gorm.DB,
	event workflowapplication.WorkflowControlEvent,
) (workspaceAnalysisCancellationAuditRun, bool, error) {
	var (
		run                                                                  workspaceAnalysisCancellationAuditRun
		id, workspaceID, conversationID, questionID, answerID, workflowRunID string
		analysisDefinitionKey, workflowDefinitionKey                         string
		analysisDefinitionVersion, workflowDefinitionVersion                 int64
		policyVersion                                                        int
		definitionHash                                                       string
	)
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		a.id::text,a.workspace_id::text,a.conversation_id::text,a.question_id::text,a.answer_id::text,a.workflow_run_id::text,
		a.definition_key,a.definition_version,d.key,d.version,a.policy_version,a.definition_hash
		FROM agent.workspace_analysis_run a
		JOIN workflow.run r ON r.id=a.workflow_run_id AND r.workspace_id=a.workspace_id
		JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
		WHERE a.workspace_id=? AND a.workflow_run_id=?
		FOR UPDATE OF a`, string(event.WorkspaceID), string(event.WorkflowRunID))).Scan(
		&id, &workspaceID, &conversationID, &questionID, &answerID, &workflowRunID,
		&analysisDefinitionKey, &analysisDefinitionVersion, &workflowDefinitionKey, &workflowDefinitionVersion, &policyVersion, &definitionHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
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
		analysisDefinitionKey != "workspace-analysis" || (analysisDefinitionVersion != 1 && analysisDefinitionVersion != 2) ||
		workflowDefinitionKey != "workspace-analysis" || workflowDefinitionVersion != analysisDefinitionVersion {
		return workspaceAnalysisCancellationAuditRun{}, false, consistency(
			ErrorCodeWorkspaceAnalysisFinalizeCorrupt,
			errors.New("workspace analysis cancellation definition binding is invalid"),
		)
	}
	run.DefinitionVersion = analysisDefinitionVersion
	if err := gormValidateWorkspaceAnalysisPersistedDefinition(ctx, tx, run.WorkspaceID, run.WorkflowRunID, run.DefinitionVersion, policyVersion, definitionHash); err != nil {
		return workspaceAnalysisCancellationAuditRun{}, false, err
	}
	return run, true, nil
}

func appendScopedWorkspaceAnalysisCancelRequestedAudit(
	ctx context.Context,
	scope foundation.TransactionScope,
	recorder ScopedWorkspaceAnalysisAuditRecorder,
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
		DefinitionKey: "workspace-analysis", DefinitionVersion: workspaceAnalysisPublicationVersion(run.DefinitionVersion),
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
	return recordFreshScopedWorkspaceAnalysisAudit(ctx, scope, recorder, event, ErrorCodeWorkspaceAnalysisFinalizeCorrupt)
}

var _ workflowapplication.ScopedWorkflowControlHook = (*GORMWorkspaceAnalysisCancellationAuditHook)(nil)
