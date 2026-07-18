// Package httpapi owns shared HTTP response contracts.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const maxJSONRequestBytes = 1 << 20

// Problem 是 API 边界稳定的错误响应结构。
type Problem struct {
	ErrorCode     string         `json:"error_code"`
	Message       string         `json:"message"`
	Retryable     bool           `json:"retryable"`
	WorkflowRunID string         `json:"workflow_run_id,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

// StatusForErrorKind 返回项目统一 Foundation 错误分类对应的基础 HTTP 状态。
func StatusForErrorKind(kind foundation.ErrorKind) int {
	switch kind {
	case foundation.ErrorInvalidInput:
		return http.StatusBadRequest
	case foundation.ErrorNotFound:
		return http.StatusNotFound
	case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		return http.StatusConflict
	case foundation.ErrorPermissionDenied:
		return http.StatusForbidden
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// WriteJSON 写入 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// WriteProblem 写入统一 Problem 错误响应。
func WriteProblem(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	WriteJSON(w, status, Problem{ErrorCode: code, Message: message, Retryable: retryable, Details: details})
}

// DecodeJSON 读取有大小上限且只允许一个 JSON 值的请求体。
func DecodeJSON(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONRequestBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxJSONRequestBytes {
		return errors.New("request body exceeds JSON size limit")
	}
	if !utf8.Valid(body) || !validJSONSurrogatePairs(body) {
		return errors.New("request body contains invalid Unicode")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("request body contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validJSONSurrogatePairs(body []byte) bool {
	inString := false
	for index := 0; index < len(body); index++ {
		switch body[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			index++
			if index >= len(body) {
				return false
			}
			if body[index] != 'u' {
				continue
			}
			code, ok := decodeJSONHex4(body, index+1)
			if !ok {
				return false
			}
			index += 4
			if code >= 0xdc00 && code <= 0xdfff {
				return false
			}
			if code < 0xd800 || code > 0xdbff {
				continue
			}
			if index+6 >= len(body) || body[index+1] != '\\' || body[index+2] != 'u' {
				return false
			}
			low, ok := decodeJSONHex4(body, index+3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 6
		}
	}
	return true
}

func decodeJSONHex4(body []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(body) {
		return 0, false
	}
	var value uint16
	for _, character := range body[start : start+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
