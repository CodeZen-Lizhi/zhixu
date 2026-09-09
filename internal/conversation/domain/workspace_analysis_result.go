package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// WorkspaceAnalysisAnswerSchemaID 是已通过 Citation 与 Review 门禁的工作区分析结果 Schema。
	WorkspaceAnalysisAnswerSchemaID = "conversation.workspace_analysis_answer"
	// WorkspaceAnalysisRefusalSchemaID 是工作区分析稳定拒答结果 Schema。
	WorkspaceAnalysisRefusalSchemaID = "conversation.workspace_analysis_refusal"
	// WorkspaceAnalysisTerminationSchemaID 是工作区分析失败或取消结果 Schema。
	WorkspaceAnalysisTerminationSchemaID = "conversation.workspace_analysis_termination"
	// WorkspaceAnalysisResultSchemaVersionV1 是工作区分析公开结果的首个版本。
	WorkspaceAnalysisResultSchemaVersionV1 = "v1"
	// WorkspaceAnalysisProposalHref 是建议查看 Proposal 工作台时唯一允许的服务端链接。
	WorkspaceAnalysisProposalHref = "/proposals"
	// MaxWorkspaceAnalysisResultBytes 是单个工作区分析终态文档的最大字节数。
	MaxWorkspaceAnalysisResultBytes = 256 * 1024

	maxWorkspaceAnalysisAnswerBytes   = 64 * 1024
	maxWorkspaceAnalysisSummaryBytes  = 4 * 1024
	maxWorkspaceAnalysisBranchBytes   = 255
	maxWorkspaceAnalysisCitations     = 500
	maxWorkspaceAnalysisCitationIDLen = 128
	maxWorkspaceAnalysisGitCount      = 1_000_000
)

const (
	// AnswerResultWorkspaceAnalysis 表示成功发布的工作区分析回答。
	AnswerResultWorkspaceAnalysis AnswerResultType = "workspace_analysis"
	// AnswerResultWorkspaceAnalysisRefusal 表示工作区分析的稳定拒答。
	AnswerResultWorkspaceAnalysisRefusal AnswerResultType = "workspace_analysis_refusal"
	// AnswerResultWorkspaceAnalysisTermination 表示工作区分析失败或取消终态。
	AnswerResultWorkspaceAnalysisTermination AnswerResultType = "workspace_analysis_termination"
)

const (
	// WorkspaceAnalysisPublicationFailed 表示工作区分析以稳定失败终止。
	WorkspaceAnalysisPublicationFailed AnswerPublicationStatus = "failed"
	// WorkspaceAnalysisPublicationCancelled 表示工作区分析由用户取消。
	WorkspaceAnalysisPublicationCancelled AnswerPublicationStatus = "cancelled"
)

// WorkspaceAnalysisModelRunRequirement 描述某个发布终态对 Answer Model Run 的要求。
type WorkspaceAnalysisModelRunRequirement string

const (
	// WorkspaceAnalysisModelRunRequired 表示终态必须绑定唯一 authoring Model Run。
	WorkspaceAnalysisModelRunRequired WorkspaceAnalysisModelRunRequirement = "required"
	// WorkspaceAnalysisModelRunConditional 表示是否绑定由严格结果原因决定。
	WorkspaceAnalysisModelRunConditional WorkspaceAnalysisModelRunRequirement = "conditional"
	// WorkspaceAnalysisModelRunOptional 表示终态可以绑定相关 Model Run，也可在调用前失败。
	WorkspaceAnalysisModelRunOptional WorkspaceAnalysisModelRunRequirement = "optional"
)

// WorkspaceAnalysisPublicationRule 冻结一个工作区分析发布状态的结果类型和 Model Run 规则。
type WorkspaceAnalysisPublicationRule struct {
	Status              AnswerPublicationStatus
	ResultType          AnswerResultType
	ModelRunRequirement WorkspaceAnalysisModelRunRequirement
}

