package domain

import (
	"encoding/json"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

// WorkspaceAnalysisResultSchemaVersionV2 是动态工具循环成功结果的版本；拒答和终止仍使用其原始 Schema。
const WorkspaceAnalysisResultSchemaVersionV2 = "v2"

// WorkspaceAnalysisBudgetSummaryV2 是动态循环账本的公开已结算使用量。
type WorkspaceAnalysisBudgetSummaryV2 WorkspaceAnalysisBudgetSummary

// ValidateCompleted 校验真实动态链路的有界使用量，不要求固定工具顺序或耗尽模型预算。
func (summary WorkspaceAnalysisBudgetSummaryV2) ValidateCompleted() error {
	if summary.ModelCalls < agentdomain.WorkspaceAnalysisV2MinCompletedModelCalls || summary.ModelCalls > agentdomain.WorkspaceAnalysisV2MaxModelCalls ||
		summary.ToolCalls < agentdomain.WorkspaceAnalysisV2MinCompletedToolCalls || summary.ToolCalls > agentdomain.WorkspaceAnalysisV2MaxToolCalls ||
		summary.InputTokens < 0 || summary.InputTokens > agentdomain.WorkspaceAnalysisV2MaxRunInputTokens ||
		summary.OutputTokens < 0 || summary.OutputTokens > agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens ||
		(summary.EstimatedCostMicrounits != nil && *summary.EstimatedCostMicrounits < 0) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 completed budget is invalid", nil)
	}
	// Every completed decision either selects one loop tool or ends evidence gathering.
	// Candidate generation and independent review add two model calls; final validation adds one tool call.
	if summary.ModelCalls != summary.ToolCalls+2 || summary.InputTokens > int64(summary.ModelCalls)*agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall ||
		summary.OutputTokens > int64(summary.ModelCalls-2)*agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens+
			agentdomain.WorkspaceAnalysisV2SynthesisMaxOutputTokens+agentdomain.WorkspaceAnalysisV2ReviewMaxOutputTokens {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 completed usage is inconsistent with its call counts", nil)
	}
	return nil
}

// WorkspaceAnalysisAnswerPayloadV2 只在实际调用 Git 时披露 Git 聚合；未调用时显式为 null。
type WorkspaceAnalysisAnswerPayloadV2 struct {
	AnswerMarkdown     string                               `json:"answer_markdown"`
	Citations          []agentdomain.Citation               `json:"citations"`
	GitStatus          *WorkspaceAnalysisGitStatus          `json:"git_status"`
	Budget             WorkspaceAnalysisBudgetSummaryV2     `json:"budget"`
	ProposalSuggestion *WorkspaceAnalysisProposalSuggestion `json:"proposal_suggestion"`
	TerminationReason  string                               `json:"termination_reason"`
}

// Validate 校验 v2 成功结果的 Citation 闭包、可空 Git 与独立预算合同。
func (payload WorkspaceAnalysisAnswerPayloadV2) Validate() error {
	if !validBoundedText(payload.AnswerMarkdown, maxWorkspaceAnalysisAnswerBytes, true) ||
		payload.Citations == nil || len(payload.Citations) == 0 || len(payload.Citations) > agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs ||
		payload.TerminationReason != WorkspaceAnalysisCompleted {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 answer payload is invalid", nil)
	}
	citationIDs, err := validateWorkspaceAnalysisCitations(payload.Citations)
	if err != nil {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 answer facts are invalid", err)
	}
	if payload.GitStatus != nil {
		if err := payload.GitStatus.Validate(); err != nil {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 git facts are invalid", err)
		}
	}
	if err := payload.Budget.ValidateCompleted(); err != nil {
		return err
	}
	if payload.ProposalSuggestion != nil {
		return payload.ProposalSuggestion.validate(citationIDs)
	}
	return nil
}

