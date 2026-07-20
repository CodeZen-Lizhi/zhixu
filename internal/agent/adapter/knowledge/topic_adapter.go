package knowledge

import (
	"context"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// EvidenceTopicService 是 Related Topic 解析允许调用的 Knowledge Application 最小只读 seam。
type EvidenceTopicService interface {
	// ResolveEvidenceTopics 单批解析正式证据绑定的 Active Topic。
	ResolveEvidenceTopics(context.Context, knowledgeapplication.ResolveEvidenceTopicsQuery) ([]knowledgedomain.EvidenceTopicBinding, error)
}

// TopicAdapter 将 RAG Related Topic 查询委托给 Knowledge Application。
type TopicAdapter struct {
	service EvidenceTopicService
}

// NewTopicAdapter 创建不改变既有 NewAdapter 依赖的 Related Topic 只读适配器。
func NewTopicAdapter(service EvidenceTopicService) (*TopicAdapter, error) {
	if nilDependency(service) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New("knowledge evidence topic service is unavailable"))
	}
	return &TopicAdapter{service: service}, nil
}

// ResolveEvidenceTopics 委托 Knowledge Application 执行 Workspace 作用域批量 Topic 解析。
func (adapter *TopicAdapter) ResolveEvidenceTopics(ctx context.Context, query knowledgeapplication.ResolveEvidenceTopicsQuery) ([]knowledgedomain.EvidenceTopicBinding, error) {
	if adapter == nil || nilDependency(adapter.service) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New("knowledge topic adapter is not initialized"))
	}
	return adapter.service.ResolveEvidenceTopics(ctx, query)
}

// ResolveRAGTopics 把 Agent 的窄参数转换为 Knowledge Application 查询。
func (adapter *TopicAdapter) ResolveRAGTopics(ctx context.Context, workspaceID foundation.ID, provenance []knowledgedomain.ProvenanceRef) ([]knowledgedomain.EvidenceTopicBinding, error) {
	return adapter.ResolveEvidenceTopics(ctx, knowledgeapplication.ResolveEvidenceTopicsQuery{
		WorkspaceID: workspaceID,
		Provenance:  provenance,
	})
}

var _ EvidenceTopicService = (*knowledgeapplication.EvidenceTopicService)(nil)
var _ agentapplication.RAGEvidenceTopicPort = (*TopicAdapter)(nil)
