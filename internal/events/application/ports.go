// Package application 定义 Server Event 重放的短查询端口。
package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Store 提供按 Workspace 隔离的持久事件水位和有界重放读取。
type Store interface {
	// CurrentWatermark 返回该 Workspace 已分配的最高事件序号；无事件时返回零。
	CurrentWatermark(context.Context, foundation.ID) (int64, error)
	// EarliestRetained 返回当前仍在逻辑保留窗口内的最早序号；无保留事件时返回 nil。
	EarliestRetained(context.Context, foundation.ID) (*int64, error)
	// ListAfter 按序号升序返回游标之后且仍在保留窗口内的有界事件。
	ListAfter(context.Context, foundation.ID, int64, int) ([]domain.ServerEvent, error)
}

// ScopedAppender 在调用方拥有的事务内追加或精确重放一个 Server Event。
type ScopedAppender interface {
	// AppendScoped 不提交或回滚 scope；返回值中的 bool 表示 exact replay。
	AppendScoped(context.Context, foundation.TransactionScope, domain.AppendRequest) (domain.ServerEvent, bool, error)
}

// ScopedStore 提供不暴露具体数据库事务类型的完整 Server Event Store。
type ScopedStore interface {
	Store
	ScopedAppender
}
