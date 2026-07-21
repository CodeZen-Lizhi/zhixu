package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
)

// SemanticLinkScanCancellationGuard 在 Workflow cancel 事务内同步 Scan 终态。
type SemanticLinkScanCancellationGuard struct{}

var _ workflowapplication.CancellationSafetyGuard = (*SemanticLinkScanCancellationGuard)(nil)

// NewSemanticLinkScanCancellationGuard 创建持久 Topic scan 的取消同步边界。
func NewSemanticLinkScanCancellationGuard() *SemanticLinkScanCancellationGuard {
	return &SemanticLinkScanCancellationGuard{}
}

// SafeToCancelWorkflowNode 将运行中的 Scan 原子推进为 CANCELLED；已完成 Scan 拒绝迟到取消。
func (guard *SemanticLinkScanCancellationGuard) SafeToCancelWorkflowNode(ctx context.Context, transaction any, nodeRunID foundation.ID) (bool, error) {
	if guard == nil {
		return false, scanRepositoryUnavailable(errors.New("semantic link scan cancellation guard is unavailable"))
	}
	parsed, err := foundation.ParseID(string(nodeRunID))
	if err != nil || parsed != nodeRunID {
		return false, scanRepositoryInvalid(errors.New("semantic link scan cancellation node is invalid"))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil {
		return false, scanRepositoryInvalid(errors.New("semantic link scan cancellation transaction is invalid"))
	}
	var scanID, workspaceID string
	var status graphdomain.SemanticLinkScanStatus
	var version int64
	err = tx.QueryRow(ctx, `
		SELECT scan.id::text,scan.workspace_id::text,scan.status,scan.version
		FROM graph.semantic_link_scan scan
		JOIN workflow.node_run node ON node.run_id=scan.workflow_run_id
		WHERE node.id=$1
		FOR UPDATE OF scan`, string(nodeRunID)).Scan(&scanID, &workspaceID, &status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_QUERY_FAILED")
	}
	switch status {
	case graphdomain.SemanticLinkScanStatusPending, graphdomain.SemanticLinkScanStatusRunning:
		var updatedStatus graphdomain.SemanticLinkScanStatus
		if err := tx.QueryRow(ctx, `
			UPDATE graph.semantic_link_scan
			SET status='CANCELLED',last_error=NULL,version=version+1,
			    updated_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP
			WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
			RETURNING status`, scanID, workspaceID, version).Scan(&updatedStatus); err != nil {
			return false, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_FAILED")
		}
		return updatedStatus == graphdomain.SemanticLinkScanStatusCancelled, nil
	case graphdomain.SemanticLinkScanStatusCancelled:
		return true, nil
	case graphdomain.SemanticLinkScanStatusSucceeded, graphdomain.SemanticLinkScanStatusFailed:
		return false, foundation.NewError(foundation.ErrorVersionConflict, "GRAPH_SEMANTIC_LINK_SCAN_ALREADY_TERMINAL", false, errors.New("semantic link scan completed before cancellation"))
	default:
		return false, scanRepositoryConsistency(errors.New("semantic link scan cancellation observed an invalid status"))
	}
}
