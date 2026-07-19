package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxToolSummaryBytes 是单个持久化 Tool 摘要的最大字节数。
	MaxToolSummaryBytes = 16 * 1024
	// MaxToolIdempotencyKeyBytes 是 Tool 幂等键的最大字节数。
	MaxToolIdempotencyKeyBytes = 128
	// MaxToolResultRefBytes 是 canonical 结果或副作用引用的最大字节数。
	MaxToolResultRefBytes = 512
)

var stableCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

var stableSideEffectTypes = map[string]struct{}{
	"evaluation_run":      {},
	"reindex_delivery":    {},
	"writeback_execution": {},
}

// CallStatus 是一次 Tool 调用的持久生命周期状态。
type CallStatus string

const (
	CallStarted   CallStatus = "STARTED"
	CallSucceeded CallStatus = "SUCCEEDED"
	CallFailed    CallStatus = "FAILED"
	CallRefused   CallStatus = "REFUSED"
	CallUnknown   CallStatus = "UNKNOWN"
)

// TrustedExecutionIdentity 是服务端从持久 Workflow 事实解析出的执行身份。
type TrustedExecutionIdentity struct {
	WorkspaceID       foundation.ID
	DefinitionID      foundation.ID
	DefinitionVersion int64
	DefinitionHash    string
	WorkflowRunID     foundation.ID
	NodeKey           string
	NodeRunID         foundation.ID
	NodeAttemptID     foundation.ID
	LeaseOwner        string
	LeaseFence        int64
}

// Validate 校验执行身份完整且所有稳定 ID 互不混用。
func (identity TrustedExecutionIdentity) Validate() error {
	ids := []foundation.ID{identity.WorkspaceID, identity.DefinitionID, identity.WorkflowRunID, identity.NodeRunID, identity.NodeAttemptID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return invalid(ErrorCodeExecutionIdentityInvalid, "tool execution identity contains invalid id")
		}
		if _, exists := seen[id]; exists {
			return invalid(ErrorCodeExecutionIdentityInvalid, "tool execution identity reuses an id")
		}
		seen[id] = struct{}{}
	}
	if identity.DefinitionVersion < 1 || !lowerHex(identity.DefinitionHash, 64) || !workflowKeyPattern.MatchString(identity.NodeKey) ||
		strings.TrimSpace(identity.LeaseOwner) == "" || len(identity.LeaseOwner) > 256 || identity.LeaseFence < 1 {
		return invalid(ErrorCodeExecutionIdentityInvalid, "tool execution identity binding is invalid")
	}
	return nil
}

// IdempotencyBinding 冻结 Tool replay 必须逐项相等的请求身份。
type IdempotencyBinding struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
	Tool          ToolRef
	SchemaVersion int64
	Capability    capability.Capability
	TargetRef     string
	RequestHash   string
	Key           string
}

// Validate 校验幂等键与完整请求绑定。
func (binding IdempotencyBinding) Validate() error {
	ids := []foundation.ID{binding.WorkspaceID, binding.WorkflowRunID, binding.NodeRunID, binding.NodeAttemptID}
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return invalid(ErrorCodeIdempotencyBindingInvalid, "tool idempotency binding id is invalid")
		}
	}
	if binding.Tool.Validate() != nil || binding.SchemaVersion < 1 || !capability.IsKnown(binding.Capability) ||
		!canonicalReference(binding.TargetRef, MaxToolResultRefBytes) || !lowerHex(binding.RequestHash, 64) ||
		!canonicalReference(binding.Key, MaxToolIdempotencyKeyBytes) {
		return invalid(ErrorCodeIdempotencyBindingInvalid, "tool idempotency binding is invalid")
	}
	return nil
}

