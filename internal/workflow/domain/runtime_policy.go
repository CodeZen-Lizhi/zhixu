package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxTrustedRetryAfter 是受信 Executor 可请求的最长业务重试等待时间。
	MaxTrustedRetryAfter = 24 * time.Hour
	// MaxFailureSummaryRunes 是持久化错误摘要允许的最大 Unicode 字符数。
	MaxFailureSummaryRunes = 256
	// RedactedFailureSummary 表示摘要命中敏感信息规则后写入的稳定替代文本。
	RedactedFailureSummary = "failure details redacted"
)

// FailureClass 是 Executor 失败归约到 Workflow 状态机的唯一分类。
type FailureClass string

const (
	// FailureClassRetryable 表示可消耗一次业务 retry 的失败。
	FailureClassRetryable FailureClass = "retryable"
	// FailureClassNonRetryable 表示不可自动重试的普通失败。
	FailureClassNonRetryable FailureClass = "non_retryable"
	// FailureClassManualRecovery 表示结果不确定或需要人工恢复。
	FailureClassManualRecovery FailureClass = "manual_recovery"
	// FailureClassLeaseLost 表示旧 owner 已失去 Workflow lease。
	FailureClassLeaseLost FailureClass = "lease_lost"
	// FailureClassCancelled 表示已由可信状态证明的取消。
	FailureClassCancelled FailureClass = "cancelled"
)

// FailureInput 是受信 Executor 结果进入领域分类器的输入。
type FailureInput struct {
	Err                error
	Explicit           *FailureEnvelope
	CancellationProven bool
	RetryAfter         time.Duration
	TrustedSummary     string
}

// FailureEnvelope 是可安全持久化的失败事实，不保留原始 cause、stderr 或正文。
type FailureEnvelope struct {
	Class      FailureClass
	ErrorKind  foundation.ErrorKind
	Code       string
	Summary    string
	RetryAfter time.Duration
}

// AttemptResult 表示一次 Attempt 的成功输出或失败事实，两者必须且只能存在一个。
type AttemptResult struct {
	Output              json.RawMessage
	OutputSchemaVersion int
	Failure             *FailureEnvelope
}

// CounterTransition 表示会影响 NodeRun 三类计数之一的领域事件。
type CounterTransition string

const (
	// CounterInitialDispatch 创建节点的首个 River Job generation。
	CounterInitialDispatch CounterTransition = "initial_dispatch"
	// CounterRiverRedelivery 表示同一 River Job 的 transport 重投。
	CounterRiverRedelivery CounterTransition = "river_redelivery"
	// CounterLeaseAcquired 表示首次取得当前 generation 的 lease。
	CounterLeaseAcquired CounterTransition = "lease_acquired"
	// CounterLeaseReclaimed 表示旧 lease 过期后的重新取得。
	CounterLeaseReclaimed CounterTransition = "lease_reclaimed"
	// CounterBusinessRetry 表示已提交的业务 Retryable 归约。
	CounterBusinessRetry CounterTransition = "business_retry"
	// CounterHumanResume 表示人工决策完成后创建新 generation。
	CounterHumanResume CounterTransition = "human_resume"
	// CounterPauseResume 表示 Run 恢复后创建新 generation。
	CounterPauseResume CounterTransition = "pause_resume"
	// CounterRecoveryRepublish 表示显式恢复操作创建新 generation。
	CounterRecoveryRepublish CounterTransition = "recovery_republish"
)

// NodeCounters 是 NodeRun 上 attempt、dispatch、retry 的兼容投影。
type NodeCounters struct {
	AttemptNo  int
	DispatchNo int
	RetryNo    int
}

// RetryIdentity 是确定性 jitter 的稳定业务身份。
type RetryIdentity struct {
	RunID   foundation.ID
	NodeKey string
}

// RetrySchedule 是一次业务 Retryable 归约的纯计算结果。
type RetrySchedule struct {
	NextRetryNo int
	Delay       time.Duration
	Exhausted   bool
}

