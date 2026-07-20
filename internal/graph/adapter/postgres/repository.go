// Package postgres implements Graph read projections over canonical Knowledge facts.
package postgres

import (
	"context"
	"errors"

	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	"github.com/jackc/pgx/v5"
)

// DB 是 Graph read adapter 所需的最小参数化查询边界。
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 直接从 Knowledge canonical facts 构建只读 Graph projection。
type Repository struct{ db DB }

// NewRepository 构造不拥有任何 Graph 写事实的 PostgreSQL adapter。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, unavailable(errors.New("graph database is nil"))
	}
	return &Repository{db: db}, nil
}

var _ graphapp.QueryPort = (*Repository)(nil)
