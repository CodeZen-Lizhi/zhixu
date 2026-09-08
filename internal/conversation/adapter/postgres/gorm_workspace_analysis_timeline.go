package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

const gormWorkspaceAnalysisTimelineRunSQL = `
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
WHERE r.workspace_id=? AND r.answer_id=?`

const gormWorkspaceAnalysisTimelineNodesSQL = `
SELECT node.node_key,node.status,
       CASE WHEN node.completed_at IS NULL THEN NULL
            ELSE floor(extract(epoch FROM (node.completed_at-COALESCE(attempt.first_started_at,node.created_at)))*1000)::bigint END
FROM workflow.node_run AS node
LEFT JOIN LATERAL (
    SELECT MIN(node_attempt.started_at) AS first_started_at
    FROM workflow.node_attempt AS node_attempt
    WHERE node_attempt.node_run_id=node.id
) AS attempt ON true
WHERE node.run_id=?
ORDER BY node.node_key`

const gormWorkspaceAnalysisTimelineOperationsSQL = `
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
WHERE operation.analysis_run_id=? AND operation.workspace_id=? AND operation.workflow_run_id=?
ORDER BY operation.node_key,operation.operation_kind,operation.ordinal`

// GetWorkspaceAnalysisTimeline 在只读一致快照中构造脱敏时间线。
func (repository *GORMRepository) GetWorkspaceAnalysisTimeline(ctx context.Context, query conversationapplication.WorkspaceAnalysisTimelineQuery) (conversationdomain.WorkspaceAnalysisTimeline, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return conversationdomain.WorkspaceAnalysisTimeline{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("workspace analysis timeline repository is unavailable"))
	}
	if ctx == nil || !validCanonicalID(query.WorkspaceID) || !validCanonicalID(query.AnswerID) || query.WorkspaceID == query.AnswerID {
		return conversationdomain.WorkspaceAnalysisTimeline{}, invalid(ErrorCodePersistenceInvalid, errors.New("workspace analysis timeline query is invalid"))
	}
	var timeline conversationdomain.WorkspaceAnalysisTimeline
	options := foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}
	err := withinConversationTransaction(ctx, repository.uow, options, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		run, err := gormLoadWorkspaceAnalysisTimelineRun(ctx, tx, query)
		if err != nil {
			return err
		}
		items, err := gormLoadWorkspaceAnalysisTimelineItems(ctx, tx, run)
		if err != nil {
			return err
		}
		timeline = workspaceAnalysisTimelineFromRows(run, items)
		if err := timeline.Validate(); err != nil {
			return consistency(ErrorCodePersistenceCorrupt, err)
		}
		return nil
	})
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return timeline, nil
}

func gormLoadWorkspaceAnalysisTimelineRun(
	ctx context.Context,
	tx *gorm.DB,
	query conversationapplication.WorkspaceAnalysisTimelineQuery,
) (workspaceAnalysisTimelineRunRow, error) {
	var row workspaceAnalysisTimelineRunRow
	var analysisRunID, workspaceID, answerID, workflowRunID string
	err := gormScanRow(tx.WithContext(ctx).Raw(gormWorkspaceAnalysisTimelineRunSQL, string(query.WorkspaceID), string(query.AnswerID))).Scan(
		&analysisRunID, &workspaceID, &answerID, &workflowRunID, &row.status, &row.terminationReason,
		&row.modelCalls, &row.toolCalls, &row.sourceReads, &row.inputTokens, &row.outputTokens,
		&row.maxModelCalls, &row.maxToolCalls, &row.maxSourceReads, &row.maxInputTokens, &row.maxOutputTokens,
		&row.cost, &row.maxCost, &row.latestServerEventSequence,
	)
	if errors.Is(err, sql.ErrNoRows) {
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

func gormLoadWorkspaceAnalysisTimelineItems(
	ctx context.Context,
	tx *gorm.DB,
	run workspaceAnalysisTimelineRunRow,
) ([]workspaceAnalysisTimelineSortableItem, error) {
	nodes, err := gormLoadWorkspaceAnalysisTimelineNodes(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	operations, err := gormLoadWorkspaceAnalysisTimelineOperations(ctx, tx, run)
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

func gormLoadWorkspaceAnalysisTimelineNodes(
	ctx context.Context,
	tx *gorm.DB,
	run workspaceAnalysisTimelineRunRow,
) ([]workspaceAnalysisTimelineSortableItem, error) {
	rows, err := tx.WithContext(ctx).Raw(gormWorkspaceAnalysisTimelineNodesSQL, string(run.workflowRunID)).Rows()
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

func gormLoadWorkspaceAnalysisTimelineOperations(
	ctx context.Context,
	tx *gorm.DB,
	run workspaceAnalysisTimelineRunRow,
) ([]workspaceAnalysisTimelineSortableItem, error) {
	rows, err := tx.WithContext(ctx).Raw(gormWorkspaceAnalysisTimelineOperationsSQL, string(run.analysisRunID), string(run.workspaceID), string(run.workflowRunID)).Rows()
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

var _ conversationapplication.WorkspaceAnalysisTimelineReader = (*GORMRepository)(nil)