// WorkspaceAnalysisRuleForPublication 返回工作区分析终态的唯一发布规则。
func WorkspaceAnalysisRuleForPublication(status AnswerPublicationStatus) (WorkspaceAnalysisPublicationRule, error) {
	switch status {
	case AnswerPublicationCompleted:
		return WorkspaceAnalysisPublicationRule{status, AnswerResultWorkspaceAnalysis, WorkspaceAnalysisModelRunRequired}, nil
	case AnswerPublicationRefused:
		return WorkspaceAnalysisPublicationRule{status, AnswerResultWorkspaceAnalysisRefusal, WorkspaceAnalysisModelRunConditional}, nil
	case AnswerPublicationClarificationRequired:
		return WorkspaceAnalysisPublicationRule{status, AnswerResultClarification, WorkspaceAnalysisModelRunRequired}, nil
	case WorkspaceAnalysisPublicationFailed, WorkspaceAnalysisPublicationCancelled:
		return WorkspaceAnalysisPublicationRule{status, AnswerResultWorkspaceAnalysisTermination, WorkspaceAnalysisModelRunOptional}, nil
	default:
		return WorkspaceAnalysisPublicationRule{}, invalid(ErrorCodeAnswerTransitionInvalid, "workspace analysis publication status is invalid", nil)
	}
}

// WorkspaceAnalysisGitStatus 是可公开的 Git 聚合，不包含路径、porcelain 或仓库根目录。
type WorkspaceAnalysisGitStatus struct {
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	Clean          bool   `json:"clean"`
	StagedCount    int    `json:"staged_count"`
	UnstagedCount  int    `json:"unstaged_count"`
	UntrackedCount int    `json:"untracked_count"`
	ConflictCount  int    `json:"conflict_count"`
}

// Validate 校验 Git 聚合有界且 clean 与计数一致。
func (status WorkspaceAnalysisGitStatus) Validate() error {
	counts := []int{status.StagedCount, status.UnstagedCount, status.UntrackedCount, status.ConflictCount}
	dirty := false
	for _, count := range counts {
		if count < 0 || count > maxWorkspaceAnalysisGitCount {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis git count is invalid", nil)
		}
		dirty = dirty || count != 0
	}
	if !validBoundedText(status.Branch, maxWorkspaceAnalysisBranchBytes, true) || !validLowerHex(status.Head, 40, 64) || status.Clean == dirty {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis git identity or clean flag is invalid", nil)
	}
	return nil
}

// WorkspaceAnalysisBudgetSummary 是公开的已结算预算计数；它不包含 Prompt、价格配置或 reservation。
type WorkspaceAnalysisBudgetSummary struct {
	ModelCalls              int    `json:"model_calls"`
	ToolCalls               int    `json:"tool_calls"`
	InputTokens             int64  `json:"input_tokens"`
	OutputTokens            int64  `json:"output_tokens"`
	EstimatedCostMicrounits *int64 `json:"estimated_cost_microunits"`
}

// ValidateCompleted 校验成功链路只能披露 v1 策略允许的实际使用量。
func (summary WorkspaceAnalysisBudgetSummary) ValidateCompleted() error {
	if summary.ModelCalls != agentdomain.WorkspaceAnalysisV1MaxModelCalls ||
		summary.ToolCalls < agentdomain.WorkspaceAnalysisV1MinCompletedToolCalls || summary.ToolCalls > agentdomain.WorkspaceAnalysisV1MaxToolCalls ||
		summary.InputTokens < 0 || summary.InputTokens > agentdomain.WorkspaceAnalysisV1MaxRunInputTokens ||
		summary.OutputTokens < 0 || summary.OutputTokens > agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens ||
		(summary.EstimatedCostMicrounits != nil && *summary.EstimatedCostMicrounits < 0) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis completed budget is invalid", nil)
	}
	return nil
}

// WorkspaceAnalysisProposalSuggestion 是只读查看 Proposal 工作台的可选建议。
type WorkspaceAnalysisProposalSuggestion struct {
	Summary     string   `json:"summary"`
	CitationIDs []string `json:"citation_ids"`
	Href        string   `json:"href"`
}

