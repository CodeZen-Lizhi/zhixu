package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormDB 在共享 UnitOfWork 回调内同步执行 Health 查询，不持有或模拟驱动事务。
type gormDB struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

func newHealthGORMDB(database *gorm.DB, unitOfWork foundation.UnitOfWork) (*gormDB, error) {
	if !validHealthGORMDatabase(database) || isNilHealthDependency(unitOfWork) {
		return nil, errors.New("health GORM database is unavailable")
	}
	return &gormDB{database: database, unitOfWork: unitOfWork}, nil
}

func (database *gormDB) Within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, healthTransaction) error) error {
	if database == nil || !validHealthGORMDatabase(database.database) || isNilHealthDependency(database.unitOfWork) || ctx == nil || work == nil {
		return errors.New("health GORM transaction is unavailable")
	}
	return database.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(callbackCtx, healthTransaction{healthSQL: &gormDB{database: transaction}, scope: scope})
	})
}

func (database *gormDB) Exec(ctx context.Context, query string, arguments ...any) (healthResult, error) {
	if database == nil || !validHealthGORMDatabase(database.database) || ctx == nil {
		return 0, errors.New("health GORM statement is unavailable")
	}
	result := database.database.WithContext(ctx).Exec(query, normalizeHealthGORMArgs(arguments)...)
	if result.Error != nil {
		return 0, healthContextError(ctx, result.Error)
	}
	return healthResult(result.RowsAffected), nil
}

func (database *gormDB) Query(ctx context.Context, query string, arguments ...any) (healthRows, error) {
	if database == nil || !validHealthGORMDatabase(database.database) || ctx == nil {
		return nil, errors.New("health GORM query is unavailable")
	}
	statement := database.database.WithContext(ctx).Raw(query, normalizeHealthGORMArgs(arguments)...)
	if statement.Error != nil {
		return nil, healthContextError(ctx, statement.Error)
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, healthContextError(ctx, err)
	}
	return &healthGORMRows{rows: rows, ctx: ctx}, nil
}

func (database *gormDB) QueryRow(ctx context.Context, query string, arguments ...any) healthRow {
	if database == nil || !validHealthGORMDatabase(database.database) || ctx == nil {
		return healthGORMRow{err: errors.New("health GORM row query is unavailable")}
	}
	statement := database.database.WithContext(ctx).Raw(query, normalizeHealthGORMArgs(arguments)...)
	if statement.Error != nil {
		return healthGORMRow{err: healthContextError(ctx, statement.Error)}
	}
	return healthGORMRow{row: statement.Row(), ctx: ctx}
}

type healthGORMRow struct {
	row *sql.Row
	ctx context.Context
	err error
}

func (row healthGORMRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.row == nil {
		return errors.New("health GORM row is unavailable")
	}
	return healthContextError(row.ctx, row.row.Scan(destinations...))
}

type healthGORMRows struct {
	rows *sql.Rows
	ctx  context.Context
	err  error
}

func (rows *healthGORMRows) Close() {
	if rows != nil && rows.rows != nil {
		if err := rows.rows.Close(); err != nil && rows.err == nil {
			rows.err = err
		}
	}
}

func (rows *healthGORMRows) Err() error {
	if rows == nil || rows.rows == nil {
		return errors.New("health GORM rows are unavailable")
	}
	if rows.err != nil {
		return healthContextError(rows.ctx, rows.err)
	}
	return healthContextError(rows.ctx, rows.rows.Err())
}

func (rows *healthGORMRows) Next() bool {
	if rows == nil || rows.rows == nil || rows.err != nil {
		return false
	}
	if rows.rows.Next() {
		return true
	}
	rows.Close()
	return false
}

func (rows *healthGORMRows) Scan(destinations ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("health GORM rows are unavailable")
	}
	return healthContextError(rows.ctx, rows.rows.Scan(destinations...))
}

func normalizeHealthGORMArgs(arguments []any) []any {
	result := append([]any(nil), arguments...)
	for index, argument := range result {
		switch value := argument.(type) {
		case []string:
			result[index] = pq.Array(value)
		case []foundation.ID:
			values := make([]string, len(value))
			for item := range value {
				values[item] = string(value[item])
			}
			result[index] = pq.Array(values)
		}
	}
	return result
}

func validHealthGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func isNilHealthDependency(value any) bool {
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

var _ healthStore = (*gormDB)(nil)
