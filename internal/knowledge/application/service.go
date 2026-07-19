// Package application 编排 Knowledge 模块的短事务命令与有界查询。
package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// MaxBatchSize 是 Knowledge 批量查询允许的最大对象数。
	MaxBatchSize = domain.MaxBatchLimit
	// MaxIdempotencyKeyBytes 是 Workspace 作用域幂等键的最大字节数。
	MaxIdempotencyKeyBytes = 128
)

const (
	errorCodeServiceUnavailable = "KNOWLEDGE_SERVICE_UNAVAILABLE"
	errorCodeRequestInvalid     = "KNOWLEDGE_REQUEST_INVALID"
	errorCodeResultConsistency  = "KNOWLEDGE_RESULT_CONSISTENCY"
)

// ProvenanceVerifier 验证 Source Version 到 Source Span 的完整不可变绑定。
type ProvenanceVerifier interface {
	// Verify 必须拒绝不存在、损坏或跨 Workspace 的 Provenance 引用。
	Verify(context.Context, domain.ProvenanceRef) error
}

// ConfirmationVerifier 验证关系确认引用来自允许的审批或来源事实。
type ConfirmationVerifier interface {
	// Verify 必须拒绝无法证明、过期或错绑的确认引用。
	Verify(context.Context, domain.Confirmation) error
}

// Dependencies 是 Knowledge Application Service 的显式端口集合。
type Dependencies struct {
	Repository   domain.Repository
	Provenance   ProvenanceVerifier
	Confirmation ConfirmationVerifier
	IDs          foundation.IDGenerator
	Clock        foundation.Clock
}

// Service 编排领域规则、外部验证端口和 Repository 原子命令。
type Service struct{ dependencies Dependencies }

// NewService 创建 fail-closed 的 Knowledge Application Service。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.Provenance == nil || dependencies.Confirmation == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge dependencies are incomplete"))
	}
	return &Service{dependencies: dependencies}, nil
}
