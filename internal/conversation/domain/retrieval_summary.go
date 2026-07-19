package domain

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// MaxRetrievalSummaryBytes 是持久化解释摘要允许的最大 canonical JSON 字节数。
	MaxRetrievalSummaryBytes = 16 * 1024
	maxRetrievalRewrites     = 3
	maxRetrievalCandidates   = int(retrievaldomain.MaxSearchLimit) * maxRetrievalRewrites
)

// RetrievalScopeSummary 是刷新后可解释的规范检索范围，不包含 Question 正文。
type RetrievalScopeSummary struct {
	SourceIDs            []foundation.ID `json:"source_ids"`
	SourceVersionIDs     []foundation.ID `json:"source_version_ids"`
	PathPrefixes         []string        `json:"path_prefixes"`
	CapturedAtFrom       *time.Time      `json:"captured_at_from"`
	CapturedAtBefore     *time.Time      `json:"captured_at_before"`
	AllowOriginalSources bool            `json:"allow_original_sources"`
	AllowWeb             bool            `json:"allow_web"`
}

// RetrievalDegradation 记录一次查询未使用完整 Vector 或 Rerank 能力的稳定原因。
type RetrievalDegradation struct {
	Capability retrievaldomain.SearchDegradationCapability `json:"capability"`
	Code       string                                      `json:"code"`
	Retryable  bool                                        `json:"retryable"`
}

// RetrievalSummary 是 Answer 查询和“为什么这样回答”面板的持久权威摘要。
type RetrievalSummary struct {
	Rewrites           []string                   `json:"rewrites"`
	RequestedMode      retrievaldomain.SearchMode `json:"requested_mode"`
	EffectiveMode      retrievaldomain.SearchMode `json:"effective_mode"`
	Scope              RetrievalScopeSummary      `json:"scope"`
	IndexVersionID     *foundation.ID             `json:"index_version_id"`
	EmbeddingVersionID *foundation.ID             `json:"embedding_version_id"`
	CandidateCount     int                        `json:"candidate_count"`
	SelectedCount      int                        `json:"selected_count"`
	ConflictCount      int                        `json:"conflict_count"`
	Degradations       []RetrievalDegradation     `json:"degradations"`
}

type retrievalScopeSummaryPersistenceDocument struct {
	SourceIDs            *[]foundation.ID `json:"source_ids"`
	SourceVersionIDs     *[]foundation.ID `json:"source_version_ids"`
	PathPrefixes         *[]string        `json:"path_prefixes"`
	CapturedAtFrom       json.RawMessage  `json:"captured_at_from"`
	CapturedAtBefore     json.RawMessage  `json:"captured_at_before"`
	AllowOriginalSources *bool            `json:"allow_original_sources"`
	AllowWeb             *bool            `json:"allow_web"`
}

type retrievalDegradationPersistenceDocument struct {
	Capability *retrievaldomain.SearchDegradationCapability `json:"capability"`
	Code       *string                                      `json:"code"`
	Retryable  *bool                                        `json:"retryable"`
}

type retrievalSummaryPersistenceDocument struct {
	Rewrites           *[]string                                  `json:"rewrites"`
	RequestedMode      *retrievaldomain.SearchMode                `json:"requested_mode"`
	EffectiveMode      *retrievaldomain.SearchMode                `json:"effective_mode"`
	Scope              *retrievalScopeSummaryPersistenceDocument  `json:"scope"`
	IndexVersionID     json.RawMessage                            `json:"index_version_id"`
	EmbeddingVersionID json.RawMessage                            `json:"embedding_version_id"`
	CandidateCount     *int                                       `json:"candidate_count"`
	SelectedCount      *int                                       `json:"selected_count"`
	ConflictCount      *int                                       `json:"conflict_count"`
	Degradations       *[]retrievalDegradationPersistenceDocument `json:"degradations"`
}

