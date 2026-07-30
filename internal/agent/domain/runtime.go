package domain

import (
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxStableErrorCodeBytes = 128
	// MaxModelCallOutputTokens 是单次模型调用可持久化的最大输出 Token 上限。
	MaxModelCallOutputTokens = 128 * 1024
)

// ModelRunStatus 是一次 Agent pipeline 的持久状态。
type ModelRunStatus string

const (
	ModelRunRunning   ModelRunStatus = "RUNNING"
	ModelRunSucceeded ModelRunStatus = "SUCCEEDED"
	ModelRunRefused   ModelRunStatus = "REFUSED"
	ModelRunFailed    ModelRunStatus = "FAILED"
	ModelRunUnknown   ModelRunStatus = "UNKNOWN"
)

// ModelCallPhase 是调用预算内受控的模型调用阶段。
type ModelCallPhase string

const (
	// ModelCallPlan 表示生成检索改写或澄清判断的计划调用。
	ModelCallPlan    ModelCallPhase = "PLAN"
	ModelCallInitial ModelCallPhase = "INITIAL"
	ModelCallRepair  ModelCallPhase = "REPAIR"
	ModelCallReduced ModelCallPhase = "REDUCED"
	ModelCallReview  ModelCallPhase = "REVIEW"
)

// ModelCallStatus 是一次 Provider 调用的持久状态。
type ModelCallStatus string

const (
	ModelCallStarted   ModelCallStatus = "STARTED"
	ModelCallSucceeded ModelCallStatus = "SUCCEEDED"
	ModelCallFailed    ModelCallStatus = "FAILED"
	ModelCallUnknown   ModelCallStatus = "UNKNOWN"
)

// RetrievalRef 固化模型输入使用的 Retrieval 与可选 Embedding/Rerank 版本。
type RetrievalRef struct {
	IndexVersionID     foundation.ID  `json:"index_version_id"`
	EmbeddingVersionID *foundation.ID `json:"embedding_version_id,omitempty"`
	RerankModelVersion string         `json:"rerank_model_version,omitempty"`
}

// Validate 校验 Retrieval 版本引用完整且 canonical。
func (ref RetrievalRef) Validate() error {
	if !canonicalID(ref.IndexVersionID) {
		return invalid(ErrorCodeReferenceInvalid, "retrieval index version is invalid")
	}
	if ref.EmbeddingVersionID != nil && (!canonicalID(*ref.EmbeddingVersionID) || *ref.EmbeddingVersionID == ref.IndexVersionID) {
		return invalid(ErrorCodeReferenceInvalid, "embedding version is invalid")
	}
	if ref.RerankModelVersion != "" && !canonicalReference(ref.RerankModelVersion, maxReferenceVersionBytes) {
		return invalid(ErrorCodeReferenceInvalid, "rerank model version is invalid")
	}
	return nil
}

// IsBound 表示 Model Run 已冻结实际使用的 Retrieval 版本。
func (ref RetrievalRef) IsBound() bool {
	return ref.IndexVersionID != "" || ref.EmbeddingVersionID != nil || ref.RerankModelVersion != ""
}

// TokenUsage 保存 Provider 回显的非负 Token 计数。
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// Validate 校验 Token 计数非负且总数精确相等。
func (usage TokenUsage) Validate() error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 ||
		usage.InputTokens > int64(^uint64(0)>>1)-usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return invalid(ErrorCodeModelCallInvalid, "model token usage is invalid")
	}
	return nil
}

// ModelRun 是一次绑定 Workflow Node Attempt 与全部实际版本的 Agent pipeline 事实。
type ModelRun struct {
	ID                    foundation.ID
	WorkspaceID           foundation.ID
	WorkflowRunID         foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	ModelSettingsRevision *int64
	Model                 ModelRef
	Profile               ModelProfileRef
	Prompt                PromptRef
	Schema                SchemaRef
	ReducedSchema         SchemaRef
	Retrieval             RetrievalRef
	MemoryContext         RAGMemoryContextRef
	Status                ModelRunStatus
	FinalResultType       string
	FinalErrorCode        string
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CompletedAt           *time.Time
}

