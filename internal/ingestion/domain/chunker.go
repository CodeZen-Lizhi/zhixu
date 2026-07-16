package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"unicode/utf8"
)

// ChunkOptions 固定 canonical Chunk 的版本和结构软上限。
type ChunkOptions struct {
	StrategyVersion string
	SchemaVersion   string
	SoftMaxBytes    int64
}

// DefaultChunkSoftMaxBytes keeps ordinary chunks bounded while preserving
// atomic code/table blocks through the explicit oversized flag.
const DefaultChunkSoftMaxBytes int64 = 256 * 1024

// ChunkResult 返回确定性 Chunk 和显式降级警告。
type ChunkResult struct {
	Chunks   []CanonicalChunk
	Warnings []Warning
}

// BuildCanonicalChunks 将每个结构块映射为一个连续 Source Span Chunk。
// v1 采用一块一 Chunk，优先保证原始位置、代码和表格结构完整性。
func BuildCanonicalChunks(parsed ParsedDocument, spans []SourceSpan, options ChunkOptions) (ChunkResult, error) {
	if options.StrategyVersion == "" || options.SchemaVersion == "" {
		return ChunkResult{}, errors.New("chunk strategy and schema versions are required")
	}
	if options.SoftMaxBytes < 0 {
		return ChunkResult{}, errors.New("chunk soft max bytes must not be negative")
	}
	if len(parsed.Blocks) != len(spans) {
		return ChunkResult{}, errors.New("parsed blocks and source spans must have equal length")
	}
	result := ChunkResult{Chunks: make([]CanonicalChunk, 0, len(parsed.Blocks))}
	for index, block := range parsed.Blocks {
		if block.Content == "" {
			continue
		}
		span := spans[index]
		if span.ID == "" {
			return ChunkResult{}, errors.New("source span id is required")
		}
		byteCount := int64(len([]byte(block.Content)))
		oversized := block.Atomic && options.SoftMaxBytes > 0 && byteCount > options.SoftMaxBytes
		if oversized {
			result.Warnings = append(result.Warnings, Warning{
				Code: "ATOMIC_BLOCK_OVERSIZED", Message: "原子结构块超过分块软上限，已保留完整内容",
				StartByte: block.StartByte, EndByte: block.EndByte,
			})
		}
		result.Chunks = append(result.Chunks, CanonicalChunk{
			WorkspaceID:          span.WorkspaceID,
			ParseProjectionID:    span.ParseProjectionID,
			Sequence:             int32(len(result.Chunks)),
			HeadingPath:          append([]string(nil), block.HeadingPath...),
			Content:              block.Content,
			ContentHash:          hashText(block.Content),
			SourceSpanID:         span.ID,
			ByteCount:            byteCount,
			RuneCount:            int64(utf8.RuneCountInString(block.Content)),
			ParserVersion:        parsed.ParserVersion,
			ChunkStrategyVersion: options.StrategyVersion,
			SchemaVersion:        options.SchemaVersion,
			AtomicOversized:      oversized,
			Status:               "active",
		})
	}
	return result, nil
}

func hashText(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}
