package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	vectorBuilderUnavailableCode         = "RETRIEVAL_VECTOR_BUILDER_UNAVAILABLE"
	vectorEmbedderVersionUnavailableCode = "RETRIEVAL_VECTOR_EMBEDDER_VERSION_UNAVAILABLE"
)

// VectorBuildStore 隐藏 pending/cache 读取与 cache+Projection 原子提交。
type VectorBuildStore interface {
	LoadVectorBuildPage(context.Context, domain.VectorBuildPageCommand) (domain.VectorBuildPage, error)
	CommitVectorBuildBatch(context.Context, domain.VectorBuildBatch) (domain.VectorBuildCommitResult, error)
}

// VectorBuilderDependencies 是有界向量构建器的显式依赖。
type VectorBuilderDependencies struct {
	Store    VectorBuildStore
	Embedder Embedder
	Clock    foundation.Clock
}

// VectorBuilder 每次处理一页 pending Projection，不在数据库事务内调用 Provider。
type VectorBuilder struct {
	store    VectorBuildStore
	embedder Embedder
	clock    foundation.Clock
	contract domain.EmbeddingContract
}

// BuildNextVectorBatchRequest 绑定一个 Building Hybrid Index 的期望版本。
type BuildNextVectorBatchRequest struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	ExpectedIndexVersion int64
}

// BuildNextVectorBatchResult 返回一页构建计数和是否已无 pending Projection。
type BuildNextVectorBatchResult struct {
	IndexVersionID foundation.ID
	ProcessedCount int64
	ProviderInputs int64
	CacheHitCount  int64
	ReadyCount     int64
	SkippedCount   int64
	FailedCount    int64
	InsertedCount  int64
	ReplayedCount  int64
	Done           bool
}

// NewVectorBuilder 创建严格绑定一个运行时 Embedder Contract 的向量构建器。
func NewVectorBuilder(dependencies VectorBuilderDependencies) (*VectorBuilder, error) {
	if dependencies.Store == nil || dependencies.Embedder == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, vectorBuilderUnavailableCode, false, errors.New("vector builder dependencies are incomplete"))
	}
	contract := dependencies.Embedder.Contract()
	if err := domain.ValidateEmbeddingContract(contract); err != nil {
		return nil, err
	}
	return &VectorBuilder{store: dependencies.Store, embedder: dependencies.Embedder, clock: dependencies.Clock, contract: contract}, nil
}

// NewVectorRecoveryBuilder 创建只用于完成历史 Hybrid Index 终态检查的构建器。
// 无 pending Projection 时它允许继续 Regression；仍有 pending 时明确要求恢复匹配的 Embedder 配置。
func NewVectorRecoveryBuilder(store VectorBuildStore, clock foundation.Clock) (*VectorBuilder, error) {
	if store == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, vectorBuilderUnavailableCode, false, errors.New("vector recovery builder dependencies are incomplete"))
	}
	return &VectorBuilder{store: store, clock: clock}, nil
}