// WorkspaceAnalysisAnswerPayload 是通过完整工作区分析门禁后的公开载荷。
type WorkspaceAnalysisAnswerPayload struct {
	AnswerMarkdown     string                               `json:"answer_markdown"`
	Citations          []agentdomain.Citation               `json:"citations"`
	GitStatus          WorkspaceAnalysisGitStatus           `json:"git_status"`
	Budget             WorkspaceAnalysisBudgetSummary       `json:"budget"`
	ProposalSuggestion *WorkspaceAnalysisProposalSuggestion `json:"proposal_suggestion"`
	TerminationReason  string                               `json:"termination_reason"`
}

// Validate 校验成功结果的公开字段、Citation 闭包和固定终止原因。
func (payload WorkspaceAnalysisAnswerPayload) Validate() error {
	if !validBoundedText(payload.AnswerMarkdown, maxWorkspaceAnalysisAnswerBytes, true) ||
		payload.Citations == nil || len(payload.Citations) == 0 || len(payload.Citations) > maxWorkspaceAnalysisCitations ||
		payload.TerminationReason != WorkspaceAnalysisCompleted {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer payload is invalid", nil)
	}
	citationIDs, err := validateWorkspaceAnalysisCitations(payload.Citations)
	if err != nil {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer facts are invalid", err)
	}
	if err := payload.GitStatus.Validate(); err != nil {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis git facts are invalid", err)
	}
	if err := payload.Budget.ValidateCompleted(); err != nil {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis budget facts are invalid", err)
	}
	if payload.ProposalSuggestion != nil {
		if err := payload.ProposalSuggestion.validate(citationIDs); err != nil {
			return err
		}
	}
	return nil
}

func (suggestion WorkspaceAnalysisProposalSuggestion) validate(citations map[string]struct{}) error {
	if !validBoundedText(suggestion.Summary, maxWorkspaceAnalysisSummaryBytes, true) || suggestion.Href != WorkspaceAnalysisProposalHref ||
		len(suggestion.CitationIDs) == 0 || len(suggestion.CitationIDs) > len(citations) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis proposal suggestion is invalid", nil)
	}
	seen := make(map[string]struct{}, len(suggestion.CitationIDs))
	for _, citationID := range suggestion.CitationIDs {
		if !validBoundedText(citationID, maxWorkspaceAnalysisCitationIDLen, true) {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis proposal citation is invalid", nil)
		}
		if _, exists := citations[citationID]; !exists {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis proposal citation is not published", nil)
		}
		if _, duplicate := seen[citationID]; duplicate {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis proposal citation is duplicated", nil)
		}
		seen[citationID] = struct{}{}
	}
	return nil
}

// WorkspaceAnalysisAnswerResult 是成功发布的工作区分析结果 Envelope。
type WorkspaceAnalysisAnswerResult struct {
	ResultType    AnswerResultType               `json:"result_type"`
	SchemaID      string                         `json:"schema_id"`
	SchemaVersion string                         `json:"schema_version"`
	ModelRunRef   foundation.ID                  `json:"model_run_ref"`
	Payload       WorkspaceAnalysisAnswerPayload `json:"payload"`
}

// Validate 校验成功结果 Envelope 与载荷。
func (result WorkspaceAnalysisAnswerResult) Validate() error {
	if result.ResultType != AnswerResultWorkspaceAnalysis || result.SchemaID != WorkspaceAnalysisAnswerSchemaID ||
		result.SchemaVersion != WorkspaceAnalysisResultSchemaVersionV1 || !validWorkspaceAnalysisID(result.ModelRunRef) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer envelope is invalid", nil)
	}
	return result.Payload.Validate()
}

// WorkspaceAnalysisRefusalReason 是拒答是否由模型创作的稳定判定依据。
type WorkspaceAnalysisRefusalReason string

