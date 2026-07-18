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
	// ErrorCodeSnapshotStructureRegressionFailed 是结构回归失败后写入 Index 的稳定失败码。
	ErrorCodeSnapshotStructureRegressionFailed = "SNAPSHOT_STRUCTURE_V1_FAILED"
	snapshotRegressionHashPrefix               = "snapshot-regression-v1"
)

// SnapshotRegressionCommand 绑定一次结构回归所验证的 Delivery、目标 Source 与 Building Index。
type SnapshotRegressionCommand struct {
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

// ValidateSnapshotRegression 证明 source-bound FTS-only Building Index 满足 Ready 前结构闭包。
func ValidateSnapshotRegression(command SnapshotRegressionCommand, observation SnapshotRegressionObservation) (SnapshotRegressionProof, error) {
	if !validSnapshotRegressionCommand(command) {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("regression command binding is invalid")
	}
	index := observation.Index
	if err := ValidateIndexVersion(index); err != nil {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("index binding is invalid")
	}
	if index.ID != command.IndexVersionID || index.WorkspaceID != command.WorkspaceID || index.Status != IndexStatusBuilding ||
		index.EmbeddingVersionID != nil || !equalCapabilities(index.DegradedCapabilities, []DegradedCapability{DegradedVector}) ||
		index.ExpectedSourceCount == nil || index.ProcessingContract == nil {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("index is not a source-bound FTS-only building index")
	}
	target := observation.TargetManifestSource
	if target.IndexVersionID != index.ID || target.WorkspaceID != command.WorkspaceID ||
		target.SourceID != command.TargetSourceID || target.SourceVersionID != command.TargetSourceVersionID ||
		target.ParseProjectionID != command.TargetParseProjectionID || target.SelectionStatus != SourceSelectionIncluded ||
		target.ExclusionCode != "" || observation.TargetSourceResultHash != command.TargetResultHash {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("target source binding is absent or inconsistent")
	}
	if observation.SourceManifestHash != index.SourceManifestHash || observation.SourceManifestCount != *index.ExpectedSourceCount ||
		observation.ChunkManifestHash != index.ManifestHash || observation.ChunkManifestCount != index.ExpectedChunkCount ||
		observation.ExpectedUnionChunkCount != index.ExpectedChunkCount || observation.MissingChunkCount != 0 || observation.ExtraChunkCount != 0 {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("source or chunk manifest closure is inconsistent")
	}
	if observation.InvalidIncludedSourceCount != 0 {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("included source processing evidence is inconsistent")
	}
	if err := ValidateBuildReady(index, observation.BuildStatus); err != nil {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("build status is not ready-complete")
	}
	if observation.BuildStatus.VectorDisabledCount != index.ExpectedChunkCount ||
		observation.BuildStatus.VectorPendingCount != 0 || observation.BuildStatus.VectorReadyCount != 0 ||
		observation.BuildStatus.VectorSkippedOversizedCount != 0 || observation.BuildStatus.VectorFailedCount != 0 {
		return SnapshotRegressionProof{}, snapshotRegressionFailure("FTS-only vector state is inconsistent")
	}
	return SnapshotRegressionProof{Code: SnapshotStructureRegressionV1, Hash: snapshotRegressionHash(command, observation)}, nil
}

func validSnapshotRegressionCommand(command SnapshotRegressionCommand) bool {
	return command.WorkspaceID != "" && command.DeliveryID != "" && command.TargetSourceID != "" &&
		command.TargetSourceVersionID != "" && command.TargetParseProjectionID != "" && command.IndexVersionID != "" &&
		isCanonicalHash(command.TargetResultHash)
}

func snapshotRegressionFailure(message string) error {
	return foundation.NewError(
		foundation.ErrorConsistencyViolation,
		ErrorCodeSnapshotStructureRegressionFailed,
		false,
		errors.New(message),
	)
}

// SnapshotRegressionFailure 创建结构回归 Store 对外返回的稳定非重试一致性错误。
func SnapshotRegressionFailure(message string) error {
	return snapshotRegressionFailure(message)
}

func snapshotRegressionHash(command SnapshotRegressionCommand, observation SnapshotRegressionObservation) string {
	digest := sha256.New()
	writeHashField(digest, snapshotRegressionHashPrefix)
	writeHashField(digest, SnapshotStructureRegressionV1)
	writeHashField(digest, string(command.WorkspaceID))
	writeHashField(digest, string(command.DeliveryID))
	writeHashField(digest, string(command.TargetSourceID))
	writeHashField(digest, string(command.TargetSourceVersionID))
	writeHashField(digest, command.TargetResultHash)
	writeHashField(digest, string(command.TargetParseProjectionID))
	writeHashField(digest, string(command.IndexVersionID))
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
