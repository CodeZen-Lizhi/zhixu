package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// DurableScanRevisionVerifier 只校验 Collection durable binding 的定义与 read-model revision。
// 它不重新计算成员精确数量，供跨页读取复用已冻结的 exact_count。
type DurableScanRevisionVerifier interface {
	VerifyDurableScanRevision(context.Context, DurableScanBinding) error
}

// ScopedDurableScanBindingVerifier 在调用方已拥有的事务范围内复核 durable
// scan binding。TransactionScope 是 opaque capability；实现不得提交、回滚
// 或退回到自己的根连接。
type ScopedDurableScanBindingVerifier interface {
	VerifyDurableScanBindingScoped(context.Context, foundation.TransactionScope, DurableScanBinding) error
}

// VerifyDurableScanRevision 在 Collection Service 边界暴露轻量 binding 复核。
// 生产 Repository 提供 O(1) 相对成员加载的 revision 校验；旧实现回退到完整 Plan，保持 fail-closed。
func (s *Service) VerifyDurableScanRevision(ctx context.Context, binding DurableScanBinding) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if !validDurableScanBinding(binding) {
		return requestInvalid("collection durable scan binding is invalid")
	}
	reader, ok := s.dependencies.Repository.(DurableScanRevisionVerifier)
	if ok {
		return reader.VerifyDurableScanRevision(ctx, binding)
	}
	planned, err := s.PlanDurableScan(ctx, binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		return err
	}
	if planned != binding {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeCursorStale, false, errors.New("collection durable scan binding changed"))
	}
	return nil
}
