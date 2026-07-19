// Package workflow 把 Tool Application 接入持久化 Workflow Executor/Definition Registry。
package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// DefinitionKey 是项目自有 Agent RAG Tool Workflow 的稳定键。
	DefinitionKey = "agent-rag"
	// DefinitionVersion 是当前 Tool Workflow Definition 版本。
	DefinitionVersion int64 = 1
	// NodeKey 是单节点 Tool Workflow 的稳定节点键。
	NodeKey = "tool"
	// NodeKind 是 Workflow Executor Registry 的稳定解析键。
	NodeKind = "agent.rag.tool"
	// InputSchemaVersion 是不含模型 reason 的持久 Tool Invocation 契约版本。
	InputSchemaVersion = toolsdomain.ToolRequestSchemaVersionV1
	// OutputSchemaVersion 是脱敏 Tool 调用结果摘要的节点输出契约版本。
	OutputSchemaVersion = 1

	// ErrorCodeExecutorUnavailable 表示持久 Tool Node 缺少共享 ExecutionService。
	ErrorCodeExecutorUnavailable = "TOOL_WORKFLOW_EXECUTOR_UNAVAILABLE"
	// ErrorCodeContractUnavailable 表示 Tool Workflow 缺少冻结 Tool contract catalog。
	ErrorCodeContractUnavailable = "TOOL_WORKFLOW_CONTRACT_UNAVAILABLE"
	// ErrorCodeDefinitionInvalid 表示 Agent RAG Tool Definition 无法从精确 Tool 引用安全派生。
	ErrorCodeDefinitionInvalid = "TOOL_WORKFLOW_DEFINITION_INVALID"
	// ErrorCodeContextInvalid 表示 River Claim 未提供完整的服务端执行身份。
	ErrorCodeContextInvalid = "TOOL_WORKFLOW_CONTEXT_INVALID"
	// ErrorCodeInputInvalid 表示持久 Node input 不是严格、安全的 Tool Invocation v1。
	ErrorCodeInputInvalid = "TOOL_WORKFLOW_INPUT_INVALID"
	// ErrorCodeOutputInvalid 表示 Tool 成功事实无法编码为安全持久结果。
	ErrorCodeOutputInvalid = "TOOL_WORKFLOW_OUTPUT_INVALID"

	maxWorkflowInputBytes  = toolsdomain.MaxToolDocumentBytes + 1024
	maxWorkflowOutputBytes = toolsdomain.MaxToolSummaryBytes + 4096
	persistedReason        = "persisted safe tool invocation"
)

// ContractCatalog 按精确 Tool 引用解析冻结契约，用于从单一事实源派生节点权限。
type ContractCatalog interface {
	ResolveToolDefinition(toolsdomain.ToolRef) (toolsdomain.Definition, error)
}

// RequestContractCatalog 解析精确 Tool Contract，用于在写入 Workflow 前完成 typed input 校验。
type RequestContractCatalog interface {
	ResolveContract(toolsdomain.ToolRef) (toolsapplication.Contract, error)
}

// ModelReadToolRefsV1 返回生产持久 Agent RAG Workflow 允许的 empty/stable-ID-only Tool 引用。
// SearchKnowledge query 与 CalculateDiff 正文必须等待 M6-04 安全 request receipt/in-process loop，
// 不得因为 Worker 存在真实 Executor 就自动进入持久模型目录。
func ModelReadToolRefsV1() []toolsdomain.ToolRef {
	return []toolsdomain.ToolRef{
		{Name: "ReadSource", Version: 1},
		{Name: "ValidateCitation", Version: 1},
		{Name: "ReadGitStatus", Version: 1},
	}
}

// NewProductionRegisteredDefinition 构造生产持久模型目录，禁止调用方误把全部已注册 Executor 暴露给模型。
func NewProductionRegisteredDefinition(catalog ContractCatalog) (workflowdomain.RegisteredDefinition, error) {
	return NewRegisteredDefinition(catalog, ModelReadToolRefsV1())
}

// PersistedToolInvocationV1 是由 strict Agent Tool Request 派生的安全持久命令。
// 模型 reason、Workspace、Capability、Credential 和内容型参数不得进入该结构。
type PersistedToolInvocationV1 struct {
	SchemaVersion int             `json:"schema_version"`
	ToolName      string          `json:"tool_name"`
	Arguments     json.RawMessage `json:"arguments"`
}

