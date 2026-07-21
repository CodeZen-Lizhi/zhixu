package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
)

// TargetReader 从服务端受控的 Workspace 边界读取目标文件当前哈希。
type TargetReader interface {
	CurrentHash(context.Context, foundation.ID, string) (string, error)
}

// ApprovalGitInspector 在批准时从服务端 Workspace 读取严格、干净且 attached 的 Git 基线。
// 调用方不能提供 HEAD；实现返回的快照是 Approval Git HEAD 的唯一来源。
type ApprovalGitInspector interface {
	CaptureApprovalSnapshot(context.Context, foundation.ID) (domain.GitSnapshot, error)
}

// Service 协调 Proposal、Approval 与无副作用 Apply 前置校验。
type Service struct {
	repo       domain.Repository
	ids        foundation.IDGenerator
	clock      foundation.Clock
	targets    TargetReader
	git        ApprovalGitInspector
	dispatcher ApprovalDispatcher
	// knowledgeApplier 是 typed knowledge_change 的唯一正式 Relation apply seam。
	// 为空时 file_patch 仍可用，但批准 knowledge_change 必须 fail closed。
	knowledgeApplier knowledgeapplication.ApprovedRelationApplyPort
}

// NewServiceWithDispatch 创建启用 Approval→Workflow/River 原子投递的 Change Control 应用服务。
func NewServiceWithDispatch(repo domain.Repository, ids foundation.IDGenerator, clock foundation.Clock, targets TargetReader, git ApprovalGitInspector, dispatcher ApprovalDispatcher, knowledgeAppliers ...knowledgeapplication.ApprovedRelationApplyPort) (*Service, error) {
	service, err := NewService(repo, ids, clock, targets, git, knowledgeAppliers...)
	if err != nil {
		return nil, err
	}
	if isNilApprovalDispatcher(dispatcher) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "APPROVAL_DISPATCHER_MISSING", false, errors.New("approval dispatcher is missing"))
	}
	service.dispatcher = dispatcher
	return service, nil
}

// MaxWriteAuthorizationTTL 是单次写权限的服务端有效期上限。
const MaxWriteAuthorizationTTL = 5 * time.Minute

// NewService 创建 Change Control 应用服务。
func NewService(repo domain.Repository, ids foundation.IDGenerator, clock foundation.Clock, targets TargetReader, git ApprovalGitInspector, knowledgeAppliers ...knowledgeapplication.ApprovedRelationApplyPort) (*Service, error) {
	if repo == nil || ids == nil || clock == nil || targets == nil || git == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DEPENDENCY_MISSING", false, errors.New("change control dependency missing"))
	}
	if len(knowledgeAppliers) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_RELATION_APPLIER_INVALID", false, errors.New("only one knowledge relation applier may be configured"))
	}
	service := &Service{repo: repo, ids: ids, clock: clock, targets: targets, git: git}
	if len(knowledgeAppliers) == 1 && !isNilKnowledgeRelationApplier(knowledgeAppliers[0]) {
		service.knowledgeApplier = knowledgeAppliers[0]
	}
	return service, nil
}

// CreateCommand 是创建第一版 Proposal 所需的完整变更快照。
type CreateCommand struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	IdempotencyKey  string
	BaseHash        string
	Content         string
	EvidenceSummary string
	Risk            string
	RollbackPlan    string
}

// CreateKnowledgeChangeCommand 是创建 `knowledge_change` Proposal 的结构化命令。
type CreateKnowledgeChangeCommand struct {
	WorkspaceID     foundation.ID
	IdempotencyKey  string
	KnowledgeChange domain.KnowledgeChange
	Risk            string
	RollbackPlan    string
}

// CreateResult 包含 Proposal 和是否命中已有幂等请求。
type CreateResult struct {
	Proposal domain.Proposal
	Replayed bool
}

