package river

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
)

type scopedTypedJobInserter interface {
	InsertTx(context.Context, foundation.TransactionScope, Args, riveradapter.InsertOptions) (riveradapter.JobReceipt, error)
}

// ScopedInserter 将 Reindex Args 写入 caller-owned opaque transaction scope。
type ScopedInserter struct {
	typed scopedTypedJobInserter
}

// ScopedApplicationInserter 将 Application ReindexJob 映射为 scoped River Args。
type ScopedApplicationInserter struct {
	transport *ScopedInserter
}

var _ application.ScopedJobInserter = (*ScopedApplicationInserter)(nil)

// NewScopedInserter 复用 Workflow 的 database/sql typed inserter 和同 Pool transaction resolver。
func NewScopedInserter(database *platformpostgres.Pool, client *riveradapter.Client, fence riveradapter.ScopedEnqueueFence) (*ScopedInserter, error) {
	typed, err := riveradapter.NewScopedTypedJobInserter(database, client, ValidateArgs, fence)
	if err != nil {
		return nil, err
	}
	return &ScopedInserter{typed: typed}, nil
}

// NewScopedApplicationInserter 创建 ScopedDispatcher 可直接使用的 Reindex Job Adapter。
func NewScopedApplicationInserter(database *platformpostgres.Pool, client *riveradapter.Client, fence riveradapter.ScopedEnqueueFence) (*ScopedApplicationInserter, error) {
	transport, err := NewScopedInserter(database, client, fence)
	if err != nil {
		return nil, err
	}
	return &ScopedApplicationInserter{transport: transport}, nil
}

// InsertScoped 在调用方事务 scope 内插入一个校验后的 Reindex transport Job。
func (i *ScopedInserter) InsertScoped(ctx context.Context, scope foundation.TransactionScope, args Args, scheduledAt time.Time) (riveradapter.JobReceipt, error) {
	if err := ValidateArgs(args); err != nil {
		return riveradapter.JobReceipt{}, err
	}
	if ctx == nil {
		return riveradapter.JobReceipt{}, argsError(foundation.ErrorInvalidInput, "REINDEX_JOB_CONTEXT_INVALID", errors.New("reindex job context is nil"))
	}
	typed := scopedTypedJobInserter((*riveradapter.ScopedTypedJobInserter[Args])(nil))
	if i != nil && i.typed != nil {
		typed = i.typed
	}
	return typed.InsertTx(ctx, scope, args, riveradapter.InsertOptions{ScheduledAt: scheduledAt.UTC()})
}

// InsertScoped 实现 Application ScopedJobInserter，transport payload 仍只包含稳定任务身份。
func (i *ScopedApplicationInserter) InsertScoped(ctx context.Context, scope foundation.TransactionScope, job application.ReindexJob) (application.JobReceipt, error) {
	args, err := NewArgs(job.DeliveryID, job.DispatchNo)
	if err != nil {
		return application.JobReceipt{}, err
	}
	transport := (*ScopedInserter)(nil)
	if i != nil {
		transport = i.transport
	}
	receipt, err := transport.InsertScoped(ctx, scope, args, job.ScheduledAt)
	if err != nil {
		return application.JobReceipt{}, err
	}
	return application.JobReceipt{JobID: receipt.JobID, Duplicate: receipt.Duplicate}, nil
}
