// Package capacity 提供 M10-03 容量基线共用的确定性数据生成与统计工具。
package capacity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultChunkCount 是正式容量基线要求的 Chunk 数量。
	DefaultChunkCount int64 = 500_000
	// DefaultRelationCount 是正式容量基线要求的 Relation 数量。
	DefaultRelationCount int64 = 500_000
	// DefaultGraphP95Limit 是局部图谱一跳查询的 P95 预算。
	DefaultGraphP95Limit = 1500 * time.Millisecond
	// DefaultRetrievalP95Limit 是检索本地阶段的 P95 预算。
	DefaultRetrievalP95Limit = 2 * time.Second
	// DefaultFrontendFPSFloor 是图谱交互采集的最低平均 FPS 门槛。
	DefaultFrontendFPSFloor = 45.0

	defaultSeed           = "m10-03-capacity-v1"
	chunkAlgorithmVersion = "sha256(seed\\x00chunk\\x00ordinal)"
	graphAlgorithmVersion = "ring-stride-v1"
	maxSeedBytes          = 128
	maxRecordCount        = 5_000_000
)

// Spec 描述一个可重放的 Chunk/Relation 容量数据集。
//
// Spec 不包含时间戳，写出的 manifest 因而可以在不同机器上逐字节复现。
type Spec struct {
	SchemaVersion  string `json:"schema_version"`
	Seed           string `json:"seed"`
	ChunkCount     int64  `json:"chunk_count"`
	RelationCount  int64  `json:"relation_count"`
	ChunkAlgorithm string `json:"chunk_algorithm"`
	GraphAlgorithm string `json:"graph_algorithm"`
}

// ChunkRecord 是生成器输出的最小 canonical Chunk 描述。
type ChunkRecord struct {
	ID            string `json:"id"`
	Sequence      int64  `json:"sequence"`
	ContentHash   string `json:"content_hash"`
	Content       string `json:"content"`
	ByteCount     int64  `json:"byte_count"`
	RuneCount     int64  `json:"rune_count"`
	Status        string `json:"status"`
	ParserVersion string `json:"parser_version"`
}

// RelationRecord 是生成器输出的最小 canonical Relation 描述。
type RelationRecord struct {
	ID          string `json:"id"`
	SourceID    string `json:"source_id"`
	TargetID    string `json:"target_id"`
	Relation    string `json:"relation_type"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint"`
}

