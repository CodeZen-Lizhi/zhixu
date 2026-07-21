// Package approvaldispatchpostgres implements the cross-schema Approval dispatch UoW.
package approvaldispatchpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changedispatch "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/dispatch"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const approvalDispatchNo = 1

type approvalRuntimeStarter interface {
	StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error)
}

// ApprovalDispatchRepository 跨 Change Control、Workflow 与 River 维护单一 Approval Dispatch UoW。
type ApprovalDispatchRepository struct {
	db      changecontrolpostgres.DB
	runtime approvalRuntimeStarter
	ids     foundation.IDGenerator
	clock   foundation.Clock
}

var _ changedispatch.Dispatcher = (*ApprovalDispatchRepository)(nil)

// NewApprovalDispatchRepository 创建跨 Schema Approval Dispatch Repository。
func NewApprovalDispatchRepository(db changecontrolpostgres.DB, runtime approvalRuntimeStarter, ids foundation.IDGenerator, clock foundation.Clock) (*ApprovalDispatchRepository, error) {
	if isNilDispatchDependency(db) || isNilDispatchDependency(runtime) || isNilDispatchDependency(ids) || isNilDispatchDependency(clock) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "APPROVAL_DISPATCH_DEPENDENCY_MISSING", true, errors.New("approval dispatch dependency is nil"))
	}
	return &ApprovalDispatchRepository{db: db, runtime: runtime, ids: ids, clock: clock}, nil
}

