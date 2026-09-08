package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormDB adapts the shared database/sql-backed GORM handle to Export's
// private transaction boundary. The adapter is confined to this PostgreSQL
// package; application and domain code never see GORM or database/sql.
type gormDB struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

func newGORMDB(database *gorm.DB, unitOfWork foundation.UnitOfWork) (*gormDB, error) {
	if !validExportGORMDatabase(database) || isNilDependency(unitOfWork) {
		return nil, errors.New("export GORM database is unavailable")
	}
	return &gormDB{database: database, unitOfWork: unitOfWork}, nil
}

func (database *gormDB) Begin(ctx context.Context) (exportTransaction, error) {
	if database == nil || !validExportGORMDatabase(database.database) || isNilDependency(database.unitOfWork) || ctx == nil {
		return nil, errors.New("export GORM transaction is unavailable")
	}
	bridge := &gormTx{ready: make(chan error, 1), done: make(chan error, 1), finished: make(chan struct{})}
	go func() {
		err := database.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			transaction, err := platformpostgres.GORMTransaction(scope)
			if err != nil {
				bridge.signalReady(err)
				return err
			}
			bridge.database = transaction.WithContext(callbackCtx)
			bridge.scope = scope
			bridge.signalReady(nil)
			return <-bridge.done
		})
		bridge.resultErr = err
		bridge.signalReady(err)
		close(bridge.finished)
	}()
	if err := <-bridge.ready; err != nil {
		return nil, err
	}
	return bridge, nil
}

func (database *gormDB) Query(ctx context.Context, query string, arguments ...any) (exportRows, error) {
	if database == nil || !validExportGORMDatabase(database.database) || ctx == nil {
		return nil, errors.New("export GORM query is unavailable")
	}
	statement := database.database.WithContext(ctx).Raw(query, normalizeExportGORMArgs(arguments)...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	return &gormRows{rows: rows}, nil
}

type gormTx struct {
	database  *gorm.DB
	scope     foundation.TransactionScope
	ready     chan error
	done      chan error
	finished  chan struct{}
	resultErr error
	once      sync.Once
	readyOnce sync.Once
}

func (transaction *gormTx) signalReady(err error) {
	if transaction == nil {
		return
	}
	transaction.readyOnce.Do(func() { transaction.ready <- err })
}

func (transaction *gormTx) Commit(context.Context) error {
	if transaction == nil || transaction.database == nil {
		return sql.ErrTxDone
	}
	transaction.once.Do(func() { transaction.done <- nil })
	<-transaction.finished
	return transaction.resultErr
}

func (transaction *gormTx) Rollback(context.Context) error {
	if transaction == nil || transaction.database == nil {
		return sql.ErrTxDone
	}
	transaction.once.Do(func() { transaction.done <- errors.New("export transaction rollback") })
	<-transaction.finished
	return nil
}

func (transaction *gormTx) Exec(ctx context.Context, query string, arguments ...any) error {
	if transaction == nil || !validExportGORMDatabase(transaction.database) || ctx == nil {
		return errors.New("export GORM transaction is unavailable")
	}
	result := transaction.database.WithContext(ctx).Exec(query, normalizeExportGORMArgs(arguments)...)
	return result.Error
}

func (transaction *gormTx) Query(ctx context.Context, query string, arguments ...any) (exportRows, error) {
	if transaction == nil || !validExportGORMDatabase(transaction.database) || ctx == nil {
		return nil, errors.New("export GORM transaction is unavailable")
	}
	statement := transaction.database.WithContext(ctx).Raw(query, normalizeExportGORMArgs(arguments)...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	return &gormRows{rows: rows}, nil
}

func (transaction *gormTx) QueryRow(ctx context.Context, query string, arguments ...any) exportRow {
	if transaction == nil || !validExportGORMDatabase(transaction.database) || ctx == nil {
		return gormRow{err: errors.New("export GORM row query is unavailable")}
	}
	statement := transaction.database.WithContext(ctx).Raw(query, normalizeExportGORMArgs(arguments)...)
	if statement.Error != nil {
		return gormRow{err: statement.Error}
	}
	return gormRow{row: statement.Row()}
}

func (transaction *gormTx) Scope() foundation.TransactionScope {
	if transaction == nil {
		return nil
	}
	return transaction.scope
}

type gormRow struct {
	row *sql.Row
	err error
}

func (row gormRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.row == nil {
		return errors.New("export GORM row is unavailable")
	}
	return row.row.Scan(destinations...)
}

type gormRows struct {
	rows *sql.Rows
	err  error
}

func (rows *gormRows) Close() {
	if rows != nil && rows.rows != nil {
		if err := rows.rows.Close(); err != nil && rows.err == nil {
			rows.err = err
		}
	}
}

func (rows *gormRows) Err() error {
	if rows == nil || rows.rows == nil {
		return errors.New("export GORM rows are unavailable")
	}
	if rows.err != nil {
		return rows.err
	}
	return rows.rows.Err()
}

func (rows *gormRows) Next() bool {
	if rows == nil || rows.rows == nil || rows.err != nil {
		return false
	}
	if rows.rows.Next() {
		return true
	}
	rows.Close()
	return false
}

func (rows *gormRows) Scan(destinations ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("export GORM rows are unavailable")
	}
	return rows.rows.Scan(destinations...)
}

func normalizeExportGORMArgs(arguments []any) []any {
	result := append([]any(nil), arguments...)
	for index, argument := range result {
		switch value := argument.(type) {
		case []string:
			result[index] = pq.Array(value)
		case []foundation.ID:
			values := make([]string, len(value))
			for i := range value {
				values[i] = string(value[i])
			}
			result[index] = pq.Array(values)
		}
	}
	return result
}
