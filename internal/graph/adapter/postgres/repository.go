// Package postgres implements Graph read projections over canonical Knowledge facts.
package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const defaultStatementTimeout = 1500 * time.Millisecond

const setLocalStatementTimeoutSQL = `SELECT pg_catalog.set_config('statement_timeout',$1,true)`

// DB 是 Graph read adapter 所需的最小参数化查询边界。
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 直接从 Knowledge canonical facts 构建只读 Graph projection。
type Repository struct {
	db               DB
	beginner         transactionBeginner
	statementTimeout time.Duration
	inReadSnapshot   bool
}

// NewRepository 构造不拥有任何 Graph 写事实的 PostgreSQL adapter。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, unavailable(errors.New("graph database is nil"))
	}
	beginner, ok := db.(transactionBeginner)
	if !ok {
		return nil, unavailable(errors.New("graph database does not support owned transactions"))
	}
	return &Repository{db: db, beginner: beginner, statementTimeout: defaultStatementTimeout}, nil
}

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func inReadSnapshot[T any](ctx context.Context, repository *Repository, query func(*Repository) (T, error)) (T, error) {
	var zero T
	if repository == nil || repository.db == nil {
		return zero, unavailable(errors.New("graph repository is unavailable"))
	}
	if repository.inReadSnapshot {
		return query(repository)
	}
	if repository.beginner == nil {
		return zero, unavailable(errors.New("graph read-only transaction is unavailable"))
	}
	tx, err := repository.beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return zero, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := configureStatementTimeout(ctx, tx, repository.statementTimeout); err != nil {
		return zero, classify(err)
	}
	result, err := query(repository.forReadSnapshot(tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, classify(err)
	}
	return result, nil
}

func (repository *Repository) forReadSnapshot(db DB) *Repository {
	return &Repository{db: db, statementTimeout: repository.statementTimeout, inReadSnapshot: true}
}

func configureStatementTimeout(ctx context.Context, db DB, timeout time.Duration) error {
	if timeout < time.Millisecond {
		return errors.New("graph statement timeout must be at least one millisecond")
	}
	milliseconds := strconv.FormatInt(timeout.Milliseconds(), 10) + "ms"
	_, err := db.Exec(ctx, setLocalStatementTimeoutSQL, milliseconds)
	return err
}

var _ graphapp.QueryPort = (*Repository)(nil)
