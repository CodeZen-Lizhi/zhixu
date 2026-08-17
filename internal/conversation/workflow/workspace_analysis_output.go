package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	workspaceAnalysisMaxInspectOutputBytes = 8 * 1024
	workspaceAnalysisMaxGitChanges         = 100_000
)

// WorkspaceAnalysisInspectOutput 是 inspect_workspace@1 的可恢复、安全持久输出。
type WorkspaceAnalysisInspectOutput struct {
	SchemaVersion   int                               `json:"schema_version"`
	ToolCallID      foundation.ID                     `json:"tool_call_id"`
	ToolReceiptID   foundation.ID                     `json:"tool_receipt_id"`
	ToolReceiptHash string                            `json:"tool_receipt_hash"`
	GitStatus       WorkspaceAnalysisGitStatusSummary `json:"git_status"`
}

// WorkspaceAnalysisGitStatusSummary 只包含不带路径、diff 或仓库根的 Git 聚合。
type WorkspaceAnalysisGitStatusSummary struct {
	Branch         string `json:"branch"`
	Clean          bool   `json:"clean"`
	ConflictCount  int    `json:"conflict_count"`
	Head           string `json:"head"`
	ObjectFormat   string `json:"object_format"`
	StagedCount    int    `json:"staged_count"`
	UnstagedCount  int    `json:"unstaged_count"`
	UntrackedCount int    `json:"untracked_count"`
}

type persistedWorkspaceAnalysisInspectOutput struct {
	SchemaVersion   *int                                        `json:"schema_version"`
	ToolCallID      *foundation.ID                              `json:"tool_call_id"`
	ToolReceiptID   *foundation.ID                              `json:"tool_receipt_id"`
	ToolReceiptHash *string                                     `json:"tool_receipt_hash"`
	GitStatus       *persistedWorkspaceAnalysisGitStatusSummary `json:"git_status"`
}

type persistedWorkspaceAnalysisGitStatusSummary struct {
	Branch         *string `json:"branch"`
	Clean          *bool   `json:"clean"`
	ConflictCount  *int    `json:"conflict_count"`
	Head           *string `json:"head"`
	ObjectFormat   *string `json:"object_format"`
	StagedCount    *int    `json:"staged_count"`
	UnstagedCount  *int    `json:"unstaged_count"`
	UntrackedCount *int    `json:"untracked_count"`
}

// EncodeWorkspaceAnalysisInspectOutput 校验并编码稳定的 inspect_workspace@1 文档。
func EncodeWorkspaceAnalysisInspectOutput(output WorkspaceAnalysisInspectOutput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisInspectOutput(output); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > workspaceAnalysisMaxInspectOutputBytes {
		return nil, workspaceAnalysisOutputError(err)
	}
	return encoded, nil
}

// DecodeWorkspaceAnalysisInspectOutput 严格拒绝 unknown、duplicate、trailing 与 null。
func DecodeWorkspaceAnalysisInspectOutput(raw json.RawMessage) (WorkspaceAnalysisInspectOutput, error) {
	persisted, err := decodeWorkspaceAnalysisInspectDocument[persistedWorkspaceAnalysisInspectOutput](raw, 8)
	if err != nil || persisted.SchemaVersion == nil || persisted.ToolCallID == nil || persisted.ToolReceiptID == nil ||
		persisted.ToolReceiptHash == nil || persisted.GitStatus == nil {
		return WorkspaceAnalysisInspectOutput{}, workspaceAnalysisOutputError(err)
	}
	summary, err := workspaceAnalysisGitStatusFromPersisted(*persisted.GitStatus)
	if err != nil {
		return WorkspaceAnalysisInspectOutput{}, err
	}
	output := WorkspaceAnalysisInspectOutput{
		SchemaVersion: *persisted.SchemaVersion, ToolCallID: *persisted.ToolCallID,
		ToolReceiptID: *persisted.ToolReceiptID, ToolReceiptHash: *persisted.ToolReceiptHash,
		GitStatus: summary,
	}
	if err := validateWorkspaceAnalysisInspectOutput(output); err != nil {
		return WorkspaceAnalysisInspectOutput{}, err
	}
	return output, nil
}

