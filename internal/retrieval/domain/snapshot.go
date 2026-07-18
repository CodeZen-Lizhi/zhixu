package domain

import (
	"encoding/json"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// DefaultSnapshotPageSize 是 Source/Chunk keyset 页的默认批次大小。
	DefaultSnapshotPageSize int32 = 1000
	// MaxSnapshotPageSize 限制单页驻留内存，防止配置绕过 bounded Snapshot。
	MaxSnapshotPageSize int32 = 10_000
	// DefaultSnapshotMaxSources 是产品基线允许的 Workspace Source 数。
	DefaultSnapshotMaxSources int64 = 10_000
	// DefaultSnapshotMaxChunks 是产品基线允许的 Canonical Chunk 数。
	DefaultSnapshotMaxChunks int64 = 500_000
)

// ProcessingContract 冻结 Snapshot 选择成功 Ingestion Attempt 所需的当前处理契约。
type ProcessingContract struct {
	ParserID             string
	ParserVersion        string
	ParserConfigHash     string
	ChunkStrategyVersion string
	SchemaVersion        string
}

// WorkspaceSnapshotCommand 描述一次在 Store 内分页选择并物化完整 Workspace Snapshot 的命令。
type WorkspaceSnapshotCommand struct {
	IndexVersion            IndexVersion
	TargetSourceID          foundation.ID
	TargetSourceVersionID   foundation.ID
	TargetParseProjectionID foundation.ID
	PageSize                int32
	MaxSources              int64
	MaxChunks               int64
}

// WorkspaceSnapshotResult 返回 bounded Snapshot 的持久 Index 与可审计计数。
type WorkspaceSnapshotResult struct {
	IndexVersion        IndexVersion
	SourceCount         int64
	ChunkCount          int64
	ExcludedSourceCount int64
	Created             bool
	Replayed            bool
}

// ValidateWorkspaceSnapshotCommand 校验 Store 查询与 Index 配置边界，不接受预先计算的 Manifest 身份。
func ValidateWorkspaceSnapshotCommand(command WorkspaceSnapshotCommand) error {
	index := command.IndexVersion
	if index.ID == "" || index.WorkspaceID == "" || index.EmbeddingVersionID != nil ||
		!isCanonicalText(index.TokenizerID) || !isCanonicalText(index.TokenizerVersion) ||
		!isCanonicalHash(index.TokenizerConfigHash) || !validJSONObject(index.FusionConfig) ||
		!isCanonicalText(index.SourceSnapshotRef) || !strings.HasPrefix(index.SourceSnapshotRef, "reindex-v1:") ||
		!isCanonicalText(index.IdempotencyKey) || len(index.IdempotencyKey) > 128 ||
		index.ManifestHash != "" || index.ExpectedChunkCount != 0 || index.SourceManifestHash != "" || index.ExpectedSourceCount != nil ||
		index.Status != IndexStatusBuilding || !equalCapabilities(index.DegradedCapabilities, []DegradedCapability{DegradedVector}) ||
		index.Version != 1 || index.CreatedAt.IsZero() || !index.CreatedAt.Equal(index.UpdatedAt) || index.FailureCode != "" {
		return invalid(ErrorCodeIndexVersionInvalid, "workspace snapshot index configuration is invalid")
	}
	if index.ProcessingContract == nil {
		return invalid(ErrorCodeManifestInvalid, "workspace snapshot processing contract is required")
	}
	contract := *index.ProcessingContract
	if command.TargetSourceID == "" || command.TargetSourceVersionID == "" || command.TargetParseProjectionID == "" ||
		!isCanonicalText(contract.ParserID) || !isCanonicalText(contract.ParserVersion) ||
		!isCanonicalHash(contract.ParserConfigHash) || !isCanonicalText(contract.ChunkStrategyVersion) ||
		!isCanonicalText(contract.SchemaVersion) || command.PageSize <= 0 || command.PageSize > MaxSnapshotPageSize ||
		command.MaxSources <= 0 || command.MaxChunks <= 0 {
		return invalid(ErrorCodeManifestInvalid, "workspace snapshot selection contract is invalid")
	}
	return nil
}

func validProcessingContract(contract ProcessingContract) bool {
	return isCanonicalText(contract.ParserID) && isCanonicalText(contract.ParserVersion) &&
		isCanonicalHash(contract.ParserConfigHash) && isCanonicalText(contract.ChunkStrategyVersion) &&
		isCanonicalText(contract.SchemaVersion)
}

func validJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}
