package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// RRFFusionSchemaVersionV1 是当前唯一支持的融合配置 Schema。
	RRFFusionSchemaVersionV1 int32 = 1
	// FusionMethodRRF 表示 Reciprocal Rank Fusion。
	FusionMethodRRF = "rrf"
	// MaxFusionCandidateLimit 限制任一检索阶段的服务端候选数。
	MaxFusionCandidateLimit int32 = 500
	// MaxRRFK 限制 RRF 平滑常数，与服务端候选配置共享同一有界产品范围。
	MaxRRFK int32 = MaxFusionCandidateLimit
)

// RRFConfig 冻结 Hybrid Index 使用的严格 RRF v1 配置。
type RRFConfig struct {
	SchemaVersion         int32  `json:"schema_version"`
	Method                string `json:"method"`
	K                     int32  `json:"k"`
	LexicalCandidateLimit int32  `json:"lexical_candidate_limit"`
	VectorCandidateLimit  int32  `json:"vector_candidate_limit"`
	FusedCandidateLimit   int32  `json:"fused_candidate_limit"`
	RerankCandidateLimit  int32  `json:"rerank_candidate_limit"`
}

// ValidateRRFConfig 校验版本、方法和全部候选上限关系。
func ValidateRRFConfig(config RRFConfig) error {
	if config.SchemaVersion != RRFFusionSchemaVersionV1 || config.Method != FusionMethodRRF ||
		config.K <= 0 || config.K > MaxRRFK {
		return invalid(ErrorCodeFusionConfigInvalid, "rrf version, method, or k is invalid")
	}
	limits := []int32{
		config.LexicalCandidateLimit,
		config.VectorCandidateLimit,
		config.FusedCandidateLimit,
		config.RerankCandidateLimit,
	}
	for _, limit := range limits {
		if limit <= 0 || limit > MaxFusionCandidateLimit {
			return invalid(ErrorCodeFusionConfigInvalid, "rrf candidate limit is invalid")
		}
	}
	if config.FusedCandidateLimit > config.LexicalCandidateLimit+config.VectorCandidateLimit ||
		config.RerankCandidateLimit > config.FusedCandidateLimit {
		return invalid(ErrorCodeFusionConfigInvalid, "rrf candidate limit relationship is invalid")
	}
	return nil
}

// CanonicalRRFConfig 返回字段顺序和数字表示稳定的 RRF v1 JSON。
func CanonicalRRFConfig(config RRFConfig) (json.RawMessage, error) {
	if err := ValidateRRFConfig(config); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, invalid(ErrorCodeFusionConfigInvalid, "rrf config cannot be encoded")
	}
	return encoded, nil
}

// ParseRRFConfig 严格解析 canonical RRF v1 ingress；空白和字段乱序也会被拒绝。
func ParseRRFConfig(raw json.RawMessage) (RRFConfig, error) {
	config, err := DecodeRRFConfig(raw)
	if err != nil {
		return RRFConfig{}, err
	}
	canonical, err := CanonicalRRFConfig(config)
	if err != nil || !bytes.Equal(raw, canonical) {
		return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config is not canonical")
	}
	return config, nil
}

// DecodeRRFConfig 严格校验 RRF v1 的字段语义，供不保留 JSON 字节顺序的 jsonb round-trip 使用。
func DecodeRRFConfig(raw json.RawMessage) (RRFConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config must be a json object")
	}
	var config RRFConfig
	seen := make(map[string]struct{}, 7)
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok {
			return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config field name is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config contains duplicate fields")
		}
		seen[key] = struct{}{}
		switch key {
		case "schema_version":
			err = decoder.Decode(&config.SchemaVersion)
		case "method":
			err = decoder.Decode(&config.Method)
		case "k":
			err = decoder.Decode(&config.K)
		case "lexical_candidate_limit":
			err = decoder.Decode(&config.LexicalCandidateLimit)
		case "vector_candidate_limit":
			err = decoder.Decode(&config.VectorCandidateLimit)
		case "fused_candidate_limit":
			err = decoder.Decode(&config.FusedCandidateLimit)
		case "rerank_candidate_limit":
			err = decoder.Decode(&config.RerankCandidateLimit)
		default:
			return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config contains unknown fields")
		}
		if err != nil {
			return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config field value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 7 {
		return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RRFConfig{}, invalid(ErrorCodeFusionConfigInvalid, "rrf config contains trailing json")
	}
	if err := ValidateRRFConfig(config); err != nil {
		return RRFConfig{}, err
	}
	return config, nil
}

