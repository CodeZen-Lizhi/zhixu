package domain

import (
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// RAGQueryPlanSchemaID 是检索改写与澄清判断的稳定 Schema ID。
	RAGQueryPlanSchemaID = "agent.rag-query-plan"
	// ResultTypeRAGQueryPlan 是 Query Plan 的稳定结果类型。
	ResultTypeRAGQueryPlan = "rag_query_plan"

	maxQueryRewrites         = 3
	maxQueryRewriteBytes     = 8 * 1024
	maxSuggestedPlanScopes   = 10
	maxProviderIntentBytes   = 512
	maxProviderRewriteBytes  = 512
	maxProviderReasonBytes   = 512
	maxProviderQuestionBytes = 1024
	maxProviderScopeBytes    = 256
)

// RAGQueryPlanProviderResultV2 是 Provider 可见的无身份 Query Plan wire。
//
// 它刻意不包含 result/schema/model_run_ref envelope，也不重复输出
// requires_clarification；服务端依据 rewrites 是否为空恢复该语义并注入身份。
// 历史持久化结果仍使用 RAGQueryPlanResult v1。
type RAGQueryPlanProviderResultV2 struct {
	Intent                string   `json:"i"`
	Rewrites              []string `json:"r"`
	ClarificationReason   string   `json:"d"`
	ClarificationQuestion string   `json:"q"`
	SuggestedScopes       []string `json:"s"`
}

type ragQueryPlanProviderDocumentV2 struct {
	Intent                *string   `json:"i"`
	Rewrites              *[]string `json:"r"`
	ClarificationReason   *string   `json:"d"`
	ClarificationQuestion *string   `json:"q"`
	SuggestedScopes       *[]string `json:"s"`
}

func (document ragQueryPlanProviderDocumentV2) Validate() error {
	if document.Intent == nil || document.Rewrites == nil || document.ClarificationReason == nil ||
		document.ClarificationQuestion == nil || document.SuggestedScopes == nil {
		return invalid(ErrorCodeQueryPlanInvalid, "query plan provider document is incomplete")
	}
	return RAGQueryPlanProviderResultV2{
		Intent: *document.Intent, Rewrites: *document.Rewrites,
		ClarificationReason: *document.ClarificationReason, ClarificationQuestion: *document.ClarificationQuestion,
		SuggestedScopes: *document.SuggestedScopes,
	}.Validate()
}

// Validate 校验无身份 wire 的必填字段、分支判定与文本边界。
func (result RAGQueryPlanProviderResultV2) Validate() error {
	if !boundedText(result.Intent, maxProviderIntentBytes, true) || result.Rewrites == nil || result.SuggestedScopes == nil {
		return invalid(ErrorCodeQueryPlanInvalid, "query plan provider intent or required lists are invalid")
	}
	if len(result.Rewrites) == 0 {
		if !boundedText(result.ClarificationReason, maxProviderReasonBytes, false) ||
			!boundedText(result.ClarificationQuestion, maxProviderQuestionBytes, false) ||
			(result.ClarificationReason == "") != (result.ClarificationQuestion == "") ||
			!uniquePlanTexts(result.SuggestedScopes, false, maxSuggestedPlanScopes, maxProviderScopeBytes) {
			return invalid(ErrorCodeQueryPlanInvalid, "query plan provider clarification is invalid")
		}
	} else if !uniquePlanTexts(result.Rewrites, true, maxQueryRewrites, maxProviderRewriteBytes) ||
		!boundedText(result.ClarificationReason, maxProviderReasonBytes, false) ||
		!boundedText(result.ClarificationQuestion, maxProviderQuestionBytes, false) ||
		!uniquePlanTexts(result.SuggestedScopes, false, maxSuggestedPlanScopes, maxProviderScopeBytes) {
		return invalid(ErrorCodeQueryPlanInvalid, "query plan provider retrieval is invalid")
	}
	return nil
}

