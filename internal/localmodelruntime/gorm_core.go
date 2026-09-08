package localmodelruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMStore persists managed lifecycle facts through the shared platform pool.
type GORMStore struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMStore builds the adapter from the shared physical pool. It never
// opens a DSN or creates a second connection pool.
func NewGORMStore(pool *platformpostgres.Pool) (*GORMStore, error) {
	if pool == nil {
		return nil, errors.New("local model runtime GORM pool is unavailable")
	}
	database, err := pool.GORM()
	if err != nil || !validGORMStoreDatabase(database) {
		return nil, errors.New("local model runtime GORM database is unavailable")
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil || nilGORMUnitOfWork(unitOfWork) {
		return nil, errors.New("local model runtime GORM unit of work is unavailable")
	}
	return &GORMStore{database: database, unitOfWork: unitOfWork}, nil
}

var _ LifecycleStore = (*GORMStore)(nil)
var _ TestPreparationStore = (*GORMStore)(nil)
var _ ScopedTxLifecycle = (*GORMStore)(nil)

func (store *GORMStore) ready(ctx context.Context) error {
	if store == nil || !validGORMStoreDatabase(store.database) || nilGORMUnitOfWork(store.unitOfWork) {
		return errors.New("local model runtime GORM store is unavailable")
	}
	if ctx == nil {
		return errors.New("local model runtime context is nil")
	}
	return nil
}

func validGORMStoreDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMUnitOfWork(unitOfWork foundation.UnitOfWork) bool {
	if unitOfWork == nil {
		return true
	}
	value := reflect.ValueOf(unitOfWork)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// gormRawRow guards GORM's Row helper. Row() itself has no error return, so a
// statement error must be checked before callers attempt to Scan.
func gormRawRow(ctx context.Context, database *gorm.DB, query string, args ...any) (*sql.Row, error) {
	if ctx == nil || !validGORMStoreDatabase(database) {
		return nil, errors.New("local model runtime GORM row query is unavailable")
	}
	result := database.WithContext(ctx).Raw(query, args...)
	if result.Error != nil {
		return nil, result.Error
	}
	row := result.Row()
	if row == nil {
		return nil, errors.New("local model runtime GORM row is nil")
	}
	return row, nil
}

func gormRawRows(ctx context.Context, database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if ctx == nil || !validGORMStoreDatabase(database) {
		return nil, errors.New("local model runtime GORM rows query is unavailable")
	}
	result := database.WithContext(ctx).Raw(query, args...)
	if result.Error != nil {
		return nil, result.Error
	}
	rows, err := result.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("local model runtime GORM rows are nil")
	}
	return rows, nil
}

func gormNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func gormContextError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		} else if !errors.Is(cause, ctx.Err()) {
			cause = errors.Join(ctx.Err(), cause)
		}
		if !errors.Is(err, cause) {
			return errors.Join(err, cause)
		}
		return err
	}
	if errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return err
}

func gormStoreError(ctx context.Context, err error) error {
	return gormContextError(ctx, err)
}

func (store *GORMStore) within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return errors.New("local model runtime GORM transaction callback is nil")
	}
	return gormStoreError(ctx, store.unitOfWork.Within(ctx, options, work))
}

type gormTxStore struct {
	store *GORMStore
	scope foundation.TransactionScope
}

// WithScope binds the four activation methods to a caller-owned live scope.
// Scope validity is checked again on every operation so a bound value cannot
// outlive the UnitOfWork callback silently.
func (store *GORMStore) WithScope(scope foundation.TransactionScope) (TxStore, error) {
	if store == nil || !validGORMStoreDatabase(store.database) || nilGORMUnitOfWork(store.unitOfWork) {
		return nil, errors.New("local model runtime GORM store is unavailable")
	}
	if scope == nil {
		return nil, errors.New("local model runtime scoped transaction is nil")
	}
	if _, err := platformpostgres.GORMTransaction(scope); err != nil {
		return nil, fmt.Errorf("local model runtime scoped transaction is unavailable: %w", err)
	}
	return &gormTxStore{store: store, scope: scope}, nil
}

func (store *gormTxStore) databaseFor(ctx context.Context) (*gorm.DB, error) {
	if store == nil || store.store == nil {
		return nil, errors.New("local model runtime scoped store is unavailable")
	}
	if err := store.store.ready(ctx); err != nil {
		return nil, err
	}
	database, err := platformpostgres.GORMTransaction(store.scope)
	if err != nil {
		return nil, fmt.Errorf("local model runtime scoped transaction is unavailable: %w", err)
	}
	return database, nil
}