const (
	// WorkspaceAnalysisEvidenceInsufficient 表示没有完整可验证的 Evidence 链。
	WorkspaceAnalysisEvidenceInsufficient WorkspaceAnalysisRefusalReason = "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT"
	// WorkspaceAnalysisCitationInvalid 表示候选引用未通过严格校验。
	WorkspaceAnalysisCitationInvalid WorkspaceAnalysisRefusalReason = "WORKSPACE_ANALYSIS_CITATION_INVALID"
	// WorkspaceAnalysisFaithfulnessRejected 表示候选未通过 Faithfulness Review。
	WorkspaceAnalysisFaithfulnessRejected WorkspaceAnalysisRefusalReason = "WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED"
	// WorkspaceAnalysisModelRefused 表示 Planner 或其他允许阶段产生了模型创作的拒答。
	WorkspaceAnalysisModelRefused WorkspaceAnalysisRefusalReason = "WORKSPACE_ANALYSIS_MODEL_REFUSED"
)

// WorkspaceAnalysisRefusalPayload 是有界且不携带事实正文的稳定拒答载荷。
type WorkspaceAnalysisRefusalPayload struct {
	ReasonCode WorkspaceAnalysisRefusalReason `json:"reason_code"`
	Summary    string                         `json:"summary"`
}

// WorkspaceAnalysisRefusalResult 是工作区分析拒答 Envelope。
type WorkspaceAnalysisRefusalResult struct {
	ResultType    AnswerResultType                `json:"result_type"`
	SchemaID      string                          `json:"schema_id"`
	SchemaVersion string                          `json:"schema_version"`
	ModelRunRef   *foundation.ID                  `json:"model_run_ref"`
	Payload       WorkspaceAnalysisRefusalPayload `json:"payload"`
}

// Validate 校验拒答原因、文本和条件 Model Run 绑定。
func (result WorkspaceAnalysisRefusalResult) Validate() error {
	if result.ResultType != AnswerResultWorkspaceAnalysisRefusal || result.SchemaID != WorkspaceAnalysisRefusalSchemaID ||
		result.SchemaVersion != WorkspaceAnalysisResultSchemaVersionV1 ||
		!validBoundedText(result.Payload.Summary, maxWorkspaceAnalysisSummaryBytes, true) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis refusal envelope is invalid", nil)
	}
	switch result.Payload.ReasonCode {
	case WorkspaceAnalysisModelRefused:
		if result.ModelRunRef == nil || !validWorkspaceAnalysisID(*result.ModelRunRef) {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "model-authored workspace analysis refusal requires a model run", nil)
		}
	case WorkspaceAnalysisEvidenceInsufficient, WorkspaceAnalysisCitationInvalid, WorkspaceAnalysisFaithfulnessRejected:
		if result.ModelRunRef != nil {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "deterministic workspace analysis refusal cannot claim a model run", nil)
		}
	default:
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis refusal reason is invalid", nil)
	}
	return nil
}

// WorkspaceAnalysisTerminationReason 是失败或取消的稳定公开原因。
type WorkspaceAnalysisTerminationReason string

const (
	// WorkspaceAnalysisCompleted 是成功结果唯一允许的终止原因。
	WorkspaceAnalysisCompleted = "COMPLETED"
	// WorkspaceAnalysisBudgetExhausted 表示持久预算不足以授权下一次调用。
	WorkspaceAnalysisBudgetExhausted WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"
	// WorkspaceAnalysisReceiptInvalid 表示确切 receipt 缺失、漂移或绑定不一致。
	WorkspaceAnalysisReceiptInvalid WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_RECEIPT_INVALID"
	// WorkspaceAnalysisResultUnknown 表示外部调用结果无法安全证明。
	WorkspaceAnalysisResultUnknown WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_RESULT_UNKNOWN"
	// WorkspaceAnalysisDeadlineExceeded 表示剩余期限不足以安全完成并持久化调用。
	WorkspaceAnalysisDeadlineExceeded WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED"
	// WorkspaceAnalysisModelFailed 表示模型调用以稳定失败终止。
	WorkspaceAnalysisModelFailed WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_MODEL_FAILED"
	// WorkspaceAnalysisToolFailed 表示精确只读 Tool 以稳定失败终止。
	WorkspaceAnalysisToolFailed WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_TOOL_FAILED"
	// WorkspaceAnalysisRuntimeFailed 表示编排阶段失败，且不存在可证明的模型或 Tool 调用事实。
	WorkspaceAnalysisRuntimeFailed WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_RUNTIME_FAILED"
	// WorkspaceAnalysisCancelled 表示用户取消已在安全检查点生效。
	WorkspaceAnalysisCancelled WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_CANCELLED"
)

