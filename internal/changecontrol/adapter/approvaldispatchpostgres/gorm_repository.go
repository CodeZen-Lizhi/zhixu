package approvaldispatchpostgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	changedispatch "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/dispatch"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontroleventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

var errGORMApprovalDispatchReplay = errors.New("approval dispatch replay requires rollback")

// GORMApprovalDispatchRepository is the staged Approval Dispatch implementation.
// Production composition remains on ApprovalDispatchRepository until TODO 9 passes.
type GORMApprovalDispatchRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	runtime    workflowapplication.ScopedRuntimeStarter
	ids        foundation.IDGenerator
	clock      foundation.Clock
	events     eventsapplication.ScopedAppender
}

var _ changedispatch.Dispatcher = (*GORMApprovalDispatchRepository)(nil)

// NewGORMApprovalDispatchRepository derives its GORM root and Unit of Work
// from one shared platform Pool. Scoped collaborators must be composed from the
// same Pool by the caller; the opaque scope cannot currently expose pool identity.
func NewGORMApprovalDispatchRepository(
	pool *platformpostgres.Pool,
	runtime workflowapplication.ScopedRuntimeStarter,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	appenders ...eventsapplication.ScopedAppender,
) (*GORMApprovalDispatchRepository, error) {
	if pool == nil || isNilDispatchDependency(runtime) || isNilDispatchDependency(ids) || isNilDispatchDependency(clock) {
		return nil, gormApprovalDispatchUnavailable(errors.New("approval dispatch dependencies are unavailable"))
	}
	if len(appenders) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_EVENT_APPENDER_INVALID", false, errors.New("only one approval dispatch event appender may be configured"))
	}
	database, err := pool.GORM()
	if err != nil || !validGORMApprovalDispatchDatabase(database) {
		return nil, gormApprovalDispatchUnavailable(errors.New("approval dispatch GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil || isNilDispatchDependency(unitOfWork) {
		return nil, gormApprovalDispatchUnavailable(errors.New("approval dispatch unit of work is unavailable"))
	}
	var events eventsapplication.ScopedAppender
	if len(appenders) == 1 {
		if isNilDispatchDependency(appenders[0]) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_EVENT_APPENDER_INVALID", false, errors.New("approval dispatch event appender is nil"))
		}
		events = appenders[0]
	}
	return &GORMApprovalDispatchRepository{
		database: database, unitOfWork: unitOfWork, runtime: runtime, ids: ids, clock: clock, events: events,
	}, nil
}

// DecideAndDispatch atomically persists or exactly replays Approval Dispatch
// facts, including the Workflow/River producer and optional rejected event.
func (repository *GORMApprovalDispatchRepository) DecideAndDispatch(ctx context.Context, command changedispatch.Command) (changedispatch.Result, error) {
	if err := validateApprovalDispatchCommand(command); err != nil {
		return changedispatch.Result{}, err
	}
	if err := repository.ready(ctx); err != nil {
		return changedispatch.Result{}, err
	}
	command = normalizeGORMApprovalDispatchCommand(command)

	var result changedispatch.Result
	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return gormApprovalDispatchUnavailable(unwrapErr)
		}
		value, rollbackReplay, dispatchErr := repository.decideScoped(callbackCtx, scope, transaction.WithContext(callbackCtx), command)
		if dispatchErr != nil {
			return dispatchErr
		}
		result = value
		if rollbackReplay {
			return errGORMApprovalDispatchReplay
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if errors.Is(err, errGORMApprovalDispatchReplay) {
		return result, nil
	}
	code := "APPROVAL_DISPATCH_TRANSACTION_FAILED"
	if callbackSucceeded {
		code = "APPROVAL_DISPATCH_COMMIT_FAILED"
	}
	return changedispatch.Result{}, classifyGORMApprovalDispatch(ctx, err, code)
}

