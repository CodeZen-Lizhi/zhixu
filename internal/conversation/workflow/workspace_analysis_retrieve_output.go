package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	workspaceAnalysisMaxRetrieveOutputBytes  = 8 * 1024
	workspaceAnalysisMaxSearchHits           = 5
	workspaceAnalysisMaxSearchDegradations   = 16
	workspaceAnalysisMaxDegradationCodeBytes = 64
	workspaceAnalysisMaxSearchSnippetBytes   = 4 * 1024
)

// WorkspaceAnalysisRetrieveEvidenceOutput 是 retrieve_evidence@1 的可恢复、安全持久输出。
// 它只保存权威调用/回执绑定和无正文 Search 摘要。
type WorkspaceAnalysisRetrieveEvidenceOutput struct {
	SchemaVersion          int                                    `json:"schema_version"`
	GitToolCallID          foundation.ID                          `json:"git_tool_call_id"`
	GitToolReceiptID       foundation.ID                          `json:"git_tool_receipt_id"`
	GitToolReceiptHash     string                                 `json:"git_tool_receipt_hash"`
	PlannerModelRunID      foundation.ID                          `json:"planner_model_run_id"`
	PlannerModelCallID     foundation.ID                          `json:"planner_model_call_id"`
	PlannerModelResultID   foundation.ID                          `json:"planner_model_result_id"`
	PlannerModelResultHash string                                 `json:"planner_model_result_hash"`
	SearchToolCallID       foundation.ID                          `json:"search_tool_call_id"`
	SearchToolReceiptID    foundation.ID                          `json:"search_tool_receipt_id"`
	SearchToolReceiptHash  string                                 `json:"search_tool_receipt_hash"`
	SearchSummary          WorkspaceAnalysisRetrieveSearchSummary `json:"search_summary"`
}

// WorkspaceAnalysisRetrieveSearchSummary 只包含 Run-local 短引用、命中数、实际模式和稳定降级码。
type WorkspaceAnalysisRetrieveSearchSummary struct {
	EffectiveMode    string   `json:"effective_mode"`
	EvidenceRefs     []string `json:"evidence_refs"`
	HitCount         int      `json:"hit_count"`
	DegradationCodes []string `json:"degradation_codes"`
}

type persistedWorkspaceAnalysisRetrieveEvidenceOutput struct {
	SchemaVersion          *int                                     `json:"schema_version"`
	GitToolCallID          *foundation.ID                           `json:"git_tool_call_id"`
	GitToolReceiptID       *foundation.ID                           `json:"git_tool_receipt_id"`
	GitToolReceiptHash     *string                                  `json:"git_tool_receipt_hash"`
	PlannerModelRunID      *foundation.ID                           `json:"planner_model_run_id"`
	PlannerModelCallID     *foundation.ID                           `json:"planner_model_call_id"`
	PlannerModelResultID   *foundation.ID                           `json:"planner_model_result_id"`
	PlannerModelResultHash *string                                  `json:"planner_model_result_hash"`
	SearchToolCallID       *foundation.ID                           `json:"search_tool_call_id"`
	SearchToolReceiptID    *foundation.ID                           `json:"search_tool_receipt_id"`
	SearchToolReceiptHash  *string                                  `json:"search_tool_receipt_hash"`
	SearchSummary          *persistedWorkspaceAnalysisSearchSummary `json:"search_summary"`
}

type persistedWorkspaceAnalysisSearchSummary struct {
	EffectiveMode    *string   `json:"effective_mode"`
	EvidenceRefs     *[]string `json:"evidence_refs"`
	HitCount         *int      `json:"hit_count"`
	DegradationCodes *[]string `json:"degradation_codes"`
}

type persistedWorkspaceAnalysisSearchToolOutput struct {
	EffectiveMode *string                                           `json:"effective_mode"`
	Items         *[]persistedWorkspaceAnalysisSearchToolOutputItem `json:"items"`
	Degradations  *[]string                                         `json:"degradations"`
}

type persistedWorkspaceAnalysisSearchToolOutputItem struct {
	EvidenceRef *string `json:"evidence_ref"`
	Rank        *int    `json:"rank"`
	Snippet     *string `json:"snippet"`
}

