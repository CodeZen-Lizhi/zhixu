// Package httpapi owns shared HTTP response contracts.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Problem 是 API 边界稳定的错误响应结构。
type Problem struct {
	ErrorCode     string         `json:"error_code"`
	Message       string         `json:"message"`
	Retryable     bool           `json:"retryable"`
	WorkflowRunID string         `json:"workflow_run_id,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
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
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
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