// CreateProposal 校验并原子创建 ready_for_review Proposal 和 Revision。
func (s *Service) CreateProposal(ctx context.Context, command CreateCommand) (CreateResult, error) {
	targetPath, pathErr := domain.ValidateTargetPath(command.TargetPath)
	if command.WorkspaceID == "" || pathErr != nil || strings.TrimSpace(command.IdempotencyKey) == "" || len(strings.TrimSpace(command.IdempotencyKey)) > 128 || !domain.ValidHash(command.BaseHash) || strings.TrimSpace(command.Content) == "" || strings.TrimSpace(command.EvidenceSummary) == "" || strings.TrimSpace(command.Risk) == "" || strings.TrimSpace(command.RollbackPlan) == "" {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal fields are invalid"))
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	baseHash := strings.ToLower(command.BaseHash)
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
		BaseHash: baseHash, Content: command.Content, EvidenceSummary: strings.TrimSpace(command.EvidenceSummary),
		Risk: strings.TrimSpace(command.Risk), RollbackPlan: strings.TrimSpace(command.RollbackPlan),
		ChangeHash: domain.ComputeChangeHash(targetPath, baseHash, command.Content), CreatedAt: now,
	}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, revision); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal, err := s.repo.CreateProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: domain.ProposalTypeFilePatch, TargetPath: targetPath,
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		RequestHash:    domain.ComputeRequestHash(command.WorkspaceID, targetPath, baseHash, command.Content, strings.TrimSpace(command.EvidenceSummary), strings.TrimSpace(command.Risk), strings.TrimSpace(command.RollbackPlan)),
		Status:         domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: !proposal.CreatedAt.Equal(now)}, nil
}

// CreateKnowledgeChangeProposal 校验并创建结构化 `knowledge_change` Proposal。
func (s *Service) CreateKnowledgeChangeProposal(ctx context.Context, command CreateKnowledgeChangeCommand) (CreateResult, error) {
	repository, ok := s.repo.(domain.KnowledgeChangeProposalRepository)
	if !ok {
		return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_CHANGE_PROPOSAL_REPOSITORY_UNAVAILABLE", false, errors.New("knowledge change proposal repository is unavailable"))
	}
	if command.WorkspaceID == "" || strings.TrimSpace(command.IdempotencyKey) == "" || len(strings.TrimSpace(command.IdempotencyKey)) > 128 || strings.TrimSpace(command.Risk) == "" || strings.TrimSpace(command.RollbackPlan) == "" {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, errors.New("knowledge change proposal fields are invalid"))
	}
	canonicalChange, err := domain.ValidateKnowledgeChange(command.KnowledgeChange)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	risk := strings.TrimSpace(command.Risk)
	rollbackPlan := strings.TrimSpace(command.RollbackPlan)
	changeHash, err := domain.ComputeKnowledgeChangeHash(canonicalChange, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	requestHash, err := domain.ComputeKnowledgeChangeRequestHash(command.WorkspaceID, canonicalChange, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1,
		Risk: risk, RollbackPlan: rollbackPlan,
		ChangeHash: changeHash, KnowledgeChange: &canonicalChange, CreatedAt: now,
	}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeKnowledgeChange, revision); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	proposal, err := repository.CreateKnowledgeChangeProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: domain.ProposalTypeKnowledgeChange,
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		RequestHash:    requestHash,
		Status:         domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: !proposal.CreatedAt.Equal(now)}, nil
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
	if s.dispatcher != nil {
		result, err := s.DecideProposalWithDispatch(ctx, proposalID, revisionID, changeHash, decision)
		return result.Approval, err
	}
	return s.decideProposalLegacy(ctx, proposalID, revisionID, changeHash, decision)
}

