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

// MaxWriteAuthorizationTTL 是单次写权限的服务端有效期上限。
const MaxWriteAuthorizationTTL = 5 * time.Minute

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
	IdempotencyKey  string
	BaseHash        string
	Content         string
	EvidenceSummary string
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
	proposal, err := s.repo.CreateProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, TargetPath: targetPath,
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		RequestHash:    domain.ComputeRequestHash(command.WorkspaceID, targetPath, baseHash, command.Content, strings.TrimSpace(command.EvidenceSummary), strings.TrimSpace(command.Risk), strings.TrimSpace(command.RollbackPlan)),
		Status:         domain.StatusReady, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: proposal.ID != proposalID}, nil
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
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return domain.Approval{}, err
	}
	if proposal.Revision.ID != revisionID || proposal.Revision.ChangeHash != strings.ToLower(changeHash) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	if proposal.Status == domain.StatusReady && decision == domain.DecisionApproved {
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
	if strings.TrimSpace(request.Credential) == "" || strings.TrimSpace(request.IdempotencyKey) == "" || len(strings.TrimSpace(request.IdempotencyKey)) > 128 || request.WorkspaceID == "" || request.WorkflowRunID == "" || request.NodeRunID == "" || request.ProposalID == "" || request.RevisionID == "" || request.ApprovalID == "" || strings.TrimSpace(request.ToolName) == "" || len(strings.TrimSpace(request.ToolName)) > 64 || strings.TrimSpace(request.Scope) == "" || len(strings.TrimSpace(request.Scope)) > 512 || !domain.ValidHash(request.ApprovedChangeHash) || !domain.ValidHash(request.TargetVersion) {
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
