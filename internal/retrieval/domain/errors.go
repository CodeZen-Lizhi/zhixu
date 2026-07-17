// Package domain 定义 Retrieval 索引、投影与激活的稳定领域契约。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeEmbeddingVersionInvalid 表示 Embedding Version 字段或枚举非法。
	ErrorCodeEmbeddingVersionInvalid = "RETRIEVAL_EMBEDDING_VERSION_INVALID"
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