// EncodeWorkspaceAnalysisRetrieveEvidenceOutput 校验并编码稳定的 retrieve_evidence@1 文档。
func EncodeWorkspaceAnalysisRetrieveEvidenceOutput(output WorkspaceAnalysisRetrieveEvidenceOutput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisRetrieveEvidenceOutput(output); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > workspaceAnalysisMaxRetrieveOutputBytes {
		return nil, workspaceAnalysisOutputError(err)
	}
	return encoded, nil
}

// DecodeWorkspaceAnalysisRetrieveEvidenceOutput 严格拒绝 unknown、duplicate、trailing、null 与越界文档。
func DecodeWorkspaceAnalysisRetrieveEvidenceOutput(raw json.RawMessage) (WorkspaceAnalysisRetrieveEvidenceOutput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxRetrieveOutputBytes
	limits.MaxDepth = 3
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = workspaceAnalysisMaxSearchDegradations
	limits.MaxObjectFields = 12
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisRetrieveEvidenceOutput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.GitToolCallID == nil || persisted.GitToolReceiptID == nil ||
		persisted.GitToolReceiptHash == nil || persisted.PlannerModelRunID == nil || persisted.PlannerModelCallID == nil ||
		persisted.PlannerModelResultID == nil || persisted.PlannerModelResultHash == nil || persisted.SearchToolCallID == nil ||
		persisted.SearchToolReceiptID == nil || persisted.SearchToolReceiptHash == nil || persisted.SearchSummary == nil {
		return WorkspaceAnalysisRetrieveEvidenceOutput{}, workspaceAnalysisOutputError(err)
	}
	searchSummary, err := workspaceAnalysisSearchSummaryFromPersisted(*persisted.SearchSummary)
	if err != nil {
		return WorkspaceAnalysisRetrieveEvidenceOutput{}, err
	}
	output := WorkspaceAnalysisRetrieveEvidenceOutput{
		SchemaVersion:          *persisted.SchemaVersion,
		GitToolCallID:          *persisted.GitToolCallID,
		GitToolReceiptID:       *persisted.GitToolReceiptID,
		GitToolReceiptHash:     *persisted.GitToolReceiptHash,
		PlannerModelRunID:      *persisted.PlannerModelRunID,
		PlannerModelCallID:     *persisted.PlannerModelCallID,
		PlannerModelResultID:   *persisted.PlannerModelResultID,
		PlannerModelResultHash: *persisted.PlannerModelResultHash,
		SearchToolCallID:       *persisted.SearchToolCallID,
		SearchToolReceiptID:    *persisted.SearchToolReceiptID,
		SearchToolReceiptHash:  *persisted.SearchToolReceiptHash,
		SearchSummary:          searchSummary,
	}
	if err := validateWorkspaceAnalysisRetrieveEvidenceOutput(output); err != nil {
		return WorkspaceAnalysisRetrieveEvidenceOutput{}, err
	}
	return output, nil
}

// DecodeWorkspaceAnalysisRetrieveSearchSummary 严格校验 SearchKnowledge@2 输出并删除 snippet 正文。
func DecodeWorkspaceAnalysisRetrieveSearchSummary(raw json.RawMessage) (WorkspaceAnalysisRetrieveSearchSummary, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = int(toolsdomain.SearchKnowledgeV2ReceiptMaxOutputBytes)
	limits.MaxDepth = 4
	limits.MaxStringBytes = workspaceAnalysisMaxSearchSnippetBytes
	limits.MaxArrayItems = workspaceAnalysisMaxSearchDegradations
	limits.MaxObjectFields = 4
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisSearchToolOutput](raw, limits, nil)
	if err != nil || persisted.EffectiveMode == nil || persisted.Items == nil || persisted.Degradations == nil {
		return WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisOutputError(err)
	}
	items := *persisted.Items
	if len(items) > workspaceAnalysisMaxSearchHits {
		return WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisOutputError(errors.New("workspace analysis search hit count is invalid"))
	}
	references := make([]string, len(items))
	for index, item := range items {
		if item.EvidenceRef == nil || item.Rank == nil || item.Snippet == nil ||
			*item.EvidenceRef != workspaceAnalysisEvidenceRef(index+1) || *item.Rank != index+1 ||
			!validWorkspaceAnalysisSearchSnippet(*item.Snippet) {
			return WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisOutputError(errors.New("workspace analysis search item is invalid"))
		}
		references[index] = *item.EvidenceRef
	}
	summary := WorkspaceAnalysisRetrieveSearchSummary{
		EffectiveMode:    *persisted.EffectiveMode,
		EvidenceRefs:     references,
		HitCount:         len(references),
		DegradationCodes: append([]string{}, (*persisted.Degradations)...),
	}
	if err := validateWorkspaceAnalysisRetrieveSearchSummary(summary); err != nil {
		return WorkspaceAnalysisRetrieveSearchSummary{}, err
	}
	return summary, nil
}

