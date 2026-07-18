package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// SnapshotStructureRegressionV1 是 M6-B 完整 Workspace Snapshot 的结构回归契约。
	SnapshotStructureRegressionV1 = "SNAPSHOT_STRUCTURE_V1"
	// SnapshotStructureRegressionV2 是绑定 Embedding Version 与 vector 终态的 Hybrid Snapshot 契约。
	SnapshotStructureRegressionV2 = "SNAPSHOT_STRUCTURE_V2"
	// ErrorCodeSnapshotStructureRegressionFailed 是结构回归失败后写入 Index 的稳定失败码。
	ErrorCodeSnapshotStructureRegressionFailed = "SNAPSHOT_STRUCTURE_V1_FAILED"
	// ErrorCodeSnapshotStructureV2RegressionFailed 是 Hybrid 结构回归失败后的稳定失败码。
	ErrorCodeSnapshotStructureV2RegressionFailed = "SNAPSHOT_STRUCTURE_V2_FAILED"
	snapshotRegressionHashPrefix                 = "snapshot-regression-v1"
	snapshotRegressionV2HashPrefix               = "snapshot-regression-v2"
)

// SnapshotRegressionCommand 绑定一次结构回归所验证的 Delivery、目标 Source 与 Building Index。
type SnapshotRegressionCommand struct {
	RegressionCode          string
	WorkspaceID             foundation.ID
	DeliveryID              foundation.ID
	TargetSourceID          foundation.ID
	TargetSourceVersionID   foundation.ID
	TargetResultHash        string
	TargetParseProjectionID foundation.ID
	IndexVersionID          foundation.ID
}

// SnapshotRegressionObservation 是 Store 在单一一致快照中收集并验证的结构事实。
type SnapshotRegressionObservation struct {
	Index                      IndexVersion
	TargetManifestSource       ManifestSource
	TargetSourceResultHash     string
	SourceManifestHash         string
	SourceManifestCount        int64
	ChunkManifestHash          string
	ChunkManifestCount         int64
	ExpectedUnionChunkCount    int64
	MissingChunkCount          int64
	ExtraChunkCount            int64
	InvalidIncludedSourceCount int64
	BuildStatus                BuildStatus
}

// SnapshotRegressionProof 返回可持久化到 Delivery checkpoint 的稳定回归码与摘要。
type SnapshotRegressionProof struct {
	Code string
	Hash string
}

// SnapshotRegressionResult 返回结构证明及数据库一致快照的通过时间。
type SnapshotRegressionResult struct {
	Code     string
	Hash     string
	PassedAt time.Time
}

