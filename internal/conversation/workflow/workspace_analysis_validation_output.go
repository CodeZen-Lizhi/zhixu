package workflow

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const workspaceAnalysisMaxValidationOutputBytes = 4 * 1024

const (
	// WorkspaceAnalysisCitationValidationReasonOK 表示引用仍可打开且正式知识资格有效。
	WorkspaceAnalysisCitationValidationReasonOK = "OK"
	// WorkspaceAnalysisCitationValidationReasonUnresolvable 表示引用无法从冻结事实中解析或重新打开。
	WorkspaceAnalysisCitationValidationReasonUnresolvable = "CITATION_UNRESOLVABLE"
	// WorkspaceAnalysisCitationValidationReasonEvidenceIneligible 表示引用已不具备正式知识资格。
	WorkspaceAnalysisCitationValidationReasonEvidenceIneligible = "EVIDENCE_INELIGIBLE"
	// WorkspaceAnalysisCitationValidationReasonBindingMismatch 表示引用内容或私有身份绑定发生漂移。
	WorkspaceAnalysisCitationValidationReasonBindingMismatch = "BINDING_MISMATCH"
)

// WorkspaceAnalysisValidationOutput 是 validate_citations@1 的可恢复、安全持久输出。
// 它不保存 Citation 私有身份、Source 正文或候选正文。
type WorkspaceAnalysisValidationOutput struct {
	SchemaVersion             int                                         `json:"schema_version"`
	CandidateID               foundation.ID                               `json:"candidate_id"`
	CandidateHash             string                                      `json:"candidate_hash"`
	ValidationToolCallID      foundation.ID                               `json:"validation_tool_call_id"`
	ValidationToolReceiptID   foundation.ID                               `json:"validation_tool_receipt_id"`
	ValidationToolReceiptHash string                                      `json:"validation_tool_receipt_hash"`
	Results                   []WorkspaceAnalysisCitationValidationResult `json:"results"`
}

// WorkspaceAnalysisCitationValidationResult 是一个候选短引用的无正文校验结果。
type WorkspaceAnalysisCitationValidationResult struct {
	EvidenceRef string `json:"evidence_ref"`
	Valid       bool   `json:"valid"`
	ReasonCode  string `json:"reason_code"`
}

type persistedWorkspaceAnalysisValidationOutput struct {
	SchemaVersion             *int                                                  `json:"schema_version"`
	CandidateID               *foundation.ID                                        `json:"candidate_id"`
	CandidateHash             *string                                               `json:"candidate_hash"`
	ValidationToolCallID      *foundation.ID                                        `json:"validation_tool_call_id"`
	ValidationToolReceiptID   *foundation.ID                                        `json:"validation_tool_receipt_id"`
	ValidationToolReceiptHash *string                                               `json:"validation_tool_receipt_hash"`
	Results                   *[]persistedWorkspaceAnalysisCitationValidationResult `json:"results"`
}

type persistedWorkspaceAnalysisCitationValidationResult struct {
	EvidenceRef *string `json:"evidence_ref"`
	Valid       *bool   `json:"valid"`
	ReasonCode  *string `json:"reason_code"`
}

// EncodeWorkspaceAnalysisValidationOutput 校验并编码稳定的 validate_citations@1 文档。
func EncodeWorkspaceAnalysisValidationOutput(output WorkspaceAnalysisValidationOutput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisValidationOutput(output); err != nil {
		return nil, err
	}
	document, err := json.Marshal(output)
	if err != nil || len(document) > workspaceAnalysisMaxValidationOutputBytes {
		return nil, workspaceAnalysisOutputError(err)
	}
	return document, nil
}

