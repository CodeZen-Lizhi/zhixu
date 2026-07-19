package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// EvidenceEligibilityService 是只暴露正式证据资格查询的最小只读 Application seam。
type EvidenceEligibilityService struct {
	repository domain.Repository
}

// NewEvidenceEligibilityService 创建不要求写命令依赖的只读资格服务。
func NewEvidenceEligibilityService(repository domain.Repository) (*EvidenceEligibilityService, error) {
	if repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge repository is nil"))
	}
	return &EvidenceEligibilityService{repository: repository}, nil
}

// CheckEvidenceEligibility 单批判断 Provenance 是否绑定 Confirmed 或 Disputed 正式知识。
func (s *EvidenceEligibilityService) CheckEvidenceEligibility(ctx context.Context, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	if s == nil || s.repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge eligibility service is nil"))
	}
	if ctx == nil {
		return nil, requestError("context is nil")
	}
	return checkEvidenceEligibility(ctx, s.repository, query)
}

// CheckEvidenceEligibility 单批判断 Provenance 是否绑定 Confirmed 或 Disputed 正式知识。
func (s *Service) CheckEvidenceEligibility(ctx context.Context, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return nil, err
	}
	return checkEvidenceEligibility(ctx, s.dependencies.Repository, query)
}

func checkEvidenceEligibility(ctx context.Context, repository domain.Repository, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	canonical, err := domain.CanonicalEvidenceEligibilityQuery(query)
	if err != nil {
		return nil, err
	}
	result, err := repository.BatchCheckEvidenceEligibility(ctx, canonical)
	if err != nil {
		return nil, err
	}
	if len(result) != len(canonical.Provenance) {
		return nil, consistencyError("repository returned an incomplete evidence eligibility result")
	}
	for index := range result {
		if result[index].Provenance != canonical.Provenance[index] || domain.ValidateProvenanceEligibility(result[index]) != nil {
			return nil, consistencyError("repository returned an invalid evidence eligibility result")
		}
	}
	return result, nil
}