func (repository *GORMApprovalDispatchRepository) decideScoped(ctx context.Context, scope foundation.TransactionScope, transaction *gorm.DB, command changedispatch.Command) (changedispatch.Result, bool, error) {
	persisted, err := gormApprovalDispatchLockProposalRevision(ctx, transaction, command.Approval.ProposalID, command.Approval.RevisionID)
	if gormApprovalDispatchNoRows(err) {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_DISPATCH_PROPOSAL_QUERY_FAILED")
	}
	proposalType := domain.NormalizeProposalType(domain.ProposalType(persisted.proposalType))
	if proposalType == domain.ProposalTypeDownstreamUpdate {
		return changedispatch.Result{}, false, domain.NewDownstreamUpdateApplyUnavailableError()
	}
	if !domain.ProposalSupportsFileWriteback(proposalType) {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_PROPOSAL_TYPE_UNSUPPORTED", false, errors.New("approval dispatch only supports governed file writeback proposals"))
	}
	if err := persisted.validate(command, proposalType); err != nil {
		return changedispatch.Result{}, false, err
	}

	existing, found, err := gormApprovalDispatchLoadApproval(ctx, transaction, command.Approval.RevisionID)
	if err != nil {
		return changedispatch.Result{}, false, err
	}
	if found {
		if !sameApprovalDecision(existing, command.Approval) {
			return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("approval decision binding differs"))
		}
		command.Approval = existing
	}

	if command.Approval.Decision == domain.DecisionRejected {
		return repository.persistRejectedScoped(ctx, scope, transaction, command, persisted, found)
	}
	if persisted.revisionWorkflowRunID != nil {
		return repository.replayApprovedScoped(ctx, scope, command, foundation.ID(*persisted.revisionWorkflowRunID))
	}
	if proposalType == domain.ProposalTypeRestoreDocument {
		if err := gormApprovalDispatchValidateRestore(ctx, transaction, command, persisted); err != nil {
			return changedispatch.Result{}, false, err
		}
	}
	if err := validateApprovedDispatchSafety(command, persisted.targetPath, domain.TargetMode(persisted.targetMode), persisted.baseHash); err != nil {
		return changedispatch.Result{}, false, err
	}
	if found {
		if persisted.status != string(domain.StatusApproved) {
			return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_DISPATCH", false, errors.New("historical approval is not dispatchable"))
		}
	} else if persisted.status != string(domain.StatusReady) {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}

	if !found {
		if _, err := gormApprovalDispatchExec(ctx, transaction, `
			INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
			VALUES(?::uuid,?::uuid,?::uuid,?,?,?,?)`,
			string(command.Approval.ID), string(command.Approval.ProposalID), string(command.Approval.RevisionID), command.Approval.ChangeHash,
			string(command.Approval.Decision), command.Approval.ApprovedGitHead, command.Approval.DecidedAt.UTC()); err != nil {
			return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_CREATE_FAILED")
		}
	}

	runtimeResult, err := repository.startSafeWritebackScoped(ctx, scope, command)
	if err != nil {
		return changedispatch.Result{}, false, err
	}
	if runtimeResult.Replayed && !found {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_RUNTIME_ORPHANED", false, errors.New("workflow exists without its approval"))
	}
	if _, err := gormApprovalDispatchExec(ctx, transaction, `
		INSERT INTO change_control.proposal_revision_dispatch(
			workspace_id,proposal_id,revision_id,approval_id,workflow_run_id,created_at
		) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?)`,
		persisted.workspaceID, string(command.Approval.ProposalID), string(command.Approval.RevisionID), string(command.Approval.ID), string(runtimeResult.Run.ID), command.Approval.DecidedAt.UTC()); err != nil {
		return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_DISPATCH_BINDING_CREATE_FAILED")
	}

	if !found {
		changed, err := gormApprovalDispatchExec(ctx, transaction, `
			UPDATE change_control.proposal
			SET status=?,workflow_run_id=COALESCE(workflow_run_id,?::uuid),updated_at=?,version=version+1
			WHERE id=?::uuid AND status=? AND version=?
			  AND (current_revision_id=?::uuid OR current_revision_id IS NULL)`,
			string(domain.StatusApproved), string(runtimeResult.Run.ID), command.Approval.DecidedAt.UTC(), string(command.Approval.ProposalID),
			string(domain.StatusReady), persisted.version, string(command.Approval.RevisionID))
		if err != nil {
			return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_DISPATCH_PROPOSAL_UPDATE_FAILED")
		}
		if changed != 1 {
			return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
		}
	} else if persisted.legacyWorkflowRunID == nil {
		changed, err := gormApprovalDispatchExec(ctx, transaction, `
			UPDATE change_control.proposal
			SET workflow_run_id=COALESCE(workflow_run_id,?::uuid),updated_at=CURRENT_TIMESTAMP,version=version+1
			WHERE id=?::uuid AND status=? AND version=?
			  AND (current_revision_id=?::uuid OR current_revision_id IS NULL)`,
			string(runtimeResult.Run.ID), string(command.Approval.ProposalID), string(domain.StatusApproved), persisted.version, string(command.Approval.RevisionID))
		if err != nil {
			return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_DISPATCH_PROPOSAL_BIND_FAILED")
		}
		if changed != 1 {
			return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("proposal workflow binding changed"))
		}
	}
	return dispatchResult(command.Approval, runtimeResult, changedispatch.StatusQueued, false), false, nil
}

