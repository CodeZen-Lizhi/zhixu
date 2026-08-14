package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RecordingChatModelDependencies 是记录型 ChatModel 的运行依赖。
type RecordingChatModelDependencies struct {
	Model       ChatModel
	Repository  ModelRunRepository
	WorkspaceID foundation.ID
	ModelRunID  foundation.ID
	// Recorder 与 BudgetLedger 必须同时提供，用于让 PLAN、结构化 metadata
	// 和 REVIEW 与 Eino Agent/ANSWER 共用一本 Attempt 级调用账本。
	Recorder     *ModelCallRecorder
	BudgetLedger *RunBudgetLedger
	// MaxInputTokens 是 PLAN/AGENT 调用的单次输入 Token 预占上限。
	MaxInputTokens int64
	// StartingCallNo 允许同一 Model Run 在已有调用后继续记录后续阶段；零值按 1 处理。
	StartingCallNo int
	IDs            foundation.IDGenerator
	Clock          foundation.Clock
}

// RecordingChatModel 在每次 Provider 调用前后持久化不含正文的 Model Call 事实。
type RecordingChatModel struct {
	model          ChatModel
	recorder       *ModelCallRecorder
	ledger         *RunBudgetLedger
	maxInputTokens int64

	mu         sync.Mutex
	nextCallNo int
}

// NewRecordingChatModel 创建一个单 Model Run 使用的记录型 ChatModel。
func NewRecordingChatModel(dependencies RecordingChatModelDependencies) (*RecordingChatModel, error) {
	sharedLedger := dependencies.BudgetLedger != nil || dependencies.Recorder != nil
	ledgerModelRunID := foundation.ID("")
	if dependencies.BudgetLedger != nil {
		ledgerModelRunID = dependencies.BudgetLedger.Snapshot().ModelRunID
	}
	if isNilChatModel(dependencies.Model) || dependencies.StartingCallNo < 0 ||
		(sharedLedger && (dependencies.BudgetLedger == nil || dependencies.Recorder == nil || dependencies.MaxInputTokens <= 0 ||
			dependencies.StartingCallNo != 0 || dependencies.Recorder.workspaceID != dependencies.WorkspaceID ||
			dependencies.Recorder.modelRunID != dependencies.ModelRunID || ledgerModelRunID != dependencies.ModelRunID)) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeModelCallPersistenceUnknown, false, errors.New("recording chat model dependencies are incomplete"))
	}
	recorder := dependencies.Recorder
	if recorder == nil {
		var err error
		recorder, err = NewModelCallRecorder(ModelCallRecorderDependencies{
			Repository: dependencies.Repository, WorkspaceID: dependencies.WorkspaceID, ModelRunID: dependencies.ModelRunID,
			IDs: dependencies.IDs, Clock: dependencies.Clock,
		})
		if err != nil {
			return nil, err
		}
	}
	startingCallNo := dependencies.StartingCallNo
	if startingCallNo == 0 {
		startingCallNo = 1
	}
	return &RecordingChatModel{
		model: dependencies.Model, recorder: recorder, ledger: dependencies.BudgetLedger,
		maxInputTokens: dependencies.MaxInputTokens, nextCallNo: startingCallNo,
	}, nil
}

// Chat 在调用 Provider 前写 STARTED，并在返回后以 CAS 写入成功或失败结果。
func (model *RecordingChatModel) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if model == nil || isNilChatModel(model.model) || model.recorder == nil {
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
	callNo := model.takeCallNo()
	var authorization *RunBudgetAuthorization
	if model.ledger != nil {
		authorization, err = model.authorize(request)
		if err != nil {
			return ChatResponse{}, err
		}
		callNo = authorization.CallNo
	}
	recording, err := model.recorder.Start(ctx, ModelCallStart{
		CallNo: callNo, Phase: request.Phase, Model: request.Model, Profile: request.ProfileRef,
		Prompt: request.PromptRef, Schema: request.SchemaRef, MaxOutputTokens: request.MaxOutputTokens,
		Request: requestDocument,
	})
	if err != nil {
		if authorization != nil {
			_ = authorization.Settle(nil)
		}
		return ChatResponse{}, err
	}

	response, callErr := model.model.Chat(ctx, cloneChatRequest(request))
	if callErr == nil {
		callErr = ValidateChatResponse(request, response)
	}
	terminal := ModelCallTerminal{Response: response.Content, Usage: response.Usage, Error: callErr}
	if callErr == nil {
		terminal.Status = domain.ModelCallSucceeded
	} else {
		terminal.Status = domain.ModelCallFailed
	}
	_, completeErr := recording.Complete(ctx, terminal)
	var settleErr error
	if authorization != nil {
		var usage *RunBudgetUsage
		if response.Usage.Validate() == nil && response.Usage.TotalTokens > 0 {
			usage = &RunBudgetUsage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}
		}
		settleErr = authorization.Settle(usage)
	}
	if completeErr != nil {
		return ChatResponse{}, completeErr
	}
	if settleErr != nil {
		return ChatResponse{}, settleErr
	}
	if callErr != nil {
		return ChatResponse{}, callErr
	}
	return cloneChatResponse(response), nil
}

func (model *RecordingChatModel) authorize(request ChatRequest) (*RunBudgetAuthorization, error) {
	maximum := RunBudgetReservation{
		ModelCalls: 1, ReservedInputTokens: model.maxInputTokens,
		ReservedOutputTokens: int64(request.MaxOutputTokens),
	}
	switch request.Phase {
	case domain.ModelCallPlan:
		return model.ledger.AuthorizePlanCall(maximum)
	case domain.ModelCallAgent:
		return model.ledger.AuthorizeAgentCall(maximum)
	case domain.ModelCallAnswer, domain.ModelCallInitial, domain.ModelCallRepair, domain.ModelCallReduced, domain.ModelCallReview:
		authorization, err := model.ledger.AuthorizeDownstreamCall(request.Phase)
		if err != nil {
			return nil, err
		}
		if authorization.Maximum.ReservedInputTokens < model.maxInputTokens ||
			authorization.Maximum.ReservedOutputTokens < int64(request.MaxOutputTokens) {
			_ = authorization.Settle(nil)
			return nil, applicationError(foundation.ErrorConsistencyViolation, ErrorCodeRunBudgetLedgerSettlement, false, errors.New("chat request exceeds its authorized token reservation"))
		}
		return authorization, nil
	default:
		return nil, applicationError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerPhase, false, errors.New("chat request phase cannot be authorized"))
	}
}

func (model *RecordingChatModel) takeCallNo() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	value := model.nextCallNo
	model.nextCallNo++
	return value
}

var _ ChatModel = (*RecordingChatModel)(nil)
