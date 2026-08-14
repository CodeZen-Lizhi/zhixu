package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxRunBudgetLedgerCalls  = domain.MaxModelCallsPerRun
	maxRunBudgetLedgerTokens = MaxRunTokens
)

const (
	// ErrorCodeRunBudgetLedgerInvalid 表示 Attempt 级预算配置不完整或超出硬上限。
	ErrorCodeRunBudgetLedgerInvalid = "AGENT_RUN_BUDGET_LEDGER_INVALID"
	// ErrorCodeRunBudgetLedgerDeadline 表示不能在 Node Attempt deadline 后发起调用。
	ErrorCodeRunBudgetLedgerDeadline = "AGENT_RUN_BUDGET_DEADLINE_EXCEEDED"
	// ErrorCodeRunBudgetLedgerModelCalls 表示模型调用额度已耗尽。
	ErrorCodeRunBudgetLedgerModelCalls = "AGENT_RUN_BUDGET_MODEL_CALLS_EXHAUSTED"
	// ErrorCodeRunBudgetLedgerIterations 表示 Agent 迭代额度已耗尽。
	ErrorCodeRunBudgetLedgerIterations = "AGENT_RUN_BUDGET_AGENT_ITERATIONS_EXHAUSTED"
	// ErrorCodeRunBudgetLedgerToolCalls 表示 Tool 调用额度已耗尽。
	ErrorCodeRunBudgetLedgerToolCalls = "AGENT_RUN_BUDGET_TOOL_CALLS_EXHAUSTED"
	// ErrorCodeRunBudgetLedgerInputTokens 表示输入 Token 额度已耗尽。
	ErrorCodeRunBudgetLedgerInputTokens = "AGENT_RUN_BUDGET_INPUT_TOKENS_EXHAUSTED"
	// ErrorCodeRunBudgetLedgerOutputTokens 表示输出 Token 额度已耗尽。
	ErrorCodeRunBudgetLedgerOutputTokens = "AGENT_RUN_BUDGET_OUTPUT_TOKENS_EXHAUSTED"
	// ErrorCodeRunBudgetLedgerPhase 表示下游阶段未预留、重复授权或不受支持。
	ErrorCodeRunBudgetLedgerPhase = "AGENT_RUN_BUDGET_PHASE_INVALID"
	// ErrorCodeRunBudgetLedgerSettlement 表示同一授权被重复结算，或实际 usage 超出保留下游额度后的运行总预算。
	ErrorCodeRunBudgetLedgerSettlement = "AGENT_RUN_BUDGET_SETTLEMENT_INVALID"
)

// RunBudgetPhase 直接复用项目 ModelCall phase，不依赖 Provider 或 Eino 类型。
type RunBudgetPhase = domain.ModelCallPhase

const (
	RunBudgetPhasePlan    = domain.ModelCallPlan
	RunBudgetPhaseAgent   = domain.ModelCallAgent
	RunBudgetPhaseAnswer  = domain.ModelCallAnswer
	RunBudgetPhaseInitial = domain.ModelCallInitial
	RunBudgetPhaseRepair  = domain.ModelCallRepair
	RunBudgetPhaseReduced = domain.ModelCallReduced
	RunBudgetPhaseReview  = domain.ModelCallReview
)

var downstreamBudgetPhases = [...]RunBudgetPhase{
	RunBudgetPhaseAnswer,
	RunBudgetPhaseInitial,
	RunBudgetPhaseRepair,
	RunBudgetPhaseReduced,
	RunBudgetPhaseReview,
}

// RunBudgetReservation 是一次模型调用在 Provider 调用前锁定的预占额度。
// Provider 可能把隐藏 reasoning 计入实际 output usage；结算时只有未预占的运行总预算可以吸收该差额，
// 已为下游阶段预留的额度不可借用。下游阶段每个 reservation 必须恰好包含一次模型调用。
type RunBudgetReservation struct {
	ModelCalls           int
	ReservedInputTokens  int64
	ReservedOutputTokens int64
}

// RunBudgetUsage 是 Provider 返回的实际 Token 使用量。
// nil usage 表示 Provider 未返回用量，账本会按授权上限扣账。
type RunBudgetUsage struct {
	InputTokens  int64
	OutputTokens int64
}

