package application

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// TargetReader 从服务端受控的 Workspace 边界读取目标文件当前哈希。
type TargetReader interface {
	CurrentHash(context.Context, foundation.ID, string) (string, error)
}

// Service 协调 Proposal、Approval 与无副作用 Apply 前置校验。
type Service struct {
	repo    domain.Repository
	ids     foundation.IDGenerator
	clock   foundation.Clock
	targets TargetReader
}

// NewService 创建 Change Control 应用服务。
func NewService(repo domain.Repository, ids foundation.IDGenerator, clock foundation.Clock, targets TargetReader) (*Service, error) {
	if repo == nil || ids == nil || clock == nil || targets == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DEPENDENCY_MISSING", false, errors.New("change control dependency missing"))
	}
	return &Service{repo: repo, ids: ids, clock: clock, targets: targets}, nil
}

// CreateCommand 是创建第一版 Proposal 所需的完整变更快照。
type CreateCommand struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	BaseHash        string
	Content         string
	EvidenceSummary string
	Risk            string
	RollbackPlan    string
}

// CreateProposal 校验并原子创建 ready_for_review Proposal 和 Revision。
func (s *Service) CreateProposal(ctx context.Context, command CreateCommand) (domain.Proposal, error) {
	targetPath, pathErr := domain.ValidateTargetPath(command.TargetPath)
	if command.WorkspaceID == "" || pathErr != nil || !domain.ValidHash(command.BaseHash) || strings.TrimSpace(command.Content) == "" || strings.TrimSpace(command.EvidenceSummary) == "" || strings.TrimSpace(command.Risk) == "" || strings.TrimSpace(command.RollbackPlan) == "" {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal fields are invalid"))
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return domain.Proposal{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return domain.Proposal{}, err
	}
	now := s.clock.Now()
	baseHash := strings.ToLower(command.BaseHash)
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
		BaseHash: baseHash, Content: command.Content, EvidenceSummary: strings.TrimSpace(command.EvidenceSummary),
		Risk: strings.TrimSpace(command.Risk), RollbackPlan: strings.TrimSpace(command.RollbackPlan),
		ChangeHash: domain.ComputeChangeHash(targetPath, baseHash, command.Content), CreatedAt: now,
	}
	return s.repo.CreateProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, TargetPath: targetPath,
		Status: domain.StatusReady, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
}

// GetProposal 返回 Proposal 当前 Revision 与已有审批决定。
func (s *Service) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	if proposalID == "" {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_ID_INVALID", false, errors.New("proposal id is required"))
	}
	return s.repo.GetProposal(ctx, proposalID)
}

// DecideProposal 由服务端生成 Approval ID，并绑定 Revision 与 Change Hash。
func (s *Service) DecideProposal(ctx context.Context, proposalID, revisionID foundation.ID, changeHash string, decision domain.Decision) (domain.Approval, error) {
	if proposalID == "" || revisionID == "" || !domain.ValidHash(changeHash) || decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return domain.Approval{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_INVALID", false, errors.New("approval fields are invalid"))
	}
	approvalID, err := s.ids.New()
	if err != nil {
		return domain.Approval{}, err
	}
	return s.repo.Approve(ctx, domain.Approval{
		ID: approvalID, ProposalID: proposalID, RevisionID: revisionID,
		ChangeHash: strings.ToLower(changeHash), Decision: decision, DecidedAt: s.clock.Now(),
	})
}

// ApplyPreflightResult 仅表示服务端当前检查通过，不代表已写回或已签发写权限。
type ApplyPreflightResult struct {
	ProposalID foundation.ID
	RevisionID foundation.ID
	ChangeHash string
	BaseHash   string
}

// HashConflict 提供不含绝对路径的版本冲突详情。
type HashConflict struct {
	Expected string
	Current  string
}

func (e *HashConflict) Error() string { return "target base hash changed" }

// CheckApplyPreflight 从服务端读取真实目标哈希，并校验审批绑定和基线。
// Safe Writeback seam 在真正写入前仍必须再次执行同等校验和 Git 检查。
func (s *Service) CheckApplyPreflight(ctx context.Context, proposalID, revisionID foundation.ID, approvedChangeHash string) (ApplyPreflightResult, error) {
	if proposalID == "" || revisionID == "" || !domain.ValidHash(approvedChangeHash) {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorInvalidInput, "APPLY_PREFLIGHT_INVALID", false, errors.New("apply preflight fields are invalid"))
	}
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return ApplyPreflightResult{}, err
	}
	if proposal.Status != domain.StatusApproved || proposal.Approval == nil || proposal.Approval.Decision != domain.DecisionApproved {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "PROPOSAL_NOT_APPROVED", false, errors.New("proposal has no approved decision"))
	}
	if proposal.Revision.ID != revisionID || proposal.Approval.RevisionID != revisionID {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	approvedChangeHash = strings.ToLower(approvedChangeHash)
	if proposal.Revision.ChangeHash != approvedChangeHash || proposal.Approval.ChangeHash != approvedChangeHash {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("approved hash does not match revision"))
	}
	if expectedChangeHash := domain.ComputeChangeHash(proposal.Revision.TargetPath, proposal.Revision.BaseHash, proposal.Revision.Content); proposal.Revision.ChangeHash != expectedChangeHash {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_INVALID", false, errors.New("revision change hash is not reproducible"))
	}
	currentBaseHash, err := s.targets.CurrentHash(ctx, proposal.WorkspaceID, proposal.TargetPath)
	if err != nil {
		return ApplyPreflightResult{}, err
	}
	currentBaseHash = strings.ToLower(currentBaseHash)
	if proposal.Revision.BaseHash != currentBaseHash {
		if markErr := s.repo.MarkNeedsRevision(ctx, proposal.ID, s.clock.Now()); markErr != nil {
			return ApplyPreflightResult{}, markErr
		}
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: currentBaseHash})
	}
	return ApplyPreflightResult{
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, BaseHash: proposal.Revision.BaseHash,
	}, nil
}