// SearchRanker 标识参与融合的有界排名来源。
type SearchRanker string

const (
	// SearchRankerLexical 表示 FTS 与 trigram 已组合的词法排名。
	SearchRankerLexical SearchRanker = "lexical"
	// SearchRankerVector 表示指定 Embedding Version 的向量排名。
	SearchRankerVector SearchRanker = "vector"
)

// RankedCandidate 是一个 Ranker 返回的稳定 Chunk 排名。
type RankedCandidate struct {
	ChunkID foundation.ID
	Rank    int32
	Score   float64
}

// FusionContribution 保留一个 Ranker 对融合结果的原始排名和分数。
type FusionContribution struct {
	Ranker SearchRanker
	Rank   int32
	Score  float64
}

// FusedCandidate 是 RRF 输出的稳定 Chunk 排名和可解释贡献。
type FusedCandidate struct {
	ChunkID       foundation.ID
	Rank          int32
	Score         float64
	BestRank      int32
	Contributions []FusionContribution
}

// FuseRRF 使用 1-based rank 融合 Lexical/Vector，不混合原始分数。
func FuseRRF(config RRFConfig, lexical, vector []RankedCandidate) ([]FusedCandidate, error) {
	if err := ValidateRRFConfig(config); err != nil {
		return nil, err
	}
	if err := validateRankedCandidates(lexical, config.LexicalCandidateLimit); err != nil {
		return nil, err
	}
	if err := validateRankedCandidates(vector, config.VectorCandidateLimit); err != nil {
		return nil, err
	}
	type accumulator struct {
		score         float64
		bestRank      int32
		contributions []FusionContribution
	}
	accumulated := make(map[foundation.ID]*accumulator, len(lexical)+len(vector))
	add := func(ranker SearchRanker, candidates []RankedCandidate) {
		for _, candidate := range candidates {
			entry := accumulated[candidate.ChunkID]
			if entry == nil {
				entry = &accumulator{bestRank: candidate.Rank}
				accumulated[candidate.ChunkID] = entry
			}
			entry.score += 1 / float64(int64(config.K)+int64(candidate.Rank))
			if candidate.Rank < entry.bestRank {
				entry.bestRank = candidate.Rank
			}
			entry.contributions = append(entry.contributions, FusionContribution{Ranker: ranker, Rank: candidate.Rank, Score: candidate.Score})
		}
	}
	add(SearchRankerLexical, lexical)
	add(SearchRankerVector, vector)
	if len(accumulated) == 0 {
		return nil, nil
	}
	result := make([]FusedCandidate, 0, len(accumulated))
	for chunkID, entry := range accumulated {
		sort.Slice(entry.contributions, func(left, right int) bool {
			return entry.contributions[left].Ranker < entry.contributions[right].Ranker
		})
		result = append(result, FusedCandidate{
			ChunkID: chunkID, Score: entry.score, BestRank: entry.bestRank,
			Contributions: append([]FusionContribution(nil), entry.contributions...),
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Score != result[right].Score {
			return result[left].Score > result[right].Score
		}
		if result[left].BestRank != result[right].BestRank {
			return result[left].BestRank < result[right].BestRank
		}
		return result[left].ChunkID < result[right].ChunkID
	})
	if len(result) > int(config.FusedCandidateLimit) {
		result = result[:config.FusedCandidateLimit]
	}
	for index := range result {
		result[index].Rank = int32(index + 1)
	}
	return result, nil
}

func validateRankedCandidates(candidates []RankedCandidate, limit int32) error {
	if len(candidates) > int(limit) {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "ranked candidate list exceeds configured limit")
	}
	seen := make(map[foundation.ID]struct{}, len(candidates))
	for index, candidate := range candidates {
		parsed, err := foundation.ParseID(string(candidate.ChunkID))
		if err != nil || parsed != candidate.ChunkID || candidate.Rank != int32(index+1) ||
			math.IsNaN(candidate.Score) || math.IsInf(candidate.Score, 0) {
			return inconsistent(ErrorCodeSearchCandidateInvalid, "ranked candidate identity, rank, or score is invalid")
		}
		if _, duplicate := seen[candidate.ChunkID]; duplicate {
			return inconsistent(ErrorCodeSearchCandidateInvalid, "ranked candidate list contains duplicate chunks")
		}
		seen[candidate.ChunkID] = struct{}{}
	}
	return nil
}
