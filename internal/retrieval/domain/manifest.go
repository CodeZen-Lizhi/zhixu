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
		if chunk.IndexVersionID != indexVersionID || chunk.WorkspaceID != workspaceID || chunk.ChunkID == "" {
			return nil, "", invalid(ErrorCodeManifestInvalid, "manifest chunk binding does not match index")
		}
		if _, duplicate := seenChunks[chunk.ChunkID]; duplicate {
			return nil, "", invalid(ErrorCodeManifestInvalid, "manifest contains duplicate chunk identity")
		}
		seenChunks[chunk.ChunkID] = struct{}{}
		if chunk.Sequence < 0 || !isCanonicalHash(chunk.ContentHash) || !isCanonicalText(chunk.ParserVersion) ||
			!isCanonicalText(chunk.ChunkStrategyVersion) || !isCanonicalText(chunk.SchemaVersion) || chunk.CreatedAt.IsZero() {
			return nil, "", invalid(ErrorCodeManifestInvalid, "manifest chunk metadata is invalid")
		}
	}
	sort.Slice(canonical, func(left, right int) bool {
		if canonical[left].Sequence != canonical[right].Sequence {
			return canonical[left].Sequence < canonical[right].Sequence
		}
		return canonical[left].ChunkID < canonical[right].ChunkID
	})

	digest := sha256.New()
	writeHashField(digest, string(workspaceID))
	for _, chunk := range canonical {
		writeHashField(digest, string(chunk.ChunkID))
		writeHashField(digest, chunk.ContentHash)
		var sequence [4]byte
		binary.BigEndian.PutUint32(sequence[:], uint32(chunk.Sequence))
		_, _ = digest.Write(sequence[:])
		writeHashField(digest, chunk.ParserVersion)
		writeHashField(digest, chunk.ChunkStrategyVersion)
		writeHashField(digest, chunk.SchemaVersion)
	}
	return canonical, hex.EncodeToString(digest.Sum(nil)), nil
}

func writeHashField(destination hash.Hash, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}
