package domain

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRRFConfigCanonicalRoundTripAndStrictIngress(t *testing.T) {
	t.Parallel()
	config := validRRFConfig()
	canonical, err := CanonicalRRFConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"method":"rrf","k":60,"lexical_candidate_limit":200,"vector_candidate_limit":200,"fused_candidate_limit":100,"rerank_candidate_limit":50}`
	if string(canonical) != want {
		t.Fatalf("canonical = %s", canonical)
	}
	parsed, err := ParseRRFConfig(canonical)
	if err != nil || parsed != config {
		t.Fatalf("parsed=%#v err=%v", parsed, err)
	}
	jsonbStyle := json.RawMessage(`{"k":60,"method":"rrf","schema_version":1,"vector_candidate_limit":200,"lexical_candidate_limit":200,"fused_candidate_limit":100,"rerank_candidate_limit":50}`)
	if _, err := DecodeRRFConfig(jsonbStyle); err != nil {
		t.Fatalf("semantic jsonb decode error = %v", err)
	}
	if _, err := ParseRRFConfig(jsonbStyle); err == nil {
		t.Fatal("non-canonical field order accepted at ingress")
	}
}

func TestRRFConfigRejectsUnknownDuplicateMissingAndInvalidLimits(t *testing.T) {
	t.Parallel()
	invalidJSON := []json.RawMessage{
		json.RawMessage(`{"schema_version":1,"method":"rrf","k":60,"lexical_candidate_limit":200,"vector_candidate_limit":200,"fused_candidate_limit":100,"rerank_candidate_limit":50,"extra":1}`),
		json.RawMessage(`{"schema_version":1,"schema_version":1,"method":"rrf","k":60,"lexical_candidate_limit":200,"vector_candidate_limit":200,"fused_candidate_limit":100,"rerank_candidate_limit":50}`),
		json.RawMessage(`{"schema_version":1,"method":"rrf","k":60}`),
		json.RawMessage(`{"schema_version":1,"method":"rrf","k":60,"lexical_candidate_limit":200,"vector_candidate_limit":200,"fused_candidate_limit":100,"rerank_candidate_limit":50} {}`),
	}
	for _, raw := range invalidJSON {
		err := func() error { _, err := DecodeRRFConfig(raw); return err }()
		if err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
		assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeFusionConfigInvalid)
	}
	for _, mutate := range []func(*RRFConfig){
		func(value *RRFConfig) { value.SchemaVersion = 2 },
		func(value *RRFConfig) { value.Method = "weighted" },
		func(value *RRFConfig) { value.K = 0 },
		func(value *RRFConfig) { value.K = MaxRRFK + 1 },
		func(value *RRFConfig) { value.LexicalCandidateLimit = MaxFusionCandidateLimit + 1 },
		func(value *RRFConfig) { value.FusedCandidateLimit = 500 },
		func(value *RRFConfig) { value.RerankCandidateLimit = 101 },
	} {
		config := validRRFConfig()
		mutate(&config)
		if err := ValidateRRFConfig(config); err == nil {
			t.Fatalf("invalid config accepted: %#v", config)
		}
	}
}

func TestFuseRRFAcceptsMaximumKWithoutIntegerOverflow(t *testing.T) {
	t.Parallel()
	config := validRRFConfig()
	config.K = MaxRRFK
	candidate := RankedCandidate{ChunkID: "10000000-0000-4000-8000-000000000001", Rank: 1, Score: 1}
	result, err := FuseRRF(config, []RankedCandidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 1 / float64(int64(MaxRRFK)+1)
	if len(result) != 1 || result[0].Score != want || math.IsNaN(result[0].Score) || math.IsInf(result[0].Score, 0) {
		t.Fatalf("result=%#v want score=%v", result, want)
	}
}

func TestFuseRRFUsesRankOnlyAndStableChunkTieBreak(t *testing.T) {
	t.Parallel()
	config := validRRFConfig()
	chunkA := foundation.ID("10000000-0000-4000-8000-000000000001")
	chunkB := foundation.ID("10000000-0000-4000-8000-000000000002")
	chunkC := foundation.ID("10000000-0000-4000-8000-000000000003")
	lexical := []RankedCandidate{{ChunkID: chunkA, Rank: 1, Score: 99}, {ChunkID: chunkB, Rank: 2, Score: -10}}
	vector := []RankedCandidate{{ChunkID: chunkB, Rank: 1, Score: 0.01}, {ChunkID: chunkC, Rank: 2, Score: 1000}}
	result, err := FuseRRF(config, lexical, vector)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 || result[0].ChunkID != chunkB || result[0].Rank != 1 || len(result[0].Contributions) != 2 {
		t.Fatalf("result=%#v", result)
	}
	if lexical[0].Score != 99 || vector[0].Score != 0.01 {
		t.Fatal("FuseRRF mutated caller input")
	}

	tieConfig := config
	tieConfig.FusedCandidateLimit = 2
	tieConfig.RerankCandidateLimit = 2
	tied, err := FuseRRF(tieConfig,
		[]RankedCandidate{{ChunkID: chunkB, Rank: 1, Score: 2}},
		[]RankedCandidate{{ChunkID: chunkA, Rank: 1, Score: -200}},
	)
	if err != nil || tied[0].ChunkID != chunkA || tied[1].ChunkID != chunkB {
		t.Fatalf("tie result=%#v err=%v", tied, err)
	}
}

func TestFuseRRFRejectsCorruptStoreCandidates(t *testing.T) {
	t.Parallel()
	config := validRRFConfig()
	valid := []RankedCandidate{{ChunkID: "10000000-0000-4000-8000-000000000001", Rank: 1, Score: 1}}
	tests := [][]RankedCandidate{
		{{ChunkID: "bad", Rank: 1, Score: 1}},
		{{ChunkID: valid[0].ChunkID, Rank: 2, Score: 1}},
		{{ChunkID: valid[0].ChunkID, Rank: 1, Score: math.NaN()}},
		{{ChunkID: valid[0].ChunkID, Rank: 1, Score: 1}, {ChunkID: valid[0].ChunkID, Rank: 2, Score: 2}},
	}
	for _, candidates := range tests {
		_, err := FuseRRF(config, candidates, nil)
		if err == nil {
			t.Fatalf("corrupt candidates accepted: %#v", candidates)
		}
		assertDomainError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeSearchCandidateInvalid)
	}
	if result, err := FuseRRF(config, nil, nil); err != nil || result != nil {
		t.Fatalf("empty fusion result=%#v err=%v", result, err)
	}
}

func validRRFConfig() RRFConfig {
	return RRFConfig{
		SchemaVersion: 1, Method: FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 200, VectorCandidateLimit: 200,
		FusedCandidateLimit: 100, RerankCandidateLimit: 50,
	}
}
