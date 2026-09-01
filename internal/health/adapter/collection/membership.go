// Package collection 把 Collection owner 的 durable keyset seam 适配为 Health membership。
package collection

import (
	"context"
	"errors"
	"sync"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const (
	collectionMembershipPageSize  = 100
	collectionMembershipCacheSize = 8
)

// DurableScanReader 是 Health 使用的最小 Collection application 契约。
type DurableScanReader interface {
	PlanDurableScan(context.Context, foundation.ID, foundation.ID) (collectionapp.DurableScanBinding, error)
	ReadDurableScanPage(context.Context, collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error)
}

// Membership 复用 Collection owner 的 version/query/revision keyset，不自行编译查询。
type Membership struct {
	reader  DurableScanReader
	cacheMu sync.RWMutex
	cache   map[healthapp.SmartCollectionBinding]healthapp.SmartCollectionMembership
}

var _ healthapp.SmartCollectionMembershipPort = (*Membership)(nil)
var _ healthapp.SmartCollectionMembershipSnapshotPort = (*Membership)(nil)
var _ healthapp.SmartCollectionBindingVerifier = (*Membership)(nil)

// NewMembership 构造 Smart Collection Health membership adapter。
func NewMembership(reader DurableScanReader) (*Membership, error) {
	if reader == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, errors.New("collection durable scan reader is unavailable"))
	}
	return &Membership{reader: reader, cache: make(map[healthapp.SmartCollectionBinding]healthapp.SmartCollectionMembership)}, nil
}