// WorkspaceAnalysisAnswerResultV2 是绑定候选创作 Model Run 的动态分析成功结果。
type WorkspaceAnalysisAnswerResultV2 struct {
	ResultType    AnswerResultType                 `json:"result_type"`
	SchemaID      string                           `json:"schema_id"`
	SchemaVersion string                           `json:"schema_version"`
	ModelRunRef   foundation.ID                    `json:"model_run_ref"`
	Payload       WorkspaceAnalysisAnswerPayloadV2 `json:"payload"`
}

// Validate 保持 v1/v2 Envelope 独立校验，不能只更改 schema_version 绕过旧版本约束。
func (result WorkspaceAnalysisAnswerResultV2) Validate() error {
	if result.ResultType != AnswerResultWorkspaceAnalysis || result.SchemaID != WorkspaceAnalysisAnswerSchemaID ||
		result.SchemaVersion != WorkspaceAnalysisResultSchemaVersionV2 || !validWorkspaceAnalysisID(result.ModelRunRef) {
		return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 answer envelope is invalid", nil)
	}
	if err := result.Payload.Validate(); err != nil {
		return err
	}
	seen := make(map[[5]foundation.ID]struct{}, len(result.Payload.Citations))
	for _, citation := range result.Payload.Citations {
		tuple := [5]foundation.ID{citation.WorkspaceID, citation.IndexVersionID, citation.ChunkID, citation.SourceVersionID, citation.SourceSpanID}
		for _, id := range tuple {
			if result.ModelRunRef == id {
				return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 model run reuses an evidence identity", nil)
			}
		}
		if _, duplicate := seen[tuple]; duplicate {
			return invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 citation tuple is duplicated", nil)
		}
		seen[tuple] = struct{}{}
	}
	return nil
}

type workspaceAnalysisAnswerEnvelopeV2 struct {
	ResultType    *AnswerResultType                     `json:"result_type"`
	SchemaID      *string                               `json:"schema_id"`
	SchemaVersion *string                               `json:"schema_version"`
	ModelRunRef   *foundation.ID                        `json:"model_run_ref"`
	Payload       *workspaceAnalysisAnswerPersistenceV2 `json:"payload"`
}

type workspaceAnalysisAnswerPersistenceV2 struct {
	AnswerMarkdown     *string                             `json:"answer_markdown"`
	Citations          *[]agentdomain.Citation             `json:"citations"`
	GitStatus          json.RawMessage                     `json:"git_status"`
	Budget             *workspaceAnalysisBudgetPersistence `json:"budget"`
	ProposalSuggestion json.RawMessage                     `json:"proposal_suggestion"`
	TerminationReason  *string                             `json:"termination_reason"`
}

