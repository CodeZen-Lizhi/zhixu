package domain

import (
	"strings"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// ClarificationSchemaID 是会话澄清结果的稳定 Schema ID。
	ClarificationSchemaID = "conversation.clarification"
	// ClarificationSchemaVersionV1 是会话澄清结果的首个版本。
	ClarificationSchemaVersionV1 = "v1"
	// MaxClarificationQuestionBytes 是澄清问题允许的最大 UTF-8 字节数。
	MaxClarificationQuestionBytes = 8 * 1024

	maxClarificationReasonBytes = 2 * 1024
	maxSuggestedScopes          = 10
	maxSuggestedScopeBytes      = 2 * 1024
)

// ClarificationPayload 描述缺失信息、澄清问题和可选范围建议。
type ClarificationPayload struct {
	Reason          string   `json:"reason"`
	Question        string   `json:"question"`
	SuggestedScopes []string `json:"suggested_scopes"`
}

// Validate 校验澄清文本有界、规范且范围建议不重复。
func (payload ClarificationPayload) Validate() error {
	if !validBoundedText(payload.Reason, maxClarificationReasonBytes, true) ||
		!validBoundedText(payload.Question, MaxClarificationQuestionBytes, true) ||
		payload.SuggestedScopes == nil || len(payload.SuggestedScopes) > maxSuggestedScopes {
		return invalid(ErrorCodeClarificationInvalid, "clarification payload is invalid", nil)
	}
	seen := make(map[string]struct{}, len(payload.SuggestedScopes))
	for _, scope := range payload.SuggestedScopes {
		if !validBoundedText(scope, maxSuggestedScopeBytes, true) {
			return invalid(ErrorCodeClarificationInvalid, "clarification scope is invalid", nil)
		}
		if _, duplicate := seen[scope]; duplicate {
			return invalid(ErrorCodeClarificationInvalid, "clarification scope is duplicated", nil)
		}
		seen[scope] = struct{}{}
	}
	return nil
}

// ClarificationResult 是独立于 Answer 和 Refusal 的版本化会话结果。
type ClarificationResult struct {
	ResultType    string               `json:"result_type"`
	SchemaID      string               `json:"schema_id"`
	SchemaVersion string               `json:"schema_version"`
	ModelRunRef   foundation.ID        `json:"model_run_ref"`
	Payload       ClarificationPayload `json:"payload"`
}

// Validate 校验 Clarification Envelope 与载荷。
func (result ClarificationResult) Validate() error {
	modelRunID, err := foundation.ParseID(string(result.ModelRunRef))
	if err != nil || modelRunID != result.ModelRunRef || result.ResultType != agentdomain.ResultTypeClarification ||
		result.SchemaID != ClarificationSchemaID || result.SchemaVersion != ClarificationSchemaVersionV1 {
		return invalid(ErrorCodeClarificationInvalid, "clarification envelope is invalid", err)
	}
	return result.Payload.Validate()
}

// DecodeClarification 严格解析一个 Clarification v1 文档。
func DecodeClarification(raw []byte, limits foundationstrictjson.Limits) (ClarificationResult, error) {
	result, err := foundationstrictjson.DecodeObject(raw, limits, ClarificationResult.Validate)
	if err != nil {
		return ClarificationResult{}, invalid(ErrorCodeClarificationInvalid, "clarification document is invalid", err)
	}
	return result, nil
}

func validBoundedText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') || len(value) > maximum {
		return false
	}
	return !required || value != ""
}
