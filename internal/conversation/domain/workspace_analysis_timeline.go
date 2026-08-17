package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// WorkspaceAnalysisTimelineSchemaID 是 Answer-scoped 工作区分析权威时间线 Schema。
	WorkspaceAnalysisTimelineSchemaID = "conversation.workspace_analysis_timeline"
	// WorkspaceAnalysisTimelineSchemaVersionV1 是工作区分析时间线首个版本。
	WorkspaceAnalysisTimelineSchemaVersionV1 = "v1"
	// MaxWorkspaceAnalysisTimelineBytes 是单个时间线快照的最大字节数。
	MaxWorkspaceAnalysisTimelineBytes = 256 * 1024
	// MaxWorkspaceAnalysisTimelineItems 是单个时间线快照的最大安全投影项数。
	MaxWorkspaceAnalysisTimelineItems = 32

	maxWorkspaceAnalysisTimelineCodes      = 16
	maxWorkspaceAnalysisTimelineCodeBytes  = 128
	maxWorkspaceAnalysisTimelineHitCount   = 5
	maxWorkspaceAnalysisTimelineDurationMS = int64(60 * 60 * 1000)
)

// WorkspaceAnalysisPhase 是 Workflow 节点、操作和公开时间线共享的稳定阶段键。
type WorkspaceAnalysisPhase string

const (
	// WorkspaceAnalysisPhaseInspectWorkspace 表示读取 Git 聚合阶段。
	WorkspaceAnalysisPhaseInspectWorkspace WorkspaceAnalysisPhase = "inspect_workspace"
	// WorkspaceAnalysisPhaseRetrieveEvidence 表示规划并检索证据阶段。
	WorkspaceAnalysisPhaseRetrieveEvidence WorkspaceAnalysisPhase = "retrieve_evidence"
	// WorkspaceAnalysisPhaseReadEvidence 表示读取获准 Source 的阶段。
	WorkspaceAnalysisPhaseReadEvidence WorkspaceAnalysisPhase = "read_evidence"
	// WorkspaceAnalysisPhaseSynthesizeAnswer 表示生成候选回答阶段。
	WorkspaceAnalysisPhaseSynthesizeAnswer WorkspaceAnalysisPhase = "synthesize_answer"
	// WorkspaceAnalysisPhaseValidateCitations 表示确定性校验引用阶段。
	WorkspaceAnalysisPhaseValidateCitations WorkspaceAnalysisPhase = "validate_citations"
	// WorkspaceAnalysisPhaseReviewPublish 表示 Faithfulness Review 与发布阶段。
	WorkspaceAnalysisPhaseReviewPublish WorkspaceAnalysisPhase = "review_publish"
)

// WorkspaceAnalysisTimelineRunStatus 是工作区分析 Run 的公开生命周期状态。
type WorkspaceAnalysisTimelineRunStatus string

const (
	WorkspaceAnalysisTimelineRunQueued                WorkspaceAnalysisTimelineRunStatus = "queued"
	WorkspaceAnalysisTimelineRunRunning               WorkspaceAnalysisTimelineRunStatus = "running"
	WorkspaceAnalysisTimelineRunSucceeded             WorkspaceAnalysisTimelineRunStatus = "succeeded"
	WorkspaceAnalysisTimelineRunRefused               WorkspaceAnalysisTimelineRunStatus = "refused"
	WorkspaceAnalysisTimelineRunClarificationRequired WorkspaceAnalysisTimelineRunStatus = "clarification_required"
	WorkspaceAnalysisTimelineRunFailed                WorkspaceAnalysisTimelineRunStatus = "failed"
	WorkspaceAnalysisTimelineRunCancelled             WorkspaceAnalysisTimelineRunStatus = "cancelled"
)

// WorkspaceAnalysisTimelineItemKind 区分 Workflow 节点、模型调用和 Tool 调用。
type WorkspaceAnalysisTimelineItemKind string

const (
	WorkspaceAnalysisTimelineItemNode  WorkspaceAnalysisTimelineItemKind = "node"
	WorkspaceAnalysisTimelineItemModel WorkspaceAnalysisTimelineItemKind = "model"
	WorkspaceAnalysisTimelineItemTool  WorkspaceAnalysisTimelineItemKind = "tool"
)

// WorkspaceAnalysisTimelineItemStatus 是公开项的稳定执行状态。
type WorkspaceAnalysisTimelineItemStatus string

const (
	WorkspaceAnalysisTimelineItemPending   WorkspaceAnalysisTimelineItemStatus = "pending"
	WorkspaceAnalysisTimelineItemWaiting   WorkspaceAnalysisTimelineItemStatus = "waiting"
	WorkspaceAnalysisTimelineItemStarted   WorkspaceAnalysisTimelineItemStatus = "started"
	WorkspaceAnalysisTimelineItemSucceeded WorkspaceAnalysisTimelineItemStatus = "succeeded"
	WorkspaceAnalysisTimelineItemFailed    WorkspaceAnalysisTimelineItemStatus = "failed"
	WorkspaceAnalysisTimelineItemRefused   WorkspaceAnalysisTimelineItemStatus = "refused"
	WorkspaceAnalysisTimelineItemUnknown   WorkspaceAnalysisTimelineItemStatus = "unknown"
	WorkspaceAnalysisTimelineItemCancelled WorkspaceAnalysisTimelineItemStatus = "cancelled"
)

// WorkspaceAnalysisTimelineToolRef 是公开时间线允许披露的精确 Tool 名称与版本。
type WorkspaceAnalysisTimelineToolRef struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

// WorkspaceAnalysisTimelineSummaryKind 是类型化安全摘要的判别字段。
type WorkspaceAnalysisTimelineSummaryKind string