// ValidateSnapshotRegression 按显式版本证明 FTS-only V1 或 Hybrid V2 满足 Ready 前结构闭包。
func ValidateSnapshotRegression(command SnapshotRegressionCommand, observation SnapshotRegressionObservation) (SnapshotRegressionProof, error) {
	if !validSnapshotRegressionCommand(command) {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "regression command binding is invalid")
	}
	index := observation.Index
	if err := ValidateIndexVersion(index); err != nil {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "index binding is invalid")
	}
	if index.ID != command.IndexVersionID || index.WorkspaceID != command.WorkspaceID || index.Status != IndexStatusBuilding ||
		index.ExpectedSourceCount == nil || index.ProcessingContract == nil {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "index is not a source-bound building index")
	}
	switch command.RegressionCode {
	case SnapshotStructureRegressionV1:
		if index.EmbeddingVersionID != nil || !equalCapabilities(index.DegradedCapabilities, []DegradedCapability{DegradedVector}) {
			return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "v1 index is not FTS-only")
		}
	case SnapshotStructureRegressionV2:
		if index.EmbeddingVersionID == nil {
			return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "v2 index has no embedding version")
		}
		if _, err := DecodeRRFConfig(index.FusionConfig); err != nil {
			return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "v2 fusion config is invalid")
		}
	}
	target := observation.TargetManifestSource
	if target.IndexVersionID != index.ID || target.WorkspaceID != command.WorkspaceID ||
		target.SourceID != command.TargetSourceID || target.SourceVersionID != command.TargetSourceVersionID ||
		target.ParseProjectionID != command.TargetParseProjectionID || target.SelectionStatus != SourceSelectionIncluded ||
		target.ExclusionCode != "" || observation.TargetSourceResultHash != command.TargetResultHash {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "target source binding is absent or inconsistent")
	}
	if observation.SourceManifestHash != index.SourceManifestHash || observation.SourceManifestCount != *index.ExpectedSourceCount ||
		observation.ChunkManifestHash != index.ManifestHash || observation.ChunkManifestCount != index.ExpectedChunkCount ||
		observation.ExpectedUnionChunkCount != index.ExpectedChunkCount || observation.MissingChunkCount != 0 || observation.ExtraChunkCount != 0 {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "source or chunk manifest closure is inconsistent")
	}
	if observation.InvalidIncludedSourceCount != 0 {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "included source processing evidence is inconsistent")
	}
	readyCapabilities := DeriveReadyDegradedCapabilities(index, observation.BuildStatus)
	observation.Index.DegradedCapabilities = readyCapabilities
	observation.BuildStatus.DegradedCapabilities = readyCapabilities
	if err := ValidateBuildReady(observation.Index, observation.BuildStatus); err != nil {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "build status is not ready-complete")
	}
	if command.RegressionCode == SnapshotStructureRegressionV1 &&
		(observation.BuildStatus.VectorDisabledCount != index.ExpectedChunkCount ||
			observation.BuildStatus.VectorPendingCount != 0 || observation.BuildStatus.VectorReadyCount != 0 ||
			observation.BuildStatus.VectorSkippedOversizedCount != 0 || observation.BuildStatus.VectorFailedCount != 0) {
		return SnapshotRegressionProof{}, snapshotRegressionFailure(command.RegressionCode, "FTS-only vector state is inconsistent")
	}
	return SnapshotRegressionProof{Code: command.RegressionCode, Hash: snapshotRegressionHash(command, observation)}, nil
}

func validSnapshotRegressionCommand(command SnapshotRegressionCommand) bool {
	return IsSnapshotRegressionCode(command.RegressionCode) && command.WorkspaceID != "" && command.DeliveryID != "" && command.TargetSourceID != "" &&
		command.TargetSourceVersionID != "" && command.TargetParseProjectionID != "" && command.IndexVersionID != "" &&
		isCanonicalHash(command.TargetResultHash)
}

func snapshotRegressionFailure(code, message string) error {
	errorCode := ErrorCodeSnapshotStructureRegressionFailed
	if code == SnapshotStructureRegressionV2 {
		errorCode = ErrorCodeSnapshotStructureV2RegressionFailed
	}
	return foundation.NewError(
		foundation.ErrorConsistencyViolation,
		errorCode,
		false,
		errors.New(message),
	)
}

// SnapshotRegressionFailure 创建结构回归 Store 对外返回的稳定非重试一致性错误。
func SnapshotRegressionFailure(message string) error {
	return snapshotRegressionFailure(SnapshotStructureRegressionV1, message)
}

// SnapshotRegressionFailureFor 创建指定版本的稳定结构回归错误。
func SnapshotRegressionFailureFor(code, message string) error {
	return snapshotRegressionFailure(code, message)
}

// IsSnapshotRegressionCode 判断 Delivery/Regression 是否使用已冻结的结构版本。
func IsSnapshotRegressionCode(code string) bool {
	return code == SnapshotStructureRegressionV1 || code == SnapshotStructureRegressionV2
}

