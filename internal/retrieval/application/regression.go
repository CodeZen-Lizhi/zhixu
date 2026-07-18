package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const regressionServiceUnavailableCode = "RETRIEVAL_REGRESSION_SERVICE_UNAVAILABLE"

// RegressionStore 隐藏结构回归的一致快照读取、闭包验证与受控失败事务。
type RegressionStore interface {
	RunSnapshotRegression(context.Context, domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error)
}

// RegressionService 独立编排 Ready 前的 Snapshot 结构回归，不扩张通用 Retrieval Service。
type RegressionService struct {
	store RegressionStore
}

// NewRegressionService 创建结构回归服务；缺少 Store 时拒绝启动。
func NewRegressionService(store RegressionStore) (*RegressionService, error) {
	if store == nil {
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			regressionServiceUnavailableCode,
			false,
			errors.New("retrieval regression store is unavailable"),
		)
	}
	return &RegressionService{store: store}, nil
}

// RunSnapshotStructureV1 在 Building 状态执行 SNAPSHOT_STRUCTURE_V1 并返回 Delivery checkpoint 结果。
func (service *RegressionService) RunSnapshotStructureV1(ctx context.Context, command domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error) {
	if service == nil || service.store == nil {
		return domain.SnapshotRegressionResult{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			regressionServiceUnavailableCode,
			false,
			errors.New("retrieval regression service is unavailable"),
		)
	}
	command.RegressionCode = domain.SnapshotStructureRegressionV1
	return service.store.RunSnapshotRegression(ctx, command)
}

// RunSnapshotStructureV2 在 Building 状态执行绑定 Embedding Version 的 SNAPSHOT_STRUCTURE_V2。
func (service *RegressionService) RunSnapshotStructureV2(ctx context.Context, command domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error) {
	if service == nil || service.store == nil {
		return domain.SnapshotRegressionResult{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			regressionServiceUnavailableCode,
			false,
			errors.New("retrieval regression service is unavailable"),
		)
	}
	command.RegressionCode = domain.SnapshotStructureRegressionV2
	return service.store.RunSnapshotRegression(ctx, command)
}