const (
	WorkspaceAnalysisTimelineSummaryGit        WorkspaceAnalysisTimelineSummaryKind = "git"
	WorkspaceAnalysisTimelineSummarySearch     WorkspaceAnalysisTimelineSummaryKind = "search"
	WorkspaceAnalysisTimelineSummarySource     WorkspaceAnalysisTimelineSummaryKind = "source"
	WorkspaceAnalysisTimelineSummaryCitation   WorkspaceAnalysisTimelineSummaryKind = "citation_validation"
	WorkspaceAnalysisTimelineSummaryModelUsage WorkspaceAnalysisTimelineSummaryKind = "model_usage"
)

// WorkspaceAnalysisTimelineSearchSummary 只披露命中数量和稳定降级码。
type WorkspaceAnalysisTimelineSearchSummary struct {
	HitCount         int      `json:"hit_count"`
	DegradationCodes []string `json:"degradation_codes"`
}

// WorkspaceAnalysisTimelineSourceSummary 只披露 Run-local 引用、内容哈希和截断标志。
type WorkspaceAnalysisTimelineSourceSummary struct {
	EvidenceRef string `json:"evidence_ref"`
	ContentHash string `json:"content_hash"`
	Truncated   bool   `json:"truncated"`
}

// WorkspaceAnalysisTimelineCitationSummary 只披露校验计数和稳定原因码。
type WorkspaceAnalysisTimelineCitationSummary struct {
	ValidCount   int      `json:"valid_count"`
	InvalidCount int      `json:"invalid_count"`
	ReasonCodes  []string `json:"reason_codes"`
}

// WorkspaceAnalysisTimelineModelSummary 只披露单次模型调用的 Token 使用量。
type WorkspaceAnalysisTimelineModelSummary struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// WorkspaceAnalysisTimelineSummary 是禁止自由文本和通用 map 的安全摘要判别联合。
type WorkspaceAnalysisTimelineSummary struct {
	Kind       WorkspaceAnalysisTimelineSummaryKind      `json:"kind"`
	Git        *WorkspaceAnalysisGitStatus               `json:"git,omitempty"`
	Search     *WorkspaceAnalysisTimelineSearchSummary   `json:"search,omitempty"`
	Source     *WorkspaceAnalysisTimelineSourceSummary   `json:"source,omitempty"`
	Citation   *WorkspaceAnalysisTimelineCitationSummary `json:"citation_validation,omitempty"`
	ModelUsage *WorkspaceAnalysisTimelineModelSummary    `json:"model_usage,omitempty"`
}

// WorkspaceAnalysisTimelineItem 是一个有序且脱敏的节点或调用投影。
type WorkspaceAnalysisTimelineItem struct {
	Sequence   int                                 `json:"sequence"`
	Kind       WorkspaceAnalysisTimelineItemKind   `json:"kind"`
	Phase      WorkspaceAnalysisPhase              `json:"phase"`
	Status     WorkspaceAnalysisTimelineItemStatus `json:"status"`
	ToolRef    *WorkspaceAnalysisTimelineToolRef   `json:"tool_ref"`
	DurationMS *int64                              `json:"duration_ms"`
	ErrorCode  *string                             `json:"error_code"`
	Summary    *WorkspaceAnalysisTimelineSummary   `json:"summary"`
}

// WorkspaceAnalysisTimelineCounter 是公开预算的已用量/硬上限对。
type WorkspaceAnalysisTimelineCounter struct {
	Used int64 `json:"used"`
	Max  int64 `json:"max"`
}

// WorkspaceAnalysisTimelineBudget 是持久账本的脱敏投影。
type WorkspaceAnalysisTimelineBudget struct {
	ModelCalls              WorkspaceAnalysisTimelineCounter  `json:"model_calls"`
	ToolCalls               WorkspaceAnalysisTimelineCounter  `json:"tool_calls"`
	SourceReads             WorkspaceAnalysisTimelineCounter  `json:"source_reads"`
	InputTokens             WorkspaceAnalysisTimelineCounter  `json:"input_tokens"`
	OutputTokens            WorkspaceAnalysisTimelineCounter  `json:"output_tokens"`
	EstimatedCostMicrounits *WorkspaceAnalysisTimelineCounter `json:"estimated_cost_microunits"`
}

// WorkspaceAnalysisTimeline 是 Answer-scoped 权威快照；SSE 只能触发其重载，不能覆盖它。
type WorkspaceAnalysisTimeline struct {
	SchemaID                  string                             `json:"schema_id"`
	SchemaVersion             string                             `json:"schema_version"`
	WorkspaceID               foundation.ID                      `json:"workspace_id"`
	AnswerID                  foundation.ID                      `json:"answer_id"`
	AnalysisRunID             foundation.ID                      `json:"analysis_run_id"`
	RunStatus                 WorkspaceAnalysisTimelineRunStatus `json:"run_status"`
	TerminationReason         *string                            `json:"termination_reason"`
	Items                     []WorkspaceAnalysisTimelineItem    `json:"items"`
	Budget                    WorkspaceAnalysisTimelineBudget    `json:"budget"`
	LatestServerEventSequence int64                              `json:"latest_server_event_sequence"`
}

// CanonicalWorkspaceAnalysisTimeline 是严格解码并重新序列化后的时间线文档。
type CanonicalWorkspaceAnalysisTimeline struct {
	Timeline WorkspaceAnalysisTimeline
	Document json.RawMessage
	Hash     string
}

