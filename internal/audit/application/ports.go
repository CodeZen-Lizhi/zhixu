// Package application 定义 Audit 追加与查询所需的窄端口。
package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedAppender 在调用方拥有的事务中追加或精确重放一个 Audit 事件。
type ScopedAppender interface {
	// AppendScoped 不提交或回滚 scope；返回值中的 bool 表示同一幂等键的精确重放。
	AppendScoped(context.Context, foundation.TransactionScope, domain.Event) (domain.Event, bool, error)
}

// ScopedReader 在 opaque caller-owned transaction 中读取不可变 Audit 事实。
// 它只用于跨 owner 的 durable closure 验证，不执行访问控制或拥有事务。
type ScopedReader interface {
	// GetScoped 按 ID 读取事件，不提交或回滚调用方事务。
	GetScoped(context.Context, foundation.TransactionScope, foundation.ID) (domain.Event, error)
}

// ScopedStore 提供不暴露具体数据库事务类型的完整 Audit Store 契约。
type ScopedStore interface {
	Repository
	ScopedAppender
	// Get 按 ID 读取安全、脱敏后的 Audit 记录；调用方必须先完成访问控制。
	Get(context.Context, foundation.ID) (domain.Event, error)
	// List 返回单个 Workspace（nil 表示全局事件）按发生时间和 ID 倒序排列的有界记录。
	List(context.Context, domain.ListQuery) ([]domain.Event, error)
}