// DecodeWorkspaceAnalysisValidationOutput 严格拒绝 unknown、duplicate、null、trailing 和越界文档。
func DecodeWorkspaceAnalysisValidationOutput(raw json.RawMessage) (WorkspaceAnalysisValidationOutput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxValidationOutputBytes
	limits.MaxDepth = 3
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = workspaceAnalysisMaxEvidenceReads
	limits.MaxObjectFields = 7
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisValidationOutput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.CandidateID == nil || persisted.CandidateHash == nil ||
		persisted.ValidationToolCallID == nil || persisted.ValidationToolReceiptID == nil ||
		persisted.ValidationToolReceiptHash == nil || persisted.Results == nil {
		return WorkspaceAnalysisValidationOutput{}, workspaceAnalysisOutputError(err)
	}

	results := make([]WorkspaceAnalysisCitationValidationResult, len(*persisted.Results))
	for index, persistedResult := range *persisted.Results {
		if persistedResult.EvidenceRef == nil || persistedResult.Valid == nil || persistedResult.ReasonCode == nil {
			return WorkspaceAnalysisValidationOutput{}, workspaceAnalysisOutputError(
				errors.New("workspace analysis citation validation result is incomplete"),
			)
		}
		results[index] = WorkspaceAnalysisCitationValidationResult{
			EvidenceRef: *persistedResult.EvidenceRef,
			Valid:       *persistedResult.Valid,
			ReasonCode:  *persistedResult.ReasonCode,
		}
	}

	output := WorkspaceAnalysisValidationOutput{
		SchemaVersion:             *persisted.SchemaVersion,
		CandidateID:               *persisted.CandidateID,
		CandidateHash:             *persisted.CandidateHash,
		ValidationToolCallID:      *persisted.ValidationToolCallID,
		ValidationToolReceiptID:   *persisted.ValidationToolReceiptID,
		ValidationToolReceiptHash: *persisted.ValidationToolReceiptHash,
		Results:                   results,
	}
	if err := validateWorkspaceAnalysisValidationOutput(output); err != nil {
		return WorkspaceAnalysisValidationOutput{}, err
	}
	return output, nil
}

// String 只输出校验结果数量，不输出 Candidate、Tool 身份或 hash。
func (output WorkspaceAnalysisValidationOutput) String() string {
	return "WorkspaceAnalysisValidationOutput{results:" + strconv.Itoa(len(output.Results)) + "}"
}

// GoString 避免 %#v 展开持久身份。
func (output WorkspaceAnalysisValidationOutput) GoString() string { return output.String() }

// LogValue 只投影固定版本与结果计数，不记录身份、hash 或引用。
func (output WorkspaceAnalysisValidationOutput) LogValue() slog.Value {
	validCount := 0
	for _, result := range output.Results {
		if result.Valid {
			validCount++
		}
	}
	return slog.GroupValue(
		slog.Int("schema_version", output.SchemaVersion),
		slog.Int("valid_count", validCount),
		slog.Int("invalid_count", len(output.Results)-validCount),
	)
}

// String 不输出短引用或校验原因，避免未来日志拼接扩大暴露面。
func (result WorkspaceAnalysisCitationValidationResult) String() string {
	return "WorkspaceAnalysisCitationValidationResult{redacted}"
}

// GoString 避免 %#v 展开短引用或校验原因。
func (result WorkspaceAnalysisCitationValidationResult) GoString() string { return result.String() }

func validateWorkspaceAnalysisValidationOutput(output WorkspaceAnalysisValidationOutput) error {
	if output.SchemaVersion != WorkspaceAnalysisOutputSchemaVersion || !validHash(output.CandidateHash) ||
		!validHash(output.ValidationToolReceiptHash) || len(output.Results) < 1 ||
		len(output.Results) > workspaceAnalysisMaxEvidenceReads {
		return workspaceAnalysisOutputError(errors.New("workspace analysis validation output version, hash, or result count is invalid"))
	}

	ids := []foundation.ID{output.CandidateID, output.ValidationToolCallID, output.ValidationToolReceiptID}
	seenIDs := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !validWorkspaceAnalysisOutputID(id) {
			return workspaceAnalysisOutputError(errors.New("workspace analysis validation output identity is invalid"))
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return workspaceAnalysisOutputError(errors.New("workspace analysis validation output identity is reused"))
		}
		seenIDs[id] = struct{}{}
	}

	seenRefs := make(map[string]struct{}, len(output.Results))
	for _, result := range output.Results {
		if !validWorkspaceAnalysisValidationEvidenceRef(result.EvidenceRef) ||
			!validWorkspaceAnalysisCitationValidationResult(result) {
			return workspaceAnalysisOutputError(errors.New("workspace analysis citation validation result is invalid"))
		}
		if _, duplicate := seenRefs[result.EvidenceRef]; duplicate {
			return workspaceAnalysisOutputError(errors.New("workspace analysis citation validation reference is reused"))
		}
		seenRefs[result.EvidenceRef] = struct{}{}
	}
	return nil
}

func validWorkspaceAnalysisValidationEvidenceRef(value string) bool {
	return value == "E1" || value == "E2" || value == "E3"
}

func validWorkspaceAnalysisCitationValidationResult(result WorkspaceAnalysisCitationValidationResult) bool {
	switch result.ReasonCode {
	case WorkspaceAnalysisCitationValidationReasonOK:
		return result.Valid
	case WorkspaceAnalysisCitationValidationReasonUnresolvable,
		WorkspaceAnalysisCitationValidationReasonEvidenceIneligible,
		WorkspaceAnalysisCitationValidationReasonBindingMismatch:
		return !result.Valid
	default:
		return false
	}
}