func (s *Service) decideProposalLegacy(ctx context.Context, proposalID, revisionID foundation.ID, changeHash string, decision domain.Decision) (domain.Approval, error) {
	if proposalID == "" || revisionID == "" || !domain.ValidHash(changeHash) || decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return domain.Approval{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_INVALID", false, errors.New("approval fields are invalid"))
	}
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return domain.Approval{}, err
	}
	if proposal.Revision.ID != revisionID || proposal.Revision.ChangeHash != strings.ToLower(changeHash) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	if err := validateProposalForApproval(proposal); err != nil {
		return domain.Approval{}, err
	}
	if proposalType(proposal) == domain.ProposalTypeKnowledgeChange && decision == domain.DecisionApproved && isNilKnowledgeRelationApplier(s.knowledgeApplier) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_RELATION_APPLIER_UNAVAILABLE", false, errors.New("knowledge relation apply seam is not configured"))
	}
	if proposal.Approval != nil {
		if proposal.Approval.RevisionID == revisionID && proposal.Approval.ChangeHash == strings.ToLower(changeHash) && proposal.Approval.Decision == decision {
			if proposalType(proposal) == domain.ProposalTypeKnowledgeChange && decision == domain.DecisionApproved {
				if _, err := s.applyApprovedKnowledgeRelation(ctx, *proposal.Approval); err != nil {
					return domain.Approval{}, err
				}
			}
			return *proposal.Approval, nil
		}
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("proposal already has a different approval decision"))
	}
	var approvedGitHead *string
	if proposal.Status == domain.StatusReady && decision == domain.DecisionApproved && proposalType(proposal) == domain.ProposalTypeFilePatch {
		currentHash, readErr := s.targets.CurrentHash(ctx, proposal.WorkspaceID, proposal.TargetPath)
		if readErr != nil {
			return s.rejectUnavailableTarget(ctx, proposal.ID, readErr)
		}
		if strings.ToLower(currentHash) != proposal.Revision.BaseHash {
			if markErr := s.repo.MarkNeedsRevision(ctx, proposal.ID, s.clock.Now()); markErr != nil {
				return domain.Approval{}, markErr
			}
			return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: strings.ToLower(currentHash)})
		}
		snapshot, inspectErr := s.git.CaptureApprovalSnapshot(ctx, proposal.WorkspaceID)
		if inspectErr != nil {
			return domain.Approval{}, inspectErr
		}
		if bindingErr := domain.ValidateGitSnapshotBinding(proposal.WorkspaceID, snapshot.Head, snapshot); bindingErr != nil {
			return domain.Approval{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_GIT_SNAPSHOT_INVALID", false, bindingErr)
		}
		head := strings.ToLower(snapshot.Head)
		approvedGitHead = &head
	}
	approvalID, err := s.ids.New()
	if err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.repo.Approve(ctx, domain.Approval{
		ID: approvalID, ProposalID: proposalID, RevisionID: revisionID,
		ChangeHash: strings.ToLower(changeHash), Decision: decision, ApprovedGitHead: approvedGitHead, DecidedAt: s.clock.Now(),
	})
	if err != nil {
		return domain.Approval{}, err
	}
	if proposalType(proposal) == domain.ProposalTypeKnowledgeChange && approval.Decision == domain.DecisionApproved {
		if _, err := s.applyApprovedKnowledgeRelation(ctx, approval); err != nil {
			return domain.Approval{}, err
		}
	}
	return approval, nil
}

