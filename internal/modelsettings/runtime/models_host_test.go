package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

func TestManagedModelsHostBuildsAndActivatesExactRevision(t *testing.T) {
	t.Parallel()

	var initialCloseCalls atomic.Int32
	initial := &Models{revision: 3, closeRuntime: func() error {
		initialCloseCalls.Add(1)
		return nil
	}}
	revisions := &revisionServiceStub{resolved: domain.ResolvedSettings{
		Revision: 4,
		Settings: domain.CanonicalDisabledSettings(),
	}}
	host, err := NewManagedModelsHost(ManagedModelsHostOptions{
		Base:       config.Defaults(),
		Revisions:  revisions,
		Role:       domain.RuntimeRoleWorker,
		InstanceID: foundation.ID("10000000-0000-4000-8000-000000000401"),
		Loaded: LoadedSettings{
			Models: initial, Revision: 3, InitialPhase: domain.RuntimePhaseActive,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	if err := host.prepare(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	if revisions.loaded != 4 {
		t.Fatalf("loaded revision = %d, want 4", revisions.loaded)
	}
	if err := host.arm(4); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(4); err != nil {
		t.Fatal(err)
	}
	if got := initialCloseCalls.Load(); got != 1 {
		t.Fatalf("retired initial close calls = %d, want 1", got)
	}
	if err := host.reopen(4); err != nil {
		t.Fatal(err)
	}
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Binding().Revision != 4 || lease.Value() == nil || lease.Value().Revision() != 4 {
		t.Fatalf("activated lease = %s / %#v", lease.Binding(), lease.Value())
	}
}

func TestManagedModelsHostRejectsRevisionSubstitution(t *testing.T) {
	t.Parallel()

	initial := &Models{revision: 3}
	host, err := NewManagedModelsHost(ManagedModelsHostOptions{
		Base:       config.Defaults(),
		Revisions:  &revisionServiceStub{resolved: domain.ResolvedSettings{Revision: 5, Settings: domain.CanonicalDisabledSettings()}},
		Role:       domain.RuntimeRoleAPI,
		InstanceID: foundation.ID("10000000-0000-4000-8000-000000000402"),
		Loaded: LoadedSettings{
			Models: initial, Revision: 3, InitialPhase: domain.RuntimePhaseActive,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)

	err = host.prepare(context.Background(), 4)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeCorrupt || classified.Retryable {
		t.Fatalf("prepare error = %v", err)
	}
	lease, acquireErr := host.Acquire(context.Background(), CurrentRuntime())
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	defer lease.Release()
	if lease.Binding().Revision != 3 {
		t.Fatalf("active revision = %d, want 3", lease.Binding().Revision)
	}
}

func TestManagedModelsHostRepairsUnavailableGenerationAtSameRevision(t *testing.T) {
	t.Parallel()

	var fallbackCloseCalls atomic.Int32
	fallback := &Models{revision: 3, closeRuntime: func() error {
		fallbackCloseCalls.Add(1)
		return nil
	}}
	revisions := &revisionServiceStub{resolved: domain.ResolvedSettings{
		Revision: 3,
		Settings: domain.CanonicalDisabledSettings(),
	}}
	host, err := NewManagedModelsHost(ManagedModelsHostOptions{
		Base:       config.Defaults(),
		Revisions:  revisions,
		Role:       domain.RuntimeRoleAPI,
		InstanceID: foundation.ID("10000000-0000-4000-8000-000000000404"),
		Loaded: LoadedSettings{
			Models: fallback, Revision: 3, InitialPhase: domain.RuntimePhaseUnavailable,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)

	if _, err := host.Acquire(context.Background(), CurrentRuntime()); err == nil {
		t.Fatal("unavailable fallback was exposed as the current runtime")
	} else {
		assertRuntimeHostCode(t, err, ErrorCodeRuntimeNotReady)
	}
	if err := host.prepare(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if revisions.loaded != 3 {
		t.Fatalf("repair loaded revision = %d, want 3", revisions.loaded)
	}
	if err := host.arm(3); err != nil {
		t.Fatal(err)
	}
	if err := host.activate(3); err != nil {
		t.Fatal(err)
	}
	if got := fallbackCloseCalls.Load(); got != 1 {
		t.Fatalf("unavailable fallback close calls = %d, want 1", got)
	}
	if err := host.reopen(3); err != nil {
		t.Fatal(err)
	}
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Value() == fallback || lease.Binding().Revision != 3 {
		t.Fatalf("repaired lease = %p / %s", lease.Value(), lease.Binding())
	}
}

func TestManagedModelsHostKeepsCallerOwnershipOnConstructionError(t *testing.T) {
	t.Parallel()

	var closeCalls atomic.Int32
	initial := &Models{revision: 3, closeRuntime: func() error {
		closeCalls.Add(1)
		return nil
	}}
	_, err := NewManagedModelsHost(ManagedModelsHostOptions{
		Base:       config.Defaults(),
		Revisions:  &revisionServiceStub{},
		Role:       "invalid",
		InstanceID: foundation.ID("10000000-0000-4000-8000-000000000403"),
		Loaded: LoadedSettings{
			Models: initial, Revision: 3, InitialPhase: domain.RuntimePhaseActive,
		},
	})
	if err == nil {
		t.Fatal("expected construction error")
	}
	if got := closeCalls.Load(); got != 0 {
		t.Fatalf("construction error transferred ownership: close calls = %d", got)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
}
