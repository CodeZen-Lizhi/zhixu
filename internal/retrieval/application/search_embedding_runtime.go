package application

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// CompatibleEmbeddingAcquirer 按持久化 Embedding Version 选择一个兼容的
// generation。它不暴露 rollout 或 settings 生命周期，Search 只拥有一次操作级租约。
type CompatibleEmbeddingAcquirer interface {
	Acquire(context.Context, domain.EmbeddingVersion) (CompatibleEmbeddingLease, error)
}

// CompatibleEmbeddingLease 固定一次 Search 所使用的 QueryEmbedder。Release
// 必须幂等，并且只能在 Search 的全部向量、融合和重排工作完成后调用。
type CompatibleEmbeddingLease interface {
	Embedder() QueryEmbedder
	Release()
}

// staticEmbeddingAcquirer 保留旧的 NewSearchService 构造方式。正式 managed
// composition 会注入基于 RuntimeHost 的 CompatibleEmbeddingAcquirer。
type staticEmbeddingAcquirer struct {
	embedder QueryEmbedder
}

func (acquirer staticEmbeddingAcquirer) Acquire(context.Context, domain.EmbeddingVersion) (CompatibleEmbeddingLease, error) {
	if nilDispatcherDependency(acquirer.embedder) {
		return nil, vectorEmbedderVersionUnavailable(errors.New("embedding runtime is unavailable"))
	}
	return &staticEmbeddingLease{embedder: acquirer.embedder}, nil
}

type staticEmbeddingLease struct {
	embedder QueryEmbedder
	released atomic.Bool
}

func (lease *staticEmbeddingLease) Embedder() QueryEmbedder {
	if lease == nil || lease.released.Load() {
		return nil
	}
	return lease.embedder
}

func (lease *staticEmbeddingLease) Release() {
	if lease != nil {
		lease.released.Store(true)
	}
}

func vectorEmbedderVersionUnavailable(cause error) error {
	if cause == nil {
		cause = errors.New("compatible embedding runtime is unavailable")
	}
	retryable := false
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		if classified.Code == vectorEmbedderVersionUnavailableCode {
			return cause
		}
		retryable = classified.Retryable
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, vectorEmbedderVersionUnavailableCode, retryable, cause)
}

func (service *SearchService) acquireEmbedding(
	ctx context.Context,
	version domain.EmbeddingVersion,
) (CompatibleEmbeddingLease, QueryEmbedder, error) {
	if service == nil || nilDispatcherDependency(service.embeddingAcquirer) {
		return nil, nil, vectorEmbedderVersionUnavailable(errors.New("compatible embedding acquirer is unavailable"))
	}
	target := version
	if version.ModelSettingsRevision != nil {
		revision := *version.ModelSettingsRevision
		target.ModelSettingsRevision = &revision
	}
	lease, err := service.embeddingAcquirer.Acquire(ctx, target)
	if err != nil {
		if !nilDispatcherDependency(lease) {
			lease.Release()
		}
		return nil, nil, vectorEmbedderVersionUnavailable(err)
	}
	if nilDispatcherDependency(lease) {
		return nil, nil, vectorEmbedderVersionUnavailable(errors.New("compatible embedding acquirer returned a nil lease"))
	}
	embedder := lease.Embedder()
	if nilDispatcherDependency(embedder) {
		lease.Release()
		return nil, nil, vectorEmbedderVersionUnavailable(errors.New("compatible embedding lease returned a nil embedder"))
	}
	return lease, embedder, nil
}
