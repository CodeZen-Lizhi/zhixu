package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const generationHoldLease = 30 * time.Second

// GenerationLifecycleAdapter maps immutable process generations to durable
// local-model holds. It is deliberately role-scoped; API and Worker never
// share an owner identity even when they use the same model revision.
type GenerationLifecycleAdapter struct {
	store      localmodelruntime.LifecycleStore
	ids        foundation.IDGenerator
	ownerID    foundation.ID
	ownerEpoch int64
	role       modelsettingsdomain.RuntimeRole
}

var _ GenerationLifecycle = (*GenerationLifecycleAdapter)(nil)

// NewGenerationLifecycle creates the narrow hold adapter used by RuntimeHost.
func NewGenerationLifecycle(store localmodelruntime.LifecycleStore, role modelsettingsdomain.RuntimeRole, ownerID foundation.ID) (*GenerationLifecycleAdapter, error) {
	if store == nil || !modelsettingsdomain.ValidRuntimeRole(role) || !validRuntimeID(ownerID) {
		return nil, errors.New("managed generation lifecycle dependencies are invalid")
	}
	return &GenerationLifecycleAdapter{
		store: store, ids: foundation.NewUUIDGenerator(nil), ownerID: ownerID, ownerEpoch: 1, role: role,
	}, nil
}

func (adapter *GenerationLifecycleAdapter) Acquire(ctx context.Context, binding RuntimeBinding, requirement localmodelruntime.Requirement) (GenerationHold, error) {
	if adapter == nil || adapter.store == nil || ctx == nil || len(requirement.Models) == 0 {
		return nil, errors.New("managed generation hold dependencies are invalid")
	}
	if binding.Mode != RuntimeModeManaged || binding.Role != RuntimeRole(adapter.role) || binding.Revision <= 0 {
		return nil, errors.New("managed generation hold binding is invalid")
	}
	holdID, err := adapter.ids.New()
	if err != nil {
		return nil, errors.New("managed generation hold identity is unavailable")
	}
	instanceID := adapter.ownerID
	revision := binding.Revision
	hold, err := adapter.store.AcquireHold(ctx, localmodelruntime.HoldAcquireCommand{
		HoldID: holdID, Kind: localmodelruntime.HoldKindGeneration,
		OwnerID: adapter.ownerID, OwnerEpoch: adapter.ownerEpoch,
		Role: string(adapter.role), InstanceID: &instanceID, Revision: &revision,
		Requirement: requirement, LeaseDuration: generationHoldLease,
	})
	if err != nil {
		return nil, err
	}
	return &generationHoldLeaseAdapter{store: adapter.store, hold: hold, ownerID: adapter.ownerID, ownerEpoch: adapter.ownerEpoch}, nil
}

type generationHoldLeaseAdapter struct {
	mu         sync.Mutex
	store      localmodelruntime.LifecycleStore
	hold       localmodelruntime.HoldRecord
	ownerID    foundation.ID
	ownerEpoch int64
	released   bool
}

func (lease *generationHoldLeaseAdapter) Renew(ctx context.Context) error {
	if lease == nil || ctx == nil {
		return errors.New("managed generation hold renewal is invalid")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return nil
	}
	hold, err := lease.store.RenewHold(ctx, localmodelruntime.HoldRenewCommand{
		HoldID: lease.hold.HoldID, OwnerID: lease.ownerID, OwnerEpoch: lease.ownerEpoch,
		ExpectedVersion: lease.hold.Version, LeaseDuration: generationHoldLease,
	})
	if err != nil {
		return err
	}
	lease.hold = hold
	return nil
}

func (lease *generationHoldLeaseAdapter) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return nil
	}
	_, err := lease.store.ReleaseHold(ctx, localmodelruntime.HoldReleaseCommand{
		HoldID: lease.hold.HoldID, OwnerID: lease.ownerID, OwnerEpoch: lease.ownerEpoch,
		ExpectedVersion: lease.hold.Version,
	})
	if err == nil {
		lease.released = true
	}
	return err
}