func (repository *GORMApprovalDispatchRepository) persistRejectedScoped(ctx context.Context, scope foundation.TransactionScope, transaction *gorm.DB, command changedispatch.Command, persisted gormApprovalDispatchProposal, replayed bool) (changedispatch.Result, bool, error) {
	if persisted.revisionWorkflowRunID != nil {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("rejected proposal is bound to a workflow"))
	}
	if replayed {
		if persisted.status != string(domain.StatusRejected) {
			return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("rejected approval status differs"))
		}
		if repository.events == nil {
			return changedispatch.Result{Approval: command.Approval, Replayed: true}, true, nil
		}
		if err := repository.appendRejectedEventScoped(ctx, scope, command, foundation.ID(persisted.workspaceID), persisted.version); err != nil {
			return changedispatch.Result{}, false, err
		}
		return changedispatch.Result{Approval: command.Approval, Replayed: true}, false, nil
	}
	if persisted.status != string(domain.StatusReady) {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}
	if command.Approval.ApprovedGitHead != nil {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_REJECTED_GIT_HEAD_INVALID", false, errors.New("rejected approval cannot bind a git head"))
	}
	if _, err := gormApprovalDispatchExec(ctx, transaction, `
		INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
		VALUES(?::uuid,?::uuid,?::uuid,?,'REJECTED',NULL,?)`,
		string(command.Approval.ID), string(command.Approval.ProposalID), string(command.Approval.RevisionID), command.Approval.ChangeHash, command.Approval.DecidedAt.UTC()); err != nil {
		return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_CREATE_FAILED")
	}
	changed, err := gormApprovalDispatchExec(ctx, transaction, `
		UPDATE change_control.proposal
		SET status=?,updated_at=?,version=version+1
		WHERE id=?::uuid AND status=? AND version=? AND (current_revision_id=?::uuid OR current_revision_id IS NULL)`,
		string(domain.StatusRejected), command.Approval.DecidedAt.UTC(), string(command.Approval.ProposalID), string(domain.StatusReady), persisted.version, string(command.Approval.RevisionID))
	if err != nil {
		return changedispatch.Result{}, false, classifyGORMApprovalDispatch(ctx, err, "PROPOSAL_DECISION_UPDATE_FAILED")
	}
	if changed != 1 {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
	}
	if err := repository.appendRejectedEventScoped(ctx, scope, command, foundation.ID(persisted.workspaceID), persisted.version+1); err != nil {
		return changedispatch.Result{}, false, err
	}
	return changedispatch.Result{Approval: command.Approval}, false, nil
}

func (repository *GORMApprovalDispatchRepository) replayApprovedScoped(ctx context.Context, scope foundation.TransactionScope, command changedispatch.Command, boundRunID foundation.ID) (changedispatch.Result, bool, error) {
	runtimeResult, err := repository.startSafeWritebackScoped(ctx, scope, command)
	if err != nil {
		return changedispatch.Result{}, false, err
	}
	if !runtimeResult.Replayed || runtimeResult.Run.ID != boundRunID {
		return changedispatch.Result{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("proposal workflow binding differs from runtime replay"))
	}
	return dispatchResult(command.Approval, runtimeResult, changedispatch.StatusReplayed, true), false, nil
}

func (repository *GORMApprovalDispatchRepository) appendRejectedEventScoped(ctx context.Context, scope foundation.TransactionScope, command changedispatch.Command, workspaceID foundation.ID, proposalVersion int64) error {
	if repository.events == nil {
		return nil
	}
	request := changecontroleventcontract.ProposalStatusRequest(
		workspaceID, command.Approval.ProposalID, command.Approval.ID,
		changecontroleventcontract.ProposalRejectedEventType, string(domain.StatusRejected), proposalVersion, command.Approval.DecidedAt,
	)
	_, _, err := repository.events.AppendScoped(ctx, scope, request)
	return err
}

