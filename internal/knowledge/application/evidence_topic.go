package application

import (
	"context"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// ResolveEvidenceTopicsQuery 是最多 500 个唯一 Provenance 的 Workspace 作用域查询。
type ResolveEvidenceTopicsQuery struct {
	WorkspaceID foundation.ID
	Provenance  []domain.ProvenanceRef
}

// EvidenceTopicRepository 是正式证据 Topic 绑定所需的最小只读仓储。
type EvidenceTopicRepository interface {
	ResolveEvidenceTopics(context.Context, foundation.ID, []domain.ProvenanceRef) ([]domain.EvidenceTopicBinding, error)
}

// EvidenceTopicService 提供正式证据到 Active Topic 的只读解析。
type EvidenceTopicService struct {
	repository EvidenceTopicRepository
}

// NewEvidenceTopicService 创建不依赖 Knowledge 写命令的只读服务。
func NewEvidenceTopicService(repository EvidenceTopicRepository) (*EvidenceTopicService, error) {
	if repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge evidence topic repository is nil"))
	}
	return &EvidenceTopicService{repository: repository}, nil
}

// ResolveEvidenceTopics 单批解析正式证据绑定的 Active Topic。
func (s *EvidenceTopicService) ResolveEvidenceTopics(ctx context.Context, query ResolveEvidenceTopicsQuery) ([]domain.EvidenceTopicBinding, error) {
	if s == nil || s.repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge evidence topic service is nil"))
	}
	if ctx == nil {
		return nil, requestError("context is nil")
	}
	return resolveEvidenceTopics(ctx, s.repository, query)
}

// ResolveEvidenceTopics 单批解析正式证据绑定的 Active Topic。
func (s *Service) ResolveEvidenceTopics(ctx context.Context, query ResolveEvidenceTopicsQuery) ([]domain.EvidenceTopicBinding, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return nil, err
	}
	repository, ok := s.dependencies.Repository.(EvidenceTopicRepository)
	if !ok {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge repository does not resolve evidence topics"))
	}
	return resolveEvidenceTopics(ctx, repository, query)
}

func resolveEvidenceTopics(ctx context.Context, repository EvidenceTopicRepository, query ResolveEvidenceTopicsQuery) ([]domain.EvidenceTopicBinding, error) {
	canonical, err := canonicalEvidenceTopicQuery(query)
	if err != nil {
		return nil, err
	}
	result, err := repository.ResolveEvidenceTopics(ctx, canonical.WorkspaceID, canonical.Provenance)
	if err != nil {
		return nil, err
	}
	wanted := make(map[domain.ProvenanceRef]struct{}, len(canonical.Provenance))
	for _, ref := range canonical.Provenance {
		wanted[ref] = struct{}{}
	}
	previous := ""
	for _, binding := range result {
		if binding.Provenance.WorkspaceID != canonical.WorkspaceID || domain.ValidateEvidenceTopicBinding(binding) != nil {
			return nil, consistencyError("repository returned an invalid evidence topic binding")
		}
		if _, ok := wanted[binding.Provenance]; !ok {
			return nil, consistencyError("repository returned an unrequested evidence topic binding")
		}
		key := evidenceTopicBindingKey(binding)
		if previous != "" && key <= previous {
			return nil, consistencyError("repository returned duplicate or unordered evidence topic bindings")
		}
		previous = key
	}
	return result, nil
}

func canonicalEvidenceTopicQuery(query ResolveEvidenceTopicsQuery) (ResolveEvidenceTopicsQuery, error) {
	if query.WorkspaceID == "" || len(query.Provenance) == 0 || len(query.Provenance) > domain.MaxBatchLimit {
		return ResolveEvidenceTopicsQuery{}, requestError("evidence topic query scope is invalid")
	}
	seen := make(map[domain.ProvenanceRef]struct{}, len(query.Provenance))
	canonical := ResolveEvidenceTopicsQuery{WorkspaceID: query.WorkspaceID, Provenance: append([]domain.ProvenanceRef(nil), query.Provenance...)}
	for _, ref := range canonical.Provenance {
		if ref.WorkspaceID != query.WorkspaceID || domain.ValidateProvenanceRef(ref) != nil {
			return ResolveEvidenceTopicsQuery{}, requestError("evidence topic query provenance is invalid")
		}
		if _, duplicate := seen[ref]; duplicate {
			return ResolveEvidenceTopicsQuery{}, requestError("evidence topic query contains duplicate provenance")
		}
		seen[ref] = struct{}{}
	}
	sort.Slice(canonical.Provenance, func(i, j int) bool {
		return evidenceTopicProvenanceKey(canonical.Provenance[i]) < evidenceTopicProvenanceKey(canonical.Provenance[j])
	})
	return canonical, nil
}

func evidenceTopicBindingKey(binding domain.EvidenceTopicBinding) string {
	return evidenceTopicProvenanceKey(binding.Provenance) + "\x00" + string(binding.TopicID)
}

func evidenceTopicProvenanceKey(ref domain.ProvenanceRef) string {
	return string(ref.SourceVersionID) + "\x00" + string(ref.SourceSpanID)
}