// EncodePersistedToolInvocationV1 在持久化前校验生产 allowlist 与 typed arguments，并丢弃模型 reason。
func EncodePersistedToolInvocationV1(catalog RequestContractCatalog, request toolsdomain.ToolRequestV1) (json.RawMessage, error) {
	if isNilInterface(catalog) || request.Validate() != nil {
		return nil, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("agent tool request is invalid"))
	}
	ref := toolsdomain.ToolRef{Name: request.ToolName, Version: 1}
	if !containsToolRef(ModelReadToolRefsV1(), ref) {
		return nil, workflowError(foundation.ErrorPermissionDenied, ErrorCodeInputInvalid, false, errors.New("tool is not allowed in persisted workflow input"))
	}
	contract, err := catalog.ResolveContract(ref)
	if err != nil {
		return nil, err
	}
	arguments, err := contract.DecodeInput(request.Arguments)
	if err != nil {
		return nil, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, err)
	}
	encoded, err := json.Marshal(PersistedToolInvocationV1{SchemaVersion: InputSchemaVersion, ToolName: ref.Name, Arguments: arguments})
	if err != nil {
		return nil, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeInputInvalid, false, err)
	}
	if _, err := DecodePersistedToolInvocationV1(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

// DecodePersistedToolInvocationV1 严格解析不含模型 reason 的生产持久命令。
func DecodePersistedToolInvocationV1(raw []byte) (PersistedToolInvocationV1, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxWorkflowInputBytes
	limits.MaxDepth = 8
	limits.MaxStringBytes = toolsdomain.MaxToolDocumentBytes
	limits.MaxArrayItems = 500
	limits.MaxObjectFields = 3
	result, err := strictjson.DecodeObject(raw, limits, validatePersistedToolInvocationV1)
	if err == nil {
		return result, nil
	}
	if _, ok := strictjson.KindOf(err); ok {
		return PersistedToolInvocationV1{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, err)
	}
	return PersistedToolInvocationV1{}, err
}

func validatePersistedToolInvocationV1(invocation PersistedToolInvocationV1) error {
	ref := toolsdomain.ToolRef{Name: invocation.ToolName, Version: 1}
	trimmed := bytes.TrimSpace(invocation.Arguments)
	if invocation.SchemaVersion != InputSchemaVersion || !containsToolRef(ModelReadToolRefsV1(), ref) || len(trimmed) < 2 ||
		trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("persisted tool invocation is invalid"))
	}
	return nil
}

func containsToolRef(values []toolsdomain.ToolRef, target toolsdomain.ToolRef) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// ToolResultV1 是允许持久化到 Workflow Node 的脱敏 Tool 调用结果。
// Raw arguments、raw output、Prompt、Credential 和路径不得进入该结构。
type ToolResultV1 struct {
	SchemaVersion   int                    `json:"schema_version"`
	ToolCallID      foundation.ID          `json:"tool_call_id"`
	Tool            toolsdomain.ToolRef    `json:"tool"`
	Status          toolsdomain.CallStatus `json:"status"`
	ResultRef       string                 `json:"result_ref,omitempty"`
	ResponseHash    string                 `json:"response_hash"`
	ResponseBytes   int64                  `json:"response_bytes"`
	ResponseSummary ToolResponseSummaryV1  `json:"response_summary"`
	UntrustedData   bool                   `json:"untrusted_data"`
}

// ToolResponseSummaryV1 是 ExecutionService 允许持久化的固定输出元数据，不承载正文或任意字段。
type ToolResponseSummaryV1 struct {
	OutputBytes        int64  `json:"output_bytes"`
	OutputHash         string `json:"output_hash"`
	RedactedFieldCount int    `json:"redacted_field_count"`
	SchemaID           string `json:"schema_id"`
	SchemaVersion      int64  `json:"schema_version"`
}

