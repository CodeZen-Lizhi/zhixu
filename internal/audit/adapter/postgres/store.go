package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// DB 是 Audit 查询所需的最小 PostgreSQL 边界。
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type transactionStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Store 从 ops.audit_event 提供追加、精确重放和有界读取。
type Store struct{ db DB }

// Repository 是 Store 的兼容别名。
type Repository = Store

// NewStore 创建 Audit PostgreSQL Store；不会创建或迁移数据库连接。
func NewStore(db DB) (*Store, error) {
	if isNilDB(db) {
		return nil, domainErrorUnavailable(errors.New("audit database is nil"))
	}
	return &Store{db: db}, nil
}

// NewRepository 创建 Audit PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) { return NewStore(db) }

// Append 在独立事务中追加事件，成功后才返回；重放不会制造第二条记录。
func (store *Store) Append(ctx context.Context, event domain.Event) (domain.Event, bool, error) {
	if store == nil || isNilDB(store.db) {
		return domain.Event{}, false, domainErrorUnavailable(errors.New("audit store is unavailable"))
	}
	if ctx == nil {
		return domain.Event{}, false, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit append context is nil"))
	}
	starter, ok := store.db.(transactionStarter)
	if !ok || isNilStarter(starter) {
		return domain.Event{}, false, domainErrorUnavailable(errors.New("audit database does not support transactions"))
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return domain.Event{}, false, classifyStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	created, replayed, appendErr := store.AppendTx(ctx, tx, event)
	if appendErr != nil {
		return domain.Event{}, false, appendErr
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Event{}, false, classifyStoreError(err)
	}
	return created, replayed, nil
}

// Get 按 ID 读取一条安全 Audit 记录；该低层方法仅供已经完成访问控制的内部调用方使用。
func (store *Store) Get(ctx context.Context, id foundation.ID) (domain.Event, error) {
	if err := store.validateQuery(ctx); err != nil {
		return domain.Event{}, err
	}
	parsed, err := foundation.ParseID(string(id))
	if err != nil || parsed != id {
		return domain.Event{}, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit id is invalid"))
	}
	event, err := scanEvent(store.db.QueryRow(ctx, auditSelect+` WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, errors.New("audit event was not found"))
	}
	return event, err
}

// List 按 occurred_at/id 倒序返回一个 Workspace（nil 表示全局事件）的一页脱敏 Audit；
// limit 必须显式有界，游标时间与 ID 必须同时提供。
func (store *Store) List(ctx context.Context, query domain.ListQuery) ([]domain.Event, error) {
	if err := store.validateQuery(ctx); err != nil {
		return nil, err
	}
	if query.Limit <= 0 || query.Limit > domain.MaxListLimit {
		return nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit list limit is invalid"))
	}
	if query.Before.IsZero() != (query.BeforeID == "") {
		return nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit list cursor is incomplete"))
	}
	var workspace any
	if query.WorkspaceID != nil {
		parsed, err := foundation.ParseID(string(*query.WorkspaceID))
		if err != nil || parsed != *query.WorkspaceID {
			return nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit workspace id is invalid"))
		}
		workspace = string(*query.WorkspaceID)
	}
	var beforeID any
	if query.BeforeID != "" {
		parsed, err := foundation.ParseID(string(query.BeforeID))
		if err != nil || parsed != query.BeforeID {
			return nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit list cursor id is invalid"))
		}
		beforeID = string(query.BeforeID)
	}
	rows, err := store.db.Query(ctx, auditSelect+`
		WHERE workspace_id IS NOT DISTINCT FROM $1::uuid
		  AND ($2::timestamptz IS NULL OR (occurred_at,id) < ($2::timestamptz,$3::uuid))
		ORDER BY occurred_at DESC, id DESC
		LIMIT $4`, workspace, nullableTime(query.Before), beforeID, query.Limit)
	if err != nil {
		return nil, classifyStoreError(err)
	}
	defer rows.Close()
	result := make([]domain.Event, 0, query.Limit)
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyStoreError(err)
	}
	return result, nil
}

func (store *Store) validateQuery(ctx context.Context) error {
	if store == nil || isNilDB(store.db) {
		return domainErrorUnavailable(errors.New("audit database is unavailable"))
	}
	if ctx == nil {
		return domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit query context is nil"))
	}
	return nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Truncate(time.Microsecond)
}

func isNilDB(value DB) bool {
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

func isNilStarter(value transactionStarter) bool {
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

var _ application.Store = (*Store)(nil)
var _ application.Appender = (*Store)(nil)