// ApplyCounterTransition 按冻结语义计算下一组计数，不修改调用方输入。
func ApplyCounterTransition(current NodeCounters, transition CounterTransition) (NodeCounters, error) {
	if current.AttemptNo < 0 || current.DispatchNo < 0 || current.RetryNo < 0 {
		return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_INVALID")
	}
	next := current
	switch transition {
	case CounterInitialDispatch:
		if current.DispatchNo != 0 || current.AttemptNo != 0 || current.RetryNo != 0 {
			return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_TRANSITION_INVALID")
		}
		next.DispatchNo = 1
	case CounterRiverRedelivery:
		if current.DispatchNo < 1 {
			return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_TRANSITION_INVALID")
		}
	case CounterLeaseAcquired, CounterLeaseReclaimed:
		if current.DispatchNo < 1 || current.AttemptNo == math.MaxInt || (transition == CounterLeaseReclaimed && current.AttemptNo < 1) {
			return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_TRANSITION_INVALID")
		}
		next.AttemptNo++
	case CounterBusinessRetry:
		if current.DispatchNo < 1 || current.AttemptNo < 1 || current.DispatchNo == math.MaxInt || current.RetryNo == math.MaxInt {
			return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_TRANSITION_INVALID")
		}
		next.DispatchNo++
		next.RetryNo++
	case CounterHumanResume, CounterPauseResume, CounterRecoveryRepublish:
		if current.DispatchNo < 1 || current.DispatchNo == math.MaxInt {
			return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_TRANSITION_INVALID")
		}
		next.DispatchNo++
	default:
		return NodeCounters{}, invalidDomainInput("WORKFLOW_COUNTER_TRANSITION_INVALID")
	}
	return next, nil
}

// ClassifyFailure 按显式分类、稳定 Code、取消事实、ErrorKind/Retryable 的固定顺序归类。
func ClassifyFailure(input FailureInput) (FailureEnvelope, error) {
	if input.RetryAfter < 0 {
		return FailureEnvelope{}, invalidDomainInput("WORKFLOW_RETRY_AFTER_INVALID")
	}
	if input.Explicit != nil {
		explicit := *input.Explicit
		if err := validateFailureEnvelope(explicit); err != nil {
			return FailureEnvelope{}, err
		}
		if explicit.RetryAfter > MaxTrustedRetryAfter {
			explicit.RetryAfter = MaxTrustedRetryAfter
		}
		explicit.Code = strings.TrimSpace(explicit.Code)
		explicit.Summary = sanitizeFailureSummary(explicit.Summary, explicit.Code)
		return explicit, nil
	}
	if input.Err == nil {
		return FailureEnvelope{}, invalidDomainInput("WORKFLOW_FAILURE_INVALID")
	}
	classified := &foundation.Error{}
	hasClassified := errors.As(input.Err, &classified)
	kind := foundation.ErrorNonRetryableFailure
	code := "WORKFLOW_EXECUTION_FAILED"
	if hasClassified {
		kind = classified.Kind
		if strings.TrimSpace(classified.Code) != "" {
			code = strings.TrimSpace(classified.Code)
		}
	}

	var class FailureClass
	switch {
	case code == "WORKFLOW_LEASE_LOST":
		class = FailureClassLeaseLost
	case input.CancellationProven:
		class = FailureClassCancelled
	case kind == foundation.ErrorManualRecoveryRequired:
		class = FailureClassManualRecovery
	case kind == foundation.ErrorRetryableFailure || (hasClassified && classified.Retryable):
		class = FailureClassRetryable
	default:
		class = FailureClassNonRetryable
	}

	retryAfter := input.RetryAfter
	if retryAfter > MaxTrustedRetryAfter {
		retryAfter = MaxTrustedRetryAfter
	}
	if retryAfter > 0 && class != FailureClassRetryable {
		return FailureEnvelope{}, invalidDomainInput("WORKFLOW_RETRY_AFTER_INVALID")
	}
	summary := sanitizeFailureSummary(input.TrustedSummary, code)
	return FailureEnvelope{Class: class, ErrorKind: kind, Code: code, Summary: summary, RetryAfter: retryAfter}, nil
}

// ValidateAttemptResult 校验 Success 与 Failure 互斥并验证输出契约版本。
func ValidateAttemptResult(result AttemptResult) error {
	hasOutput := len(result.Output) > 0
	hasFailure := result.Failure != nil
	if hasOutput == hasFailure {
		return invalidDomainInput("WORKFLOW_ATTEMPT_RESULT_INVALID")
	}
	if hasOutput {
		if result.OutputSchemaVersion < 1 || !json.Valid(result.Output) {
			return invalidDomainInput("WORKFLOW_ATTEMPT_RESULT_INVALID")
		}
		return nil
	}
	if result.OutputSchemaVersion != 0 {
		return invalidDomainInput("WORKFLOW_ATTEMPT_RESULT_INVALID")
	}
	return validateFailureEnvelope(*result.Failure)
}