// NewRegisteredDefinition 从冻结 Tool contract 派生一个服务端拥有的单节点 Definition。
// 空 Tool 集合不会生成可启动 Definition，避免 disabled runtime 暴露可达节点。
func NewRegisteredDefinition(catalog ContractCatalog, refs []toolsdomain.ToolRef) (workflowdomain.RegisteredDefinition, error) {
	if isNilCatalog(catalog) || len(refs) == 0 {
		return workflowdomain.RegisteredDefinition{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeContractUnavailable, false, errors.New("tool workflow contracts are unavailable"))
	}
	canonicalRefs := append([]toolsdomain.ToolRef(nil), refs...)
	sort.Slice(canonicalRefs, func(left, right int) bool {
		if canonicalRefs[left].Name == canonicalRefs[right].Name {
			return canonicalRefs[left].Version < canonicalRefs[right].Version
		}
		return canonicalRefs[left].Name < canonicalRefs[right].Name
	})
	permissions := make(map[capability.Capability]struct{})
	for index, ref := range canonicalRefs {
		if ref.Validate() != nil || (index > 0 && canonicalRefs[index-1].Name == ref.Name) {
			return workflowdomain.RegisteredDefinition{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeDefinitionInvalid, false, errors.New("tool workflow references are invalid"))
		}
		definition, err := catalog.ResolveToolDefinition(ref)
		if err != nil {
			return workflowdomain.RegisteredDefinition{}, err
		}
		if definition.Ref != ref || definition.InvocationPolicy != toolsdomain.InvocationModelRequestable || !allowsWorkflow(definition.AllowedWorkflows) {
			return workflowdomain.RegisteredDefinition{}, workflowError(foundation.ErrorPermissionDenied, ErrorCodeDefinitionInvalid, false, errors.New("tool contract is not model-requestable in the agent workflow"))
		}
		if definition.RequiredCapability != "" {
			permissions[definition.RequiredCapability] = struct{}{}
		}
	}
	permissionList := make([]workflowdomain.Permission, 0, len(permissions))
	for permission := range permissions {
		permissionList = append(permissionList, permission)
	}
	sort.Slice(permissionList, func(left, right int) bool { return permissionList[left] < permissionList[right] })
	return workflowdomain.RegisteredDefinition{
		Key: DefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: NodeKey, Kind: NodeKind, InputSchemaVersion: InputSchemaVersion, OutputSchemaVersion: OutputSchemaVersion,
			RetryPolicy: workflowdomain.RetryPolicy{}, RequiredPermissions: permissionList, AllowedTools: canonicalRefs,
		}}},
	}, nil
}

// DecodeToolResultV1 严格解析持久 Tool Node 输出，拒绝 unknown、duplicate、trailing 和资源越界。
func DecodeToolResultV1(raw []byte) (ToolResultV1, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxWorkflowOutputBytes
	limits.MaxDepth = 8
	limits.MaxStringBytes = toolsdomain.MaxToolSummaryBytes
	limits.MaxArrayItems = 500
	limits.MaxObjectFields = 16
	result, err := strictjson.DecodeObject(raw, limits, validateToolResultV1)
	if err == nil {
		return result, nil
	}
	if _, ok := strictjson.KindOf(err); ok {
		return ToolResultV1{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeOutputInvalid, false, err)
	}
	return ToolResultV1{}, err
}

func validateToolResultV1(result ToolResultV1) error {
	parsedCallID, err := foundation.ParseID(string(result.ToolCallID))
	if result.SchemaVersion != OutputSchemaVersion || err != nil || parsedCallID != result.ToolCallID || result.Tool.Validate() != nil ||
		result.Status != toolsdomain.CallSucceeded || result.ResponseBytes < 1 || !validHash(result.ResponseHash) ||
		validateToolResponseSummary(result.ResponseSummary) != nil || result.ResponseSummary.OutputHash != result.ResponseHash ||
		result.ResponseSummary.OutputBytes != result.ResponseBytes || !result.UntrustedData ||
		(result.ResultRef != "" && !toolsdomain.ValidResultReference(result.ResultRef)) {
		return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("tool workflow result is invalid"))
	}
	return nil
}

func decodeToolResponseSummary(raw json.RawMessage) (ToolResponseSummaryV1, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = toolsdomain.MaxToolSummaryBytes
	limits.MaxDepth = 2
	limits.MaxStringBytes = 256
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 5
	return strictjson.DecodeObject(raw, limits, validateToolResponseSummary)
}

func validateToolResponseSummary(summary ToolResponseSummaryV1) error {
	schema := toolsdomain.SchemaRef{ID: summary.SchemaID, Version: summary.SchemaVersion}
	if summary.OutputBytes < 1 || !validHash(summary.OutputHash) || summary.RedactedFieldCount < 0 || schema.Validate() != nil {
		return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("tool response summary is invalid"))
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func allowsWorkflow(bindings []toolsdomain.WorkflowBinding) bool {
	for _, binding := range bindings {
		if binding.Key == DefinitionKey && binding.Version == DefinitionVersion {
			return true
		}
	}
	return false
}

func isNilCatalog(catalog ContractCatalog) bool {
	return isNilInterface(catalog)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func workflowError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, strings.TrimSpace(code), retryable, cause)
}
