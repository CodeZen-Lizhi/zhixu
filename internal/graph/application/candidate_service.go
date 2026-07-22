package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

const (
	semanticLinkConfirmRisk         = "Creates one canonical Relation only after explicit Proposal approval"
	semanticLinkConfirmRollbackPlan = "Create a compensating Relation Proposal if the approved Relation must be retired"
)

// SemanticLinkCandidateRepository 是候选 Application 所需的查询与非 Confirm 决策端口。
type SemanticLinkCandidateRepository interface {
	SemanticLinkCandidateWindowPort
	GetSemanticLinkCandidate(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkCandidate, error)
	DecideSemanticLinkCandidate(context.Context, SemanticLinkCandidateDecisionCommand, *foundation.ID) (SemanticLinkCandidateDecisionResult, error)
}

// SemanticLinkCandidateService 实现候选独立 HTTP seam，不扩展正式 Graph 只读 Service。
type SemanticLinkCandidateService struct {
	repository SemanticLinkCandidateRepository
	confirmer  candidateconfirm.Port
	cursor     *CursorCodec
}

// NewSemanticLinkCandidateService 构造完整候选查询、决策与 Confirm 服务。
func NewSemanticLinkCandidateService(repository SemanticLinkCandidateRepository, confirmer candidateconfirm.Port, cursor *CursorCodec) (*SemanticLinkCandidateService, error) {
	if nilInterface(repository) || nilInterface(confirmer) || cursor == nil || !cursor.initialized {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, true, errors.New("semantic link candidate dependencies are unavailable"))
	}
	return &SemanticLinkCandidateService{repository: repository, confirmer: confirmer, cursor: cursor}, nil
}

// List 返回 application-owned opaque cursor 候选页面。
func (service *SemanticLinkCandidateService) List(ctx context.Context, request CandidateListRequest) (graphdomain.SemanticLinkCandidatePage, error) {
	if service == nil || nilInterface(service.repository) || service.cursor == nil || !service.cursor.initialized || ctx == nil {
		return graphdomain.SemanticLinkCandidatePage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, true, errors.New("semantic link candidate service is unavailable"))
	}
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request.Query); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	window, err := service.repository.SemanticLinkCandidateWindow(ctx, request.Query)
	if err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	if err := validateWindowState(len(window.Items), window.Truncated, window.Reason); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	if err := graphdomain.ValidateSemanticLinkCandidateWindow(request.Query, window.Items, MaxSemanticLinkCandidateWindow); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	requestHash, err := CanonicalSemanticLinkCandidateQueryHash(request.Query)
	if err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	page, err := paginateResultWindowWithState(service.cursor, ResultWindowRequest{
		QueryKind: QueryKindSemanticLinkCandidates, WorkspaceID: request.Query.WorkspaceID,
		CanonicalRequestHash: requestHash, Limit: request.Query.Limit, Cursor: request.Cursor,
	}, window.Items, window.Items, window.Truncated, window.Reason)
	if err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	result := graphdomain.SemanticLinkCandidatePage{
		WorkspaceID: request.Query.WorkspaceID,
		Items:       page.Items,
		Meta: graphdomain.PageMeta{
			Fingerprint: page.Fingerprint, NextCursor: page.NextCursor,
			Complete: page.Complete && !window.Truncated, Truncated: window.Truncated, Reason: window.Reason,
		},
	}
	if err := graphdomain.ValidateSemanticLinkCandidatePage(request.Query, result); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	return result, nil
}

// Get 返回 Workspace-scoped 候选详情。
func (service *SemanticLinkCandidateService) Get(ctx context.Context, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	if service == nil || nilInterface(service.repository) || ctx == nil {
		return graphdomain.SemanticLinkCandidate{}, foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, true, errors.New("semantic link candidate service is unavailable"))
	}
	return service.repository.GetSemanticLinkCandidate(ctx, workspaceID, candidateID)
}

// Decide 将 Confirm 路由到 Proposal 原子 UoW，其余动作写入 Candidate 决策历史。
func (service *SemanticLinkCandidateService) Decide(ctx context.Context, command SemanticLinkCandidateDecisionCommand) (CandidateDecisionReceipt, error) {
	if service == nil || nilInterface(service.repository) || nilInterface(service.confirmer) || ctx == nil {
		return CandidateDecisionReceipt{}, foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, true, errors.New("semantic link candidate service is unavailable"))
	}
	canonical, err := CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return CandidateDecisionReceipt{}, err
	}
	if canonical.Decision.Action == graphdomain.SemanticLinkCandidateDecisionConfirm || canonical.Decision.Action == graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType {
		result, confirmErr := service.confirmer.Confirm(ctx, candidateconfirm.Command{
			WorkspaceID: canonical.WorkspaceID, CandidateID: canonical.CandidateID,
			ExpectedVersion: canonical.ExpectedVersion, IdempotencyKey: canonical.IdempotencyKey,
			Action: canonical.Decision.Action, RelationType: canonical.Decision.RelationType,
			RiskLevel: candidateconfirm.ProposalRiskLevel,
			Risk:      semanticLinkConfirmRisk, RollbackPlan: semanticLinkConfirmRollbackPlan,
		})
		if confirmErr != nil {
			return CandidateDecisionReceipt{}, confirmErr
		}
		receipt := CandidateDecisionReceipt{
			ID: result.DecisionID, CandidateID: result.Candidate.ID, WorkspaceID: result.Candidate.WorkspaceID,
			Action: canonical.Decision.Action, Status: result.Candidate.Status, Version: result.Candidate.Version,
			ProposalID: &result.Proposal.ID, CreatedAt: result.Candidate.UpdatedAt, UpdatedAt: result.Candidate.UpdatedAt,
			Replayed: result.Replayed,
		}
		if err := ValidateCandidateDecisionReceipt(canonical, receipt); err != nil {
			return CandidateDecisionReceipt{}, err
		}
		return receipt, nil
	}
	result, err := service.repository.DecideSemanticLinkCandidate(ctx, canonical, nil)
	if err != nil {
		return CandidateDecisionReceipt{}, err
	}
	receipt := CandidateDecisionReceipt{
		ID: result.DecisionID, CandidateID: result.Candidate.ID, WorkspaceID: result.Candidate.WorkspaceID,
		Action: canonical.Decision.Action, Status: result.Candidate.Status, Version: result.Candidate.Version,
		ProposalID: result.ProposalID, CreatedAt: result.DecidedAt, UpdatedAt: result.Candidate.UpdatedAt,
		Replayed: result.Replayed,
	}
	if err := ValidateCandidateDecisionReceipt(canonical, receipt); err != nil {
		return CandidateDecisionReceipt{}, err
	}
	return receipt, nil
}

var _ CandidateService = (*SemanticLinkCandidateService)(nil)
