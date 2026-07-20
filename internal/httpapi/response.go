// Package httpapi owns shared HTTP response contracts.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
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
	if !strictjson.ValidUnicode(body) {
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
