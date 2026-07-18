package application

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const maxDispatchBatchSize = 1_000

// DispatchPath 区分首次 Outbox 派发与到期业务重试，两条路径不得共享查询。
type DispatchPath uint8

const (
	// DispatchPathFirst 领取尚未发布的 Reindex Outbox。
	DispatchPathFirst DispatchPath = iota + 1
	// DispatchPathRetry 领取到期的 retry_wait Delivery。
	DispatchPathRetry
)

// ReindexJob 是 Application 交给 transport Adapter 的最小任务身份。
type ReindexJob struct {
	DeliveryID  foundation.ID
	DispatchNo  int
	ScheduledAt time.Time
}

// JobReceipt 是事务型任务插入返回的稳定回执。
type JobReceipt struct {
	JobID     int64
	Duplicate bool
}

// JobInserter 在调用方数据库事务中插入一个 Reindex transport Job。
type JobInserter interface {
	InsertTx(context.Context, any, ReindexJob) (JobReceipt, error)
}

// DispatcherStore 每次调用只派发一条记录，并拥有对应短事务。
type DispatcherStore interface {
	DispatchOne(context.Context, DispatchPath, JobInserter) (bool, error)
}

// BatchDispatcher 是 Runner 使用的最小批量派发契约。
type BatchDispatcher interface {
	DispatchBatch(context.Context, int) error
}

// Dispatcher 在 first/retry 两条互斥领取路径间公平交替。
type Dispatcher struct {
	store DispatcherStore
	jobs  JobInserter

	mu   sync.Mutex
	next DispatchPath
}

var _ BatchDispatcher = (*Dispatcher)(nil)

// NewDispatcher 创建事务型 Reindex Dispatcher。
func NewDispatcher(store DispatcherStore, jobs JobInserter) (*Dispatcher, error) {
	if nilDispatcherDependency(store) || nilDispatcherDependency(jobs) {
		return nil, dispatcherError(foundation.ErrorDependencyUnavailable, "REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", true, errors.New("dispatcher store or job inserter is nil"))
	}
	return &Dispatcher{store: store, jobs: jobs, next: DispatchPathFirst}, nil
}

// DispatchBatch 最多派发 batch 条记录；每条记录由 Store 使用独立短事务提交。
func (d *Dispatcher) DispatchBatch(ctx context.Context, batch int) error {
	if d == nil || nilDispatcherDependency(d.store) || nilDispatcherDependency(d.jobs) {
		return dispatcherError(foundation.ErrorDependencyUnavailable, "REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", true, errors.New("dispatcher is not initialized"))
	}
	if ctx == nil || batch < 1 || batch > maxDispatchBatchSize {
		return dispatcherError(foundation.ErrorInvalidInput, "REINDEX_DISPATCH_BATCH_INVALID", false, errors.New("dispatch batch is outside the supported range"))
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for dispatched := 0; dispatched < batch; dispatched++ {
		path := d.next
		found, err := d.store.DispatchOne(ctx, path, d.jobs)
		if err != nil {
			return err
		}
		if !found {
			path = otherDispatchPath(path)
			found, err = d.store.DispatchOne(ctx, path, d.jobs)
			if err != nil {
				return err
			}
		}
		if !found {
			return nil
		}
		d.next = otherDispatchPath(path)
	}
	return nil
}

func otherDispatchPath(path DispatchPath) DispatchPath {
	if path == DispatchPathRetry {
		return DispatchPathFirst
	}
	return DispatchPathRetry
}

func nilDispatcherDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func dispatcherError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