// BuildNextVectorBatch 读取一个有界页，批量调用一次 Provider，并原子保存缓存与 Projection 终态。
func (builder *VectorBuilder) BuildNextVectorBatch(ctx context.Context, request BuildNextVectorBatchRequest) (BuildNextVectorBatchResult, error) {
	if builder == nil || builder.store == nil || builder.clock == nil {
		return BuildNextVectorBatchResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, vectorBuilderUnavailableCode, false, errors.New("vector builder is unavailable"))
	}
	limit := int32(1)
	maxInputBytes := domain.MaxEmbeddingInputBytes
	maxBatchInputBytes := domain.MaxEmbeddingBatchInputBytes
	if builder.embedder != nil {
		limit = builder.contract.MaxBatchSize
		maxInputBytes = builder.contract.MaxInputBytes
		maxBatchInputBytes = builder.contract.MaxBatchInputBytes
	}
	command := domain.VectorBuildPageCommand{
		WorkspaceID: request.WorkspaceID, IndexVersionID: request.IndexVersionID,
		ExpectedIndexVersion: request.ExpectedIndexVersion, Limit: limit,
		MaxInputBytes: maxInputBytes, MaxBatchInputBytes: maxBatchInputBytes,
	}
	if err := domain.ValidateVectorBuildPageCommand(command); err != nil {
		return BuildNextVectorBatchResult{}, err
	}
	page, err := builder.store.LoadVectorBuildPage(ctx, command)
	if err != nil {
		return BuildNextVectorBatchResult{}, err
	}
	if err := domain.ValidateVectorBuildPage(command, page); err != nil {
		return BuildNextVectorBatchResult{}, err
	}
	if len(page.Inputs) == 0 {
		return BuildNextVectorBatchResult{IndexVersionID: page.Index.ID, Done: true}, nil
	}
	if builder.embedder == nil {
		return BuildNextVectorBatchResult{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			vectorEmbedderVersionUnavailableCode,
			true,
			errors.New("persisted hybrid index still requires its embedding provider configuration"),
		)
	}
	if err := domain.ValidateEmbeddingContractBinding(builder.contract, page.EmbeddingVersion); err != nil {
		return BuildNextVectorBatchResult{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			vectorEmbedderVersionUnavailableCode,
			true,
			errors.New("configured embedder does not match the persisted embedding version"),
		)
	}

	contentByHash := make(map[string]string, len(page.Inputs))
	cachedByHash := make(map[string][]float32, len(page.Inputs))
	missHashes := make([]string, 0, len(page.Inputs))
	missInputs := make([]string, 0, len(page.Inputs))
	seenMiss := make(map[string]struct{}, len(page.Inputs))
	for _, input := range page.Inputs {
		if existing, ok := contentByHash[input.ContentHash]; ok && input.Content != "" && existing != input.Content {
			return BuildNextVectorBatchResult{}, vectorBuildConsistency("same content hash is bound to different text")
		}
		if input.Content != "" {
			contentByHash[input.ContentHash] = input.Content
		}
		if len(input.CachedEmbedding) > 0 {
			if input.InputBytes > int64(builder.contract.MaxInputBytes) {
				return BuildNextVectorBatchResult{}, vectorBuildConsistency("oversized input unexpectedly has a cache entry")
			}
			if existing, ok := cachedByHash[input.ContentHash]; ok && !sameEmbeddingValues(existing, input.CachedEmbedding) {
				return BuildNextVectorBatchResult{}, vectorBuildConsistency("same cache identity returned different vectors")
			}
			cachedByHash[input.ContentHash] = append([]float32(nil), input.CachedEmbedding...)
			continue
		}
		if input.InputBytes > int64(builder.contract.MaxInputBytes) {
			continue
		}
		if _, duplicate := seenMiss[input.ContentHash]; duplicate {
			continue
		}
		seenMiss[input.ContentHash] = struct{}{}
		missHashes = append(missHashes, input.ContentHash)
		missInputs = append(missInputs, input.Content)
	}

	generatedByHash := make(map[string][]float32, len(missHashes))
	if len(missInputs) > 0 {
		embedRequest := EmbedRequest{Inputs: missInputs}
		if err := ValidateEmbedRequest(builder.contract, embedRequest); err != nil {
			return BuildNextVectorBatchResult{}, err
		}
		embedResult, err := builder.embedder.Embed(ctx, embedRequest)
		if err != nil {
			return BuildNextVectorBatchResult{}, err
		}
		normalized, err := ValidateEmbedResult(builder.contract, embedRequest, embedResult)
		if err != nil {
			return BuildNextVectorBatchResult{}, err
		}
		for index, hash := range missHashes {
			generatedByHash[hash] = normalized[index]
		}
	}

	writes := make([]domain.VectorBuildWrite, len(page.Inputs))
	result := BuildNextVectorBatchResult{IndexVersionID: page.Index.ID, ProcessedCount: int64(len(page.Inputs)), ProviderInputs: int64(len(missInputs))}
	for index, input := range page.Inputs {
		write := domain.VectorBuildWrite{ChunkID: input.ChunkID, ContentHash: input.ContentHash, TokenCount: input.TokenCount}
		switch {
		case input.InputBytes > int64(builder.contract.MaxInputBytes) && input.AtomicOversized:
			write.VectorStatus = domain.VectorStatusSkippedOversized
			write.FailureCode = domain.ErrorCodeEmbeddingInputOversized
			result.SkippedCount++
		case input.InputBytes > int64(builder.contract.MaxInputBytes):
			write.VectorStatus = domain.VectorStatusFailed
			write.FailureCode = domain.ErrorCodeEmbeddingInputContractMismatch
			result.FailedCount++
		case len(input.CachedEmbedding) > 0:
			write.VectorStatus = domain.VectorStatusReady
			write.Embedding = append([]float32(nil), cachedByHash[input.ContentHash]...)
			result.CacheHitCount++
			result.ReadyCount++
		default:
			write.VectorStatus = domain.VectorStatusReady
			write.Embedding = append([]float32(nil), generatedByHash[input.ContentHash]...)
			result.ReadyCount++
		}
		writes[index] = write
	}
	batch := domain.VectorBuildBatch{
		WorkspaceID: page.Index.WorkspaceID, IndexVersionID: page.Index.ID, EmbeddingVersionID: page.EmbeddingVersion.ID,
		ExpectedIndexVersion: page.Index.Version, Writes: writes, At: builder.clock.Now(),
	}
	if err := domain.ValidateVectorBuildBatch(page.Index, page.EmbeddingVersion, batch); err != nil {
		return BuildNextVectorBatchResult{}, err
	}
	committed, err := builder.store.CommitVectorBuildBatch(ctx, batch)
	if err != nil {
		return BuildNextVectorBatchResult{}, err
	}
	if committed.IndexVersionID != page.Index.ID || committed.AttemptedCount != int64(len(writes)) ||
		committed.InsertedCount < 0 || committed.ReplayedCount < 0 ||
		committed.InsertedCount+committed.ReplayedCount != committed.AttemptedCount {
		return BuildNextVectorBatchResult{}, vectorBuildConsistency("vector build commit result is invalid")
	}
	result.InsertedCount = committed.InsertedCount
	result.ReplayedCount = committed.ReplayedCount
	result.Done = !page.HasMore
	return result, nil
}

func vectorBuildConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeProjectionInvalid, false, errors.New(message))
}

func sameEmbeddingValues(left, right []float32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
