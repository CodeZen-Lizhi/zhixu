package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// MaxIdempotencyKeyLength 是 Workflow 控制命令允许的最大幂等键长度。
	MaxIdempotencyKeyLength = 128
	controlRequestSchema    = 1
)

// ClaimDisposition 表示一次 River delivery 在 Workflow 事实源中的处理资格。
type ClaimDisposition string

const (
	// ClaimDispositionClaimed 表示成功创建了新的活动 Attempt。
	ClaimDispositionClaimed ClaimDisposition = "claimed"
	// ClaimDispositionStale 表示 delivery 因 pause、cancel 或 generation 过期而安全失效。
	ClaimDispositionStale ClaimDisposition = "stale"
)

// ControlAction 是持久化 Workflow 控制命令的稳定动作名。
type ControlAction string

const (
	// ControlActionPause 请求 Run 在安全检查点暂停。
	ControlActionPause ControlAction = "pause"
	// ControlActionResume 恢复已暂停 Run 的合法工作。
	ControlActionResume ControlAction = "resume"
	// ControlActionCancel 请求 Run 在安全检查点取消。
	ControlActionCancel ControlAction = "cancel"
)

// RuntimeStatePort 隐藏 PostgreSQL DB-time lease、结果事务和控制命令持久化实现。
type RuntimeStatePort interface {
	// Claim 使用数据库时间判断 delivery 资格并原子追加 Attempt。
	Claim(context.Context, ClaimCommand) (ClaimResult, error)
	// Heartbeat 使用数据库时间续租并执行完整 lease fence CAS。
	Heartbeat(context.Context, HeartbeatCommand) (HeartbeatResult, error)
	// TransitionDelivery 原子归约成功或失败结果及其后继、Job 和 Outbox。
	TransitionDelivery(context.Context, DeliveryTransition) (DeliveryTransitionResult, error)
	// Control 幂等执行 Pause、Resume 或 Cancel。
	Control(context.Context, ControlTransition) (ControlPersistenceResult, error)
}

// CancellationSafetyGuard 判断一个 Node 的外部副作用是否已到达可终态取消的持久检查点。
// transaction 是 Runtime 当前事务的 opaque handle，保证判定与控制状态原子；未开始
// 副作用的 Node 必须返回 true，查询失败必须返回错误并由 Runtime fail closed。
type CancellationSafetyGuard interface {
	SafeToCancelWorkflowNode(context.Context, any, foundation.ID) (bool, error)
}

// ClaimCommand 只传 lease duration，不允许调用方提交 now 或绝对 lease_until。
type ClaimCommand struct {
	NodeRunID       foundation.ID
	DispatchNo      int
	DeliveryID      string
	RiverJobID      int64
	RiverJobAttempt int
	LeaseOwner      string
	LeaseDuration   time.Duration
}

// ClaimResult 返回 DB-time Claim 的资格与持久 Attempt。
type ClaimResult struct {
	Disposition       ClaimDisposition
	Run               domain.Run
	Node              domain.NodeRun
	Attempt           domain.NodeAttempt
	ObservedNodeKind  string
	DuplicateDelivery bool
	LeaseReclaimed    bool
}

// HeartbeatCommand 使用完整 fence 与 duration 请求数据库续租。
type HeartbeatCommand struct {
	NodeRunID     foundation.ID
	Fence         domain.LeaseFence
	LeaseDuration time.Duration
}

// HeartbeatResult 返回数据库时间续租后的 Node 与 Attempt 投影。
type HeartbeatResult struct {
	Node    domain.NodeRun
	Attempt domain.NodeAttempt
}

// DeliveryBinding 将一次结果事务绑定到稳定 generation、delivery 与 lease fence。
type DeliveryBinding struct {
	NodeRunID  foundation.ID
	DispatchNo int
	DeliveryID string
	Fence      domain.LeaseFence
}

// CompleteDeliveryCommand 是成功 Executor 结果的应用输入。
type CompleteDeliveryCommand struct {
	Binding             DeliveryBinding
	Output              json.RawMessage
	OutputSchemaVersion int
}

// FailDeliveryCommand 是失败 Executor 结果的应用输入。
type FailDeliveryCommand struct {
	Binding DeliveryBinding
	Failure domain.FailureInput
}

// DeliveryTransition 是交给事务端口的已分类、成功失败互斥结果。
type DeliveryTransition struct {
	Binding DeliveryBinding
	Result  domain.AttemptResult
}

// DeliveryTransitionResult 返回一次已提交或幂等重放的持久归约。
type DeliveryTransitionResult struct {
	Run      domain.Run
	Node     domain.NodeRun
	Attempt  domain.NodeAttempt
	Replayed bool
}

