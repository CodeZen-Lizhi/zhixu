package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// RecoverStaleStarted asks the Workflow owner to lock and judge each bounded
// Tool-owned candidate before taking the Tool Call lock and performing the CAS.
func (repository *GORMRepository) RecoverStaleStarted(ctx context.Context, limit int) ([]domain.ToolCall, error) {
	if limit < 1 || limit > application.MaxToolStaleRecoveryLimit {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool stale recovery limit is invalid"))
	}
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	// Serialize the scheduler cursor so concurrent recovery workers make
	// forward progress instead of repeatedly inspecting the same page.
	repository.recoveryMu.Lock()
	defer repository.recoveryMu.Unlock()
	startCursor := cloneGORMStaleRecoveryCandidate(repository.recoveryCursor)
	result := make([]domain.ToolCall, 0, limit)
	var nextCursor *gormStaleRecoveryCandidate
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		cursor := cloneGORMStaleRecoveryCandidate(startCursor)
		wrapped := startCursor == nil
		inspected := 0
		for len(result) < limit && inspected < application.MaxToolStaleRecoveryLimit {
			remaining := application.MaxToolStaleRecoveryLimit - inspected
			upperCursor := (*gormStaleRecoveryCandidate)(nil)
			if wrapped {
				upperCursor = startCursor
			}
			candidates, queryErr := gormStaleRecoveryCandidates(callbackCtx, database, cursor, upperCursor, remaining)
			if queryErr != nil {
				return queryErr
			}
			if len(candidates) == 0 {
				if !wrapped {
					cursor = nil
					wrapped = true
					continue
				}
				break
			}
			for index := range candidates {
				candidate := candidates[index]
				cursor = &candidate
				inspected++
				fence, fenceErr := repository.recoveryFence.LockToolCallRecoveryScoped(callbackCtx, scope, workflowapplication.ToolCallRecoveryFenceRequest{
					WorkspaceID: candidate.workspaceID, WorkflowRunID: candidate.workflowRunID,
					NodeRunID: candidate.nodeRunID, NodeAttemptID: candidate.nodeAttemptID,
				})
				if fenceErr != nil {
					return classifyGORMTools(callbackCtx, fenceErr)
				}
				if !fence.Found || fence.Skipped || !fence.Stale {
					continue
				}
				started, lockErr := gormScanToolCallQuery(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call
					WHERE id=? AND workspace_id=? AND status='STARTED' AND side_effect_type IS DISTINCT FROM 'writeback_execution'
					FOR UPDATE SKIP LOCKED`, string(candidate.callID), string(candidate.workspaceID))
				if gormToolsNoRows(lockErr) {
					continue
				}
				if lockErr != nil {
					return classifyGORMTools(callbackCtx, lockErr)
				}
				if started.WorkflowRunID != candidate.workflowRunID || started.NodeRunID != candidate.nodeRunID || started.NodeAttemptID != candidate.nodeAttemptID {
					return consistency(errors.New("stale recovery candidate binding changed"))
				}
				recovered, updateErr := gormScanToolCallQuery(database, `UPDATE workflow.tool_call SET
					status='UNKNOWN',error_code='TOOL_OUTCOME_UNKNOWN',retryable=false,version=version+1,completed_at=clock_timestamp(),
					duration_ms=GREATEST(0,floor(extract(epoch FROM (clock_timestamp()-started_at))*1000)::bigint)
					WHERE id=? AND workspace_id=? AND status='STARTED' AND version=? AND side_effect_type IS DISTINCT FROM 'writeback_execution'
					RETURNING `+gormToolCallColumns, string(started.ID), string(started.WorkspaceID), started.Version)
				if gormToolsNoRows(updateErr) {
					continue
				}
				if updateErr != nil {
					return classifyGORMTools(callbackCtx, updateErr)
				}
				if recovered.Status != domain.CallUnknown || recovered.ErrorCode != "TOOL_OUTCOME_UNKNOWN" {
					return consistency(errors.New("stale recovery update returned an invalid terminal call"))
				}
				result = append(result, recovered)
				if len(result) == limit {
					break
				}
			}
			if len(candidates) < remaining {
				if !wrapped {
					cursor = nil
					wrapped = true
					continue
				}
				break
			}
		}
		if inspected == 0 {
			nextCursor = nil
		} else {
			nextCursor = cloneGORMStaleRecoveryCandidate(cursor)
		}
		return nil
	})
	if err != nil {
		return nil, classifyGORMTools(ctx, err)
	}
	repository.recoveryCursor = nextCursor
	sort.Slice(result, func(left, right int) bool { return timelineLess(result[left], result[right]) })
	return result, nil
}

type gormStaleRecoveryCandidate struct {
	callID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID foundation.ID
	startedAt                                                    time.Time
	callNo                                                       int
}

func cloneGORMStaleRecoveryCandidate(candidate *gormStaleRecoveryCandidate) *gormStaleRecoveryCandidate {
	if candidate == nil {
		return nil
	}
	clone := *candidate
	return &clone
}

func gormStaleRecoveryCandidates(ctx context.Context, database *gorm.DB, after, before *gormStaleRecoveryCandidate, limit int) ([]gormStaleRecoveryCandidate, error) {
	query := `SELECT id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,started_at,call_no
		FROM workflow.tool_call WHERE status='STARTED' AND side_effect_type IS DISTINCT FROM 'writeback_execution'`
	arguments := make([]any, 0, 7)
	if after != nil {
		query += ` AND (started_at,call_no,id)>(?,?,?::uuid)`
		arguments = append(arguments, after.startedAt, after.callNo, string(after.callID))
	}
	if before != nil {
		query += ` AND (started_at,call_no,id)<(?,?,?::uuid)`
		arguments = append(arguments, before.startedAt, before.callNo, string(before.callID))
	}
	query += ` ORDER BY started_at,call_no,id LIMIT ?`
	arguments = append(arguments, limit)
	rows, err := gormToolsRawRows(database, query, arguments...)
	if err != nil {
		return nil, classifyGORMTools(ctx, err)
	}
	result := make([]gormStaleRecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate gormStaleRecoveryCandidate
		var callID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
		if scanErr := rows.Scan(&callID, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &candidate.startedAt, &candidate.callNo); scanErr != nil {
			return nil, gormToolsCloseRows(ctx, rows, scanErr)
		}
		candidate.callID, candidate.workspaceID = foundation.ID(callID), foundation.ID(workspaceID)
		candidate.workflowRunID, candidate.nodeRunID, candidate.nodeAttemptID = foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, gormToolsCloseRows(ctx, rows, err)
	}
	if err := gormToolsCloseRows(ctx, rows, nil); err != nil {
		return nil, err
	}
	return result, nil
}