// DecideProposalWithDispatch 在外部文件/Git 安全门后，通过单一 UoW 保存 Approval 并投递唯一 Safe Writeback Workflow。
// 只有完整 Approval→Run 绑定重放可以跳过可变文件和 Git 事实读取。
func (s *Service) DecideProposalWithDispatch(ctx context.Context, proposalID, revisionID foundation.ID, changeHash string, decision domain.Decision) (ApprovalDecisionResult, error) {
	if s == nil || isNilApprovalDispatcher(s.dispatcher) {
		return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "APPROVAL_DISPATCHER_MISSING", false, errors.New("approval dispatcher is missing"))
	}
	if proposalID == "" || revisionID == "" || !domain.ValidHash(changeHash) || decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_INVALID", false, errors.New("approval fields are invalid"))
	}
	changeHash = strings.ToLower(changeHash)
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if proposal.Revision.ID != revisionID || proposal.Revision.ChangeHash != changeHash {
		return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	if err := validateProposalForApproval(proposal); err != nil {
		return ApprovalDecisionResult{}, err
	}
	if proposalType(proposal) == domain.ProposalTypeKnowledgeChange {
		if decision == domain.DecisionApproved && isNilKnowledgeRelationApplier(s.knowledgeApplier) {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_RELATION_APPLIER_UNAVAILABLE", false, errors.New("knowledge relation apply seam is not configured"))
		}
		approval, approvalErr := s.decideProposalLegacy(ctx, proposalID, revisionID, changeHash, decision)
		if approvalErr != nil {
			return ApprovalDecisionResult{}, approvalErr
		}
		return ApprovalDecisionResult{Approval: approval, Replayed: proposal.Approval != nil}, nil
	}

	approval := domain.Approval{ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: decision}
	if proposal.Approval != nil {
		if proposal.Approval.RevisionID != revisionID || !strings.EqualFold(proposal.Approval.ChangeHash, changeHash) || proposal.Approval.Decision != decision {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("proposal already has a different approval decision"))
		}
		approval = *proposal.Approval
		if decision == domain.DecisionRejected || proposal.WorkflowRunID != nil {
			return s.dispatchApproval(ctx, ApprovalDispatchCommand{WorkspaceID: proposal.WorkspaceID, Approval: approval})
		}
		if approval.ApprovedGitHead == nil || !domain.ValidGitHead(*approval.ApprovedGitHead) {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_GIT_BASELINE_MISSING", false, errors.New("historical approval has no valid git baseline"))
		}
	} else {
		approvalID, idErr := s.ids.New()
		if idErr != nil {
			return ApprovalDecisionResult{}, idErr
		}
		approval.ID = approvalID
		approval.DecidedAt = s.clock.Now()
	}

	command := ApprovalDispatchCommand{WorkspaceID: proposal.WorkspaceID, Approval: approval}
	if decision == domain.DecisionApproved {
		currentHash, readErr := s.targets.CurrentHash(ctx, proposal.WorkspaceID, proposal.TargetPath)
		if readErr != nil {
			_, rejectionErr := s.rejectUnavailableTarget(ctx, proposal.ID, readErr)
			return ApprovalDecisionResult{}, rejectionErr
		}
		currentHash = strings.ToLower(currentHash)
		if currentHash != proposal.Revision.BaseHash {
			if markErr := s.repo.MarkNeedsRevision(ctx, proposal.ID, s.clock.Now()); markErr != nil {
				return ApprovalDecisionResult{}, markErr
			}
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: currentHash})
		}
		snapshot, inspectErr := s.git.CaptureApprovalSnapshot(ctx, proposal.WorkspaceID)
		if inspectErr != nil {
			return ApprovalDecisionResult{}, inspectErr
		}
		if bindingErr := domain.ValidateGitSnapshotBinding(proposal.WorkspaceID, snapshot.Head, snapshot); bindingErr != nil {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_GIT_SNAPSHOT_INVALID", false, bindingErr)
		}
		head := strings.ToLower(snapshot.Head)
		if approval.ApprovedGitHead != nil && !strings.EqualFold(*approval.ApprovedGitHead, head) {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_GIT_HEAD_CONFLICT", false, errors.New("git head differs from approved baseline"))
		}
		approval.ApprovedGitHead = &head
		command.Approval = approval
		command.ObservedBaseHash = currentHash
		command.ObservedGitHead = head
	}
	return s.dispatchApproval(ctx, command)
}

func (s *Service) dispatchApproval(ctx context.Context, command ApprovalDispatchCommand) (ApprovalDecisionResult, error) {
	result, err := s.dispatcher.DecideAndDispatch(ctx, command)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if err := validateApprovalDispatchResult(command, result); err != nil {
		return ApprovalDecisionResult{}, err
	}
	return decisionResultFromDispatch(result), nil
}

