package application

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const rerankResultInvalidCode = "RETRIEVAL_RERANK_RESULT_INVALID"

// Reranker 是可选的有界候选重排端口。
type Reranker interface {
	// Rerank 必须精确覆盖输入候选且不得返回重复 Chunk。
	Rerank(context.Context, RerankRequest) (RerankResult, error)
}

// RerankCandidate 是传给 Reranker 的最小 Chunk 身份与有界正文。
type RerankCandidate struct {
	ChunkID foundation.ID
	Text    string
}

// RerankRequest 描述一次有界重排请求。
type RerankRequest struct {
	Query      string
	Candidates []RerankCandidate
}

// RerankItem 使用返回顺序表达最终 rank，并保留有限原始分数。
type RerankItem struct {
	ChunkID foundation.ID
	Score   float64
}

// RerankResult 是供应商无关的精确覆盖输出。
type RerankResult struct {
	ModelVersion string
	Items        []RerankItem
}

func validateRerankRequest(request RerankRequest, limit int32) error {
	if strings.TrimSpace(request.Query) == "" || request.Query != strings.TrimSpace(request.Query) ||
		len(request.Query) > domain.MaxSearchQueryBytes || !utf8.ValidString(request.Query) ||
		len(request.Candidates) == 0 || len(request.Candidates) > int(limit) {
		return rerankConsistency("rerank request is invalid")
	}
	seen := make(map[foundation.ID]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		parsed, err := foundation.ParseID(string(candidate.ChunkID))
		if err != nil || parsed != candidate.ChunkID || strings.TrimSpace(candidate.Text) == "" ||
			len(candidate.Text) > domain.MaxRerankTextBytes || !utf8.ValidString(candidate.Text) {
			return rerankConsistency("rerank candidate is invalid")
		}
		if _, duplicate := seen[candidate.ChunkID]; duplicate {
			return rerankConsistency("rerank request contains duplicate chunks")
		}
		seen[candidate.ChunkID] = struct{}{}
	}
	return nil
}

func validateRerankResult(request RerankRequest, result RerankResult) error {
	if result.ModelVersion == "" || result.ModelVersion != strings.TrimSpace(result.ModelVersion) ||
		len(result.ModelVersion) > 256 || !utf8.ValidString(result.ModelVersion) ||
		len(result.Items) != len(request.Candidates) {
		return rerankConsistency("rerank result model or count is invalid")
	}
	expected := make(map[foundation.ID]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		expected[candidate.ChunkID] = struct{}{}
	}
	seen := make(map[foundation.ID]struct{}, len(result.Items))
	for _, item := range result.Items {
		if _, exists := expected[item.ChunkID]; !exists || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) {
			return rerankConsistency("rerank result contains an unknown chunk or invalid score")
		}
		if _, duplicate := seen[item.ChunkID]; duplicate {
			return rerankConsistency("rerank result contains duplicate chunks")
		}
		seen[item.ChunkID] = struct{}{}
	}
	return nil
}

func rerankConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, rerankResultInvalidCode, false, errors.New(message))
}
