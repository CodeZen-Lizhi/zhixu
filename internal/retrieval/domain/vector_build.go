package domain

import (
	"slices"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeEmbeddingInputOversized 表示原子 Chunk 超过当前 Embedder 的单输入字节上限。
	ErrorCodeEmbeddingInputOversized = "EMBEDDING_INPUT_OVERSIZED"
	// ErrorCodeEmbeddingInputContractMismatch 表示非原子 Chunk 违反已冻结的分块与 Embedding 输入契约。
	ErrorCodeEmbeddingInputContractMismatch = "EMBEDDING_INPUT_CONTRACT_MISMATCH"
)

// VectorBuildPageCommand 请求读取一个稳定有界的 pending vector 页面。
type VectorBuildPageCommand struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	ExpectedIndexVersion int64
	Limit                int32
	MaxInputBytes        int32
	MaxBatchInputBytes   int64
}

// VectorBuildInput 是冻结 Manifest 与 Canonical Chunk 共同证明的向量输入。
type VectorBuildInput struct {
	ChunkID         foundation.ID
	ContentHash     string
	Content         string
	Sequence        int32
	TokenCount      int32
	InputBytes      int64
	AtomicOversized bool
	CachedEmbedding []float32
}

// VectorBuildPage 返回当前 Building Index、Embedding Version 与稳定顺序输入。
type VectorBuildPage struct {
	Index            IndexVersion
	EmbeddingVersion EmbeddingVersion
	Inputs           []VectorBuildInput
	HasMore          bool
}

// VectorBuildWrite 将一个 pending Projection 绑定到冻结 Content Hash 与终态结果。
type VectorBuildWrite struct {
	ChunkID      foundation.ID
	ContentHash  string
	Embedding    []float32
	TokenCount   int32
	VectorStatus VectorStatus
	FailureCode  string
}

// VectorBuildBatch 是缓存写入与 Projection 终态更新的单事务命令。
type VectorBuildBatch struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	EmbeddingVersionID   foundation.ID
	ExpectedIndexVersion int64
	Writes               []VectorBuildWrite
	At                   time.Time
}

// VectorBuildCommitResult 汇总原子提交的 Projection 创建与精确重放数量。
type VectorBuildCommitResult struct {
	IndexVersionID foundation.ID
	AttemptedCount int64
	InsertedCount  int64
	ReplayedCount  int64
}

// ValidateVectorBuildPageCommand 校验 pending page 的 Index 乐观锁与上限。
func ValidateVectorBuildPageCommand(command VectorBuildPageCommand) error {
	if command.WorkspaceID == "" || command.IndexVersionID == "" || command.ExpectedIndexVersion <= 0 ||
		command.Limit <= 0 || command.Limit > MaxEmbeddingBatchSize ||
		command.MaxInputBytes <= 0 || command.MaxInputBytes > MaxEmbeddingInputBytes ||
		command.MaxBatchInputBytes < int64(command.MaxInputBytes) ||
		command.MaxBatchInputBytes > MaxEmbeddingBatchInputBytes {
		return invalid(ErrorCodeProjectionInvalid, "vector build page command is invalid")
	}
	return nil
}

