package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

type gormUnitOfWork struct {
	database *gorm.DB
}

type transactionScope struct {
	database *gorm.DB
	sqlTx    *sql.Tx
	active   atomic.Bool
}

func (*transactionScope) TransactionScope() {}

// UnitOfWork returns the shared GORM transaction boundary. Migration pools do
// not initialize GORM and therefore reject this operation.
func (p *Pool) UnitOfWork() (foundation.UnitOfWork, error) {
	if p == nil || p.gormDB == nil || p.closed.Load() {
		return nil, errors.New("PostgreSQL GORM unit of work is not initialized")
	}
	return &gormUnitOfWork{database: p.gormDB}, nil
}

// Within runs work in a GORM transaction and owns its commit or rollback.
func (u *gormUnitOfWork) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if u == nil || u.database == nil {
		return errors.New("PostgreSQL GORM unit of work is not initialized")
	}
	if ctx == nil {
		return errors.New("PostgreSQL transaction context is nil")
	}
	if work == nil {
		return errors.New("PostgreSQL transaction callback is nil")
	}
	txOptions, err := sqlTransactionOptions(options)
	if err != nil {
		return err
	}

	err = u.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if transaction == nil || transaction.Statement == nil {
			return errors.New("PostgreSQL GORM transaction is not initialized")
		}
		sqlTx, ok := transaction.Statement.ConnPool.(*sql.Tx)
		if !ok || sqlTx == nil {
			return fmt.Errorf("PostgreSQL GORM transaction backend is %T, want *sql.Tx", transaction.Statement.ConnPool)
		}
		scope := &transactionScope{database: transaction, sqlTx: sqlTx}
		scope.active.Store(true)
		defer scope.active.Store(false)
		return work(ctx, scope)
	}, txOptions)
	if err != nil {
		return fmt.Errorf("execute PostgreSQL transaction: %w", preserveTransactionCause(ctx, err))
	}
	return nil
}

// preserveTransactionCause keeps a caller's cancel cause attached when the
// database layer returns only a context error or sql.ErrTxDone after rollback.
// The original driver error remains the primary value for errors.Is/As checks.
func preserveTransactionCause(ctx context.Context, err error) error {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return err
	}
	contextErr := ctx.Err()
	// database/sql may finish its automatic rollback before Commit observes the
	// canceled transaction context, in which case Commit returns ErrTxDone.
	if errors.Is(err, sql.ErrTxDone) && !errors.Is(err, contextErr) {
		err = errors.Join(err, contextErr)
	}
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(err, cause) {
		return err
	}
	if !errors.Is(err, contextErr) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(err, cause)
}

// GORMTransaction unwraps a live scope for a PostgreSQL infrastructure adapter.
func GORMTransaction(scope foundation.TransactionScope) (*gorm.DB, error) {
	transaction, err := activeTransactionScope(scope)
	if err != nil {
		return nil, err
	}
	return transaction.database, nil
}

// SQLTransaction unwraps a live scope for an infrastructure adapter that uses
// the shared database/sql transaction, including River's database/sql driver.
func SQLTransaction(scope foundation.TransactionScope) (*sql.Tx, error) {
	transaction, err := activeTransactionScope(scope)
	if err != nil {
		return nil, err
	}
	return transaction.sqlTx, nil
}

func activeTransactionScope(scope foundation.TransactionScope) (*transactionScope, error) {
	transaction, ok := scope.(*transactionScope)
	if !ok || transaction == nil || transaction.database == nil || transaction.sqlTx == nil {
		return nil, errors.New("PostgreSQL transaction scope is invalid")
	}
	if !transaction.active.Load() {
		return nil, errors.New("PostgreSQL transaction scope is no longer active")
	}
	return transaction, nil
}

func sqlTransactionOptions(options foundation.TransactionOptions) (*sql.TxOptions, error) {
	isolation := sql.LevelDefault
	switch options.Isolation {
	case foundation.TransactionIsolationDefault:
	case foundation.TransactionIsolationReadCommitted:
		isolation = sql.LevelReadCommitted
	case foundation.TransactionIsolationRepeatableRead:
		isolation = sql.LevelRepeatableRead
	case foundation.TransactionIsolationSerializable:
		isolation = sql.LevelSerializable
	default:
		return nil, fmt.Errorf("PostgreSQL transaction isolation %d is invalid", options.Isolation)
	}
	return &sql.TxOptions{Isolation: isolation, ReadOnly: options.ReadOnly}, nil
}
