package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// ReadGitStatusV2ReceiptMaxOutputBytes 是 ReadGitStatus@2 canonical 输出上限。
	ReadGitStatusV2ReceiptMaxOutputBytes int64 = 4 * 1024
	// ReadGitStatusV2ReceiptMaxPrivateBindingBytes 是 ReadGitStatus@2 私有绑定上限。
	ReadGitStatusV2ReceiptMaxPrivateBindingBytes int64 = 1024
	// SearchKnowledgeV2ReceiptMaxOutputBytes 是 SearchKnowledge@2 canonical 输出上限。
	SearchKnowledgeV2ReceiptMaxOutputBytes int64 = 32 * 1024
	// SearchKnowledgeV2ReceiptMaxPrivateBindingBytes 是 SearchKnowledge@2 私有绑定上限。
	SearchKnowledgeV2ReceiptMaxPrivateBindingBytes int64 = 16 * 1024
	// ReadSourceV3ReceiptMaxOutputBytes 是 ReadSource@3 canonical 输出上限。
	ReadSourceV3ReceiptMaxOutputBytes int64 = 8 * 1024
	// ReadSourceV3ReceiptMaxPrivateBindingBytes 是 ReadSource@3 私有绑定上限。
	ReadSourceV3ReceiptMaxPrivateBindingBytes int64 = 4 * 1024
	// ValidateCitationV3ReceiptMaxOutputBytes 是 ValidateCitation@3 canonical 输出上限。
	ValidateCitationV3ReceiptMaxOutputBytes int64 = 16 * 1024
	// ValidateCitationV3ReceiptMaxPrivateBindingBytes 是 ValidateCitation@3 私有绑定上限。
	ValidateCitationV3ReceiptMaxPrivateBindingBytes int64 = 16 * 1024
)

const (
	workspaceAnalysisWorkflowKey     = "workspace-analysis"
	workspaceAnalysisWorkflowVersion = int64(1)
)

// ResultReceiptContract 冻结一个 exact Tool 可持久化的输出与私有绑定边界。
type ResultReceiptContract struct {
	// Tool 是唯一允许持久化该合同的 exact Tool 版本。
	Tool ToolRef
	// OutputSchema 是模型安全 canonical 输出的 exact Schema。
	OutputSchema SchemaRef
	// PrivateBindingSchema 是 server-only 身份绑定的 exact Schema。
	PrivateBindingSchema SchemaRef
	// MaxOutputBytes 是 canonical 输出编码后的最大字节数。
	MaxOutputBytes int64
	// MaxPrivateBindingBytes 是 canonical 私有绑定编码后的最大字节数。
	MaxPrivateBindingBytes int64
	// RequiresPrivateBinding 表示成功回执不得省略私有身份绑定。
	RequiresPrivateBinding bool
}

// ResultReceiptDraft 是成功 Tool Call 终结事务写入回执所需的最小文档。
type ResultReceiptDraft struct {
	// ID 是待插入回执的 canonical UUID。
	ID foundation.ID
	// Output 是已经过 exact Tool output decoder 验证的模型安全文档。
	Output json.RawMessage `json:"-"`
	// PrivateBindingSchema 显式声明 server-only 文档采用的 exact Schema。
	PrivateBindingSchema SchemaRef
	// PrivateBinding 是已经过 exact Tool private binding decoder 验证的文档。
	PrivateBinding json.RawMessage `json:"-"`
	// CreatedAt 是与数据库精度一致的回执创建时间。
	CreatedAt time.Time
}

// ResultReceiptPrivateBinding 保存只供受信 resolver 读取的 canonical 身份绑定。
type ResultReceiptPrivateBinding struct {
	// Schema 是私有身份绑定的 exact Schema。
	Schema SchemaRef
	// Document 是不得进入通用日志、HTTP 或时间线的 canonical 文档。
	Document json.RawMessage `json:"-"`
	// Hash 是 Document canonical 字节的 SHA-256 小写十六进制值。
	Hash string
	// Bytes 是 Document canonical 字节数。
	Bytes int64
}

// String 只返回私有绑定的 Schema、Hash 与长度，不返回文档。
func (binding ResultReceiptPrivateBinding) String() string {
	return fmt.Sprintf("ResultReceiptPrivateBinding{schema:%s@%d hash:%s bytes:%d}",
		binding.Schema.ID, binding.Schema.Version, binding.Hash, binding.Bytes)
}

// GoString 避免 %#v 调试格式绕过私有绑定的安全 String 投影。
func (binding ResultReceiptPrivateBinding) GoString() string { return binding.String() }

// ResultReceipt 是与一个成功 Tool Call 一对一绑定的不可变 canonical 输出事实。
type ResultReceipt struct {
	// ID 是回执自身的 canonical UUID。
	ID foundation.ID
	// ToolCallID 是一对一拥有该回执的成功 Tool Call。
	ToolCallID foundation.ID
	// WorkspaceID 是 Tool Call 所属 Workspace。
	WorkspaceID foundation.ID
	// WorkflowRunID 是 Tool Call 所属 Workflow Run。
	WorkflowRunID foundation.ID
	// NodeRunID 是 Tool Call 所属 Node Run。
	NodeRunID foundation.ID
	// NodeAttemptID 是实际完成 Tool Call 的 Node Attempt。
	NodeAttemptID foundation.ID
	// Tool 是成功 Call 解析出的 exact Tool 版本。
	Tool ToolRef
	// OutputSchema 是 canonical 输出的 exact Schema。
	OutputSchema SchemaRef
	// DefinitionHash 是成功 Call 使用的 canonical Tool Definition hash。
	DefinitionHash string
	// PersistencePolicy 必须是 PERSIST_CANONICAL。
	PersistencePolicy ResultPersistencePolicy
	// MaxOutputBytes 是 exact Tool 合同冻结的输出上限。
	MaxOutputBytes int64
	// MaxPrivateBindingBytes 是 exact Tool 合同冻结的私有绑定上限。
	MaxPrivateBindingBytes int64
	// Output 是不得进入通用日志或错误的模型安全 canonical 文档。
	Output json.RawMessage `json:"-"`
	// OutputHash 是 Output canonical 字节的 SHA-256 小写十六进制值。
	OutputHash string
	// OutputBytes 是 Output canonical 字节数。
	OutputBytes int64
	// PrivateBinding 是只能由受信 resolver 读取的可选身份绑定。
	PrivateBinding *ResultReceiptPrivateBinding `json:"-"`
	// CreatedAt 是成功 Call 完成之后的不可变创建时间。
	CreatedAt time.Time
}

