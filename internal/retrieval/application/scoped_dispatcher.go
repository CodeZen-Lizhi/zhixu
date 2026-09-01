package application

import (
	"context"
	"errors"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedJobInserter 在调用方持有的事务 scope 内插入一个 Reindex transport Job。
// 实现不得开始、提交、回滚事务，也不得回退到 root 数据库。
type ScopedJobInserter interface {
	InsertScoped(context.Context, foundation.TransactionScope, ReindexJob) (JobReceipt, error)
}

// ScopedDispatcherStore 每次调用只在一个独立的 caller-owned scope 中派发一条记录。
type ScopedDispatcherStore interface {
	DispatchOneScoped(context.Context, DispatchPath, ScopedJobInserter) (bool, error)
}

// ScopedDispatcher 在 first/retry 两条 scoped 领取路径间公平交替。
type ScopedDispatcher struct {
	store ScopedDispatcherStore
	jobs  ScopedJobInserter

	mu   sync.Mutex
	next DispatchPath
}

var _ BatchDispatcher = (*ScopedDispatcher)(nil)

// NewScopedDispatcher 创建只接受 opaque TransactionScope 的 Reindex Dispatcher。
func NewScopedDispatcher(store ScopedDispatcherStore, jobs ScopedJobInserter) (*ScopedDispatcher, error) {
	if nilDispatcherDependency(store) || nilDispatcherDependency(jobs) {
		return nil, dispatcherError(foundation.ErrorDependencyUnavailable, "REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", true, errors.New("scoped dispatcher store or job inserter is nil"))
	}
	return &ScopedDispatcher{store: store, jobs: jobs, next: DispatchPathFirst}, nil
}

// DispatchBatch 最多派发 batch 条记录；每条记录的事务生命周期由 scoped Store 独立拥有。
func (d *ScopedDispatcher) DispatchBatch(ctx context.Context, batch int) error {
	if d == nil || nilDispatcherDependency(d.store) || nilDispatcherDependency(d.jobs) {
		return dispatcherError(foundation.ErrorDependencyUnavailable, "REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", true, errors.New("scoped dispatcher is not initialized"))
	}
	return dispatchBatch(ctx, batch, &d.mu, &d.next, func(ctx context.Context, path DispatchPath) (bool, error) {
		return d.store.DispatchOneScoped(ctx, path, d.jobs)
	})
}
