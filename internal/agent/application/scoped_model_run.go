package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedModelRunFinalizer 允许调用方在其持有的强类型事务 scope 内读取并终结 Model Run。
// 实现不得提交、回滚或缓存 scope。
type ScopedModelRunFinalizer interface {
	GetModelRunScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (domain.ModelRun, error)
	GetModelRunRecordScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (ModelRunRecord, error)
	FinalizeModelRunScoped(context.Context, foundation.TransactionScope, FinalizeModelRunCommand) (domain.ModelRun, bool, error)
}

// ScopedModelRunAttemptFinder 在调用方事务 scope 内按 Node Attempt 查找 Model Run。
type ScopedModelRunAttemptFinder interface {
	GetModelRunByAttemptScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (domain.ModelRun, bool, error)
}

// ScopedModelRunStore 组合需要在同一强类型事务内读取、终结和按 Attempt 查询的调用方能力。
type ScopedModelRunStore interface {
	ScopedModelRunFinalizer
	ScopedModelRunAttemptFinder
}
