package domain

import (
	"encoding/json"
	"sort"
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

// SnapshotTarget binds one Source to the exact successful projection selected
// by a Workspace snapshot.
type SnapshotTarget struct {
	SourceID          foundation.ID
	SourceVersionID   foundation.ID
	ParseProjectionID foundation.ID
}

// WorkspaceSnapshotCommand 描述一次在 Store 内分页选择并物化完整 Workspace Snapshot 的命令。
type WorkspaceSnapshotCommand struct {
	IndexVersion IndexVersion
	// TargetSource* keeps the single-source API backward compatible. New batch
	// callers use Targets and leave these legacy fields empty.
	TargetSourceID          foundation.ID
	TargetSourceVersionID   foundation.ID
	TargetParseProjectionID foundation.ID
	Targets                 []SnapshotTarget
	RemovedSourceIDs        []foundation.ID
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
// FTS-only Snapshot 保持历史 JSON 兼容并显式声明 vector degraded；Hybrid Snapshot 必须绑定
// Embedding Version、使用 canonical RRF v1，且不能预先声明 vector degraded。
func ValidateWorkspaceSnapshotCommand(command WorkspaceSnapshotCommand) error {
	index := command.IndexVersion
	if index.ID == "" || index.WorkspaceID == "" ||
		!isCanonicalText(index.TokenizerID) || !isCanonicalText(index.TokenizerVersion) ||
		!isCanonicalHash(index.TokenizerConfigHash) ||
		!isCanonicalText(index.SourceSnapshotRef) || !validSourceSnapshotRef(index.SourceSnapshotRef) ||
		!isCanonicalText(index.IdempotencyKey) || len(index.IdempotencyKey) > 128 ||
		index.ManifestHash != "" || index.ExpectedChunkCount != 0 || index.SourceManifestHash != "" || index.ExpectedSourceCount != nil ||
		index.Status != IndexStatusBuilding ||
		index.Version != 1 || index.CreatedAt.IsZero() || !index.CreatedAt.Equal(index.UpdatedAt) || index.FailureCode != "" {
		return invalid(ErrorCodeIndexVersionInvalid, "workspace snapshot index configuration is invalid")
	}
	if index.EmbeddingVersionID == nil {
		if !validJSONObject(index.FusionConfig) || !equalCapabilities(index.DegradedCapabilities, []DegradedCapability{DegradedVector}) {
			return invalid(ErrorCodeIndexVersionInvalid, "FTS-only workspace snapshot configuration is invalid")
		}
	} else {
		if *index.EmbeddingVersionID == "" || len(index.DegradedCapabilities) != 0 {
			return invalid(ErrorCodeIndexVersionInvalid, "hybrid workspace snapshot embedding binding is invalid")
		}
		if _, err := ParseRRFConfig(index.FusionConfig); err != nil {
			return err
		}
	}
	if index.ProcessingContract == nil {
		return invalid(ErrorCodeManifestInvalid, "workspace snapshot processing contract is required")
	}
	contract := *index.ProcessingContract
	targets, err := WorkspaceSnapshotTargets(command)
	if err != nil {
		return err
	}
	if !isCanonicalText(contract.ParserID) || !isCanonicalText(contract.ParserVersion) ||
		!isCanonicalHash(contract.ParserConfigHash) || !isCanonicalText(contract.ChunkStrategyVersion) ||
		!isCanonicalText(contract.SchemaVersion) || command.PageSize <= 0 || command.PageSize > MaxSnapshotPageSize ||
		command.MaxSources <= 0 || command.MaxChunks <= 0 {
		return invalid(ErrorCodeManifestInvalid, "workspace snapshot selection contract is invalid")
	}
	if len(targets) == 0 && len(command.RemovedSourceIDs) == 0 {
		return invalid(ErrorCodeManifestInvalid, "workspace snapshot has no source changes")
	}
	if err := validateSnapshotSourceSets(targets, command.RemovedSourceIDs); err != nil {
		return err
	}
	return nil
}

// WorkspaceSnapshotTargets returns the canonical target set for legacy and
// batch snapshot commands.
func WorkspaceSnapshotTargets(command WorkspaceSnapshotCommand) ([]SnapshotTarget, error) {
	legacyConfigured := command.TargetSourceID != "" || command.TargetSourceVersionID != "" || command.TargetParseProjectionID != ""
	if len(command.Targets) > 0 {
		if legacyConfigured {
			return nil, invalid(ErrorCodeManifestInvalid, "workspace snapshot target forms cannot be mixed")
		}
		return append([]SnapshotTarget(nil), command.Targets...), nil
	}
	if !legacyConfigured {
		return nil, nil
	}
	if command.TargetSourceID == "" || command.TargetSourceVersionID == "" || command.TargetParseProjectionID == "" {
		return nil, invalid(ErrorCodeManifestInvalid, "workspace snapshot legacy target is incomplete")
	}
	return []SnapshotTarget{{
		SourceID: command.TargetSourceID, SourceVersionID: command.TargetSourceVersionID,
		ParseProjectionID: command.TargetParseProjectionID,
	}}, nil
}

func validateSnapshotSourceSets(targets []SnapshotTarget, removed []foundation.ID) error {
	seen := make(map[foundation.ID]struct{}, len(targets)+len(removed))
	for index, target := range targets {
		if target.SourceID == "" || target.SourceVersionID == "" || target.ParseProjectionID == "" ||
			(index > 0 && targets[index-1].SourceID >= target.SourceID) {
			return invalid(ErrorCodeManifestInvalid, "workspace snapshot targets must be complete, unique, and sorted")
		}
		seen[target.SourceID] = struct{}{}
	}
	if !sort.SliceIsSorted(removed, func(left, right int) bool { return removed[left] < removed[right] }) {
		return invalid(ErrorCodeManifestInvalid, "workspace snapshot removals must be sorted")
	}
	for index, sourceID := range removed {
		if sourceID == "" || (index > 0 && removed[index-1] == sourceID) {
			return invalid(ErrorCodeManifestInvalid, "workspace snapshot removals must be unique")
		}
		if _, exists := seen[sourceID]; exists {
			return invalid(ErrorCodeManifestInvalid, "workspace snapshot source cannot be targeted and removed")
		}
		seen[sourceID] = struct{}{}
	}
	return nil
}

func validSourceSnapshotRef(value string) bool {
	return strings.HasPrefix(value, "reindex-v1:") || strings.HasPrefix(value, "source-refresh-v1:") ||
		strings.HasPrefix(value, "git-fast-forward-v1:")
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
