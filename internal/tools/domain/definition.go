package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// MaxToolNameBytes 是 Tool 稳定名称的最大字节数。
	MaxToolNameBytes = 64
	// MaxToolDescriptionBytes 是 Tool 描述的最大 UTF-8 字节数。
	MaxToolDescriptionBytes = 4096
	// MaxToolSchemaBytes 是单个 Tool JSON Schema 文档的最大字节数。
	MaxToolSchemaBytes = 512 * 1024
	// MaxToolDocumentBytes 是单次 Tool 输入或输出的绝对字节上限。
	MaxToolDocumentBytes = 16 * 1024 * 1024
	// MaxToolTimeout 是单次 Tool 执行允许声明的最长时间。
	MaxToolTimeout = 5 * time.Minute
	// MaxToolRetryAttempts 是 Workflow 可声明的最大 Tool 尝试次数。
	MaxToolRetryAttempts = 5
	// MaxSensitiveFieldPaths 是单个 Tool 可声明的敏感字段路径上限。
	MaxSensitiveFieldPaths = 64
	// MaxAllowedWorkflowBindings 是单个 Tool 可绑定的 Workflow 版本上限。
	MaxAllowedWorkflowBindings = 64
)

var (
	toolNamePattern      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,63}$`)
	schemaIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
	workflowKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
	sensitivePathPattern = regexp.MustCompile(`^/[A-Za-z0-9_~./-]{1,255}$`)
)

// SideEffectLevel 描述 Tool 在外部世界产生副作用的风险等级。
type SideEffectLevel string

const (
	SideEffectNone         SideEffectLevel = "NONE"
	SideEffectExternalRead SideEffectLevel = "EXTERNAL_READ"
	SideEffectDomainWrite  SideEffectLevel = "DOMAIN_WRITE"
	SideEffectUnknown      SideEffectLevel = "IRREVERSIBLE_OR_UNKNOWN"
)

// InvocationPolicy 描述谁可以提出 Tool 调用。
type InvocationPolicy string

const (
	InvocationModelRequestable    InvocationPolicy = "MODEL_REQUESTABLE"
	InvocationTrustedWorkflowOnly InvocationPolicy = "TRUSTED_WORKFLOW_ONLY"
)

// InvocationSource 标识本次调用来自模型请求还是服务端受信 Workflow。
type InvocationSource string

const (
	// InvocationSourceModelRequest 表示请求来自模型结构化输出。
	InvocationSourceModelRequest InvocationSource = "MODEL_REQUEST"
	// InvocationSourceTrustedWorkflow 表示请求由服务端受信 Workflow 发起。
	InvocationSourceTrustedWorkflow InvocationSource = "TRUSTED_WORKFLOW"
)

// ResultPersistencePolicy 声明成功 Tool 输出是否需要保存受限 canonical receipt。
type ResultPersistencePolicy string

const (
	// ResultPersistenceDisabled 保持历史 Tool 只保存摘要或既有 result ref 的行为。
	ResultPersistenceDisabled ResultPersistencePolicy = ""
	// ResultPersistenceCanonical 要求成功终结时原子保存受 Schema 和字节上限约束的 canonical receipt。
	ResultPersistenceCanonical ResultPersistencePolicy = "PERSIST_CANONICAL"
)

// IdempotencyMode 描述 Tool 对幂等键的要求。
type IdempotencyMode string

const (
	IdempotencyNone     IdempotencyMode = "NONE"
	IdempotencyOptional IdempotencyMode = "OPTIONAL"
	IdempotencyRequired IdempotencyMode = "REQUIRED"
)

// ToolRef 精确引用一个 Tool 名称与版本，不支持 latest。
type ToolRef struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

// Validate 校验 Tool 引用使用 canonical 名称与正版本。
func (ref ToolRef) Validate() error {
	if !toolNamePattern.MatchString(ref.Name) || ref.Version < 1 {
		return invalid(ErrorCodeDefinitionInvalid, "tool reference is invalid")
	}
	return nil
}

// SchemaRef 精确引用一个 Tool 输入或输出 Schema。
type SchemaRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// Validate 校验 Schema 引用使用稳定 ID 与正版本。
func (ref SchemaRef) Validate() error {
	if !schemaIDPattern.MatchString(ref.ID) || ref.Version < 1 {
		return invalid(ErrorCodeDefinitionInvalid, "tool schema reference is invalid")
	}
	return nil
}

// RetryPolicy 声明由 Workflow 拥有的有界重试预算；Registry 自身不隐藏重试。
type RetryPolicy struct {
	MaxAttempts int           `json:"max_attempts"`
	BaseDelay   time.Duration `json:"base_delay"`
	MaxDelay    time.Duration `json:"max_delay"`
}

// WorkflowBinding 精确限制 Tool 可用于哪个 Workflow Definition 版本。
type WorkflowBinding struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
}

// Definition 冻结一个 Tool 的 Schema、权限、执行与输出安全契约。
type Definition struct {
	Ref                  ToolRef               `json:"ref"`
	Description          string                `json:"description"`
	InputSchema          SchemaRef             `json:"input_schema"`
	InputSchemaDocument  json.RawMessage       `json:"input_schema_document"`
	OutputSchema         SchemaRef             `json:"output_schema"`
	OutputSchemaDocument json.RawMessage       `json:"output_schema_document"`
	RequiredCapability   capability.Capability `json:"required_capability,omitempty"`
	SideEffectLevel      SideEffectLevel       `json:"side_effect_level"`
	InvocationPolicy     InvocationPolicy      `json:"invocation_policy"`
	// ResultPersistencePolicy 控制新 Tool 版本是否持久化受限 canonical 输出；零值保持历史 hash 兼容。
	ResultPersistencePolicy ResultPersistencePolicy `json:"result_persistence_policy,omitempty"`
	Timeout                 time.Duration           `json:"timeout"`
	RetryPolicy             RetryPolicy             `json:"retry_policy"`
	IdempotencyMode         IdempotencyMode         `json:"idempotency_mode"`
	SensitiveFields         []string                `json:"sensitive_fields,omitempty"`
	AllowedWorkflows        []WorkflowBinding       `json:"allowed_workflows"`
	MaxInputBytes           int64                   `json:"max_input_bytes"`
	MaxOutputBytes          int64                   `json:"max_output_bytes"`
	DefinitionHash          string                  `json:"definition_hash"`
}

// AllowsInvocation 判断调用来源是否满足 Definition 的暴露策略。
func (definition Definition) AllowsInvocation(source InvocationSource) bool {
	switch source {
	case InvocationSourceModelRequest:
		return definition.InvocationPolicy == InvocationModelRequestable
	case InvocationSourceTrustedWorkflow:
		return definition.InvocationPolicy == InvocationModelRequestable || definition.InvocationPolicy == InvocationTrustedWorkflowOnly
	default:
		return false
	}
}

// CanonicalizeDefinition 校验、深拷贝、排序并计算不可变 Definition Hash。
func CanonicalizeDefinition(definition Definition) (Definition, error) {
	definition.Description = strings.TrimSpace(definition.Description)
	definition.DefinitionHash = ""
	if err := validateDefinitionScalars(definition); err != nil {
		return Definition{}, err
	}
	inputSchema, err := canonicalJSONObject(definition.InputSchemaDocument, MaxToolSchemaBytes)
	if err != nil {
		return Definition{}, invalid(ErrorCodeDefinitionInvalid, "tool input schema document is invalid")
	}
	outputSchema, err := canonicalJSONObject(definition.OutputSchemaDocument, MaxToolSchemaBytes)
	if err != nil {
		return Definition{}, invalid(ErrorCodeDefinitionInvalid, "tool output schema document is invalid")
	}
	definition.InputSchemaDocument = inputSchema
	definition.OutputSchemaDocument = outputSchema

	sensitive, err := canonicalSensitiveFields(definition.SensitiveFields)
	if err != nil {
		return Definition{}, err
	}
	bindings, err := canonicalWorkflowBindings(definition.AllowedWorkflows)
	if err != nil {
		return Definition{}, err
	}
	definition.SensitiveFields = sensitive
	definition.AllowedWorkflows = bindings

	encoded, err := json.Marshal(definition)
	if err != nil {
		return Definition{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeDefinitionInvalid, false, errors.New("tool definition cannot be encoded"))
	}
	digest := sha256.Sum256(encoded)
	definition.DefinitionHash = hex.EncodeToString(digest[:])
	return definition, nil
}

func validateDefinitionScalars(definition Definition) error {
	if err := definition.Ref.Validate(); err != nil {
		return err
	}
	if definition.Description == "" || len(definition.Description) > MaxToolDescriptionBytes || !utf8.ValidString(definition.Description) {
		return invalid(ErrorCodeDefinitionInvalid, "tool description is invalid")
	}
	if definition.InputSchema.Validate() != nil || definition.OutputSchema.Validate() != nil || definition.InputSchema == definition.OutputSchema {
		return invalid(ErrorCodeDefinitionInvalid, "tool schema references are invalid")
	}
	if definition.RequiredCapability != "" && !capability.IsKnown(definition.RequiredCapability) {
		return invalid(ErrorCodeDefinitionInvalid, "tool required capability is unknown")
	}
	if !validSideEffect(definition.SideEffectLevel) || !validInvocationPolicy(definition.InvocationPolicy) ||
		!validResultPersistencePolicy(definition.ResultPersistencePolicy) || !validIdempotencyMode(definition.IdempotencyMode) {
		return invalid(ErrorCodeDefinitionInvalid, "tool execution policy is invalid")
	}
	if definition.RequiredCapability == "" && definition.SideEffectLevel != SideEffectNone {
		return invalid(ErrorCodeDefinitionInvalid, "side-effecting tool requires a capability")
	}
	if definition.InvocationPolicy == InvocationModelRequestable && (definition.SideEffectLevel == SideEffectDomainWrite || definition.SideEffectLevel == SideEffectUnknown) {
		return invalid(ErrorCodeDefinitionInvalid, "write or unknown tool cannot be model requestable")
	}
	if (definition.SideEffectLevel == SideEffectDomainWrite || definition.SideEffectLevel == SideEffectUnknown) && definition.IdempotencyMode != IdempotencyRequired {
		return invalid(ErrorCodeDefinitionInvalid, "side-effecting tool requires idempotency")
	}
	if definition.Timeout <= 0 || definition.Timeout > MaxToolTimeout || !validRetryPolicy(definition.RetryPolicy) {
		return invalid(ErrorCodeDefinitionInvalid, "tool timeout or retry policy is invalid")
	}
	if definition.MaxInputBytes <= 0 || definition.MaxInputBytes > MaxToolDocumentBytes || definition.MaxOutputBytes <= 0 || definition.MaxOutputBytes > MaxToolDocumentBytes {
		return invalid(ErrorCodeDefinitionInvalid, "tool document byte limit is invalid")
	}
	return nil
}

func validResultPersistencePolicy(policy ResultPersistencePolicy) bool {
	return policy == ResultPersistenceDisabled || policy == ResultPersistenceCanonical
}

func validRetryPolicy(policy RetryPolicy) bool {
	if policy.MaxAttempts < 1 || policy.MaxAttempts > MaxToolRetryAttempts || policy.BaseDelay < 0 || policy.MaxDelay < 0 || policy.MaxDelay < policy.BaseDelay {
		return false
	}
	if policy.MaxAttempts == 1 {
		return policy.BaseDelay == 0 && policy.MaxDelay == 0
	}
	return policy.BaseDelay > 0 && policy.MaxDelay > 0
}

func canonicalSensitiveFields(values []string) ([]string, error) {
	if len(values) > MaxSensitiveFieldPaths {
		return nil, invalid(ErrorCodeDefinitionInvalid, "tool sensitive field list is too large")
	}
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if !sensitivePathPattern.MatchString(result[index]) {
			return nil, invalid(ErrorCodeDefinitionInvalid, "tool sensitive field path is invalid")
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, invalid(ErrorCodeDefinitionInvalid, "tool sensitive field path is duplicated")
		}
	}
	return result, nil
}

func canonicalWorkflowBindings(values []WorkflowBinding) ([]WorkflowBinding, error) {
	if len(values) == 0 || len(values) > MaxAllowedWorkflowBindings {
		return nil, invalid(ErrorCodeDefinitionInvalid, "tool workflow bindings are empty or too large")
	}
	result := append([]WorkflowBinding(nil), values...)
	for index := range result {
		result[index].Key = strings.TrimSpace(result[index].Key)
		if !workflowKeyPattern.MatchString(result[index].Key) || result[index].Version < 1 {
			return nil, invalid(ErrorCodeDefinitionInvalid, "tool workflow binding is invalid")
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Key == result[right].Key {
			return result[left].Version < result[right].Version
		}
		return result[left].Key < result[right].Key
	})
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, invalid(ErrorCodeDefinitionInvalid, "tool workflow binding is duplicated")
		}
	}
	return result, nil
}

func canonicalJSONObject(document json.RawMessage, maxBytes int) (json.RawMessage, error) {
	if len(document) == 0 || len(document) > maxBytes || !utf8.Valid(document) {
		return nil, errors.New("json document is empty, oversized, or invalid utf-8")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBytes
	limits.MaxDepth = 32
	limits.MaxStringBytes = maxBytes
	limits.MaxArrayItems = 4096
	limits.MaxObjectFields = 4096
	value, err := strictjson.DecodeObject[map[string]any](document, limits, nil)
	if err != nil || value == nil {
		return nil, errors.New("json document is not an object")
	}
	return json.Marshal(value)
}

func validSideEffect(value SideEffectLevel) bool {
	return value == SideEffectNone || value == SideEffectExternalRead || value == SideEffectDomainWrite || value == SideEffectUnknown
}

func validInvocationPolicy(value InvocationPolicy) bool {
	return value == InvocationModelRequestable || value == InvocationTrustedWorkflowOnly
}

func validIdempotencyMode(value IdempotencyMode) bool {
	return value == IdempotencyNone || value == IdempotencyOptional || value == IdempotencyRequired
}
