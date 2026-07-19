package application

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const errorCodeFormalClaimNotFound = "KNOWLEDGE_FORMAL_CLAIM_NOT_FOUND"

// FormalClaimReader 只读取可供 Agent 使用的 Confirmed 或 Disputed Claim 完整事实。
type FormalClaimReader struct {
	repository domain.Repository
}

// NewFormalClaimReader 创建不依赖写命令端口的正式 Claim 只读服务。
func NewFormalClaimReader(repository domain.Repository) (*FormalClaimReader, error) {
	if repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge repository is nil"))
	}
	return &FormalClaimReader{repository: repository}, nil
}

// Load 单批加载指定 Workspace 内全部正式 Claim；任一缺失或非正式状态均 fail closed。
func (reader *FormalClaimReader) Load(ctx context.Context, workspaceID foundation.ID, claimIDs []foundation.ID) ([]domain.ClaimWithSources, error) {
	if reader == nil || reader.repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("formal claim reader is nil"))
	}
	if ctx == nil {
		return nil, requestError("context is nil")
	}
	if len(claimIDs) == 0 || len(claimIDs) > domain.MaxBatchLimit {
		return nil, requestError("formal claim query count is invalid")
	}
	canonicalIDs := append([]foundation.ID(nil), claimIDs...)
	slices.Sort(canonicalIDs)
	if err := domain.ValidateBatchQuery(workspaceID, canonicalIDs, len(canonicalIDs)); err != nil {
		return nil, err
	}
	result, err := reader.repository.BatchGetClaims(ctx, domain.BatchGetClaimsQuery{
		WorkspaceID: workspaceID,
		IDs:         canonicalIDs,
		Statuses:    []domain.ClaimStatus{domain.ClaimStatusConfirmed, domain.ClaimStatusDisputed},
		Limit:       len(canonicalIDs),
	})
	if err != nil {
		return nil, err
	}
	if len(result) < len(canonicalIDs) {
		return nil, foundation.NewError(foundation.ErrorNotFound, errorCodeFormalClaimNotFound, false, errors.New("one or more formal claims are unavailable"))
	}
	if len(result) > len(canonicalIDs) {
		return nil, consistencyError("repository returned too many formal claims")
	}
	byID := make(map[foundation.ID]domain.ClaimWithSources, len(result))
	for _, item := range result {
		if item.Claim.WorkspaceID != workspaceID ||
			(item.Claim.Status != domain.ClaimStatusConfirmed && item.Claim.Status != domain.ClaimStatusDisputed) ||
			domain.ValidateClaimAggregate(item.Claim, item.Sources) != nil {
			return nil, consistencyError("repository returned an invalid formal claim")
		}
		if _, requested := slices.BinarySearch(canonicalIDs, item.Claim.ID); !requested {
			return nil, consistencyError("repository returned an out-of-scope formal claim")
		}
		if _, duplicate := byID[item.Claim.ID]; duplicate {
			return nil, consistencyError("repository returned duplicate formal claims")
		}
		byID[item.Claim.ID] = cloneFormalClaim(item)
	}
	ordered := make([]domain.ClaimWithSources, len(canonicalIDs))
	for index, id := range canonicalIDs {
		item, exists := byID[id]
		if !exists {
			return nil, foundation.NewError(foundation.ErrorNotFound, errorCodeFormalClaimNotFound, false, errors.New("one or more formal claims are unavailable"))
		}
		ordered[index] = item
	}
	return ordered, nil
}

func cloneFormalClaim(input domain.ClaimWithSources) domain.ClaimWithSources {
	cloned := input
	cloned.Claim.Applicability.CanonicalJSON = append(json.RawMessage(nil), input.Claim.Applicability.CanonicalJSON...)
	cloned.Claim.ConfidenceFactors = append(json.RawMessage(nil), input.Claim.ConfidenceFactors...)
	cloned.Claim.CreatedAt = input.Claim.CreatedAt.UTC()
	cloned.Claim.UpdatedAt = input.Claim.UpdatedAt.UTC()
	cloned.Sources = append([]domain.ClaimSource(nil), input.Sources...)
	for index := range cloned.Sources {
		cloned.Sources[index].CreatedAt = input.Sources[index].CreatedAt.UTC()
		if input.Sources[index].ModelRunRef != nil {
			value := *input.Sources[index].ModelRunRef
			cloned.Sources[index].ModelRunRef = &value
		}
	}
	return cloned
}
