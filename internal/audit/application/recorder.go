// Package application 提供 append-only Audit 的应用边界。
package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Repository 追加审计事件并对相同幂等键执行精确重放。
type Repository interface {
	Append(context.Context, domain.Event) (domain.Event, bool, error)
}

// Recorder 是产生业务决策的模块可注入的窄审计接口。
type Recorder struct{ repository Repository }

// NewRecorder 构造审计记录器。
func NewRecorder(repository Repository) (*Recorder, error) {
	if nilRepository(repository) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("audit repository is unavailable"))
	}
	return &Recorder{repository: repository}, nil
}

// Record 校验并追加审计事件；精确重放返回既有事实。
func (recorder *Recorder) Record(ctx context.Context, event domain.Event) (domain.Event, bool, error) {
	if err := recorder.ready(ctx); err != nil {
		return domain.Event{}, false, err
	}
	redacted, err := event.Redacted()
	if err != nil {
		return domain.Event{}, false, err
	}
	persisted, replayed, err := recorder.repository.Append(ctx, redacted)
	if err != nil {
		return domain.Event{}, false, err
	}
	return validateRecordedEvent(redacted, persisted, replayed)
}

// RecordTx 在调用方事务内校验并追加审计事件；不会提交或回滚事务。
func (recorder *Recorder) RecordTx(ctx context.Context, transaction any, event domain.Event) (domain.Event, bool, error) {
	if err := recorder.ready(ctx); err != nil {
		return domain.Event{}, false, err
	}
	appender, ok := recorder.repository.(Appender)
	if !ok || nilAppender(appender) {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable, true, errors.New("audit transaction appender is unavailable"))
	}
	redacted, err := event.Redacted()
	if err != nil {
		return domain.Event{}, false, err
	}
	persisted, replayed, err := appender.AppendTx(ctx, transaction, redacted)
	if err != nil {
		return domain.Event{}, false, err
	}
	return validateRecordedEvent(redacted, persisted, replayed)
}

// RecordScoped validates and appends an Audit event in an opaque caller-owned
// transaction. It does not commit or roll back the transaction.
func (recorder *Recorder) RecordScoped(ctx context.Context, scope foundation.TransactionScope, event domain.Event) (domain.Event, bool, error) {
	if err := recorder.ready(ctx); err != nil {
		return domain.Event{}, false, err
	}
	appender, ok := recorder.repository.(ScopedAppender)
	if !ok || nilScopedAppender(appender) {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable, true, errors.New("audit scoped transaction appender is unavailable"))
	}
	redacted, err := event.Redacted()
	if err != nil {
		return domain.Event{}, false, err
	}
	persisted, replayed, err := appender.AppendScoped(ctx, scope, redacted)
	if err != nil {
		return domain.Event{}, false, err
	}
	return validateRecordedEvent(redacted, persisted, replayed)
}

// ReadScoped loads one immutable Audit event from the caller-owned opaque
// transaction. The caller remains responsible for authorization and lifecycle.
func (recorder *Recorder) ReadScoped(ctx context.Context, scope foundation.TransactionScope, id foundation.ID) (domain.Event, error) {
	if err := recorder.ready(ctx); err != nil {
		return domain.Event{}, err
	}
	reader, ok := recorder.repository.(ScopedReader)
	if !ok || nilScopedReader(reader) {
		return domain.Event{}, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable, true, errors.New("audit scoped transaction reader is unavailable"))
	}
	persisted, err := reader.GetScoped(ctx, scope, id)
	if err != nil {
		return domain.Event{}, err
	}
	if persisted.Validate() != nil || persisted.ID != id {
		return domain.Event{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("audit repository returned a different scoped event"))
	}
	return persisted, nil
}

func (recorder *Recorder) ready(ctx context.Context) error {
	if recorder == nil || nilRepository(recorder.repository) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("audit recorder is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, errors.New("audit context is nil"))
	}
	return nil
}

func validateRecordedEvent(event, persisted domain.Event, replayed bool) (domain.Event, bool, error) {
	if err := persisted.Validate(); err != nil {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("audit repository returned an invalid event"))
	}
	if event.WorkspaceID == nil != (persisted.WorkspaceID == nil) || event.WorkspaceID != nil && *event.WorkspaceID != *persisted.WorkspaceID {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("audit repository crossed workspace boundary"))
	}
	if !replayed && persisted.ID != event.ID {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("audit repository changed a newly created event id"))
	}
	if !domain.EqualBinding(event, persisted) {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("audit repository returned a different event binding"))
	}
	return persisted, replayed, nil
}

func nilAppender(appender Appender) bool {
	if appender == nil {
		return true
	}
	value := reflect.ValueOf(appender)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilScopedAppender(appender ScopedAppender) bool {
	if appender == nil {
		return true
	}
	value := reflect.ValueOf(appender)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilScopedReader(reader ScopedReader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilRepository(repository Repository) bool {
	if repository == nil {
		return true
	}
	value := reflect.ValueOf(repository)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
