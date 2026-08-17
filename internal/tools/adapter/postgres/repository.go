package postgres

import (
	"context"
	"errors"
	"reflect"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

type transactionStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 在 PostgreSQL 中实现 Tool Call Repository 与 Workflow Policy Reader。
type Repository struct {
	db     transactionStarter
	events eventsapplication.Appender
	audit  workspaceAnalysisToolRefusalAuditRecorder
}

// NewRepositoryWithWorkspaceAnalysisEventsAndAudit constructs the complete
// Workspace Analysis tool persistence boundary. Refusal facts require both the
// timeline event writer and the append-only audit writer in the same UoW.
func NewRepositoryWithWorkspaceAnalysisEventsAndAudit(
	database transactionStarter,
	events eventsapplication.Appender,
	audit *auditapplication.Recorder,
) (*Repository, error) {
	repository, err := NewRepositoryWithWorkspaceAnalysisEvents(database, events)
	if err != nil {
		return nil, err
	}
	if isNilWorkspaceAnalysisToolRefusalAuditRecorder(audit) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("workspace analysis audit recorder is missing"))
	}
	repository.audit = audit
	return repository, nil
}

// NewRepository 构造 Tool PostgreSQL Adapter；数据库依赖缺失时 fail closed。
func NewRepository(database transactionStarter) (*Repository, error) {
	if isNilDatabase(database) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("tool database is missing"))
	}
	return &Repository{db: database}, nil
}

// NewRepositoryWithWorkspaceAnalysisEvents 构造要求工具调用事件闭包的 Workspace Analysis Repository。
// 旧构造器保留给不参与该受限流程的既有调用方。
func NewRepositoryWithWorkspaceAnalysisEvents(database transactionStarter, events eventsapplication.Appender) (*Repository, error) {
	repository, err := NewRepository(database)
	if err != nil {
		return nil, err
	}
	if isNilWorkspaceAnalysisEventAppender(events) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("workspace analysis event appender is missing"))
	}
	repository.events = events
	return repository, nil
}

func isNilDatabase(database transactionStarter) bool {
	if database == nil {
		return true
	}
	value := reflect.ValueOf(database)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func isNilWorkspaceAnalysisEventAppender(appender eventsapplication.Appender) bool {
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

func isNilWorkspaceAnalysisToolRefusalAuditRecorder(recorder workspaceAnalysisToolRefusalAuditRecorder) bool {
	if recorder == nil {
		return true
	}
	value := reflect.ValueOf(recorder)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (repository *Repository) begin(ctx context.Context) (pgx.Tx, error) {
	if repository == nil || isNilDatabase(repository.db) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("tool repository is not initialized"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, classify(err)
	}
	return tx, nil
}