// ValidateVectorBuildPage 校验 Store 返回的 Index/Embedding/input 完整绑定与稳定顺序。
func ValidateVectorBuildPage(command VectorBuildPageCommand, page VectorBuildPage) error {
	if err := ValidateVectorBuildPageCommand(command); err != nil {
		return err
	}
	if err := ValidateIndexVersion(page.Index); err != nil {
		return err
	}
	if err := ValidateEmbeddingVersion(page.EmbeddingVersion); err != nil {
		return err
	}
	if page.Index.WorkspaceID != command.WorkspaceID || page.Index.ID != command.IndexVersionID ||
		page.Index.Version != command.ExpectedIndexVersion || page.Index.Status != IndexStatusBuilding ||
		page.Index.EmbeddingVersionID == nil || *page.Index.EmbeddingVersionID != page.EmbeddingVersion.ID {
		return inconsistent(ErrorCodeProjectionInvalid, "vector build page index binding is invalid")
	}
	if len(page.Inputs) > int(command.Limit) || page.HasMore && len(page.Inputs) == 0 {
		return inconsistent(ErrorCodeProjectionInvalid, "vector build page size is invalid")
	}
	seen := make(map[foundation.ID]struct{}, len(page.Inputs))
	var loadedInputBytes int64
	var previousSequence int32 = -1
	var previousChunkID foundation.ID
	for index, input := range page.Inputs {
		if input.ChunkID == "" || !isCanonicalHash(input.ContentHash) || input.Sequence < 0 || input.TokenCount < 0 || input.InputBytes <= 0 {
			return inconsistent(ErrorCodeProjectionInvalid, "vector build input is invalid")
		}
		hasCache := len(input.CachedEmbedding) > 0
		oversized := input.InputBytes > int64(command.MaxInputBytes)
		if hasCache && (input.Content != "" || oversized) || oversized && input.Content != "" ||
			!hasCache && !oversized && (input.Content == "" || int64(len(input.Content)) != input.InputBytes) {
			return inconsistent(ErrorCodeProjectionInvalid, "vector build input payload boundary is invalid")
		}
		loadedInputBytes += int64(len(input.Content))
		if loadedInputBytes > command.MaxBatchInputBytes {
			return inconsistent(ErrorCodeProjectionInvalid, "vector build page exceeds the batch input byte limit")
		}
		if _, duplicate := seen[input.ChunkID]; duplicate {
			return inconsistent(ErrorCodeProjectionInvalid, "vector build page contains duplicate chunks")
		}
		seen[input.ChunkID] = struct{}{}
		if index > 0 && (input.Sequence < previousSequence || input.Sequence == previousSequence && input.ChunkID <= previousChunkID) {
			return inconsistent(ErrorCodeProjectionInvalid, "vector build page order is unstable")
		}
		previousSequence, previousChunkID = input.Sequence, input.ChunkID
		if len(input.CachedEmbedding) > 0 {
			if err := ValidateEmbeddingVector(page.EmbeddingVersion, input.CachedEmbedding); err != nil {
				return inconsistent(ErrorCodeEmbeddingContractInvalid, "cached embedding violates version contract")
			}
		}
	}
	return nil
}

// ValidateVectorBuildBatch 校验缓存与 Projection 原子提交的完整绑定和重复 Content Hash 一致性。
func ValidateVectorBuildBatch(index IndexVersion, embeddingVersion EmbeddingVersion, batch VectorBuildBatch) error {
	if len(batch.Writes) == 0 {
		return invalid(ErrorCodeProjectionInvalid, "vector build batch is empty")
	}
	projections := make([]VectorProjectionWrite, len(batch.Writes))
	seenContent := make(map[string][]float32, len(batch.Writes))
	for position, write := range batch.Writes {
		if !isCanonicalHash(write.ContentHash) {
			return invalid(ErrorCodeProjectionInvalid, "vector build content hash is invalid")
		}
		projections[position] = VectorProjectionWrite{
			ChunkID: write.ChunkID, Embedding: append([]float32(nil), write.Embedding...), TokenCount: write.TokenCount,
			VectorStatus: write.VectorStatus, FailureCode: write.FailureCode,
		}
		if write.VectorStatus != VectorStatusReady {
			continue
		}
		if existing, ok := seenContent[write.ContentHash]; ok && !slices.Equal(existing, write.Embedding) {
			return inconsistent(ErrorCodeEmbeddingContractInvalid, "same content hash has different embeddings")
		}
		seenContent[write.ContentHash] = append([]float32(nil), write.Embedding...)
	}
	return ValidateVectorProjectionBatch(index, embeddingVersion, VectorProjectionBatch{
		WorkspaceID: batch.WorkspaceID, IndexVersionID: batch.IndexVersionID,
		EmbeddingVersionID: batch.EmbeddingVersionID, ExpectedIndexVersion: batch.ExpectedIndexVersion,
		Projections: projections, At: batch.At,
	})
}
