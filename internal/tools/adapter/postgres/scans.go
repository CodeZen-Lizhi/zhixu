package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

type rowScanner interface {
	Scan(...any) error
}

func sameStartBinding(existing, requested domain.ToolCall, includeCallNo bool) bool {
	if includeCallNo && existing.CallNo != requested.CallNo {
		return false
	}
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.NodeAttemptID == requested.NodeAttemptID &&
		existing.RequestedToolName == requested.RequestedToolName && sameToolRef(existing.Tool, requested.Tool) &&
		existing.DefinitionHash == requested.DefinitionHash && sameSchemaRef(existing.InputSchema, requested.InputSchema) &&
		sameSchemaRef(existing.OutputSchema, requested.OutputSchema) && existing.Capability == requested.Capability &&
		existing.SideEffectLevel == requested.SideEffectLevel && existing.InvocationPolicy == requested.InvocationPolicy &&
		existing.IdempotencyKey == requested.IdempotencyKey && existing.RequestHash == requested.RequestHash &&
		existing.RequestBytes == requested.RequestBytes && jsonEqual(existing.RequestSummary, requested.RequestSummary)
}

func sameTerminalResult(existing, requested domain.ToolCall) bool {
	return sameStartBinding(existing, requested, true) && existing.Status == requested.Status &&
		existing.ResponseHash == requested.ResponseHash && existing.ResponseBytes == requested.ResponseBytes &&
		jsonEqual(existing.ResponseSummary, requested.ResponseSummary) && existing.ResultRef == requested.ResultRef &&
		existing.SideEffectType == requested.SideEffectType && existing.SideEffectID == requested.SideEffectID &&
		existing.ErrorCode == requested.ErrorCode && existing.Retryable == requested.Retryable && existing.Version == requested.Version
}

func sameToolRef(left, right *domain.ToolRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameSchemaRef(left, right *domain.SchemaRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func jsonEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == 0 && len(right) == 0
	}
	leftValue, leftErr := decodeJSONValue(left)
	rightValue, rightErr := decodeJSONValue(right)
	return leftErr == nil && rightErr == nil && deepEqualJSON(leftValue, rightValue)
}

func deepEqualJSON(left, right any) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftEncoded) == string(rightEncoded)
}

func decodeJSONValue(document json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("json document contains trailing data")
	}
	return value, nil
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalToolVersion(ref *domain.ToolRef) any {
	if ref == nil {
		return nil
	}
	return ref.Version
}

func optionalSchemaID(ref *domain.SchemaRef) any {
	if ref == nil {
		return nil
	}
	return ref.ID
}

func optionalSchemaVersion(ref *domain.SchemaRef) any {
	if ref == nil {
		return nil
	}
	return ref.Version
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