// WorkspaceAnalysisTerminationPayload 是失败或取消的脱敏公开载荷。
type WorkspaceAnalysisTerminationPayload struct {
	TerminationReason WorkspaceAnalysisTerminationReason `json:"termination_reason"`
	Summary           string                             `json:"summary"`
}

// WorkspaceAnalysisTerminationResult 是失败或取消共用的严格 Envelope。
type WorkspaceAnalysisTerminationResult struct {
	ResultType    AnswerResultType                    `json:"result_type"`
	SchemaID      string                              `json:"schema_id"`
	SchemaVersion string                              `json:"schema_version"`
	ModelRunRef   *foundation.ID                      `json:"model_run_ref"`
	Payload       WorkspaceAnalysisTerminationPayload `json:"payload"`
}

// ValidateForPublication 校验失败/取消状态与稳定原因一致。
func (result WorkspaceAnalysisTerminationResult) ValidateForPublication(status AnswerPublicationStatus) error {
	if result.ResultType != AnswerResultWorkspaceAnalysisTermination || result.SchemaID != WorkspaceAnalysisTerminationSchemaID ||
		result.SchemaVersion != WorkspaceAnalysisResultSchemaVersionV1 ||
		!validBoundedText(result.Payload.Summary, maxWorkspaceAnalysisSummaryBytes, true) ||
		(result.ModelRunRef != nil && !validWorkspaceAnalysisID(*result.ModelRunRef)) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis termination envelope is invalid", nil)
	}
	switch status {
	case WorkspaceAnalysisPublicationCancelled:
		if result.Payload.TerminationReason != WorkspaceAnalysisCancelled {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "cancelled workspace analysis has an inconsistent reason", nil)
		}
	case WorkspaceAnalysisPublicationFailed:
		switch result.Payload.TerminationReason {
		case WorkspaceAnalysisBudgetExhausted, WorkspaceAnalysisReceiptInvalid, WorkspaceAnalysisResultUnknown,
			WorkspaceAnalysisDeadlineExceeded, WorkspaceAnalysisModelFailed, WorkspaceAnalysisToolFailed,
			WorkspaceAnalysisRuntimeFailed:
		default:
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "failed workspace analysis has an inconsistent reason", nil)
		}
	default:
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis termination status is invalid", nil)
	}
	if result.Payload.TerminationReason == WorkspaceAnalysisRuntimeFailed && result.ModelRunRef != nil {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "runtime-failed workspace analysis cannot claim a model run", nil)
	}
	return nil
}

// WorkspaceAnalysisPublishedResult 是规范化后可写入 Answer 的工作区分析终态文档。
type WorkspaceAnalysisPublishedResult struct {
	Type       AnswerResultType
	ModelRunID *foundation.ID
	Document   json.RawMessage
	Hash       string
}

