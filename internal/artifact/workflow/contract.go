// Package workflow 定义 Artifact 章节生成与通用持久 Workflow、Agent 运行时之间的受控边界。
package workflow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// DefinitionKey 是 Artifact 章节生成唯一允许启动的内置 Definition。
	DefinitionKey = "artifact-section-generation"
	// DefinitionVersion 是 Artifact 章节生成 Definition 的首个冻结版本。
	DefinitionVersion int64 = 1
	// NodeKey 是单节点 Definition 的稳定逻辑键。
	NodeKey = "artifact.generate-section"
	// NodeKind 是 Worker Executor Registry 使用的稳定节点类型。
	NodeKind = "artifact.generate-section"
	// InputSchemaVersion 是只含 Artifact 冻结身份的持久输入版本。
	InputSchemaVersion = 1
	// OutputSchemaVersion 是不含正文或 Provider 数据的持久回执版本。
	OutputSchemaVersion = 1

	// ErrorCodeCapabilityUnavailable 表示章节生成所需的模型或受控依赖未启用。
	ErrorCodeCapabilityUnavailable = "ARTIFACT_WORKFLOW_CAPABILITY_UNAVAILABLE"
	// ErrorCodeInputInvalid 表示持久输入或执行身份未通过严格校验。
	ErrorCodeInputInvalid = "ARTIFACT_WORKFLOW_INPUT_INVALID"
	// ErrorCodeOutputInvalid 表示模型输出、终态回执或其服务端绑定不一致。
	ErrorCodeOutputInvalid = "ARTIFACT_WORKFLOW_OUTPUT_INVALID"
	// ErrorCodeContextInvalid 表示重新加载的 Artifact 事实不再匹配冻结输入。
	ErrorCodeContextInvalid = "ARTIFACT_WORKFLOW_CONTEXT_INVALID"
	// ErrorCodeEvidenceInvalid 表示检索、打开或正式资格投影不完整。
	ErrorCodeEvidenceInvalid = "ARTIFACT_WORKFLOW_EVIDENCE_INVALID"
	// ErrorCodeRunReplayUnsafe 表示 Model Run 已存在但没有可安全重放的终态回执。
	ErrorCodeRunReplayUnsafe = "ARTIFACT_WORKFLOW_MODEL_RUN_REPLAY_UNSAFE"
	// ErrorCodeFinalizationUnknown 表示 Artifact Revision 与 Model Run 的原子提交结果无法确认。
	ErrorCodeFinalizationUnknown = "ARTIFACT_WORKFLOW_FINALIZATION_UNKNOWN"

	maxContractBytes    = 2 * 1024
	maxSectionKeyBytes  = 128
	definitionGraphHash = "801d15d17955b5660f5054fa22946b5465f7b925622cf6a73c105ebd2f3b3aee"
)

// Input 是章节生成的最小持久输入；Workspace、标题、正文、证据和运行版本必须由执行端重新加载。
type Input struct {
	SchemaVersion   int           `json:"schema_version"`
	ArtifactID      foundation.ID `json:"artifact_id"`
	RevisionID      foundation.ID `json:"revision_id"`
	RevisionNo      int64         `json:"revision_no"`
	ArtifactVersion int64         `json:"artifact_version"`
	SectionKey      string        `json:"section_key"`
}

type persistedInput struct {
	SchemaVersion   *int           `json:"schema_version"`
	ArtifactID      *foundation.ID `json:"artifact_id"`
	RevisionID      *foundation.ID `json:"revision_id"`
	RevisionNo      *int64         `json:"revision_no"`
	ArtifactVersion *int64         `json:"artifact_version"`
	SectionKey      *string        `json:"section_key"`
}

// OutputReceipt 是节点唯一允许持久化的终态摘要；不包含章节正文、摘录、Prompt 或 Provider 数据。
type OutputReceipt struct {
	SchemaVersion   int           `json:"schema_version"`
	ArtifactID      foundation.ID `json:"artifact_id"`
	BaseRevisionID  foundation.ID `json:"base_revision_id"`
	RevisionID      foundation.ID `json:"revision_id"`
	RevisionNo      int64         `json:"revision_no"`
	ArtifactVersion int64         `json:"artifact_version"`
	SectionKey      string        `json:"section_key"`
	ModelRunID      foundation.ID `json:"model_run_id"`
	ContentHash     string        `json:"content_hash"`
}

