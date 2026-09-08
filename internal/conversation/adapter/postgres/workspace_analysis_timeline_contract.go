package postgres

import (
	"encoding/json"
	"errors"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type workspaceAnalysisTimelineRunRow struct {
	analysisRunID, workspaceID, answerID, workflowRunID foundation.ID
	status                                              string
	terminationReason                                   *string
	modelCalls, toolCalls, sourceReads                  int64
	inputTokens, outputTokens                           int64
	maxModelCalls, maxToolCalls, maxSourceReads         int64
	maxInputTokens, maxOutputTokens                     int64
	cost, maxCost                                       *int64
	latestServerEventSequence                           int64
}

type workspaceAnalysisTimelineOperationRow struct {
	nodeKey, operationKind, callKind, status string
	ordinal                                  int
	durationMS                               *int64
	errorCode                                *string
	modelCallStatus                          *string
	inputTokens, outputTokens                *int64
	toolCallStatus, toolName                 *string
	toolVersion                              *int64
	gitBranch, gitHead                       *string
	gitClean                                 *bool
	gitStaged, gitUnstaged                   *int
	gitUntracked, gitConflicts               *int
	searchHitCount                           *int
	searchDegradations                       *string
	sourceEvidenceRef, sourceContentHash     *string
	sourceTruncated                          *bool
	citationValid, citationInvalid           *int64
	citationReasons                          *string
}

type workspaceAnalysisTimelineSortableItem struct {
	phaseOrder int
	itemOrder  int
	item       conversationdomain.WorkspaceAnalysisTimelineItem
}

func scanWorkspaceAnalysisTimelineOperation(row scanner) (workspaceAnalysisTimelineOperationRow, error) {
	var value workspaceAnalysisTimelineOperationRow
	err := row.Scan(
		&value.nodeKey, &value.operationKind, &value.ordinal, &value.callKind, &value.status,
		&value.durationMS, &value.errorCode,
		&value.modelCallStatus, &value.inputTokens, &value.outputTokens,
		&value.toolCallStatus, &value.toolName, &value.toolVersion,
		&value.gitBranch, &value.gitHead, &value.gitClean, &value.gitStaged, &value.gitUnstaged, &value.gitUntracked, &value.gitConflicts,
		&value.searchHitCount, &value.searchDegradations,
		&value.sourceEvidenceRef, &value.sourceContentHash, &value.sourceTruncated,
		&value.citationValid, &value.citationInvalid, &value.citationReasons,
	)
	if err != nil {
		return workspaceAnalysisTimelineOperationRow{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	return value, nil
}

func workspaceAnalysisTimelineOperationItem(
	run workspaceAnalysisTimelineRunRow,
	row workspaceAnalysisTimelineOperationRow,
) (conversationdomain.WorkspaceAnalysisTimelineItem, int, int, error) {
	key := agentdomain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: run.analysisRunID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeKey(row.nodeKey),
		Kind: agentdomain.WorkspaceAnalysisOperationKind(row.operationKind), Ordinal: row.ordinal,
	}
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(key)
	if err != nil || string(contract.CallKind) != row.callKind {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, errors.New("workspace analysis timeline operation slot is invalid")
	}
	phase, phaseOrder, ok := workspaceAnalysisTimelinePhase(row.nodeKey)
	if !ok {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, errors.New("workspace analysis timeline operation phase is invalid")
	}
	itemStatus, code, err := workspaceAnalysisTimelineOperationStatus(row, run)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, err
	}
	item := conversationdomain.WorkspaceAnalysisTimelineItem{
		Kind: conversationdomain.WorkspaceAnalysisTimelineItemModel, Phase: phase, Status: itemStatus,
		DurationMS: row.durationMS, ErrorCode: code,
	}
	if contract.CallKind == agentdomain.WorkspaceAnalysisOperationCallTool {
		item.Kind = conversationdomain.WorkspaceAnalysisTimelineItemTool
		ref, refErr := workspaceAnalysisTimelineToolRef(contract.Kind)
		if refErr != nil {
			return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, refErr
		}
		item.ToolRef = &ref
		if row.status != string(agentdomain.WorkspaceAnalysisOperationPending) &&
			(row.toolName == nil || row.toolVersion == nil || *row.toolName != ref.Name || *row.toolVersion != ref.Version || row.toolCallStatus == nil) {
			return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, errors.New("workspace analysis timeline tool call binding is incomplete")
		}
	} else if row.status != string(agentdomain.WorkspaceAnalysisOperationPending) && row.modelCallStatus == nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, errors.New("workspace analysis timeline model call binding is incomplete")
	}
	if item.Status == conversationdomain.WorkspaceAnalysisTimelineItemSucceeded {
		item.Summary, err = workspaceAnalysisTimelineOperationSummary(contract, row)
		if err != nil {
			return conversationdomain.WorkspaceAnalysisTimelineItem{}, 0, 0, err
		}
	}
	return item, phaseOrder, workspaceAnalysisTimelineOperationOrder(contract), nil
}

