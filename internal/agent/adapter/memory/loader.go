// Package memory maps effective Memory facts into Agent-owned non-evidence context items.
package memory

import (
	"context"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

// Loader 固定 stable single-user owner，并且只通过 Memory Application Service 读取有效事实。
type Loader struct {
	service *memoryapplication.Service
	owner   memorydomain.Principal
}

// NewLoader 创建 Conversation RAG 专用的有效 Memory Adapter。
func NewLoader(service *memoryapplication.Service, owner memorydomain.Principal) (*Loader, error) {
	if service == nil || memorydomain.ValidatePrincipal(owner) != nil || owner != memorydomain.SingleUserOwner() {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeMemoryContextUnavailable, false, errors.New("agent memory loader dependency is unavailable"))
	}
	return &Loader{service: service, owner: owner}, nil
}

// Load 返回稳定排序的 Agent-owned item；正文仍只来自 Memory 事实源和本次内存请求。
func (loader *Loader) Load(ctx context.Context, query agentapplication.EffectiveMemoryQuery) ([]agentapplication.EffectiveMemoryItem, error) {
	if loader == nil || loader.service == nil || loader.owner != memorydomain.SingleUserOwner() ||
		query.Owner.Kind != string(loader.owner.Kind) || query.Owner.ID != loader.owner.ID {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, agentapplication.ErrorCodeMemoryContextUnavailable, false, errors.New("agent memory loader binding is unavailable"))
	}
	taskScopeID := query.TaskScopeID
	items, err := loader.service.LoadEffective(ctx, memoryapplication.EffectiveQuery{
		Scope:       memoryapplication.Scope{WorkspaceID: query.WorkspaceID, Owner: loader.owner},
		TaskScopeID: &taskScopeID,
		Limit:       query.Limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]agentapplication.EffectiveMemoryItem, 0, len(items))
	for _, item := range items {
		category := agentapplication.MemoryCategoryTaskContext
		if item.Type == memorydomain.TypePreference {
			category = agentapplication.MemoryCategoryUserPreference
		}
		result = append(result, agentapplication.EffectiveMemoryItem{
			ID: item.ID, Version: item.Version, Type: string(item.Type), Category: category,
			Content: append([]byte(nil), item.Content...),
		})
	}
	return result, nil
}

var _ agentapplication.EffectiveMemoryLoader = (*Loader)(nil)
