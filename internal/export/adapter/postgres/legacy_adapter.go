package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 legacy Export Repository 所需的最小 pgx 边界。
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// NewRepository 创建 legacy Export PostgreSQL Repository。
func NewRepository(database DB, options ...Option) (*Repository, error) {
	if isNilDependency(database) {
		return nil, unavailable(errors.New("export database is nil"))
	}
	return newRepository(&legacyDatabase{database: database}, options...)
}

type legacyDatabase struct {
	database DB
}

func (database *legacyDatabase) Begin(ctx context.Context) (exportTransaction, error) {
	if database == nil || isNilDependency(database.database) {
		return nil, errors.New("export legacy database is unavailable")
	}
	transaction, err := database.database.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if isNilDependency(transaction) {
		return nil, errors.New("export legacy transaction is unavailable")
	}
	return &legacyTransaction{transaction: transaction}, nil
}

func (database *legacyDatabase) Query(ctx context.Context, query string, arguments ...any) (exportRows, error) {
	if database == nil || isNilDependency(database.database) {
		return nil, errors.New("export legacy database is unavailable")
	}
	return database.database.Query(ctx, query, arguments...)
}

type legacyTransaction struct {
	transaction pgx.Tx
}

func (transaction *legacyTransaction) Exec(ctx context.Context, query string, arguments ...any) error {
	if transaction == nil || isNilDependency(transaction.transaction) {
		return errors.New("export legacy transaction is unavailable")
	}
	_, err := transaction.transaction.Exec(ctx, query, arguments...)
	return err
}

func (transaction *legacyTransaction) Query(ctx context.Context, query string, arguments ...any) (exportRows, error) {
	if transaction == nil || isNilDependency(transaction.transaction) {
		return nil, errors.New("export legacy transaction is unavailable")
	}
	return transaction.transaction.Query(ctx, query, arguments...)
}

func (transaction *legacyTransaction) QueryRow(ctx context.Context, query string, arguments ...any) exportRow {
	if transaction == nil || isNilDependency(transaction.transaction) {
		return legacyRow{err: errors.New("export legacy transaction is unavailable")}
	}
	return transaction.transaction.QueryRow(ctx, query, arguments...)
}

func (transaction *legacyTransaction) Commit(ctx context.Context) error {
	if transaction == nil || isNilDependency(transaction.transaction) {
		return pgx.ErrTxClosed
	}
	return transaction.transaction.Commit(ctx)
}

func (transaction *legacyTransaction) Rollback(ctx context.Context) error {
	if transaction == nil || isNilDependency(transaction.transaction) {
		return pgx.ErrTxClosed
	}
	return transaction.transaction.Rollback(ctx)
}

func (transaction *legacyTransaction) SideFactTransaction() any {
	if transaction == nil {
		return nil
	}
	return transaction.transaction
}

type legacyRow struct {
	err error
}

func (row legacyRow) Scan(...any) error {
	return row.err
}
