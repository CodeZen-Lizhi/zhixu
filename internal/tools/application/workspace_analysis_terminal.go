package application

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// WorkspaceAnalysisToolTerminal 是已由专用事务确认闭合的 Workspace Analysis Tool 终态证据。
// 身份仅供服务端错误恢复使用，禁止进入 JSON 或结构化日志。
type WorkspaceAnalysisToolTerminal struct {
	OperationID foundation.ID     `json:"-"`
	CallID      foundation.ID     `json:"-"`
	CallStatus  domain.CallStatus `json:"-"`
	ErrorCode   string            `json:"-"`
}

// WorkspaceAnalysisReceiptFailureTerminal 是已由专用事务确认的 canonical receipt 形成失败证据。
// 身份和哈希只供服务端 finalizer 精确核对，禁止进入通用 JSON、日志或错误文本。
type WorkspaceAnalysisReceiptFailureTerminal struct {
	OperationID  foundation.ID                   `json:"-"`
	FailureID    foundation.ID                   `json:"-"`
	Code         domain.ResultReceiptFailureCode `json:"-"`
	ExpectedHash string                          `json:"-"`
	ActualHash   *string                         `json:"-"`
}

// String 只投影稳定失败类别，不暴露 Operation、失败事实或哈希。
func (terminal WorkspaceAnalysisReceiptFailureTerminal) String() string {
	return "WorkspaceAnalysisReceiptFailureTerminal{code:" + string(terminal.Code) + "}"
}

// GoString 避免 %#v 绕过 receipt failure 的安全投影。
func (terminal WorkspaceAnalysisReceiptFailureTerminal) GoString() string { return terminal.String() }

// LogValue 禁止结构化日志反射序列化服务端恢复身份和哈希。
func (terminal WorkspaceAnalysisReceiptFailureTerminal) LogValue() slog.Value {
	return slog.GroupValue(slog.String("code", string(terminal.Code)))
}

// MarshalJSON 禁止将 receipt failure 终态证据作为通用 JSON 数据传递。
func (WorkspaceAnalysisReceiptFailureTerminal) MarshalJSON() ([]byte, error) {
	return json.RawMessage(`{}`), nil
}

// String 只投影终态类别，避免错误或调试输出泄露持久身份。
func (terminal WorkspaceAnalysisToolTerminal) String() string {
	return "WorkspaceAnalysisToolTerminal{call_status:" + string(terminal.CallStatus) + " error_code:" + terminal.ErrorCode + "}"
}

// GoString 避免 %#v 绕过终态证据的安全投影。
func (terminal WorkspaceAnalysisToolTerminal) GoString() string { return terminal.String() }

// LogValue 禁止结构化日志反射序列化 Operation 或 Call 身份。
func (terminal WorkspaceAnalysisToolTerminal) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("call_status", string(terminal.CallStatus)),
		slog.String("error_code", terminal.ErrorCode),
	)
}

// MarshalJSON 禁止将终态证据作为通用 JSON 数据传递。
func (WorkspaceAnalysisToolTerminal) MarshalJSON() ([]byte, error) { return json.RawMessage(`{}`), nil }

// LogValue keeps server-only operation identity out of generic Tool result logs.
func (result ToolExecutionResult) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("call_status", string(result.Call.Status)),
		slog.Bool("replayed", result.Replayed),
		slog.Bool("has_result_receipt", result.ResultReceiptID != ""),
	)
}

type workspaceAnalysisToolTerminalError struct {
	terminal WorkspaceAnalysisToolTerminal
	cause    error
}

type workspaceAnalysisReceiptFailureTerminalError struct {
	terminal WorkspaceAnalysisReceiptFailureTerminal
	cause    error
}

func (err *workspaceAnalysisReceiptFailureTerminalError) Error() string {
	if err == nil || err.cause == nil {
		return "<nil>"
	}
	return err.cause.Error()
}

func (err *workspaceAnalysisReceiptFailureTerminalError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// GoString 避免 %#v 展开 retained receipt failure evidence。
func (err *workspaceAnalysisReceiptFailureTerminalError) GoString() string { return err.Error() }

func (err *workspaceAnalysisToolTerminalError) Error() string {
	if err == nil || err.cause == nil {
		return "<nil>"
	}
	return err.cause.Error()
}

func (err *workspaceAnalysisToolTerminalError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// GoString avoids %#v exposing the retained terminal evidence.
func (err *workspaceAnalysisToolTerminalError) GoString() string { return err.Error() }

// WorkspaceAnalysisToolTerminalFromError returns terminal proof only when a
// Workspace Analysis Tool call was confirmed FAILED or UNKNOWN by its dedicated
// operation/reservation/call transaction.
func WorkspaceAnalysisToolTerminalFromError(err error) (WorkspaceAnalysisToolTerminal, bool) {
	var terminalErr *workspaceAnalysisToolTerminalError
	if !errors.As(err, &terminalErr) || terminalErr == nil {
		return WorkspaceAnalysisToolTerminal{}, false
	}
	return terminalErr.terminal, true
}

// WorkspaceAnalysisReceiptFailureTerminalFromError 仅在专用 UoW 已确认
// SUCCEEDED Call 与 immutable receipt failure 后返回终态证据。
func WorkspaceAnalysisReceiptFailureTerminalFromError(err error) (WorkspaceAnalysisReceiptFailureTerminal, bool) {
	var terminalErr *workspaceAnalysisReceiptFailureTerminalError
	if !errors.As(err, &terminalErr) || terminalErr == nil {
		return WorkspaceAnalysisReceiptFailureTerminal{}, false
	}
	terminal := terminalErr.terminal
	if terminal.ActualHash != nil {
		actualHash := *terminal.ActualHash
		terminal.ActualHash = &actualHash
	}
	return terminal, true
}

func workspaceAnalysisToolTerminalErrorFor(
	operationID foundation.ID,
	call domain.ToolCall,
	cause error,
) error {
	if !validApplicationID(operationID) || !validApplicationID(call.ID) ||
		(call.Status != domain.CallFailed && call.Status != domain.CallUnknown) ||
		call.ErrorCode == "" || domain.ValidateToolCall(call) != nil {
		return cause
	}
	return &workspaceAnalysisToolTerminalError{
		terminal: WorkspaceAnalysisToolTerminal{
			OperationID: operationID,
			CallID:      call.ID,
			CallStatus:  call.Status,
			ErrorCode:   call.ErrorCode,
		},
		cause: cause,
	}
}

func workspaceAnalysisReceiptFailureTerminalErrorFor(
	operationID foundation.ID,
	failure domain.ResultReceiptFailure,
	cause error,
) error {
	if !validApplicationID(operationID) || operationID != failure.OperationID ||
		!validApplicationID(failure.ID) || failure.ExpectedOutputHash == "" {
		return cause
	}
	actualHash := receiptFailureActualHash(failure)
	return &workspaceAnalysisReceiptFailureTerminalError{
		terminal: WorkspaceAnalysisReceiptFailureTerminal{
			OperationID:  operationID,
			FailureID:    failure.ID,
			Code:         failure.FailureCode,
			ExpectedHash: failure.ExpectedOutputHash,
			ActualHash:   actualHash,
		},
		cause: cause,
	}
}

func receiptFailureActualHash(failure domain.ResultReceiptFailure) *string {
	if failure.FailureCode == domain.ResultReceiptFailureMissing || failure.ObservedOutputHash == "" {
		return nil
	}
	actualHash := failure.ObservedOutputHash
	return &actualHash
}
