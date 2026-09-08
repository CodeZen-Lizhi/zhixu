package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// ScopedFrozenMaterialFence 在确认方持有的事务中锁定并重验冻结材料。
// 实现不能创建另一事务、提交、回滚或缓存 scope。
type ScopedFrozenMaterialFence interface {
	VerifyFrozenScoped(context.Context, foundation.TransactionScope, foundation.ID, []domain.MaterialRef) error
}

// TerminalResultRequest 将成功终态结果绑定到冻结的 Workflow Definition。
type TerminalResultRequest struct {
	Result            domain.RunResult
	DefinitionKey     string
	DefinitionVersion int64
}

// ScopedTerminalResultWriter 在 Workflow 终态事务中追加或精确重放结果。
type ScopedTerminalResultWriter interface {
	BindSucceededResultScoped(context.Context, foundation.TransactionScope, TerminalResultRequest) error
}