// ValidateModelRun 校验 Model Run 的绑定、版本快照和生命周期一致性。
func ValidateModelRun(run ModelRun) error {
	ids := []foundation.ID{run.ID, run.WorkspaceID, run.WorkflowRunID, run.NodeRunID, run.NodeAttemptID}
	if !canonicalUniqueIDs(ids, true) || run.Model.Validate() != nil || run.Profile.Validate() != nil ||
		run.Prompt.Validate() != nil || run.Schema.Validate() != nil || run.ReducedSchema.Validate() != nil ||
		run.Version < 1 || run.CreatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) {
		return invalid(ErrorCodeModelRunInvalid, "model run binding or version snapshot is invalid")
	}
	if run.ModelSettingsRevision != nil && *run.ModelSettingsRevision < 0 {
		return invalid(ErrorCodeModelRunInvalid, "model settings revision cannot be negative")
	}
	retrievalBound := run.Retrieval.IsBound()
	if retrievalBound && run.Retrieval.Validate() != nil {
		return invalid(ErrorCodeModelRunInvalid, "model run retrieval snapshot is invalid")
	}
	if run.MemoryContext.IsBound() && run.MemoryContext.Validate() != nil {
		return invalid(ErrorCodeModelRunInvalid, "model run memory context snapshot is invalid")
	}
	if !retrievalBound && run.Schema.ID != RAGAnswerSchemaID {
		return invalid(ErrorCodeModelRunInvalid, "model run retrieval snapshot is required")
	}
	switch run.Status {
	case ModelRunRunning:
		if run.CompletedAt != nil || run.FinalResultType != "" || run.FinalErrorCode != "" {
			return inconsistent(ErrorCodeModelRunInvalid, "running model run cannot have a final result")
		}
	case ModelRunSucceeded, ModelRunRefused:
		if !validModelRunCompletionTime(run.CompletedAt, run.UpdatedAt) {
			return inconsistent(ErrorCodeModelRunInvalid, "completed model run has invalid lifecycle")
		}
		if run.Status == ModelRunSucceeded && (!validSuccessfulResultType(run.FinalResultType) || run.FinalErrorCode != "") {
			return inconsistent(ErrorCodeModelRunInvalid, "successful model run cannot have an error code")
		}
		if run.FinalResultType == ResultTypeRAGAnswer && !retrievalBound {
			return inconsistent(ErrorCodeModelRunInvalid, "rag answer model run requires a retrieval snapshot")
		}
		if run.Status == ModelRunRefused && (run.FinalResultType != ResultTypeRefusal || !canonicalErrorCode(run.FinalErrorCode)) {
			return inconsistent(ErrorCodeModelRunInvalid, "refused model run requires a stable reason code")
		}
	case ModelRunFailed, ModelRunUnknown:
		if !validModelRunCompletionTime(run.CompletedAt, run.UpdatedAt) || run.FinalResultType != "" || !canonicalErrorCode(run.FinalErrorCode) {
			return inconsistent(ErrorCodeModelRunInvalid, "failed or unknown model run requires a stable error")
		}
	default:
		return invalid(ErrorCodeModelRunInvalid, "model run status is unsupported")
	}
	return nil
}

// ValidateModelRunTransition 校验 Model Run 只能从 RUNNING 进入一个终态。
func ValidateModelRunTransition(from, to ModelRunStatus) error {
	if from == ModelRunRunning && (to == ModelRunSucceeded || to == ModelRunRefused || to == ModelRunFailed || to == ModelRunUnknown) {
		return nil
	}
	return versionConflict(ErrorCodeModelRunTransitionInvalid, "model run status transition is not allowed")
}

// ModelCall 是调用前写入 STARTED、完成后 CAS 到终态的模型调用事实。
type ModelCall struct {
	ID              foundation.ID
	ModelRunID      foundation.ID
	CallNo          int
	Phase           ModelCallPhase
	Model           ModelRef
	Profile         ModelProfileRef
	Prompt          PromptRef
	Schema          SchemaRef
	MaxOutputTokens int
	Status          ModelCallStatus
	RequestHash     string
	ResponseHash    string
	RequestBytes    int64
	ResponseBytes   int64
	Usage           TokenUsage
	LatencyMillis   int64
	ErrorCode       string
	Version         int64
	StartedAt       time.Time
	CompletedAt     *time.Time
}

