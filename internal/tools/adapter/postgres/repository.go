package postgres

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

type transactionStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 在 PostgreSQL 中实现 Tool Call Repository 与 Workflow Policy Reader。
type Repository struct {
	db transactionStarter
}

// NewRepository 构造 Tool PostgreSQL Adapter；数据库依赖缺失时 fail closed。
func NewRepository(database transactionStarter) (*Repository, error) {
	if isNilDatabase(database) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("tool database is missing"))
	}
	return &Repository{db: database}, nil
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
