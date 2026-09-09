package domain

import (
	"strconv"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

const (
	// WorkspaceAnalysisTimelineSchemaVersionV2 是按持久操作序位投影动态循环的版本。
	WorkspaceAnalysisTimelineSchemaVersionV2 = "v2"
	// WorkspaceAnalysisPhaseDecideNext 表示模型依据已取得结果选择下一工具或结束取证。
	WorkspaceAnalysisPhaseDecideNext WorkspaceAnalysisPhase = "decide_next"
	// MaxWorkspaceAnalysisTimelineItemsV2 由调用预算推导；v2 不追加 Workflow node 占位项。
	MaxWorkspaceAnalysisTimelineItemsV2 = agentdomain.WorkspaceAnalysisV2MaxModelCalls + agentdomain.WorkspaceAnalysisV2MaxToolCalls
)

func (timeline WorkspaceAnalysisTimeline) validateV2() error {
	if timeline.SchemaID != WorkspaceAnalysisTimelineSchemaID || timeline.SchemaVersion != WorkspaceAnalysisTimelineSchemaVersionV2 ||
		!validWorkspaceAnalysisID(timeline.WorkspaceID) || !validWorkspaceAnalysisID(timeline.AnswerID) ||
		!validWorkspaceAnalysisID(timeline.AnalysisRunID) || timeline.WorkspaceID == timeline.AnswerID ||
		timeline.WorkspaceID == timeline.AnalysisRunID || timeline.AnswerID == timeline.AnalysisRunID ||
		timeline.Items == nil || len(timeline.Items) > MaxWorkspaceAnalysisTimelineItemsV2 || timeline.LatestServerEventSequence < 0 {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline envelope is invalid", nil)
	}
	if err := validateWorkspaceAnalysisTimelineRun(timeline.RunStatus, timeline.TerminationReason); err != nil {
		return err
	}
	if err := timeline.Budget.ValidateV2(); err != nil {
		return err
	}
	for index, item := range timeline.Items {
		if item.Sequence != index+1 {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline sequence is not continuous", nil)
		}
		if err := item.ValidateV2(); err != nil {
			return err
		}
	}
	return timeline.validateOperationProjectionV2()
}

// ValidateV2 校验动态调用精确 Tool tuple、阶段、状态与安全摘要。
func (item WorkspaceAnalysisTimelineItem) ValidateV2() error {
	if item.Sequence < 1 || item.Sequence > MaxWorkspaceAnalysisTimelineItemsV2 ||
		(!validWorkspaceAnalysisPhase(item.Phase) && item.Phase != WorkspaceAnalysisPhaseDecideNext) {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline item identity is invalid", nil)
	}
	switch item.Kind {
	case WorkspaceAnalysisTimelineItemNode:
		if item.ToolRef != nil || (item.Phase != WorkspaceAnalysisPhaseDecideNext && item.Phase != WorkspaceAnalysisPhaseSynthesizeAnswer &&
			item.Phase != WorkspaceAnalysisPhaseValidateCitations && item.Phase != WorkspaceAnalysisPhaseReviewPublish) {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 node item binding is invalid", nil)
		}
	case WorkspaceAnalysisTimelineItemModel:
		if item.ToolRef != nil || (item.Phase != WorkspaceAnalysisPhaseDecideNext &&
			item.Phase != WorkspaceAnalysisPhaseSynthesizeAnswer && item.Phase != WorkspaceAnalysisPhaseReviewPublish) {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 model item binding is invalid", nil)
		}
	case WorkspaceAnalysisTimelineItemTool:
		if item.ToolRef == nil || !validWorkspaceAnalysisTimelineToolV2(*item.ToolRef, item.Phase) {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 tool item binding is invalid", nil)
		}
	default:
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline item kind is invalid", nil)
	}
	if err := item.validateStatusFields(); err != nil {
		return err
	}
	if item.Summary != nil {
		if err := item.Summary.ValidateV2(); err != nil {
			return err
		}
	}
	if item.Status != WorkspaceAnalysisTimelineItemSucceeded {
		return nil
	}
	switch item.Kind {
	case WorkspaceAnalysisTimelineItemNode:
		if item.Summary != nil {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 node item contains an operation summary", nil)
		}
	case WorkspaceAnalysisTimelineItemModel:
		if item.Summary == nil || item.Summary.Kind != WorkspaceAnalysisTimelineSummaryModelUsage {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 model item summary is invalid", nil)
		}
		maximum := int64(agentdomain.WorkspaceAnalysisV2ReviewMaxOutputTokens)
		switch item.Phase {
		case WorkspaceAnalysisPhaseDecideNext:
			maximum = agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens
		case WorkspaceAnalysisPhaseSynthesizeAnswer:
			maximum = agentdomain.WorkspaceAnalysisV2SynthesisMaxOutputTokens
		}
		if item.Summary.ModelUsage.OutputTokens > maximum {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 model output usage exceeds phase budget", nil)
		}
	case WorkspaceAnalysisTimelineItemTool:
		if item.Summary == nil || item.Summary.Kind != workspaceAnalysisSummaryKindForPhase(item.Phase) {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 tool item summary is invalid", nil)
		}
	}
	return nil
}

// ValidateV2 保留摘要联合的严格形状，扩展 Run-global 来源命名空间与 v2 模型预算。
func (summary WorkspaceAnalysisTimelineSummary) ValidateV2() error {
	set := 0
	for _, present := range []bool{summary.Git != nil, summary.Search != nil, summary.Source != nil, summary.Citation != nil, summary.ModelUsage != nil} {
		if present {
			set++
		}
	}
	if set != 1 {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 summary is not a discriminated union", nil)
	}
	switch summary.Kind {
	case WorkspaceAnalysisTimelineSummarySource:
		if summary.Source == nil || !validWorkspaceAnalysisEvidenceRefV2(summary.Source.EvidenceRef) || !validLowerHex(summary.Source.ContentHash, 64) {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 source summary is invalid", nil)
		}
	case WorkspaceAnalysisTimelineSummaryCitation:
		if summary.Citation == nil || summary.Citation.ValidCount < 0 || summary.Citation.InvalidCount < 0 ||
			summary.Citation.ValidCount > agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs || summary.Citation.InvalidCount > agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs ||
			summary.Citation.ValidCount+summary.Citation.InvalidCount < 1 ||
			summary.Citation.ValidCount+summary.Citation.InvalidCount > agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs ||
			!validWorkspaceAnalysisCitationCodes(summary.Citation.ReasonCodes) {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 citation summary is invalid", nil)
		}
		if (summary.Citation.ValidCount > 0) != containsWorkspaceAnalysisCode(summary.Citation.ReasonCodes, "OK") ||
			(summary.Citation.InvalidCount == 0 && len(summary.Citation.ReasonCodes) != 1) ||
			(summary.Citation.InvalidCount > 0 && len(summary.Citation.ReasonCodes) == 1 && summary.Citation.ReasonCodes[0] == "OK") {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 citation counts and reasons are inconsistent", nil)
		}
	case WorkspaceAnalysisTimelineSummaryModelUsage:
		if summary.ModelUsage == nil || summary.ModelUsage.InputTokens < 0 ||
			summary.ModelUsage.InputTokens > agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall || summary.ModelUsage.OutputTokens < 0 {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 model summary is invalid", nil)
		}
	default:
		// Git and Search shapes have not changed; reuse their frozen validation.
		return summary.Validate()
	}
	return nil
}

// ValidateV2 校验来自 Run 账本的硬上限，不从时间线项目生成已用量。
func (budget WorkspaceAnalysisTimelineBudget) ValidateV2() error {
	exact := []struct {
		counter WorkspaceAnalysisTimelineCounter
		maximum int64
	}{
		{budget.ModelCalls, agentdomain.WorkspaceAnalysisV2MaxModelCalls},
		{budget.ToolCalls, agentdomain.WorkspaceAnalysisV2MaxToolCalls},
		{budget.SourceReads, agentdomain.WorkspaceAnalysisV2MaxSourceReads},
		{budget.InputTokens, agentdomain.WorkspaceAnalysisV2MaxRunInputTokens},
	}
	for _, value := range exact {
		if value.counter.Max != value.maximum || value.counter.Used < 0 || value.counter.Used > value.counter.Max {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline budget is invalid", nil)
		}
	}
	minimumOutputTokens := agentdomain.WorkspaceAnalysisV2MaxDecisions*agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens + agentdomain.WorkspaceAnalysisV2ReviewMaxOutputTokens + 1
	if budget.OutputTokens.Max < minimumOutputTokens || budget.OutputTokens.Max > agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens ||
		budget.OutputTokens.Used < 0 || budget.OutputTokens.Used > budget.OutputTokens.Max {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline output budget is invalid", nil)
	}
	if budget.SourceReads.Used > budget.ToolCalls.Used || budget.InputTokens.Used > budget.ModelCalls.Used*agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall ||
		budget.OutputTokens.Used > budget.ModelCalls.Used*agentdomain.WorkspaceAnalysisV2SynthesisMaxOutputTokens {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline budget counters are inconsistent", nil)
	}
	if budget.EstimatedCostMicrounits != nil && (budget.EstimatedCostMicrounits.Max < 1 || budget.EstimatedCostMicrounits.Used < 0 ||
		budget.EstimatedCostMicrounits.Used > budget.EstimatedCostMicrounits.Max) {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline cost budget is invalid", nil)
	}
	return nil
}

// validateOperationProjectionV2 accepts prefixes of the dynamic protocol, not a fixed tool sequence.
// Only a published success requires equality with all settled ledger counters.
func (timeline WorkspaceAnalysisTimeline) validateOperationProjectionV2() error {
	var previous *WorkspaceAnalysisTimelineItem
	var decisions, modelCalls, toolCalls, sourceReads int64
	var inputTokens, outputTokens int64
	var haveSearch, awaitingChoice, haveCandidate, haveValidation, haveReview, haveUnknown bool
	sources := make(map[string]string)
	nodes := make(map[WorkspaceAnalysisPhase]struct{})
	for index := range timeline.Items {
		item := &timeline.Items[index]
		if item.Kind == WorkspaceAnalysisTimelineItemNode {
			if _, duplicate := nodes[item.Phase]; duplicate {
				return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline repeats a node", nil)
			}
			nodes[item.Phase] = struct{}{}
			if timeline.RunStatus == WorkspaceAnalysisTimelineRunSucceeded && item.Status != WorkspaceAnalysisTimelineItemSucceeded {
				return workspaceAnalysisTimelineInvalid("workspace analysis v2 success contains an unfinished node", nil)
			}
			continue
		}
		if previous != nil && previous.Status != WorkspaceAnalysisTimelineItemSucceeded {
			return workspaceAnalysisTimelineInvalid("workspace analysis v2 timeline continues an unfinished operation", nil)
		}
		// A pending slot can be the causal request denied by a budget gate.
		// It has a journal sequence but no authorized Call or reservation.
		if item.Status != WorkspaceAnalysisTimelineItemPending {
			if item.Kind == WorkspaceAnalysisTimelineItemModel {
				modelCalls++
			} else {
				toolCalls++
			}
		}
		if haveCandidate {
			switch {
			case !haveValidation && item.Kind == WorkspaceAnalysisTimelineItemTool && item.Phase == WorkspaceAnalysisPhaseValidateCitations:
				haveValidation = true
			case haveValidation && !haveReview && item.Kind == WorkspaceAnalysisTimelineItemModel && item.Phase == WorkspaceAnalysisPhaseReviewPublish:
				if previous.Summary.Citation.InvalidCount != 0 {
					return workspaceAnalysisTimelineInvalid("workspace analysis v2 review follows invalid citations", nil)
				}
				haveReview = true
			default:
				return workspaceAnalysisTimelineInvalid("workspace analysis v2 publication order is invalid", nil)
			}
		} else {
			switch {
			case !awaitingChoice && item.Kind == WorkspaceAnalysisTimelineItemModel && item.Phase == WorkspaceAnalysisPhaseDecideNext:
				if item.Status != WorkspaceAnalysisTimelineItemPending {
					decisions++
				}
				awaitingChoice = true
			case awaitingChoice && item.Kind == WorkspaceAnalysisTimelineItemTool:
				awaitingChoice = false
				if item.Phase == WorkspaceAnalysisPhaseValidateCitations && !haveSearch {
					return workspaceAnalysisTimelineInvalid("workspace analysis v2 citation check precedes search", nil)
				}
				if item.Phase == WorkspaceAnalysisPhaseReadEvidence {
					if !haveSearch {
						return workspaceAnalysisTimelineInvalid("workspace analysis v2 source read precedes search", nil)
					}
					if item.Status != WorkspaceAnalysisTimelineItemPending {
						sourceReads++
					}
				}
			case awaitingChoice && item.Kind == WorkspaceAnalysisTimelineItemModel && item.Phase == WorkspaceAnalysisPhaseSynthesizeAnswer:
				if !haveSearch || len(sources) == 0 {
					return workspaceAnalysisTimelineInvalid("workspace analysis v2 candidate has no read evidence", nil)
				}
				haveCandidate = true
			default:
				return workspaceAnalysisTimelineInvalid("workspace analysis v2 decision and tool order is invalid", nil)
			}
		}
		if item.Status == WorkspaceAnalysisTimelineItemSucceeded {
			switch item.Summary.Kind {
			case WorkspaceAnalysisTimelineSummarySearch:
				haveSearch = haveSearch || item.Summary.Search.HitCount > 0
			case WorkspaceAnalysisTimelineSummarySource:
				source := item.Summary.Source
				if hash, found := sources[source.EvidenceRef]; found && hash != source.ContentHash {
					return workspaceAnalysisTimelineInvalid("workspace analysis v2 evidence reference was rebound", nil)
				}
				sources[source.EvidenceRef] = source.ContentHash
			case WorkspaceAnalysisTimelineSummaryCitation:
				if haveCandidate && item.Summary.Citation.ValidCount+item.Summary.Citation.InvalidCount > len(sources) {
					return workspaceAnalysisTimelineInvalid("workspace analysis v2 citations exceed read evidence", nil)
				}
			case WorkspaceAnalysisTimelineSummaryModelUsage:
				inputTokens += item.Summary.ModelUsage.InputTokens
				outputTokens += item.Summary.ModelUsage.OutputTokens
			}
		}
		haveUnknown = haveUnknown || item.Status == WorkspaceAnalysisTimelineItemUnknown
		previous = item
	}
	if decisions > agentdomain.WorkspaceAnalysisV2MaxDecisions || modelCalls > agentdomain.WorkspaceAnalysisV2MaxModelCalls ||
		toolCalls > agentdomain.WorkspaceAnalysisV2MaxToolCalls || sourceReads > agentdomain.WorkspaceAnalysisV2MaxSourceReads {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 operation count exceeds policy", nil)
	}
	if modelCalls > timeline.Budget.ModelCalls.Used || toolCalls > timeline.Budget.ToolCalls.Used || sourceReads > timeline.Budget.SourceReads.Used ||
		inputTokens > timeline.Budget.InputTokens.Used || outputTokens > timeline.Budget.OutputTokens.Used {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 ledger omits authorized operation facts", nil)
	}
	if timeline.RunStatus == WorkspaceAnalysisTimelineRunQueued && (modelCalls != 0 || toolCalls != 0) {
		return workspaceAnalysisTimelineInvalid("queued workspace analysis v2 timeline contains authorized calls", nil)
	}
	if timeline.TerminationReason != nil && *timeline.TerminationReason == string(WorkspaceAnalysisResultUnknown) && !haveUnknown {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 unknown termination has no unknown operation", nil)
	}
	if timeline.RunStatus == WorkspaceAnalysisTimelineRunSucceeded && (!haveReview || previous == nil || previous.Status != WorkspaceAnalysisTimelineItemSucceeded ||
		modelCalls != timeline.Budget.ModelCalls.Used || toolCalls != timeline.Budget.ToolCalls.Used || sourceReads != timeline.Budget.SourceReads.Used ||
		inputTokens != timeline.Budget.InputTokens.Used || outputTokens != timeline.Budget.OutputTokens.Used) {
		return workspaceAnalysisTimelineInvalid("workspace analysis v2 success has incomplete settled operation facts", nil)
	}
	return nil
}

func validWorkspaceAnalysisTimelineToolV2(ref WorkspaceAnalysisTimelineToolRef, phase WorkspaceAnalysisPhase) bool {
	switch phase {
	case WorkspaceAnalysisPhaseInspectWorkspace:
		return ref == (WorkspaceAnalysisTimelineToolRef{Name: "ReadGitStatus", Version: 3})
	case WorkspaceAnalysisPhaseRetrieveEvidence:
		return ref == (WorkspaceAnalysisTimelineToolRef{Name: "SearchKnowledge", Version: 3})
	case WorkspaceAnalysisPhaseReadEvidence:
		return ref == (WorkspaceAnalysisTimelineToolRef{Name: "ReadSource", Version: 4})
	case WorkspaceAnalysisPhaseValidateCitations:
		return ref == (WorkspaceAnalysisTimelineToolRef{Name: "ValidateCitation", Version: 4})
	default:
		return false
	}
}

func validWorkspaceAnalysisEvidenceRefV2(value string) bool {
	if len(value) < 2 || len(value) > 3 || value[0] != 'E' {
		return false
	}
	number, err := strconv.Atoi(value[1:])
	return err == nil && number >= 1 && number <= agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs && value == "E"+strconv.Itoa(number)
}

func containsWorkspaceAnalysisCode(codes []string, wanted string) bool {
	for _, code := range codes {
		if code == wanted {
			return true
		}
	}
	return false
}