func (repository *GORMApprovalDispatchRepository) startSafeWritebackScoped(ctx context.Context, scope foundation.TransactionScope, command changedispatch.Command) (workflowapplication.RuntimeStartResult, error) {
	input, err := changecontrolworkflow.EncodeBootstrapInput(command.Approval.ProposalID, command.Approval.RevisionID, command.Approval.ChangeHash)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	request, err := workflowapplication.BuildRuntimeStartRequest(repository.ids, repository.clock, command.WorkspaceID, "safe-writeback-approval:"+string(command.Approval.ID), input, changecontrolworkflow.RegisteredDefinition())
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	result, err := repository.runtime.StartScoped(ctx, scope, request)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	if result.FirstNode.InputSchemaVersion != changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion || result.FirstNode.OutputSchemaVersion != changecontrolworkflow.SafeWritebackOutputSchemaVersion || result.FirstNode.DispatchNo != approvalDispatchNo || result.FirstNode.NodeKey != changecontrolworkflow.SafeWritebackNodeKey || result.FirstNode.NodeType != changecontrolworkflow.SafeWritebackNodeKind || !jsonEqualDispatch(result.FirstNode.Input, input) {
		return workflowapplication.RuntimeStartResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_NODE_BINDING_CONFLICT", false, errors.New("safe writeback node binding differs"))
	}
	return result, nil
}

func (repository *GORMApprovalDispatchRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMApprovalDispatchDatabase(repository.database) || isNilDispatchDependency(repository.unitOfWork) ||
		isNilDispatchDependency(repository.runtime) || isNilDispatchDependency(repository.ids) || isNilDispatchDependency(repository.clock) {
		return gormApprovalDispatchUnavailable(errors.New("approval dispatch GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_INVALID", false, errors.New("approval dispatch context is nil"))
	}
	return nil
}

func normalizeGORMApprovalDispatchCommand(command changedispatch.Command) changedispatch.Command {
	command.Approval.ChangeHash = strings.ToLower(command.Approval.ChangeHash)
	command.ObservedBaseHash = strings.ToLower(strings.TrimSpace(command.ObservedBaseHash))
	command.ObservedGitHead = strings.ToLower(strings.TrimSpace(command.ObservedGitHead))
	if command.Approval.ApprovedGitHead != nil {
		head := strings.ToLower(strings.TrimSpace(*command.Approval.ApprovedGitHead))
		command.Approval.ApprovedGitHead = &head
	}
	return command
}

type gormApprovalDispatchProposal struct {
	workspaceID                    string
	proposalType                   string
	status                         string
	version                        int64
	legacyWorkflowRunID            *string
	revisionWorkflowRunID          *string
	revisionHash                   string
	targetPath                     string
	targetMode                     string
	baseHash                       string
	restoreDocumentID              *string
	restoreExpectedHead            *string
	restoreExpectedDocumentVersion *int64
	restoreCurrentContentHash      *string
}

func (value gormApprovalDispatchProposal) validate(command changedispatch.Command, proposalType domain.ProposalType) error {
	if proposalType == domain.ProposalTypeRestoreDocument {
		if value.restoreDocumentID == nil || value.restoreExpectedHead == nil || value.restoreExpectedDocumentVersion == nil || value.restoreCurrentContentHash == nil ||
			!domain.ValidGitObjectID(*value.restoreExpectedHead) || !domain.ValidHash(*value.restoreCurrentContentHash) || !strings.EqualFold(*value.restoreCurrentContentHash, value.baseHash) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "DOCUMENT_RESTORE_BINDING_INVALID", false, errors.New("restore revision binding is incomplete"))
		}
	} else if value.restoreDocumentID != nil || value.restoreExpectedHead != nil || value.restoreExpectedDocumentVersion != nil || value.restoreCurrentContentHash != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("file patch carries restore fields"))
	}
	if value.workspaceID != string(command.WorkspaceID) || !strings.EqualFold(value.revisionHash, command.Approval.ChangeHash) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("proposal revision binding differs"))
	}
	return nil
}

func gormApprovalDispatchLockProposalRevision(ctx context.Context, database *gorm.DB, proposalID, revisionID foundation.ID) (gormApprovalDispatchProposal, error) {
	row, err := gormApprovalDispatchRawRow(ctx, database, `
		SELECT p.workspace_id::text,p.proposal_type,p.status,p.version,p.workflow_run_id::text,d.workflow_run_id::text,
		       r.change_hash,r.target_path,r.target_mode,r.base_hash,
		       r.restore_document_id::text,r.restore_expected_head,r.restore_expected_document_version,r.restore_current_content_hash
		  FROM change_control.proposal AS p
		  JOIN change_control.proposal_revision AS r ON r.proposal_id=p.id AND r.id=?::uuid
		  LEFT JOIN change_control.proposal_revision_dispatch AS d ON d.proposal_id=p.id AND d.revision_id=r.id
		 WHERE p.id=?::uuid AND (p.current_revision_id=r.id OR p.current_revision_id IS NULL)
		 FOR UPDATE OF p,r`, string(revisionID), string(proposalID))
	if err != nil {
		return gormApprovalDispatchProposal{}, err
	}
	var value gormApprovalDispatchProposal
	err = row.Scan(&value.workspaceID, &value.proposalType, &value.status, &value.version, &value.legacyWorkflowRunID, &value.revisionWorkflowRunID,
		&value.revisionHash, &value.targetPath, &value.targetMode, &value.baseHash, &value.restoreDocumentID, &value.restoreExpectedHead,
		&value.restoreExpectedDocumentVersion, &value.restoreCurrentContentHash)
	return value, err
}

