// Package postgres contains the PostgreSQL connection boundary used by API
// and Worker processes.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pinger is the minimal dependency required by readiness and worker health
// checks. It keeps handlers testable without a live database.
type Pinger interface {
	Ping(context.Context) error
}

// Pool owns a pgx connection pool and exposes only the health boundary to
// callers until domain repositories are introduced in later milestones.
type Pool struct {
	pool *pgxpool.Pool
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

// Close releases all pool resources.
func (p *Pool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}