func (s *Service) rejectUnavailableTarget(ctx context.Context, proposalID foundation.ID, readErr error) (domain.Approval, error) {
	var unavailable *domain.TargetUnavailableError
	if !errors.As(readErr, &unavailable) {
		return domain.Approval{}, readErr
	}
	if markErr := s.repo.MarkNeedsRevision(ctx, proposalID, s.clock.Now()); markErr != nil {
		return domain.Approval{}, markErr
	}
	return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_UNAVAILABLE", false, unavailable)
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
	if err := requireFilePatchProposal(proposal, "APPLY_PREFLIGHT_PROPOSAL_TYPE_UNSUPPORTED"); err != nil {
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
		var unavailable *domain.TargetUnavailableError
		if errors.As(err, &unavailable) {
			if markErr := s.repo.MarkNeedsRevision(ctx, proposal.ID, s.clock.Now()); markErr != nil {
				return ApplyPreflightResult{}, markErr
			}
			return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_UNAVAILABLE", false, unavailable)
		}
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

// IssueWriteAuthorization 在批准事实和当前目标版本一致时签发服务端短期写权限。
// 返回的 Credential 只在首次签发时出现，调用方不得写入日志、模型上下文或 API 响应。
func (s *Service) IssueWriteAuthorization(ctx context.Context, command domain.AuthorizationIssue) (domain.AuthorizationIssueResult, error) {
	authorizations, ok := s.repo.(domain.AuthorizationRepository)
	if !ok {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_REPOSITORY_UNAVAILABLE", false, errors.New("authorization repository is unavailable"))
	}
	if err := domain.ValidateAuthorizationIssue(command, MaxWriteAuthorizationTTL); err != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_INVALID", false, err)
	}
	proposal, err := s.repo.GetProposal(ctx, command.ProposalID)
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	if err := requireFilePatchProposal(proposal, "WRITE_AUTHORIZATION_PROPOSAL_TYPE_UNSUPPORTED"); err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	if proposal.WorkspaceID != command.WorkspaceID || proposal.Revision.ID != command.RevisionID || proposal.Approval == nil || proposal.Approval.ID != command.ApprovalID || proposal.Status != domain.StatusApproved || proposal.Approval.Decision != domain.DecisionApproved {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("proposal approval binding is not valid"))
	}
	if strings.TrimSpace(command.Scope) != domain.ExpectedAuthorizationScope(proposal.Revision.TargetPath) {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_SCOPE_INVALID", false, errors.New("authorization scope is broader than the approved target"))
	}
	if err := domain.ValidateToolBinding(command.ToolName, command.Capability); err != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_INVALID", false, err)
	}
	if proposal.Approval.ChangeHash != proposal.Revision.ChangeHash || proposal.Revision.ChangeHash != domain.ComputeChangeHash(proposal.Revision.TargetPath, proposal.Revision.BaseHash, proposal.Revision.Content) {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_CHANGE_HASH_INVALID", false, errors.New("approved change hash is not reproducible"))
	}
	currentHash, err := s.targets.CurrentHash(ctx, proposal.WorkspaceID, proposal.TargetPath)
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	if strings.ToLower(currentHash) != proposal.Revision.BaseHash {
		if markErr := s.repo.MarkNeedsRevision(ctx, proposal.ID, s.clock.Now()); markErr != nil {
			return domain.AuthorizationIssueResult{}, markErr
		}
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: strings.ToLower(currentHash)})
	}
	if err := authorizations.ValidateWorkflowContext(ctx, command.WorkspaceID, command.WorkflowRunID, command.NodeRunID); err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	authorizationID, err := s.ids.New()
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	credential, err := newCredential()
	if err != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_RANDOM_UNAVAILABLE", true, err)
	}
	now := s.clock.Now()
	result, err := authorizations.CreateAuthorization(ctx, domain.ToolAuthorization{
		ID:          authorizationID,
		WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, RevisionID: command.RevisionID, ApprovalID: command.ApprovalID,
		ToolName: strings.TrimSpace(command.ToolName), Capability: command.Capability, Scope: strings.TrimSpace(command.Scope),
		ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: proposal.Revision.BaseHash,
		TokenHash: hashCredential(credential), IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		Status: domain.AuthorizationIssued, IssuedAt: now, ExpiresAt: now.Add(command.TTL), Version: 1,
	})
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	result.Authorization.TokenHash = ""
	if result.Replayed {
		return result, nil
	}
	result.Credential = credential
	return result, nil
}

// ConsumeWriteAuthorization 原子消费一次性写权限，不执行任何文件/Git 副作用。
func (s *Service) ConsumeWriteAuthorization(ctx context.Context, request domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error) {
	authorizations, ok := s.repo.(domain.AuthorizationRepository)
	if !ok {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_REPOSITORY_UNAVAILABLE", false, errors.New("authorization repository is unavailable"))
	}
	if strings.TrimSpace(request.Credential) == "" || len(request.Credential) > domain.MaxAuthorizationCredentialBytes || strings.TrimSpace(request.IdempotencyKey) == "" || len(strings.TrimSpace(request.IdempotencyKey)) > 128 || request.WorkspaceID == "" || request.WorkflowRunID == "" || request.NodeRunID == "" || request.ProposalID == "" || request.RevisionID == "" || request.ApprovalID == "" || strings.TrimSpace(request.ToolName) == "" || len(strings.TrimSpace(request.ToolName)) > 64 || strings.TrimSpace(request.Scope) == "" || len(strings.TrimSpace(request.Scope)) > 512 || !domain.ValidHash(request.ApprovedChangeHash) || !domain.ValidHash(request.TargetVersion) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_CONSUME_INVALID", false, errors.New("authorization consume binding is incomplete"))
	}
	if err := domain.ValidateToolBinding(request.ToolName, request.Capability); err != nil {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_CONSUME_INVALID", false, err)
	}
	request.ToolName = strings.TrimSpace(request.ToolName)
	request.Scope = strings.TrimSpace(request.Scope)
	request.ApprovedChangeHash = strings.ToLower(request.ApprovedChangeHash)
	request.TargetVersion = strings.ToLower(request.TargetVersion)
	request.Credential = hashCredential(request.Credential)
	existing, err := authorizations.GetAuthorization(ctx, request.WorkspaceID, request.IdempotencyKey, request.Credential)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if err := domain.ValidateAuthorizationConsumeBinding(existing, request); err != nil {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_BINDING_CONFLICT", false, err)
	}
	if existing.Status != domain.AuthorizationIssued {
		result, consumeErr := authorizations.ConsumeAuthorization(ctx, request)
		result.Authorization.TokenHash = ""
		return result, consumeErr
	}
	proposal, err := s.repo.GetProposal(ctx, request.ProposalID)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if err := requireFilePatchProposal(proposal, "WRITE_AUTHORIZATION_PROPOSAL_TYPE_UNSUPPORTED"); err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if proposal.WorkspaceID != request.WorkspaceID || proposal.Revision.ID != request.RevisionID || proposal.Approval == nil || proposal.Approval.ID != request.ApprovalID || proposal.Status != domain.StatusApproved || proposal.Approval.Decision != domain.DecisionApproved || proposal.Revision.ChangeHash != request.ApprovedChangeHash || proposal.Revision.BaseHash != request.TargetVersion || proposal.Approval.ChangeHash != request.ApprovedChangeHash || strings.TrimSpace(request.Scope) != domain.ExpectedAuthorizationScope(proposal.Revision.TargetPath) || proposal.Revision.ChangeHash != domain.ComputeChangeHash(proposal.Revision.TargetPath, proposal.Revision.BaseHash, proposal.Revision.Content) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("authorization approval binding is no longer valid"))
	}
	currentHash, err := s.targets.CurrentHash(ctx, proposal.WorkspaceID, proposal.TargetPath)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if strings.ToLower(currentHash) != proposal.Revision.BaseHash {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: strings.ToLower(currentHash)})
	}
	if err := authorizations.ValidateWorkflowContext(ctx, request.WorkspaceID, request.WorkflowRunID, request.NodeRunID); err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	result, err := authorizations.ConsumeAuthorization(ctx, request)
	result.Authorization.TokenHash = ""
	return result, err
}

// RevokeWriteAuthorization 使尚未消费的授权立即失效。
func (s *Service) RevokeWriteAuthorization(ctx context.Context, id foundation.ID) error {
	authorizations, ok := s.repo.(domain.AuthorizationRepository)
	if !ok {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_REPOSITORY_UNAVAILABLE", false, errors.New("authorization repository is unavailable"))
	}
	if id == "" {
		return foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_ID_INVALID", false, errors.New("authorization id is required"))
	}
	return authorizations.RevokeAuthorization(ctx, id, s.clock.Now())
}

func newCredential() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func hashCredential(credential string) string {
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:])
}

func proposalType(proposal domain.Proposal) domain.ProposalType {
	return domain.NormalizeProposalType(proposal.Type)
}

func validateProposalForApproval(proposal domain.Proposal) error {
	if proposalType(proposal) == domain.ProposalTypeKnowledgeChange && strings.TrimSpace(proposal.TargetPath) != "" {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, errors.New("knowledge change proposal must not carry file target fields"))
	}
	if err := domain.ValidateProposalRevisionForType(proposalType(proposal), proposal.Revision); err != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
	}
	return nil
}

func requireFilePatchProposal(proposal domain.Proposal, code string) error {
	if proposalType(proposal) != domain.ProposalTypeFilePatch {
		return foundation.NewError(foundation.ErrorPermissionDenied, code, false, errors.New("proposal type does not support file writeback"))
	}
	return nil
}
