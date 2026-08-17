package workflow

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	workspaceAnalysisMaxReadEvidenceOutputBytes = 4 * 1024
	workspaceAnalysisMaxEvidenceReads           = 3
)

// WorkspaceAnalysisReadEvidenceOutput 是 read_evidence@1 的可恢复、安全持久输出。
// 它只保存按 E1..E3 排序的 Tool 调用与 canonical 回执绑定，不保存 Source 正文或私有身份。
type WorkspaceAnalysisReadEvidenceOutput struct {
	SchemaVersion int                                    `json:"schema_version"`
	Reads         []WorkspaceAnalysisReadEvidenceReceipt `json:"reads"`
}

// WorkspaceAnalysisReadEvidenceReceipt 是一次 ReadSource@3 的安全回执引用。
type WorkspaceAnalysisReadEvidenceReceipt struct {
	EvidenceRef     string        `json:"evidence_ref"`
	ToolCallID      foundation.ID `json:"tool_call_id"`
	ToolReceiptID   foundation.ID `json:"tool_receipt_id"`
	ToolReceiptHash string        `json:"tool_receipt_hash"`
}

// String 不输出短引用之外的 Tool 身份、回执 hash 或未来可能增加的私有字段。
func (receipt WorkspaceAnalysisReadEvidenceReceipt) String() string {
	return "WorkspaceAnalysisReadEvidenceReceipt{redacted}"
}

// GoString 避免 %#v 调试格式绕过安全 String 投影。
func (receipt WorkspaceAnalysisReadEvidenceReceipt) GoString() string { return receipt.String() }

type persistedWorkspaceAnalysisReadEvidenceOutput struct {
	SchemaVersion *int                                             `json:"schema_version"`
	Reads         *[]persistedWorkspaceAnalysisReadEvidenceReceipt `json:"reads"`
}

type persistedWorkspaceAnalysisReadEvidenceReceipt struct {
	EvidenceRef     *string        `json:"evidence_ref"`
	ToolCallID      *foundation.ID `json:"tool_call_id"`
	ToolReceiptID   *foundation.ID `json:"tool_receipt_id"`
	ToolReceiptHash *string        `json:"tool_receipt_hash"`
}

// EncodeWorkspaceAnalysisReadEvidenceOutput 校验并编码稳定的 read_evidence@1 文档。
func EncodeWorkspaceAnalysisReadEvidenceOutput(output WorkspaceAnalysisReadEvidenceOutput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisReadEvidenceOutput(output); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > workspaceAnalysisMaxReadEvidenceOutputBytes {
		return nil, workspaceAnalysisOutputError(err)
	}
	return encoded, nil
}

// DecodeWorkspaceAnalysisReadEvidenceOutput 严格拒绝 unknown、duplicate、trailing、null 与越界文档。
func DecodeWorkspaceAnalysisReadEvidenceOutput(raw json.RawMessage) (WorkspaceAnalysisReadEvidenceOutput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxReadEvidenceOutputBytes
	limits.MaxDepth = 3
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = workspaceAnalysisMaxEvidenceReads
	limits.MaxObjectFields = 4
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisReadEvidenceOutput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.Reads == nil {
		return WorkspaceAnalysisReadEvidenceOutput{}, workspaceAnalysisOutputError(err)
	}

	reads := make([]WorkspaceAnalysisReadEvidenceReceipt, len(*persisted.Reads))
	for index, persistedRead := range *persisted.Reads {
		if persistedRead.EvidenceRef == nil || persistedRead.ToolCallID == nil || persistedRead.ToolReceiptID == nil ||
			persistedRead.ToolReceiptHash == nil {
			return WorkspaceAnalysisReadEvidenceOutput{}, workspaceAnalysisOutputError(
				errors.New("workspace analysis evidence read receipt is incomplete"),
			)
		}
		reads[index] = WorkspaceAnalysisReadEvidenceReceipt{
			EvidenceRef:     *persistedRead.EvidenceRef,
			ToolCallID:      *persistedRead.ToolCallID,
			ToolReceiptID:   *persistedRead.ToolReceiptID,
			ToolReceiptHash: *persistedRead.ToolReceiptHash,
		}
	}

	output := WorkspaceAnalysisReadEvidenceOutput{SchemaVersion: *persisted.SchemaVersion, Reads: reads}
	if err := validateWorkspaceAnalysisReadEvidenceOutput(output); err != nil {
		return WorkspaceAnalysisReadEvidenceOutput{}, err
	}
	return output, nil
}

// String 只返回读取数量，不输出 Tool 身份、回执 hash、Source 正文或私有绑定。
func (output WorkspaceAnalysisReadEvidenceOutput) String() string {
	return "WorkspaceAnalysisReadEvidenceOutput{reads:" + strconv.Itoa(len(output.Reads)) + "}"
}

// GoString 避免 %#v 调试格式绕过安全 String 投影。
func (output WorkspaceAnalysisReadEvidenceOutput) GoString() string { return output.String() }

func validateWorkspaceAnalysisReadEvidenceOutput(output WorkspaceAnalysisReadEvidenceOutput) error {
	if output.SchemaVersion != WorkspaceAnalysisOutputSchemaVersion || len(output.Reads) < 1 ||
		len(output.Reads) > workspaceAnalysisMaxEvidenceReads {
		return workspaceAnalysisOutputError(errors.New("workspace analysis evidence reads are invalid"))
	}

	seen := make(map[foundation.ID]struct{}, len(output.Reads)*2)
	for index, read := range output.Reads {
		if read.EvidenceRef != workspaceAnalysisEvidenceRef(index+1) || !validHash(read.ToolReceiptHash) ||
			!validWorkspaceAnalysisOutputID(read.ToolCallID) || !validWorkspaceAnalysisOutputID(read.ToolReceiptID) ||
			read.ToolCallID == read.ToolReceiptID {
			return workspaceAnalysisOutputError(errors.New("workspace analysis evidence read binding is invalid"))
		}
		for _, id := range []foundation.ID{read.ToolCallID, read.ToolReceiptID} {
			if _, duplicate := seen[id]; duplicate {
				return workspaceAnalysisOutputError(errors.New("workspace analysis evidence read identity is reused"))
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}