// ToolCall 是调用前登记、完成后 CAS 归约的 Tool 执行事实。
type ToolCall struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	WorkflowRunID     foundation.ID
	NodeRunID         foundation.ID
	NodeAttemptID     foundation.ID
	CallNo            int
	RequestedToolName string
	Tool              *ToolRef
	DefinitionHash    string
	InputSchema       *SchemaRef
	OutputSchema      *SchemaRef
	Capability        capability.Capability
	SideEffectLevel   SideEffectLevel
	InvocationPolicy  InvocationPolicy
	IdempotencyKey    string
	RequestHash       string
	RequestBytes      int64
	RequestSummary    json.RawMessage
	ResponseHash      string
	ResponseBytes     int64
	ResponseSummary   json.RawMessage
	ResultRef         string
	SideEffectType    string
	SideEffectID      string
	Status            CallStatus
	ErrorCode         string
	Retryable         bool
	Version           int64
	StartedAt         time.Time
	CompletedAt       *time.Time
	DurationMillis    int64
}

// ValidateToolCall 校验 Tool Call 的身份、摘要和生命周期不变量。
func ValidateToolCall(call ToolCall) error {
	ids := []foundation.ID{call.ID, call.WorkspaceID, call.WorkflowRunID, call.NodeRunID, call.NodeAttemptID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return invalid(ErrorCodeCallInvalid, "tool call contains invalid id")
		}
		if _, exists := seen[id]; exists {
			return invalid(ErrorCodeCallInvalid, "tool call reuses an id")
		}
		seen[id] = struct{}{}
	}
	if call.CallNo < 1 || !toolNamePattern.MatchString(call.RequestedToolName) || call.RequestBytes <= 0 ||
		!lowerHex(call.RequestHash, 64) || call.ResponseBytes < 0 || call.Version < 1 || call.StartedAt.IsZero() ||
		!validSummary(call.RequestSummary) || call.DurationMillis < 0 {
		return invalid(ErrorCodeCallInvalid, "tool call binding or request summary is invalid")
	}
	if call.Tool != nil {
		if call.Tool.Validate() != nil || call.RequestedToolName != call.Tool.Name || !lowerHex(call.DefinitionHash, 64) ||
			call.InputSchema == nil || call.InputSchema.Validate() != nil || call.OutputSchema == nil || call.OutputSchema.Validate() != nil ||
			!validSideEffect(call.SideEffectLevel) || !validInvocationPolicy(call.InvocationPolicy) {
			return invalid(ErrorCodeCallInvalid, "resolved tool call contract is invalid")
		}
		if call.Capability != "" && !capability.IsKnown(call.Capability) {
			return invalid(ErrorCodeCallInvalid, "tool call capability is invalid")
		}
	} else if call.Status != CallRefused || call.DefinitionHash != "" || call.InputSchema != nil || call.OutputSchema != nil || call.Capability != "" {
		return inconsistent(ErrorCodeCallInvalid, "unresolved tool call must be refused without contract claims")
	}
	if call.IdempotencyKey != "" && !canonicalReference(call.IdempotencyKey, MaxToolIdempotencyKeyBytes) {
		return invalid(ErrorCodeCallInvalid, "tool call idempotency key is invalid")
	}
	if !ValidSideEffectReference(call.SideEffectType, call.SideEffectID) {
		return invalid(ErrorCodeCallInvalid, "tool call side effect reference is invalid")
	}
	return validateCallLifecycle(call)
}

// ValidateToolCallTransition 校验 STARTED 只能进入一个终态。
func ValidateToolCallTransition(from, to CallStatus) error {
	if from == CallStarted && (to == CallSucceeded || to == CallFailed || to == CallUnknown) {
		return nil
	}
	return versionConflict(ErrorCodeCallTransitionInvalid, "tool call status transition is not allowed")
}

