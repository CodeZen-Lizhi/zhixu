// Package domain 定义 Retrieval 索引、投影与激活的稳定领域契约。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeEmbeddingVersionInvalid 表示 Embedding Version 字段或枚举非法。
	ErrorCodeEmbeddingVersionInvalid = "RETRIEVAL_EMBEDDING_VERSION_INVALID"
	// ErrorCodeEmbeddingContractInvalid 表示运行时 Embedder Contract 不完整或与版本绑定冲突。
	ErrorCodeEmbeddingContractInvalid = "RETRIEVAL_EMBEDDING_CONTRACT_INVALID"
	// ErrorCodeEmbedRequestInvalid 表示发给 Embedder 的批量输入不满足 Contract。
	ErrorCodeEmbedRequestInvalid = "RETRIEVAL_EMBED_REQUEST_INVALID"
	// ErrorCodeEmbedResultInvalid 表示 Embedder 返回的模型、数量或向量损坏。
	ErrorCodeEmbedResultInvalid = "RETRIEVAL_EMBED_RESULT_INVALID"
	// ErrorCodeFusionConfigInvalid 表示版本化融合配置不满足严格 Schema。
	ErrorCodeFusionConfigInvalid = "RETRIEVAL_FUSION_CONFIG_INVALID"
	// ErrorCodeSearchRequestInvalid 表示检索请求、过滤或服务端上限非法。
	ErrorCodeSearchRequestInvalid = "RETRIEVAL_SEARCH_REQUEST_INVALID"
	// ErrorCodeSearchCandidateInvalid 表示候选、证据或阶段分数绑定非法。
	ErrorCodeSearchCandidateInvalid = "RETRIEVAL_SEARCH_CANDIDATE_INVALID"
	// ErrorCodeSearchResultInvalid 表示检索模式、降级或结果绑定不一致。
	ErrorCodeSearchResultInvalid = "RETRIEVAL_SEARCH_RESULT_INVALID"
	// ErrorCodeIndexVersionInvalid 表示 Index Version 或构建请求不满足领域约束。
	ErrorCodeIndexVersionInvalid = "RETRIEVAL_INDEX_VERSION_INVALID"
	// ErrorCodeIndexTransitionInvalid 表示尝试绕过冻结的 Index 生命周期。
	ErrorCodeIndexTransitionInvalid = "RETRIEVAL_INDEX_STATE_TRANSITION_INVALID"
	// ErrorCodeManifestInvalid 表示不可变 Manifest 缺失、重复或绑定不一致。
	ErrorCodeManifestInvalid = "RETRIEVAL_MANIFEST_INVALID"
	// ErrorCodeProjectionInvalid 表示 Chunk Projection 内容、状态或版本绑定非法。
	ErrorCodeProjectionInvalid = "RETRIEVAL_PROJECTION_INVALID"
	// ErrorCodeActivationInvalid 表示激活命令或目标状态不满足原子切换契约。
	ErrorCodeActivationInvalid = "RETRIEVAL_ACTIVATION_INVALID"
	// ErrorCodeBuildIncomplete 表示 Manifest/Projection 聚合不足以进入 Ready。
	ErrorCodeBuildIncomplete = "RETRIEVAL_BUILD_INCOMPLETE"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func conflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}
