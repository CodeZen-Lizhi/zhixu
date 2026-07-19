package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	// ToolRequestSchemaVersionV1 是项目自有 Tool Request 的首个版本。
	ToolRequestSchemaVersionV1 = 1
	// MaxToolReasonBytes 是模型解释调用原因的最大 UTF-8 字节数。
	MaxToolReasonBytes = 2048
)

// ToolRequestV1 是模型可以提出的最小结构化 Tool 请求。
type ToolRequestV1 struct {
	SchemaVersion int             `json:"schema_version"`
	ToolName      string          `json:"tool_name"`
	Arguments     json.RawMessage `json:"arguments"`
	Reason        string          `json:"reason"`
}

// Validate 校验 Tool Request envelope，不解释具体 Tool 参数业务语义。
func (request ToolRequestV1) Validate() error {
	if request.SchemaVersion != ToolRequestSchemaVersionV1 || !toolNamePattern.MatchString(request.ToolName) {
		return invalid(ErrorCodeRequestInvalid, "tool request schema version or name is invalid")
	}
	if len(request.Arguments) == 0 || len(request.Arguments) > MaxToolDocumentBytes || !utf8.Valid(request.Arguments) {
		return invalid(ErrorCodeRequestInvalid, "tool request arguments are invalid")
	}
	trimmed := bytes.TrimSpace(request.Arguments)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return invalid(ErrorCodeRequestInvalid, "tool request arguments must be one json object")
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" || len(reason) > MaxToolReasonBytes || !utf8.ValidString(reason) {
		return invalid(ErrorCodeRequestInvalid, "tool request reason is invalid")
	}
	return nil
}
