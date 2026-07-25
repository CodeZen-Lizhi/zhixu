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
