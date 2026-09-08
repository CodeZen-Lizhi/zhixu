// Package domain 定义独立于普通日志和 Knowledge Timeline 的 append-only Audit 合同。
package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// SchemaVersion 是 Audit 元数据的冻结版本。
	SchemaVersion = "audit-event/v1"
	// MaxMetadataBytes 限制单个 correlation/metadata JSON 对象大小。
	MaxMetadataBytes = 32 * 1024
	// MaxListLimit 限制单次 Audit 查询数量。
	MaxListLimit = 200
	// RedactedValue 是敏感值唯一允许的持久替代文本。
	RedactedValue = "<redacted>"
)

const (
	// ErrorCodeInvalid 表示 Audit 事件或查询输入非法。
	ErrorCodeInvalid = "AUDIT_EVENT_INVALID"
	// ErrorCodeUnavailable 表示 Audit 持久化依赖不可用。
	ErrorCodeUnavailable = "AUDIT_EVENT_UNAVAILABLE"
	// ErrorCodeIdempotencyConflict 表示同一幂等键绑定了不同事实。
	ErrorCodeIdempotencyConflict = "AUDIT_IDEMPOTENCY_CONFLICT"
	// ErrorCodeNotFound 表示 Audit 事件不存在。
	ErrorCodeNotFound = "AUDIT_EVENT_NOT_FOUND"
	// ErrorCodeCorrupt 表示持久 Audit 无法安全解码。
	ErrorCodeCorrupt = "AUDIT_EVENT_CORRUPT"
	// ErrorCodeTransactionUnavailable 表示 scoped 操作缺少可用事务。
	ErrorCodeTransactionUnavailable = "AUDIT_TRANSACTION_UNAVAILABLE"

	// 兼容清晰的长名称，避免 Adapter 或调用方重复定义错误码。
	ErrorCodeEventInvalid                 = ErrorCodeInvalid
	ErrorCodePayloadInvalid               = ErrorCodeInvalid
	ErrorCodeAppendConflict               = ErrorCodeIdempotencyConflict
	ErrorCodeAppendTransactionUnavailable = ErrorCodeTransactionUnavailable
	ErrorCodeStoreUnavailable             = ErrorCodeUnavailable
	ErrorCodeEventCorrupt                 = ErrorCodeCorrupt
	ErrorCodeEventNotFound                = ErrorCodeNotFound
	ErrorCodeQueryInvalid                 = ErrorCodeInvalid
)

// ActorType 是审计行为主体类型。
type ActorType string

const (
	// ActorUser 表示人工用户。
	ActorUser ActorType = "USER"
	// ActorAPIToken 表示受限 Bearer API Token 的自动化调用方。
	ActorAPIToken ActorType = "API_TOKEN"
	// ActorAgent 表示受限 Agent 编排。
	ActorAgent ActorType = "AGENT"
	// ActorSystem 表示系统进程。
	ActorSystem ActorType = "SYSTEM"
	// ActorWorker 表示异步 Worker。
	ActorWorker ActorType = "WORKER"
	// ActorAnonymous 表示未认证但需要留痕的安全请求。
	ActorAnonymous ActorType = "ANONYMOUS"
)

// Outcome 是受控审计结果。
type Outcome string

const (
	// OutcomeSucceeded 表示动作成功完成。
	OutcomeSucceeded Outcome = "SUCCEEDED"
	// OutcomeRejected 表示动作被业务或安全边界拒绝。
	OutcomeRejected Outcome = "REJECTED"
	// OutcomeFailed 表示动作执行失败。
	OutcomeFailed Outcome = "FAILED"
	// OutcomeUnknown 表示外部副作用结果无法安全确认。
	OutcomeUnknown Outcome = "UNKNOWN"
)

// Event 是安全和业务决策的不可变审计事实。
type Event struct {
	ID             foundation.ID
	WorkspaceID    *foundation.ID
	ActorType      ActorType
	ActorRef       string
	Action         string
	ResourceType   string
	ResourceRef    string
	Outcome        Outcome
	ErrorCode      string
	IdempotencyKey string
	Correlation    json.RawMessage
	Metadata       json.RawMessage
	SchemaVersion  string
	OccurredAt     time.Time
}

// AuditEvent 是 Event 的架构术语别名。
type AuditEvent = Event

