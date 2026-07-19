package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	recordingPersistenceTimeout    = 5 * time.Second
)

// RecordingChatModelDependencies 是记录型 ChatModel 的运行依赖。
type RecordingChatModelDependencies struct {
	Model       ChatModel
	Repository  ModelRunRepository
	WorkspaceID foundation.ID
	ModelRunID  foundation.ID
	// StartingCallNo 允许同一 Model Run 在 generation calls 后继续记录 REVIEW；零值按 1 处理。
	StartingCallNo int
	IDs            foundation.IDGenerator
	Clock          foundation.Clock
}

// RecordingChatModel 在每次 Provider 调用前后持久化不含正文的 Model Call 事实。
type RecordingChatModel struct {
	model       ChatModel
	repository  ModelRunRepository
	workspaceID foundation.ID
	modelRunID  foundation.ID
	ids         foundation.IDGenerator
	clock       foundation.Clock

	mu         sync.Mutex
	nextCallNo int
}

// NewRecordingChatModel 创建一个单 Model Run 使用的记录型 ChatModel。
func NewRecordingChatModel(dependencies RecordingChatModelDependencies) (*RecordingChatModel, error) {
	if isNilChatModel(dependencies.Model) || isNilPort(dependencies.Repository) ||
		!canonicalApplicationID(dependencies.WorkspaceID) || !canonicalApplicationID(dependencies.ModelRunID) ||
		dependencies.StartingCallNo < 0 || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeModelCallPersistenceUnknown, false, errors.New("recording chat model dependencies are incomplete"))
	}
	startingCallNo := dependencies.StartingCallNo
	if startingCallNo == 0 {
		startingCallNo = 1
	}
	return &RecordingChatModel{
		model: dependencies.Model, repository: dependencies.Repository,
		workspaceID: dependencies.WorkspaceID, modelRunID: dependencies.ModelRunID,
		ids: dependencies.IDs, clock: dependencies.Clock, nextCallNo: startingCallNo,
	}, nil
}

// Chat 在调用 Provider 前写 STARTED，并在返回后以 CAS 写入成功或失败结果。
func (model *RecordingChatModel) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if model == nil || isNilChatModel(model.model) || isNilPort(model.repository) {
		return ChatResponse{}, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeModelCallPersistenceUnknown, false, errors.New("recording chat model is unavailable"))
	}
	if err := ValidateChatRequest(request); err != nil {
		return ChatResponse{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestDocument, err := json.Marshal(request)
	if err != nil {
		return ChatResponse{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRequestEncode, false, errors.New("chat request could not be encoded"))
	}
	callID, err := model.ids.New()
	if err != nil {
		return ChatResponse{}, err
	}
	startedAt := model.clock.Now()
	call := domain.ModelCall{
		ID: callID, ModelRunID: model.modelRunID, CallNo: model.takeCallNo(), Phase: request.Phase,
		Model: request.Model, Profile: request.ProfileRef, Prompt: request.PromptRef, Schema: request.SchemaRef,
		MaxOutputTokens: request.MaxOutputTokens,
		Status:          domain.ModelCallStarted, RequestHash: sha256Hex(requestDocument), RequestBytes: int64(len(requestDocument)),
		Version: 1, StartedAt: startedAt,
	}
	started, replayed, err := model.repository.StartModelCall(ctx, model.workspaceID, call)
	if err != nil {
		return ChatResponse{}, err
	}
	if replayed || started.Status != domain.ModelCallStarted || started.Version != 1 {
		return ChatResponse{}, applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallReplayUnsafe, false, errors.New("persisted model call cannot be safely replayed without a provider response"))
	}

	response, callErr := model.model.Chat(ctx, cloneChatRequest(request))
	if callErr == nil {
		callErr = ValidateChatResponse(request, response)
	}
	completedAt := model.clock.Now()
	if completedAt.Before(startedAt) {
		completedAt = startedAt
	}
	completed := started
	completed.Version++
	completed.CompletedAt = &completedAt
	completed.LatencyMillis = completedAt.Sub(startedAt).Milliseconds()
	if callErr == nil {
		completed.Status = domain.ModelCallSucceeded
		completed.ResponseHash = sha256Hex(response.Content)
		completed.ResponseBytes = int64(len(response.Content))
		completed.Usage = response.Usage
	} else {
		completed.Status = domain.ModelCallFailed
		completed.ErrorCode = stableAgentErrorCode(callErr, "AGENT_MODEL_CALL_FAILED")
		if len(response.Content) != 0 {
			completed.ResponseHash = sha256Hex(response.Content)
			completed.ResponseBytes = int64(len(response.Content))
			if response.Usage.Validate() == nil {
				completed.Usage = response.Usage
			}
		}
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordingPersistenceTimeout)
	_, _, completeErr := model.repository.CompleteModelCall(persistContext, CompleteModelCallCommand{
		WorkspaceID: model.workspaceID, ExpectedVersion: started.Version, Call: completed,
	})
	cancel()
	if completeErr != nil {
		return ChatResponse{}, applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallPersistenceUnknown, false, completeErr)
	}
	if callErr != nil {
		return ChatResponse{}, callErr
	}
	return cloneChatResponse(response), nil
}

func (model *RecordingChatModel) takeCallNo() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	value := model.nextCallNo
	model.nextCallNo++
	return value
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func stableAgentErrorCode(err error, fallback string) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code
	}
	return fallback
}

var _ ChatModel = (*RecordingChatModel)(nil)
