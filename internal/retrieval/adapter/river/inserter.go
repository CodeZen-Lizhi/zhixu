package river

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
)

type typedJobInserter interface {
	InsertTx(context.Context, any, Args, riveradapter.InsertOptions) (riveradapter.JobReceipt, error)
}

// Inserter 将 Reindex Delivery 参数适配到共享的 River 事务插入边界。
type Inserter struct {
	typed typedJobInserter
}

// ApplicationInserter 将 Application ReindexJob 映射为固定三字段 River Args。
type ApplicationInserter struct {
	transport *Inserter
}

// NewInserter 使用现有 schema-scoped River Client 构造 Reindex Inserter。
func NewInserter(client *riveradapter.Client) (*Inserter, error) {
	typed, err := riveradapter.NewTypedJobInserter(client, ValidateArgs)
	if err != nil {
		return nil, err
	}
	return &Inserter{typed: typed}, nil
}

// NewApplicationInserter 创建 Dispatcher 可直接使用的事务插入 Adapter。
func NewApplicationInserter(client *riveradapter.Client) (*ApplicationInserter, error) {
	transport, err := NewInserter(client)
	if err != nil {
		return nil, err
	}
	return &ApplicationInserter{transport: transport}, nil
}

// InsertTx 先校验 Reindex Args，再在调用方事务中插入唯一 River Job。
func (i *Inserter) InsertTx(ctx context.Context, transaction any, args Args, scheduledAt time.Time) (riveradapter.JobReceipt, error) {
	if err := ValidateArgs(args); err != nil {
		return riveradapter.JobReceipt{}, err
	}
	typed := typedJobInserter((*riveradapter.TypedJobInserter[Args])(nil))
	if i != nil && i.typed != nil {
		typed = i.typed
	}
	return typed.InsertTx(ctx, transaction, args, riveradapter.InsertOptions{ScheduledAt: scheduledAt.UTC()})
}

// InsertTx 实现 Application JobInserter，并保持 transport payload 不含业务字段。
func (i *ApplicationInserter) InsertTx(ctx context.Context, transaction any, job application.ReindexJob) (application.JobReceipt, error) {
	args, err := NewArgs(job.DeliveryID, job.DispatchNo)
	if err != nil {
		return application.JobReceipt{}, err
	}
	transport := (*Inserter)(nil)
	if i != nil {
		transport = i.transport
	}
	receipt, err := transport.InsertTx(ctx, transaction, args, job.ScheduledAt)
	if err != nil {
		return application.JobReceipt{}, err
	}
	return application.JobReceipt{JobID: receipt.JobID, Duplicate: receipt.Duplicate}, nil
}
