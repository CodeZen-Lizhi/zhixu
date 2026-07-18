// Package contract 定义 Safe Writeback 与 Retrieval Consumer 共用的重索引消息契约。
package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// EventTypeReindexRequested 是 Safe Writeback 发布重索引请求的稳定 Outbox 类型。
	EventTypeReindexRequested = "retrieval.revision.reindex_requested"
	// SchemaVersionV1 是 retrieval.revision.reindex_requested 的首个固定契约版本。
	SchemaVersionV1 = 1
	// ErrorCodeInvalidContract 是重索引消息格式或绑定不合法时的稳定错误码。
	ErrorCodeInvalidContract = "REINDEX_REQUEST_CONTRACT_INVALID"
)

// RequestV1 是 Safe Writeback 发布并由 Retrieval 消费的固定 11 字段消息。
type RequestV1 struct {
	SchemaVersion        int           `json:"schema_version"`
	WorkspaceID          foundation.ID `json:"workspace_id"`
	WorkflowRunID        foundation.ID `json:"workflow_run_id"`
	NodeRunID            foundation.ID `json:"node_run_id"`
	ProposalID           foundation.ID `json:"proposal_id"`
	RevisionID           foundation.ID `json:"revision_id"`
	ApprovalID           foundation.ID `json:"approval_id"`
	WritebackExecutionID foundation.ID `json:"writeback_execution_id"`
	TargetPath           string        `json:"target_path"`
	ResultHash           string        `json:"result_hash"`
	GitCommit            string        `json:"git_commit"`
}

// Binding 是持久 Writeback 事实对重索引消息的完整不可变约束。
type Binding struct {
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	WritebackExecutionID                  foundation.ID
	TargetPath, ResultHash, GitCommit     string
}

// DecodeStrict 严格解析单个 v1 JSON 对象，拒绝未知、缺失、重复字段和非 canonical 值。
func DecodeStrict(payload []byte) (RequestV1, error) {
	if err := validateObjectShape(payload); err != nil {
		return RequestV1{}, contractError(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request RequestV1
	if err := decoder.Decode(&request); err != nil {
		return RequestV1{}, contractError(err)
	}
	if err := requireEOF(decoder); err != nil {
		return RequestV1{}, contractError(err)
	}
	if err := validateRequest(request); err != nil {
		return RequestV1{}, contractError(err)
	}
	return request, nil
}

// EncodeCanonical 校验请求并按固定字段顺序编码，Hash 与 Git Commit 统一为小写。
func EncodeCanonical(request RequestV1) ([]byte, error) {
	request.ResultHash = strings.ToLower(request.ResultHash)
	request.GitCommit = strings.ToLower(request.GitCommit)
	if err := validateRequest(request); err != nil {
		return nil, contractError(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, contractError(err)
	}
	return encoded, nil
}

// ValidateBinding 确认消息只能引用当前持久 Writeback 的完整不可变事实。
func ValidateBinding(request RequestV1, binding Binding) error {
	if err := validateRequest(request); err != nil {
		return contractError(err)
	}
	if request.WorkspaceID != binding.WorkspaceID || request.WorkflowRunID != binding.WorkflowRunID ||
		request.NodeRunID != binding.NodeRunID || request.ProposalID != binding.ProposalID ||
		request.RevisionID != binding.RevisionID || request.ApprovalID != binding.ApprovalID ||
		request.WritebackExecutionID != binding.WritebackExecutionID || request.TargetPath != binding.TargetPath ||
		request.ResultHash != strings.ToLower(binding.ResultHash) || request.GitCommit != strings.ToLower(binding.GitCommit) {
		return contractError(errors.New("reindex request binding does not match"))
	}
	return nil
}

func validateObjectShape(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return errors.New("reindex request must be a JSON object")
	}
	seen := make(map[string]struct{}, 11)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("reindex request field name is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("reindex request contains a duplicate field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return errors.New("reindex request object is not closed")
	}
	if len(seen) != 11 {
		return errors.New("reindex request must contain exactly 11 fields")
	}
	return requireEOF(decoder)
}

func validateRequest(request RequestV1) error {
	if request.SchemaVersion != SchemaVersionV1 {
		return errors.New("unsupported reindex request schema version")
	}
	ids := []foundation.ID{
		request.WorkspaceID, request.WorkflowRunID, request.NodeRunID, request.ProposalID,
		request.RevisionID, request.ApprovalID, request.WritebackExecutionID,
	}
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return errors.New("reindex request contains a non-canonical id")
		}
	}
	if !canonicalRelativePath(request.TargetPath) {
		return errors.New("reindex request contains a non-canonical target path")
	}
	if !lowerHex(request.ResultHash, 64) {
		return errors.New("reindex request result hash is invalid")
	}
	if !lowerHex(request.GitCommit, 40) && !lowerHex(request.GitCommit, 64) {
		return errors.New("reindex request git commit is invalid")
	}
	return nil
}

func canonicalRelativePath(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') ||
		strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func lowerHex(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("reindex request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func contractError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalidContract, false, cause)
}