// DecodeRetrievalSummary 严格解析持久化 JSON，并返回规范化的检索摘要副本。
func DecodeRetrievalSummary(workspaceID foundation.ID, raw json.RawMessage) (RetrievalSummary, error) {
	parsedWorkspaceID, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsedWorkspaceID != workspaceID {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary workspace is invalid", err)
	}
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = MaxRetrievalSummaryBytes
	limits.MaxStringBytes = MaxRetrievalSummaryBytes
	limits.MaxArrayItems = retrievaldomain.MaxSearchFilterValues
	limits.MaxObjectFields = 10
	persisted, err := foundationstrictjson.DecodeObject[retrievalSummaryPersistenceDocument](raw, limits, nil)
	if err != nil || persisted.Rewrites == nil || persisted.RequestedMode == nil || persisted.EffectiveMode == nil ||
		persisted.Scope == nil || persisted.CandidateCount == nil || persisted.SelectedCount == nil ||
		persisted.ConflictCount == nil || persisted.Degradations == nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary document is invalid", err)
	}
	scope := persisted.Scope
	if scope.SourceIDs == nil || scope.SourceVersionIDs == nil || scope.PathPrefixes == nil ||
		scope.AllowOriginalSources == nil || scope.AllowWeb == nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary scope document is invalid", nil)
	}
	fromText, err := decodeNullableJSONString(scope.CapturedAtFrom)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary scope start time is invalid", err)
	}
	from, err := parseQuestionScopeTime(fromText)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary scope start time is invalid", err)
	}
	beforeText, err := decodeNullableJSONString(scope.CapturedAtBefore)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary scope end time is invalid", err)
	}
	before, err := parseQuestionScopeTime(beforeText)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary scope end time is invalid", err)
	}
	indexVersionID, err := decodeNullableFoundationID(persisted.IndexVersionID)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary index version is invalid", err)
	}
	embeddingVersionID, err := decodeNullableFoundationID(persisted.EmbeddingVersionID)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary embedding version is invalid", err)
	}
	degradations := make([]RetrievalDegradation, len(*persisted.Degradations))
	for index, degradation := range *persisted.Degradations {
		if degradation.Capability == nil || degradation.Code == nil || degradation.Retryable == nil {
			return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary degradation document is invalid", nil)
		}
		degradations[index] = RetrievalDegradation{
			Capability: *degradation.Capability,
			Code:       *degradation.Code,
			Retryable:  *degradation.Retryable,
		}
	}
	decoded := RetrievalSummary{
		Rewrites:      append([]string{}, (*persisted.Rewrites)...),
		RequestedMode: *persisted.RequestedMode,
		EffectiveMode: *persisted.EffectiveMode,
		Scope: RetrievalScopeSummary{
			SourceIDs:        append([]foundation.ID{}, (*scope.SourceIDs)...),
			SourceVersionIDs: append([]foundation.ID{}, (*scope.SourceVersionIDs)...),
			PathPrefixes:     append([]string{}, (*scope.PathPrefixes)...),
			CapturedAtFrom:   from, CapturedAtBefore: before,
			AllowOriginalSources: *scope.AllowOriginalSources, AllowWeb: *scope.AllowWeb,
		},
		IndexVersionID: indexVersionID, EmbeddingVersionID: embeddingVersionID,
		CandidateCount: *persisted.CandidateCount, SelectedCount: *persisted.SelectedCount,
		ConflictCount: *persisted.ConflictCount, Degradations: degradations,
	}
	canonical, err := CanonicalizeRetrievalSummary(workspaceID, decoded)
	if err != nil {
		return RetrievalSummary{}, err
	}
	return canonical, nil
}

func decodeNullableFoundationID(raw json.RawMessage) (*foundation.ID, error) {
	value, err := decodeNullableJSONString(raw)
	if err != nil || value == nil {
		return nil, err
	}
	id := foundation.ID(*value)
	return &id, nil
}

// CanonicalizeRetrievalSummary 校验、复制并规范化范围和降级顺序。
func CanonicalizeRetrievalSummary(workspaceID foundation.ID, summary RetrievalSummary) (RetrievalSummary, error) {
	parsedWorkspaceID, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsedWorkspaceID != workspaceID || summary.Rewrites == nil || summary.Degradations == nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary identity or required lists are invalid", err)
	}
	if len(summary.Rewrites) > maxRetrievalRewrites {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary contains too many rewrites", nil)
	}
	rewrites := make([]string, len(summary.Rewrites))
	seenRewrites := make(map[string]struct{}, len(summary.Rewrites))
	for index, rewrite := range summary.Rewrites {
		if !validBoundedText(rewrite, MaxQuestionBytes, true) {
			return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary rewrite is invalid", nil)
		}
		if _, duplicate := seenRewrites[rewrite]; duplicate {
			return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary rewrite is duplicated", nil)
		}
		seenRewrites[rewrite] = struct{}{}
		rewrites[index] = rewrite
	}
	canonicalRequested, err := retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID, Query: "retrieval-summary", Mode: summary.RequestedMode, Limit: 1,
		Filter: retrievaldomain.SearchFilter{
			SourceIDs: summary.Scope.SourceIDs, SourceVersionIDs: summary.Scope.SourceVersionIDs,
			PathPrefixes: summary.Scope.PathPrefixes, CapturedAtFrom: summary.Scope.CapturedAtFrom,
			CapturedAtBefore: summary.Scope.CapturedAtBefore,
		},
	})
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary requested scope is invalid", err)
	}
	if _, err := retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID, Query: "retrieval-summary", Mode: summary.EffectiveMode, Limit: 1,
	}); err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary effective mode is invalid", err)
	}
	indexVersionID, err := canonicalOptionalID(summary.IndexVersionID)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary index version is invalid", err)
	}
	embeddingVersionID, err := canonicalOptionalID(summary.EmbeddingVersionID)
	if err != nil || (embeddingVersionID != nil && (indexVersionID == nil || *embeddingVersionID == *indexVersionID)) {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary embedding version is invalid", err)
	}
	if summary.CandidateCount < 0 || summary.CandidateCount > maxRetrievalCandidates ||
		summary.SelectedCount < 0 || summary.SelectedCount > int(retrievaldomain.MaxSearchLimit) ||
		summary.SelectedCount > summary.CandidateCount || summary.ConflictCount < 0 || summary.ConflictCount > summary.CandidateCount {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary counts are invalid", nil)
	}
	degradations := make([]retrievaldomain.SearchDegradation, len(summary.Degradations))
	for index, degradation := range summary.Degradations {
		degradations[index] = retrievaldomain.SearchDegradation{
			Capability: degradation.Capability, Code: degradation.Code, Retryable: degradation.Retryable,
		}
	}
	normalized, err := retrievaldomain.NormalizeSearchDegradations(degradations)
	if err != nil {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary degradation is invalid", err)
	}
	canonicalDegradations := make([]RetrievalDegradation, len(normalized))
	for index, degradation := range normalized {
		canonicalDegradations[index] = RetrievalDegradation{
			Capability: degradation.Capability, Code: degradation.Code, Retryable: degradation.Retryable,
		}
	}
	canonical := RetrievalSummary{
		Rewrites: rewrites, RequestedMode: canonicalRequested.Mode, EffectiveMode: summary.EffectiveMode,
		Scope: RetrievalScopeSummary{
			SourceIDs: canonicalRequested.Filter.SourceIDs, SourceVersionIDs: canonicalRequested.Filter.SourceVersionIDs,
			PathPrefixes: canonicalRequested.Filter.PathPrefixes, CapturedAtFrom: canonicalRequested.Filter.CapturedAtFrom,
			CapturedAtBefore:     canonicalRequested.Filter.CapturedAtBefore,
			AllowOriginalSources: summary.Scope.AllowOriginalSources, AllowWeb: summary.Scope.AllowWeb,
		},
		IndexVersionID: indexVersionID, EmbeddingVersionID: embeddingVersionID,
		CandidateCount: summary.CandidateCount, SelectedCount: summary.SelectedCount, ConflictCount: summary.ConflictCount,
		Degradations: canonicalDegradations,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil || len(encoded) > MaxRetrievalSummaryBytes {
		return RetrievalSummary{}, invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary is oversized or cannot be encoded", err)
	}
	return canonical, nil
}