// SnapshotRegressionCodeMatchesIndex 证明 V1 只用于 FTS-only，V2 只用于 Hybrid。
func SnapshotRegressionCodeMatchesIndex(code string, index IndexVersion) bool {
	return code == SnapshotStructureRegressionV1 && index.EmbeddingVersionID == nil ||
		code == SnapshotStructureRegressionV2 && index.EmbeddingVersionID != nil
}

func snapshotRegressionHash(command SnapshotRegressionCommand, observation SnapshotRegressionObservation) string {
	digest := sha256.New()
	prefix := snapshotRegressionHashPrefix
	if command.RegressionCode == SnapshotStructureRegressionV2 {
		prefix = snapshotRegressionV2HashPrefix
	}
	writeHashField(digest, prefix)
	writeHashField(digest, command.RegressionCode)
	writeHashField(digest, string(command.WorkspaceID))
	writeHashField(digest, string(command.DeliveryID))
	writeHashField(digest, string(command.TargetSourceID))
	writeHashField(digest, string(command.TargetSourceVersionID))
	writeHashField(digest, command.TargetResultHash)
	writeHashField(digest, string(command.TargetParseProjectionID))
	writeHashField(digest, string(command.IndexVersionID))
	if command.RegressionCode == SnapshotStructureRegressionV2 {
		writeHashField(digest, string(*observation.Index.EmbeddingVersionID))
	}
	writeHashField(digest, observation.SourceManifestHash)
	writeRegressionInt64(digest, observation.SourceManifestCount)
	writeHashField(digest, observation.ChunkManifestHash)
	writeRegressionInt64(digest, observation.ChunkManifestCount)
	writeRegressionInt64(digest, observation.ExpectedUnionChunkCount)
	writeRegressionInt64(digest, observation.InvalidIncludedSourceCount)
	writeHashField(digest, observation.Index.TokenizerID)
	writeHashField(digest, observation.Index.TokenizerVersion)
	writeHashField(digest, observation.Index.TokenizerConfigHash)
	writeHashField(digest, canonicalRegressionJSON(observation.Index.FusionConfig))
	writeHashField(digest, observation.Index.ProcessingContract.ParserID)
	writeHashField(digest, observation.Index.ProcessingContract.ParserVersion)
	writeHashField(digest, observation.Index.ProcessingContract.ParserConfigHash)
	writeHashField(digest, observation.Index.ProcessingContract.ChunkStrategyVersion)
	writeHashField(digest, observation.Index.ProcessingContract.SchemaVersion)
	writeRegressionInt64(digest, observation.Index.Version)
	writeRegressionBuildStatus(digest, observation.BuildStatus)
	return hex.EncodeToString(digest.Sum(nil))
}

func writeRegressionBuildStatus(digest hash.Hash, status BuildStatus) {
	writeRegressionInt64(digest, status.ExpectedChunkCount)
	writeRegressionInt64(digest, status.ManifestChunkCount)
	writeRegressionInt64(digest, status.SourceManifestCount)
	writeRegressionInt64(digest, status.IncludedSourceCount)
	writeRegressionInt64(digest, status.ExcludedSourceCount)
	writeRegressionInt64(digest, status.ProjectionCount)
	writeRegressionInt64(digest, status.LexicalPendingCount)
	writeRegressionInt64(digest, status.LexicalReadyCount)
	writeRegressionInt64(digest, status.LexicalFailedCount)
	writeRegressionInt64(digest, status.VectorDisabledCount)
	writeRegressionInt64(digest, status.VectorPendingCount)
	writeRegressionInt64(digest, status.VectorReadyCount)
	writeRegressionInt64(digest, status.VectorSkippedOversizedCount)
	writeRegressionInt64(digest, status.VectorFailedCount)
	for _, capability := range status.DegradedCapabilities {
		writeHashField(digest, string(capability))
	}
}

func writeRegressionInt64(digest hash.Hash, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = digest.Write(encoded[:])
}

func canonicalRegressionJSON(value json.RawMessage) string {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return string(value)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return string(value)
	}
	return string(canonical)
}
