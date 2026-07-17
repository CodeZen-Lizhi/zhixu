package domain

import (
	"math"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// LexicalStatus 是单个 Chunk 全文投影的构建状态。
type LexicalStatus string

const (
	// LexicalStatusPending 表示全文投影尚未生成。
	LexicalStatusPending LexicalStatus = "pending"
	// LexicalStatusReady 表示 search_vector 已完整生成。
	LexicalStatusReady LexicalStatus = "ready"
	// LexicalStatusFailed 表示全文投影以稳定错误码失败。
	LexicalStatusFailed LexicalStatus = "failed"
)

// VectorStatus 是单个 Chunk 向量投影的构建状态。
type VectorStatus string

const (
	// VectorStatusDisabled 表示 FTS-only Index 未配置 Embedding Version。
	VectorStatusDisabled VectorStatus = "disabled"
	// VectorStatusPending 表示向量仍待生成。
	VectorStatusPending VectorStatus = "pending"
	// VectorStatusReady 表示向量已通过形状和数值校验。
	VectorStatusReady VectorStatus = "ready"
	// VectorStatusSkippedOversized 表示原子 Chunk 超出模型输入上限并显式降级。
	VectorStatusSkippedOversized VectorStatus = "skipped_oversized"
	// VectorStatusFailed 表示向量生成以稳定错误码失败。
	VectorStatusFailed VectorStatus = "failed"
)

// ChunkProjection 保存对 Canonical Chunk 的可重建 FTS/vector 投影，不复制正文。
type ChunkProjection struct {
	IndexVersionID     foundation.ID
	ChunkID            foundation.ID
	WorkspaceID        foundation.ID
	EmbeddingVersionID *foundation.ID
	SearchVector       string
	Embedding          []float32
	TokenCount         int32
	LexicalStatus      LexicalStatus
	VectorStatus       VectorStatus
	FailureCode        string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// LexicalBuildCommand 请求 Repository 从冻结 Manifest 批量生成全文投影。
type LexicalBuildCommand struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	ExpectedIndexVersion int64
	At                   time.Time
}

// ProjectionBatchResult 汇总批量投影命令的写入及精确重放数量。
type ProjectionBatchResult struct {
	IndexVersionID foundation.ID
	AttemptedCount int64
	InsertedCount  int64
	ReplayedCount  int64
}

// VectorProjectionWrite 请求更新既有 lexical-ready Projection 的向量结果。
// Search Vector 不在该契约中，避免向量调用方复制或覆盖全文投影事实；
// Repository 仍须在同一事务中证明目标 Projection 已 lexical ready。
type VectorProjectionWrite struct {
	ChunkID      foundation.ID
	Embedding    []float32
	TokenCount   int32
	VectorStatus VectorStatus
	FailureCode  string
}

// VectorProjectionBatch 是一次全有或全无的向量结果保存批次。
type VectorProjectionBatch struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	EmbeddingVersionID   foundation.ID
	ExpectedIndexVersion int64
	Projections          []VectorProjectionWrite
	At                   time.Time
}

// ValidateLexicalBuildCommand 校验全文批量构建与 Building Index 的乐观锁绑定。
func ValidateLexicalBuildCommand(index IndexVersion, command LexicalBuildCommand) error {
	if command.WorkspaceID != index.WorkspaceID || command.IndexVersionID != index.ID ||
		command.ExpectedIndexVersion != index.Version {
		return conflict(ErrorCodeProjectionInvalid, "lexical build binding or expected version is stale")
	}
	if index.Status != IndexStatusBuilding || command.At.IsZero() || command.At.Before(index.UpdatedAt) {
		return invalid(ErrorCodeProjectionInvalid, "lexical build requires a current building index")
	}
	return nil
}

// ValidateChunkProjection 校验 Projection 与 Index/Embedding Version 的完整绑定和状态内容。
func ValidateChunkProjection(index IndexVersion, embeddingVersion *EmbeddingVersion, projection ChunkProjection) error {
	if err := ValidateIndexVersion(index); err != nil {
		return err
	}
	if projection.IndexVersionID != index.ID || projection.WorkspaceID != index.WorkspaceID || projection.ChunkID == "" {
		return inconsistent(ErrorCodeProjectionInvalid, "projection binding does not match index")
	}
	if projection.TokenCount < 0 || projection.CreatedAt.IsZero() || projection.UpdatedAt.IsZero() || projection.UpdatedAt.Before(projection.CreatedAt) {
		return invalid(ErrorCodeProjectionInvalid, "projection counters or timestamps are invalid")
	}
	if !optionalIDEqual(index.EmbeddingVersionID, projection.EmbeddingVersionID) {
		return inconsistent(ErrorCodeProjectionInvalid, "projection embedding version does not match index")
	}
	if index.EmbeddingVersionID == nil {
		if embeddingVersion != nil {
			return inconsistent(ErrorCodeProjectionInvalid, "FTS-only index cannot bind an embedding version")
		}
	} else {
		if embeddingVersion == nil || embeddingVersion.ID != *index.EmbeddingVersionID {
			return inconsistent(ErrorCodeProjectionInvalid, "projection embedding metadata is missing or mismatched")
		}
		if err := ValidateEmbeddingVersion(*embeddingVersion); err != nil {
			return err
		}
	}
	if err := validateLexicalProjection(projection); err != nil {
		return err
	}
	if err := validateVectorProjection(index, embeddingVersion, projection); err != nil {
		return err
	}
	requiresFailureCode := projection.LexicalStatus == LexicalStatusFailed ||
		projection.VectorStatus == VectorStatusSkippedOversized || projection.VectorStatus == VectorStatusFailed
	if requiresFailureCode {
		if !isCanonicalText(projection.FailureCode) || len(projection.FailureCode) > 128 {
			return invalid(ErrorCodeProjectionInvalid, "failed or skipped projection requires a stable failure code")
		}
	} else if projection.FailureCode != "" {
		return invalid(ErrorCodeProjectionInvalid, "successful or pending projection cannot carry a failure code")
	}
	return nil
}