// ValidateRetrievalSummary 校验摘要已经规范，并满足对应 Answer 终态的检索事实。
func ValidateRetrievalSummary(workspaceID foundation.ID, status AnswerPublicationStatus, summary RetrievalSummary) error {
	canonical, err := CanonicalizeRetrievalSummary(workspaceID, summary)
	if err != nil || !reflect.DeepEqual(canonical, summary) {
		return invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary is not canonical", err)
	}
	switch status {
	case AnswerPublicationCompleted:
		if err := validateRetrievalModeOutcome(summary); err != nil {
			return err
		}
		if len(summary.Rewrites) == 0 || summary.IndexVersionID == nil || summary.SelectedCount == 0 ||
			(summary.EffectiveMode != retrievaldomain.SearchModeKeyword && summary.EmbeddingVersionID == nil) {
			return invalid(ErrorCodeRetrievalSummaryInvalid, "completed answer requires selected retrieval evidence", nil)
		}
	case AnswerPublicationRefused:
		// Refusal 可以发生在实际检索前，也可以保存零命中的完整检索摘要。
		if hasRetrievalAttempt(summary) {
			if err := validateRetrievalModeOutcome(summary); err != nil {
				return err
			}
		}
	case AnswerPublicationClarificationRequired:
		if len(summary.Rewrites) != 0 || summary.RequestedMode != summary.EffectiveMode || summary.IndexVersionID != nil ||
			summary.EmbeddingVersionID != nil || summary.CandidateCount != 0 || summary.SelectedCount != 0 ||
			summary.ConflictCount != 0 || len(summary.Degradations) != 0 {
			return invalid(ErrorCodeRetrievalSummaryInvalid, "clarification cannot claim retrieval work", nil)
		}
	default:
		return invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary requires a published answer status", nil)
	}
	return nil
}

func validateRetrievalModeOutcome(summary RetrievalSummary) error {
	if err := retrievaldomain.ValidateSearchModeOutcome(
		summary.RequestedMode,
		summary.EffectiveMode,
		summary.EmbeddingVersionID != nil,
		toSearchDegradations(summary.Degradations),
	); err != nil {
		return invalid(ErrorCodeRetrievalSummaryInvalid, "retrieval summary mode outcome is invalid", err)
	}
	return nil
}

func hasRetrievalAttempt(summary RetrievalSummary) bool {
	return len(summary.Rewrites) != 0 || summary.IndexVersionID != nil || summary.EmbeddingVersionID != nil ||
		summary.CandidateCount != 0 || summary.SelectedCount != 0 || summary.ConflictCount != 0 || len(summary.Degradations) != 0
}

func toSearchDegradations(values []RetrievalDegradation) []retrievaldomain.SearchDegradation {
	result := make([]retrievaldomain.SearchDegradation, len(values))
	for index, value := range values {
		result[index] = retrievaldomain.SearchDegradation{
			Capability: value.Capability,
			Code:       value.Code,
			Retryable:  value.Retryable,
		}
	}
	return result
}

func canonicalOptionalID(value *foundation.ID) (*foundation.ID, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := foundation.ParseID(string(*value))
	if err != nil || parsed != *value {
		return nil, err
	}
	return &parsed, nil
}