// Validate 校验时间线身份、状态、顺序、安全摘要和预算边界。
func (timeline WorkspaceAnalysisTimeline) Validate() error {
	if timeline.SchemaID != WorkspaceAnalysisTimelineSchemaID || timeline.SchemaVersion != WorkspaceAnalysisTimelineSchemaVersionV1 ||
		!validWorkspaceAnalysisID(timeline.WorkspaceID) || !validWorkspaceAnalysisID(timeline.AnswerID) ||
		!validWorkspaceAnalysisID(timeline.AnalysisRunID) || timeline.WorkspaceID == timeline.AnswerID ||
		timeline.WorkspaceID == timeline.AnalysisRunID || timeline.AnswerID == timeline.AnalysisRunID ||
		timeline.Items == nil || len(timeline.Items) > MaxWorkspaceAnalysisTimelineItems || timeline.LatestServerEventSequence < 0 {
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline envelope is invalid", nil)
	}
	if err := validateWorkspaceAnalysisTimelineRun(timeline.RunStatus, timeline.TerminationReason); err != nil {
		return err
	}
	for index := range timeline.Items {
		if timeline.Items[index].Sequence != index+1 {
			return workspaceAnalysisTimelineInvalid("workspace analysis timeline sequence is not continuous", nil)
		}
		if err := timeline.Items[index].Validate(); err != nil {
			return err
		}
	}
	if err := timeline.Budget.Validate(); err != nil {
		return err
	}
	if timeline.RunStatus == WorkspaceAnalysisTimelineRunSucceeded &&
		(timeline.Budget.ModelCalls.Used != agentdomain.WorkspaceAnalysisV1MaxModelCalls ||
			timeline.Budget.ToolCalls.Used < agentdomain.WorkspaceAnalysisV1MinCompletedToolCalls ||
			!timeline.hasCompleteSuccessfulOperationProjection()) {
		return workspaceAnalysisTimelineInvalid("successful workspace analysis timeline has incomplete budget facts", nil)
	}
	return nil
}

func (timeline WorkspaceAnalysisTimeline) hasCompleteSuccessfulOperationProjection() bool {
	operationCounts := make(map[WorkspaceAnalysisPhase]int, 6)
	modelCalls, toolCalls := int64(0), int64(0)
	inputTokens, outputTokens := int64(0), int64(0)
	for _, item := range timeline.Items {
		if item.Kind == WorkspaceAnalysisTimelineItemNode {
			continue
		}
		if item.Status != WorkspaceAnalysisTimelineItemSucceeded {
			return false
		}
		operationCounts[item.Phase]++
		if item.Kind == WorkspaceAnalysisTimelineItemModel {
			modelCalls++
			inputTokens += item.Summary.ModelUsage.InputTokens
			outputTokens += item.Summary.ModelUsage.OutputTokens
		} else {
			toolCalls++
		}
	}
	return operationCounts[WorkspaceAnalysisPhaseInspectWorkspace] == 1 &&
		operationCounts[WorkspaceAnalysisPhaseRetrieveEvidence] == 2 &&
		operationCounts[WorkspaceAnalysisPhaseReadEvidence] >= 1 &&
		operationCounts[WorkspaceAnalysisPhaseReadEvidence] <= agentdomain.WorkspaceAnalysisV1MaxSourceReads &&
		operationCounts[WorkspaceAnalysisPhaseSynthesizeAnswer] == 1 &&
		operationCounts[WorkspaceAnalysisPhaseValidateCitations] == 1 &&
		operationCounts[WorkspaceAnalysisPhaseReviewPublish] == 1 &&
		modelCalls == timeline.Budget.ModelCalls.Used && toolCalls == timeline.Budget.ToolCalls.Used &&
		int64(operationCounts[WorkspaceAnalysisPhaseReadEvidence]) == timeline.Budget.SourceReads.Used &&
		inputTokens == timeline.Budget.InputTokens.Used && outputTokens == timeline.Budget.OutputTokens.Used
}

// Validate 校验时间线项的阶段、精确 Tool、状态字段和类型化摘要。
func (item WorkspaceAnalysisTimelineItem) Validate() error {
	if item.Sequence < 1 || item.Sequence > MaxWorkspaceAnalysisTimelineItems || !validWorkspaceAnalysisPhase(item.Phase) {
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline item identity is invalid", nil)
	}
	if err := item.validateKindBinding(); err != nil {
		return err
	}
	if err := item.validateStatusFields(); err != nil {
		return err
	}
	if item.Summary != nil {
		if err := item.Summary.Validate(); err != nil {
			return err
		}
	}
	return item.validateSummaryBinding()
}

func (item WorkspaceAnalysisTimelineItem) validateKindBinding() error {
	switch item.Kind {
	case WorkspaceAnalysisTimelineItemNode:
		if item.ToolRef != nil {
			return workspaceAnalysisTimelineInvalid("workspace analysis node item contains a tool reference", nil)
		}
	case WorkspaceAnalysisTimelineItemModel:
		if item.ToolRef != nil || (item.Phase != WorkspaceAnalysisPhaseRetrieveEvidence &&
			item.Phase != WorkspaceAnalysisPhaseSynthesizeAnswer && item.Phase != WorkspaceAnalysisPhaseReviewPublish) {
			return workspaceAnalysisTimelineInvalid("workspace analysis model item phase is invalid", nil)
		}
	case WorkspaceAnalysisTimelineItemTool:
		if item.ToolRef == nil || !validWorkspaceAnalysisTimelineTool(*item.ToolRef, item.Phase) {
			return workspaceAnalysisTimelineInvalid("workspace analysis tool item reference is invalid", nil)
		}
	default:
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline item kind is invalid", nil)
	}
	return nil
}

func (item WorkspaceAnalysisTimelineItem) validateStatusFields() error {
	switch item.Status {
	case WorkspaceAnalysisTimelineItemPending, WorkspaceAnalysisTimelineItemWaiting, WorkspaceAnalysisTimelineItemStarted:
		if item.DurationMS != nil || item.ErrorCode != nil || item.Summary != nil {
			return workspaceAnalysisTimelineInvalid("active workspace analysis timeline item contains terminal facts", nil)
		}
	case WorkspaceAnalysisTimelineItemSucceeded:
		if !validWorkspaceAnalysisDuration(item.DurationMS) || item.ErrorCode != nil {
			return workspaceAnalysisTimelineInvalid("successful workspace analysis timeline item is invalid", nil)
		}
	case WorkspaceAnalysisTimelineItemFailed, WorkspaceAnalysisTimelineItemRefused,
		WorkspaceAnalysisTimelineItemUnknown, WorkspaceAnalysisTimelineItemCancelled:
		if !validWorkspaceAnalysisDuration(item.DurationMS) || item.ErrorCode == nil || item.Summary != nil ||
			!validWorkspaceAnalysisItemError(item.Status, *item.ErrorCode) {
			return workspaceAnalysisTimelineInvalid("terminated workspace analysis timeline item is invalid", nil)
		}
	default:
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline item status is invalid", nil)
	}
	return nil
}

