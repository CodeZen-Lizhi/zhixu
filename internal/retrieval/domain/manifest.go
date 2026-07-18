package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SourceSelectionStatus 表示 Index Snapshot 对一个 Workspace Source 的稳定选择结果。
type SourceSelectionStatus string

const (
	// SourceSelectionIncluded 表示 Source 已冻结一个合格 SourceVersion 与 ParseProjection。
	SourceSelectionIncluded SourceSelectionStatus = "included"
	// SourceSelectionExcluded 表示 Source 因稳定原因未进入本次 Chunk 并集。
	SourceSelectionExcluded SourceSelectionStatus = "excluded"
)

// SourceExclusionCode 是 Source 未进入 Snapshot 的稳定原因。
type SourceExclusionCode string

const (
	// SourceExclusionNoCurrentSuccessfulProjection 表示不存在匹配当前处理契约的成功投影。
	SourceExclusionNoCurrentSuccessfulProjection SourceExclusionCode = "NO_CURRENT_SUCCESSFUL_PROJECTION"
)

// ManifestSource 冻结一个 Workspace Source 在 Index Snapshot 中的 included/excluded 事实。
type ManifestSource struct {
	IndexVersionID    foundation.ID
	WorkspaceID       foundation.ID
	SourceID          foundation.ID
	SourceVersionID   foundation.ID
	ParseProjectionID foundation.ID
	SelectionStatus   SourceSelectionStatus
	ExclusionCode     SourceExclusionCode
	CreatedAt         time.Time
}

// ManifestHasher 以稳定顺序增量计算 Chunk Manifest，避免大 Snapshot 全量驻留内存。
type ManifestHasher struct {
	workspaceID, indexVersionID foundation.ID
	digest                      hash.Hash
	lastSequence                int32
	lastChunkID                 foundation.ID
	count                       int64
	started                     bool
}

// NewManifestHasher 创建与 CanonicalizeManifest 相同字节契约的流式 Hasher。
func NewManifestHasher(workspaceID, indexVersionID foundation.ID) (*ManifestHasher, error) {
	if workspaceID == "" || indexVersionID == "" {
		return nil, invalid(ErrorCodeManifestInvalid, "manifest workspace and index identities are required")
	}
	digest := sha256.New()
	writeHashField(digest, string(workspaceID))
	return &ManifestHasher{workspaceID: workspaceID, indexVersionID: indexVersionID, digest: digest}, nil
}

// Add 按 sequence、chunk_id 顺序追加一行 Manifest。
func (h *ManifestHasher) Add(chunk ManifestChunk) error {
	if h == nil || h.digest == nil || !validManifestChunk(h.workspaceID, h.indexVersionID, chunk) {
		return invalid(ErrorCodeManifestInvalid, "manifest chunk metadata is invalid")
	}
	if h.started && (chunk.Sequence < h.lastSequence || chunk.Sequence == h.lastSequence && chunk.ChunkID <= h.lastChunkID) {
		return invalid(ErrorCodeManifestInvalid, "manifest chunks are not in canonical order")
	}
	writeHashField(h.digest, string(chunk.ChunkID))
	writeHashField(h.digest, chunk.ContentHash)
	var sequence [4]byte
	binary.BigEndian.PutUint32(sequence[:], uint32(chunk.Sequence))
	_, _ = h.digest.Write(sequence[:])
	writeHashField(h.digest, chunk.ParserVersion)
	writeHashField(h.digest, chunk.ChunkStrategyVersion)
	writeHashField(h.digest, chunk.SchemaVersion)
	h.lastSequence, h.lastChunkID, h.started = chunk.Sequence, chunk.ChunkID, true
	h.count++
	return nil
}

// Result 返回当前 Chunk Count 与 SHA-256；零 Chunk 是合法空 Manifest。
func (h *ManifestHasher) Result() (string, int64, error) {
	if h == nil || h.digest == nil {
		return "", 0, invalid(ErrorCodeManifestInvalid, "manifest hasher is not initialized")
	}
	return hex.EncodeToString(h.digest.Sum(nil)), h.count, nil
}