func workspaceAnalysisTimelineOperationSummary(
	contract agentdomain.WorkspaceAnalysisOperationContract,
	row workspaceAnalysisTimelineOperationRow,
) (*conversationdomain.WorkspaceAnalysisTimelineSummary, error) {
	summary := &conversationdomain.WorkspaceAnalysisTimelineSummary{}
	switch contract.Kind {
	case agentdomain.WorkspaceAnalysisOperationRetrievalPlan, agentdomain.WorkspaceAnalysisOperationAnswerSynthesis,
		agentdomain.WorkspaceAnalysisOperationFaithfulnessReview:
		if row.modelCallStatus == nil || *row.modelCallStatus != "SUCCEEDED" || row.inputTokens == nil || row.outputTokens == nil {
			return nil, errors.New("workspace analysis timeline model usage is incomplete")
		}
		summary.Kind = conversationdomain.WorkspaceAnalysisTimelineSummaryModelUsage
		summary.ModelUsage = &conversationdomain.WorkspaceAnalysisTimelineModelSummary{InputTokens: *row.inputTokens, OutputTokens: *row.outputTokens}
	case agentdomain.WorkspaceAnalysisOperationGitStatus:
		if row.gitBranch == nil || row.gitHead == nil || row.gitClean == nil || row.gitStaged == nil || row.gitUnstaged == nil || row.gitUntracked == nil || row.gitConflicts == nil {
			return nil, errors.New("workspace analysis timeline Git summary is incomplete")
		}
		summary.Kind = conversationdomain.WorkspaceAnalysisTimelineSummaryGit
		summary.Git = &conversationdomain.WorkspaceAnalysisGitStatus{
			Branch: *row.gitBranch, Head: *row.gitHead, Clean: *row.gitClean,
			StagedCount: *row.gitStaged, UnstagedCount: *row.gitUnstaged,
			UntrackedCount: *row.gitUntracked, ConflictCount: *row.gitConflicts,
		}
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		codes, err := workspaceAnalysisTimelineCodes(row.searchDegradations)
		if err != nil || row.searchHitCount == nil {
			return nil, errors.New("workspace analysis timeline search summary is incomplete")
		}
		summary.Kind = conversationdomain.WorkspaceAnalysisTimelineSummarySearch
		summary.Search = &conversationdomain.WorkspaceAnalysisTimelineSearchSummary{HitCount: *row.searchHitCount, DegradationCodes: codes}
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		if row.sourceEvidenceRef == nil || row.sourceContentHash == nil || row.sourceTruncated == nil {
			return nil, errors.New("workspace analysis timeline source summary is incomplete")
		}
		summary.Kind = conversationdomain.WorkspaceAnalysisTimelineSummarySource
		summary.Source = &conversationdomain.WorkspaceAnalysisTimelineSourceSummary{
			EvidenceRef: *row.sourceEvidenceRef, ContentHash: *row.sourceContentHash, Truncated: *row.sourceTruncated,
		}
	case agentdomain.WorkspaceAnalysisOperationCitationValidation:
		codes, err := workspaceAnalysisTimelineCodes(row.citationReasons)
		if err != nil || row.citationValid == nil || row.citationInvalid == nil {
			return nil, errors.New("workspace analysis timeline citation summary is incomplete")
		}
		summary.Kind = conversationdomain.WorkspaceAnalysisTimelineSummaryCitation
		summary.Citation = &conversationdomain.WorkspaceAnalysisTimelineCitationSummary{
			ValidCount: int(*row.citationValid), InvalidCount: int(*row.citationInvalid), ReasonCodes: codes,
		}
	default:
		return nil, errors.New("workspace analysis timeline operation summary kind is unsupported")
	}
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return summary, nil
}

func workspaceAnalysisTimelineCodes(document *string) ([]string, error) {
	if document == nil {
		return nil, errors.New("workspace analysis timeline code projection is missing")
	}
	var codes []string
	if err := json.Unmarshal([]byte(*document), &codes); err != nil || codes == nil {
		return nil, errors.New("workspace analysis timeline code projection is invalid")
	}
	return codes, nil
}