func (item WorkspaceAnalysisTimelineItem) validateSummaryBinding() error {
	if item.Status != WorkspaceAnalysisTimelineItemSucceeded {
		return nil
	}
	switch item.Kind {
	case WorkspaceAnalysisTimelineItemNode:
		if item.Summary != nil {
			return workspaceAnalysisTimelineInvalid("workspace analysis node item contains an operation summary", nil)
		}
	case WorkspaceAnalysisTimelineItemModel:
		if item.Summary == nil || item.Summary.Kind != WorkspaceAnalysisTimelineSummaryModelUsage {
			return workspaceAnalysisTimelineInvalid("workspace analysis model item summary is invalid", nil)
		}
		maximum := agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens
		switch item.Phase {
		case WorkspaceAnalysisPhaseRetrieveEvidence:
			maximum = agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens
		case WorkspaceAnalysisPhaseSynthesizeAnswer:
			maximum = agentdomain.WorkspaceAnalysisV1SynthesisMaxOutputTokens
		}
		if item.Summary.ModelUsage.OutputTokens > maximum {
			return workspaceAnalysisTimelineInvalid("workspace analysis model item output usage exceeds phase budget", nil)
		}
	case WorkspaceAnalysisTimelineItemTool:
		if item.Summary == nil || item.Summary.Kind != workspaceAnalysisSummaryKindForPhase(item.Phase) {
			return workspaceAnalysisTimelineInvalid("workspace analysis tool item summary is invalid", nil)
		}
	}
	return nil
}

// Validate 校验安全摘要只设置与 kind 对应的一个成员。
func (summary WorkspaceAnalysisTimelineSummary) Validate() error {
	set := 0
	for _, present := range []bool{summary.Git != nil, summary.Search != nil, summary.Source != nil, summary.Citation != nil, summary.ModelUsage != nil} {
		if present {
			set++
		}
	}
	if set != 1 {
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline summary is not a discriminated union", nil)
	}
	switch summary.Kind {
	case WorkspaceAnalysisTimelineSummaryGit:
		if summary.Git == nil || summary.Git.Validate() != nil {
			return workspaceAnalysisTimelineInvalid("workspace analysis git timeline summary is invalid", nil)
		}
	case WorkspaceAnalysisTimelineSummarySearch:
		if summary.Search == nil || summary.Search.HitCount < 0 || summary.Search.HitCount > maxWorkspaceAnalysisTimelineHitCount ||
			!validWorkspaceAnalysisCodes(summary.Search.DegradationCodes, maxWorkspaceAnalysisTimelineCodes, false) {
			return workspaceAnalysisTimelineInvalid("workspace analysis search timeline summary is invalid", nil)
		}
	case WorkspaceAnalysisTimelineSummarySource:
		if summary.Source == nil || !validWorkspaceAnalysisEvidenceRef(summary.Source.EvidenceRef) ||
			!validLowerHex(summary.Source.ContentHash, 64) {
			return workspaceAnalysisTimelineInvalid("workspace analysis source timeline summary is invalid", nil)
		}
	case WorkspaceAnalysisTimelineSummaryCitation:
		if summary.Citation == nil || summary.Citation.ValidCount < 0 || summary.Citation.InvalidCount < 0 ||
			summary.Citation.ValidCount+summary.Citation.InvalidCount < 1 ||
			summary.Citation.ValidCount+summary.Citation.InvalidCount > maxWorkspaceAnalysisCitations ||
			!validWorkspaceAnalysisCitationCodes(summary.Citation.ReasonCodes) {
			return workspaceAnalysisTimelineInvalid("workspace analysis citation timeline summary is invalid", nil)
		}
	case WorkspaceAnalysisTimelineSummaryModelUsage:
		if summary.ModelUsage == nil || summary.ModelUsage.InputTokens < 0 ||
			summary.ModelUsage.InputTokens > agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall || summary.ModelUsage.OutputTokens < 0 {
			return workspaceAnalysisTimelineInvalid("workspace analysis model timeline summary is invalid", nil)
		}
	default:
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline summary kind is invalid", nil)
	}
	return nil
}

// Validate 校验预算用量不超过 v1 硬上限，且 maxima 不可由调用方改变。
func (budget WorkspaceAnalysisTimelineBudget) Validate() error {
	exact := []struct {
		counter WorkspaceAnalysisTimelineCounter
		maximum int64
	}{
		{budget.ModelCalls, agentdomain.WorkspaceAnalysisV1MaxModelCalls},
		{budget.ToolCalls, agentdomain.WorkspaceAnalysisV1MaxToolCalls},
		{budget.SourceReads, agentdomain.WorkspaceAnalysisV1MaxSourceReads},
		{budget.InputTokens, agentdomain.WorkspaceAnalysisV1MaxRunInputTokens},
	}
	for _, value := range exact {
		if value.counter.Max != value.maximum || value.counter.Used < 0 || value.counter.Used > value.counter.Max {
			return workspaceAnalysisTimelineInvalid("workspace analysis timeline budget is invalid", nil)
		}
	}
	minimumOutputTokens := agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens + agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens + 1
	if budget.OutputTokens.Max < minimumOutputTokens || budget.OutputTokens.Max > agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens ||
		budget.OutputTokens.Used < 0 || budget.OutputTokens.Used > budget.OutputTokens.Max {
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline output budget is invalid", nil)
	}
	if budget.EstimatedCostMicrounits != nil &&
		(budget.EstimatedCostMicrounits.Max < 1 || budget.EstimatedCostMicrounits.Used < 0 ||
			budget.EstimatedCostMicrounits.Used > budget.EstimatedCostMicrounits.Max) {
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline cost budget is invalid", nil)
	}
	return nil
}