// SourceManifestHasher 以 Source ID 稳定顺序增量计算 Source Manifest。
type SourceManifestHasher struct {
	workspaceID, indexVersionID foundation.ID
	digest                      hash.Hash
	lastSourceID                foundation.ID
	count, excludedCount        int64
}

// NewSourceManifestHasher 创建 source-manifest-v1 流式 Hasher。
func NewSourceManifestHasher(workspaceID, indexVersionID foundation.ID) (*SourceManifestHasher, error) {
	if workspaceID == "" || indexVersionID == "" {
		return nil, invalid(ErrorCodeManifestInvalid, "source manifest identities are required")
	}
	digest := sha256.New()
	writeHashField(digest, "source-manifest-v1")
	return &SourceManifestHasher{workspaceID: workspaceID, indexVersionID: indexVersionID, digest: digest}, nil
}

// Add 按 Source ID 追加一个已验证的 included/excluded 选择。
func (h *SourceManifestHasher) Add(source ManifestSource) error {
	if h == nil || h.digest == nil || !validManifestSource(h.workspaceID, h.indexVersionID, source) {
		return invalid(ErrorCodeManifestInvalid, "source manifest binding is invalid")
	}
	if h.lastSourceID != "" && source.SourceID <= h.lastSourceID {
		return invalid(ErrorCodeManifestInvalid, "source manifest is not in canonical order")
	}
	writeHashField(h.digest, string(source.SourceID))
	writeHashField(h.digest, string(source.SelectionStatus))
	writeHashField(h.digest, string(source.SourceVersionID))
	writeHashField(h.digest, string(source.ParseProjectionID))
	writeHashField(h.digest, string(source.ExclusionCode))
	h.lastSourceID = source.SourceID
	h.count++
	if source.SelectionStatus == SourceSelectionExcluded {
		h.excludedCount++
	}
	return nil
}

// Result 返回 Source Count、Excluded Count 与 SHA-256；Source Manifest 不允许为空。
func (h *SourceManifestHasher) Result() (string, int64, int64, error) {
	if h == nil || h.digest == nil || h.count == 0 {
		return "", 0, 0, invalid(ErrorCodeManifestInvalid, "source manifest is empty or uninitialized")
	}
	return hex.EncodeToString(h.digest.Sum(nil)), h.count, h.excludedCount, nil
}

// ManifestChunk 冻结一次 Index Build 所消费的 Canonical Chunk 身份与版本绑定。
// 它只保存引用和哈希，不复制 Canonical Chunk 正文。
type ManifestChunk struct {
	IndexVersionID       foundation.ID
	ChunkID              foundation.ID
	WorkspaceID          foundation.ID
	ContentHash          string
	Sequence             int32
	ParserVersion        string
	ChunkStrategyVersion string
	SchemaVersion        string
	CreatedAt            time.Time
}

// CanonicalizeManifest 校验 Manifest 绑定、返回稳定排序副本并计算不可变 SHA-256。
// 摘要不包含 IndexVersionID 和 CreatedAt，使同一 Workspace 快照的重建身份保持稳定。
func CanonicalizeManifest(workspaceID, indexVersionID foundation.ID, chunks []ManifestChunk) ([]ManifestChunk, string, error) {
	if workspaceID == "" || indexVersionID == "" {
		return nil, "", invalid(ErrorCodeManifestInvalid, "manifest workspace and index identities are required")
	}
	canonical := append([]ManifestChunk(nil), chunks...)
	seenChunks := make(map[foundation.ID]struct{}, len(canonical))
	for _, chunk := range canonical {
		if !validManifestChunk(workspaceID, indexVersionID, chunk) {
			return nil, "", invalid(ErrorCodeManifestInvalid, "manifest chunk metadata is invalid")
		}
		if _, duplicate := seenChunks[chunk.ChunkID]; duplicate {
			return nil, "", invalid(ErrorCodeManifestInvalid, "manifest contains duplicate chunk identity")
		}
		seenChunks[chunk.ChunkID] = struct{}{}
	}
	sort.Slice(canonical, func(left, right int) bool {
		if canonical[left].Sequence != canonical[right].Sequence {
			return canonical[left].Sequence < canonical[right].Sequence
		}
		return canonical[left].ChunkID < canonical[right].ChunkID
	})

	hasher, _ := NewManifestHasher(workspaceID, indexVersionID)
	for _, chunk := range canonical {
		if err := hasher.Add(chunk); err != nil {
			return nil, "", err
		}
	}
	manifestHash, _, _ := hasher.Result()
	return canonical, manifestHash, nil
}

