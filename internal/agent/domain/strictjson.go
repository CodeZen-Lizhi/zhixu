package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// DecodeLimits 限制单次结构化输出解析可消耗的资源。
type DecodeLimits struct {
	MaxDocumentBytes int
	MaxDepth         int
	MaxStringBytes   int
	MaxArrayItems    int
	MaxObjectFields  int
}

// DefaultDecodeLimits 返回 Agent 任务 Schema 的默认安全边界。
func DefaultDecodeLimits() DecodeLimits {
	return DecodeLimits{
		MaxDocumentBytes: 256 * 1024,
		MaxDepth:         16,
		MaxStringBytes:   32 * 1024,
		MaxArrayItems:    500,
		MaxObjectFields:  128,
	}
}

// DecodeStrict 解析单个 JSON object，拒绝重复键、未知字段、尾随值和资源越界。
func DecodeStrict[T any](raw []byte, limits DecodeLimits, validate func(T) error) (T, error) {
	var zero T
	if err := limits.validate(); err != nil {
		return zero, err
	}
	if len(raw) == 0 || len(raw) > limits.MaxDocumentBytes {
		return zero, limitError("structured output document exceeds byte limit")
	}
	if !utf8.Valid(raw) {
		return zero, structuredError("structured output is not valid utf-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := inspectJSONObject(decoder, limits, 1); err != nil {
		return zero, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return zero, err
	}

	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var decoded T
	if err := decoder.Decode(&decoded); err != nil {
		return zero, structuredError("structured output does not match schema")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return zero, err
	}
	if validate != nil {
		if err := validate(decoded); err != nil {
			return zero, err
		}
	}
	return decoded, nil
}

func (limits DecodeLimits) validate() error {
	if limits.MaxDocumentBytes < 2 || limits.MaxDepth < 1 || limits.MaxStringBytes < 1 ||
		limits.MaxArrayItems < 0 || limits.MaxObjectFields < 1 {
		return invalid(ErrorCodeStructuredOutputInvalid, "structured output decode limits are invalid")
	}
	return nil
}

func inspectJSONObject(decoder *json.Decoder, limits DecodeLimits, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return structuredError("structured output is not valid json")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return structuredError("structured output must be a json object")
	}
	return inspectObjectBody(decoder, limits, depth)
}

func inspectObjectBody(decoder *json.Decoder, limits DecodeLimits, depth int) error {
	if depth > limits.MaxDepth {
		return limitError("structured output nesting exceeds depth limit")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		if len(seen) >= limits.MaxObjectFields {
			return limitError("structured output object exceeds field limit")
		}
		keyToken, err := decoder.Token()
		if err != nil {
			return structuredError("structured output object key is invalid")
		}
		key, ok := keyToken.(string)
		if !ok || len(key) > limits.MaxStringBytes || !utf8.ValidString(key) {
			return limitError("structured output object key exceeds string limit")
		}
		if _, duplicate := seen[key]; duplicate {
			return structuredError("structured output contains a duplicate object key")
		}
		seen[key] = struct{}{}
		if err := inspectJSONValue(decoder, limits, depth+1); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return structuredError("structured output object is not terminated")
	}
	return nil
}

func inspectJSONArray(decoder *json.Decoder, limits DecodeLimits, depth int) error {
	if depth > limits.MaxDepth {
		return limitError("structured output nesting exceeds depth limit")
	}
	count := 0
	for decoder.More() {
		if count >= limits.MaxArrayItems {
			return limitError("structured output array exceeds item limit")
		}
		if err := inspectJSONValue(decoder, limits, depth+1); err != nil {
			return err
		}
		count++
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return structuredError("structured output array is not terminated")
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder, limits DecodeLimits, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return structuredError("structured output contains invalid json value")
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return inspectObjectBody(decoder, limits, depth)
		case '[':
			return inspectJSONArray(decoder, limits, depth)
		default:
			return structuredError("structured output contains an unexpected delimiter")
		}
	case string:
		if len(value) > limits.MaxStringBytes || !utf8.ValidString(value) {
			return limitError("structured output string exceeds byte limit")
		}
	case json.Number, bool, nil:
		return nil
	default:
		return structuredError("structured output contains unsupported json value")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return structuredError("structured output contains multiple json values")
		}
		return structuredError("structured output has invalid trailing data")
	}
	return nil
}

func structuredError(message string) error {
	return invalid(ErrorCodeStructuredOutputInvalid, message)
}

func limitError(message string) error {
	return invalid(ErrorCodeStructuredOutputLimitExceeded, message)
}
