package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const workspaceAnalysisTimelineRunSQL = `
SELECT r.id::text,r.workspace_id::text,r.answer_id::text,r.workflow_run_id::text,
       r.status,r.termination_reason,
	       (r.reserved_model_calls+r.settled_model_calls)::bigint,
	       (r.reserved_tool_calls+r.settled_tool_calls)::bigint,
	       (r.reserved_source_reads+r.settled_source_reads)::bigint,
	       (r.reserved_input_tokens+r.settled_input_tokens)::bigint,
	       (r.reserved_output_tokens+r.settled_output_tokens)::bigint,
	       r.max_model_calls::bigint,r.max_tool_calls::bigint,r.max_source_reads::bigint,r.max_input_tokens,r.max_output_tokens,
       CASE WHEN r.max_cost_microunits IS NULL THEN NULL
            ELSE COALESCE(r.reserved_cost_microunits,0)+COALESCE(r.settled_cost_microunits,0) END,
       r.max_cost_microunits,
       COALESCE((SELECT MAX(event.seq) FROM ops.server_event AS event WHERE event.workspace_id=r.workspace_id),0)
FROM agent.workspace_analysis_run AS r
JOIN agent.answer AS answer
  ON answer.id=r.answer_id AND answer.workspace_id=r.workspace_id AND answer.workflow_run_id=r.workflow_run_id
JOIN workflow.run AS workflow_run
  ON workflow_run.id=r.workflow_run_id AND workflow_run.workspace_id=r.workspace_id
WHERE r.workspace_id=$1 AND r.answer_id=$2`

const workspaceAnalysisTimelineNodesSQL = `
SELECT node.node_key,node.status,
       CASE WHEN node.completed_at IS NULL THEN NULL
            ELSE floor(extract(epoch FROM (node.completed_at-COALESCE(attempt.first_started_at,node.created_at)))*1000)::bigint END
FROM workflow.node_run AS node
LEFT JOIN LATERAL (
    SELECT MIN(node_attempt.started_at) AS first_started_at
    FROM workflow.node_attempt AS node_attempt
    WHERE node_attempt.node_run_id=node.id
) AS attempt ON true
WHERE node.run_id=$1
ORDER BY node.node_key`

const workspaceAnalysisTimelineOperationsSQL = `
SELECT operation.node_key,operation.operation_kind,operation.ordinal,operation.call_kind,operation.status,
       CASE WHEN operation.completed_at IS NULL OR operation.started_at IS NULL THEN NULL
            ELSE floor(extract(epoch FROM (operation.completed_at-operation.started_at))*1000)::bigint END,
       operation.error_code,
       model_call.status,model_call.input_tokens,model_call.output_tokens,
       tool_call.status,tool_call.requested_tool_name,tool_call.tool_version,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN receipt.document->>'branch' END,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN receipt.document->>'head' END,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN (receipt.document->>'clean')::boolean END,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN (receipt.document->>'staged_count')::integer END,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN (receipt.document->>'unstaged_count')::integer END,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN (receipt.document->>'untracked_count')::integer END,
       CASE WHEN operation.operation_kind='GIT_STATUS' THEN (receipt.document->>'conflict_count')::integer END,
       CASE WHEN operation.operation_kind='KNOWLEDGE_SEARCH' THEN jsonb_array_length(receipt.document->'items') END,
       CASE WHEN operation.operation_kind='KNOWLEDGE_SEARCH' THEN (
           SELECT COALESCE(jsonb_agg(code ORDER BY code),'[]'::jsonb)::text
           FROM jsonb_array_elements_text(receipt.document->'degradations') AS degradation(code)
       ) END,
       CASE WHEN operation.operation_kind='SOURCE_READ' THEN receipt.document->>'evidence_ref' END,
       CASE WHEN operation.operation_kind='SOURCE_READ' THEN receipt.document->>'content_hash' END,
       CASE WHEN operation.operation_kind='SOURCE_READ' THEN (receipt.document->>'truncated')::boolean END,
       CASE WHEN operation.operation_kind='CITATION_VALIDATION' THEN (
           SELECT count(*)::bigint FROM jsonb_array_elements(receipt.document->'results') AS result(value)
           WHERE (result.value->>'valid')::boolean
       ) END,
       CASE WHEN operation.operation_kind='CITATION_VALIDATION' THEN (
           SELECT count(*)::bigint FROM jsonb_array_elements(receipt.document->'results') AS result(value)
           WHERE NOT (result.value->>'valid')::boolean
       ) END,
       CASE WHEN operation.operation_kind='CITATION_VALIDATION' THEN (
           SELECT COALESCE(jsonb_agg(reason.code ORDER BY reason.code),'[]'::jsonb)::text
           FROM (
               SELECT DISTINCT result.value->>'reason_code' AS code
               FROM jsonb_array_elements(receipt.document->'results') AS result(value)
           ) AS reason
       ) END
FROM agent.workspace_analysis_operation AS operation
LEFT JOIN agent.model_call AS model_call ON model_call.id=operation.model_call_id
LEFT JOIN workflow.tool_call AS tool_call
  ON tool_call.id=operation.tool_call_id
 AND tool_call.workspace_id=operation.workspace_id
 AND tool_call.workflow_run_id=operation.workflow_run_id
 AND tool_call.node_run_id=operation.node_run_id
LEFT JOIN LATERAL (
    SELECT convert_from(result_receipt.output_document,'UTF8')::jsonb AS document
    FROM workflow.tool_result_receipt AS result_receipt
    WHERE result_receipt.id=operation.result_id
      AND result_receipt.tool_call_id=operation.tool_call_id
      AND result_receipt.workspace_id=operation.workspace_id
      AND result_receipt.workflow_run_id=operation.workflow_run_id
      AND result_receipt.node_run_id=operation.node_run_id
) AS receipt ON operation.status='SUCCEEDED' AND operation.call_kind='TOOL'
WHERE operation.analysis_run_id=$1 AND operation.workspace_id=$2 AND operation.workflow_run_id=$3
ORDER BY operation.node_key,operation.operation_kind,operation.ordinal`

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

