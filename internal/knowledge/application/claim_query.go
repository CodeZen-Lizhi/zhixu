package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// ClaimQueryRepository is the bounded read-only repository surface needed by ClaimQueryService.
type ClaimQueryRepository interface {
	// BatchGetClaims returns Workspace-scoped Claim aggregates without per-Claim queries.
	BatchGetClaims(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error)
}

// ClaimQueryService exposes formal Claim reads without requiring unrelated Knowledge write verifiers.
type ClaimQueryService struct {
	repository ClaimQueryRepository
}

// NewClaimQueryService creates a read-only, fail-closed Knowledge query boundary.
func NewClaimQueryService(repository ClaimQueryRepository) (*ClaimQueryService, error) {
	if isNil(repository) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false,
			errors.New("knowledge claim query repository is unavailable"))
	}
	return &ClaimQueryService{repository: repository}, nil
}

// GetClaims returns a bounded, validated page of formal Claim aggregates.
func (service *ClaimQueryService) GetClaims(ctx context.Context, query GetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if service == nil || isNil(service.repository) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false,
			errors.New("knowledge claim query service is unavailable"))
	}
	if ctx == nil {
		return nil, requestError("context is nil")
	}
	return getClaims(ctx, service.repository, query)
}