type persistedOutputReceipt struct {
	SchemaVersion   *int           `json:"schema_version"`
	ArtifactID      *foundation.ID `json:"artifact_id"`
	BaseRevisionID  *foundation.ID `json:"base_revision_id"`
	RevisionID      *foundation.ID `json:"revision_id"`
	RevisionNo      *int64         `json:"revision_no"`
	ArtifactVersion *int64         `json:"artifact_version"`
	SectionKey      *string        `json:"section_key"`
	ModelRunID      *foundation.ID `json:"model_run_id"`
	ContentHash     *string        `json:"content_hash"`
}

// GenerationContextQuery 绑定执行节点与必须重新加载的 Artifact 冻结身份。
type GenerationContextQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	Input         Input
}

// GenerationContext 是执行端从 Artifact 唯一事实源恢复的当前状态、冻结来源 Revision 和服务端模型 Profile。
// SourceRevision 保持模型输入可重放；Current 允许在 Provider 调用前拒绝大纲或目标章节漂移。
type GenerationContext struct {
	Current        artifactapplication.State
	SourceRevision artifactdomain.Revision
	ProfileRef     agentdomain.ModelProfileRef
}

// ContextLoader 是 Worker 重新读取 Artifact 当前状态的窄端口。
type ContextLoader interface {
	// LoadGenerationContext 必须验证 Workflow 与 Artifact 绑定并返回当前不可变 Revision。
	LoadGenerationContext(context.Context, GenerationContextQuery) (GenerationContext, error)
}

// FinalizationLookup 是 Provider 调用前用于恢复既有原子终态的完整身份。
type FinalizationLookup struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
	Input         Input
}

// SectionProposal 是 Executor 从服务端上下文、严格模型输出和 Citation label 映射构造的候选章节。
// Finalizer 必须重新复核 Citation tuple、Outline title 与 Metadata，再原子创建 Revision 并终结 Model Run。
type SectionProposal struct {
	SectionKey string
	Title      string
	Content    string
	Citations  []agentdomain.Citation
	Coverage   artifactdomain.Coverage
	Metadata   artifactdomain.GenerationMetadata
}

// FinalizeSectionCommand 请求在一个事务中写入不可变 Artifact Revision 并终结 Model Run。
type FinalizeSectionCommand struct {
	FinalizationLookup
	ModelRunID              foundation.ID
	ExpectedModelRunVersion int64
	Proposal                SectionProposal
}

// Finalizer 是 Workflow 访问 Artifact/ModelRun 原子终态的唯一端口。
type Finalizer interface {
	// Lookup 在任何 Provider 调用前恢复并验证既有终态；未完成时返回 found=false。
	Lookup(context.Context, FinalizationLookup) (OutputReceipt, bool, error)
	// Finalize 锁定当前 Artifact，复核冻结大纲与目标章节未漂移，把 Proposal rebase 到当前 Revision，
	// 再原子创建下一 Revision、完成 SectionGeneration 并终结 Model Run；bool 表示精确重放。
	Finalize(context.Context, FinalizeSectionCommand) (OutputReceipt, bool, error)
}

// RegisteredDefinition 返回无 Tool、只读本地证据并写 Artifact 候选区的单节点 Definition。
func RegisteredDefinition() workflowdomain.RegisteredDefinition {
	return workflowdomain.RegisteredDefinition{
		Key: DefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: NodeKey, Kind: NodeKind, InputSchemaVersion: InputSchemaVersion, OutputSchemaVersion: OutputSchemaVersion,
			RetryPolicy: workflowdomain.RetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 10 * time.Second},
			RequiredPermissions: []workflowdomain.Permission{
				workflowdomain.PermissionReadLocal,
				workflowdomain.PermissionWriteProposal,
			},
		}}},
		GraphHash: definitionGraphHash,
	}
}

// EncodeInput 校验并编码恰好六个字段的 canonical Workflow Input。
func EncodeInput(input Input) (json.RawMessage, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, inputError(err)
	}
	return encoded, nil
}