// CalculateRetry 根据业务 retry_no 计算带确定性 jitter 的下一次等待时间。
func CalculateRetry(policy RetryPolicy, identity RetryIdentity, currentRetryNo int, trustedRetryAfter time.Duration) (RetrySchedule, error) {
	if policy.MaxRetries < 0 || currentRetryNo < 0 || trustedRetryAfter < 0 || strings.TrimSpace(string(identity.RunID)) == "" || strings.TrimSpace(identity.NodeKey) == "" {
		return RetrySchedule{}, invalidDomainInput("WORKFLOW_RETRY_POLICY_INVALID")
	}
	if currentRetryNo >= policy.MaxRetries {
		return RetrySchedule{NextRetryNo: currentRetryNo, Exhausted: true}, nil
	}
	if policy.BaseDelay <= 0 || policy.MaxDelay < policy.BaseDelay {
		return RetrySchedule{}, invalidDomainInput("WORKFLOW_RETRY_POLICY_INVALID")
	}

	nextRetryNo := currentRetryNo + 1
	base := saturatingExponentialDelay(policy.BaseDelay, policy.MaxDelay, nextRetryNo-1)
	jitterCeiling := base / 5
	jitter := deterministicJitter(identity, nextRetryNo, jitterCeiling)
	delay := saturatingAddDuration(base, jitter, policy.MaxDelay)
	if trustedRetryAfter > MaxTrustedRetryAfter {
		trustedRetryAfter = MaxTrustedRetryAfter
	}
	if trustedRetryAfter > delay {
		delay = trustedRetryAfter
	}
	if delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	return RetrySchedule{NextRetryNo: nextRetryNo, Delay: delay}, nil
}

func validFailureClass(class FailureClass) bool {
	switch class {
	case FailureClassRetryable, FailureClassNonRetryable, FailureClassManualRecovery, FailureClassLeaseLost, FailureClassCancelled:
		return true
	default:
		return false
	}
}

func validateFailureEnvelope(failure FailureEnvelope) error {
	if !validFailureClass(failure.Class) || strings.TrimSpace(failure.Code) == "" || failure.RetryAfter < 0 {
		return invalidDomainInput("WORKFLOW_FAILURE_INVALID")
	}
	if failure.RetryAfter > 0 && failure.Class != FailureClassRetryable {
		return invalidDomainInput("WORKFLOW_RETRY_AFTER_INVALID")
	}
	return nil
}

func sanitizeFailureSummary(summary, fallback string) string {
	summary = strings.TrimSpace(strings.Join(strings.Fields(summary), " "))
	if summary == "" {
		summary = fallback
	}
	lower := strings.ToLower(summary)
	for _, marker := range []string{"token=", "token:", "secret=", "secret:", "password=", "password:", "api_key=", "api-key=", "authorization:", "bearer "} {
		if strings.Contains(lower, marker) {
			return RedactedFailureSummary
		}
	}
	for _, field := range strings.Fields(summary) {
		if strings.HasPrefix(field, "/") || (len(field) >= 3 && field[1] == ':' && (field[2] == '\\' || field[2] == '/')) {
			return RedactedFailureSummary
		}
	}
	if utf8.RuneCountInString(summary) <= MaxFailureSummaryRunes {
		return summary
	}
	runes := []rune(summary)
	return string(runes[:MaxFailureSummaryRunes])
}

func saturatingExponentialDelay(base, max time.Duration, exponent int) time.Duration {
	delay := base
	for range exponent {
		if delay >= max || delay > max/2 {
			return max
		}
		delay *= 2
	}
	return delay
}

func deterministicJitter(identity RetryIdentity, retryNo int, ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	digest := sha256.Sum256([]byte(string(identity.RunID) + "\x00" + identity.NodeKey + "\x00" + strconv.Itoa(retryNo)))
	return time.Duration(binary.BigEndian.Uint64(digest[:8]) % (uint64(ceiling) + 1))
}

func saturatingAddDuration(left, right, max time.Duration) time.Duration {
	if left >= max || right >= max-left {
		return max
	}
	return left + right
}

func invalidDomainInput(code string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New("workflow domain input is invalid"))
}