// DecodeWorkspaceAnalysisGitStatusSummary 把已验证的 ReadGitStatus@2 输出收窄为节点安全投影。
func DecodeWorkspaceAnalysisGitStatusSummary(raw json.RawMessage) (WorkspaceAnalysisGitStatusSummary, error) {
	persisted, err := decodeWorkspaceAnalysisInspectDocument[persistedWorkspaceAnalysisGitStatusSummary](raw, 8)
	if err != nil {
		return WorkspaceAnalysisGitStatusSummary{}, workspaceAnalysisOutputError(err)
	}
	return workspaceAnalysisGitStatusFromPersisted(persisted)
}

func decodeWorkspaceAnalysisInspectDocument[T any](raw json.RawMessage, maxFields int) (T, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxInspectOutputBytes
	limits.MaxDepth = 3
	limits.MaxStringBytes = workspaceAnalysisMaxInspectOutputBytes
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = maxFields
	return foundationstrictjson.DecodeObject[T](raw, limits, nil)
}

func workspaceAnalysisGitStatusFromPersisted(value persistedWorkspaceAnalysisGitStatusSummary) (WorkspaceAnalysisGitStatusSummary, error) {
	if value.Branch == nil || value.Clean == nil || value.ConflictCount == nil || value.Head == nil ||
		value.ObjectFormat == nil || value.StagedCount == nil || value.UnstagedCount == nil || value.UntrackedCount == nil {
		return WorkspaceAnalysisGitStatusSummary{}, workspaceAnalysisOutputError(errors.New("workspace analysis git status summary is incomplete"))
	}
	summary := WorkspaceAnalysisGitStatusSummary{
		Branch: *value.Branch, Clean: *value.Clean, ConflictCount: *value.ConflictCount,
		Head: *value.Head, ObjectFormat: *value.ObjectFormat, StagedCount: *value.StagedCount,
		UnstagedCount: *value.UnstagedCount, UntrackedCount: *value.UntrackedCount,
	}
	if err := validateWorkspaceAnalysisGitStatusSummary(summary); err != nil {
		return WorkspaceAnalysisGitStatusSummary{}, err
	}
	return summary, nil
}

func validateWorkspaceAnalysisInspectOutput(output WorkspaceAnalysisInspectOutput) error {
	if output.SchemaVersion != WorkspaceAnalysisOutputSchemaVersion || !validHash(output.ToolReceiptHash) ||
		!validWorkspaceAnalysisOutputID(output.ToolCallID) || !validWorkspaceAnalysisOutputID(output.ToolReceiptID) ||
		output.ToolCallID == output.ToolReceiptID {
		return workspaceAnalysisOutputError(errors.New("workspace analysis inspect output binding is invalid"))
	}
	return validateWorkspaceAnalysisGitStatusSummary(output.GitStatus)
}

func validateWorkspaceAnalysisGitStatusSummary(summary WorkspaceAnalysisGitStatusSummary) error {
	if summary.Branch == "" || len(summary.Branch) > 255 || !utf8.ValidString(summary.Branch) ||
		strings.ContainsAny(summary.Branch, "\x00\r\n") ||
		(summary.ObjectFormat != "sha1" && summary.ObjectFormat != "sha256") ||
		!workspaceAnalysisGitOID(summary.Head, summary.ObjectFormat) {
		return workspaceAnalysisOutputError(errors.New("workspace analysis git identity is invalid"))
	}
	counts := []int{summary.StagedCount, summary.UnstagedCount, summary.UntrackedCount, summary.ConflictCount}
	changes := 0
	for _, count := range counts {
		if count < 0 || count > workspaceAnalysisMaxGitChanges {
			return workspaceAnalysisOutputError(errors.New("workspace analysis git count is invalid"))
		}
		changes += count
	}
	if summary.Clean != (changes == 0) {
		return workspaceAnalysisOutputError(errors.New("workspace analysis git clean flag is invalid"))
	}
	return nil
}

func workspaceAnalysisGitOID(value, objectFormat string) bool {
	expected := 40
	if objectFormat == "sha256" {
		expected = 64
	}
	if len(value) != expected {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

func validWorkspaceAnalysisOutputID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func workspaceAnalysisOutputError(cause error) error {
	if cause == nil {
		cause = errors.New("workspace analysis output is incomplete")
	}
	return outputContractError(cause)
}
