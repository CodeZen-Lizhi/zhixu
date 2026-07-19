// Package domain 定义 Agent 的稳定结构化输出与模型运行契约。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// NewValidationExhaustedError 创建三阶段结构化输出预算耗尽的稳定非重试错误。
func NewValidationExhaustedError(cause error) error {
	if cause == nil {
		cause = errors.New("structured output validation exhausted")
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeValidationExhausted, false, cause)
}

const (
	// ErrorCodeStructuredOutputInvalid 表示模型输出不是受支持的严格 JSON 文档。
	ErrorCodeStructuredOutputInvalid = "AGENT_STRUCTURED_OUTPUT_INVALID"
	// ErrorCodeStructuredOutputLimitExceeded 表示模型输出超过固定资源边界。
	ErrorCodeStructuredOutputLimitExceeded = "AGENT_STRUCTURED_OUTPUT_LIMIT_EXCEEDED"
	// ErrorCodeSchemaInvalid 表示任务 Schema 身份或载荷不合法。
	ErrorCodeSchemaInvalid = "AGENT_SCHEMA_INVALID"
	// ErrorCodeReferenceInvalid 表示版本化模型、Profile、Prompt 或 Schema 引用不合法。
	ErrorCodeReferenceInvalid = "AGENT_REFERENCE_INVALID"
	// ErrorCodeCitationInvalid 表示 Citation 身份或绑定不合法。
	ErrorCodeCitationInvalid = "AGENT_CITATION_INVALID"
	// ErrorCodeEvidenceInvalid 表示 Evidence 载荷或资格投影不合法。
	ErrorCodeEvidenceInvalid = "AGENT_EVIDENCE_INVALID"
	// ErrorCodeAssertionInvalid 表示 Answer assertion 未绑定引用或推断标记。
	ErrorCodeAssertionInvalid = "AGENT_ASSERTION_INVALID"
	// ErrorCodeAnswerInvalid 表示 RAG Answer 载荷不合法。
	ErrorCodeAnswerInvalid = "AGENT_ANSWER_INVALID"
	// ErrorCodeQueryPlanInvalid 表示 RAG Query Plan 或澄清判断不合法。
	ErrorCodeQueryPlanInvalid = "AGENT_QUERY_PLAN_INVALID"
	// ErrorCodeRefusalInvalid 表示 Refusal 载荷不合法。
	ErrorCodeRefusalInvalid = "AGENT_REFUSAL_INVALID"
	// ErrorCodeFaithfulnessReviewInvalid 表示 Faithfulness Review 载荷不合法。
	ErrorCodeFaithfulnessReviewInvalid = "AGENT_FAITHFULNESS_REVIEW_INVALID"
	// ErrorCodeRelationAssessmentInvalid 表示 Relation Assessment 载荷不合法。
	ErrorCodeRelationAssessmentInvalid = "AGENT_RELATION_ASSESSMENT_INVALID"
	// ErrorCodeModelRunInvalid 表示 Model Run 事实不完整或不一致。
	ErrorCodeModelRunInvalid = "AGENT_MODEL_RUN_INVALID"
	// ErrorCodeModelCallInvalid 表示 Model Call 事实不完整或不一致。
	ErrorCodeModelCallInvalid = "AGENT_MODEL_CALL_INVALID"
	// ErrorCodeModelRunTransitionInvalid 表示 Model Run 状态迁移不合法。
	ErrorCodeModelRunTransitionInvalid = "AGENT_MODEL_RUN_TRANSITION_INVALID"
	// ErrorCodeModelCallTransitionInvalid 表示 Model Call 状态迁移不合法。
	ErrorCodeModelCallTransitionInvalid = "AGENT_MODEL_CALL_TRANSITION_INVALID"
	// ErrorCodeValidationExhausted 表示 INITIAL、REPAIR、REDUCED 均未得到合法输出。
	ErrorCodeValidationExhausted = "VALIDATION_EXHAUSTED"
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