// AppendRequest 是 Event 的追加语义别名。
type AppendRequest = Event

// ListQuery 描述按 Workspace 和发生时间游标读取的有界查询。
type ListQuery struct {
	WorkspaceID *foundation.ID
	Before      time.Time
	// BeforeID 与 Before 组成不会漏掉同微秒事件的稳定倒序游标。
	BeforeID foundation.ID
	Limit    int
}

// NewEvent 构造并统一脱敏一个 Audit 事件。
func NewEvent(event Event) (Event, error) {
	return event.Redacted()
}

// Redacted 返回 correlation/metadata 已递归脱敏且 canonical 的副本。
func (event Event) Redacted() (Event, error) {
	correlation, err := RedactJSONObject(event.Correlation)
	if err != nil {
		return Event{}, invalid("audit correlation is invalid", err)
	}
	metadata, err := RedactJSONObject(event.Metadata)
	if err != nil {
		return Event{}, invalid("audit metadata is invalid", err)
	}
	event.Correlation = correlation
	event.Metadata = metadata
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Validate 校验审计事件只保存稳定关联和脱敏摘要。
func (event Event) Validate() error {
	if !validID(event.ID) {
		return invalid("audit event id is invalid", nil)
	}
	if event.WorkspaceID != nil && !validID(*event.WorkspaceID) {
		return invalid("audit workspace id is invalid", nil)
	}
	if !validActor(event.ActorType) || !validText(event.Action, 128, false) || !validText(event.IdempotencyKey, 256, false) {
		return invalid("audit actor, action or idempotency key is invalid", nil)
	}
	if event.ActorRef != "" && !validText(event.ActorRef, 512, false) ||
		event.ResourceType != "" && !validText(event.ResourceType, 128, false) ||
		event.ResourceRef != "" && !validText(event.ResourceRef, 512, false) {
		return invalid("audit resource reference is invalid", nil)
	}
	if !validOutcome(event.Outcome) {
		return invalid("audit outcome is invalid", nil)
	}
	if (event.Outcome == OutcomeFailed || event.Outcome == OutcomeRejected || event.Outcome == OutcomeUnknown) && !validText(event.ErrorCode, 128, false) {
		return invalid("non-success audit event requires an error code", nil)
	}
	if event.Outcome == OutcomeSucceeded && event.ErrorCode != "" {
		return invalid("successful audit event cannot carry an error code", nil)
	}
	if event.SchemaVersion != SchemaVersion || event.OccurredAt.IsZero() {
		return invalid("audit schema or timestamp is invalid", nil)
	}
	if !event.OccurredAt.Equal(event.OccurredAt.UTC().Truncate(time.Microsecond)) {
		return invalid("audit timestamp must be canonical UTC microseconds", nil)
	}
	correlation, err := RedactJSONObject(event.Correlation)
	if err != nil || !bytes.Equal(correlation, event.Correlation) {
		return invalid("audit correlation is not canonical", err)
	}
	metadata, err := RedactJSONObject(event.Metadata)
	if err != nil || !bytes.Equal(metadata, event.Metadata) {
		return invalid("audit metadata is not canonical", err)
	}
	return nil
}

// Fingerprint 返回除数据库 ID 外完整 Audit 绑定的 SHA-256 摘要。
func (event Event) Fingerprint() (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	value := struct {
		WorkspaceID    *foundation.ID  `json:"workspace_id,omitempty"`
		ActorType      ActorType       `json:"actor_type"`
		ActorRef       string          `json:"actor_ref,omitempty"`
		Action         string          `json:"action"`
		ResourceType   string          `json:"resource_type,omitempty"`
		ResourceRef    string          `json:"resource_ref,omitempty"`
		Outcome        Outcome         `json:"outcome"`
		ErrorCode      string          `json:"error_code,omitempty"`
		IdempotencyKey string          `json:"idempotency_key"`
		Correlation    json.RawMessage `json:"correlation"`
		Metadata       json.RawMessage `json:"metadata"`
		SchemaVersion  string          `json:"schema_version"`
		OccurredAt     time.Time       `json:"occurred_at"`
	}{
		WorkspaceID: event.WorkspaceID, ActorType: event.ActorType, ActorRef: event.ActorRef,
		Action: event.Action, ResourceType: event.ResourceType, ResourceRef: event.ResourceRef,
		Outcome: event.Outcome, ErrorCode: event.ErrorCode, IdempotencyKey: event.IdempotencyKey,
		Correlation: event.Correlation, Metadata: event.Metadata, SchemaVersion: event.SchemaVersion,
		OccurredAt: event.OccurredAt,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", invalid("audit fingerprint cannot be encoded", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// EqualBinding 比较两个幂等 Audit 是否为同一完整事实。
func EqualBinding(left, right Event) bool {
	leftHash, leftErr := left.Fingerprint()
	rightHash, rightErr := right.Fingerprint()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

// RedactJSONObject 严格解码、递归脱敏并返回 canonical JSON 对象。
func RedactJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	decoded, err := decodeJSONObject(raw)
	if err != nil {
		return nil, err
	}
	safe := redactObject(decoded)
	canonical, err := json.Marshal(safe)
	if err != nil || len(canonical) > MaxMetadataBytes {
		return nil, errors.New("audit json object cannot be encoded")
	}
	return json.RawMessage(canonical), nil
}

// CanonicalJSONObject 严格解码并规范化一个 JSON 对象，但不改变其值。
func CanonicalJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	decoded, err := decodeJSONObject(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || len(canonical) > MaxMetadataBytes {
		return nil, errors.New("audit json object cannot be encoded")
	}
	return json.RawMessage(canonical), nil
}

func decodeJSONObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	limits := foundationstrictjson.Limits{
		MaxDocumentBytes: MaxMetadataBytes,
		MaxDepth:         8,
		MaxStringBytes:   4096,
		MaxArrayItems:    256,
		MaxObjectFields:  128,
	}
	decoded, err := foundationstrictjson.DecodeObject[map[string]any](raw, limits, nil)
	if err != nil || decoded == nil {
		return nil, errors.New("audit json object is invalid")
	}
	return decoded, nil
}

func redactObject(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = redactValue(key, value)
	}
	return result
}

func redactValue(key string, value any) any {
	if sensitiveKey(key) {
		return RedactedValue
	}
	switch typed := value.(type) {
	case string:
		if sensitiveText(typed) || foundationredaction.ContainsAbsolutePath(typed) {
			return RedactedValue
		}
		return typed
	case map[string]any:
		return redactObject(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactValue(key, item)
		}
		return result
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.TrimSpace(key)))
	for _, marker := range []string{
		"authorization", "cookie", "credential", "password", "passwd", "secret", "token",
		"csrf", "session", "refresh", "accesskey", "apikey", "dsn", "databaseurl",
		"connectionstring", "stderr", "locktoken",
	} {
		if strings.Contains(normalized, marker) && !safeSummaryKey(normalized) {
			return true
		}
	}
	for _, marker := range []string{"prompt", "source", "rawresponse", "requestbody", "responsebody", "content", "body"} {
		if strings.Contains(normalized, marker) && !safeSummaryKey(normalized) {
			return true
		}
	}
	return false
}

