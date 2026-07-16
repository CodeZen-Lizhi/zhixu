// Package postgres persists Change Control aggregates in PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Repository 所需的最小 pgx 边界。
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 是 Change Control 的 PostgreSQL 实现。
type Repository struct{ db DB }

// NewRepository 创建 PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	return &Repository{db: db}, nil
}

// CreateProposal 在同一事务中创建 Proposal 和不可变 Revision。
func (r *Repository) CreateProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO change_control.proposal(id,workspace_id,idempotency_key,request_hash,status,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingID, requestHash string
		if queryErr := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM change_control.proposal WHERE workspace_id=$1 AND idempotency_key=$2`, string(proposal.WorkspaceID), proposal.IdempotencyKey).Scan(&existingID, &requestHash); queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if requestHash != proposal.RequestHash {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, foundation.ID(existingID))
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.TargetPath,
		revision.BaseHash, revision.Content, revision.EvidenceSummary, revision.Risk,
		revision.RollbackPlan, revision.ChangeHash, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	return proposal, nil
}

// GetProposal 使用单条查询返回一致的 Proposal、Revision 和 Approval 快照。
func (r *Repository) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	row := r.db.QueryRow(ctx, `
		SELECT p.id::text,p.workspace_id::text,p.idempotency_key,p.request_hash,p.status,p.created_at,p.updated_at,
			r.id::text,r.revision_no,r.target_path,r.base_hash,r.content,r.evidence_summary,r.risk,r.rollback_plan,r.change_hash,r.created_at,
			a.id::text,a.change_hash,a.decision,a.decided_at
		FROM change_control.proposal p
		JOIN LATERAL (
			SELECT id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
			FROM change_control.proposal_revision
			WHERE proposal_id=p.id ORDER BY revision_no DESC LIMIT 1
		) r ON true
		LEFT JOIN change_control.approval a ON a.revision_id=r.id
		WHERE p.id=$1`, string(proposalID))
	proposal, err := scanProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_QUERY_FAILED")
	}
	return proposal, nil
}

// Approve 只允许 ready_for_review Proposal 被一次性批准或拒绝。
func (r *Repository) Approve(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status, revisionHash string
	err = tx.QueryRow(ctx, `
		SELECT p.status,r.change_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		WHERE p.id=$1 FOR UPDATE`, string(approval.ProposalID), string(approval.RevisionID)).Scan(&status, &revisionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_QUERY_FAILED")
	}
	if status != string(domain.StatusReady) {
		existing, existingErr := getApproval(ctx, tx, approval.RevisionID)
		if existingErr == nil && existing.ProposalID == approval.ProposalID && existing.ChangeHash == approval.ChangeHash && existing.Decision == approval.Decision {
			return existing, nil
		}
		if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
			return domain.Approval{}, classify(existingErr, "APPROVAL_QUERY_FAILED")
		}
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}
	if revisionHash != approval.ChangeHash {
		return domain.Approval{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("change hash mismatch"))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,decided_at)
		VALUES($1,$2,$3,$4,$5,$6)`, string(approval.ID), string(approval.ProposalID), string(approval.RevisionID), approval.ChangeHash, string(approval.Decision), approval.DecidedAt.UTC()); err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_CREATE_FAILED")
	}
	newStatus := domain.StatusRejected
	if approval.Decision == domain.DecisionApproved {
		newStatus = domain.StatusApproved
	}
	command, err := tx.Exec(ctx, `
		UPDATE change_control.proposal SET status=$1,updated_at=$2
		WHERE id=$3 AND status=$4`, string(newStatus), approval.DecidedAt.UTC(), string(approval.ProposalID), string(domain.StatusReady))
	if err != nil {
		return domain.Approval{}, classify(err, "PROPOSAL_DECISION_UPDATE_FAILED")
	}
	if command.RowsAffected() != 1 {
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_COMMIT_FAILED")
	}
	return approval, nil
}

// MarkNeedsRevision 记录服务端观察到的目标基线冲突。
func (r *Repository) MarkNeedsRevision(ctx context.Context, proposalID foundation.ID, at time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return classify(err, "PROPOSAL_STATE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `
		UPDATE change_control.proposal SET status=$1,updated_at=$2
		WHERE id=$3 AND status IN ($4,$5)`, string(domain.StatusNeedsRevision), at.UTC(), string(proposalID), string(domain.StatusApproved), string(domain.StatusReady))
	if err != nil {
		return classify(err, "PROPOSAL_STATE_UPDATE_FAILED")
	}
	if command.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
	}
	return tx.Commit(ctx)
}

func scanProposal(row pgx.Row) (domain.Proposal, error) {
	var proposalID, workspaceID, idempotencyKey, requestHash, status string
	var revisionID, targetPath, baseHash, content, evidence, risk, rollback, changeHash string
	var approvalID, approvalHash, decision *string
	var createdAt, updatedAt, revisionCreatedAt time.Time
	var decidedAt *time.Time
	var revisionNo int
	err := row.Scan(
		&proposalID, &workspaceID, &idempotencyKey, &requestHash, &status, &createdAt, &updatedAt,
		&revisionID, &revisionNo, &targetPath, &baseHash, &content, &evidence, &risk, &rollback, &changeHash, &revisionCreatedAt,
		&approvalID, &approvalHash, &decision, &decidedAt,
	)
	if err != nil {
		return domain.Proposal{}, err
	}
	proposal := domain.Proposal{
		ID: foundation.ID(proposalID), WorkspaceID: foundation.ID(workspaceID), TargetPath: targetPath,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		Status: domain.ProposalStatus(status), CreatedAt: createdAt, UpdatedAt: updatedAt,
		Revision: domain.Revision{
			ID: foundation.ID(revisionID), ProposalID: foundation.ID(proposalID), RevisionNo: revisionNo,
			TargetPath: targetPath, BaseHash: baseHash, Content: content, EvidenceSummary: evidence,
			Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, CreatedAt: revisionCreatedAt,
		},
	}
	if approvalID != nil && approvalHash != nil && decision != nil && decidedAt != nil {
		proposal.Approval = &domain.Approval{
			ID: foundation.ID(*approvalID), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ChangeHash: *approvalHash, Decision: domain.Decision(*decision), DecidedAt: *decidedAt,
		}
	}
	return proposal, nil
}

func getApproval(ctx context.Context, row interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, revisionID foundation.ID) (domain.Approval, error) {
	var id, proposalID, revision, changeHash, decision string
	var decidedAt time.Time
	err := row.QueryRow(ctx, `SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,decided_at FROM change_control.approval WHERE revision_id=$1`, string(revisionID)).Scan(&id, &proposalID, &revision, &changeHash, &decision, &decidedAt)
	if err != nil {
		return domain.Approval{}, err
	}
	return domain.Approval{ID: foundation.ID(id), ProposalID: foundation.ID(proposalID), RevisionID: foundation.ID(revision), ChangeHash: changeHash, Decision: domain.Decision(decision), DecidedAt: decidedAt}, nil
}

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_ALREADY_DECIDED", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}