type readGitStatusV2ReceiptOutput struct {
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	ObjectFormat   string `json:"object_format"`
	Clean          *bool  `json:"clean"`
	StagedCount    *int   `json:"staged_count"`
	UnstagedCount  *int   `json:"unstaged_count"`
	UntrackedCount *int   `json:"untracked_count"`
	ConflictCount  *int   `json:"conflict_count"`
}

type readGitStatusV2PrivateBinding struct {
	WorkspaceRootHash      string `json:"workspace_root_hash"`
	RepositoryIdentityHash string `json:"repository_identity_hash"`
}

type searchKnowledgeV2ReceiptOutput struct {
	EffectiveMode string                         `json:"effective_mode"`
	Items         []searchKnowledgeV2ReceiptItem `json:"items"`
	Degradations  []string                       `json:"degradations"`
}

type searchKnowledgeV2ReceiptItem struct {
	EvidenceRef string `json:"evidence_ref"`
	Rank        int    `json:"rank"`
	Snippet     string `json:"snippet"`
}

type resultReceiptCitationIdentity struct {
	EvidenceRef     string        `json:"evidence_ref"`
	CitationID      string        `json:"citation_id"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	ContentHash     string        `json:"content_hash"`
}

type searchKnowledgeV2PrivateBinding struct {
	Items        []resultReceiptCitationIdentity `json:"items"`
	SelectedRefs []string                        `json:"selected_refs"`
}

type readSourceV3ReceiptOutput struct {
	EvidenceRef string `json:"evidence_ref"`
	ContentHash string `json:"content_hash"`
	Truncated   *bool  `json:"truncated"`
	Excerpt     string `json:"excerpt"`
}

type readSourceV3PrivateBinding struct {
	SearchReceiptID   foundation.ID `json:"search_receipt_id"`
	SearchReceiptHash string        `json:"search_receipt_hash"`
	resultReceiptCitationIdentity
}

// ReadSourceV3ReceiptEvidence 是从同 Run Search/Read receipt 闭包中得到的模型安全证据。
// 它只包含短引用和已经有界的可见摘录，不包含任何 Source/Citation 私有身份。
type ReadSourceV3ReceiptEvidence struct {
	EvidenceRef string
	Excerpt     string
	Truncated   bool
}

type validateCitationV3ReceiptOutput struct {
	Results []validateCitationV3ReceiptResult `json:"results"`
}

type validateCitationV3ReceiptResult struct {
	EvidenceRef string `json:"evidence_ref"`
	Valid       *bool  `json:"valid"`
	ReasonCode  string `json:"reason_code"`
}

// ValidateCitationV3ReceiptResult 是完成候选绑定校验后的安全 Citation 结果投影。
// 它不包含候选身份、完整 Citation tuple 或任何 Source 内容。
type ValidateCitationV3ReceiptResult struct {
	EvidenceRef string `json:"evidence_ref"`
	Valid       bool   `json:"valid"`
	ReasonCode  string `json:"reason_code"`
}

// ValidateCitationV3ReceiptCitation 是成功发布器可使用的受信 Citation 身份投影。
// 全部字段都来自已严格校验的 private binding，且禁止通用 JSON 或结构化日志反射序列化。
type ValidateCitationV3ReceiptCitation struct {
	EvidenceRef     string        `json:"-"`
	CitationID      string        `json:"-"`
	IndexVersionID  foundation.ID `json:"-"`
	ChunkID         foundation.ID `json:"-"`
	SourceVersionID foundation.ID `json:"-"`
	SourceSpanID    foundation.ID `json:"-"`
}

// String 不回显短引用、Citation ID 或私有身份 tuple。
func (citation ValidateCitationV3ReceiptCitation) String() string {
	return "ValidateCitationV3ReceiptCitation{redacted}"
}

// GoString 避免 %#v 展开私有身份 tuple。
func (citation ValidateCitationV3ReceiptCitation) GoString() string { return citation.String() }

// LogValue 禁止结构化日志反射序列化私有身份 tuple。
func (citation ValidateCitationV3ReceiptCitation) LogValue() slog.Value {
	return slog.GroupValue(slog.String("binding", "redacted"))
}

type validateCitationV3PrivateBinding struct {
	CandidateID   foundation.ID                   `json:"candidate_id"`
	CandidateHash string                          `json:"candidate_hash"`
	Results       []resultReceiptCitationIdentity `json:"results"`
}

// String 只返回允许进入日志的稳定 ID、Schema、Hash 与长度，不返回任何文档。
func (receipt ResultReceipt) String() string {
	bindingHash := ""
	bindingBytes := int64(0)
	if receipt.PrivateBinding != nil {
		bindingHash = receipt.PrivateBinding.Hash
		bindingBytes = receipt.PrivateBinding.Bytes
	}
	return fmt.Sprintf("ResultReceipt{id:%s tool_call_id:%s tool:%s@%d output_schema:%s@%d output_hash:%s output_bytes:%d private_binding_hash:%s private_binding_bytes:%d}",
		receipt.ID, receipt.ToolCallID, receipt.Tool.Name, receipt.Tool.Version, receipt.OutputSchema.ID,
		receipt.OutputSchema.Version, receipt.OutputHash, receipt.OutputBytes, bindingHash, bindingBytes)
}

// GoString 避免 %#v 调试格式绕过回执的安全 String 投影。
func (receipt ResultReceipt) GoString() string { return receipt.String() }

// WorkspaceAnalysisResultReceiptContracts 返回四个 Workspace Analysis Tool 的冻结回执合同副本。
func WorkspaceAnalysisResultReceiptContracts() []ResultReceiptContract {
	return []ResultReceiptContract{
		workspaceAnalysisResultReceiptContract(ToolRef{Name: "ReadGitStatus", Version: 2}),
		workspaceAnalysisResultReceiptContract(ToolRef{Name: "SearchKnowledge", Version: 2}),
		workspaceAnalysisResultReceiptContract(ToolRef{Name: "ReadSource", Version: 3}),
		workspaceAnalysisResultReceiptContract(ToolRef{Name: "ValidateCitation", Version: 3}),
	}
}

// WorkspaceAnalysisResultReceiptContract 返回 exact Tool 的冻结回执合同。
func WorkspaceAnalysisResultReceiptContract(ref ToolRef) (ResultReceiptContract, bool) {
	contract := workspaceAnalysisResultReceiptContract(ref)
	return contract, contract.Tool.Name != ""
}

// NewResultReceipt 规范化成功输出并从权威 Call/Definition 派生不可变回执。
func NewResultReceipt(draft ResultReceiptDraft, call ToolCall, definition Definition) (ResultReceipt, error) {
	contract, err := validateResultReceiptAuthority(call, definition)
	if err != nil {
		return ResultReceipt{}, err
	}
	output, err := canonicalJSONObject(draft.Output, int(contract.MaxOutputBytes))
	if err != nil || int64(len(output)) > contract.MaxOutputBytes {
		return ResultReceipt{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt output is invalid")
	}

	var privateBinding *ResultReceiptPrivateBinding
	if len(draft.PrivateBinding) == 0 {
		if draft.PrivateBindingSchema != (SchemaRef{}) || contract.RequiresPrivateBinding {
			return ResultReceipt{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt private binding is required")
		}
	} else {
		binding, bindingErr := canonicalJSONObject(draft.PrivateBinding, int(contract.MaxPrivateBindingBytes))
		if bindingErr != nil || int64(len(binding)) > contract.MaxPrivateBindingBytes ||
			draft.PrivateBindingSchema != contract.PrivateBindingSchema || !safePrivateBinding(binding) {
			return ResultReceipt{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt private binding is invalid")
		}
		privateBinding = &ResultReceiptPrivateBinding{
			Schema:   contract.PrivateBindingSchema,
			Document: append(json.RawMessage(nil), binding...),
			Hash:     resultReceiptHash(binding),
			Bytes:    int64(len(binding)),
		}
	}
	if err := validateExactResultReceiptDocuments(contract, output, privateBinding); err != nil {
		return ResultReceipt{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt document does not match its exact schema")
	}

	receipt := ResultReceipt{
		ID: draft.ID, ToolCallID: call.ID, WorkspaceID: call.WorkspaceID, WorkflowRunID: call.WorkflowRunID,
		NodeRunID: call.NodeRunID, NodeAttemptID: call.NodeAttemptID, Tool: definition.Ref,
		OutputSchema: definition.OutputSchema, DefinitionHash: definition.DefinitionHash,
		PersistencePolicy: definition.ResultPersistencePolicy, MaxOutputBytes: contract.MaxOutputBytes,
		MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes, Output: append(json.RawMessage(nil), output...),
		OutputHash: resultReceiptHash(output), OutputBytes: int64(len(output)), PrivateBinding: privateBinding,
		CreatedAt: draft.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	if err := ValidateResultReceipt(receipt, call, definition); err != nil {
		return ResultReceipt{}, err
	}
	return receipt, nil
}

// ValidateResultReceipt 校验回放回执与权威成功 Call、Definition 和 canonical 文档逐项相等。
func ValidateResultReceipt(receipt ResultReceipt, call ToolCall, definition Definition) error {
	contract, err := validateResultReceiptAuthority(call, definition)
	if err != nil {
		return err
	}
	if err := validateResultReceiptIDs(receipt); err != nil {
		return err
	}
	if receipt.ToolCallID != call.ID || receipt.WorkspaceID != call.WorkspaceID || receipt.WorkflowRunID != call.WorkflowRunID ||
		receipt.NodeRunID != call.NodeRunID || receipt.NodeAttemptID != call.NodeAttemptID || receipt.Tool != definition.Ref ||
		receipt.OutputSchema != definition.OutputSchema || receipt.DefinitionHash != definition.DefinitionHash ||
		receipt.PersistencePolicy != ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt authority binding drifted")
	}
	if receipt.CreatedAt.IsZero() || !receipt.CreatedAt.Equal(receipt.CreatedAt.UTC().Truncate(time.Microsecond)) ||
		call.CompletedAt == nil || receipt.CreatedAt.Before(call.CompletedAt.UTC().Truncate(time.Microsecond)) {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt creation time drifted")
	}

	output, canonicalErr := canonicalJSONObject(receipt.Output, int(contract.MaxOutputBytes))
	if canonicalErr != nil || int64(len(output)) > contract.MaxOutputBytes || !bytes.Equal(output, receipt.Output) || receipt.OutputBytes != int64(len(output)) ||
		receipt.OutputHash != resultReceiptHash(output) || call.ResponseBytes != receipt.OutputBytes || call.ResponseHash != receipt.OutputHash {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt output binding drifted")
	}
	if err := validateResultReceiptPrivateBinding(receipt.PrivateBinding, contract); err != nil {
		return err
	}
	if err := validateExactResultReceiptDocuments(contract, receipt.Output, receipt.PrivateBinding); err != nil {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt document schema drifted")
	}
	return nil
}

func workspaceAnalysisResultReceiptContract(ref ToolRef) ResultReceiptContract {
	switch ref {
	case ToolRef{Name: "ReadGitStatus", Version: 2}:
		return ResultReceiptContract{
			Tool: ref, OutputSchema: SchemaRef{ID: "tool.read_git_status.output", Version: 1},
			PrivateBindingSchema: SchemaRef{ID: "tool.read_git_status.private_binding", Version: 1},
			MaxOutputBytes:       ReadGitStatusV2ReceiptMaxOutputBytes, MaxPrivateBindingBytes: ReadGitStatusV2ReceiptMaxPrivateBindingBytes,
		}
	case ToolRef{Name: "SearchKnowledge", Version: 2}:
		return ResultReceiptContract{
			Tool: ref, OutputSchema: SchemaRef{ID: "tool.search_knowledge.output", Version: 2},
			PrivateBindingSchema: SchemaRef{ID: "tool.search_knowledge.private_binding", Version: 1},
			MaxOutputBytes:       SearchKnowledgeV2ReceiptMaxOutputBytes, MaxPrivateBindingBytes: SearchKnowledgeV2ReceiptMaxPrivateBindingBytes,
			RequiresPrivateBinding: true,
		}
	case ToolRef{Name: "ReadSource", Version: 3}:
		return ResultReceiptContract{
			Tool: ref, OutputSchema: SchemaRef{ID: "tool.read_source.output", Version: 2},
			PrivateBindingSchema: SchemaRef{ID: "tool.read_source.private_binding", Version: 1},
			MaxOutputBytes:       ReadSourceV3ReceiptMaxOutputBytes, MaxPrivateBindingBytes: ReadSourceV3ReceiptMaxPrivateBindingBytes,
			RequiresPrivateBinding: true,
		}
	case ToolRef{Name: "ValidateCitation", Version: 3}:
		return ResultReceiptContract{
			Tool: ref, OutputSchema: SchemaRef{ID: "tool.validate_citation.output", Version: 2},
			PrivateBindingSchema: SchemaRef{ID: "tool.validate_citation.private_binding", Version: 1},
			MaxOutputBytes:       ValidateCitationV3ReceiptMaxOutputBytes, MaxPrivateBindingBytes: ValidateCitationV3ReceiptMaxPrivateBindingBytes,
			RequiresPrivateBinding: true,
		}
	default:
		return ResultReceiptContract{}
	}
}

func validateResultReceiptAuthority(call ToolCall, definition Definition) (ResultReceiptContract, error) {
	return validateResultReceiptAuthorityWithin(call, definition, 0)
}

func validateResultReceiptAuthorityWithin(
	call ToolCall,
	definition Definition,
	maxResponseBytes int64,
) (ResultReceiptContract, error) {
	contract, found := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	if !found {
		return ResultReceiptContract{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt contract is unsupported")
	}
	canonical, err := CanonicalizeDefinition(definition)
	if err != nil {
		return ResultReceiptContract{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt definition is invalid")
	}
	canonicalBytes, canonicalErr := json.Marshal(canonical)
	definitionBytes, definitionErr := json.Marshal(definition)
	if canonicalErr != nil || definitionErr != nil || !bytes.Equal(canonicalBytes, definitionBytes) ||
		definition.DefinitionHash != canonical.DefinitionHash || definition.OutputSchema != contract.OutputSchema ||
		definition.MaxOutputBytes != contract.MaxOutputBytes || definition.RequiredCapability != capability.ReadLocal ||
		definition.SideEffectLevel != SideEffectNone || definition.InvocationPolicy != InvocationTrustedWorkflowOnly ||
		definition.ResultPersistencePolicy != ResultPersistenceCanonical || len(definition.AllowedWorkflows) != 1 ||
		definition.AllowedWorkflows[0] != (WorkflowBinding{Key: workspaceAnalysisWorkflowKey, Version: workspaceAnalysisWorkflowVersion}) {
		return ResultReceiptContract{}, invalid(ErrorCodeResultReceiptInvalid, "tool result receipt definition is not canonical or opted in")
	}
	if err := ValidateToolCall(call); err != nil {
		return ResultReceiptContract{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt call is invalid")
	}
	if maxResponseBytes <= 0 {
		maxResponseBytes = contract.MaxOutputBytes
	}
	if call.Status != CallSucceeded || call.Tool == nil || *call.Tool != definition.Ref || call.DefinitionHash != definition.DefinitionHash ||
		call.InputSchema == nil || *call.InputSchema != definition.InputSchema || call.OutputSchema == nil || *call.OutputSchema != definition.OutputSchema ||
		call.Capability != definition.RequiredCapability || call.SideEffectLevel != definition.SideEffectLevel ||
		call.InvocationPolicy != definition.InvocationPolicy || call.ResponseHash == "" || call.ResponseBytes <= 0 ||
		call.ResponseBytes > maxResponseBytes || call.ResultRef != "" || call.SideEffectType != "" || call.SideEffectID != "" {
		return ResultReceiptContract{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "only an exact successful tool call may own a result receipt")
	}
	return contract, nil
}

func validateResultReceiptIDs(receipt ResultReceipt) error {
	ids := []foundation.ID{receipt.ID, receipt.ToolCallID, receipt.WorkspaceID, receipt.WorkflowRunID, receipt.NodeRunID, receipt.NodeAttemptID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return invalid(ErrorCodeResultReceiptInvalid, "tool result receipt identity is invalid")
		}
		if _, exists := seen[id]; exists {
			return invalid(ErrorCodeResultReceiptInvalid, "tool result receipt reuses an identity")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateResultReceiptPrivateBinding(binding *ResultReceiptPrivateBinding, contract ResultReceiptContract) error {
	if binding == nil {
		if contract.RequiresPrivateBinding {
			return inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt private binding is missing")
		}
		return nil
	}
	canonical, err := canonicalJSONObject(binding.Document, int(contract.MaxPrivateBindingBytes))
	if err != nil || int64(len(canonical)) > contract.MaxPrivateBindingBytes || !bytes.Equal(canonical, binding.Document) || !safePrivateBinding(canonical) ||
		binding.Schema != contract.PrivateBindingSchema || binding.Bytes != int64(len(canonical)) || binding.Hash != resultReceiptHash(canonical) {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "tool result receipt private binding drifted")
	}
	return nil
}

func validateExactResultReceiptDocuments(
	contract ResultReceiptContract,
	output json.RawMessage,
	binding *ResultReceiptPrivateBinding,
) error {
	switch contract.Tool {
	case ToolRef{Name: "ReadGitStatus", Version: 2}:
		if _, err := decodeExactReceiptDocument(output, contract.MaxOutputBytes, validateReadGitStatusV2ReceiptOutput); err != nil {
			return err
		}
		if binding == nil {
			return nil
		}
		_, err := decodeExactReceiptDocument(binding.Document, contract.MaxPrivateBindingBytes, validateReadGitStatusV2PrivateBinding)
		return err
	case ToolRef{Name: "SearchKnowledge", Version: 2}:
		decodedOutput, err := decodeExactReceiptDocument(output, contract.MaxOutputBytes, validateSearchKnowledgeV2ReceiptOutput)
		if err != nil || binding == nil {
			return errors.New("search receipt documents are invalid")
		}
		decodedBinding, err := decodeExactReceiptDocument(binding.Document, contract.MaxPrivateBindingBytes, validateSearchKnowledgeV2PrivateBinding)
		if err != nil || !searchKnowledgeV2ReceiptBindingMatches(decodedOutput, decodedBinding) {
			return errors.New("search receipt binding does not match output")
		}
		return nil
	case ToolRef{Name: "ReadSource", Version: 3}:
		decodedOutput, err := decodeExactReceiptDocument(output, contract.MaxOutputBytes, validateReadSourceV3ReceiptOutput)
		if err != nil || binding == nil {
			return errors.New("source receipt documents are invalid")
		}
		decodedBinding, err := decodeExactReceiptDocument(binding.Document, contract.MaxPrivateBindingBytes, validateReadSourceV3PrivateBinding)
		if err != nil || decodedOutput.EvidenceRef != decodedBinding.EvidenceRef || decodedOutput.ContentHash != decodedBinding.ContentHash {
			return errors.New("source receipt binding does not match output")
		}
		return nil
	case ToolRef{Name: "ValidateCitation", Version: 3}:
		decodedOutput, err := decodeExactReceiptDocument(output, contract.MaxOutputBytes, validateValidateCitationV3ReceiptOutput)
		if err != nil || binding == nil {
			return errors.New("citation receipt documents are invalid")
		}
		decodedBinding, err := decodeExactReceiptDocument(binding.Document, contract.MaxPrivateBindingBytes, validateValidateCitationV3PrivateBinding)
		if err != nil || !validateCitationV3ReceiptBindingMatches(decodedOutput, decodedBinding) {
			return errors.New("citation receipt binding does not match output")
		}
		return nil
	default:
		return errors.New("tool result receipt exact schema is unsupported")
	}
}

func decodeExactReceiptDocument[T any](document json.RawMessage, maxBytes int64, validate func(T) error) (T, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = int(maxBytes)
	limits.MaxDepth = 8
	limits.MaxStringBytes = int(maxBytes)
	limits.MaxArrayItems = 16
	limits.MaxObjectFields = 16
	return strictjson.DecodeObject(document, limits, validate)
}

func validateReadGitStatusV2ReceiptOutput(output readGitStatusV2ReceiptOutput) error {
	counts := []*int{output.StagedCount, output.UnstagedCount, output.UntrackedCount, output.ConflictCount}
	changes := 0
	for _, count := range counts {
		if count == nil || *count < 0 || *count > 100000 {
			return errors.New("Git receipt count is invalid")
		}
		changes += *count
	}
	if !canonicalReference(output.Branch, 255) ||
		(output.ObjectFormat != "sha1" && output.ObjectFormat != "sha256") ||
		!lowerHex(output.Head, map[string]int{"sha1": 40, "sha256": 64}[output.ObjectFormat]) ||
		output.Clean == nil || *output.Clean != (changes == 0) {
		return errors.New("Git receipt aggregate is invalid")
	}
	return nil
}

func validateReadGitStatusV2PrivateBinding(binding readGitStatusV2PrivateBinding) error {
	if !lowerHex(binding.WorkspaceRootHash, 64) || !lowerHex(binding.RepositoryIdentityHash, 64) {
		return errors.New("Git receipt private identity is invalid")
	}
	return nil
}

func validateSearchKnowledgeV2ReceiptOutput(output searchKnowledgeV2ReceiptOutput) error {
	if (output.EffectiveMode != "keyword" && output.EffectiveMode != "semantic" && output.EffectiveMode != "hybrid") ||
		output.Items == nil || len(output.Items) > 5 || output.Degradations == nil || len(output.Degradations) > 16 {
		return errors.New("search receipt output is invalid")
	}
	for index, item := range output.Items {
		if item.EvidenceRef != resultReceiptEvidenceRef(index+1) || item.Rank != index+1 ||
			!validResultReceiptText(item.Snippet, 1, 4*1024) {
			return errors.New("search receipt item is invalid")
		}
	}
	if !sort.StringsAreSorted(output.Degradations) || !validResultReceiptTokens(output.Degradations, 64) {
		return errors.New("search receipt degradations are invalid")
	}
	return nil
}

func validateSearchKnowledgeV2PrivateBinding(binding searchKnowledgeV2PrivateBinding) error {
	if binding.Items == nil || len(binding.Items) > 5 || binding.SelectedRefs == nil || len(binding.SelectedRefs) > 3 ||
		len(binding.SelectedRefs) != min(3, len(binding.Items)) {
		return errors.New("search receipt private binding is invalid")
	}
	citationIDs := make(map[string]struct{}, len(binding.Items))
	var indexVersionID foundation.ID
	for index, item := range binding.Items {
		if item.EvidenceRef != resultReceiptEvidenceRef(index+1) || !validResultReceiptCitationIdentity(item, 5) {
			return errors.New("search receipt private item is invalid")
		}
		if index == 0 {
			indexVersionID = item.IndexVersionID
		} else if item.IndexVersionID != indexVersionID {
			return errors.New("search receipt private items span multiple index versions")
		}
		if _, duplicate := citationIDs[item.CitationID]; duplicate {
			return errors.New("search receipt citation identity is duplicated")
		}
		citationIDs[item.CitationID] = struct{}{}
	}
	for index, ref := range binding.SelectedRefs {
		if ref != resultReceiptEvidenceRef(index+1) {
			return errors.New("search receipt selected refs are invalid")
		}
	}
	return nil
}

// SearchKnowledgeV2ReceiptIndexVersionID 从已验证的 SearchKnowledge@2 receipt 返回唯一命中索引。
// 零命中没有可绑定的索引，调用方只能将其用于确定性 evidence-insufficient 终止，不能继续合成答案。
func SearchKnowledgeV2ReceiptIndexVersionID(receipt ResultReceipt) (foundation.ID, bool, error) {
	_, binding, err := decodeSearchKnowledgeV2ReceiptAuthority(receipt)
	if err != nil {
		return "", false, err
	}
	if len(binding.Items) == 0 {
		return "", false, nil
	}
	return binding.Items[0].IndexVersionID, true, nil
}

// SearchKnowledgeV2ReceiptSelectedRefs 返回 Search receipt 冻结的连续读取集合。
// 返回值只有 E1..E3，不暴露任何 Source/Citation 私有身份。
func SearchKnowledgeV2ReceiptSelectedRefs(receipt ResultReceipt) ([]string, error) {
	_, binding, err := decodeSearchKnowledgeV2ReceiptAuthority(receipt)
	if err != nil {
		return nil, err
	}
	selected := make([]string, len(binding.SelectedRefs))
	copy(selected, binding.SelectedRefs)
	return selected, nil
}

// ReadSourceV3ReceiptEvidenceForSearch 验证一个 ReadSource@3 receipt 精确来自给定 SearchKnowledge@2 receipt。
// 返回值只投影可进入模型输入的短引用、摘录和截断标记。
func ReadSourceV3ReceiptEvidenceForSearch(
	receipt ResultReceipt,
	searchReceipt ResultReceipt,
) (ReadSourceV3ReceiptEvidence, error) {
	_, searchBinding, err := decodeSearchKnowledgeV2ReceiptAuthority(searchReceipt)
	if err != nil {
		return ReadSourceV3ReceiptEvidence{}, err
	}
	output, binding, err := decodeReadSourceV3ReceiptAuthority(receipt)
	if err != nil {
		return ReadSourceV3ReceiptEvidence{}, err
	}
	if binding.SearchReceiptID != searchReceipt.ID || binding.SearchReceiptHash != searchReceipt.OutputHash {
		return ReadSourceV3ReceiptEvidence{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"source receipt does not bind the authoritative search receipt",
		)
	}
	selected := false
	for _, reference := range searchBinding.SelectedRefs {
		if reference == output.EvidenceRef {
			selected = true
			break
		}
	}
	if !selected {
		return ReadSourceV3ReceiptEvidence{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"source receipt reference was not selected by the search receipt",
		)
	}
	matched := false
	for _, identity := range searchBinding.Items {
		if identity.EvidenceRef == output.EvidenceRef {
			matched = identity == binding.resultReceiptCitationIdentity
			break
		}
	}
	if !matched {
		return ReadSourceV3ReceiptEvidence{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"source receipt citation identity differs from the search receipt",
		)
	}
	return ReadSourceV3ReceiptEvidence{
		EvidenceRef: output.EvidenceRef,
		Excerpt:     output.Excerpt,
		Truncated:   *output.Truncated,
	}, nil
}

// ValidateCitationV3ReceiptResults 验证 ValidateCitation@3 receipt 精确绑定指定候选和有序短引用。
// 返回值只包含可进入 Workflow 输出的安全结果，不暴露 server-only Citation 身份。
func ValidateCitationV3ReceiptResults(
	receipt ResultReceipt,
	candidateID foundation.ID,
	candidateHash string,
	evidenceRefs []string,
) ([]ValidateCitationV3ReceiptResult, error) {
	if !canonicalResultReceiptID(candidateID) || !lowerHex(candidateHash, 64) || len(evidenceRefs) < 1 || len(evidenceRefs) > 3 {
		return nil, invalid(ErrorCodeResultReceiptInvalid, "citation receipt candidate binding is invalid")
	}
	seen := make(map[string]struct{}, len(evidenceRefs))
	for _, reference := range evidenceRefs {
		if !validResultReceiptEvidenceRef(reference, 3) {
			return nil, invalid(ErrorCodeResultReceiptInvalid, "citation receipt evidence references are invalid")
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, invalid(ErrorCodeResultReceiptInvalid, "citation receipt evidence references are duplicated")
		}
		seen[reference] = struct{}{}
	}

	output, binding, err := decodeValidateCitationV3ReceiptAuthority(receipt)
	if err != nil {
		return nil, err
	}
	if binding.CandidateID != candidateID || binding.CandidateHash != candidateHash || len(output.Results) != len(evidenceRefs) {
		return nil, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"citation receipt does not bind the authoritative candidate",
		)
	}
	results := make([]ValidateCitationV3ReceiptResult, len(output.Results))
	for index, result := range output.Results {
		if result.EvidenceRef != evidenceRefs[index] {
			return nil, inconsistent(
				ErrorCodeResultReceiptBindingConflict,
				"citation receipt result order differs from the authoritative candidate",
			)
		}
		results[index] = ValidateCitationV3ReceiptResult{
			EvidenceRef: result.EvidenceRef,
			Valid:       *result.Valid,
			ReasonCode:  result.ReasonCode,
		}
	}
	return results, nil
}

// ValidateCitationV3ReceiptCitations 在完整候选、顺序与 output/private-binding
// 闭包校验后返回有序 Citation identity。调用方只能用它构造受控发布或模型输入，
// 不得直接投影到 HTTP、Workflow output 或日志。
func ValidateCitationV3ReceiptCitations(
	receipt ResultReceipt,
	candidateID foundation.ID,
	candidateHash string,
	evidenceRefs []string,
) ([]ValidateCitationV3ReceiptCitation, error) {
	if _, err := ValidateCitationV3ReceiptResults(receipt, candidateID, candidateHash, evidenceRefs); err != nil {
		return nil, err
	}
	_, binding, err := decodeValidateCitationV3ReceiptAuthority(receipt)
	if err != nil {
		return nil, err
	}
	citations := make([]ValidateCitationV3ReceiptCitation, len(binding.Results))
	for index, identity := range binding.Results {
		citations[index] = ValidateCitationV3ReceiptCitation{
			EvidenceRef: identity.EvidenceRef, CitationID: identity.CitationID,
			IndexVersionID: identity.IndexVersionID, ChunkID: identity.ChunkID,
			SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID,
		}
	}
	return citations, nil
}

func decodeSearchKnowledgeV2ReceiptAuthority(
	receipt ResultReceipt,
) (searchKnowledgeV2ReceiptOutput, searchKnowledgeV2PrivateBinding, error) {
	contract, found := WorkspaceAnalysisResultReceiptContract(ToolRef{Name: "SearchKnowledge", Version: 2})
	if !found || receipt.Tool != contract.Tool || receipt.OutputSchema != contract.OutputSchema ||
		receipt.PersistencePolicy != ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || receipt.PrivateBinding == nil ||
		validateResultReceiptIDs(receipt) != nil {
		return searchKnowledgeV2ReceiptOutput{}, searchKnowledgeV2PrivateBinding{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "search receipt authority binding is invalid")
	}
	output, outputErr := decodeExactReceiptDocument(receipt.Output, contract.MaxOutputBytes, validateSearchKnowledgeV2ReceiptOutput)
	if outputErr != nil || int64(len(receipt.Output)) != receipt.OutputBytes || receipt.OutputHash != resultReceiptHash(receipt.Output) ||
		validateResultReceiptPrivateBinding(receipt.PrivateBinding, contract) != nil {
		return searchKnowledgeV2ReceiptOutput{}, searchKnowledgeV2PrivateBinding{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "search receipt document is invalid")
	}
	binding, bindingErr := decodeExactReceiptDocument(
		receipt.PrivateBinding.Document,
		contract.MaxPrivateBindingBytes,
		validateSearchKnowledgeV2PrivateBinding,
	)
	if bindingErr != nil || !searchKnowledgeV2ReceiptBindingMatches(output, binding) {
		return searchKnowledgeV2ReceiptOutput{}, searchKnowledgeV2PrivateBinding{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "search receipt private binding is invalid")
	}
	return output, binding, nil
}

func decodeReadSourceV3ReceiptAuthority(
	receipt ResultReceipt,
) (readSourceV3ReceiptOutput, readSourceV3PrivateBinding, error) {
	contract, found := WorkspaceAnalysisResultReceiptContract(ToolRef{Name: "ReadSource", Version: 3})
	if !found || receipt.Tool != contract.Tool || receipt.OutputSchema != contract.OutputSchema ||
		receipt.PersistencePolicy != ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || receipt.PrivateBinding == nil ||
		validateResultReceiptIDs(receipt) != nil {
		return readSourceV3ReceiptOutput{}, readSourceV3PrivateBinding{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"source receipt authority binding is invalid",
		)
	}
	output, outputErr := decodeExactReceiptDocument(
		receipt.Output,
		contract.MaxOutputBytes,
		validateReadSourceV3ReceiptOutput,
	)
	if outputErr != nil || int64(len(receipt.Output)) != receipt.OutputBytes ||
		receipt.OutputHash != resultReceiptHash(receipt.Output) ||
		validateResultReceiptPrivateBinding(receipt.PrivateBinding, contract) != nil {
		return readSourceV3ReceiptOutput{}, readSourceV3PrivateBinding{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"source receipt document is invalid",
		)
	}
	binding, bindingErr := decodeExactReceiptDocument(
		receipt.PrivateBinding.Document,
		contract.MaxPrivateBindingBytes,
		validateReadSourceV3PrivateBinding,
	)
	if bindingErr != nil || output.EvidenceRef != binding.EvidenceRef || output.ContentHash != binding.ContentHash {
		return readSourceV3ReceiptOutput{}, readSourceV3PrivateBinding{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"source receipt private binding is invalid",
		)
	}
	return output, binding, nil
}

func decodeValidateCitationV3ReceiptAuthority(
	receipt ResultReceipt,
) (validateCitationV3ReceiptOutput, validateCitationV3PrivateBinding, error) {
	contract, found := WorkspaceAnalysisResultReceiptContract(ToolRef{Name: "ValidateCitation", Version: 3})
	if !found || receipt.Tool != contract.Tool || receipt.OutputSchema != contract.OutputSchema ||
		receipt.PersistencePolicy != ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || receipt.PrivateBinding == nil ||
		validateResultReceiptIDs(receipt) != nil || !lowerHex(receipt.DefinitionHash, 64) ||
		receipt.CreatedAt.IsZero() || !receipt.CreatedAt.Equal(receipt.CreatedAt.UTC().Truncate(time.Microsecond)) {
		return validateCitationV3ReceiptOutput{}, validateCitationV3PrivateBinding{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"citation receipt authority binding is invalid",
		)
	}
	canonicalOutput, canonicalErr := canonicalJSONObject(receipt.Output, int(contract.MaxOutputBytes))
	output, outputErr := decodeExactReceiptDocument(
		receipt.Output,
		contract.MaxOutputBytes,
		validateValidateCitationV3ReceiptOutput,
	)
	if canonicalErr != nil || !bytes.Equal(canonicalOutput, receipt.Output) || outputErr != nil ||
		receipt.OutputBytes != int64(len(receipt.Output)) || receipt.OutputBytes < 1 ||
		receipt.OutputHash != resultReceiptHash(receipt.Output) ||
		validateResultReceiptPrivateBinding(receipt.PrivateBinding, contract) != nil {
		return validateCitationV3ReceiptOutput{}, validateCitationV3PrivateBinding{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"citation receipt document is invalid",
		)
	}
	binding, bindingErr := decodeExactReceiptDocument(
		receipt.PrivateBinding.Document,
		contract.MaxPrivateBindingBytes,
		validateValidateCitationV3PrivateBinding,
	)
	if bindingErr != nil || !validateCitationV3ReceiptBindingMatches(output, binding) {
		return validateCitationV3ReceiptOutput{}, validateCitationV3PrivateBinding{}, inconsistent(
			ErrorCodeResultReceiptBindingConflict,
			"citation receipt private binding is invalid",
		)
	}
	return output, binding, nil
}

func searchKnowledgeV2ReceiptBindingMatches(output searchKnowledgeV2ReceiptOutput, binding searchKnowledgeV2PrivateBinding) bool {
	if len(output.Items) != len(binding.Items) {
		return false
	}
	for index := range output.Items {
		if output.Items[index].EvidenceRef != binding.Items[index].EvidenceRef {
			return false
		}
	}
	return true
}

func validateReadSourceV3ReceiptOutput(output readSourceV3ReceiptOutput) error {
	if !validResultReceiptEvidenceRef(output.EvidenceRef, 3) || !lowerHex(output.ContentHash, 64) || output.Truncated == nil ||
		!validResultReceiptText(output.Excerpt, 1, 4*1024) {
		return errors.New("source receipt output is invalid")
	}
	return nil
}

func validateReadSourceV3PrivateBinding(binding readSourceV3PrivateBinding) error {
	if !canonicalResultReceiptID(binding.SearchReceiptID) || !lowerHex(binding.SearchReceiptHash, 64) ||
		!validResultReceiptCitationIdentity(binding.resultReceiptCitationIdentity, 3) {
		return errors.New("source receipt private binding is invalid")
	}
	return nil
}

func validateValidateCitationV3ReceiptOutput(output validateCitationV3ReceiptOutput) error {
	if len(output.Results) < 1 || len(output.Results) > 3 {
		return errors.New("citation receipt output is invalid")
	}
	seen := make(map[string]struct{}, len(output.Results))
	for _, result := range output.Results {
		if !validResultReceiptEvidenceRef(result.EvidenceRef, 3) || result.Valid == nil || !validResultReceiptCitationReason(result.ReasonCode) ||
			(*result.Valid != (result.ReasonCode == "OK")) {
			return errors.New("citation receipt result is invalid")
		}
		if _, duplicate := seen[result.EvidenceRef]; duplicate {
			return errors.New("citation receipt result is duplicated")
		}
		seen[result.EvidenceRef] = struct{}{}
	}
	return nil
}

func validateValidateCitationV3PrivateBinding(binding validateCitationV3PrivateBinding) error {
	if !canonicalResultReceiptID(binding.CandidateID) || !lowerHex(binding.CandidateHash, 64) ||
		len(binding.Results) < 1 || len(binding.Results) > 3 {
		return errors.New("citation receipt private binding is invalid")
	}
	seen := make(map[string]struct{}, len(binding.Results))
	citationIDs := make(map[string]struct{}, len(binding.Results))
	tuples := make(map[string]struct{}, len(binding.Results))
	var indexVersionID foundation.ID
	for index, result := range binding.Results {
		if !validResultReceiptCitationIdentity(result, 3) {
			return errors.New("citation receipt private result is invalid")
		}
		if index == 0 {
			indexVersionID = result.IndexVersionID
		} else if result.IndexVersionID != indexVersionID {
			return errors.New("citation receipt private results span multiple index versions")
		}
		if _, duplicate := seen[result.EvidenceRef]; duplicate {
			return errors.New("citation receipt private result is duplicated")
		}
		tupleKey := strings.Join([]string{
			string(result.IndexVersionID), string(result.ChunkID), string(result.SourceVersionID), string(result.SourceSpanID),
		}, "\x00")
		if _, duplicate := citationIDs[result.CitationID]; duplicate {
			return errors.New("citation receipt private citation is duplicated")
		}
		if _, duplicate := tuples[tupleKey]; duplicate {
			return errors.New("citation receipt private identity is duplicated")
		}
		seen[result.EvidenceRef] = struct{}{}
		citationIDs[result.CitationID] = struct{}{}
		tuples[tupleKey] = struct{}{}
	}
	return nil
}

func validateCitationV3ReceiptBindingMatches(output validateCitationV3ReceiptOutput, binding validateCitationV3PrivateBinding) bool {
	if len(output.Results) != len(binding.Results) {
		return false
	}
	for index := range output.Results {
		if output.Results[index].EvidenceRef != binding.Results[index].EvidenceRef {
			return false
		}
	}
	return true
}

func validResultReceiptCitationIdentity(identity resultReceiptCitationIdentity, maximumRef int) bool {
	ids := []foundation.ID{identity.IndexVersionID, identity.ChunkID, identity.SourceVersionID, identity.SourceSpanID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalResultReceiptID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return validResultReceiptEvidenceRef(identity.EvidenceRef, maximumRef) && validResultReceiptCitationID(identity.CitationID) &&
		lowerHex(identity.ContentHash, 64)
}

func validResultReceiptCitationID(value string) bool {
	return len(value) == len("cite-")+64 && strings.HasPrefix(value, "cite-") && lowerHex(value[len("cite-"):], 64)
}

func validResultReceiptCitationReason(value string) bool {
	switch value {
	case "OK", "CITATION_UNRESOLVABLE", "EVIDENCE_INELIGIBLE", "BINDING_MISMATCH":
		return true
	default:
		return false
	}
}

func validResultReceiptTokens(values []string, maxBytes int) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) > maxBytes || !stableCodePattern.MatchString(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validResultReceiptText(value string, minimumBytes, maximumBytes int) bool {
	return len(value) >= minimumBytes && len(value) <= maximumBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && (minimumBytes == 0 || strings.TrimSpace(value) != "")
}

func validResultReceiptEvidenceRef(value string, maximum int) bool {
	return len(value) == 2 && value[0] == 'E' && value[1] >= '1' && value[1] <= byte('0'+maximum)
}

func resultReceiptEvidenceRef(ordinal int) string {
	return string([]byte{'E', byte('0' + ordinal)})
}

func canonicalResultReceiptID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func safePrivateBinding(document json.RawMessage) bool {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return false
	}
	return !privateBindingContainsUnsafeValue("", value)
}

func privateBindingContainsUnsafeValue(key string, value any) bool {
	if privateBindingKeyForbidden(key) {
		return true
	}
	switch typed := value.(type) {
	case string:
		return foundationredaction.ContainsSecret(typed) || foundationredaction.ContainsPII(typed) || foundationredaction.ContainsAbsolutePath(typed)
	case map[string]any:
		for childKey, child := range typed {
			if privateBindingContainsUnsafeValue(childKey, child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if privateBindingContainsUnsafeValue(key, child) {
				return true
			}
		}
	}
	return false
}

func privateBindingKeyForbidden(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.TrimSpace(key)))
	if normalized == "" {
		return false
	}
	if strings.HasSuffix(normalized, "hash") || strings.HasSuffix(normalized, "bytes") {
		return false
	}
	for _, marker := range []string{
		"authorization", "cookie", "credential", "password", "passwd", "secret", "token", "csrf", "session",
		"dsn", "databaseurl", "connectionstring", "stderr", "stdout", "prompt", "query", "snippet", "excerpt",
		"body", "text", "content", "path", "url", "argv", "command", "diff",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func resultReceiptHash(document json.RawMessage) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}