// ValidateVectorProjectionBatch 校验 vector-only 批次的版本绑定、数值和失败语义。
func ValidateVectorProjectionBatch(index IndexVersion, embeddingVersion EmbeddingVersion, batch VectorProjectionBatch) error {
	if err := ValidateIndexVersion(index); err != nil {
		return err
	}
	if batch.WorkspaceID != index.WorkspaceID || batch.IndexVersionID != index.ID ||
		batch.ExpectedIndexVersion != index.Version || index.EmbeddingVersionID == nil ||
		batch.EmbeddingVersionID != *index.EmbeddingVersionID || embeddingVersion.ID != batch.EmbeddingVersionID {
		return conflict(ErrorCodeProjectionInvalid, "vector projection batch binding or expected version is stale")
	}
	if index.Status != IndexStatusBuilding || len(batch.Projections) == 0 || batch.At.IsZero() || batch.At.Before(index.UpdatedAt) {
		return invalid(ErrorCodeProjectionInvalid, "vector projection batch requires a current building index")
	}
	if err := ValidateEmbeddingVersion(embeddingVersion); err != nil {
		return err
	}
	seenChunks := make(map[foundation.ID]struct{}, len(batch.Projections))
	for _, projection := range batch.Projections {
		if projection.ChunkID == "" || projection.TokenCount < 0 {
			return invalid(ErrorCodeProjectionInvalid, "vector projection identity or token count is invalid")
		}
		if _, duplicate := seenChunks[projection.ChunkID]; duplicate {
			return invalid(ErrorCodeProjectionInvalid, "vector projection batch contains duplicate chunk identity")
		}
		seenChunks[projection.ChunkID] = struct{}{}
		if err := validateVectorWrite(embeddingVersion, projection); err != nil {
			return err
		}
	}
	return nil
}

func validateLexicalProjection(projection ChunkProjection) error {
	switch projection.LexicalStatus {
	case LexicalStatusPending, LexicalStatusFailed:
		if strings.TrimSpace(projection.SearchVector) != "" {
			return invalid(ErrorCodeProjectionInvalid, "non-ready lexical projection cannot carry a search vector")
		}
	case LexicalStatusReady:
		if strings.TrimSpace(projection.SearchVector) == "" {
			return invalid(ErrorCodeProjectionInvalid, "ready lexical projection requires a search vector")
		}
	default:
		return invalid(ErrorCodeProjectionInvalid, "lexical projection status is unsupported")
	}
	return nil
}

func validateVectorProjection(index IndexVersion, embeddingVersion *EmbeddingVersion, projection ChunkProjection) error {
	switch projection.VectorStatus {
	case VectorStatusDisabled:
		if index.EmbeddingVersionID != nil || len(projection.Embedding) != 0 {
			return invalid(ErrorCodeProjectionInvalid, "disabled vector is only valid for FTS-only index")
		}
	case VectorStatusPending, VectorStatusSkippedOversized, VectorStatusFailed:
		if index.EmbeddingVersionID == nil || len(projection.Embedding) != 0 {
			return invalid(ErrorCodeProjectionInvalid, "non-ready vector status requires an embedding version and no vector value")
		}
	case VectorStatusReady:
		if embeddingVersion == nil {
			return invalid(ErrorCodeProjectionInvalid, "ready vector requires an embedding version")
		}
		if err := validateReadyVector(*embeddingVersion, projection.Embedding); err != nil {
			return err
		}
	default:
		return invalid(ErrorCodeProjectionInvalid, "vector projection status is unsupported")
	}
	return nil
}

func validateVectorWrite(embeddingVersion EmbeddingVersion, projection VectorProjectionWrite) error {
	switch projection.VectorStatus {
	case VectorStatusReady:
		if err := validateReadyVector(embeddingVersion, projection.Embedding); err != nil {
			return err
		}
		if projection.FailureCode != "" {
			return invalid(ErrorCodeProjectionInvalid, "ready vector cannot carry a failure code")
		}
	case VectorStatusSkippedOversized, VectorStatusFailed:
		if len(projection.Embedding) != 0 {
			return invalid(ErrorCodeProjectionInvalid, "non-ready vector cannot carry a vector value")
		}
		if !isCanonicalText(projection.FailureCode) || len(projection.FailureCode) > 128 {
			return invalid(ErrorCodeProjectionInvalid, "failed or skipped vector requires a stable failure code")
		}
	default:
		return invalid(ErrorCodeProjectionInvalid, "vector write status must be ready, skipped or failed")
	}
	return nil
}

func validateReadyVector(embeddingVersion EmbeddingVersion, embedding []float32) error {
	if len(embedding) != int(embeddingVersion.Dimensions) {
		return invalid(ErrorCodeProjectionInvalid, "ready vector dimensions do not match embedding version")
	}
	norm := float64(0)
	for _, value := range embedding {
		component := float64(value)
		if math.IsNaN(component) || math.IsInf(component, 0) {
			return invalid(ErrorCodeProjectionInvalid, "ready vector contains a non-finite value")
		}
		norm += component * component
	}
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return invalid(ErrorCodeProjectionInvalid, "ready vector must have a finite non-zero norm")
	}
	return nil
}
