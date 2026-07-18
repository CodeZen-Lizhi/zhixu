package application

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// Embedder 是 Retrieval 唯一的批量向量 Provider 端口。
type Embedder interface {
	// Contract 返回不含 Credential 的不可变运行时能力与配置身份。
	Contract() domain.EmbeddingContract
	// Embed 按输入顺序返回同数量向量；Adapter 不得在内部逐条远程调用。
	Embed(context.Context, EmbedRequest) (EmbedResult, error)
}

// EmbedRequest 是传给 Provider 的有界原始字符串批次。
type EmbedRequest struct {
	Inputs []string
}

// EmbedResult 是 Adapter 已恢复输入顺序后的模型与二维向量。
type EmbedResult struct {
	Model      string
	Embeddings [][]float32
}

// ValidateEmbedRequest 校验批量数量、单输入字节上限和 UTF-8，不修改正文。
func ValidateEmbedRequest(contract domain.EmbeddingContract, request EmbedRequest) error {
	if err := domain.ValidateEmbeddingContract(contract); err != nil {
		return err
	}
	if len(request.Inputs) == 0 || len(request.Inputs) > int(contract.MaxBatchSize) {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEmbedRequestInvalid, false, errors.New("embedding request batch size is invalid"))
	}
	var totalBytes int64
	for _, input := range request.Inputs {
		if strings.TrimSpace(input) == "" || len(input) > int(contract.MaxInputBytes) || !utf8.ValidString(input) {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEmbedRequestInvalid, false, errors.New("embedding request input is empty, oversized, or invalid utf-8"))
		}
		totalBytes += int64(len(input))
		if totalBytes > contract.MaxBatchInputBytes {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEmbedRequestInvalid, false, errors.New("embedding request batch input bytes exceed the configured limit"))
		}
	}
	return nil
}

// ValidateEmbedResult 校验精确模型、数量和向量后，返回统一归一化的调用方独立副本。
func ValidateEmbedResult(contract domain.EmbeddingContract, request EmbedRequest, result EmbedResult) ([][]float32, error) {
	if err := ValidateEmbedRequest(contract, request); err != nil {
		return nil, err
	}
	if result.Model != contract.Model || len(result.Embeddings) != len(request.Inputs) {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false, errors.New("embedding result model or count is invalid"))
	}
	normalized := make([][]float32, len(result.Embeddings))
	for index, embedding := range result.Embeddings {
		value, err := domain.NormalizeEmbeddingVector(contract.Normalization, embedding)
		if err != nil {
			return nil, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false, errors.New("embedding result vector cannot be normalized"))
		}
		if err := domain.ValidateEmbeddingContractVector(contract, value); err != nil {
			return nil, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid, false, errors.New("embedding result vector violates contract"))
		}
		normalized[index] = value
	}
	return normalized, nil
}
