package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	// OutlinePromptID 是冻结材料大纲生成的服务端 Prompt 身份。
	OutlinePromptID = "organizing.outline-generation"
	// DocumentPromptID 是冻结材料文档生成的服务端 Prompt 身份。
	DocumentPromptID = "organizing.document-generation"
	// OutlineSchemaID 是大纲严格输出 Schema 身份。
	OutlineSchemaID = "organizing.outline-generation"
	// OutlineReducedSchemaID 是只允许显式 GAP 的大纲 Schema 身份。
	OutlineReducedSchemaID = "organizing.outline-generation.reduced"
	// DocumentSchemaID 是文档严格输出 Schema 身份。
	DocumentSchemaID = "organizing.document-generation"
	// DocumentReducedSchemaID 是只允许显式 GAP 的文档 Schema 身份。
	DocumentReducedSchemaID = "organizing.document-generation.reduced"
	// GenerationRuntimeVersion 冻结 Organizing Prompt 与 Schema 的首版契约。
	GenerationRuntimeVersion = agentdomain.OutputSchemaVersionV1

	maxGeneratedSectionBytes = 64 * 1024
	maxGeneratedGapBytes     = 4 * 1024
	maxGeneratedGaps         = 50
	maxGeneratedComparisons  = 64
	maxGenerationOutputBytes = 512 * 1024
)

// GenerationKind 区分可审大纲与最终文档结构化输出。
type GenerationKind string

const (
	// GenerationOutline 生成专题文章大纲。
	GenerationOutline GenerationKind = "OUTLINE"
	// GenerationDocument 生成完整分章文档。
	GenerationDocument GenerationKind = "DOCUMENT"
)

// GenerationStatus 是一次节点级模型输出的恢复状态。
type GenerationStatus string

const (
	// GenerationRunning 表示模型输出尚未形成可重放终态。
	GenerationRunning GenerationStatus = "RUNNING"
	// GenerationReady 表示严格输出与 Model Run 已原子完成。
	GenerationReady GenerationStatus = "READY"
	// GenerationFailed 表示本次 Attempt 已安全失败。
	GenerationFailed GenerationStatus = "FAILED"
	// GenerationRecoveryRequired 表示 Provider 或持久化结果不确定，禁止自动重调。
	GenerationRecoveryRequired GenerationStatus = "RECOVERY_REQUIRED"
)

// GenerationRecord 是节点 Attempt 与可重放结构化输出的持久绑定。
type GenerationRecord struct {
	ID                    foundation.ID
	WorkspaceID           foundation.ID
	SnapshotID            foundation.ID
	WorkflowRunID         foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	Kind                  GenerationKind
	RequestHash           string
	ModelSettingsRevision *int64
	ModelRunID            foundation.ID
	Status                GenerationStatus
	Output                json.RawMessage
	OutputHash            string
	ErrorCode             string
	Retryable             bool
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CompletedAt           *time.Time
}

// Validate 校验 Generation 的身份、生命周期与恢复载荷。
func (record GenerationRecord) Validate() error {
	if !validID(record.ID) || !validID(record.WorkspaceID) || !validID(record.SnapshotID) ||
		!validID(record.WorkflowRunID) || !validID(record.NodeRunID) || !validID(record.NodeAttemptID) ||
		record.Kind != GenerationOutline && record.Kind != GenerationDocument || !validHash(record.RequestHash) ||
		record.Version < 1 || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) ||
		(record.ModelSettingsRevision != nil && *record.ModelSettingsRevision < 0) {
		return workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_INVALID", false, "organizing generation binding is invalid")
	}
	switch record.Status {
	case GenerationRunning:
		if record.CompletedAt != nil || len(record.Output) != 0 || record.OutputHash != "" || record.ErrorCode != "" || record.Retryable {
			return workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_INVALID", false, "running organizing generation has terminal facts")
		}
	case GenerationReady:
		if !validID(record.ModelRunID) || record.CompletedAt == nil || !record.CompletedAt.Equal(record.UpdatedAt) ||
			len(record.Output) == 0 || len(record.Output) > maxGenerationOutputBytes || !utf8.Valid(record.Output) ||
			!validHash(record.OutputHash) || hashBytes(record.Output) != record.OutputHash || record.ErrorCode != "" || record.Retryable {
			return workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_INVALID", false, "ready organizing generation is incomplete")
		}
	case GenerationFailed, GenerationRecoveryRequired:
		if record.CompletedAt == nil || !record.CompletedAt.Equal(record.UpdatedAt) || len(record.Output) != 0 ||
			record.OutputHash != "" || !validGenerationErrorCode(record.ErrorCode) ||
			(record.Status == GenerationRecoveryRequired && record.Retryable) {
			return workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_INVALID", false, "failed organizing generation is incomplete")
		}
	default:
		return workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_INVALID", false, "organizing generation status is invalid")
	}
	return nil
}

