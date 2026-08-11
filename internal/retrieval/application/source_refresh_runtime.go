package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const sourceRefreshRuntimeUnavailableCode = "SOURCE_REFRESH_RUNTIME_UNAVAILABLE"

// SourceRefreshOperationRunner freezes the complete source-processing graph
// used by one Refresh or RefreshBatch operation. Implementations are immutable
// after construction and may be shared by independent concurrent leases.
type SourceRefreshOperationRunner interface {
	Refresh(context.Context, SourceRefreshRequest) (SourceRefreshResult, error)
	RefreshBatch(context.Context, BatchSourceRefreshRequest) (BatchSourceRefreshResult, error)
}

// SourceRefreshOperationLease exposes one immutable generation-bound runner.
// Runner must return the same value for the lease lifetime, and Release must be
// idempotent. The runner must not be used after Release.
type SourceRefreshOperationLease interface {
	Runner() SourceRefreshOperationRunner
	Release()
}

// SourceRefreshOperationAcquirer selects the generation for one complete
// source-refresh operation. Revision and current-generation selection remain a
// composition concern and are intentionally absent from this interface. Every
// successful call must return an independently releasable lease, even when
// several leases expose the same runner.
type SourceRefreshOperationAcquirer interface {
	Acquire(context.Context) (SourceRefreshOperationLease, error)
}

// DynamicSourceRefresher is a stable facade for Capture and Git Sync. Every
// operation acquires exactly one generation and holds it through ingestion,
// vector building, snapshot readiness, and activation.
type DynamicSourceRefresher struct {
	acquirer SourceRefreshOperationAcquirer
}

var (
	_ SourceRefreshOperationRunner = (*SourceRefresher)(nil)
	_ SourceRefreshOperationRunner = (*DynamicSourceRefresher)(nil)
)

// NewDynamicSourceRefresher creates a composition-neutral dynamic facade.
func NewDynamicSourceRefresher(acquirer SourceRefreshOperationAcquirer) (*DynamicSourceRefresher, error) {
	if nilDispatcherDependency(acquirer) {
		return nil, sourceRefreshRuntimeUnavailable(errors.New("source refresh runtime acquirer is unavailable"))
	}
	return &DynamicSourceRefresher{acquirer: acquirer}, nil
}

// Refresh holds one generation lease until the complete single-source refresh
// has returned.
func (refresher *DynamicSourceRefresher) Refresh(ctx context.Context, request SourceRefreshRequest) (SourceRefreshResult, error) {
	lease, runner, err := refresher.acquire(ctx)
	if err != nil {
		return SourceRefreshResult{}, err
	}
	defer lease.Release()
	return runner.Refresh(ctx, request)
}

// RefreshBatch holds one generation lease until the complete batch refresh has
// returned.
func (refresher *DynamicSourceRefresher) RefreshBatch(ctx context.Context, request BatchSourceRefreshRequest) (BatchSourceRefreshResult, error) {
	lease, runner, err := refresher.acquire(ctx)
	if err != nil {
		return BatchSourceRefreshResult{}, err
	}
	defer lease.Release()
	return runner.RefreshBatch(ctx, request)
}

func (refresher *DynamicSourceRefresher) acquire(ctx context.Context) (SourceRefreshOperationLease, SourceRefreshOperationRunner, error) {
	if refresher == nil || nilDispatcherDependency(refresher.acquirer) {
		return nil, nil, sourceRefreshRuntimeUnavailable(errors.New("source refresh runtime facade is unavailable"))
	}
	lease, err := refresher.acquirer.Acquire(ctx)
	if err != nil {
		if !nilDispatcherDependency(lease) {
			lease.Release()
		}
		return nil, nil, err
	}
	if nilDispatcherDependency(lease) {
		return nil, nil, sourceRefreshRuntimeUnavailable(errors.New("source refresh runtime acquirer returned a nil lease"))
	}
	runner := lease.Runner()
	if nilDispatcherDependency(runner) {
		lease.Release()
		return nil, nil, sourceRefreshRuntimeUnavailable(errors.New("source refresh runtime lease returned a nil runner"))
	}
	return lease, runner, nil
}

func sourceRefreshRuntimeUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, sourceRefreshRuntimeUnavailableCode, true, cause)
}