// DecideAndDispatch 原子保存或重放 Approval、Proposal→Run binding、Workflow facts、Outbox 与 River Job。
func (r *ApprovalDispatchRepository) DecideAndDispatch(ctx context.Context, command changedispatch.Command) (changedispatch.Result, error) {
	if err := validateApprovalDispatchCommand(command); err != nil {
		return changedispatch.Result{}, err
	}
	command.Approval.ChangeHash = strings.ToLower(command.Approval.ChangeHash)
	command.ObservedBaseHash = strings.ToLower(strings.TrimSpace(command.ObservedBaseHash))
	command.ObservedGitHead = strings.ToLower(strings.TrimSpace(command.ObservedGitHead))
	if command.Approval.ApprovedGitHead != nil {
		head := strings.ToLower(strings.TrimSpace(*command.Approval.ApprovedGitHead))
		command.Approval.ApprovedGitHead = &head
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_DISPATCH_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID, proposalType, status, revisionHash, baseHash string
	var workflowRunID *string
	err = tx.QueryRow(ctx, `
		SELECT p.workspace_id::text,p.proposal_type,p.status,p.workflow_run_id::text,r.change_hash,r.base_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		WHERE p.id=$1
		FOR UPDATE OF p,r`, string(command.Approval.ProposalID), string(command.Approval.RevisionID)).Scan(&workspaceID, &proposalType, &status, &workflowRunID, &revisionHash, &baseHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_DISPATCH_PROPOSAL_QUERY_FAILED")
	}
	if domain.NormalizeProposalType(domain.ProposalType(proposalType)) != domain.ProposalTypeFilePatch {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_PROPOSAL_TYPE_UNSUPPORTED", false, errors.New("approval dispatch only supports file patch proposals"))
	}
	if workspaceID != string(command.WorkspaceID) || !strings.EqualFold(revisionHash, command.Approval.ChangeHash) {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("proposal revision binding differs"))
	}

	existing, existingErr := getDispatchApproval(ctx, tx, command.Approval.RevisionID)
	if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
		return changedispatch.Result{}, classifyDispatch(existingErr, "APPROVAL_DISPATCH_APPROVAL_QUERY_FAILED")
	}
	if existingErr == nil {
		if !sameApprovalDecision(existing, command.Approval) {
			return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("approval decision binding differs"))
		}
		command.Approval = existing
	}

	if command.Approval.Decision == domain.DecisionRejected {
		return r.persistRejected(ctx, tx, command, status, workflowRunID, existingErr == nil)
	}
	if workflowRunID != nil {
		return r.replayApproved(ctx, tx, command, foundation.ID(*workflowRunID))
	}
	if err := validateApprovedDispatchSafety(command, baseHash); err != nil {
		return changedispatch.Result{}, err
	}
	if existingErr == nil {
		if status != string(domain.StatusApproved) {
			return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_DISPATCH", false, errors.New("historical approval is not dispatchable"))
		}
	} else if status != string(domain.StatusReady) {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}

	if existingErr != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, string(command.Approval.ID), string(command.Approval.ProposalID), string(command.Approval.RevisionID), command.Approval.ChangeHash, string(command.Approval.Decision), command.Approval.ApprovedGitHead, command.Approval.DecidedAt.UTC()); err != nil {
			return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_CREATE_FAILED")
		}
	}

	runtimeResult, err := r.startSafeWriteback(ctx, tx, command)
	if err != nil {
		return changedispatch.Result{}, err
	}
	if runtimeResult.Replayed && existingErr != nil {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_RUNTIME_ORPHANED", false, errors.New("workflow exists without its approval"))
	}
	if existingErr != nil {
		tag, updateErr := tx.Exec(ctx, `UPDATE change_control.proposal
			SET status=$2,workflow_run_id=$3,updated_at=$4,version=version+1
			WHERE id=$1 AND status=$5 AND workflow_run_id IS NULL`, string(command.Approval.ProposalID), string(domain.StatusApproved), string(runtimeResult.Run.ID), command.Approval.DecidedAt.UTC(), string(domain.StatusReady))
		if updateErr != nil {
			return changedispatch.Result{}, classifyDispatch(updateErr, "APPROVAL_DISPATCH_PROPOSAL_UPDATE_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
		}
	} else {
		tag, updateErr := tx.Exec(ctx, `UPDATE change_control.proposal
			SET workflow_run_id=$2,updated_at=CURRENT_TIMESTAMP,version=version+1
			WHERE id=$1 AND status=$3 AND workflow_run_id IS NULL`, string(command.Approval.ProposalID), string(runtimeResult.Run.ID), string(domain.StatusApproved))
		if updateErr != nil {
			return changedispatch.Result{}, classifyDispatch(updateErr, "APPROVAL_DISPATCH_PROPOSAL_BIND_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("proposal workflow binding changed"))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_DISPATCH_COMMIT_FAILED")
	}
	return dispatchResult(command.Approval, runtimeResult, changedispatch.StatusQueued, false), nil
}

func (r *ApprovalDispatchRepository) persistRejected(ctx context.Context, tx pgx.Tx, command changedispatch.Command, status string, workflowRunID *string, replayed bool) (changedispatch.Result, error) {
	if workflowRunID != nil {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("rejected proposal is bound to a workflow"))
	}
	if replayed {
		if status != string(domain.StatusRejected) {
			return changedispatch.Result{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("rejected approval status differs"))
		}
		if err := tx.Commit(ctx); err != nil {
			return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_DISPATCH_COMMIT_FAILED")
		}
		return changedispatch.Result{Approval: command.Approval, Replayed: true}, nil
	}
	if status != string(domain.StatusReady) {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}
	if command.Approval.ApprovedGitHead != nil {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_REJECTED_GIT_HEAD_INVALID", false, errors.New("rejected approval cannot bind a git head"))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
		VALUES($1,$2,$3,$4,$5,NULL,$6)`, string(command.Approval.ID), string(command.Approval.ProposalID), string(command.Approval.RevisionID), command.Approval.ChangeHash, string(command.Approval.Decision), command.Approval.DecidedAt.UTC()); err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_CREATE_FAILED")
	}
	tag, err := tx.Exec(ctx, `UPDATE change_control.proposal SET status=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status=$4 AND workflow_run_id IS NULL`, string(command.Approval.ProposalID), string(domain.StatusRejected), command.Approval.DecidedAt.UTC(), string(domain.StatusReady))
	if err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "PROPOSAL_DECISION_UPDATE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
	}
	if err := tx.Commit(ctx); err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_DISPATCH_COMMIT_FAILED")
	}
	return changedispatch.Result{Approval: command.Approval}, nil
}

func (r *ApprovalDispatchRepository) replayApproved(ctx context.Context, tx pgx.Tx, command changedispatch.Command, boundRunID foundation.ID) (changedispatch.Result, error) {
	runtimeResult, err := r.startSafeWriteback(ctx, tx, command)
	if err != nil {
		return changedispatch.Result{}, err
	}
	if !runtimeResult.Replayed || runtimeResult.Run.ID != boundRunID {
		return changedispatch.Result{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_BINDING_CONFLICT", false, errors.New("proposal workflow binding differs from runtime replay"))
	}
	if err := tx.Commit(ctx); err != nil {
		return changedispatch.Result{}, classifyDispatch(err, "APPROVAL_DISPATCH_COMMIT_FAILED")
	}
	return dispatchResult(command.Approval, runtimeResult, changedispatch.StatusReplayed, true), nil
}

func (r *ApprovalDispatchRepository) startSafeWriteback(ctx context.Context, tx pgx.Tx, command changedispatch.Command) (workflowapplication.RuntimeStartResult, error) {
	input, err := changecontrolworkflow.EncodeBootstrapInput(command.Approval.ProposalID, command.Approval.RevisionID, command.Approval.ChangeHash)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	request, err := workflowapplication.BuildRuntimeStartRequest(r.ids, r.clock, command.WorkspaceID, "safe-writeback-approval:"+string(command.Approval.ID), input, changecontrolworkflow.RegisteredDefinition())
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	result, err := r.runtime.StartTx(ctx, tx, request)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	if result.FirstNode.InputSchemaVersion != changecontrolworkflow.SafeWritebackBootstrapInputSchemaVersion || result.FirstNode.OutputSchemaVersion != changecontrolworkflow.SafeWritebackOutputSchemaVersion || result.FirstNode.DispatchNo != approvalDispatchNo || result.FirstNode.NodeKey != changecontrolworkflow.SafeWritebackNodeKey || result.FirstNode.NodeType != changecontrolworkflow.SafeWritebackNodeKind || !jsonEqualDispatch(result.FirstNode.Input, input) {
		return workflowapplication.RuntimeStartResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_DISPATCH_NODE_BINDING_CONFLICT", false, errors.New("safe writeback node binding differs"))
	}
	return result, nil
}

func validateApprovalDispatchCommand(command changedispatch.Command) error {
	approval := command.Approval
	if command.WorkspaceID == "" || approval.ID == "" || approval.ProposalID == "" || approval.RevisionID == "" || !domain.ValidHash(approval.ChangeHash) || approval.DecidedAt.IsZero() || approval.Decision != domain.DecisionApproved && approval.Decision != domain.DecisionRejected {
		return foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_DISPATCH_INVALID", false, errors.New("approval dispatch command is invalid"))
	}
	return nil
}

func validateApprovedDispatchSafety(command changedispatch.Command, persistedBaseHash string) error {
	if command.Approval.ApprovedGitHead == nil || !domain.ValidGitHead(*command.Approval.ApprovedGitHead) || !domain.ValidHash(command.ObservedBaseHash) || !domain.ValidGitHead(command.ObservedGitHead) || !strings.EqualFold(command.ObservedBaseHash, persistedBaseHash) || !strings.EqualFold(command.ObservedGitHead, *command.Approval.ApprovedGitHead) {
		return foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_DISPATCH_SAFETY_CONFLICT", false, errors.New("approval safety observations do not match persisted facts"))
	}
	return nil
}

func sameApprovalDecision(existing, requested domain.Approval) bool {
	return existing.ProposalID == requested.ProposalID && existing.RevisionID == requested.RevisionID && strings.EqualFold(existing.ChangeHash, requested.ChangeHash) && existing.Decision == requested.Decision && sameDispatchOptionalString(existing.ApprovedGitHead, requested.ApprovedGitHead)
}

func sameDispatchOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(strings.TrimSpace(*left), strings.TrimSpace(*right))
}

func dispatchResult(approval domain.Approval, runtime workflowapplication.RuntimeStartResult, status changedispatch.Status, replayed bool) changedispatch.Result {
	return changedispatch.Result{Approval: approval, WorkflowRunID: runtime.Run.ID, NodeRunID: runtime.FirstNode.ID, JobID: runtime.Job.JobID, DispatchStatus: status, Replayed: replayed}
}

func jsonEqualDispatch(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func isNilDispatchDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func getDispatchApproval(ctx context.Context, tx pgx.Tx, revisionID foundation.ID) (domain.Approval, error) {
	var approval domain.Approval
	var id, proposalID, persistedRevisionID, decision string
	if err := tx.QueryRow(ctx, `SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at FROM change_control.approval WHERE revision_id=$1 FOR UPDATE`, string(revisionID)).Scan(&id, &proposalID, &persistedRevisionID, &approval.ChangeHash, &decision, &approval.ApprovedGitHead, &approval.DecidedAt); err != nil {
		return domain.Approval{}, err
	}
	approval.ID, approval.ProposalID, approval.RevisionID = foundation.ID(id), foundation.ID(proposalID), foundation.ID(persistedRevisionID)
	approval.Decision = domain.Decision(decision)
	return approval, nil
}

func classifyDispatch(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23503", "23505", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