// CanonicalizeWorkspaceAnalysisPublishedResult 严格解析终态并执行发布矩阵与 Model Run 规则。
func CanonicalizeWorkspaceAnalysisPublishedResult(status AnswerPublicationStatus, resultType AnswerResultType, raw json.RawMessage) (WorkspaceAnalysisPublishedResult, error) {
	rule, err := WorkspaceAnalysisRuleForPublication(status)
	if err != nil || rule.ResultType != resultType {
		return WorkspaceAnalysisPublishedResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis publication type is inconsistent", err)
	}
	var (
		modelRunID *foundation.ID
		document   json.RawMessage
	)
	switch resultType {
	case AnswerResultWorkspaceAnalysis:
		decoded, decodeErr := decodeWorkspaceAnalysisAnswerDocument(raw)
		if decodeErr != nil {
			return WorkspaceAnalysisPublishedResult{}, decodeErr
		}
		modelRunID = copyWorkspaceAnalysisID(&decoded.ModelRunRef)
		document = decoded.Document
	case AnswerResultWorkspaceAnalysisRefusal:
		decoded, decodeErr := decodeWorkspaceAnalysisRefusal(raw)
		if decodeErr != nil {
			return WorkspaceAnalysisPublishedResult{}, decodeErr
		}
		modelRunID = copyWorkspaceAnalysisID(decoded.ModelRunRef)
		document, err = json.Marshal(decoded)
	case AnswerResultClarification:
		decoded, decodeErr := DecodeClarification(raw, workspaceAnalysisDecodeLimits())
		if decodeErr != nil {
			return WorkspaceAnalysisPublishedResult{}, decodeErr
		}
		modelRunID = copyWorkspaceAnalysisID(&decoded.ModelRunRef)
		document, err = json.Marshal(decoded)
	case AnswerResultWorkspaceAnalysisTermination:
		decoded, decodeErr := decodeWorkspaceAnalysisTermination(raw, status)
		if decodeErr != nil {
			return WorkspaceAnalysisPublishedResult{}, decodeErr
		}
		modelRunID = copyWorkspaceAnalysisID(decoded.ModelRunRef)
		document, err = json.Marshal(decoded)
	default:
		return WorkspaceAnalysisPublishedResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis result type is unsupported", nil)
	}
	if err != nil || len(document) > MaxWorkspaceAnalysisResultBytes {
		return WorkspaceAnalysisPublishedResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis result cannot be canonicalized", err)
	}
	digest := sha256.Sum256(document)
	return WorkspaceAnalysisPublishedResult{Type: resultType, ModelRunID: modelRunID, Document: document, Hash: hex.EncodeToString(digest[:])}, nil
}

type workspaceAnalysisNullableEnvelope[T any] struct {
	ResultType    *AnswerResultType `json:"result_type"`
	SchemaID      *string           `json:"schema_id"`
	SchemaVersion *string           `json:"schema_version"`
	ModelRunRef   json.RawMessage   `json:"model_run_ref"`
	Payload       *T                `json:"payload"`
}

type workspaceAnalysisAnswerEnvelope struct {
	ResultType    *AnswerResultType                   `json:"result_type"`
	SchemaID      *string                             `json:"schema_id"`
	SchemaVersion *string                             `json:"schema_version"`
	ModelRunRef   *foundation.ID                      `json:"model_run_ref"`
	Payload       *workspaceAnalysisAnswerPersistence `json:"payload"`
}

type workspaceAnalysisAnswerPersistence struct {
	AnswerMarkdown     *string                                `json:"answer_markdown"`
	Citations          *[]agentdomain.Citation                `json:"citations"`
	GitStatus          *workspaceAnalysisGitStatusPersistence `json:"git_status"`
	Budget             *workspaceAnalysisBudgetPersistence    `json:"budget"`
	ProposalSuggestion json.RawMessage                        `json:"proposal_suggestion"`
	TerminationReason  *string                                `json:"termination_reason"`
}

type workspaceAnalysisGitStatusPersistence struct {
	Branch         *string `json:"branch"`
	Head           *string `json:"head"`
	Clean          *bool   `json:"clean"`
	StagedCount    *int    `json:"staged_count"`
	UnstagedCount  *int    `json:"unstaged_count"`
	UntrackedCount *int    `json:"untracked_count"`
	ConflictCount  *int    `json:"conflict_count"`
}

type workspaceAnalysisBudgetPersistence struct {
	ModelCalls              *int            `json:"model_calls"`
	ToolCalls               *int            `json:"tool_calls"`
	InputTokens             *int64          `json:"input_tokens"`
	OutputTokens            *int64          `json:"output_tokens"`
	EstimatedCostMicrounits json.RawMessage `json:"estimated_cost_microunits"`
}

type workspaceAnalysisProposalPersistence struct {
	Summary     *string   `json:"summary"`
	CitationIDs *[]string `json:"citation_ids"`
	Href        *string   `json:"href"`
}