func validateCallLifecycle(call ToolCall) error {
	switch call.Status {
	case CallStarted:
		if call.CompletedAt != nil || call.ErrorCode != "" || call.ResponseHash != "" || call.ResponseBytes != 0 || len(call.ResponseSummary) != 0 ||
			call.ResultRef != "" || call.DurationMillis != 0 || call.Retryable {
			return inconsistent(ErrorCodeCallInvalid, "started tool call cannot contain a terminal result")
		}
		if call.SideEffectType != "" && (call.InvocationPolicy != InvocationTrustedWorkflowOnly || call.SideEffectLevel != SideEffectDomainWrite || call.SideEffectType != "writeback_execution") {
			return inconsistent(ErrorCodeCallInvalid, "started tool call contains an unsupported expected side effect")
		}
	case CallSucceeded:
		hasResponse := call.ResponseHash != "" || call.ResponseBytes != 0 || len(call.ResponseSummary) != 0
		validResponse := lowerHex(call.ResponseHash, 64) && call.ResponseBytes > 0 && validSummary(call.ResponseSummary)
		hasResultRef := call.ResultRef != ""
		if !validCompletion(call.CompletedAt, call.StartedAt) || call.ErrorCode != "" || call.Retryable ||
			(hasResponse && !validResponse) || (hasResultRef && !ValidResultReference(call.ResultRef)) || (!hasResponse && !hasResultRef) {
			return inconsistent(ErrorCodeCallInvalid, "successful tool call result is invalid")
		}
	case CallFailed, CallRefused:
		if !validCompletion(call.CompletedAt, call.StartedAt) || !stableCodePattern.MatchString(call.ErrorCode) ||
			call.ResponseHash != "" || call.ResponseBytes != 0 || len(call.ResponseSummary) != 0 || call.ResultRef != "" ||
			(call.Status == CallRefused && call.Retryable) {
			return inconsistent(ErrorCodeCallInvalid, "failed or refused tool call result is invalid")
		}
	case CallUnknown:
		if !validCompletion(call.CompletedAt, call.StartedAt) || !stableCodePattern.MatchString(call.ErrorCode) || call.Retryable ||
			call.ResponseHash != "" || call.ResponseBytes != 0 || len(call.ResponseSummary) != 0 || call.ResultRef != "" {
			return inconsistent(ErrorCodeCallInvalid, "unknown tool call cannot claim a verified result")
		}
	default:
		return invalid(ErrorCodeCallInvalid, "tool call status is unsupported")
	}
	return nil
}

func validSummary(value json.RawMessage) bool {
	if len(value) == 0 || len(value) > MaxToolSummaryBytes || !utf8.Valid(value) {
		return false
	}
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

func validCompletion(completedAt *time.Time, startedAt time.Time) bool {
	return completedAt != nil && !completedAt.IsZero() && !completedAt.Before(startedAt)
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func canonicalReference(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

// ValidResultReference 校验 Tool Call 只保存受控类型的稳定结果引用。
func ValidResultReference(value string) bool {
	if !canonicalReference(value, MaxToolResultRefBytes) {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "index-version", "source-span", "writeback-execution", "reindex-delivery", "evaluation-run":
		return len(parts) == 2 && canonicalReferenceID(parts[1])
	case "citation-batch", "web-content":
		return len(parts) == 2 && lowerHex(parts[1], 64)
	case "git-head":
		return len(parts) == 2 && (lowerHex(parts[1], 40) || lowerHex(parts[1], 64))
	case "diff":
		return len(parts) == 3 && lowerHex(parts[1], 64) && lowerHex(parts[2], 64)
	default:
		return false
	}
}

// ValidSideEffectReference 校验副作用类型与稳定 UUID 引用的精确组合。
func ValidSideEffectReference(effectType, effectID string) bool {
	if effectType == "" || effectID == "" {
		return effectType == "" && effectID == ""
	}
	if effectType == "web_fetch" {
		return lowerHex(effectID, 64)
	}
	_, allowed := stableSideEffectTypes[effectType]
	return allowed && canonicalReferenceID(effectID)
}

func canonicalReferenceID(value string) bool {
	parsed, err := foundation.ParseID(value)
	return err == nil && string(parsed) == value
}