// CanonicalizeSourceManifest 校验 Source Snapshot、返回按 Source ID 排序的副本与稳定 SHA-256。
// IndexVersionID 与 CreatedAt 不进入摘要，使相同 Source 选择在精确重建时保持同一身份。
func CanonicalizeSourceManifest(workspaceID, indexVersionID foundation.ID, sources []ManifestSource) ([]ManifestSource, string, int64, error) {
	if workspaceID == "" || indexVersionID == "" || len(sources) == 0 {
		return nil, "", 0, invalid(ErrorCodeManifestInvalid, "source manifest identities and rows are required")
	}
	canonical := append([]ManifestSource(nil), sources...)
	seenSources := make(map[foundation.ID]struct{}, len(canonical))
	var excludedCount int64
	for _, source := range canonical {
		if !validManifestSource(workspaceID, indexVersionID, source) {
			return nil, "", 0, invalid(ErrorCodeManifestInvalid, "source manifest binding is invalid")
		}
		if _, duplicate := seenSources[source.SourceID]; duplicate {
			return nil, "", 0, invalid(ErrorCodeManifestInvalid, "source manifest contains duplicate source identity")
		}
		seenSources[source.SourceID] = struct{}{}
		switch source.SelectionStatus {
		case SourceSelectionIncluded:
		case SourceSelectionExcluded:
			excludedCount++
		default:
			return nil, "", 0, invalid(ErrorCodeManifestInvalid, "source manifest selection status is invalid")
		}
	}
	sort.Slice(canonical, func(left, right int) bool { return canonical[left].SourceID < canonical[right].SourceID })
	hasher, _ := NewSourceManifestHasher(workspaceID, indexVersionID)
	for _, source := range canonical {
		if err := hasher.Add(source); err != nil {
			return nil, "", 0, err
		}
	}
	manifestHash, _, _, _ := hasher.Result()
	return canonical, manifestHash, excludedCount, nil
}

func validManifestChunk(workspaceID, indexVersionID foundation.ID, chunk ManifestChunk) bool {
	return chunk.IndexVersionID == indexVersionID && chunk.WorkspaceID == workspaceID && chunk.ChunkID != "" &&
		chunk.Sequence >= 0 && isCanonicalHash(chunk.ContentHash) && isCanonicalText(chunk.ParserVersion) &&
		isCanonicalText(chunk.ChunkStrategyVersion) && isCanonicalText(chunk.SchemaVersion) && !chunk.CreatedAt.IsZero()
}

func validManifestSource(workspaceID, indexVersionID foundation.ID, source ManifestSource) bool {
	if source.IndexVersionID != indexVersionID || source.WorkspaceID != workspaceID || source.SourceID == "" || source.CreatedAt.IsZero() {
		return false
	}
	if source.SelectionStatus == SourceSelectionIncluded {
		return source.SourceVersionID != "" && source.ParseProjectionID != "" && source.ExclusionCode == ""
	}
	return source.SelectionStatus == SourceSelectionExcluded && source.SourceVersionID == "" && source.ParseProjectionID == "" &&
		source.ExclusionCode == SourceExclusionNoCurrentSuccessfulProjection
}

func writeHashField(destination hash.Hash, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}
