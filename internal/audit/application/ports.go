// Package application 定义 Audit 追加与查询所需的窄端口。
package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Store 提供 append-only Audit 的追加和有界只读查询。
type Store interface {
	// Append 追加一个事件；返回值中的 replayed 表示同一幂等键的精确重放。
	Append(context.Context, domain.Event) (domain.Event, bool, error)
	// AppendTx 在调用方事务内追加事件，不提交或回滚事务。
	AppendTx(context.Context, any, domain.Event) (domain.Event, bool, error)
	// Get 按 ID 读取一条安全、脱敏后的 Audit 记录；调用方必须先完成访问控制。
	Get(context.Context, foundation.ID) (domain.Event, error)
	// List 返回单个 Workspace（nil 表示全局事件）按发生时间和 ID 倒序排列的有界记录。
	List(context.Context, domain.ListQuery) ([]domain.Event, error)
}

// Appender 是只依赖事务追加能力的最小接口，便于领域事务注入。
type Appender interface {
	// AppendTx 在已有事务中追加或精确重放一个 Audit 事件。
	AppendTx(context.Context, any, domain.Event) (domain.Event, bool, error)
}

// ScopedAppender 是 GORM 迁移路径使用的 opaque transaction 追加边界。
// legacy pgx 调用方继续使用 Appender，直到各自模块完成迁移。
type ScopedAppender interface {
	// AppendScoped 在已有 opaque transaction 中追加或精确重放一个 Audit 事件。
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
	Get(context.Context, foundation.ID) (domain.Event, error)
	List(context.Context, domain.ListQuery) ([]domain.Event, error)
}