func decodeWorkspaceAnalysisAnswerV2(raw json.RawMessage) (WorkspaceAnalysisAnswerResultV2, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisAnswerEnvelopeV2](raw, workspaceAnalysisDecodeLimits(), nil)
	if err != nil || !completeWorkspaceAnalysisAnswerPersistenceV2(persisted) {
		return WorkspaceAnalysisAnswerResultV2{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 answer document is invalid", err)
	}
	var gitStatus *WorkspaceAnalysisGitStatus
	if !isJSONNull(persisted.Payload.GitStatus) {
		gitStatus, err = decodeWorkspaceAnalysisTimelineGit(persisted.Payload.GitStatus)
		if err != nil {
			return WorkspaceAnalysisAnswerResultV2{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 git document is invalid", err)
		}
	}
	estimatedCost, err := decodeWorkspaceAnalysisNullableCost(persisted.Payload.Budget.EstimatedCostMicrounits)
	if err != nil {
		return WorkspaceAnalysisAnswerResultV2{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 estimated cost is invalid", err)
	}
	proposal, err := decodeWorkspaceAnalysisProposal(persisted.Payload.ProposalSuggestion)
	if err != nil {
		return WorkspaceAnalysisAnswerResultV2{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis v2 proposal suggestion is invalid", err)
	}
	budget := persisted.Payload.Budget
	decoded := WorkspaceAnalysisAnswerResultV2{
		ResultType: *persisted.ResultType, SchemaID: *persisted.SchemaID, SchemaVersion: *persisted.SchemaVersion,
		ModelRunRef: *persisted.ModelRunRef,
		Payload: WorkspaceAnalysisAnswerPayloadV2{
			AnswerMarkdown: *persisted.Payload.AnswerMarkdown, Citations: append([]agentdomain.Citation{}, (*persisted.Payload.Citations)...),
			GitStatus: gitStatus,
			Budget: WorkspaceAnalysisBudgetSummaryV2{
				ModelCalls: *budget.ModelCalls, ToolCalls: *budget.ToolCalls,
				InputTokens: *budget.InputTokens, OutputTokens: *budget.OutputTokens, EstimatedCostMicrounits: estimatedCost,
			},
			ProposalSuggestion: proposal, TerminationReason: *persisted.Payload.TerminationReason,
		},
	}
	if err := decoded.Validate(); err != nil {
		return WorkspaceAnalysisAnswerResultV2{}, err
	}
	return decoded, nil
}

func completeWorkspaceAnalysisAnswerPersistenceV2(persisted workspaceAnalysisAnswerEnvelopeV2) bool {
	if persisted.ResultType == nil || persisted.SchemaID == nil || persisted.SchemaVersion == nil || persisted.ModelRunRef == nil || persisted.Payload == nil {
		return false
	}
	payload := persisted.Payload
	if payload.AnswerMarkdown == nil || payload.Citations == nil || len(payload.GitStatus) == 0 || payload.Budget == nil ||
		len(payload.ProposalSuggestion) == 0 || payload.TerminationReason == nil {
		return false
	}
	budget := payload.Budget
	return budget.ModelCalls != nil && budget.ToolCalls != nil && budget.InputTokens != nil && budget.OutputTokens != nil &&
		len(budget.EstimatedCostMicrounits) != 0
}

// workspaceAnalysisDecodedAnswer shares a projection, never a widened v1 payload.
type workspaceAnalysisDecodedAnswer struct {
	SchemaVersion  string
	ModelRunRef    foundation.ID
	AnswerMarkdown string
	Citations      []agentdomain.Citation
	Document       json.RawMessage
}

func decodeWorkspaceAnalysisAnswerDocument(raw json.RawMessage) (workspaceAnalysisDecodedAnswer, error) {
	envelope, err := foundationstrictjson.DecodeObject[workspaceAnalysisNullableEnvelope[json.RawMessage]](raw, workspaceAnalysisDecodeLimits(), nil)
	if err != nil || envelope.SchemaVersion == nil {
		return workspaceAnalysisDecodedAnswer{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer version is invalid", err)
	}
	var projected workspaceAnalysisDecodedAnswer
	switch *envelope.SchemaVersion {
	case WorkspaceAnalysisResultSchemaVersionV1:
		decoded, decodeErr := decodeWorkspaceAnalysisAnswer(raw)
		if decodeErr != nil {
			return projected, decodeErr
		}
		projected.SchemaVersion, projected.ModelRunRef = decoded.SchemaVersion, decoded.ModelRunRef
		projected.AnswerMarkdown, projected.Citations = decoded.Payload.AnswerMarkdown, decoded.Payload.Citations
		projected.Document, err = json.Marshal(decoded)
	case WorkspaceAnalysisResultSchemaVersionV2:
		decoded, decodeErr := decodeWorkspaceAnalysisAnswerV2(raw)
		if decodeErr != nil {
			return projected, decodeErr
		}
		projected.SchemaVersion, projected.ModelRunRef = decoded.SchemaVersion, decoded.ModelRunRef
		projected.AnswerMarkdown, projected.Citations = decoded.Payload.AnswerMarkdown, decoded.Payload.Citations
		projected.Document, err = json.Marshal(decoded)
	default:
		return projected, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer version is unsupported", nil)
	}
	if err != nil || len(projected.Document) > MaxWorkspaceAnalysisResultBytes {
		return workspaceAnalysisDecodedAnswer{}, invalid(ErrorCodeWorkspaceAnalysisResultInvalid, "workspace analysis answer cannot be canonicalized", err)
	}
	return projected, nil
}
