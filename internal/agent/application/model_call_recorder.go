package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeModelCallPersistenceUnknown 表示 Provider 已被调用，但调用结果持久化状态无法确认。
	ErrorCodeModelCallPersistenceUnknown = "AGENT_MODEL_CALL_RESULT_UNKNOWN"
	// ErrorCodeModelCallReplayUnsafe 表示已有调用事实无法在不重复调用 Provider 的前提下重放。
	ErrorCodeModelCallReplayUnsafe = "AGENT_MODEL_CALL_REPLAY_UNSAFE"
	// ErrorCodeModelCallTerminalInvalid 表示调用方提交了不完整或不匹配的终态。
	ErrorCodeModelCallTerminalInvalid = "AGENT_MODEL_CALL_TERMINAL_INVALID"
	// ErrorCodeModelCallFailed 是未分类 Provider 失败的稳定错误码。
	ErrorCodeModelCallFailed = "AGENT_MODEL_CALL_FAILED"
	// MaxModelCallRequestBytes 是单次 ModelCall 规范化请求的有界上限。
	MaxModelCallRequestBytes int64 = 16 * 1024 * 1024
	// MaxModelCallResponseBytes 是单次 ModelCall 规范化响应的有界上限。
	MaxModelCallResponseBytes int64 = 16 * 1024 * 1024

	modelCallRecorderPersistenceTimeout = 5 * time.Second
)

// ModelCallRecorderDependencies 是 ModelCallRecorder 的项目持久化依赖。
type ModelCallRecorderDependencies struct {
	Repository  ModelRunRepository
	WorkspaceID foundation.ID
	ModelRunID  foundation.ID
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

// ModelCallRecorder 只拥有 ModelCall 的生命周期与摘要，不依赖具体模型 SDK。
// 一个实例可以被多个并发的 Provider wrapper 使用；调用号由调用方显式提供。
type ModelCallRecorder struct {
	repository  ModelRunRepository
	workspaceID foundation.ID
	modelRunID  foundation.ID
	ids         foundation.IDGenerator
	clock       foundation.Clock
	idMu        sync.Mutex
}

// ModelCallStart 是一次 Provider 调用开始前的项目规范化快照。
// Request 必须是调用方按项目合同生成的 canonical bytes，不能是 Provider SDK 的序列化结果。
type ModelCallStart struct {
	CallNo          int
	Phase           domain.ModelCallPhase
	Model           domain.ModelRef
	Profile         domain.ModelProfileRef
	Prompt          domain.PromptRef
	Schema          domain.SchemaRef
	MaxOutputTokens int
	Request         []byte
}

// ModelCallTerminal 描述 Provider 调用结束时的项目结果。
// Unknown 表示不能证明 Provider 结果；该状态不会保存 Response 或 Usage。
type ModelCallTerminal struct {
	Status    domain.ModelCallStatus
	Response  []byte
	Usage     domain.TokenUsage
	Error     error
	ErrorCode string
}

// ModelCallRecording 是已经写入 STARTED 的调用句柄。
// 同一句柄只能归约一次，适合同时包装 Generate 和 Stream 的 EOF/错误路径。
type ModelCallRecording struct {
	recorder *ModelCallRecorder
	started  domain.ModelCall
	mu       sync.Mutex
	closed   bool
}

// NewModelCallRecorder 创建一个共享的 ModelCall 记录器。
func NewModelCallRecorder(dependencies ModelCallRecorderDependencies) (*ModelCallRecorder, error) {
	if isNilPort(dependencies.Repository) || dependencies.IDs == nil || dependencies.Clock == nil ||
		!canonicalApplicationID(dependencies.WorkspaceID) || !canonicalApplicationID(dependencies.ModelRunID) ||
		dependencies.WorkspaceID == dependencies.ModelRunID {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeModelCallPersistenceUnknown, false, errors.New("model call recorder dependencies are incomplete"))
	}
	return &ModelCallRecorder{
		repository:  dependencies.Repository,
		workspaceID: dependencies.WorkspaceID,
		modelRunID:  dependencies.ModelRunID,
		ids:         dependencies.IDs,
		clock:       dependencies.Clock,
	}, nil
}