// PrepareGenerationCommand creates one node-attempt recovery fence.
type PrepareGenerationCommand struct {
	Record GenerationRecord
}

// BindGenerationModelRunCommand binds the only Model Run allowed for an Attempt.
type BindGenerationModelRunCommand struct {
	WorkspaceID     foundation.ID
	GenerationID    foundation.ID
	ExpectedVersion int64
	ModelRunID      foundation.ID
}

// CompleteGenerationCommand atomically stores strict output and succeeds its Model Run.
type CompleteGenerationCommand struct {
	WorkspaceID             foundation.ID
	GenerationID            foundation.ID
	ExpectedVersion         int64
	ModelRunID              foundation.ID
	ExpectedModelRunVersion int64
	ResultType              string
	Output                  json.RawMessage
	OutputHash              string
	CompletedAt             time.Time
}

// FailGenerationCommand atomically fails a Generation and any bound Model Run.
type FailGenerationCommand struct {
	WorkspaceID             foundation.ID
	GenerationID            foundation.ID
	ExpectedVersion         int64
	ModelRunID              foundation.ID
	ExpectedModelRunVersion int64
	ModelRunStatus          agentdomain.ModelRunStatus
	ErrorCode               string
	Retryable               bool
	CompletedAt             time.Time
}

// GenerationStore owns node-attempt recovery and atomic Agent terminal output.
type GenerationStore interface {
	LookupReady(context.Context, foundation.ID, foundation.ID, GenerationKind, string) (GenerationRecord, bool, error)
	Prepare(context.Context, PrepareGenerationCommand) (GenerationRecord, bool, error)
	BindModelRun(context.Context, BindGenerationModelRunCommand) (GenerationRecord, error)
	Complete(context.Context, CompleteGenerationCommand) (GenerationRecord, bool, error)
	Fail(context.Context, FailGenerationCommand) error
}

// GeneratedDocument 是 Artifact owner 可消费的完整、证据绑定文档。
type GeneratedDocument struct {
	Sections   []artifactapp.SectionInput
	Comparison mergeComparison
	Metadata   artifactdomain.GenerationMetadata
}

// ContentGenerator 只基于已冻结 Snapshot 生成可审大纲或完整文档。
type ContentGenerator interface {
	GenerateOutline(context.Context, workflowapp.ExecutionContext, organizingdomain.Snapshot, organizingdomain.TemplateRevision) (outlineReceipt, error)
	GenerateDocument(context.Context, workflowapp.ExecutionContext, organizingdomain.Snapshot, organizingdomain.TemplateRevision, []artifactdomain.OutlineSection) (GeneratedDocument, error)
}

func outlinePromptRef() agentdomain.PromptRef {
	return agentdomain.PromptRef{ID: OutlinePromptID, Version: GenerationRuntimeVersion}
}

func documentPromptRef() agentdomain.PromptRef {
	return agentdomain.PromptRef{ID: DocumentPromptID, Version: GenerationRuntimeVersion}
}

func outlineSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: OutlineSchemaID, Version: GenerationRuntimeVersion}
}

func outlineReducedSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: OutlineReducedSchemaID, Version: GenerationRuntimeVersion}
}

func documentSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: DocumentSchemaID, Version: GenerationRuntimeVersion}
}

func documentReducedSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: DocumentReducedSchemaID, Version: GenerationRuntimeVersion}
}

func generationRequestHash(kind GenerationKind, execution workflowapp.ExecutionContext, snapshot organizingdomain.Snapshot, revision organizingdomain.TemplateRevision, outline []artifactdomain.OutlineSection) (string, error) {
	payload := struct {
		Kind                  GenerationKind                  `json:"kind"`
		WorkspaceID           foundation.ID                   `json:"workspace_id"`
		WorkflowRunID         foundation.ID                   `json:"workflow_run_id"`
		NodeRunID             foundation.ID                   `json:"node_run_id"`
		ModelSettingsRevision *int64                          `json:"model_settings_revision"`
		SnapshotID            foundation.ID                   `json:"snapshot_id"`
		SnapshotHash          string                          `json:"snapshot_hash"`
		TemplateID            foundation.ID                   `json:"template_id"`
		TemplateHash          string                          `json:"template_hash"`
		Outline               []artifactdomain.OutlineSection `json:"outline"`
	}{kind, execution.WorkspaceID, execution.RunID, execution.NodeRunID, execution.ModelSettingsRevision, snapshot.ID, snapshot.Hash, revision.ID, revision.DeclarationHash, outline}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validGenerationErrorCode(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func generationResultType(kind GenerationKind) (string, error) {
	switch kind {
	case GenerationOutline:
		return agentdomain.ResultTypeOrganizingOutline, nil
	case GenerationDocument:
		return agentdomain.ResultTypeOrganizingDocument, nil
	default:
		return "", errors.New("organizing generation kind is invalid")
	}
}