// LoadForScope 读取或复用同一 durable binding 的成员快照。
// 首次读取仍经 Collection owner 的 Plan+Load；命中缓存时只做持久 revision 校验，缓存不是事实源。
func (membership *Membership) LoadForScope(ctx context.Context, workspaceID foundation.ID, scope domain.ScanScope) (healthapp.SmartCollectionMembership, error) {
	if membership == nil || membership.reader == nil {
		return healthapp.SmartCollectionMembership{}, unavailable(errors.New("collection membership is unavailable"))
	}
	if ctx == nil {
		return healthapp.SmartCollectionMembership{}, inconsistent(errors.New("collection membership context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return healthapp.SmartCollectionMembership{}, err
	}
	if workspaceID == "" {
		return healthapp.SmartCollectionMembership{}, inconsistent(errors.New("collection membership workspace is invalid"))
	}
	if err := domain.ValidateScanScope(scope); err != nil {
		return healthapp.SmartCollectionMembership{}, err
	}
	expected := healthapp.SmartCollectionBinding{
		WorkspaceID: workspaceID, CollectionID: scope.Ref, CollectionVersion: scope.Version,
		QueryHash: scope.Hash, ReadModelRevision: scope.ReadModelRevision, ExactCount: scope.ExactCount,
	}
	if cached, ok := membership.cached(expected); ok {
		if err := membership.VerifyBinding(ctx, expected); err != nil {
			return healthapp.SmartCollectionMembership{}, err
		}
		return cloneMembership(cached), nil
	}
	planned, err := membership.Plan(ctx, workspaceID, scope.Ref)
	if err != nil {
		return healthapp.SmartCollectionMembership{}, err
	}
	if planned != expected {
		return healthapp.SmartCollectionMembership{}, stale(errors.New("collection membership binding changed before loading"))
	}
	snapshot, err := membership.Load(ctx, planned, int(domain.MaxScanItems))
	if err != nil {
		return healthapp.SmartCollectionMembership{}, err
	}
	membership.putCached(snapshot)
	return cloneMembership(snapshot), nil
}

// VerifyBinding 只做 binding/revision 复核，不重新读取成员集合。
func (membership *Membership) VerifyBinding(ctx context.Context, binding healthapp.SmartCollectionBinding) error {
	if membership == nil || membership.reader == nil {
		return unavailable(errors.New("collection membership is unavailable"))
	}
	if ctx == nil {
		return inconsistent(errors.New("collection membership context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if verifier, ok := membership.reader.(collectionapp.DurableScanRevisionVerifier); ok {
		if err := verifier.VerifyDurableScanRevision(ctx, toCollectionBinding(binding)); err != nil {
			membership.deleteCached(binding)
			return classify(err)
		}
		return nil
	}
	planned, err := membership.Plan(ctx, binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		return err
	}
	if planned != binding {
		membership.deleteCached(binding)
		return stale(errors.New("collection membership binding changed"))
	}
	return nil
}

func (membership *Membership) cached(binding healthapp.SmartCollectionBinding) (healthapp.SmartCollectionMembership, bool) {
	membership.cacheMu.RLock()
	defer membership.cacheMu.RUnlock()
	value, ok := membership.cache[binding]
	return cloneMembership(value), ok
}

func (membership *Membership) putCached(snapshot healthapp.SmartCollectionMembership) {
	membership.cacheMu.Lock()
	defer membership.cacheMu.Unlock()
	if len(membership.cache) >= collectionMembershipCacheSize {
		for binding := range membership.cache {
			delete(membership.cache, binding)
			break
		}
	}
	membership.cache[snapshot.Binding] = cloneMembership(snapshot)
}

func (membership *Membership) deleteCached(binding healthapp.SmartCollectionBinding) {
	membership.cacheMu.Lock()
	defer membership.cacheMu.Unlock()
	delete(membership.cache, binding)
}

func cloneMembership(snapshot healthapp.SmartCollectionMembership) healthapp.SmartCollectionMembership {
	snapshot.Members = append([]domain.ObjectRef(nil), snapshot.Members...)
	return snapshot
}

// Plan 返回 Collection owner 计算的当前稳定 binding。
func (membership *Membership) Plan(ctx context.Context, workspaceID, collectionID foundation.ID) (healthapp.SmartCollectionBinding, error) {
	if membership == nil || membership.reader == nil {
		return healthapp.SmartCollectionBinding{}, unavailable(errors.New("collection membership is unavailable"))
	}
	binding, err := membership.reader.PlanDurableScan(ctx, workspaceID, collectionID)
	if err != nil {
		return healthapp.SmartCollectionBinding{}, classify(err)
	}
	return toHealthBinding(binding), nil
}

// Load 读取完整但有硬上限的稳定成员集合；每页和最终 Plan 都必须保持同一 binding。
func (membership *Membership) Load(ctx context.Context, binding healthapp.SmartCollectionBinding, limit int) (healthapp.SmartCollectionMembership, error) {
	if membership == nil || membership.reader == nil {
		return healthapp.SmartCollectionMembership{}, unavailable(errors.New("collection membership is unavailable"))
	}
	if limit < 1 || int64(limit) > domain.MaxScanItems || binding.ExactCount < 0 || binding.ExactCount > int64(limit) {
		return healthapp.SmartCollectionMembership{}, unavailable(errors.New("collection membership exceeds the bounded health scan capacity"))
	}
	collectionBinding := toCollectionBinding(binding)
	members := make([]domain.ObjectRef, 0, int(binding.ExactCount))
	var after *collectionapp.DurableScanKey
	for {
		pageLimit := collectionMembershipPageSize
		remaining := int(binding.ExactCount) - len(members)
		if remaining > 0 && remaining < pageLimit {
			pageLimit = remaining
		}
		if pageLimit < 1 {
			pageLimit = 1
		}
		page, err := membership.reader.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{
			Binding: collectionBinding, After: cloneKey(after), Limit: pageLimit,
		})
		if err != nil {
			return healthapp.SmartCollectionMembership{}, classify(err)
		}
		for _, item := range page.Items {
			objectType := domain.ObjectType(item.ObjectType)
			if objectType != domain.ObjectTypeTopic && objectType != domain.ObjectTypeClaim {
				return healthapp.SmartCollectionMembership{}, inconsistent(errors.New("collection membership contains an unsupported object type"))
			}
			members = append(members, domain.ObjectRef{Type: objectType, ID: item.ID})
		}
		if page.Complete {
			break
		}
		if page.Next == nil || len(members) > limit {
			return healthapp.SmartCollectionMembership{}, inconsistent(errors.New("collection membership page did not advance within bounds"))
		}
		after = cloneKey(page.Next)
	}
	if int64(len(members)) != binding.ExactCount {
		return healthapp.SmartCollectionMembership{}, stale(errors.New("collection membership count changed while loading"))
	}
	latest, err := membership.reader.PlanDurableScan(ctx, binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		return healthapp.SmartCollectionMembership{}, classify(err)
	}
	if latest != collectionBinding {
		return healthapp.SmartCollectionMembership{}, stale(errors.New("collection membership binding changed after loading"))
	}
	return healthapp.SmartCollectionMembership{Binding: binding, Members: members}, nil
}

func toHealthBinding(binding collectionapp.DurableScanBinding) healthapp.SmartCollectionBinding {
	return healthapp.SmartCollectionBinding{
		WorkspaceID: binding.WorkspaceID, CollectionID: binding.CollectionID,
		CollectionVersion: binding.CollectionVersion, QueryHash: binding.QueryHash,
		ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount,
	}
}

func toCollectionBinding(binding healthapp.SmartCollectionBinding) collectionapp.DurableScanBinding {
	return collectionapp.DurableScanBinding{
		WorkspaceID: binding.WorkspaceID, CollectionID: binding.CollectionID,
		CollectionVersion: binding.CollectionVersion, QueryHash: binding.QueryHash,
		ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount,
	}
}

func cloneKey(value *collectionapp.DurableScanKey) *collectionapp.DurableScanKey {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func classify(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Kind == foundation.ErrorVersionConflict {
		return stale(err)
	}
	return err
}

func stale(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanScopeStale, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, cause)
}

func inconsistent(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, cause)
}
