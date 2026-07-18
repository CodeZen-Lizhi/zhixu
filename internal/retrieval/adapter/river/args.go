// Package river 实现 Reindex Delivery 的 River transport 边界。
package river

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// JobKind 是持久化 Reindex River Job 的稳定类型名。
	JobKind = "retrieval_reindex_delivery_v1"
	// JobSchemaVersion 是 Args JSON 契约版本。
	JobSchemaVersion = 1
)

// Args 只携带 Delivery transport generation，不复制业务 Payload 或正文。
type Args struct {
	SchemaVersion int           `json:"schema_version"`
	DeliveryID    foundation.ID `json:"delivery_id" river:"unique"`
	DispatchNo    int           `json:"dispatch_no" river:"unique"`
}

// Kind 实现 River JobArgs，并保持类型名稳定。
func (Args) Kind() string { return JobKind }

// NewArgs 创建并校验 Reindex River 参数。
func NewArgs(deliveryID foundation.ID, dispatchNo int) (Args, error) {
	args := Args{SchemaVersion: JobSchemaVersion, DeliveryID: deliveryID, DispatchNo: dispatchNo}
	if err := ValidateArgs(args); err != nil {
		return Args{}, err
	}
	return args, nil
}

// ValidateArgs 校验 River 参数的版本、规范 UUID 与 generation。
func ValidateArgs(args Args) error {
	if args.SchemaVersion != JobSchemaVersion {
		return argsError(foundation.ErrorInvalidInput, "REINDEX_JOB_SCHEMA_INVALID", errors.New("unsupported reindex job schema version"))
	}
	parsed, err := foundation.ParseID(string(args.DeliveryID))
	if err != nil || parsed != args.DeliveryID || strings.ContainsAny(string(args.DeliveryID), "\r\n\t/") {
		return argsError(foundation.ErrorInvalidInput, "REINDEX_JOB_DELIVERY_ID_INVALID", errors.New("delivery id must be a canonical UUID"))
	}
	if args.DispatchNo < 1 {
		return argsError(foundation.ErrorInvalidInput, "REINDEX_JOB_DISPATCH_INVALID", errors.New("dispatch number must be positive"))
	}
	return nil
}

// DecodeStrict 严格解码单个固定三字段 Args JSON。
func DecodeStrict(encoded []byte) (Args, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var args Args
	if err := decoder.Decode(&args); err != nil {
		return Args{}, argsError(foundation.ErrorNonRetryableFailure, "REINDEX_JOB_PAYLOAD_INVALID", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("reindex job payload contains multiple JSON values")
		}
		return Args{}, argsError(foundation.ErrorNonRetryableFailure, "REINDEX_JOB_PAYLOAD_INVALID", err)
	}
	if err := ValidateArgs(args); err != nil {
		return Args{}, err
	}
	return args, nil
}

// ValidateEncodedArgs 校验 River 持久 JSON 与 typed Args 完全一致。
func ValidateEncodedArgs(encoded []byte, expected Args) error {
	persisted, err := DecodeStrict(encoded)
	if err != nil {
		return err
	}
	if persisted != expected {
		return argsError(foundation.ErrorConsistencyViolation, "REINDEX_JOB_DECODE_CONFLICT", errors.New("decoded reindex job args differ from typed payload"))
	}
	return nil
}

func argsError(kind foundation.ErrorKind, code string, cause error) error {
	return foundation.NewError(kind, code, false, cause)
}
