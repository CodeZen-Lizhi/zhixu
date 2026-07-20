// Package postgres implements Graph read projections over canonical Knowledge facts.
package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
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

// FindPath 由 T05 实现；当前明确失败而不是返回无路径假成功。
func (*Repository) FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error) {
	return graphdomain.PathResult{}, unavailable(errors.New("graph path query is not implemented"))
}

// RelationEvidenceWindow 由 T06 实现；当前明确失败而不是返回空 Evidence 假成功。
func (*Repository) RelationEvidenceWindow(context.Context, foundation.ID, foundation.ID) (graphapp.RelationEvidenceResultWindow, error) {
	return graphapp.RelationEvidenceResultWindow{}, unavailable(errors.New("graph relation evidence query is not implemented"))
}

var _ graphapp.QueryPort = (*Repository)(nil)
