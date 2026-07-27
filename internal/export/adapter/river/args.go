// Package river 实现 Export Job 的 River transport 边界。
package river

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// JobKind 是持久化 Export River Job 的稳定类型名。
	JobKind = "export_job_v1"
	// JobSchemaVersion 是 Export Args JSON 契约版本。
	JobSchemaVersion = 1
)

// Args 只携带 Workspace/Export identity，不复制导出定义或结果。
type Args struct {
	SchemaVersion int           `json:"schema_version"`
	WorkspaceID   foundation.ID `json:"workspace_id" river:"unique"`
	ExportID      foundation.ID `json:"export_id" river:"unique"`
}

// Kind 实现 River JobArgs。
func (Args) Kind() string { return JobKind }

// NewArgs 创建严格校验的 Export transport 参数。
func NewArgs(workspaceID, exportID foundation.ID) (Args, error) {
	args := Args{SchemaVersion: JobSchemaVersion, WorkspaceID: workspaceID, ExportID: exportID}
	if err := ValidateArgs(args); err != nil {
		return Args{}, err
	}
	return args, nil
}

// ValidateArgs 校验版本和规范 UUID。
func ValidateArgs(args Args) error {
	if args.SchemaVersion != JobSchemaVersion || !validID(args.WorkspaceID) || !validID(args.ExportID) {
		return foundation.NewError(foundation.ErrorInvalidInput, "EXPORT_RIVER_ARGS_INVALID", false, errors.New("export River args are invalid"))
	}
	return nil
}

// DecodeStrict 严格解码持久化 Args JSON。
func DecodeStrict(encoded []byte) (Args, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var args Args
	if err := decoder.Decode(&args); err != nil {
		return Args{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "EXPORT_RIVER_PAYLOAD_INVALID", false, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("export River payload contains multiple JSON values")
		}
		return Args{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "EXPORT_RIVER_PAYLOAD_INVALID", false, err)
	}
	if err := ValidateArgs(args); err != nil {
		return Args{}, err
	}
	return args, nil
}

// ValidateEncodedArgs 确保持久 JSON 与 typed Args 完全一致。
func ValidateEncodedArgs(encoded []byte, expected Args) error {
	decoded, err := DecodeStrict(encoded)
	if err != nil {
		return err
	}
	if decoded != expected {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_RIVER_DECODE_CONFLICT", false, errors.New("decoded export args differ from typed payload"))
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
