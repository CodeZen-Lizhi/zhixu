package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	ragAgentToolBridgeUnavailable = "AGENT_RAG_TOOL_BRIDGE_UNAVAILABLE"
	ragAgentToolIdentityInvalid   = "AGENT_RAG_TOOL_IDENTITY_INVALID"
	ragAgentToolNotAllowed        = "AGENT_RAG_TOOL_NOT_ALLOWED"
	ragAgentToolScopeInvalid      = "AGENT_RAG_TOOL_SCOPE_INVALID"
	ragAgentToolArgumentsInvalid  = "AGENT_RAG_TOOL_ARGUMENTS_INVALID"
	ragAgentToolReferenceDenied   = "AGENT_RAG_TOOL_REFERENCE_DENIED"
	ragAgentToolResultInvalid     = "AGENT_RAG_TOOL_RESULT_INVALID"

	ragReadSourceTool       = "ReadSource"
	ragValidateCitationTool = "ValidateCitation"
	ragAgentToolMaxRefs     = 500
)

// RAGAgentToolBridge 把当前已 Claim 的 RAG NodeAttempt 身份绑定到唯一的
// Tool ExecutionService。模型只能提供 tool name/arguments，不能提供或覆盖
// Workspace、Workflow、Attempt、lease/fence 与精确 Tool 版本。
type RAGAgentToolBridge struct {
	service        RAGAgentExecutionService
	identity       toolsdomain.TrustedExecutionIdentity
	specs          []agentapplication.AgentToolSpec
	allowed        map[toolsdomain.ToolRef]struct{}
	outputDecoders map[toolsdomain.ToolRef]toolsapplication.DocumentDecoder
	scope          *ragAgentToolScope
}

type ragAgentToolScope struct {
	evidenceByRef map[string]agentdomain.Citation
	evidenceByID  map[string]string
	conflictByRef map[string][]string
	evidenceRefs  []string
	conflictRefs  []string
}

type ragReadSourceArguments struct {
	EvidenceRef string `json:"evidence_ref"`
}

type ragValidateCitationArguments struct {
	EvidenceRefs []string `json:"evidence_refs"`
	ConflictRefs []string `json:"conflict_refs"`
}

type ragReadSourceExecutionArguments struct {
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
}