// Start 在 Provider 调用前持久化 STARTED，并返回一个可完成的调用句柄。
func (recorder *ModelCallRecorder) Start(ctx context.Context, request ModelCallStart) (*ModelCallRecording, error) {
	if recorder == nil || isNilPort(recorder.repository) || recorder.ids == nil || recorder.clock == nil {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeModelCallPersistenceUnknown, false, errors.New("model call recorder is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateModelCallStart(request); err != nil {
		return nil, err
	}

	recorder.idMu.Lock()
	callID, err := recorder.ids.New()
	recorder.idMu.Unlock()
	if err != nil {
		return nil, err
	}
	startedAt := recorder.clock.Now().UTC()
	if startedAt.IsZero() {
		return nil, applicationError(foundation.ErrorConsistencyViolation, ErrorCodeModelCallPersistenceUnknown, false, errors.New("model call clock returned zero time"))
	}
	started := domain.ModelCall{
		ID: callID, ModelRunID: recorder.modelRunID, CallNo: request.CallNo, Phase: request.Phase,
		Model: request.Model, Profile: request.Profile, Prompt: request.Prompt, Schema: request.Schema,
		MaxOutputTokens: request.MaxOutputTokens, Status: domain.ModelCallStarted,
		RequestHash: sha256Hex(request.Request), RequestBytes: int64(len(request.Request)), Version: 1,
		StartedAt: startedAt,
	}
	if err := domain.ValidateModelCall(started); err != nil {
		return nil, applicationError(foundation.ErrorConsistencyViolation, ErrorCodeModelCallTerminalInvalid, false, err)
	}
	persisted, replayed, err := recorder.repository.StartModelCall(ctx, recorder.workspaceID, started)
	if err != nil {
		return nil, err
	}
	if replayed || !sameStartedModelCall(started, persisted) {
		return nil, applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallReplayUnsafe, false, errors.New("persisted model call cannot be safely replayed without a provider response"))
	}
	return &ModelCallRecording{recorder: recorder, started: persisted}, nil
}

// Started 返回不可变的 STARTED 快照，供 adapter 关联调用号与审计。
func (recording *ModelCallRecording) Started() domain.ModelCall {
	if recording == nil {
		return domain.ModelCall{}
	}
	return recording.started
}

// Complete 以 CAS 归约 STARTED 调用；成功/失败会保存规范化响应摘要，未知结果严格不声称响应。
func (recording *ModelCallRecording) Complete(ctx context.Context, terminal ModelCallTerminal) (domain.ModelCall, error) {
	if recording == nil || recording.recorder == nil {
		return domain.ModelCall{}, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeModelCallPersistenceUnknown, false, errors.New("model call recording is unavailable"))
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if recording.closed {
		return domain.ModelCall{}, applicationError(foundation.ErrorConsistencyViolation, ErrorCodeModelCallTerminalInvalid, false, errors.New("model call recording is already terminal"))
	}
	recording.closed = true
	if ctx == nil {
		ctx = context.Background()
	}

	completed, callerErr := recording.recorder.buildTerminal(recording.started, terminal)
	if err := domain.ValidateModelCall(completed); err != nil {
		// Provider 已经被调用，但终态无法按项目合同表达时只能记为 UNKNOWN。
		completed = recording.recorder.unknownTerminal(recording.started, ErrorCodeModelCallTerminalInvalid)
		callerErr = applicationError(foundation.ErrorConsistencyViolation, ErrorCodeModelCallTerminalInvalid, false, err)
	}
	persisted, replayed, persistErr := recording.recorder.persistTerminal(ctx, recording.started, completed)
	if persistErr != nil {
		return domain.ModelCall{}, recording.recorder.persistenceUnknown(ctx, recording.started, persistErr)
	}
	if replayed && !sameTerminalModelCall(completed, persisted) {
		return domain.ModelCall{}, applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallPersistenceUnknown, false, errors.New("persisted model call terminal result differs from requested result"))
	}
	if callerErr != nil {
		return persisted, callerErr
	}
	if terminal.Status == domain.ModelCallUnknown {
		return persisted, applicationError(foundation.ErrorManualRecoveryRequired, persisted.ErrorCode, false, terminal.Error)
	}
	if terminal.Status == domain.ModelCallFailed {
		return persisted, terminalError(terminal)
	}
	return persisted, nil
}

func (recorder *ModelCallRecorder) persistTerminal(ctx context.Context, started, completed domain.ModelCall) (domain.ModelCall, bool, error) {
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelCallRecorderPersistenceTimeout)
	defer cancel()
	return recorder.repository.CompleteModelCall(persistContext, CompleteModelCallCommand{
		WorkspaceID: recorder.workspaceID, ExpectedVersion: started.Version, Call: completed,
	})
}

func (recorder *ModelCallRecorder) persistenceUnknown(ctx context.Context, started domain.ModelCall, cause error) error {
	unknown := recorder.unknownTerminal(started, ErrorCodeModelCallPersistenceUnknown)
	if _, _, err := recorder.persistTerminal(ctx, started, unknown); err != nil {
		return applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallPersistenceUnknown, false, errors.Join(cause, err))
	}
	return applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallPersistenceUnknown, false, cause)
}