// CanonicalizeWorkspaceAnalysisTimeline 严格解析、校验并重新序列化一个时间线快照。
func CanonicalizeWorkspaceAnalysisTimeline(raw json.RawMessage) (CanonicalWorkspaceAnalysisTimeline, error) {
	timeline, err := decodeWorkspaceAnalysisTimeline(raw)
	if err != nil {
		return CanonicalWorkspaceAnalysisTimeline{}, err
	}
	document, err := json.Marshal(timeline)
	if err != nil || len(document) > MaxWorkspaceAnalysisTimelineBytes {
		return CanonicalWorkspaceAnalysisTimeline{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline cannot be canonicalized", err)
	}
	digest := sha256.Sum256(document)
	return CanonicalWorkspaceAnalysisTimeline{Timeline: timeline, Document: document, Hash: hex.EncodeToString(digest[:])}, nil
}

type workspaceAnalysisTimelinePersistence struct {
	SchemaID                  *string                             `json:"schema_id"`
	SchemaVersion             *string                             `json:"schema_version"`
	WorkspaceID               *foundation.ID                      `json:"workspace_id"`
	AnswerID                  *foundation.ID                      `json:"answer_id"`
	AnalysisRunID             *foundation.ID                      `json:"analysis_run_id"`
	RunStatus                 *WorkspaceAnalysisTimelineRunStatus `json:"run_status"`
	TerminationReason         json.RawMessage                     `json:"termination_reason"`
	Items                     *[]json.RawMessage                  `json:"items"`
	Budget                    json.RawMessage                     `json:"budget"`
	LatestServerEventSequence *int64                              `json:"latest_server_event_sequence"`
}

type workspaceAnalysisTimelineItemPersistence struct {
	Sequence   *int                                 `json:"sequence"`
	Kind       *WorkspaceAnalysisTimelineItemKind   `json:"kind"`
	Phase      *WorkspaceAnalysisPhase              `json:"phase"`
	Status     *WorkspaceAnalysisTimelineItemStatus `json:"status"`
	ToolRef    json.RawMessage                      `json:"tool_ref"`
	DurationMS json.RawMessage                      `json:"duration_ms"`
	ErrorCode  json.RawMessage                      `json:"error_code"`
	Summary    json.RawMessage                      `json:"summary"`
}

type workspaceAnalysisTimelineToolRefPersistence struct {
	Name    *string `json:"name"`
	Version *int64  `json:"version"`
}

type workspaceAnalysisTimelineBudgetPersistence struct {
	ModelCalls              json.RawMessage `json:"model_calls"`
	ToolCalls               json.RawMessage `json:"tool_calls"`
	SourceReads             json.RawMessage `json:"source_reads"`
	InputTokens             json.RawMessage `json:"input_tokens"`
	OutputTokens            json.RawMessage `json:"output_tokens"`
	EstimatedCostMicrounits json.RawMessage `json:"estimated_cost_microunits"`
}

type workspaceAnalysisTimelineCounterPersistence struct {
	Used *int64 `json:"used"`
	Max  *int64 `json:"max"`
}

type workspaceAnalysisTimelineSummaryPersistence struct {
	Kind       *WorkspaceAnalysisTimelineSummaryKind `json:"kind"`
	Git        json.RawMessage                       `json:"git"`
	Search     json.RawMessage                       `json:"search"`
	Source     json.RawMessage                       `json:"source"`
	Citation   json.RawMessage                       `json:"citation_validation"`
	ModelUsage json.RawMessage                       `json:"model_usage"`
}

type workspaceAnalysisTimelineSearchPersistence struct {
	HitCount         *int      `json:"hit_count"`
	DegradationCodes *[]string `json:"degradation_codes"`
}

type workspaceAnalysisTimelineSourcePersistence struct {
	EvidenceRef *string `json:"evidence_ref"`
	ContentHash *string `json:"content_hash"`
	Truncated   *bool   `json:"truncated"`
}

type workspaceAnalysisTimelineCitationPersistence struct {
	ValidCount   *int      `json:"valid_count"`
	InvalidCount *int      `json:"invalid_count"`
	ReasonCodes  *[]string `json:"reason_codes"`
}

type workspaceAnalysisTimelineModelPersistence struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

func decodeWorkspaceAnalysisTimeline(raw json.RawMessage) (WorkspaceAnalysisTimeline, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelinePersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.SchemaID == nil || persisted.SchemaVersion == nil || persisted.WorkspaceID == nil ||
		persisted.AnswerID == nil || persisted.AnalysisRunID == nil || persisted.RunStatus == nil ||
		len(persisted.TerminationReason) == 0 || persisted.Items == nil || len(persisted.Budget) == 0 ||
		persisted.LatestServerEventSequence == nil {
		return WorkspaceAnalysisTimeline{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline document is invalid", err)
	}
	reason, err := decodeWorkspaceAnalysisNullableString(persisted.TerminationReason)
	if err != nil {
		return WorkspaceAnalysisTimeline{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline termination reason is invalid", err)
	}
	items := make([]WorkspaceAnalysisTimelineItem, len(*persisted.Items))
	for index, itemRaw := range *persisted.Items {
		items[index], err = decodeWorkspaceAnalysisTimelineItem(itemRaw)
		if err != nil {
			return WorkspaceAnalysisTimeline{}, err
		}
	}
	budget, err := decodeWorkspaceAnalysisTimelineBudget(persisted.Budget)
	if err != nil {
		return WorkspaceAnalysisTimeline{}, err
	}
	timeline := WorkspaceAnalysisTimeline{
		SchemaID: *persisted.SchemaID, SchemaVersion: *persisted.SchemaVersion,
		WorkspaceID: *persisted.WorkspaceID, AnswerID: *persisted.AnswerID, AnalysisRunID: *persisted.AnalysisRunID,
		RunStatus: *persisted.RunStatus, TerminationReason: reason, Items: items, Budget: budget,
		LatestServerEventSequence: *persisted.LatestServerEventSequence,
	}
	if err := timeline.Validate(); err != nil {
		return WorkspaceAnalysisTimeline{}, err
	}
	return timeline, nil
}

func decodeWorkspaceAnalysisTimelineItem(raw json.RawMessage) (WorkspaceAnalysisTimelineItem, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineItemPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.Sequence == nil || persisted.Kind == nil || persisted.Phase == nil || persisted.Status == nil ||
		len(persisted.ToolRef) == 0 || len(persisted.DurationMS) == 0 || len(persisted.ErrorCode) == 0 || len(persisted.Summary) == 0 {
		return WorkspaceAnalysisTimelineItem{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline item document is invalid", err)
	}
	toolRef, err := decodeWorkspaceAnalysisTimelineToolRef(persisted.ToolRef)
	if err != nil {
		return WorkspaceAnalysisTimelineItem{}, err
	}
	duration, err := decodeWorkspaceAnalysisNullableInt64(persisted.DurationMS)
	if err != nil {
		return WorkspaceAnalysisTimelineItem{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline duration is invalid", err)
	}
	errorCode, err := decodeWorkspaceAnalysisNullableString(persisted.ErrorCode)
	if err != nil {
		return WorkspaceAnalysisTimelineItem{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline error code is invalid", err)
	}
	summary, err := decodeWorkspaceAnalysisTimelineSummary(persisted.Summary)
	if err != nil {
		return WorkspaceAnalysisTimelineItem{}, err
	}
	item := WorkspaceAnalysisTimelineItem{
		Sequence: *persisted.Sequence, Kind: *persisted.Kind, Phase: *persisted.Phase, Status: *persisted.Status,
		ToolRef: toolRef, DurationMS: duration, ErrorCode: errorCode, Summary: summary,
	}
	if err := item.Validate(); err != nil {
		return WorkspaceAnalysisTimelineItem{}, err
	}
	return item, nil
}

func decodeWorkspaceAnalysisTimelineToolRef(raw json.RawMessage) (*WorkspaceAnalysisTimelineToolRef, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineToolRefPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.Name == nil || persisted.Version == nil {
		return nil, workspaceAnalysisTimelineInvalid("workspace analysis timeline tool reference document is invalid", err)
	}
	return &WorkspaceAnalysisTimelineToolRef{Name: *persisted.Name, Version: *persisted.Version}, nil
}

func decodeWorkspaceAnalysisTimelineBudget(raw json.RawMessage) (WorkspaceAnalysisTimelineBudget, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineBudgetPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || len(persisted.ModelCalls) == 0 || len(persisted.ToolCalls) == 0 || len(persisted.SourceReads) == 0 || len(persisted.InputTokens) == 0 ||
		len(persisted.OutputTokens) == 0 || len(persisted.EstimatedCostMicrounits) == 0 {
		return WorkspaceAnalysisTimelineBudget{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline budget document is invalid", err)
	}
	modelCalls, err := decodeWorkspaceAnalysisTimelineCounter(persisted.ModelCalls)
	if err != nil {
		return WorkspaceAnalysisTimelineBudget{}, err
	}
	toolCalls, err := decodeWorkspaceAnalysisTimelineCounter(persisted.ToolCalls)
	if err != nil {
		return WorkspaceAnalysisTimelineBudget{}, err
	}
	sourceReads, err := decodeWorkspaceAnalysisTimelineCounter(persisted.SourceReads)
	if err != nil {
		return WorkspaceAnalysisTimelineBudget{}, err
	}
	inputTokens, err := decodeWorkspaceAnalysisTimelineCounter(persisted.InputTokens)
	if err != nil {
		return WorkspaceAnalysisTimelineBudget{}, err
	}
	outputTokens, err := decodeWorkspaceAnalysisTimelineCounter(persisted.OutputTokens)
	if err != nil {
		return WorkspaceAnalysisTimelineBudget{}, err
	}
	var cost *WorkspaceAnalysisTimelineCounter
	if !isJSONNull(persisted.EstimatedCostMicrounits) {
		decoded, decodeErr := decodeWorkspaceAnalysisTimelineCounter(persisted.EstimatedCostMicrounits)
		if decodeErr != nil {
			return WorkspaceAnalysisTimelineBudget{}, decodeErr
		}
		cost = &decoded
	}
	budget := WorkspaceAnalysisTimelineBudget{
		ModelCalls: modelCalls, ToolCalls: toolCalls, SourceReads: sourceReads, InputTokens: inputTokens, OutputTokens: outputTokens,
		EstimatedCostMicrounits: cost,
	}
	if err := budget.Validate(); err != nil {
		return WorkspaceAnalysisTimelineBudget{}, err
	}
	return budget, nil
}

func decodeWorkspaceAnalysisTimelineCounter(raw json.RawMessage) (WorkspaceAnalysisTimelineCounter, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineCounterPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.Used == nil || persisted.Max == nil {
		return WorkspaceAnalysisTimelineCounter{}, workspaceAnalysisTimelineInvalid("workspace analysis timeline counter document is invalid", err)
	}
	return WorkspaceAnalysisTimelineCounter{Used: *persisted.Used, Max: *persisted.Max}, nil
}

func decodeWorkspaceAnalysisTimelineSummary(raw json.RawMessage) (*WorkspaceAnalysisTimelineSummary, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineSummaryPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.Kind == nil {
		return nil, workspaceAnalysisTimelineInvalid("workspace analysis timeline summary document is invalid", err)
	}
	summary := WorkspaceAnalysisTimelineSummary{Kind: *persisted.Kind}
	switch summary.Kind {
	case WorkspaceAnalysisTimelineSummaryGit:
		summary.Git, err = decodeWorkspaceAnalysisTimelineGit(persisted.Git)
	case WorkspaceAnalysisTimelineSummarySearch:
		summary.Search, err = decodeWorkspaceAnalysisTimelineSearch(persisted.Search)
	case WorkspaceAnalysisTimelineSummarySource:
		summary.Source, err = decodeWorkspaceAnalysisTimelineSource(persisted.Source)
	case WorkspaceAnalysisTimelineSummaryCitation:
		summary.Citation, err = decodeWorkspaceAnalysisTimelineCitation(persisted.Citation)
	case WorkspaceAnalysisTimelineSummaryModelUsage:
		summary.ModelUsage, err = decodeWorkspaceAnalysisTimelineModel(persisted.ModelUsage)
	default:
		err = errors.New("unknown workspace analysis timeline summary kind")
	}
	if err != nil || hasUnexpectedWorkspaceAnalysisSummaryMember(persisted, summary.Kind) {
		return nil, workspaceAnalysisTimelineInvalid("workspace analysis timeline summary member is invalid", err)
	}
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return &summary, nil
}

func decodeWorkspaceAnalysisTimelineGit(raw json.RawMessage) (*WorkspaceAnalysisGitStatus, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisGitStatusPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.Branch == nil || persisted.Head == nil || persisted.Clean == nil || persisted.StagedCount == nil ||
		persisted.UnstagedCount == nil || persisted.UntrackedCount == nil || persisted.ConflictCount == nil {
		return nil, errors.New("workspace analysis git timeline summary is incomplete")
	}
	return &WorkspaceAnalysisGitStatus{
		Branch: *persisted.Branch, Head: *persisted.Head, Clean: *persisted.Clean,
		StagedCount: *persisted.StagedCount, UnstagedCount: *persisted.UnstagedCount,
		UntrackedCount: *persisted.UntrackedCount, ConflictCount: *persisted.ConflictCount,
	}, nil
}

func decodeWorkspaceAnalysisTimelineSearch(raw json.RawMessage) (*WorkspaceAnalysisTimelineSearchSummary, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineSearchPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.HitCount == nil || persisted.DegradationCodes == nil {
		return nil, errors.New("workspace analysis search timeline summary is incomplete")
	}
	return &WorkspaceAnalysisTimelineSearchSummary{HitCount: *persisted.HitCount, DegradationCodes: append([]string{}, (*persisted.DegradationCodes)...)}, nil
}

func decodeWorkspaceAnalysisTimelineSource(raw json.RawMessage) (*WorkspaceAnalysisTimelineSourceSummary, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineSourcePersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.EvidenceRef == nil || persisted.ContentHash == nil || persisted.Truncated == nil {
		return nil, errors.New("workspace analysis source timeline summary is incomplete")
	}
	return &WorkspaceAnalysisTimelineSourceSummary{EvidenceRef: *persisted.EvidenceRef, ContentHash: *persisted.ContentHash, Truncated: *persisted.Truncated}, nil
}

func decodeWorkspaceAnalysisTimelineCitation(raw json.RawMessage) (*WorkspaceAnalysisTimelineCitationSummary, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineCitationPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.ValidCount == nil || persisted.InvalidCount == nil || persisted.ReasonCodes == nil {
		return nil, errors.New("workspace analysis citation timeline summary is incomplete")
	}
	return &WorkspaceAnalysisTimelineCitationSummary{
		ValidCount: *persisted.ValidCount, InvalidCount: *persisted.InvalidCount,
		ReasonCodes: append([]string{}, (*persisted.ReasonCodes)...),
	}, nil
}

func decodeWorkspaceAnalysisTimelineModel(raw json.RawMessage) (*WorkspaceAnalysisTimelineModelSummary, error) {
	persisted, err := foundationstrictjson.DecodeObject[workspaceAnalysisTimelineModelPersistence](raw, workspaceAnalysisTimelineDecodeLimits(), nil)
	if err != nil || persisted.InputTokens == nil || persisted.OutputTokens == nil {
		return nil, errors.New("workspace analysis model timeline summary is incomplete")
	}
	return &WorkspaceAnalysisTimelineModelSummary{InputTokens: *persisted.InputTokens, OutputTokens: *persisted.OutputTokens}, nil
}

func hasUnexpectedWorkspaceAnalysisSummaryMember(value workspaceAnalysisTimelineSummaryPersistence, kind WorkspaceAnalysisTimelineSummaryKind) bool {
	members := map[WorkspaceAnalysisTimelineSummaryKind]json.RawMessage{
		WorkspaceAnalysisTimelineSummaryGit: value.Git, WorkspaceAnalysisTimelineSummarySearch: value.Search,
		WorkspaceAnalysisTimelineSummarySource: value.Source, WorkspaceAnalysisTimelineSummaryCitation: value.Citation,
		WorkspaceAnalysisTimelineSummaryModelUsage: value.ModelUsage,
	}
	for candidate, raw := range members {
		if candidate != kind && len(raw) != 0 {
			return true
		}
	}
	return len(members[kind]) == 0
}

func validateWorkspaceAnalysisTimelineRun(status WorkspaceAnalysisTimelineRunStatus, reason *string) error {
	switch status {
	case WorkspaceAnalysisTimelineRunQueued, WorkspaceAnalysisTimelineRunRunning:
		if reason != nil {
			return workspaceAnalysisTimelineInvalid("active workspace analysis timeline has a termination reason", nil)
		}
	case WorkspaceAnalysisTimelineRunSucceeded:
		if reason == nil || *reason != WorkspaceAnalysisCompleted {
			return workspaceAnalysisTimelineInvalid("successful workspace analysis timeline reason is invalid", nil)
		}
	case WorkspaceAnalysisTimelineRunRefused:
		if reason == nil || !workspaceAnalysisCodeHasCategory(*reason, WorkspaceAnalysisCodeRefusal) {
			return workspaceAnalysisTimelineInvalid("refused workspace analysis timeline reason is invalid", nil)
		}
	case WorkspaceAnalysisTimelineRunClarificationRequired:
		if reason == nil || *reason != WorkspaceAnalysisClarificationRequired {
			return workspaceAnalysisTimelineInvalid("clarification workspace analysis timeline reason is invalid", nil)
		}
	case WorkspaceAnalysisTimelineRunFailed:
		contract, ok := workspaceAnalysisContract(reason)
		if !ok || contract.PublicationStatus != WorkspaceAnalysisPublicationFailed {
			return workspaceAnalysisTimelineInvalid("failed workspace analysis timeline reason is invalid", nil)
		}
	case WorkspaceAnalysisTimelineRunCancelled:
		if reason == nil || *reason != string(WorkspaceAnalysisCancelled) {
			return workspaceAnalysisTimelineInvalid("cancelled workspace analysis timeline reason is invalid", nil)
		}
	default:
		return workspaceAnalysisTimelineInvalid("workspace analysis timeline run status is invalid", nil)
	}
	return nil
}

func validWorkspaceAnalysisItemError(status WorkspaceAnalysisTimelineItemStatus, code string) bool {
	contract, ok := WorkspaceAnalysisContractForPublicCode(code)
	if !ok {
		return false
	}
	switch status {
	case WorkspaceAnalysisTimelineItemFailed:
		return contract.PublicationStatus == WorkspaceAnalysisPublicationFailed && code != string(WorkspaceAnalysisResultUnknown)
	case WorkspaceAnalysisTimelineItemRefused:
		return contract.Category == WorkspaceAnalysisCodeRefusal
	case WorkspaceAnalysisTimelineItemUnknown:
		return code == string(WorkspaceAnalysisResultUnknown)
	case WorkspaceAnalysisTimelineItemCancelled:
		return code == string(WorkspaceAnalysisCancelled)
	default:
		return false
	}
}

func validWorkspaceAnalysisTimelineTool(ref WorkspaceAnalysisTimelineToolRef, phase WorkspaceAnalysisPhase) bool {
	want := WorkspaceAnalysisTimelineToolRef{}
	switch phase {
	case WorkspaceAnalysisPhaseInspectWorkspace:
		want = WorkspaceAnalysisTimelineToolRef{Name: "ReadGitStatus", Version: 2}
	case WorkspaceAnalysisPhaseRetrieveEvidence:
		want = WorkspaceAnalysisTimelineToolRef{Name: "SearchKnowledge", Version: 2}
	case WorkspaceAnalysisPhaseReadEvidence:
		want = WorkspaceAnalysisTimelineToolRef{Name: "ReadSource", Version: 3}
	case WorkspaceAnalysisPhaseValidateCitations:
		want = WorkspaceAnalysisTimelineToolRef{Name: "ValidateCitation", Version: 3}
	}
	return ref == want
}

func workspaceAnalysisSummaryKindForPhase(phase WorkspaceAnalysisPhase) WorkspaceAnalysisTimelineSummaryKind {
	switch phase {
	case WorkspaceAnalysisPhaseInspectWorkspace:
		return WorkspaceAnalysisTimelineSummaryGit
	case WorkspaceAnalysisPhaseRetrieveEvidence:
		return WorkspaceAnalysisTimelineSummarySearch
	case WorkspaceAnalysisPhaseReadEvidence:
		return WorkspaceAnalysisTimelineSummarySource
	case WorkspaceAnalysisPhaseValidateCitations:
		return WorkspaceAnalysisTimelineSummaryCitation
	default:
		return ""
	}
}

func validWorkspaceAnalysisPhase(phase WorkspaceAnalysisPhase) bool {
	switch phase {
	case WorkspaceAnalysisPhaseInspectWorkspace, WorkspaceAnalysisPhaseRetrieveEvidence, WorkspaceAnalysisPhaseReadEvidence,
		WorkspaceAnalysisPhaseSynthesizeAnswer, WorkspaceAnalysisPhaseValidateCitations, WorkspaceAnalysisPhaseReviewPublish:
		return true
	default:
		return false
	}
}

func validWorkspaceAnalysisDuration(value *int64) bool {
	return value != nil && *value >= 0 && *value <= maxWorkspaceAnalysisTimelineDurationMS
}

func validWorkspaceAnalysisEvidenceRef(value string) bool {
	return len(value) == 2 && value[0] == 'E' && value[1] >= '1' && value[1] <= '5'
}

func validWorkspaceAnalysisCodes(values []string, maximum int, required bool) bool {
	if values == nil || len(values) > maximum || (required && len(values) == 0) || !sort.StringsAreSorted(values) {
		return false
	}
	previous := ""
	for _, value := range values {
		if value == previous || !validWorkspaceAnalysisStableCode(value) {
			return false
		}
		previous = value
	}
	return true
}

func validWorkspaceAnalysisCitationCodes(values []string) bool {
	if !validWorkspaceAnalysisCodes(values, 4, true) {
		return false
	}
	for _, value := range values {
		switch value {
		case "OK", "BINDING_MISMATCH", "CITATION_UNRESOLVABLE", "EVIDENCE_INELIGIBLE":
		default:
			return false
		}
	}
	return true
}

func validWorkspaceAnalysisStableCode(value string) bool {
	if len(value) == 0 || len(value) > maxWorkspaceAnalysisTimelineCodeBytes || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func decodeWorkspaceAnalysisNullableString(raw json.RawMessage) (*string, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func decodeWorkspaceAnalysisNullableInt64(raw json.RawMessage) (*int64, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func workspaceAnalysisContract(reason *string) (WorkspaceAnalysisPublicCodeContract, bool) {
	if reason == nil {
		return WorkspaceAnalysisPublicCodeContract{}, false
	}
	return WorkspaceAnalysisContractForPublicCode(*reason)
}

func workspaceAnalysisCodeHasCategory(code string, category WorkspaceAnalysisPublicCodeCategory) bool {
	contract, ok := WorkspaceAnalysisContractForPublicCode(code)
	return ok && contract.Category == category
}

func workspaceAnalysisTimelineDecodeLimits() foundationstrictjson.Limits {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = MaxWorkspaceAnalysisTimelineBytes
	limits.MaxStringBytes = 512
	limits.MaxArrayItems = MaxWorkspaceAnalysisTimelineItems
	limits.MaxObjectFields = 16
	return limits
}

func workspaceAnalysisTimelineInvalid(message string, cause error) error {
	return invalid(ErrorCodeWorkspaceAnalysisTimelineInvalid, message, cause)
}