func safeSummaryKey(normalized string) bool {
	for _, suffix := range []string{"id", "ref", "hash", "size", "length", "bytes", "count", "version", "status", "code", "kind", "configured"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func sensitiveText(value string) bool {
	return foundationredaction.ContainsSecret(value)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validActor(value ActorType) bool {
	return value == ActorUser || value == ActorAPIToken || value == ActorAgent || value == ActorSystem || value == ActorWorker || value == ActorAnonymous
}

func validOutcome(value Outcome) bool {
	return value == OutcomeSucceeded || value == OutcomeRejected || value == OutcomeFailed || value == OutcomeUnknown
}

func validText(value string, maximum int, optional bool) bool {
	if value == "" {
		return optional
	}
	if len(value) > maximum || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n") || sensitiveText(value) || foundationredaction.ContainsAbsolutePath(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func invalid(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalid, false, cause)
}

// String 返回不包含关联值、Token、Cookie 或元数据的安全摘要。
func (event Event) String() string {
	return fmt.Sprintf("AuditEvent{workspace_configured:%t actor:%q action_configured:%t outcome:%q idempotency_configured:%t}", event.WorkspaceID != nil, event.ActorType, event.Action != "", event.Outcome, event.IdempotencyKey != "")
}

// GoString 与 String 使用相同的安全表示。
func (event Event) GoString() string { return event.String() }
