package application

import (
	"math"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateRerankResultRequiresExactCoverageWithoutDuplicates(t *testing.T) {
	t.Parallel()
	request := RerankRequest{Query: "query", Candidates: []RerankCandidate{
		{ChunkID: searchID(1), Text: "first"},
		{ChunkID: searchID(2), Text: "second"},
	}}
	if err := validateRerankRequest(request, 2); err != nil {
		t.Fatal(err)
	}
	valid := RerankResult{ModelVersion: "rerank-v1", Items: []RerankItem{
		{ChunkID: searchID(2), Score: 0.9},
		{ChunkID: searchID(1), Score: 0.7},
	}}
	if err := validateRerankResult(request, valid); err != nil {
		t.Fatal(err)
	}

	invalid := []RerankResult{
		{ModelVersion: "rerank-v1", Items: valid.Items[:1]},
		{ModelVersion: "rerank-v1", Items: []RerankItem{{ChunkID: searchID(1), Score: 1}, {ChunkID: searchID(1), Score: 0.5}}},
		{ModelVersion: "rerank-v1", Items: []RerankItem{{ChunkID: searchID(1), Score: 1}, {ChunkID: searchID(3), Score: 0.5}}},
		{ModelVersion: "rerank-v1", Items: []RerankItem{{ChunkID: searchID(1), Score: 1}, {ChunkID: searchID(2), Score: math.NaN()}}},
		{ModelVersion: " rerank-v1 ", Items: valid.Items},
	}
	for _, result := range invalid {
		err := validateRerankResult(request, result)
		assertSearchError(t, err, foundation.ErrorConsistencyViolation, rerankResultInvalidCode, false)
	}
}

func TestValidateRerankRequestRejectsUnboundedOrDamagedCandidates(t *testing.T) {
	t.Parallel()
	valid := RerankRequest{Query: "query", Candidates: []RerankCandidate{{ChunkID: searchID(1), Text: "text"}}}
	invalid := []RerankRequest{
		{Query: " ", Candidates: valid.Candidates},
		{Query: " query ", Candidates: valid.Candidates},
		{Query: "query"},
		{Query: "query", Candidates: []RerankCandidate{{ChunkID: "bad", Text: "text"}}},
		{Query: "query", Candidates: []RerankCandidate{{ChunkID: searchID(1), Text: " "}}},
		{Query: "query", Candidates: []RerankCandidate{{ChunkID: searchID(1), Text: strings.Repeat("x", 16*1024+1)}}},
		{Query: "query", Candidates: []RerankCandidate{{ChunkID: searchID(1), Text: "a"}, {ChunkID: searchID(1), Text: "b"}}},
	}
	for _, request := range invalid {
		err := validateRerankRequest(request, 1)
		assertSearchError(t, err, foundation.ErrorConsistencyViolation, rerankResultInvalidCode, false)
	}
}