func decodeWorkspaceAnalysisAnswer(raw json.RawMessage) (WorkspaceAnalysisAnswerResult, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisAnswerEnvelope](raw, workspaceAnalysisDecodeLimits(), nil)
	if err != nil || !completeWorkspaceAnalysisAnswerPersistence(persisted) {
		return WorkspaceAnalysisAnswerResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer document is invalid", err)
	}
	estimatedCost, err := decodeWorkspaceAnalysisNullableCost(persisted.Payload.Budget.EstimatedCostMicrounits)
	if err != nil {
		return WorkspaceAnalysisAnswerResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis estimated cost is invalid", err)
	}
	proposal, err := decodeWorkspaceAnalysisProposal(persisted.Payload.ProposalSuggestion)
	if err != nil {
		return WorkspaceAnalysisAnswerResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis proposal suggestion is invalid", err)
	}
	gitStatus := persisted.Payload.GitStatus
	budget := persisted.Payload.Budget
	decoded := WorkspaceAnalysisAnswerResult{
		ResultType: *persisted.ResultType, SchemaID: *persisted.SchemaID, SchemaVersion: *persisted.SchemaVersion,
		ModelRunRef: *persisted.ModelRunRef,
		Payload: WorkspaceAnalysisAnswerPayload{
			AnswerMarkdown: *persisted.Payload.AnswerMarkdown,
			Citations:      append([]agentdomain.Citation{}, (*persisted.Payload.Citations)...),
			GitStatus: WorkspaceAnalysisGitStatus{
				Branch: *gitStatus.Branch, Head: *gitStatus.Head, Clean: *gitStatus.Clean,
				StagedCount: *gitStatus.StagedCount, UnstagedCount: *gitStatus.UnstagedCount,
				UntrackedCount: *gitStatus.UntrackedCount, ConflictCount: *gitStatus.ConflictCount,
			},
			Budget: WorkspaceAnalysisBudgetSummary{
				ModelCalls: *budget.ModelCalls, ToolCalls: *budget.ToolCalls,
				InputTokens: *budget.InputTokens, OutputTokens: *budget.OutputTokens,
				EstimatedCostMicrounits: estimatedCost,
			},
			ProposalSuggestion: proposal,
			TerminationReason:  *persisted.Payload.TerminationReason,
		},
	}
	if err := decoded.Validate(); err != nil {
		return WorkspaceAnalysisAnswerResult{}, err
	}
	return decoded, nil
}

func completeWorkspaceAnalysisAnswerPersistence(persisted workspaceAnalysisAnswerEnvelope) bool {
	if persisted.ResultType == nil || persisted.SchemaID == nil || persisted.SchemaVersion == nil || persisted.ModelRunRef == nil || persisted.Payload == nil {
		return false
	}
	payload := persisted.Payload
	if payload.AnswerMarkdown == nil || payload.Citations == nil || payload.GitStatus == nil || payload.Budget == nil ||
		len(payload.ProposalSuggestion) == 0 || payload.TerminationReason == nil {
		return false
	}
	gitStatus := payload.GitStatus
	if gitStatus.Branch == nil || gitStatus.Head == nil || gitStatus.Clean == nil || gitStatus.StagedCount == nil ||
		gitStatus.UnstagedCount == nil || gitStatus.UntrackedCount == nil || gitStatus.ConflictCount == nil {
		return false
	}
	budget := payload.Budget
	return budget.ModelCalls != nil && budget.ToolCalls != nil && budget.InputTokens != nil && budget.OutputTokens != nil &&
		len(budget.EstimatedCostMicrounits) != 0
}

func decodeWorkspaceAnalysisNullableCost(raw json.RawMessage) (*int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var value int64
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &value) != nil || value < 0 {
		return nil, errors.New("workspace analysis estimated cost is not a non-negative integer or null")
	}
	return &value, nil
}

