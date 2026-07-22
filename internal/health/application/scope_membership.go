package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

// SmartCollectionBinding 是 Health scan 持久化并在每页复核的 Collection membership 快照绑定。
type SmartCollectionBinding struct {
	WorkspaceID       foundation.ID
	CollectionID      foundation.ID
	CollectionVersion int64
	QueryHash         string
	ReadModelRevision string
	ExactCount        int64
}

// SmartCollectionMembership 是一个已按绑定验证、稳定排序且去重的 Topic/Claim 成员集合。
type SmartCollectionMembership struct {
	Binding SmartCollectionBinding
	Members []domain.ObjectRef
}

// SmartCollectionMembershipPort 由 Collection owner 规划并读取同一个 durable membership。
type SmartCollectionMembershipPort interface {
	Plan(context.Context, foundation.ID, foundation.ID) (SmartCollectionBinding, error)
	Load(context.Context, SmartCollectionBinding, int) (SmartCollectionMembership, error)
}

// SmartCollectionMembershipSnapshotPort 是 detector 分页可选的 binding-aware fast path。
// 实现必须以 durable Collection binding 为事实源；缓存只能作为可丢弃的重复读取优化。
type SmartCollectionMembershipSnapshotPort interface {
	LoadForScope(context.Context, foundation.ID, domain.ScanScope) (SmartCollectionMembership, error)
}

// SmartCollectionBindingVerifier 在 detector page 完成后复核持久 binding，不重新加载成员集合。
type SmartCollectionBindingVerifier interface {
	VerifyBinding(context.Context, SmartCollectionBinding) error
}

func validateSmartCollectionBinding(binding SmartCollectionBinding) error {
	scope := domain.ScanScope{
		Type:              domain.ScanScopeTypeSmartCollection,
		Ref:               binding.CollectionID,
		Version:           binding.CollectionVersion,
		SchemaVersion:     "health-scope/smart-collection/v1",
		Hash:              binding.QueryHash,
		ReadModelRevision: binding.ReadModelRevision,
		ExactCount:        binding.ExactCount,
	}
	if !validScanID(binding.WorkspaceID) || binding.ExactCount < 0 || binding.ExactCount > domain.MaxScanItems {
		return scanConsistency(errors.New("smart-collection membership binding is invalid"))
	}
	if err := domain.ValidateScanScope(scope); err != nil {
		return scanConsistency(err)
	}
	return nil
}

func smartCollectionScopeMatches(workspaceID foundation.ID, scope domain.ScanScope, binding SmartCollectionBinding) bool {
	return binding.WorkspaceID == workspaceID && binding.CollectionID == scope.Ref &&
		binding.CollectionVersion == scope.Version && binding.QueryHash == scope.Hash &&
		binding.ReadModelRevision == scope.ReadModelRevision && binding.ExactCount == scope.ExactCount
}

func scanScopeStale(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanScopeStale, false, cause)
}