// ValidateModelCall 校验 Model Call 阶段、hash、usage 和生命周期一致性。
func ValidateModelCall(call ModelCall) error {
	if !canonicalID(call.ID) || !canonicalID(call.ModelRunID) || call.ID == call.ModelRunID || call.CallNo < 1 ||
		!validModelCallPhase(call.Phase) || call.Model.Validate() != nil || call.Profile.Validate() != nil ||
		call.Prompt.Validate() != nil || call.Schema.Validate() != nil ||
		call.MaxOutputTokens <= 0 || call.MaxOutputTokens > MaxModelCallOutputTokens ||
		!lowerHex(call.RequestHash, 64) || call.RequestBytes <= 0 ||
		call.ResponseBytes < 0 || call.Version < 1 || call.StartedAt.IsZero() || call.Usage.Validate() != nil {
		return invalid(ErrorCodeModelCallInvalid, "model call binding or metrics are invalid")
	}
	switch call.Status {
	case ModelCallStarted:
		if call.CompletedAt != nil || call.ResponseHash != "" || call.ResponseBytes != 0 || call.LatencyMillis != 0 ||
			call.ErrorCode != "" || call.Usage != (TokenUsage{}) {
			return inconsistent(ErrorCodeModelCallInvalid, "started model call cannot contain a final result")
		}
	case ModelCallSucceeded:
		if !validCompletionTime(call.CompletedAt, call.StartedAt) || !lowerHex(call.ResponseHash, 64) ||
			call.ResponseBytes <= 0 || call.LatencyMillis < 0 || call.ErrorCode != "" {
			return inconsistent(ErrorCodeModelCallInvalid, "successful model call result is invalid")
		}
	case ModelCallFailed, ModelCallUnknown:
		if !validCompletionTime(call.CompletedAt, call.StartedAt) || call.LatencyMillis < 0 || !canonicalErrorCode(call.ErrorCode) {
			return inconsistent(ErrorCodeModelCallInvalid, "failed or unknown model call requires a stable error")
		}
		if call.Status == ModelCallUnknown && (call.ResponseHash != "" || call.ResponseBytes != 0 || call.Usage != (TokenUsage{})) {
			return inconsistent(ErrorCodeModelCallInvalid, "unknown model call cannot claim a provider response")
		}
		if call.ResponseHash != "" && !lowerHex(call.ResponseHash, 64) {
			return invalid(ErrorCodeModelCallInvalid, "model call response hash is invalid")
		}
		if (call.ResponseHash == "") != (call.ResponseBytes == 0) {
			return inconsistent(ErrorCodeModelCallInvalid, "model call response hash and byte count are inconsistent")
		}
	default:
		return invalid(ErrorCodeModelCallInvalid, "model call status is unsupported")
	}
	return nil
}

// ValidateModelCallTransition 校验 Model Call 只能从 STARTED 进入一个终态。
func ValidateModelCallTransition(from, to ModelCallStatus) error {
	if from == ModelCallStarted && (to == ModelCallSucceeded || to == ModelCallFailed || to == ModelCallUnknown) {
		return nil
	}
	return versionConflict(ErrorCodeModelCallTransitionInvalid, "model call status transition is not allowed")
}

func validModelCallPhase(value ModelCallPhase) bool {
	return value == ModelCallPlan || value == ModelCallInitial || value == ModelCallRepair || value == ModelCallReduced || value == ModelCallReview
}

func validCompletionTime(completedAt *time.Time, earliest time.Time) bool {
	return completedAt != nil && !completedAt.IsZero() && !completedAt.Before(earliest)
}

func validModelRunCompletionTime(completedAt *time.Time, updatedAt time.Time) bool {
	return completedAt != nil && !completedAt.IsZero() && completedAt.Equal(updatedAt)
}

func validSuccessfulResultType(value string) bool {
	return value == ResultTypeRelationAssessment || value == ResultTypeRAGAnswer || value == ResultTypeFaithfulnessReview ||
		value == ResultTypeArtifactSection || value == ResultTypeToolRequest || value == ResultTypeClarification
}

func canonicalErrorCode(value string) bool {
	if value == "" || len(value) > maxStableErrorCodeBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func lowerHex(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