// RunBudgetLedgerConfig 将预算严格绑定到一个 Node Attempt 和其唯一 Model Run。
type RunBudgetLedgerConfig struct {
	NodeAttemptID foundation.ID
	ModelRunID    foundation.ID

	MaxTotalModelCalls int
	MaxAgentIterations int
	MaxToolCalls       int
	MaxInputTokens     int64
	MaxOutputTokens    int64
	Deadline           time.Time
	Clock              foundation.Clock

	// DownstreamReservations 必须为 ANSWER、INITIAL、REPAIR、REDUCED、REVIEW
	// 分别保留一次模型调用和最大输入/输出 Token。
	DownstreamReservations map[RunBudgetPhase]RunBudgetReservation
}

// RunBudgetLedger 在单个 Node Attempt 内串行化所有模型、Agent 和 Tool 额度。
// 它只管理进程内授权；持久 ModelCall/ToolCall 事实由后续调用方拥有。
type RunBudgetLedger struct {
	mu sync.Mutex

	config RunBudgetLedgerConfig
	clock  foundation.Clock

	modelCalls      int
	agentIterations int
	toolCalls       int
	planAuthorized  bool
	inputTokens     int64
	outputTokens    int64
	inFlightInput   int64
	inFlightOutput  int64
	pending         map[RunBudgetPhase]RunBudgetReservation
	authorizations  map[uint64]runBudgetAuthorizationState
	nextID          uint64
}

type runBudgetAuthorizationState struct {
	phase   RunBudgetPhase
	maximum RunBudgetReservation
	settled bool
}

// RunBudgetAuthorization 代表已获准执行的一次模型调用。
// 调用方必须在 Provider 返回后恰好调用一次 Settle。
type RunBudgetAuthorization struct {
	ledger  *RunBudgetLedger
	id      uint64
	CallNo  int
	Phase   RunBudgetPhase
	Maximum RunBudgetReservation
}

// RunBudgetLedgerSnapshot 仅暴露可审计的计数快照，不包含 Prompt 或 Provider 内容。
type RunBudgetLedgerSnapshot struct {
	NodeAttemptID foundation.ID
	ModelRunID    foundation.ID
	Deadline      time.Time

	ModelCalls      int
	AgentIterations int
	ToolCalls       int
	InputTokens     int64
	OutputTokens    int64
	InFlightInput   int64
	InFlightOutput  int64

	ReservedModelCalls   int
	ReservedInputTokens  int64
	ReservedOutputTokens int64
}

// NewRunBudgetLedger 创建一个不含 Provider/Eino 依赖且并发安全的 Attempt 级账本。
func NewRunBudgetLedger(config RunBudgetLedgerConfig) (*RunBudgetLedger, error) {
	if err := validateRunBudgetLedgerConfig(config); err != nil {
		return nil, err
	}
	pending := make(map[RunBudgetPhase]RunBudgetReservation, len(config.DownstreamReservations))
	for phase, reservation := range config.DownstreamReservations {
		pending[phase] = reservation
	}
	config.DownstreamReservations = nil
	return &RunBudgetLedger{
		config: config, clock: config.Clock, pending: pending, authorizations: make(map[uint64]runBudgetAuthorizationState),
	}, nil
}

