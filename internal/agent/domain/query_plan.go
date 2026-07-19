package domain

import "github.com/CodeZen-Lizhi/zhixu/internal/foundation"

const (
	// RAGQueryPlanSchemaID 是检索改写与澄清判断的稳定 Schema ID。
	RAGQueryPlanSchemaID = "agent.rag-query-plan"
	// ResultTypeRAGQueryPlan 是 Query Plan 的稳定结果类型。
	ResultTypeRAGQueryPlan = "rag_query_plan"

	maxQueryRewrites       = 3
	maxQueryRewriteBytes   = 8 * 1024
	maxSuggestedPlanScopes = 10
)

// RAGQueryPlanPayload 描述是否需要澄清及后续实际检索改写。
type RAGQueryPlanPayload struct {
	Intent                string   `json:"intent"`
	RequiresClarification bool     `json:"requires_clarification"`
	Rewrites              []string `json:"rewrites"`
	ClarificationReason   string   `json:"clarification_reason"`
	ClarificationQuestion string   `json:"clarification_question"`
	SuggestedScopes       []string `json:"suggested_scopes"`
}

// Validate 校验 Query Plan 的澄清和检索分支互斥且所有文本有界。
func (payload RAGQueryPlanPayload) Validate() error {
	if !boundedText(payload.Intent, maxReasonBytes, true) || payload.Rewrites == nil || payload.SuggestedScopes == nil {
		return invalid(ErrorCodeQueryPlanInvalid, "query plan intent or required lists are invalid")
	}
	if payload.RequiresClarification {
		if len(payload.Rewrites) != 0 || !boundedText(payload.ClarificationReason, maxReasonBytes, true) ||
			!boundedText(payload.ClarificationQuestion, maxQueryRewriteBytes, true) ||
			!uniquePlanTexts(payload.SuggestedScopes, false, maxSuggestedPlanScopes, maxReasonBytes) {
			return invalid(ErrorCodeQueryPlanInvalid, "clarification plan is invalid")
		}
		return nil
	}
	if payload.ClarificationReason != "" || payload.ClarificationQuestion != "" || len(payload.SuggestedScopes) != 0 ||
		!uniquePlanTexts(payload.Rewrites, true, maxQueryRewrites, maxQueryRewriteBytes) {
		return invalid(ErrorCodeQueryPlanInvalid, "retrieval plan is invalid")
	}
	return nil
}

// RAGQueryPlanResult 是独立版本化的 Query Plan Envelope。
type RAGQueryPlanResult struct {
	ResultType    string              `json:"result_type"`
	SchemaID      string              `json:"schema_id"`
	SchemaVersion string              `json:"schema_version"`
	ModelRunRef   foundation.ID       `json:"model_run_ref"`
	Payload       RAGQueryPlanPayload `json:"payload"`
}

// Validate 校验 Query Plan Envelope 与载荷。
func (result RAGQueryPlanResult) Validate() error {
	if result.ResultType != ResultTypeRAGQueryPlan || result.SchemaID != RAGQueryPlanSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV1 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "rag query plan envelope is invalid")
	}
	return result.Payload.Validate()
}

// DecodeRAGQueryPlan 严格解析一个 RAG Query Plan v1 文档。
func DecodeRAGQueryPlan(raw []byte, limits DecodeLimits) (RAGQueryPlanResult, error) {
	return DecodeStrict(raw, limits, RAGQueryPlanResult.Validate)
}

func uniquePlanTexts(values []string, required bool, maximumItems, maximumBytes int) bool {
	if len(values) > maximumItems || (required && len(values) == 0) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !boundedText(value, maximumBytes, true) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