// GetWorkspaceAnalysisTimeline 在一个只读一致快照内构造 Answer-scoped 脱敏时间线。
func (repository *Repository) GetWorkspaceAnalysisTimeline(
	ctx context.Context,
	query conversationapplication.WorkspaceAnalysisTimelineQuery,
) (conversationdomain.WorkspaceAnalysisTimeline, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationdomain.WorkspaceAnalysisTimeline{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("workspace analysis timeline repository is unavailable"))
	}
	if ctx == nil || !validCanonicalID(query.WorkspaceID) || !validCanonicalID(query.AnswerID) || query.WorkspaceID == query.AnswerID {
		return conversationdomain.WorkspaceAnalysisTimeline{}, invalid(ErrorCodePersistenceInvalid, errors.New("workspace analysis timeline query is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, classify(err, ErrorCodeDatabaseUnavailable)
	}

	run, err := loadWorkspaceAnalysisTimelineRun(ctx, tx, query)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, err
	}
	items, err := loadWorkspaceAnalysisTimelineItems(ctx, tx, run)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, err
	}
	timeline := workspaceAnalysisTimelineFromRows(run, items)
	if err := timeline.Validate(); err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return timeline, nil
}

func loadWorkspaceAnalysisTimelineRun(
	ctx context.Context,
	tx pgx.Tx,
	query conversationapplication.WorkspaceAnalysisTimelineQuery,
) (workspaceAnalysisTimelineRunRow, error) {
	var row workspaceAnalysisTimelineRunRow
	var analysisRunID, workspaceID, answerID, workflowRunID string
	err := tx.QueryRow(ctx, workspaceAnalysisTimelineRunSQL, string(query.WorkspaceID), string(query.AnswerID)).Scan(
		&analysisRunID, &workspaceID, &answerID, &workflowRunID, &row.status, &row.terminationReason,
		&row.modelCalls, &row.toolCalls, &row.sourceReads, &row.inputTokens, &row.outputTokens,
		&row.maxModelCalls, &row.maxToolCalls, &row.maxSourceReads, &row.maxInputTokens, &row.maxOutputTokens,
		&row.cost, &row.maxCost, &row.latestServerEventSequence,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return workspaceAnalysisTimelineRunRow{}, notFound(ErrorCodeAnswerNotFound, err)
	}
	if err != nil {
		return workspaceAnalysisTimelineRunRow{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	ids := []*foundation.ID{&row.analysisRunID, &row.workspaceID, &row.answerID, &row.workflowRunID}
	for index, value := range []string{analysisRunID, workspaceID, answerID, workflowRunID} {
		parsed, parseErr := parseCanonicalID(value)
		if parseErr != nil {
			return workspaceAnalysisTimelineRunRow{}, consistency(ErrorCodePersistenceCorrupt, parseErr)
		}
		*ids[index] = parsed
	}
	if row.workspaceID != query.WorkspaceID || row.answerID != query.AnswerID || row.analysisRunID == row.answerID ||
		row.analysisRunID == row.workspaceID || row.latestServerEventSequence < 0 {
		return workspaceAnalysisTimelineRunRow{}, consistency(ErrorCodePersistenceCorrupt, errors.New("workspace analysis timeline run scope is inconsistent"))
	}
	return row, nil
}

func loadWorkspaceAnalysisTimelineItems(
	ctx context.Context,
	tx pgx.Tx,
	run workspaceAnalysisTimelineRunRow,
) ([]workspaceAnalysisTimelineSortableItem, error) {
	nodes, err := loadWorkspaceAnalysisTimelineNodes(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	operations, err := loadWorkspaceAnalysisTimelineOperations(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	items := append(nodes, operations...)
	if len(items) > conversationdomain.MaxWorkspaceAnalysisTimelineItems {
		return nil, consistency(ErrorCodePersistenceCorrupt, errors.New("workspace analysis timeline exceeds its item limit"))
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].phaseOrder != items[right].phaseOrder {
			return items[left].phaseOrder < items[right].phaseOrder
		}
		return items[left].itemOrder < items[right].itemOrder
	})
	for index := range items {
		items[index].item.Sequence = index + 1
	}
	return items, nil
}

func loadWorkspaceAnalysisTimelineNodes(
	ctx context.Context,
	tx pgx.Tx,
	run workspaceAnalysisTimelineRunRow,
) ([]workspaceAnalysisTimelineSortableItem, error) {
	rows, err := tx.Query(ctx, workspaceAnalysisTimelineNodesSQL, string(run.workflowRunID))
	if err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	items := make([]workspaceAnalysisTimelineSortableItem, 0, agentdomain.WorkspaceAnalysisV1MaxNodes)
	seen := make(map[conversationdomain.WorkspaceAnalysisPhase]struct{}, agentdomain.WorkspaceAnalysisV1MaxNodes)
	for rows.Next() {
		var nodeKey, status string
		var durationMS *int64
		if err := rows.Scan(&nodeKey, &status, &durationMS); err != nil {
			return nil, consistency(ErrorCodePersistenceCorrupt, err)
		}
		phase, phaseOrder, ok := workspaceAnalysisTimelinePhase(nodeKey)
		if !ok {
			return nil, consistency(ErrorCodePersistenceCorrupt, errors.New("workspace analysis timeline contains an unknown node"))
		}
		if _, duplicate := seen[phase]; duplicate {
			return nil, consistency(ErrorCodePersistenceCorrupt, errors.New("workspace analysis timeline contains a duplicate node"))
		}
		seen[phase] = struct{}{}
		itemStatus, code, mapErr := workspaceAnalysisTimelineNodeStatus(status, run.status, run.terminationReason)
		if mapErr != nil {
			return nil, consistency(ErrorCodePersistenceCorrupt, mapErr)
		}
		items = append(items, workspaceAnalysisTimelineSortableItem{phaseOrder: phaseOrder, itemOrder: 0, item: conversationdomain.WorkspaceAnalysisTimelineItem{
			Kind: conversationdomain.WorkspaceAnalysisTimelineItemNode, Phase: phase, Status: itemStatus,
			DurationMS: durationMS, ErrorCode: code,
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	if len(items) > agentdomain.WorkspaceAnalysisV1MaxNodes {
		return nil, consistency(ErrorCodePersistenceCorrupt, errors.New("workspace analysis timeline contains too many nodes"))
	}
	return items, nil
}

func loadWorkspaceAnalysisTimelineOperations(
	ctx context.Context,
	tx pgx.Tx,
	run workspaceAnalysisTimelineRunRow,
) ([]workspaceAnalysisTimelineSortableItem, error) {
	rows, err := tx.Query(ctx, workspaceAnalysisTimelineOperationsSQL, string(run.analysisRunID), string(run.workspaceID), string(run.workflowRunID))
	if err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	items := make([]workspaceAnalysisTimelineSortableItem, 0, len(agentdomain.WorkspaceAnalysisV1OperationContracts()))
	for rows.Next() {
		row, scanErr := scanWorkspaceAnalysisTimelineOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		item, phaseOrder, itemOrder, buildErr := workspaceAnalysisTimelineOperationItem(run, row)
		if buildErr != nil {
			return nil, consistency(ErrorCodePersistenceCorrupt, buildErr)
		}
		items = append(items, workspaceAnalysisTimelineSortableItem{phaseOrder: phaseOrder, itemOrder: itemOrder, item: item})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	if len(items) > len(agentdomain.WorkspaceAnalysisV1OperationContracts()) {
		return nil, consistency(ErrorCodePersistenceCorrupt, errors.New("workspace analysis timeline contains too many operations"))
	}
	return items, nil
}

func scanWorkspaceAnalysisTimelineOperation(row pgx.Row) (workspaceAnalysisTimelineOperationRow, error) {
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

var _ conversationapplication.WorkspaceAnalysisTimelineReader = (*Repository)(nil)