func workspaceAnalysisTimelineFromRows(
	run workspaceAnalysisTimelineRunRow,
	sortable []workspaceAnalysisTimelineSortableItem,
) conversationdomain.WorkspaceAnalysisTimeline {
	items := make([]conversationdomain.WorkspaceAnalysisTimelineItem, len(sortable))
	for index := range sortable {
		items[index] = sortable[index].item
	}
	budget := conversationdomain.WorkspaceAnalysisTimelineBudget{
		ModelCalls:  conversationdomain.WorkspaceAnalysisTimelineCounter{Used: run.modelCalls, Max: run.maxModelCalls},
		ToolCalls:   conversationdomain.WorkspaceAnalysisTimelineCounter{Used: run.toolCalls, Max: run.maxToolCalls},
		SourceReads: conversationdomain.WorkspaceAnalysisTimelineCounter{Used: run.sourceReads, Max: run.maxSourceReads},
		InputTokens: conversationdomain.WorkspaceAnalysisTimelineCounter{Used: run.inputTokens, Max: run.maxInputTokens},
		OutputTokens: conversationdomain.WorkspaceAnalysisTimelineCounter{
			Used: run.outputTokens, Max: run.maxOutputTokens,
		},
	}
	if run.cost != nil && run.maxCost != nil {
		budget.EstimatedCostMicrounits = &conversationdomain.WorkspaceAnalysisTimelineCounter{Used: *run.cost, Max: *run.maxCost}
	}
	return conversationdomain.WorkspaceAnalysisTimeline{
		SchemaID: conversationdomain.WorkspaceAnalysisTimelineSchemaID, SchemaVersion: conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV1,
		WorkspaceID: run.workspaceID, AnswerID: run.answerID, AnalysisRunID: run.analysisRunID,
		RunStatus: conversationdomain.WorkspaceAnalysisTimelineRunStatus(run.status), TerminationReason: run.terminationReason,
		Items: items, Budget: budget, LatestServerEventSequence: run.latestServerEventSequence,
	}
}

func workspaceAnalysisTimelinePhase(nodeKey string) (conversationdomain.WorkspaceAnalysisPhase, int, bool) {
	switch agentdomain.WorkspaceAnalysisOperationNodeKey(nodeKey) {
	case agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace:
		return conversationdomain.WorkspaceAnalysisPhaseInspectWorkspace, 1, true
	case agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence:
		return conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence, 2, true
	case agentdomain.WorkspaceAnalysisOperationNodeReadEvidence:
		return conversationdomain.WorkspaceAnalysisPhaseReadEvidence, 3, true
	case agentdomain.WorkspaceAnalysisOperationNodeSynthesizeAnswer:
		return conversationdomain.WorkspaceAnalysisPhaseSynthesizeAnswer, 4, true
	case agentdomain.WorkspaceAnalysisOperationNodeValidateCitations:
		return conversationdomain.WorkspaceAnalysisPhaseValidateCitations, 5, true
	case agentdomain.WorkspaceAnalysisOperationNodeReviewPublish:
		return conversationdomain.WorkspaceAnalysisPhaseReviewPublish, 6, true
	default:
		return "", 0, false
	}
}

func workspaceAnalysisTimelineNodeStatus(
	status string,
	runStatus string,
	runReason *string,
) (conversationdomain.WorkspaceAnalysisTimelineItemStatus, *string, error) {
	switch status {
	case "pending":
		return conversationdomain.WorkspaceAnalysisTimelineItemPending, nil, nil
	case "running":
		return conversationdomain.WorkspaceAnalysisTimelineItemStarted, nil, nil
	case "waiting_for_human", "retry_wait", "paused":
		return conversationdomain.WorkspaceAnalysisTimelineItemWaiting, nil, nil
	case "succeeded":
		return conversationdomain.WorkspaceAnalysisTimelineItemSucceeded, nil, nil
	case "cancelled":
		code := string(conversationdomain.WorkspaceAnalysisCancelled)
		return conversationdomain.WorkspaceAnalysisTimelineItemCancelled, &code, nil
	case "failed":
		return workspaceAnalysisTimelineTerminalItemStatus(runStatus, runReason)
	default:
		return "", nil, errors.New("workspace analysis timeline node status is unsupported")
	}
}