// RunControlCommand 是 HTTP/Application 可接受的控制命令，不包含 Workspace 覆盖值。
type RunControlCommand struct {
	WorkflowRunID   foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// ControlTransition 是交给事务端口的动作与稳定 request hash。
type ControlTransition struct {
	WorkflowRunID   foundation.ID
	Action          ControlAction
	ExpectedVersion int64
	IdempotencyKey  string
	RequestHash     string
}

// ControlPersistenceResult 是控制事务返回的持久 Run 投影。
type ControlPersistenceResult struct {
	WorkflowRunID foundation.ID
	Status        domain.RunStatus
	Version       int64
}

// RunControlResult 是 Pause、Resume、Cancel 的统一应用响应。
type RunControlResult struct {
	WorkflowRunID foundation.ID
	Status        domain.RunStatus
	Version       int64
	StatusURL     string
}

// RuntimeCoordinator 负责 Runtime Application 边界校验和纯编排。
type RuntimeCoordinator struct {
	state RuntimeStatePort
	human RuntimeHumanStatePort
}

// NewRuntimeCoordinator 构造不依赖数据库或 River 具体类型的 Runtime 协调器。
func NewRuntimeCoordinator(state RuntimeStatePort) (*RuntimeCoordinator, error) {
	if isNilRuntimeStatePort(state) {
		return nil, runtimeContractError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_STATE_PORT_MISSING")
	}
	coordinator := &RuntimeCoordinator{state: state}
	if human, ok := state.(RuntimeHumanStatePort); ok {
		coordinator.human = human
	}
	return coordinator, nil
}

// WaitForHuman exposes the optional Human outcome seam to the River Worker.
func (c *RuntimeCoordinator) WaitForHuman(ctx context.Context, command HumanWaitTransition) (HumanTransitionResult, error) {
	if c == nil || isNilRuntimeHumanStatePort(c.human) {
		return HumanTransitionResult{}, runtimeContractError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HUMAN_STATE_PORT_MISSING")
	}
	return c.human.WaitForHuman(ctx, command)
}

// SubmitHuman exposes the optional Human decision seam to API composition.
func (c *RuntimeCoordinator) SubmitHuman(ctx context.Context, command HumanDecisionTransition) (HumanTransitionResult, error) {
	if c == nil || isNilRuntimeHumanStatePort(c.human) {
		return HumanTransitionResult{}, runtimeContractError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HUMAN_STATE_PORT_MISSING")
	}
	return c.human.SubmitHuman(ctx, command)
}

// Claim 校验稳定 delivery 后把 DB-time Claim 委托给状态端口。
func (c *RuntimeCoordinator) Claim(ctx context.Context, command ClaimCommand) (ClaimResult, error) {
	command.LeaseOwner = strings.TrimSpace(command.LeaseOwner)
	command.DeliveryID = strings.TrimSpace(command.DeliveryID)
	if !isValidClaimCommand(command) {
		return ClaimResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_CLAIM_INVALID")
	}
	result, err := c.state.Claim(ctx, command)
	if err != nil {
		return ClaimResult{}, err
	}
	if !isValidClaimResult(command, result) {
		return ClaimResult{}, runtimeContractError(foundation.ErrorConsistencyViolation, "WORKFLOW_CLAIM_RESULT_INVALID")
	}
	return result, nil
}

// Heartbeat 校验完整 fence 后把 duration 交给 DB-time 续租端口。
func (c *RuntimeCoordinator) Heartbeat(ctx context.Context, command HeartbeatCommand) (HeartbeatResult, error) {
	command.Fence.Owner = strings.TrimSpace(command.Fence.Owner)
	if command.NodeRunID == "" || command.Fence.Owner == "" || command.Fence.AttemptNo < 1 || command.Fence.NodeVersion < 1 || command.LeaseDuration <= 0 {
		return HeartbeatResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_HEARTBEAT_INVALID")
	}
	result, err := c.state.Heartbeat(ctx, command)
	if err != nil {
		return HeartbeatResult{}, err
	}
	if result.Node.ID != command.NodeRunID || result.Node.Status != domain.NodeStatusRunning || result.Node.Version <= command.Fence.NodeVersion || result.Attempt.NodeRunID != command.NodeRunID || result.Attempt.AttemptNo != command.Fence.AttemptNo || result.Attempt.LeaseOwner != command.Fence.Owner || result.Attempt.Status != domain.AttemptStatusRunning {
		return HeartbeatResult{}, runtimeContractError(foundation.ErrorConsistencyViolation, "WORKFLOW_HEARTBEAT_RESULT_INVALID")
	}
	return result, nil
}

// Complete 校验并 canonicalize 成功输出后提交单一结果事务。
func (c *RuntimeCoordinator) Complete(ctx context.Context, command CompleteDeliveryCommand) (DeliveryTransitionResult, error) {
	canonicalOutput, err := canonicalRuntimeInput(command.Output)
	if err != nil {
		return DeliveryTransitionResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_DELIVERY_RESULT_INVALID")
	}
	result := domain.AttemptResult{Output: canonicalOutput, OutputSchemaVersion: command.OutputSchemaVersion}
	if !isValidDeliveryBinding(command.Binding) || domain.ValidateAttemptResult(result) != nil {
		return DeliveryTransitionResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_DELIVERY_RESULT_INVALID")
	}
	return c.transition(ctx, DeliveryTransition{Binding: normalizedDeliveryBinding(command.Binding), Result: result})
}

// Fail 按 Domain 唯一分类器生成安全 FailureEnvelope 后提交单一结果事务。
func (c *RuntimeCoordinator) Fail(ctx context.Context, command FailDeliveryCommand) (DeliveryTransitionResult, error) {
	if !isValidDeliveryBinding(command.Binding) {
		return DeliveryTransitionResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_DELIVERY_RESULT_INVALID")
	}
	failure, err := domain.ClassifyFailure(command.Failure)
	if err != nil {
		return DeliveryTransitionResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_DELIVERY_RESULT_INVALID")
	}
	result := domain.AttemptResult{Failure: &failure}
	if err := domain.ValidateAttemptResult(result); err != nil {
		return DeliveryTransitionResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_DELIVERY_RESULT_INVALID")
	}
	return c.transition(ctx, DeliveryTransition{Binding: normalizedDeliveryBinding(command.Binding), Result: result})
}

// Pause 幂等请求 Run 在安全检查点暂停。
func (c *RuntimeCoordinator) Pause(ctx context.Context, command RunControlCommand) (RunControlResult, error) {
	return c.control(ctx, ControlActionPause, command)
}

// Resume 幂等恢复已暂停 Run 的合法工作。
func (c *RuntimeCoordinator) Resume(ctx context.Context, command RunControlCommand) (RunControlResult, error) {
	return c.control(ctx, ControlActionResume, command)
}

// Cancel 幂等请求 Run 在安全检查点取消。
func (c *RuntimeCoordinator) Cancel(ctx context.Context, command RunControlCommand) (RunControlResult, error) {
	return c.control(ctx, ControlActionCancel, command)
}

func (c *RuntimeCoordinator) transition(ctx context.Context, command DeliveryTransition) (DeliveryTransitionResult, error) {
	result, err := c.state.TransitionDelivery(ctx, command)
	if err != nil {
		return DeliveryTransitionResult{}, err
	}
	if result.Node.ID != command.Binding.NodeRunID || result.Run.ID == "" || result.Node.RunID != result.Run.ID || result.Attempt.NodeRunID != command.Binding.NodeRunID || result.Attempt.AttemptNo != command.Binding.Fence.AttemptNo || !validTransitionOutcome(command.Result, result) {
		return DeliveryTransitionResult{}, runtimeContractError(foundation.ErrorConsistencyViolation, "WORKFLOW_DELIVERY_TRANSITION_RESULT_INVALID")
	}
	return result, nil
}

func validTransitionOutcome(request domain.AttemptResult, persisted DeliveryTransitionResult) bool {
	if persisted.Attempt.Status == domain.AttemptStatusCancelled {
		if persisted.Node.Status == domain.NodeStatusPaused && (persisted.Run.Status == domain.RunStatusPaused || persisted.Run.Status == domain.RunStatusRunning) {
			return true
		}
		if persisted.Node.Status == domain.NodeStatusCancelled && (persisted.Run.Status == domain.RunStatusCancelled || persisted.Run.Status == domain.RunStatusRunning) {
			return true
		}
	}
	if request.Failure == nil {
		return persisted.Attempt.Status == domain.AttemptStatusSucceeded && persisted.Node.Status == domain.NodeStatusSucceeded
	}
	switch request.Failure.Class {
	case domain.FailureClassRetryable:
		return (persisted.Attempt.Status == domain.AttemptStatusRetryScheduled && persisted.Node.Status == domain.NodeStatusRetryWait) || (persisted.Attempt.Status == domain.AttemptStatusFailed && persisted.Node.Status == domain.NodeStatusFailed)
	case domain.FailureClassNonRetryable:
		return persisted.Attempt.Status == domain.AttemptStatusFailed && persisted.Node.Status == domain.NodeStatusFailed
	case domain.FailureClassManualRecovery:
		return persisted.Attempt.Status == domain.AttemptStatusManualRecovery && persisted.Node.Status == domain.NodeStatusFailed
	case domain.FailureClassLeaseLost:
		return persisted.Attempt.Status == domain.AttemptStatusLeaseLost
	case domain.FailureClassCancelled:
		return persisted.Attempt.Status == domain.AttemptStatusCancelled && persisted.Node.Status == domain.NodeStatusCancelled
	default:
		return false
	}
}

func (c *RuntimeCoordinator) control(ctx context.Context, action ControlAction, command RunControlCommand) (RunControlResult, error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if command.IdempotencyKey == "" {
		return RunControlResult{}, runtimeContractError(foundation.ErrorInvalidInput, "IDEMPOTENCY_KEY_REQUIRED")
	}
	if command.WorkflowRunID == "" || command.ExpectedVersion < 1 || len(command.IdempotencyKey) > MaxIdempotencyKeyLength {
		return RunControlResult{}, runtimeContractError(foundation.ErrorInvalidInput, "WORKFLOW_CONTROL_INVALID")
	}
	transition := ControlTransition{WorkflowRunID: command.WorkflowRunID, Action: action, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: command.IdempotencyKey}
	transition.RequestHash = controlRequestHash(transition)
	persisted, err := c.state.Control(ctx, transition)
	if err != nil {
		return RunControlResult{}, err
	}
	if persisted.WorkflowRunID != command.WorkflowRunID || persisted.Version <= command.ExpectedVersion || !knownRunStatus(persisted.Status) {
		return RunControlResult{}, runtimeContractError(foundation.ErrorConsistencyViolation, "WORKFLOW_CONTROL_RESULT_INVALID")
	}
	return RunControlResult{WorkflowRunID: persisted.WorkflowRunID, Status: persisted.Status, Version: persisted.Version, StatusURL: "/api/v1/workflows/" + string(persisted.WorkflowRunID)}, nil
}

func isValidClaimCommand(command ClaimCommand) bool {
	return command.NodeRunID != "" && command.DispatchNo >= 1 && command.DeliveryID != "" && command.RiverJobID > 0 && command.RiverJobAttempt >= 0 && command.LeaseOwner != "" && command.LeaseDuration > 0
}

func isValidClaimResult(command ClaimCommand, result ClaimResult) bool {
	if result.Disposition == ClaimDispositionStale {
		return result.Run.ID == "" && result.Node.ID == "" && result.Attempt.NodeRunID == "" && !result.LeaseReclaimed && (!result.DuplicateDelivery || strings.TrimSpace(result.ObservedNodeKind) != "")
	}
	return result.Disposition == ClaimDispositionClaimed && result.Run.ID != "" && result.Node.ID == command.NodeRunID && result.Node.RunID == result.Run.ID && result.Node.Status == domain.NodeStatusRunning && (result.ObservedNodeKind == "" || result.ObservedNodeKind == result.Node.NodeType) && (!result.LeaseReclaimed || result.DuplicateDelivery) && result.Attempt.NodeRunID == command.NodeRunID && result.Attempt.AttemptNo >= 1 && result.Attempt.DispatchNo == command.DispatchNo && result.Attempt.DeliveryID == command.DeliveryID && result.Attempt.RiverJobID == command.RiverJobID && result.Attempt.RiverJobAttempt == command.RiverJobAttempt && result.Attempt.LeaseOwner == command.LeaseOwner && result.Attempt.Status == domain.AttemptStatusRunning
}

func isValidDeliveryBinding(binding DeliveryBinding) bool {
	return binding.NodeRunID != "" && binding.DispatchNo >= 1 && strings.TrimSpace(binding.DeliveryID) != "" && strings.TrimSpace(binding.Fence.Owner) != "" && binding.Fence.AttemptNo >= 1 && binding.Fence.NodeVersion >= 1
}

func normalizedDeliveryBinding(binding DeliveryBinding) DeliveryBinding {
	binding.DeliveryID = strings.TrimSpace(binding.DeliveryID)
	binding.Fence.Owner = strings.TrimSpace(binding.Fence.Owner)
	return binding
}

func controlRequestHash(command ControlTransition) string {
	value := strconv.Itoa(controlRequestSchema) + "\x00" + string(command.WorkflowRunID) + "\x00" + string(command.Action) + "\x00" + strconv.FormatInt(command.ExpectedVersion, 10) + "\x00" + command.IdempotencyKey
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func knownRunStatus(status domain.RunStatus) bool {
	switch status {
	case domain.RunStatusPending, domain.RunStatusRunning, domain.RunStatusWaitingForHuman, domain.RunStatusRetryWait, domain.RunStatusPaused, domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func isNilRuntimeStatePort(state RuntimeStatePort) bool {
	if state == nil {
		return true
	}
	value := reflect.ValueOf(state)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func runtimeContractError(kind foundation.ErrorKind, code string) error {
	return foundation.NewError(kind, code, false, errors.New("workflow runtime application contract is invalid"))
}