func decodeWorkspaceAnalysisProposal(raw json.RawMessage) (*WorkspaceAnalysisProposalSuggestion, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisProposalPersistence](trimmed, workspaceAnalysisDecodeLimits(), nil)
	if err != nil || persisted.Summary == nil || persisted.CitationIDs == nil || persisted.Href == nil {
		return nil, errors.New("workspace analysis proposal suggestion document is incomplete")
	}
	return &WorkspaceAnalysisProposalSuggestion{
		Summary: *persisted.Summary, CitationIDs: append([]string{}, (*persisted.CitationIDs)...), Href: *persisted.Href,
	}, nil
}

func decodeWorkspaceAnalysisRefusal(raw json.RawMessage) (WorkspaceAnalysisRefusalResult, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisNullableEnvelope[WorkspaceAnalysisRefusalPayload]](raw, workspaceAnalysisDecodeLimits(), nil)
	if err != nil || persisted.ResultType == nil || persisted.SchemaID == nil || persisted.SchemaVersion == nil || persisted.Payload == nil || len(persisted.ModelRunRef) == 0 {
		return WorkspaceAnalysisRefusalResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis refusal document is invalid", err)
	}
	modelRunID, err := decodeNullableFoundationID(persisted.ModelRunRef)
	if err != nil {
		return WorkspaceAnalysisRefusalResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis refusal model run is invalid", err)
	}
	decoded := WorkspaceAnalysisRefusalResult{
		ResultType: *persisted.ResultType, SchemaID: *persisted.SchemaID, SchemaVersion: *persisted.SchemaVersion,
		ModelRunRef: modelRunID, Payload: *persisted.Payload,
	}
	if err := decoded.Validate(); err != nil {
		return WorkspaceAnalysisRefusalResult{}, err
	}
	return decoded, nil
}

func decodeWorkspaceAnalysisTermination(raw json.RawMessage, status AnswerPublicationStatus) (WorkspaceAnalysisTerminationResult, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisNullableEnvelope[WorkspaceAnalysisTerminationPayload]](raw, workspaceAnalysisDecodeLimits(), nil)
	if err != nil || persisted.ResultType == nil || persisted.SchemaID == nil || persisted.SchemaVersion == nil || persisted.Payload == nil || len(persisted.ModelRunRef) == 0 {
		return WorkspaceAnalysisTerminationResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis termination document is invalid", err)
	}
	modelRunID, err := decodeNullableFoundationID(persisted.ModelRunRef)
	if err != nil {
		return WorkspaceAnalysisTerminationResult{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis termination model run is invalid", err)
	}
	decoded := WorkspaceAnalysisTerminationResult{
		ResultType: *persisted.ResultType, SchemaID: *persisted.SchemaID, SchemaVersion: *persisted.SchemaVersion,
		ModelRunRef: modelRunID, Payload: *persisted.Payload,
	}
	if err := decoded.ValidateForPublication(status); err != nil {
		return WorkspaceAnalysisTerminationResult{}, err
	}
	return decoded, nil
}

func workspaceAnalysisDecodeLimits() foundationstrictjson.Limits {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = MaxWorkspaceAnalysisResultBytes
	limits.MaxStringBytes = maxWorkspaceAnalysisAnswerBytes
	limits.MaxArrayItems = maxWorkspaceAnalysisCitations
	limits.MaxObjectFields = 16
	return limits
}

func validateWorkspaceAnalysisCitations(citations []agentdomain.Citation) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(citations))
	var workspaceID, indexVersionID foundation.ID
	for _, citation := range citations {
		if err := citation.Validate(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[citation.ID]; duplicate {
			return nil, errors.New("workspace analysis citation is duplicated")
		}
		if workspaceID == "" {
			workspaceID, indexVersionID = citation.WorkspaceID, citation.IndexVersionID
		} else if citation.WorkspaceID != workspaceID || citation.IndexVersionID != indexVersionID {
			return nil, errors.New("workspace analysis citations cross workspace or index version")
		}
		seen[citation.ID] = struct{}{}
	}
	return seen, nil
}

func validWorkspaceAnalysisID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validLowerHex(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		validLength = validLength || len(value) == length
	}
	if !validLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func copyWorkspaceAnalysisID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
