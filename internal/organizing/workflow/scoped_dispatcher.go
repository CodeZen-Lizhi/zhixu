package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// AtomicStartRecord 将持有的材料 Outbox lease 与服务器构造的 Workflow 请求绑定。
type AtomicStartRecord struct {
	Lease     organizingapp.StartOutboxLease
	Request   workflowapp.RuntimeStartRequest
	BindingID foundation.ID
	StartedAt time.Time
}

// AtomicStartRepository 在同一事务中闭合 Workflow/River、RunBinding 与 Start Outbox。
type AtomicStartRepository interface {
	organizingapp.StartRepository
	StartWorkflow(context.Context, AtomicStartRecord) (organizingdomain.RunBinding, bool, error)
}

// ScopedDispatcherDependencies 提供持久启动事务、冻结 Definition 和原有重试策略。
type ScopedDispatcherDependencies struct {
	Repository    AtomicStartRepository
	Definitions   *workflowapp.DefinitionRegistry
	IDs           foundation.IDGenerator
	Clock         foundation.Clock
	Owner         string
	LeaseDuration time.Duration
	RetryBase     time.Duration
	MaxAttempts   int
}

// ScopedDispatcher 将有界 Outbox 领取交给同事务 Workflow 启动端口。
type ScopedDispatcher struct{ dependencies ScopedDispatcherDependencies }

// NewScopedDispatcher 创建不暴露数据库类型的原子启动 Dispatcher。
func NewScopedDispatcher(dependencies ScopedDispatcherDependencies) (*ScopedDispatcher, error) {
	dependencies.Owner = strings.TrimSpace(dependencies.Owner)
	if nilScopedDependency(dependencies.Repository) || dependencies.Definitions == nil || nilScopedDependency(dependencies.IDs) || nilScopedDependency(dependencies.Clock) ||
		dependencies.Owner == "" || dependencies.LeaseDuration <= 0 || dependencies.RetryBase <= 0 || dependencies.MaxAttempts < 1 || dependencies.MaxAttempts > 1000 {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_DISPATCHER_UNAVAILABLE", true, "organizing dispatcher dependencies are incomplete")
	}
	return &ScopedDispatcher{dependencies: dependencies}, nil
}

func nilScopedDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	}
	return false
}

// DispatchBatch 最多启动 limit 个到期 Snapshot，并保持原有有界重试与 poison 语义。
func (dispatcher *ScopedDispatcher) DispatchBatch(ctx context.Context, limit int) (DispatchBatchResult, error) {
	if dispatcher == nil || ctx == nil || limit < 1 || limit > 100 {
		return DispatchBatchResult{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_DISPATCH_BATCH_INVALID", false, "organizing dispatch batch is invalid")
	}
	dependencies := dispatcher.dependencies
	var result DispatchBatchResult
	for result.Claimed < limit {
		lease, found, err := dependencies.Repository.ClaimStart(ctx, dependencies.Owner, dependencies.LeaseDuration)
		if err != nil {
			return result, err
		}
		if !found {
			return result, nil
		}
		result.Claimed++
		key, version, err := definitionForKind(lease.TemplateKind)
		if err != nil {
			if poisonErr := poisonStart(ctx, dependencies.Repository, lease, "ORGANIZING_TEMPLATE_KIND_UNREGISTERED"); poisonErr != nil {
				return result, errors.Join(err, poisonErr)
			}
			result.Poisoned++
			continue
		}
		definition, startErr := dependencies.Definitions.Resolve(key, version)
		var replayed bool
		if startErr == nil {
			input, marshalErr := json.Marshal(StartInput{SnapshotID: lease.SnapshotID})
			if marshalErr != nil {
				return result, marshalErr
			}
			request, requestErr := workflowapp.BuildRuntimeStartRequest(dependencies.IDs, dependencies.Clock, lease.WorkspaceID,
				organizingapp.StartIdempotencyKey(lease.SnapshotID), input, definition)
			startErr = requestErr
			if requestErr == nil {
				bindingID, idErr := dependencies.IDs.New()
				if idErr != nil {
					return result, idErr
				}
				_, replayed, startErr = dependencies.Repository.StartWorkflow(ctx, AtomicStartRecord{Lease: lease, Request: request,
					BindingID: bindingID, StartedAt: canonicalTime(dependencies.Clock.Now())})
			}
		}
		if startErr != nil {
			if err := recoverStartFailure(ctx, dependencies.Repository, dependencies.RetryBase, dependencies.MaxAttempts, lease, startErr, &result); err != nil {
				return result, err
			}
			continue
		}
		if replayed {
			result.Replayed++
		} else {
			result.Started++
		}
	}
	return result, nil
}