func gormApprovalDispatchLoadApproval(ctx context.Context, database *gorm.DB, revisionID foundation.ID) (domain.Approval, bool, error) {
	row, err := gormApprovalDispatchRawRow(ctx, database, `
		SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at
		  FROM change_control.approval WHERE revision_id=?::uuid FOR UPDATE`, string(revisionID))
	if err != nil {
		return domain.Approval{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_DISPATCH_APPROVAL_QUERY_FAILED")
	}
	var approval domain.Approval
	var id, proposalID, persistedRevisionID, decision string
	if err := row.Scan(&id, &proposalID, &persistedRevisionID, &approval.ChangeHash, &decision, &approval.ApprovedGitHead, &approval.DecidedAt); gormApprovalDispatchNoRows(err) {
		return domain.Approval{}, false, nil
	} else if err != nil {
		return domain.Approval{}, false, classifyGORMApprovalDispatch(ctx, err, "APPROVAL_DISPATCH_APPROVAL_QUERY_FAILED")
	}
	approval.ID, approval.ProposalID, approval.RevisionID = foundation.ID(id), foundation.ID(proposalID), foundation.ID(persistedRevisionID)
	approval.Decision, approval.DecidedAt = domain.Decision(decision), approval.DecidedAt.UTC()
	return approval, true, nil
}

func gormApprovalDispatchValidateRestore(ctx context.Context, database *gorm.DB, command changedispatch.Command, persisted gormApprovalDispatchProposal) error {
	row, err := gormApprovalDispatchRawRow(ctx, database, `
		SELECT canonical_path,lifecycle_status,COALESCE(current_published_revision_id::text,''),version
		  FROM core.document WHERE workspace_id=?::uuid AND id=?::uuid FOR SHARE`, persisted.workspaceID, *persisted.restoreDocumentID)
	if err != nil {
		return classifyGORMApprovalDispatch(ctx, err, "DOCUMENT_RESTORE_BINDING_QUERY_FAILED")
	}
	var documentPath, lifecycle, currentRevisionID string
	var documentVersion int64
	if err := row.Scan(&documentPath, &lifecycle, &currentRevisionID, &documentVersion); gormApprovalDispatchNoRows(err) {
		return foundation.NewError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", false, err)
	} else if err != nil {
		return classifyGORMApprovalDispatch(ctx, err, "DOCUMENT_RESTORE_BINDING_QUERY_FAILED")
	}
	if documentPath != persisted.targetPath || lifecycle != "PUBLISHED" || currentRevisionID == "" || documentVersion != *persisted.restoreExpectedDocumentVersion ||
		!strings.EqualFold(command.ObservedGitHead, *persisted.restoreExpectedHead) || !strings.EqualFold(command.ObservedBaseHash, *persisted.restoreCurrentContentHash) {
		return foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("restore baseline changed before approval"))
	}
	return nil
}

func validGORMApprovalDispatchDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil && database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func gormApprovalDispatchRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGORMApprovalDispatchDatabase(database) {
		return nil, errors.New("approval dispatch GORM query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("approval dispatch GORM query returned no row handle")
	}
	return row, nil
}

func gormApprovalDispatchExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validGORMApprovalDispatchDatabase(database) {
		return 0, errors.New("approval dispatch GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	return result.RowsAffected, result.Error
}

func gormApprovalDispatchNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMApprovalDispatch(ctx context.Context, err error, code string) error {
	if err == nil {
		return nil
	}
	var known *foundation.Error
	if errors.As(err, &known) {
		return err
	}
	if cause := gormApprovalDispatchContextCause(ctx, err); cause != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
	}
	return classifyDispatch(err, code)
}

func gormApprovalDispatchContextCause(ctx context.Context, err error) error {
	causes := make([]error, 0, 2)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		causes = append(causes, err)
	}
	if ctx != nil && ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		} else if !errors.Is(cause, ctx.Err()) {
			cause = errors.Join(ctx.Err(), cause)
		}
		causes = append(causes, cause)
	}
	if len(causes) == 0 {
		return nil
	}
	return errors.Join(causes...)
}

func gormApprovalDispatchUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "APPROVAL_DISPATCH_DEPENDENCY_MISSING", true, cause)
}
