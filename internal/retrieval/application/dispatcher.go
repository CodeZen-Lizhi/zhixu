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

// BatchDispatcher 是 Runner 使用的最小批量派发契约。
type BatchDispatcher interface {
	DispatchBatch(context.Context, int) error
}

type dispatchOneFunc func(context.Context, DispatchPath) (bool, error)

func dispatchBatch(ctx context.Context, batch int, mu *sync.Mutex, next *DispatchPath, dispatch dispatchOneFunc) error {
	if ctx == nil || batch < 1 || batch > maxDispatchBatchSize {
		return dispatcherError(foundation.ErrorInvalidInput, "REINDEX_DISPATCH_BATCH_INVALID", false, errors.New("dispatch batch is outside the supported range"))
	}

	mu.Lock()
	defer mu.Unlock()
	for dispatched := 0; dispatched < batch; dispatched++ {
		path := *next
		found, err := dispatch(ctx, path)
		if err != nil {
			return err
		}
		if !found {
			path = otherDispatchPath(path)
			found, err = dispatch(ctx, path)
			if err != nil {
				return err
			}
		}
		if !found {
			return nil
		}
		*next = otherDispatchPath(path)
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