func workspaceAnalysisTimelineOperationStatus(
	row workspaceAnalysisTimelineOperationRow,
	run workspaceAnalysisTimelineRunRow,
) (conversationdomain.WorkspaceAnalysisTimelineItemStatus, *string, error) {
	switch agentdomain.WorkspaceAnalysisOperationStatus(row.status) {
	case agentdomain.WorkspaceAnalysisOperationPending:
		return conversationdomain.WorkspaceAnalysisTimelineItemPending, nil, nil
	case agentdomain.WorkspaceAnalysisOperationStarted:
		return conversationdomain.WorkspaceAnalysisTimelineItemStarted, nil, nil
	case agentdomain.WorkspaceAnalysisOperationSucceeded:
		return conversationdomain.WorkspaceAnalysisTimelineItemSucceeded, nil, nil
	case agentdomain.WorkspaceAnalysisOperationUnknown:
		code := string(conversationdomain.WorkspaceAnalysisResultUnknown)
		return conversationdomain.WorkspaceAnalysisTimelineItemUnknown, &code, nil
	case agentdomain.WorkspaceAnalysisOperationFailed:
		if run.status == string(conversationdomain.WorkspaceAnalysisTimelineRunRefused) ||
			run.status == string(conversationdomain.WorkspaceAnalysisTimelineRunFailed) ||
			run.status == string(conversationdomain.WorkspaceAnalysisTimelineRunCancelled) {
			return workspaceAnalysisTimelineTerminalItemStatus(run.status, run.terminationReason)
		}
		fallback := string(conversationdomain.WorkspaceAnalysisToolFailed)
		if row.callKind == string(agentdomain.WorkspaceAnalysisOperationCallModel) {
			fallback = string(conversationdomain.WorkspaceAnalysisModelFailed)
		}
		if row.errorCode != nil {
			if contract, ok := conversationdomain.WorkspaceAnalysisContractForPublicCode(*row.errorCode); ok &&
				contract.PublicationStatus == conversationdomain.WorkspaceAnalysisPublicationFailed && *row.errorCode != string(conversationdomain.WorkspaceAnalysisResultUnknown) {
				fallback = *row.errorCode
			}
		}
		return conversationdomain.WorkspaceAnalysisTimelineItemFailed, &fallback, nil
	default:
		return "", nil, errors.New("workspace analysis timeline operation status is unsupported")
	}
}

func workspaceAnalysisTimelineTerminalItemStatus(
	runStatus string,
	runReason *string,
) (conversationdomain.WorkspaceAnalysisTimelineItemStatus, *string, error) {
	if runReason == nil {
		return "", nil, errors.New("workspace analysis terminal timeline item has no public reason")
	}
	if _, ok := conversationdomain.WorkspaceAnalysisContractForPublicCode(*runReason); !ok {
		return "", nil, errors.New("workspace analysis terminal timeline item has an unsupported public reason")
	}
	switch conversationdomain.WorkspaceAnalysisTimelineRunStatus(runStatus) {
	case conversationdomain.WorkspaceAnalysisTimelineRunRefused:
		return conversationdomain.WorkspaceAnalysisTimelineItemRefused, runReason, nil
	case conversationdomain.WorkspaceAnalysisTimelineRunFailed:
		if *runReason == string(conversationdomain.WorkspaceAnalysisResultUnknown) {
			return conversationdomain.WorkspaceAnalysisTimelineItemUnknown, runReason, nil
		}
		return conversationdomain.WorkspaceAnalysisTimelineItemFailed, runReason, nil
	case conversationdomain.WorkspaceAnalysisTimelineRunCancelled:
		return conversationdomain.WorkspaceAnalysisTimelineItemCancelled, runReason, nil
	default:
		return "", nil, errors.New("workspace analysis timeline item does not match a terminal run")
	}
}

func workspaceAnalysisTimelineToolRef(kind agentdomain.WorkspaceAnalysisOperationKind) (conversationdomain.WorkspaceAnalysisTimelineToolRef, error) {
	switch kind {
	case agentdomain.WorkspaceAnalysisOperationGitStatus:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "ReadGitStatus", Version: 2}, nil
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "SearchKnowledge", Version: 2}, nil
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "ReadSource", Version: 3}, nil
	case agentdomain.WorkspaceAnalysisOperationCitationValidation:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "ValidateCitation", Version: 3}, nil
	default:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{}, errors.New("workspace analysis timeline Tool kind is invalid")
	}
}

func workspaceAnalysisTimelineOperationOrder(contract agentdomain.WorkspaceAnalysisOperationContract) int {
	switch contract.Kind {
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		return 2
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		return contract.Ordinal
	default:
		return 1
	}
}