type ragCitationTuple struct {
	CitationID      string        `json:"citation_id"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
}

type ragValidateCitationExecutionArguments struct {
	Citations []ragCitationTuple `json:"citations"`
}

type ragReadSourceExecutionOutput struct {
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	ContentHash     string `json:"content_hash"`
	Excerpt         string `json:"excerpt"`
}

type ragValidateCitationExecutionResult struct {
	ragCitationTuple
	Valid      bool   `json:"valid"`
	ReasonCode string `json:"reason_code"`
}

type ragValidateCitationExecutionOutput struct {
	Results []ragValidateCitationExecutionResult `json:"results"`
}

type ragReadSourceModelOutput struct {
	EvidenceRef string `json:"evidence_ref"`
	Excerpt     string `json:"excerpt"`
}

type ragValidateCitationModelResult struct {
	EvidenceRef string `json:"evidence_ref"`
	Valid       bool   `json:"valid"`
	ReasonCode  string `json:"reason_code"`
}

type ragValidateCitationModelOutput struct {
	Results []ragValidateCitationModelResult `json:"results"`
}

// RAGAgentExecutionService 是 Tool ExecutionService 暴露给 RAG 组合根的窄接口。
type RAGAgentExecutionService interface {
	Execute(context.Context, toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error)
}

// NewRAGAgentToolBridge 从 v2 RegisteredDefinition 与冻结 Tool Registry
// 构造精确白名单。v1 replay 不会创建该 bridge。
func NewRAGAgentToolBridge(
	execution workflowapplication.ExecutionContext,
	contracts *toolsapplication.Registry,
	service RAGAgentExecutionService,
) (*RAGAgentToolBridge, error) {
	if contracts == nil || nilRAGAgentExecutionService(service) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ragAgentToolBridgeUnavailable, false, errors.New("rag agent tool dependencies are unavailable"))
	}
	definition := conversationworkflow.RegisteredDefinitionV2()
	if execution.NodeKind != RAGWorkflowNodeKind || execution.NodeKey != conversationworkflow.NodeKey ||
		execution.InputSchemaVersion != RAGWorkflowInputSchemaVersion || execution.DefinitionVersion != definition.Version ||
		execution.DefinitionHash != definition.GraphHash || execution.AttemptNo < 1 || execution.DispatchNo < 1 ||
		execution.RetryNo < 0 || execution.NodeVersion < 1 {
		return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolIdentityInvalid, false, errors.New("rag agent workflow claim does not match the frozen v2 definition"))
	}
	identity := toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: execution.WorkspaceID, DefinitionID: execution.DefinitionID,
		DefinitionVersion: execution.DefinitionVersion, DefinitionHash: execution.DefinitionHash,
		WorkflowRunID: execution.RunID, NodeKey: execution.NodeKey, NodeRunID: execution.NodeRunID,
		NodeAttemptID: execution.NodeAttemptID, LeaseOwner: execution.LeaseOwner, LeaseFence: int64(execution.AttemptNo),
	}
	if err := identity.Validate(); err != nil {
		return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolIdentityInvalid, false, err)
	}
	if len(definition.Graph.Nodes) != 1 || len(definition.Graph.Nodes[0].AllowedTools) == 0 {
		return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolIdentityInvalid, false, errors.New("rag agent definition has no exact tool allowlist"))
	}
	allowed := make(map[toolsdomain.ToolRef]struct{}, len(definition.Graph.Nodes[0].AllowedTools))
	outputDecoders := make(map[toolsdomain.ToolRef]toolsapplication.DocumentDecoder, len(definition.Graph.Nodes[0].AllowedTools))
	specs := make([]agentapplication.AgentToolSpec, 0, len(definition.Graph.Nodes[0].AllowedTools))
	for _, ref := range definition.Graph.Nodes[0].AllowedTools {
		contract, err := contracts.ResolveContract(ref)
		if err != nil {
			return nil, err
		}
		if contract.Definition.Ref != ref || contract.Definition.SideEffectLevel != toolsdomain.SideEffectNone ||
			!contract.Definition.AllowsInvocation(toolsdomain.InvocationSourceModelRequest) {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolNotAllowed, false, errors.New("rag agent allowlist contains a non-read-only tool contract"))
		}
		if _, duplicate := allowed[ref]; duplicate {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolNotAllowed, false, errors.New("rag agent allowlist contains a duplicate tool"))
		}
		allowed[ref] = struct{}{}
		outputDecoders[ref] = contract.DecodeOutput
		specs = append(specs, agentapplication.AgentToolSpec{
			Ref: ref, Name: ref.Name, Description: contract.Definition.Description,
			InputSchema: append([]byte(nil), contract.Definition.InputSchemaDocument...),
		})
	}
	sort.Slice(specs, func(left, right int) bool {
		if specs[left].Name == specs[right].Name {
			return specs[left].Ref.Version < specs[right].Ref.Version
		}
		return specs[left].Name < specs[right].Name
	})
	return &RAGAgentToolBridge{
		service: service, identity: identity, specs: specs, allowed: allowed, outputDecoders: outputDecoders,
	}, nil
}

// BindMetadataScope 为一次冻结的 RAG generation 构造短引用 Tool bridge。
// 返回值与基础 bridge 共享只读执行依赖，但独占 E*/C* 映射和模型 schema，
// 因而并发生成之间不会泄漏 Citation、Workspace 或 Source 身份。
func (bridge *RAGAgentToolBridge) BindMetadataScope(bindings agentdomain.RAGAnswerMetadataBindings) (*RAGAgentToolBridge, error) {
	if bridge == nil || nilRAGAgentExecutionService(bridge.service) || len(bridge.specs) == 0 {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ragAgentToolBridgeUnavailable, false, errors.New("rag agent tool bridge is unavailable"))
	}
	if err := bindings.Validate(); err != nil {
		return nil, workflowError(foundation.ErrorInvalidInput, ragAgentToolScopeInvalid, false, errors.New("rag agent tool scope is invalid"))
	}
	scope := &ragAgentToolScope{
		evidenceByRef: make(map[string]agentdomain.Citation, len(bindings.Evidence)),
		evidenceByID:  make(map[string]string, len(bindings.Evidence)),
		conflictByRef: make(map[string][]string, len(bindings.Conflicts)),
		evidenceRefs:  make([]string, 0, len(bindings.Evidence)),
		conflictRefs:  make([]string, 0, len(bindings.Conflicts)),
	}
	for _, binding := range bindings.Evidence {
		if binding.Citation.WorkspaceID != bridge.identity.WorkspaceID {
			return nil, workflowError(foundation.ErrorPermissionDenied, ragAgentToolReferenceDenied, false, errors.New("rag agent tool scope crosses its trusted workspace"))
		}
		scope.evidenceByRef[binding.Ref] = binding.Citation
		scope.evidenceByID[binding.Citation.ID] = binding.Ref
		scope.evidenceRefs = append(scope.evidenceRefs, binding.Ref)
	}
	for _, binding := range bindings.Conflicts {
		refs := make([]string, len(binding.CitationIDs))
		for index, citationID := range binding.CitationIDs {
			ref, ok := scope.evidenceByID[citationID]
			if !ok {
				return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolScopeInvalid, false, errors.New("rag agent conflict scope references unknown evidence"))
			}
			refs[index] = ref
		}
		scope.conflictByRef[binding.Ref] = refs
		scope.conflictRefs = append(scope.conflictRefs, binding.Ref)
	}

	boundSpecs := make([]agentapplication.AgentToolSpec, len(bridge.specs))
	for index, spec := range bridge.specs {
		boundSpecs[index] = spec
		var err error
		switch spec.Ref.Name {
		case ragReadSourceTool:
			boundSpecs[index].Description = "Open one frozen approved evidence excerpt by its E* reference."
			boundSpecs[index].InputSchema, err = ragReadSourceModelSchema(scope.evidenceRefs)
		case ragValidateCitationTool:
			boundSpecs[index].Description = "Validate frozen evidence selected by E* evidence refs or C* conflict refs; provide both arrays and leave an unused array empty."
			boundSpecs[index].InputSchema, err = ragValidateCitationModelSchema(scope.evidenceRefs, scope.conflictRefs)
		default:
			err = errors.New("rag agent scope contains an unsupported tool")
		}
		if err != nil {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolScopeInvalid, false, err)
		}
	}

	return &RAGAgentToolBridge{
		service: bridge.service, identity: bridge.identity, specs: boundSpecs,
		allowed: cloneRAGAgentToolAllowlist(bridge.allowed), outputDecoders: cloneRAGAgentToolDecoders(bridge.outputDecoders), scope: scope,
	}, nil
}

// Specs 返回模型可见的冻结 schema 深拷贝。
func (bridge *RAGAgentToolBridge) Specs() []agentapplication.AgentToolSpec {
	if bridge == nil || bridge.scope == nil {
		return nil
	}
	output := make([]agentapplication.AgentToolSpec, len(bridge.specs))
	for index := range bridge.specs {
		output[index] = bridge.specs[index]
		output[index].InputSchema = append([]byte(nil), bridge.specs[index].InputSchema...)
	}
	return output
}

// Invoke 经共享 ExecutionService 执行并持久化一次只读 ToolCall。
func (bridge *RAGAgentToolBridge) Invoke(ctx context.Context, invocation agentapplication.AgentToolInvocation) (agentapplication.AgentToolResult, error) {
	if bridge == nil || nilRAGAgentExecutionService(bridge.service) {
		return agentapplication.AgentToolResult{}, workflowError(foundation.ErrorDependencyUnavailable, ragAgentToolBridgeUnavailable, false, errors.New("rag agent tool bridge is unavailable"))
	}
	if bridge.scope == nil {
		return agentapplication.AgentToolResult{}, workflowError(foundation.ErrorPermissionDenied, ragAgentToolScopeInvalid, false, errors.New("rag agent tool bridge has no frozen short-reference scope"))
	}
	if _, ok := bridge.allowed[invocation.Ref]; !ok || invocation.Ref.Name == "" {
		return agentapplication.AgentToolResult{}, workflowError(foundation.ErrorPermissionDenied, ragAgentToolNotAllowed, false, errors.New("rag agent requested a tool outside the exact allowlist"))
	}
	arguments := append([]byte(nil), invocation.Arguments...)
	arguments, selectedRefs, err := bridge.expandScopedArguments(invocation.Ref, invocation.Arguments)
	if err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	request := toolsdomain.ToolRequestV1{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1, ToolName: invocation.Ref.Name,
		Arguments: arguments, Reason: invocation.Reason,
	}
	result, err := bridge.service.Execute(ctx, toolsapplication.ExecuteToolCommand{
		Identity: bridge.identity, Invocation: toolsdomain.InvocationSourceModelRequest,
		Request: request, CallNo: invocation.CallNo,
	})
	if err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	if result.Call.Status != toolsdomain.CallSucceeded || result.Call.Tool == nil || *result.Call.Tool != invocation.Ref ||
		!result.UntrustedData || len(result.Output) == 0 || result.Call.ResponseBytes != int64(len(result.Output)) {
		return agentapplication.AgentToolResult{}, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent tool result differs from its persisted receipt"))
	}
	output, err := bridge.projectScopedOutput(invocation.Ref, selectedRefs, result.Output)
	if err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	return agentapplication.AgentToolResult{Output: output}, nil
}

func (bridge *RAGAgentToolBridge) expandScopedArguments(ref toolsdomain.ToolRef, raw []byte) ([]byte, []string, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 64 * 1024
	limits.MaxDepth = 4
	limits.MaxStringBytes = 16
	limits.MaxArrayItems = ragAgentToolMaxRefs
	limits.MaxObjectFields = 2
	switch ref.Name {
	case ragReadSourceTool:
		arguments, err := strictjson.DecodeObject(raw, limits, func(value ragReadSourceArguments) error {
			if !strings.HasPrefix(value.EvidenceRef, "E") {
				return errors.New("read source requires one evidence ref")
			}
			return nil
		})
		if err != nil {
			return nil, nil, workflowError(foundation.ErrorInvalidInput, ragAgentToolArgumentsInvalid, false, errors.New("rag agent read-source arguments are invalid"))
		}
		citation, ok := bridge.scope.evidenceByRef[arguments.EvidenceRef]
		if !ok {
			return nil, nil, workflowError(foundation.ErrorPermissionDenied, ragAgentToolReferenceDenied, false, errors.New("rag agent requested evidence outside its frozen scope"))
		}
		expanded, err := json.Marshal(ragReadSourceExecutionArguments{
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		})
		if err != nil {
			return nil, nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolScopeInvalid, false, errors.New("rag agent read-source arguments could not be expanded"))
		}
		return expanded, []string{arguments.EvidenceRef}, nil
	case ragValidateCitationTool:
		arguments, err := strictjson.DecodeObject(raw, limits, func(value ragValidateCitationArguments) error {
			if value.EvidenceRefs == nil || value.ConflictRefs == nil || len(value.EvidenceRefs)+len(value.ConflictRefs) == 0 ||
				len(value.EvidenceRefs)+len(value.ConflictRefs) > ragAgentToolMaxRefs ||
				hasDuplicateStrings(value.EvidenceRefs) || hasDuplicateStrings(value.ConflictRefs) {
				return errors.New("citation validation refs are missing, duplicated, or oversized")
			}
			return nil
		})
		if err != nil {
			return nil, nil, workflowError(foundation.ErrorInvalidInput, ragAgentToolArgumentsInvalid, false, errors.New("rag agent citation arguments are invalid"))
		}
		selected, err := bridge.selectScopedEvidence(arguments)
		if err != nil {
			return nil, nil, err
		}
		citations := make([]ragCitationTuple, len(selected))
		for index, evidenceRef := range selected {
			citation := bridge.scope.evidenceByRef[evidenceRef]
			citations[index] = ragCitationTuple{
				CitationID: citation.ID, IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
				SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
			}
		}
		expanded, marshalErr := json.Marshal(ragValidateCitationExecutionArguments{Citations: citations})
		if marshalErr != nil {
			return nil, nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolScopeInvalid, false, errors.New("rag agent citation arguments could not be expanded"))
		}
		return expanded, selected, nil
	default:
		return nil, nil, workflowError(foundation.ErrorPermissionDenied, ragAgentToolNotAllowed, false, errors.New("rag agent requested an unsupported scoped tool"))
	}
}

func (bridge *RAGAgentToolBridge) selectScopedEvidence(arguments ragValidateCitationArguments) ([]string, error) {
	selected := make([]string, 0, len(arguments.EvidenceRefs)+len(arguments.ConflictRefs))
	seen := make(map[string]struct{}, len(arguments.EvidenceRefs)+len(arguments.ConflictRefs))
	appendEvidence := func(ref string) error {
		if _, ok := bridge.scope.evidenceByRef[ref]; !ok {
			return workflowError(foundation.ErrorPermissionDenied, ragAgentToolReferenceDenied, false, errors.New("rag agent requested evidence outside its frozen scope"))
		}
		if _, duplicate := seen[ref]; duplicate {
			return workflowError(foundation.ErrorInvalidInput, ragAgentToolArgumentsInvalid, false, errors.New("rag agent citation arguments select overlapping evidence"))
		}
		seen[ref] = struct{}{}
		selected = append(selected, ref)
		return nil
	}
	for _, ref := range arguments.EvidenceRefs {
		if err := appendEvidence(ref); err != nil {
			return nil, err
		}
	}
	for _, conflictRef := range arguments.ConflictRefs {
		evidenceRefs, ok := bridge.scope.conflictByRef[conflictRef]
		if !ok {
			return nil, workflowError(foundation.ErrorPermissionDenied, ragAgentToolReferenceDenied, false, errors.New("rag agent requested a conflict outside its frozen scope"))
		}
		for _, evidenceRef := range evidenceRefs {
			if err := appendEvidence(evidenceRef); err != nil {
				return nil, err
			}
		}
	}
	return selected, nil
}

func (bridge *RAGAgentToolBridge) projectScopedOutput(ref toolsdomain.ToolRef, selectedRefs []string, raw []byte) ([]byte, error) {
	decoder := bridge.outputDecoders[ref]
	if decoder == nil {
		return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent tool output decoder is unavailable"))
	}
	canonical, err := decoder(raw)
	if err != nil {
		return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent tool output does not match its frozen contract"))
	}
	switch ref.Name {
	case ragReadSourceTool:
		if len(selectedRefs) != 1 {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent read-source result has no evidence binding"))
		}
		var output ragReadSourceExecutionOutput
		if json.Unmarshal(canonical, &output) != nil {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent read-source result could not be decoded"))
		}
		citation := bridge.scope.evidenceByRef[selectedRefs[0]]
		if output.SourceVersionID != string(citation.SourceVersionID) || output.SourceSpanID != string(citation.SourceSpanID) {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent read-source result differs from its evidence binding"))
		}
		projected, marshalErr := json.Marshal(ragReadSourceModelOutput{EvidenceRef: selectedRefs[0], Excerpt: output.Excerpt})
		if marshalErr != nil {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent read-source result could not be projected"))
		}
		return projected, nil
	case ragValidateCitationTool:
		var output ragValidateCitationExecutionOutput
		if json.Unmarshal(canonical, &output) != nil || len(output.Results) != len(selectedRefs) {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent citation result count is invalid"))
		}
		projected := ragValidateCitationModelOutput{Results: make([]ragValidateCitationModelResult, len(output.Results))}
		for index, result := range output.Results {
			citation := bridge.scope.evidenceByRef[selectedRefs[index]]
			expected := ragCitationTuple{
				CitationID: citation.ID, IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
				SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
			}
			if result.ragCitationTuple != expected {
				return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent citation result differs from its evidence binding"))
			}
			projected.Results[index] = ragValidateCitationModelResult{
				EvidenceRef: selectedRefs[index], Valid: result.Valid, ReasonCode: result.ReasonCode,
			}
		}
		encoded, marshalErr := json.Marshal(projected)
		if marshalErr != nil {
			return nil, workflowError(foundation.ErrorConsistencyViolation, ragAgentToolResultInvalid, false, errors.New("rag agent citation result could not be projected"))
		}
		return encoded, nil
	default:
		return nil, workflowError(foundation.ErrorPermissionDenied, ragAgentToolNotAllowed, false, errors.New("rag agent requested an unsupported scoped tool"))
	}
}

func ragReadSourceModelSchema(evidenceRefs []string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "additionalProperties": false,
		"required": []string{"evidence_ref"}, "properties": map[string]any{
			"evidence_ref": map[string]any{"type": "string", "enum": append([]string(nil), evidenceRefs...)},
		},
	})
}

func ragValidateCitationModelSchema(evidenceRefs, conflictRefs []string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "additionalProperties": false,
		"required": []string{"evidence_refs", "conflict_refs"}, "properties": map[string]any{
			"evidence_refs": map[string]any{
				"type": "array", "minItems": 0, "maxItems": len(evidenceRefs),
				"items": map[string]any{"type": "string", "enum": append([]string(nil), evidenceRefs...)},
			},
			"conflict_refs": map[string]any{
				"type": "array", "minItems": 0, "maxItems": len(conflictRefs),
				"items": map[string]any{"type": "string", "enum": append([]string(nil), conflictRefs...)},
			},
		},
	})
}

func cloneRAGAgentToolAllowlist(input map[toolsdomain.ToolRef]struct{}) map[toolsdomain.ToolRef]struct{} {
	output := make(map[toolsdomain.ToolRef]struct{}, len(input))
	for ref := range input {
		output[ref] = struct{}{}
	}
	return output
}

func cloneRAGAgentToolDecoders(input map[toolsdomain.ToolRef]toolsapplication.DocumentDecoder) map[toolsdomain.ToolRef]toolsapplication.DocumentDecoder {
	output := make(map[toolsdomain.ToolRef]toolsapplication.DocumentDecoder, len(input))
	for ref, decoder := range input {
		output[ref] = decoder
	}
	return output
}

func hasDuplicateStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func nilRAGAgentExecutionService(service RAGAgentExecutionService) bool {
	if service == nil {
		return true
	}
	value := reflect.ValueOf(service)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

var _ agentapplication.AgentToolInvoker = (*RAGAgentToolBridge)(nil)
