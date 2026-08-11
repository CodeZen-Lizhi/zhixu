package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// apiEmbeddingRuntimeAcquirer 将 Retrieval 的持久 Embedding Version 绑定到
// RuntimeHost generation；它不向 Retrieval 暴露 activation 生命周期。
type apiEmbeddingRuntimeAcquirer struct {
	runtime modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models]
}

var _ retrievalapplication.CompatibleEmbeddingAcquirer = (*apiEmbeddingRuntimeAcquirer)(nil)

func newAPIEmbeddingRuntimeAcquirer(
	runtime modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models],
) (retrievalapplication.CompatibleEmbeddingAcquirer, error) {
	if runtime == nil {
		return nil, errors.New("api embedding runtime is unavailable")
	}
	return &apiEmbeddingRuntimeAcquirer{runtime: runtime}, nil
}

func (acquirer *apiEmbeddingRuntimeAcquirer) Acquire(
	ctx context.Context,
	version retrievaldomain.EmbeddingVersion,
) (retrievalapplication.CompatibleEmbeddingLease, error) {
	if acquirer == nil || acquirer.runtime == nil || ctx == nil {
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			modelsettingsruntime.ErrorCodeRuntimeNotReady,
			true,
			errors.New("api embedding runtime is unavailable"),
		)
	}
	runtimeLease, err := acquirer.runtime.Acquire(ctx, modelsettingsruntime.EmbeddingRuntime(version))
	if err != nil {
		if runtimeLease != nil {
			runtimeLease.Release()
		}
		return nil, err
	}
	if runtimeLease == nil {
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			modelsettingsruntime.ErrorCodeRuntimeNotReady,
			true,
			errors.New("api embedding generation is unavailable"),
		)
	}
	models := runtimeLease.Value()
	if models == nil {
		runtimeLease.Release()
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			modelsettingsruntime.ErrorCodeRuntimeNotReady,
			true,
			errors.New("api embedding generation is unavailable"),
		)
	}
	if err := models.ValidateEmbeddingVersion(version); err != nil {
		runtimeLease.Release()
		return nil, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			modelsettingsruntime.ErrorCodeRuntimeEmbeddingContractMismatch,
			false,
			errors.New("api embedding generation does not match the persisted version"),
		)
	}
	embedder := models.Embedding().Embedder()
	if embedder == nil {
		runtimeLease.Release()
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			modelsettingsruntime.ErrorCodeRuntimeNotReady,
			true,
			errors.New("api embedding generation is unavailable"),
		)
	}
	return &apiEmbeddingRuntimeLease{runtime: runtimeLease, embedder: embedder}, nil
}

// apiEmbeddingRuntimeLease 保证 QueryEmbedder 只在底层 generation lease 生命周期内可见。
type apiEmbeddingRuntimeLease struct {
	runtime  modelsettingsruntime.RuntimeLease[*modelsettingsruntime.Models]
	embedder retrievalapplication.QueryEmbedder
	released atomic.Bool
}

var _ retrievalapplication.CompatibleEmbeddingLease = (*apiEmbeddingRuntimeLease)(nil)

func (lease *apiEmbeddingRuntimeLease) Embedder() retrievalapplication.QueryEmbedder {
	if lease == nil || lease.released.Load() {
		return nil
	}
	return lease.embedder
}

func (lease *apiEmbeddingRuntimeLease) Release() {
	if lease == nil || !lease.released.CompareAndSwap(false, true) {
		return
	}
	if lease.runtime != nil {
		lease.runtime.Release()
	}
}

func newAPISearchService(
	store retrievalapplication.SearchStore,
	embedder retrievalapplication.QueryEmbedder,
	reranker retrievalapplication.Reranker,
	acquirers ...retrievalapplication.CompatibleEmbeddingAcquirer,
) (*retrievalapplication.SearchService, error) {
	if len(acquirers) > 1 {
		return nil, errors.New("api search accepts at most one embedding runtime")
	}
	if len(acquirers) == 1 && acquirers[0] != nil {
		return retrievalapplication.NewSearchServiceWithEmbeddingAcquirer(store, acquirers[0], reranker)
	}
	return retrievalapplication.NewSearchService(store, embedder, reranker)
}