// DefaultSpec 返回 M10-03 的默认数据集规格。
func DefaultSpec(seed string) (Spec, error) {
	if seed == "" {
		seed = defaultSeed
	}
	spec := Spec{
		SchemaVersion:  "zhixu-capacity/v1",
		Seed:           seed,
		ChunkCount:     DefaultChunkCount,
		RelationCount:  DefaultRelationCount,
		ChunkAlgorithm: chunkAlgorithmVersion,
		GraphAlgorithm: graphAlgorithmVersion,
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// Validate 检查容量规格的边界，避免脚本因环境变量误配而无界分配。
func (spec Spec) Validate() error {
	if spec.SchemaVersion == "" || spec.SchemaVersion != "zhixu-capacity/v1" {
		return errors.New("capacity schema version is invalid")
	}
	if spec.Seed == "" || spec.Seed != strings.TrimSpace(spec.Seed) || !utf8.ValidString(spec.Seed) ||
		strings.ContainsRune(spec.Seed, '\x00') || len([]byte(spec.Seed)) > maxSeedBytes {
		return errors.New("capacity seed must be bounded canonical UTF-8")
	}
	if spec.ChunkCount < 1 || spec.ChunkCount > maxRecordCount || spec.RelationCount < 1 || spec.RelationCount > maxRecordCount {
		return fmt.Errorf("capacity counts must be between 1 and %d", maxRecordCount)
	}
	if spec.ChunkAlgorithm != chunkAlgorithmVersion || spec.GraphAlgorithm != graphAlgorithmVersion {
		return errors.New("capacity generation algorithms are invalid")
	}
	return nil
}

// RelationNodeCount 返回关系拓扑使用的稳定节点数。
func RelationNodeCount(spec Spec) int64 {
	nodes := spec.RelationCount / 5
	if nodes < 2 {
		nodes = 2
	}
	if nodes > 100_000 {
		nodes = 100_000
	}
	return nodes
}

// StableID 将 seed、kind 和序号映射为稳定的 RFC 4122 UUID。
func StableID(seed, kind string, ordinal int64) string {
	digest := sha256.Sum256([]byte(seed + "\x00" + kind + "\x00" + fmt.Sprint(ordinal)))
	bytes := digest[:16]
	// 设置 version 4 / RFC 4122 variant 位，使下游 UUID 解码器可直接使用。
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(bytes[0:4]), hex.EncodeToString(bytes[4:6]),
		hex.EncodeToString(bytes[6:8]), hex.EncodeToString(bytes[8:10]),
		hex.EncodeToString(bytes[10:16]))
}

// ChunkAt 返回指定序号的确定性 Chunk。
func ChunkAt(spec Spec, ordinal int64) (ChunkRecord, error) {
	if err := spec.Validate(); err != nil {
		return ChunkRecord{}, err
	}
	return chunkAtValidated(spec, ordinal)
}

func chunkAtValidated(spec Spec, ordinal int64) (ChunkRecord, error) {
	if ordinal < 0 || ordinal >= spec.ChunkCount {
		return ChunkRecord{}, fmt.Errorf("chunk ordinal %d is outside [0,%d)", ordinal, spec.ChunkCount)
	}
	content := fmt.Sprintf("M10 capacity chunk %08d seed=%s", ordinal, spec.Seed)
	digest := sha256.Sum256([]byte(content))
	return ChunkRecord{
		ID:            StableID(spec.Seed, "chunk", ordinal),
		Sequence:      ordinal,
		ContentHash:   hex.EncodeToString(digest[:]),
		Content:       content,
		ByteCount:     int64(len(content)),
		RuneCount:     int64(utf8.RuneCountInString(content)),
		Status:        "active",
		ParserVersion: "capacity-parser-v1",
	}, nil
}

// RelationAt 返回指定序号的确定性、无自环 Relation。
func RelationAt(spec Spec, ordinal int64) (RelationRecord, error) {
	if err := spec.Validate(); err != nil {
		return RelationRecord{}, err
	}
	return relationAtValidated(spec, ordinal)
}

func relationAtValidated(spec Spec, ordinal int64) (RelationRecord, error) {
	if ordinal < 0 || ordinal >= spec.RelationCount {
		return RelationRecord{}, fmt.Errorf("relation ordinal %d is outside [0,%d)", ordinal, spec.RelationCount)
	}
	nodes := RelationNodeCount(spec)
	var sourceOrdinal, targetOrdinal int64
	if ordinal < nodes-1 {
		sourceOrdinal = 0
		targetOrdinal = ordinal + 1
	} else {
		position := ordinal - (nodes - 1)
		sourceOrdinal = 1 + position%(nodes-1)
		stride := 1 + (position/(nodes-1))%(nodes-1)
		targetOrdinal = 1 + ((sourceOrdinal - 1 + stride) % (nodes - 1))
	}
	sourceID := StableID(spec.Seed, "topic", sourceOrdinal)
	targetID := StableID(spec.Seed, "topic", targetOrdinal)
	fingerprintDigest := sha256.Sum256([]byte("capacity-relation/v1\n" + spec.Seed + "\n" + sourceID + "\n" + targetID))
	return RelationRecord{
		ID:          StableID(spec.Seed, "relation", ordinal),
		SourceID:    sourceID,
		TargetID:    targetID,
		Relation:    "IMPACTS",
		Status:      "CONFIRMED",
		Fingerprint: hex.EncodeToString(fingerprintDigest[:]),
	}, nil
}

// WriteManifest 将规格以稳定缩进 JSON 写入 writer。
func WriteManifest(writer io.Writer, spec Spec) error {
	if writer == nil {
		return errors.New("capacity manifest writer is nil")
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(spec)
}

// WriteJSONL 流式写出 Chunk 或 Relation，避免一次性分配 500k 条记录。
func WriteJSONL(writer io.Writer, spec Spec, kind string) (int64, error) {
	if writer == nil {
		return 0, errors.New("capacity JSONL writer is nil")
	}
	if err := spec.Validate(); err != nil {
		return 0, err
	}
	if kind != "chunks" && kind != "relations" {
		return 0, errors.New("capacity JSONL kind must be chunks or relations")
	}
	encoder := json.NewEncoder(writer)
	var count int64
	if kind == "chunks" {
		for ordinal := int64(0); ordinal < spec.ChunkCount; ordinal++ {
			record, err := chunkAtValidated(spec, ordinal)
			if err != nil {
				return count, err
			}
			if err := encoder.Encode(record); err != nil {
				return count, err
			}
			count++
		}
		return count, nil
	}
	for ordinal := int64(0); ordinal < spec.RelationCount; ordinal++ {
		record, err := relationAtValidated(spec, ordinal)
		if err != nil {
			return count, err
		}
		if err := encoder.Encode(record); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// PercentileNearestRank 返回 nearest-rank 百分位，适合容量采样的保守门禁。
func PercentileNearestRank(values []time.Duration, percentile int) (time.Duration, error) {
	if len(values) == 0 || percentile < 1 || percentile > 100 {
		return 0, errors.New("percentile requires non-empty values and 1..100")
	}
	ordered := append([]time.Duration(nil), values...)
	slices.Sort(ordered)
	rank := (percentile*len(ordered) + 99) / 100
	return ordered[rank-1], nil
}