// DecodeInput 严格拒绝 unknown、duplicate、trailing、null 与非法冻结身份。
func DecodeInput(raw json.RawMessage) (Input, error) {
	limits := contractDecodeLimits(6)
	persisted, err := foundationstrictjson.DecodeObject[persistedInput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.ArtifactID == nil || persisted.RevisionID == nil ||
		persisted.RevisionNo == nil || persisted.ArtifactVersion == nil || persisted.SectionKey == nil {
		return Input{}, inputError(err)
	}
	input := Input{
		SchemaVersion: *persisted.SchemaVersion, ArtifactID: *persisted.ArtifactID,
		RevisionID: *persisted.RevisionID, RevisionNo: *persisted.RevisionNo,
		ArtifactVersion: *persisted.ArtifactVersion, SectionKey: *persisted.SectionKey,
	}
	if err := validateInput(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// EncodeOutputReceipt 校验并编码只含身份、内容 hash 与 Model Run ID 的终态回执。
func EncodeOutputReceipt(receipt OutputReceipt) (json.RawMessage, error) {
	if err := validateOutputReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, outputError(err)
	}
	return encoded, nil
}

// DecodeOutputReceipt 严格拒绝正文、摘录、Provider 字段及不完整终态身份。
func DecodeOutputReceipt(raw json.RawMessage) (OutputReceipt, error) {
	limits := contractDecodeLimits(9)
	persisted, err := foundationstrictjson.DecodeObject[persistedOutputReceipt](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.ArtifactID == nil || persisted.BaseRevisionID == nil ||
		persisted.RevisionID == nil || persisted.RevisionNo == nil || persisted.ArtifactVersion == nil ||
		persisted.SectionKey == nil || persisted.ModelRunID == nil || persisted.ContentHash == nil {
		return OutputReceipt{}, outputError(err)
	}
	receipt := OutputReceipt{
		SchemaVersion: *persisted.SchemaVersion, ArtifactID: *persisted.ArtifactID,
		BaseRevisionID: *persisted.BaseRevisionID, RevisionID: *persisted.RevisionID,
		RevisionNo: *persisted.RevisionNo, ArtifactVersion: *persisted.ArtifactVersion,
		SectionKey: *persisted.SectionKey, ModelRunID: *persisted.ModelRunID, ContentHash: *persisted.ContentHash,
	}
	if err := validateOutputReceipt(receipt); err != nil {
		return OutputReceipt{}, err
	}
	return receipt, nil
}

func validateInput(input Input) error {
	if input.SchemaVersion != InputSchemaVersion || input.RevisionNo < 1 || input.ArtifactVersion < 1 ||
		!validSectionKey(input.SectionKey) || !validDistinctIDs(input.ArtifactID, input.RevisionID) {
		return inputError(errors.New("artifact generation workflow input is invalid"))
	}
	return nil
}

func validateOutputReceipt(receipt OutputReceipt) error {
	if receipt.SchemaVersion != OutputSchemaVersion || receipt.RevisionNo < 2 || receipt.ArtifactVersion < 2 ||
		!validSectionKey(receipt.SectionKey) || !validHash(receipt.ContentHash) ||
		!validDistinctIDs(receipt.ArtifactID, receipt.BaseRevisionID, receipt.RevisionID, receipt.ModelRunID) {
		return outputError(errors.New("artifact generation workflow receipt is invalid"))
	}
	return nil
}

// ValidateOutputReceiptBinding 校验终态仍属于冻结来源，但允许其他章节先创建中间 Revision。
// BaseRevisionID 是 Finalizer 实际锁定并写入的基线；Finalizer 还必须保证终态版本恰为该基线的下一版本。
func ValidateOutputReceiptBinding(receipt OutputReceipt, input Input) error {
	if err := validateOutputReceipt(receipt); err != nil {
		return err
	}
	if receipt.ArtifactID != input.ArtifactID || receipt.RevisionNo <= input.RevisionNo ||
		receipt.ArtifactVersion <= input.ArtifactVersion ||
		receipt.SectionKey != input.SectionKey {
		return outputError(errors.New("artifact generation receipt differs from frozen input"))
	}
	return nil
}

func contractDecodeLimits(fields int) foundationstrictjson.Limits {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxContractBytes
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = fields
	return limits
}

func validDistinctIDs(values ...foundation.ID) bool {
	seen := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		if !validID(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validSectionKey(value string) bool {
	if value == "" || len(value) > maxSectionKeyBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func inputError(cause error) error {
	if cause == nil {
		cause = errors.New("artifact generation workflow input is incomplete")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, cause)
}

func outputError(cause error) error {
	if cause == nil {
		cause = errors.New("artifact generation workflow output is incomplete")
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, cause)
}

func workflowError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
