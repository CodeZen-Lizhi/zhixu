package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

// CandidateListRequest 是候选 HTTP 查询传入 Application 的有界请求。
// Cursor 由 Application 负责签发、验证和绑定查询窗口，HTTP 层不解释其内容。
type CandidateListRequest struct {
	Query  graphdomain.SemanticLinkCandidateQuery
	Cursor string
}

// CandidateDecisionReceipt 是一次候选决策的持久化回执。
// Receipt ID 必须对应 append-only decision 记录，而不是 Candidate ID。
type CandidateDecisionReceipt struct {
	ID          foundation.ID
	CandidateID foundation.ID
	WorkspaceID foundation.ID
	Action      graphdomain.SemanticLinkCandidateDecisionAction
	Status      graphdomain.SemanticLinkCandidateStatus
	Version     int64
	ProposalID  *foundation.ID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Replayed    bool
}

// CandidateService 是候选独立 HTTP seam 所需的最小应用层接口。
// 该接口不扩展正式 Graph 只读 Service，也不暴露 PostgreSQL 或 Proposal UoW 细节。
type CandidateService interface {
	// List 查询同一 Workspace 内的稳定、有界候选页面。
	List(ctx context.Context, request CandidateListRequest) (graphdomain.SemanticLinkCandidatePage, error)
	// Get 查询同一 Workspace 内的候选详情。
	Get(ctx context.Context, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error)
	// Decide 执行带幂等键和 expected version 的候选决策。
	Decide(ctx context.Context, command SemanticLinkCandidateDecisionCommand) (CandidateDecisionReceipt, error)
}

// ValidateCandidateDecisionReceipt 校验持久化决策回执与原命令的 Workspace、版本和状态绑定。
func ValidateCandidateDecisionReceipt(command SemanticLinkCandidateDecisionCommand, receipt CandidateDecisionReceipt) error {
	canonical, err := CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return err
	}
	if !validID(receipt.ID) || receipt.ID == canonical.WorkspaceID || receipt.ID == canonical.CandidateID ||
		receipt.WorkspaceID != canonical.WorkspaceID || receipt.CandidateID != canonical.CandidateID ||
		receipt.Action != canonical.Decision.Action || receipt.Version != canonical.ExpectedVersion+1 ||
		receipt.CreatedAt.IsZero() || receipt.UpdatedAt.Before(receipt.CreatedAt) {
		return invalidCandidateDecisionReceipt("candidate decision receipt binding is inconsistent")
	}
	if receipt.ProposalID != nil && (!validID(*receipt.ProposalID) || *receipt.ProposalID == receipt.ID ||
		*receipt.ProposalID == receipt.CandidateID || *receipt.ProposalID == receipt.WorkspaceID) {
		return invalidCandidateDecisionReceipt("candidate decision receipt proposal binding is invalid")
	}
	switch canonical.Decision.Action {
	case graphdomain.SemanticLinkCandidateDecisionConfirm, graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType:
		if receipt.Status != graphdomain.SemanticLinkCandidateStatusProposalCreated || receipt.ProposalID == nil {
			return invalidCandidateDecisionReceipt("confirmed candidate receipt requires a proposal")
		}
	case graphdomain.SemanticLinkCandidateDecisionIgnore:
		if receipt.Status != graphdomain.SemanticLinkCandidateStatusIgnored || receipt.ProposalID != nil {
			return invalidCandidateDecisionReceipt("ignored candidate receipt is inconsistent")
		}
	case graphdomain.SemanticLinkCandidateDecisionFalsePositive:
		if receipt.Status != graphdomain.SemanticLinkCandidateStatusFalsePositive || receipt.ProposalID != nil {
			return invalidCandidateDecisionReceipt("false-positive candidate receipt is inconsistent")
		}
	case graphdomain.SemanticLinkCandidateDecisionDefer:
		if receipt.Status != graphdomain.SemanticLinkCandidateStatusDeferred || receipt.ProposalID != nil {
			return invalidCandidateDecisionReceipt("deferred candidate receipt is inconsistent")
		}
	case graphdomain.SemanticLinkCandidateDecisionResume:
		if receipt.Status != graphdomain.SemanticLinkCandidateStatusActive || receipt.ProposalID != nil {
			return invalidCandidateDecisionReceipt("resumed candidate receipt is inconsistent")
		}
	default:
		return invalidCandidateDecisionReceipt("candidate decision receipt action is unsupported")
	}
	return nil
}

func invalidCandidateDecisionReceipt(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeSemanticLinkCandidateResultInvalid, false, errors.New(message))
}
