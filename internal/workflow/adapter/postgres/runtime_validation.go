package workflowpostgres

import (
	"bytes"
	encodinghex "encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"regexp"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const runtimeEventSchemaVersion = 1

// A 24-section Organizing outline can carry 32 immutable Evidence tuples per
// section. Keep the owner read bounded without rejecting that valid contract.
const maxSucceededNodeOutputBytes = 512 * 1024

func validateRuntimeStartRequest(request application.RuntimeStartRequest) error {
	if request.Definition.ID == "" || request.Definition.WorkspaceID == "" || strings.TrimSpace(request.Definition.Key) == "" || request.Definition.Version < 1 || request.DefinitionInputSchemaVersion < 1 || !objectBytes(request.Definition.Graph) || !validRuntimeHash(request.DefinitionGraphHash) {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_DEFINITION_INVALID", false, errors.New("runtime definition is invalid"))
	}
	if request.Run.ID == "" || request.Run.WorkspaceID != request.Definition.WorkspaceID || request.Run.IdempotencyKey == "" || request.Run.RequestHash == "" || request.RequestHash != request.Run.RequestHash || !validRuntimeHash(request.RequestHash) || request.Run.Status != domain.StatusPending {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_RUN_INVALID", false, errors.New("runtime run is invalid"))
	}
	if request.FirstNode.ID == "" || request.FirstNode.RunID != request.Run.ID || request.FirstNode.IdempotencyKey == "" || request.FirstNode.InputSchemaVersion < 1 || request.FirstNode.OutputSchemaVersion < 1 || request.FirstNode.DispatchNo != 1 || request.FirstNode.Status != domain.StatusPending {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_NODE_INVALID", false, errors.New("runtime node is invalid"))
	}
	if request.Event.ID == "" || request.Event.RunID == nil || *request.Event.RunID != request.Run.ID || request.Event.WorkspaceID != request.Run.WorkspaceID || request.Event.EventKey == "" || request.Event.SchemaVersion < 1 || request.Event.EventVersion < 1 {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_START_EVENT_INVALID", false, errors.New("runtime event is invalid"))
	}
	return nil
}

func validateRuntimeNodeReplay(node, candidate domain.NodeRun) error {
	if node.RunID != candidate.RunID || node.NodeKey != candidate.NodeKey || node.NodeType != candidate.NodeType || node.IdempotencyKey != candidate.IdempotencyKey || node.InputSchemaVersion != candidate.InputSchemaVersion || node.OutputSchemaVersion != candidate.OutputSchemaVersion || node.DispatchNo != candidate.DispatchNo || !jsonEqual(node.Input, candidate.Input) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_NODE_BINDING_CONFLICT", false, errors.New("replayed root node binding differs"))
	}
	return nil
}

func objectBytes(value []byte) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validRuntimeHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := encodinghex.DecodeString(value)
	return err == nil
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

func modelRuntimeOwnershipLost() error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_MODEL_RUNTIME_OWNERSHIP_LOST", true, errors.New("worker model runtime ownership changed"))
}

func sameModelRuntimeOwner(revision *int64, instanceID *foundation.ID, owner *foundation.ID) bool {
	if owner == nil {
		return revision == nil && instanceID == nil
	}
	return revision != nil && *revision >= 0 && instanceID != nil && *instanceID == *owner
}

func validateDecisionSchema(schema, decision json.RawMessage) error {
	var schemaObject map[string]any
	var decisionObject map[string]any
	if json.Unmarshal(schema, &schemaObject) != nil || json.Unmarshal(decision, &decisionObject) != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision schema or value is invalid"))
	}
	if schemaType, exists := schemaObject["type"]; exists && schemaType != "object" {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New("human task schema root must be object"))
	}
	// 这只是有限的属性级子集，并非通用 JSON Schema 校验器。
	// 先校验约束再校验值，包括缺省属性的 Schema。
	var exactSchema map[string]any
	var exactDecision map[string]any
	schemaDecoder := json.NewDecoder(bytes.NewReader(schema))
	schemaDecoder.UseNumber()
	decisionDecoder := json.NewDecoder(bytes.NewReader(decision))
	decisionDecoder.UseNumber()
	if err := schemaDecoder.Decode(&exactSchema); err != nil {
		return err
	}
	if err := decisionDecoder.Decode(&exactDecision); err != nil {
		return err
	}
	properties, _ := exactSchema["properties"].(map[string]any)
	patterns := make(map[string]*regexp.Regexp)
	for name, rawProperty := range properties {
		property, valid := rawProperty.(map[string]any)
		if !valid {
			return humanSchemaUnsupported("human task property schema is invalid")
		}
		if rawType, exists := property["type"]; exists {
			expected, valid := rawType.(string)
			if !valid {
				return humanSchemaUnsupported("human task property type must be a string")
			}
			switch expected {
			case "string", "number", "integer", "boolean", "object", "array", "null":
			default:
				return humanSchemaUnsupported("human task property type is invalid or unsupported")
			}
		}
		if rawEnum, exists := property["enum"]; exists {
			values, valid := rawEnum.([]any)
			if !valid || len(values) == 0 {
				return humanSchemaUnsupported("human task enum must be a nonempty array")
			}
		}
		if rawPattern, exists := property["pattern"]; exists {
			pattern, valid := rawPattern.(string)
			if !valid {
				return humanSchemaUnsupported("human task pattern must be a string")
			}
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return humanSchemaUnsupported("human task pattern is invalid")
			}
			patterns[name] = compiled
		}
	}
	if required, ok := schemaObject["required"].([]any); ok {
		for _, rawName := range required {
			name, valid := rawName.(string)
			if !valid || strings.TrimSpace(name) == "" {
				return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New("human task required field is invalid"))
			}
			if _, exists := decisionObject[name]; !exists {
				return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision misses required field"))
			}
		}
	}
	for name, rawProperty := range properties {
		value, exists := decisionObject[name]
		if !exists {
			continue
		}
		property, valid := rawProperty.(map[string]any)
		if !valid {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New("human task property schema is invalid"))
		}
		if values, exists := property["enum"].([]any); exists {
			matched := false
			for _, candidate := range values {
				if reflect.DeepEqual(humanEnumValue(exactDecision[name]), humanEnumValue(candidate)) {
					matched = true
					break
				}
			}
			if !matched {
				return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision field is outside enum"))
			}
		}
		if pattern := patterns[name]; pattern != nil {
			if text, isString := value.(string); isString && !pattern.MatchString(text) {
				return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision field does not match pattern"))
			}
		}
		expected, _ := property["type"].(string)
		if expected != "" && !matchesJSONType(value, expected) {
			return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision field type differs"))
		}
	}
	if additional, ok := schemaObject["additionalProperties"].(bool); ok && !additional {
		for name := range decisionObject {
			if _, exists := properties[name]; !exists {
				return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision contains unknown field"))
			}
		}
	}
	return nil
}

func humanSchemaUnsupported(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New(message))
}

// 保留 enum 中 JSON 数字的精确相等性（包括嵌套值），同时
// 保持旧版基于 float64 的 number/integer 类型检查不变。
func humanEnumValue(value any) any {
	switch value := value.(type) {
	case json.Number:
		number, ok := new(big.Rat).SetString(string(value))
		if ok {
			return number
		}
		return value
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = humanEnumValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = humanEnumValue(item)
		}
		return result
	default:
		return value
	}
}

func matchesJSONType(value any, expected string) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func validRuntimeOutputID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
