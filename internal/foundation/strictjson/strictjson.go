// Package strictjson 提供无业务语义的有界严格 JSON 对象解码边界。
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Limits 限制单个 JSON 文档解码可消耗的资源。
type Limits struct {
	MaxDocumentBytes int
	MaxDepth         int
	MaxStringBytes   int
	MaxArrayItems    int
	MaxObjectFields  int
}

// ErrorKind 标识严格 JSON 边界的稳定失败分类。
type ErrorKind string

const (
	// ErrorKindInvalid 表示文档、Schema 或限制配置无效。
	ErrorKindInvalid ErrorKind = "INVALID"
	// ErrorKindLimitExceeded 表示文档超过资源限制。
	ErrorKindLimitExceeded ErrorKind = "LIMIT_EXCEEDED"
)

type strictError struct {
	kind    ErrorKind
	message string
}

func (err *strictError) Error() string {
	return err.message
}

// DefaultLimits 返回严格 JSON 文档的默认安全边界。
func DefaultLimits() Limits {
	return Limits{
		MaxDocumentBytes: 256 * 1024,
		MaxDepth:         16,
		MaxStringBytes:   32 * 1024,
		MaxArrayItems:    500,
		MaxObjectFields:  128,
	}
}

// DecodeObject 解码单个 JSON 对象，拒绝重复键、未知字段、尾随值和资源越界。
func DecodeObject[T any](raw []byte, limits Limits, validate func(T) error) (T, error) {
	var zero T
	if err := limits.validate(); err != nil {
		return zero, err
	}
	if len(raw) == 0 || len(raw) > limits.MaxDocumentBytes {
		return zero, limitError("structured output document exceeds byte limit")
	}
	if !utf8.Valid(raw) {
		return zero, invalidError("structured output is not valid utf-8")
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
		return zero, invalidError("structured output does not match schema")
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

// KindOf 返回严格 JSON 边界错误的稳定分类。
func KindOf(err error) (ErrorKind, bool) {
	var typed *strictError
	if !errors.As(err, &typed) {
		return "", false
	}
	return typed.kind, true
}

func (limits Limits) validate() error {
	if limits.MaxDocumentBytes < 2 || limits.MaxDepth < 1 || limits.MaxStringBytes < 1 ||
		limits.MaxArrayItems < 0 || limits.MaxObjectFields < 1 {
		return invalidError("structured output decode limits are invalid")
	}
	return nil
}

func inspectJSONObject(decoder *json.Decoder, limits Limits, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return invalidError("structured output is not valid json")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return invalidError("structured output must be a json object")
	}
	return inspectObjectBody(decoder, limits, depth)
}

func inspectObjectBody(decoder *json.Decoder, limits Limits, depth int) error {
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
			return invalidError("structured output object key is invalid")
		}
		key, ok := keyToken.(string)
		if !ok || len(key) > limits.MaxStringBytes || !utf8.ValidString(key) {
			return limitError("structured output object key exceeds string limit")
		}
		if _, duplicate := seen[key]; duplicate {
			return invalidError("structured output contains a duplicate object key")
		}
		seen[key] = struct{}{}
		if err := inspectJSONValue(decoder, limits, depth+1); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return invalidError("structured output object is not terminated")
	}
	return nil
}

func inspectJSONArray(decoder *json.Decoder, limits Limits, depth int) error {
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
		return invalidError("structured output array is not terminated")
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder, limits Limits, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return invalidError("structured output contains invalid json value")
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return inspectObjectBody(decoder, limits, depth)
		case '[':
			return inspectJSONArray(decoder, limits, depth)
		default:
			return invalidError("structured output contains an unexpected delimiter")
		}
	case string:
		if len(value) > limits.MaxStringBytes || !utf8.ValidString(value) {
			return limitError("structured output string exceeds byte limit")
		}
	case json.Number, bool, nil:
		return nil
	default:
		return invalidError("structured output contains unsupported json value")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalidError("structured output contains multiple json values")
		}
		return invalidError("structured output has invalid trailing data")
	}
	return nil
}

func invalidError(message string) error {
	return &strictError{kind: ErrorKindInvalid, message: message}
}

func limitError(message string) error {
	return &strictError{kind: ErrorKindLimitExceeded, message: message}
}