// ValidateRAGQueryPlanSourceQuery 校验服务端从冻结输入读取的原始用户问题。
func ValidateRAGQueryPlanSourceQuery(query string) error {
	if !boundedText(query, maxQueryRewriteBytes, true) {
		return invalid(ErrorCodeQueryPlanInvalid, "query plan source query is invalid")
	}
	return nil
}

// Compose 注入服务端 ModelRun 身份，并以原始问题锚定检索分支。
func (result RAGQueryPlanProviderResultV2) Compose(modelRunRef foundation.ID, sourceQuery string) (RAGQueryPlanResult, error) {
	if !canonicalID(modelRunRef) {
		return RAGQueryPlanResult{}, invalid(ErrorCodeSchemaInvalid, "query plan model run reference is invalid")
	}
	if err := result.Validate(); err != nil {
		return RAGQueryPlanResult{}, err
	}
	if err := ValidateRAGQueryPlanSourceQuery(sourceQuery); err != nil {
		return RAGQueryPlanResult{}, err
	}
	requiresClarification := len(result.Rewrites) == 0 && containsQueryPlanLetter(result.ClarificationReason) && containsQueryPlanLetter(result.ClarificationQuestion)
	rewrites := []string{}
	clarificationReason, clarificationQuestion := "", ""
	suggestedScopes := []string{}
	if requiresClarification {
		clarificationReason = result.ClarificationReason
		clarificationQuestion = result.ClarificationQuestion
		suggestedScopes = cloneQueryPlanStrings(result.SuggestedScopes)
	} else {
		rewrites = anchoredQueryPlanRewrites(sourceQuery, result.Rewrites)
	}
	composed := RAGQueryPlanResult{
		ResultType:    ResultTypeRAGQueryPlan,
		SchemaID:      RAGQueryPlanSchemaID,
		SchemaVersion: OutputSchemaVersionV1,
		ModelRunRef:   modelRunRef,
		Payload: RAGQueryPlanPayload{
			Intent:                result.Intent,
			RequiresClarification: requiresClarification,
			Rewrites:              rewrites,
			ClarificationReason:   clarificationReason,
			ClarificationQuestion: clarificationQuestion,
			SuggestedScopes:       suggestedScopes,
		},
	}
	if err := composed.Validate(); err != nil {
		return RAGQueryPlanResult{}, err
	}
	return composed, nil
}

func anchoredQueryPlanRewrites(sourceQuery string, providerRewrites []string) []string {
	rewrites := make([]string, 0, maxQueryRewrites)
	rewrites = append(rewrites, sourceQuery)
	for _, rewrite := range providerRewrites {
		if len(rewrites) == maxQueryRewrites || rewrite == sourceQuery || !containsQueryPlanLetter(rewrite) {
			continue
		}
		rewrites = append(rewrites, rewrite)
	}
	return rewrites
}

func containsQueryPlanLetter(value string) bool {
	for _, character := range value {
		if unicode.IsLetter(character) {
			return true
		}
	}
	return false
}

func cloneQueryPlanStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

// DecodeRAGQueryPlanProviderV2 严格解析 Provider 无身份 wire v2。
func DecodeRAGQueryPlanProviderV2(raw []byte, limits DecodeLimits) (RAGQueryPlanProviderResultV2, error) {
	document, err := DecodeStrict(raw, limits, ragQueryPlanProviderDocumentV2.Validate)
	if err != nil {
		return RAGQueryPlanProviderResultV2{}, err
	}
	return RAGQueryPlanProviderResultV2{
		Intent: *document.Intent, Rewrites: cloneQueryPlanStrings(*document.Rewrites),
		ClarificationReason: *document.ClarificationReason, ClarificationQuestion: *document.ClarificationQuestion,
		SuggestedScopes: cloneQueryPlanStrings(*document.SuggestedScopes),
	}, nil
}

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