func (recorder *ModelCallRecorder) buildTerminal(started domain.ModelCall, terminal ModelCallTerminal) (domain.ModelCall, error) {
	completedAt := recorder.clock.Now().UTC()
	if completedAt.Before(started.StartedAt) {
		completedAt = started.StartedAt
	}
	completed := started
	completed.Version++
	completed.CompletedAt = &completedAt
	completed.LatencyMillis = completedAt.Sub(started.StartedAt).Milliseconds()
	status := terminal.Status
	if status == "" {
		if terminal.Error != nil {
			status = domain.ModelCallFailed
		} else {
			status = domain.ModelCallSucceeded
		}
	}
	completed.Status = status
	switch status {
	case domain.ModelCallSucceeded:
		if terminal.Error != nil || len(terminal.Response) == 0 || int64(len(terminal.Response)) > MaxModelCallResponseBytes {
			return completed, errors.New("successful model call requires a bounded response without an error")
		}
		if err := terminal.Usage.Validate(); err != nil {
			return completed, err
		}
		completed.ResponseHash = sha256Hex(terminal.Response)
		completed.ResponseBytes = int64(len(terminal.Response))
		completed.Usage = terminal.Usage
	case domain.ModelCallFailed:
		if terminal.Error == nil && terminal.ErrorCode == "" {
			return completed, errors.New("failed model call requires an error code")
		}
		completed.ErrorCode = stableTerminalErrorCode(terminal.ErrorCode, terminal.Error, ErrorCodeModelCallFailed)
		if len(terminal.Response) > 0 {
			if int64(len(terminal.Response)) > MaxModelCallResponseBytes {
				return completed, errors.New("failed model call response exceeds the bounded limit")
			}
			completed.ResponseHash = sha256Hex(terminal.Response)
			completed.ResponseBytes = int64(len(terminal.Response))
			if terminal.Usage.Validate() == nil {
				completed.Usage = terminal.Usage
			}
		}
	case domain.ModelCallUnknown:
		completed.ErrorCode = stableTerminalErrorCode(terminal.ErrorCode, terminal.Error, ErrorCodeModelCallPersistenceUnknown)
		// UNKNOWN 只保留稳定错误，不把部分响应或 usage 当作可证明事实。
	default:
		return completed, errors.New("model call terminal status is unsupported")
	}
	return completed, nil
}

func (recorder *ModelCallRecorder) unknownTerminal(started domain.ModelCall, code string) domain.ModelCall {
	completedAt := recorder.clock.Now().UTC()
	if completedAt.Before(started.StartedAt) {
		completedAt = started.StartedAt
	}
	unknown := started
	unknown.Status = domain.ModelCallUnknown
	unknown.ErrorCode = stableTerminalErrorCode(code, nil, ErrorCodeModelCallPersistenceUnknown)
	unknown.Version++
	unknown.CompletedAt = &completedAt
	unknown.LatencyMillis = completedAt.Sub(started.StartedAt).Milliseconds()
	return unknown
}

func validateModelCallStart(request ModelCallStart) error {
	if request.CallNo < 1 || request.CallNo > domain.MaxModelCallsPerRun || !validChatPhase(request.Phase) ||
		request.Model.Validate() != nil || request.Profile.Validate() != nil || request.Prompt.Validate() != nil || request.Schema.Validate() != nil ||
		request.MaxOutputTokens <= 0 || request.MaxOutputTokens > MaxOutputTokens || len(request.Request) == 0 || int64(len(request.Request)) > MaxModelCallRequestBytes {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeModelCallTerminalInvalid, false, errors.New("model call start binding or request bytes are invalid"))
	}
	return nil
}

func stableTerminalErrorCode(explicit string, cause error, fallback string) string {
	if explicit != "" && canonicalStableErrorCode(explicit) {
		return explicit
	}
	if cause != nil {
		if code := stableAgentErrorCode(cause, ""); code != "" && canonicalStableErrorCode(code) {
			return code
		}
	}
	return fallback
}

// stableAgentErrorCode extracts only the public project error code from a
// classified error. Provider/SDK text is deliberately never used as a code.
func stableAgentErrorCode(err error, fallback string) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified != nil && canonicalStableErrorCode(classified.Code) {
		return classified.Code
	}
	return fallback
}

func canonicalStableErrorCode(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func terminalError(terminal ModelCallTerminal) error {
	if terminal.Error != nil {
		return terminal.Error
	}
	return applicationError(foundation.ErrorNonRetryableFailure, stableTerminalErrorCode(terminal.ErrorCode, nil, ErrorCodeModelCallFailed), false, errors.New("model call failed"))
}

func sameStartedModelCall(expected, actual domain.ModelCall) bool {
	return actual.ID == expected.ID && actual.ModelRunID == expected.ModelRunID && actual.CallNo == expected.CallNo &&
		actual.Phase == expected.Phase && actual.Model == expected.Model && actual.Profile == expected.Profile &&
		actual.Prompt == expected.Prompt && actual.Schema == expected.Schema && actual.MaxOutputTokens == expected.MaxOutputTokens &&
		actual.Status == domain.ModelCallStarted && actual.RequestHash == expected.RequestHash && actual.RequestBytes == expected.RequestBytes &&
		actual.Version == expected.Version && actual.CompletedAt == nil && actual.StartedAt.Equal(expected.StartedAt)
}

func sameTerminalModelCall(expected, actual domain.ModelCall) bool {
	return reflect.DeepEqual(expected, actual)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
