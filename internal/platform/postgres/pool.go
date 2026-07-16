// Package postgres contains the PostgreSQL connection boundary used by API
// and Worker processes.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pinger is the minimal dependency required by readiness and worker health
// checks. It keeps handlers testable without a live database.
type Pinger interface {
	Ping(context.Context) error
}

// Pool owns a pgx connection pool and exposes health plus composition-root
// query boundaries; domain repositories still hide pgx from application code.
type Pool struct {
	pool *pgxpool.Pool
}

// DB exposes the pgx pool to composition-root adapters. Domain packages must
// depend on their own repository interfaces rather than this concrete type.
func (p *Pool) DB() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.pool
}

// Open parses the configured URL and creates a pool. It does not claim the DB
// is ready; callers must call Ping with a bounded context.
func Open(ctx context.Context, databaseURL string, maxConns, minConns int32) (*Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("database URL is empty")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	if minConns > 0 {
		config.MinConns = minConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	return &Pool{pool: pool}, nil
}

// Ping verifies that PostgreSQL is reachable and accepting requests.
func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return fmt.Errorf("database pool is not initialized")
	}
	return p.pool.Ping(ctx)
}

// QueryRow executes a parameterized query returning one row.
func (p *Pool) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	if p == nil || p.pool == nil {
		return errorRow{err: fmt.Errorf("database pool is not initialized")}
	}
	return p.pool.QueryRow(ctx, sql, arguments...)
}

// Query executes a parameterized query returning multiple rows.
func (p *Pool) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("database pool is not initialized")
	}
	return p.pool.Query(ctx, sql, arguments...)
}

// Begin starts a database transaction for an infrastructure adapter.
func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("database pool is not initialized")
	}
	return p.pool.Begin(ctx)
}

// Close releases all pool resources.
func (p *Pool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }
