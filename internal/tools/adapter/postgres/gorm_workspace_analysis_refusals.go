package postgres

import (
	"context"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"gorm.io/gorm"
)

// RecordWorkspaceAnalysisToolRefusal records a deterministic pre-executor
// refusal in the caller-owned GORM UoW. Workflow fencing, the Agent-owned
// refusal fact, and its audit event remain one atomic closure.
func (repository *GORMWorkspaceAnalysisRepository) RecordWorkspaceAnalysisToolRefusal(
	ctx context.Context,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
) (toolsapplication.WorkspaceAnalysisToolRefusalResult, error) {
	if err := validateWorkspaceAnalysisToolRefusalCommand(command); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	if err := repository.ready(ctx); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	auditEventID, err := workspaceAnalysisToolRefusalAuditEventID(command.OperationKey)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, consistency(err)
	}
	// Replay is a durable fact lookup and must remain available after the live
	// Workflow lease/cancel fence has expired. Only an absent refusal proceeds to
	// the admission-and-insert transaction below.
	if replayed, found, replayErr := repository.gormLoadWorkspaceAnalysisToolRefusalClosure(ctx, command, auditEventID); replayErr != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classifyGORMTools(ctx, replayErr)
	} else if found {
		return replayed, nil
	}

	var result toolsapplication.WorkspaceAnalysisToolRefusalResult
	completed := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		_, identity, lockErr := repository.lockExecution(callbackCtx, scope, command.Identity)
		if lockErr != nil {
			return lockErr
		}
		refusal, replayed, refusalErr := repository.refusalStore.RecordWorkspaceAnalysisToolRefusalScoped(
			callbackCtx,
			scope,
			agentapplication.RecordWorkspaceAnalysisToolRefusalScopedCommand{
				Identity: identity, OperationKey: command.OperationKey, RefusalID: command.RefusalID,
				AuditEventID: auditEventID, ErrorCode: command.ErrorCode,
			},
		)
		if refusalErr != nil {
			return classifyGORMTools(callbackCtx, refusalErr)
		}
		if err := refusal.Validate(); err != nil || refusal.ID != command.RefusalID ||
			refusal.AuditEventID != auditEventID || refusal.ErrorCode != command.ErrorCode ||
			refusal.WorkspaceID != command.Identity.WorkspaceID || refusal.WorkflowRunID != command.Identity.WorkflowRunID ||
			refusal.NodeRunID != command.Identity.NodeRunID || refusal.OperationKey != command.OperationKey {
			if err == nil {
				err = errors.New("workspace analysis GORM refusal binding differs")
			}
			return consistency(err)
		}
		auditEvent, eventErr := newWorkspaceAnalysisToolRefusalAuditEvent(command, refusal.CreatedAt)
		if eventErr != nil || auditEvent.ID != auditEventID {
			if eventErr == nil {
				eventErr = errors.New("workspace analysis GORM refusal audit identity differs")
			}
			return consistency(eventErr)
		}
		_, auditReplayed, auditErr := repository.audit.RecordScoped(callbackCtx, scope, auditEvent)
		if auditErr != nil {
			return classifyGORMTools(callbackCtx, auditErr)
		}
		if auditReplayed != replayed {
			return consistency(errors.New("workspace analysis GORM refusal audit replay differs"))
		}
		if constraintErr := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); constraintErr != nil {
			return classifyGORMTools(callbackCtx, constraintErr)
		}
		result = toolsapplication.WorkspaceAnalysisToolRefusalResult{
			RefusalID: refusal.ID, ErrorCode: refusal.ErrorCode, Replayed: replayed,
		}
		completed = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if completed && gormToolsCommitFailure(err) {
		return repository.gormRecoverWorkspaceAnalysisToolRefusalAfterCommitError(ctx, command, auditEventID, err)
	}
	if replayed, found, replayErr := repository.gormLoadWorkspaceAnalysisToolRefusalClosure(ctx, command, auditEventID); replayErr == nil && found {
		return replayed, nil
	} else if replayErr != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classifyGORMTools(ctx, errors.Join(err, replayErr))
	}
	return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classifyGORMTools(ctx, err)
}

// gormRecoverWorkspaceAnalysisToolRefusalAfterCommitError proves the immutable
// refusal and Audit closure. It deliberately does not reacquire a live Workflow
// fence or re-run admission after an ambiguous commit response.
func (repository *GORMWorkspaceAnalysisRepository) gormRecoverWorkspaceAnalysisToolRefusalAfterCommitError(
	ctx context.Context,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
	auditEventID foundation.ID,
	commitErr error,
) (toolsapplication.WorkspaceAnalysisToolRefusalResult, error) {
	result, found, err := repository.gormLoadWorkspaceAnalysisToolRefusalClosure(ctx, command, auditEventID)
	if err == nil && found {
		return result, nil
	}
	if err == nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classifyGORMTools(ctx, commitErr)
	}
	return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classifyGORMTools(ctx, errors.Join(commitErr, err))
}

// gormLoadWorkspaceAnalysisToolRefusalClosure proves an immutable refusal and
// its matching Audit fact without re-running live Workflow admission. found is
// true once the refusal row exists, even if its binding or Audit proof is
// corrupt; callers must then surface that proof error rather than proceed with
// a new admission.
func (repository *GORMWorkspaceAnalysisRepository) gormLoadWorkspaceAnalysisToolRefusalClosure(
	ctx context.Context,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
	auditEventID foundation.ID,
) (result toolsapplication.WorkspaceAnalysisToolRefusalResult, found bool, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ *gorm.DB, scope foundation.TransactionScope) error {
		refusal, rowFound, loadErr := repository.refusalStore.LoadWorkspaceAnalysisToolRefusalExactScoped(
			callbackCtx,
			scope,
			agentapplication.WorkspaceAnalysisToolRefusalQuery{
				WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID,
				AnalysisRunID: command.OperationKey.AnalysisRunID, NodeRunID: command.Identity.NodeRunID,
				OperationKey: command.OperationKey, RefusalID: command.RefusalID, AuditEventID: auditEventID,
				ErrorCode: command.ErrorCode,
			},
		)
		if loadErr != nil {
			return loadErr
		}
		if !rowFound {
			return nil
		}
		found = true
		if refusal.Validate() != nil || refusal.ID != command.RefusalID ||
			refusal.AuditEventID != auditEventID || refusal.ErrorCode != command.ErrorCode {
			return errors.New("workspace analysis GORM refusal commit closure was not proven")
		}
		expectedAudit, eventErr := newWorkspaceAnalysisToolRefusalAuditEvent(command, refusal.CreatedAt)
		if eventErr != nil || expectedAudit.ID != auditEventID {
			if eventErr == nil {
				eventErr = errors.New("workspace analysis GORM refusal audit identity differs")
			}
			return consistency(eventErr)
		}
		persistedAudit, auditErr := repository.audit.ReadScoped(callbackCtx, scope, auditEventID)
		if auditErr != nil {
			return auditErr
		}
		if persistedAudit.ID != auditEventID || !auditdomain.EqualBinding(expectedAudit, persistedAudit) {
			return consistency(errors.New("workspace analysis GORM refusal Audit closure differs"))
		}
		result = toolsapplication.WorkspaceAnalysisToolRefusalResult{
			RefusalID: refusal.ID, ErrorCode: refusal.ErrorCode, Replayed: true,
		}
		return nil
	})
	return result, found, err
}

var _ toolsapplication.WorkspaceAnalysisToolRefusalRepository = (*GORMWorkspaceAnalysisRepository)(nil)