func workspaceAnalysisSearchSummaryFromPersisted(value persistedWorkspaceAnalysisSearchSummary) (WorkspaceAnalysisRetrieveSearchSummary, error) {
	if value.EffectiveMode == nil || value.EvidenceRefs == nil || value.HitCount == nil || value.DegradationCodes == nil {
		return WorkspaceAnalysisRetrieveSearchSummary{}, workspaceAnalysisOutputError(errors.New("workspace analysis search summary is incomplete"))
	}
	summary := WorkspaceAnalysisRetrieveSearchSummary{
		EffectiveMode:    *value.EffectiveMode,
		EvidenceRefs:     append([]string{}, (*value.EvidenceRefs)...),
		HitCount:         *value.HitCount,
		DegradationCodes: append([]string{}, (*value.DegradationCodes)...),
	}
	if err := validateWorkspaceAnalysisRetrieveSearchSummary(summary); err != nil {
		return WorkspaceAnalysisRetrieveSearchSummary{}, err
	}
	return summary, nil
}

func validateWorkspaceAnalysisRetrieveEvidenceOutput(output WorkspaceAnalysisRetrieveEvidenceOutput) error {
	if output.SchemaVersion != WorkspaceAnalysisOutputSchemaVersion || !validHash(output.GitToolReceiptHash) ||
		!validHash(output.PlannerModelResultHash) || !validHash(output.SearchToolReceiptHash) {
		return workspaceAnalysisOutputError(errors.New("workspace analysis retrieve output version or hash is invalid"))
	}
	ids := []foundation.ID{
		output.GitToolCallID, output.GitToolReceiptID,
		output.PlannerModelRunID, output.PlannerModelCallID, output.PlannerModelResultID,
		output.SearchToolCallID, output.SearchToolReceiptID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !validWorkspaceAnalysisOutputID(id) {
			return workspaceAnalysisOutputError(errors.New("workspace analysis retrieve output identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisOutputError(errors.New("workspace analysis retrieve output identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return validateWorkspaceAnalysisRetrieveSearchSummary(output.SearchSummary)
}

func validateWorkspaceAnalysisRetrieveSearchSummary(summary WorkspaceAnalysisRetrieveSearchSummary) error {
	if !validWorkspaceAnalysisSearchMode(summary.EffectiveMode) || summary.EvidenceRefs == nil ||
		summary.HitCount < 0 || summary.HitCount > workspaceAnalysisMaxSearchHits ||
		len(summary.EvidenceRefs) != summary.HitCount || summary.DegradationCodes == nil ||
		len(summary.DegradationCodes) > workspaceAnalysisMaxSearchDegradations {
		return workspaceAnalysisOutputError(errors.New("workspace analysis search summary is invalid"))
	}
	for index, reference := range summary.EvidenceRefs {
		if reference != workspaceAnalysisEvidenceRef(index+1) {
			return workspaceAnalysisOutputError(errors.New("workspace analysis search references are invalid"))
		}
	}
	previous := ""
	for index, code := range summary.DegradationCodes {
		if !validWorkspaceAnalysisDegradationCode(code) || index > 0 && code <= previous {
			return workspaceAnalysisOutputError(errors.New("workspace analysis search degradations are invalid"))
		}
		previous = code
	}
	return nil
}

func validWorkspaceAnalysisSearchMode(value string) bool {
	return value == "keyword" || value == "semantic" || value == "hybrid"
}

func validWorkspaceAnalysisDegradationCode(value string) bool {
	if len(value) == 0 || len(value) > workspaceAnalysisMaxDegradationCodeBytes || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range []byte(value[1:]) {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func validWorkspaceAnalysisSearchSnippet(value string) bool {
	return len(value) >= 1 && len(value) <= workspaceAnalysisMaxSearchSnippetBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && strings.TrimSpace(value) != ""
}

func workspaceAnalysisEvidenceRef(ordinal int) string {
	return string([]byte{'E', byte('0' + ordinal)})
}
