package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	AnchorDiscoveryWaiting   = "WAITING_PROFILE"
	AnchorDiscoveryPrepared  = "PREPARED"
	AnchorDiscoveryRequested = "REQUESTED"
	AnchorDiscoveryStale     = "STALE"
)

// 发现记录持久订阅一个 source-ready 事实和一个已确认的范围修订，
// 不会再次消费该事实。
type AnchorSourceDiscovery struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	ProcessingID      foundation.ID
	AnchorID          foundation.ID
	ScopeVersion      int64
	Source            domain.SynthesisSourceVersion
	Status            string
	Version           int64
	ProfileRevisionID foundation.ID
	Evidence          []domain.SynthesisSourceRef
	RequestIDs        []foundation.ID
	ErrorCode         string
}

type AnchorDiscoveryStore interface {
	SeedAnchorSourceDiscoveries(context.Context, int) (int, error)
	ListDueAnchorSourceDiscoveries(context.Context, int) ([]AnchorSourceDiscovery, error)
	// 若其他扫描器已先完成冻结，Freeze 返回已保存的快照。
	FreezeAnchorSourceDiscovery(context.Context, AnchorSourceDiscovery, foundation.ID, []domain.SynthesisSourceRef) (AnchorSourceDiscovery, error)
	CompleteAnchorSourceDiscovery(context.Context, AnchorSourceDiscovery, []foundation.ID) error
	// Defer 持久保存有界的重试延迟；stale 是此范围与来源组合的终态。
	DeferAnchorSourceDiscovery(context.Context, AnchorSourceDiscovery, string, bool) error
}

// 只解析所选知识目录片段的元数据。
// 缩小范围后，再由建议执行器打开来源原文。
type AnchorDiscoverySourceResolver interface {
	ResolveAnchorDiscoverySources(context.Context, domain.SynthesisSourceVersion, []foundation.ID) ([]domain.SynthesisSourceRef, error)
}
