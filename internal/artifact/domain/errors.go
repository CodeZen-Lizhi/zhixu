// Package domain 定义 Artifact、Revision 与来源覆盖的纯领域规则。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeArtifactInvalid 表示 Artifact、Revision 或其不可变内容不合法。
	ErrorCodeArtifactInvalid = "ARTIFACT_INVALID"
	// ErrorCodeArtifactTransitionInvalid 表示 Artifact 生命周期迁移不合法。
	ErrorCodeArtifactTransitionInvalid = "ARTIFACT_TRANSITION_INVALID"
	// ErrorCodeArtifactCoverageInvalid 表示章节引用、Coverage 或知识缺口不合法。
	ErrorCodeArtifactCoverageInvalid = "ARTIFACT_COVERAGE_INVALID"
	// ErrorCodeArtifactPublicationInvalid 表示正式知识发布请求或确认绑定不合法。
	ErrorCodeArtifactPublicationInvalid = "ARTIFACT_PUBLICATION_INVALID"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}