// AuthorizePlanCall 为可选 PLAN 调用预占额度；同一 Attempt 只允许一次。
func (ledger *RunBudgetLedger) AuthorizePlanCall(maximum RunBudgetReservation) (*RunBudgetAuthorization, error) {
	if ledger == nil {
		return nil, runBudgetLedgerError(foundation.ErrorDependencyUnavailable, ErrorCodeRunBudgetLedgerInvalid, errors.New("run budget ledger is nil"))
	}
	if err := validateModelReservation(maximum); err != nil {
		return nil, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.authorizeBeforeDeadline(); err != nil {
		return nil, err
	}
	if ledger.planAuthorized {
		return nil, runBudgetLedgerError(foundation.ErrorConsistencyViolation, ErrorCodeRunBudgetLedgerPhase, errors.New("plan phase is already authorized"))
	}
	authorization, err := ledger.authorizeModelCall(RunBudgetPhasePlan, maximum, false)
	if err != nil {
		return nil, err
	}
	ledger.planAuthorized = true
	return authorization, nil
}

// AuthorizeAgentCall 为下一次 Agent 模型调用预占最大额度，同时保留全部未执行下游阶段的额度。
func (ledger *RunBudgetLedger) AuthorizeAgentCall(maximum RunBudgetReservation) (*RunBudgetAuthorization, error) {
	if ledger == nil {
		return nil, runBudgetLedgerError(foundation.ErrorDependencyUnavailable, ErrorCodeRunBudgetLedgerInvalid, errors.New("run budget ledger is nil"))
	}
	if err := validateModelReservation(maximum); err != nil {
		return nil, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.authorizeBeforeDeadline(); err != nil {
		return nil, err
	}
	if ledger.agentIterations >= ledger.config.MaxAgentIterations {
		return nil, runBudgetLedgerError(foundation.ErrorNonRetryableFailure, ErrorCodeRunBudgetLedgerIterations, errors.New("agent iteration budget is exhausted"))
	}
	return ledger.authorizeModelCall(RunBudgetPhaseAgent, maximum, true)
}

// AuthorizeDownstreamCall 兑现一个预留的 ANSWER、metadata 或 REVIEW 阶段调用。
func (ledger *RunBudgetLedger) AuthorizeDownstreamCall(phase RunBudgetPhase) (*RunBudgetAuthorization, error) {
	if ledger == nil {
		return nil, runBudgetLedgerError(foundation.ErrorDependencyUnavailable, ErrorCodeRunBudgetLedgerInvalid, errors.New("run budget ledger is nil"))
	}
	if !isDownstreamBudgetPhase(phase) {
		return nil, runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerPhase, errors.New("run budget phase is not downstream"))
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.authorizeBeforeDeadline(); err != nil {
		return nil, err
	}
	maximum, found := ledger.pending[phase]
	if !found {
		return nil, runBudgetLedgerError(foundation.ErrorConsistencyViolation, ErrorCodeRunBudgetLedgerPhase, errors.New("downstream phase is already authorized"))
	}
	delete(ledger.pending, phase)
	authorization, err := ledger.authorizeModelCall(phase, maximum, false)
	if err != nil {
		ledger.pending[phase] = maximum
		return nil, err
	}
	return authorization, nil
}

// AuthorizeToolCall 消耗一次顺序 Tool 调用额度。
func (ledger *RunBudgetLedger) AuthorizeToolCall() error {
	if ledger == nil {
		return runBudgetLedgerError(foundation.ErrorDependencyUnavailable, ErrorCodeRunBudgetLedgerInvalid, errors.New("run budget ledger is nil"))
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.authorizeBeforeDeadline(); err != nil {
		return err
	}
	if ledger.toolCalls >= ledger.config.MaxToolCalls {
		return runBudgetLedgerError(foundation.ErrorNonRetryableFailure, ErrorCodeRunBudgetLedgerToolCalls, errors.New("tool call budget is exhausted"))
	}
	ledger.toolCalls++
	return nil
}

// Settle 写入实际 Provider usage；缺失 usage 时按预占额度扣账。
// 实际 usage 可以超过本次预占，但必须落在保留其他 in-flight 与下游 reservation 后的运行总预算内。
func (authorization *RunBudgetAuthorization) Settle(usage *RunBudgetUsage) error {
	if authorization == nil || authorization.ledger == nil {
		return runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerSettlement, errors.New("run budget authorization is nil"))
	}
	ledger := authorization.ledger
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	state, found := ledger.authorizations[authorization.id]
	if !found || state.settled || state.phase != authorization.Phase || state.maximum != authorization.Maximum {
		return runBudgetLedgerError(foundation.ErrorConsistencyViolation, ErrorCodeRunBudgetLedgerSettlement, errors.New("run budget authorization is already settled or does not belong to this ledger"))
	}
	charged := state.maximum
	if usage != nil {
		if err := validateRunBudgetUsage(*usage); err != nil {
			return err
		}
		charged.ReservedInputTokens = usage.InputTokens
		charged.ReservedOutputTokens = usage.OutputTokens
		if !ledger.settlementFitsRunBudget(state, charged) {
			ledger.settleAuthorization(authorization.id, state, state.maximum)
			return runBudgetLedgerError(foundation.ErrorConsistencyViolation, ErrorCodeRunBudgetLedgerSettlement, errors.New("provider usage exceeded the remaining run budget"))
		}
	}
	ledger.settleAuthorization(authorization.id, state, charged)
	return nil
}

func (ledger *RunBudgetLedger) settlementFitsRunBudget(state runBudgetAuthorizationState, charged RunBudgetReservation) bool {
	pending := ledger.pendingTotals()
	otherInFlightInput := ledger.inFlightInput - state.maximum.ReservedInputTokens
	otherInFlightOutput := ledger.inFlightOutput - state.maximum.ReservedOutputTokens
	if otherInFlightInput < 0 || otherInFlightOutput < 0 ||
		ledger.inputTokens > ledger.config.MaxInputTokens-otherInFlightInput-pending.ReservedInputTokens ||
		ledger.outputTokens > ledger.config.MaxOutputTokens-otherInFlightOutput-pending.ReservedOutputTokens {
		return false
	}
	availableInput := ledger.config.MaxInputTokens - ledger.inputTokens - otherInFlightInput - pending.ReservedInputTokens
	availableOutput := ledger.config.MaxOutputTokens - ledger.outputTokens - otherInFlightOutput - pending.ReservedOutputTokens
	return charged.ReservedInputTokens <= availableInput && charged.ReservedOutputTokens <= availableOutput
}

// Snapshot 返回某一时刻的账本计数，供运行期绑定校验与审计调用方使用。
func (ledger *RunBudgetLedger) Snapshot() RunBudgetLedgerSnapshot {
	if ledger == nil {
		return RunBudgetLedgerSnapshot{}
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	snapshot := RunBudgetLedgerSnapshot{
		NodeAttemptID: ledger.config.NodeAttemptID, ModelRunID: ledger.config.ModelRunID, Deadline: ledger.config.Deadline,
		ModelCalls: ledger.modelCalls, AgentIterations: ledger.agentIterations, ToolCalls: ledger.toolCalls,
		InputTokens: ledger.inputTokens, OutputTokens: ledger.outputTokens,
		InFlightInput: ledger.inFlightInput, InFlightOutput: ledger.inFlightOutput,
	}
	for _, reservation := range ledger.pending {
		snapshot.ReservedModelCalls += reservation.ModelCalls
		snapshot.ReservedInputTokens += reservation.ReservedInputTokens
		snapshot.ReservedOutputTokens += reservation.ReservedOutputTokens
	}
	return snapshot
}

func (ledger *RunBudgetLedger) authorizeBeforeDeadline() error {
	if ledger.clock == nil || !ledger.clock.Now().Before(ledger.config.Deadline) {
		return runBudgetLedgerError(foundation.ErrorRetryableFailure, ErrorCodeRunBudgetLedgerDeadline, context.DeadlineExceeded)
	}
	return nil
}

func (ledger *RunBudgetLedger) authorizeModelCall(phase RunBudgetPhase, maximum RunBudgetReservation, agent bool) (*RunBudgetAuthorization, error) {
	pending := ledger.pendingTotals()
	if ledger.modelCalls > ledger.config.MaxTotalModelCalls-pending.ModelCalls-maximum.ModelCalls {
		return nil, runBudgetLedgerError(foundation.ErrorNonRetryableFailure, ErrorCodeRunBudgetLedgerModelCalls, errors.New("model call budget is exhausted"))
	}
	if ledger.inputTokens > ledger.config.MaxInputTokens-ledger.inFlightInput-pending.ReservedInputTokens-maximum.ReservedInputTokens {
		return nil, runBudgetLedgerError(foundation.ErrorNonRetryableFailure, ErrorCodeRunBudgetLedgerInputTokens, errors.New("input token budget is exhausted"))
	}
	if ledger.outputTokens > ledger.config.MaxOutputTokens-ledger.inFlightOutput-pending.ReservedOutputTokens-maximum.ReservedOutputTokens {
		return nil, runBudgetLedgerError(foundation.ErrorNonRetryableFailure, ErrorCodeRunBudgetLedgerOutputTokens, errors.New("output token budget is exhausted"))
	}
	ledger.modelCalls += maximum.ModelCalls
	ledger.inFlightInput += maximum.ReservedInputTokens
	ledger.inFlightOutput += maximum.ReservedOutputTokens
	if agent {
		ledger.agentIterations++
	}
	ledger.nextID++
	state := runBudgetAuthorizationState{phase: phase, maximum: maximum}
	ledger.authorizations[ledger.nextID] = state
	return &RunBudgetAuthorization{ledger: ledger, id: ledger.nextID, CallNo: ledger.modelCalls, Phase: phase, Maximum: maximum}, nil
}

func (ledger *RunBudgetLedger) settleAuthorization(id uint64, state runBudgetAuthorizationState, charged RunBudgetReservation) {
	ledger.inFlightInput -= state.maximum.ReservedInputTokens
	ledger.inFlightOutput -= state.maximum.ReservedOutputTokens
	ledger.inputTokens += charged.ReservedInputTokens
	ledger.outputTokens += charged.ReservedOutputTokens
	state.settled = true
	ledger.authorizations[id] = state
}

func (ledger *RunBudgetLedger) pendingTotals() RunBudgetReservation {
	var total RunBudgetReservation
	for _, reservation := range ledger.pending {
		total.ModelCalls += reservation.ModelCalls
		total.ReservedInputTokens += reservation.ReservedInputTokens
		total.ReservedOutputTokens += reservation.ReservedOutputTokens
	}
	return total
}

func validateRunBudgetLedgerConfig(config RunBudgetLedgerConfig) error {
	if !canonicalApplicationID(config.NodeAttemptID) || !canonicalApplicationID(config.ModelRunID) || config.NodeAttemptID == config.ModelRunID ||
		config.MaxTotalModelCalls <= 0 || config.MaxTotalModelCalls > maxRunBudgetLedgerCalls ||
		config.MaxAgentIterations <= 0 || config.MaxAgentIterations > config.MaxTotalModelCalls ||
		config.MaxToolCalls < 0 || config.MaxToolCalls > maxRunBudgetLedgerCalls ||
		config.MaxInputTokens <= 0 || config.MaxInputTokens > maxRunBudgetLedgerTokens ||
		config.MaxOutputTokens <= 0 || config.MaxOutputTokens > maxRunBudgetLedgerTokens ||
		config.Clock == nil || config.Deadline.IsZero() || !config.Deadline.After(config.Clock.Now()) ||
		len(config.DownstreamReservations) != len(downstreamBudgetPhases) {
		return runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerInvalid, errors.New("run budget ledger configuration is invalid"))
	}
	var downstream RunBudgetReservation
	for _, phase := range downstreamBudgetPhases {
		reservation, found := config.DownstreamReservations[phase]
		if !found || validateModelReservation(reservation) != nil || !addReservation(&downstream, reservation) {
			return runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerInvalid, errors.New("downstream reservation is invalid"))
		}
	}
	if downstream.ModelCalls > config.MaxTotalModelCalls || downstream.ReservedInputTokens > config.MaxInputTokens || downstream.ReservedOutputTokens > config.MaxOutputTokens {
		return runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerInvalid, errors.New("downstream reservations exceed the run budget"))
	}
	return nil
}

func validateModelReservation(reservation RunBudgetReservation) error {
	if reservation.ModelCalls != 1 || reservation.ReservedInputTokens <= 0 || reservation.ReservedInputTokens > maxRunBudgetLedgerTokens ||
		reservation.ReservedOutputTokens <= 0 || reservation.ReservedOutputTokens > maxRunBudgetLedgerTokens {
		return runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerInvalid, errors.New("model call reservation is invalid"))
	}
	return nil
}

func validateRunBudgetUsage(usage RunBudgetUsage) error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return runBudgetLedgerError(foundation.ErrorInvalidInput, ErrorCodeRunBudgetLedgerSettlement, errors.New("provider usage is negative"))
	}
	return nil
}

func addReservation(total *RunBudgetReservation, next RunBudgetReservation) bool {
	if total.ModelCalls > maxRunBudgetLedgerCalls-next.ModelCalls ||
		total.ReservedInputTokens > maxRunBudgetLedgerTokens-next.ReservedInputTokens ||
		total.ReservedOutputTokens > maxRunBudgetLedgerTokens-next.ReservedOutputTokens {
		return false
	}
	total.ModelCalls += next.ModelCalls
	total.ReservedInputTokens += next.ReservedInputTokens
	total.ReservedOutputTokens += next.ReservedOutputTokens
	return true
}

func isDownstreamBudgetPhase(phase RunBudgetPhase) bool {
	for _, candidate := range downstreamBudgetPhases {
		if phase == candidate {
			return true
		}
	}
	return false
}

func runBudgetLedgerError(kind foundation.ErrorKind, code string, cause error) error {
	return applicationError(kind, code, false, cause)
}
