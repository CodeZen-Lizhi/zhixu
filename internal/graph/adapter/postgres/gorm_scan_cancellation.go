package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

var _ workflowapplication.ScopedCancellationSafetyGuard = (*GORMRepository)(nil)

// SafeToCancelWorkflowNodeScoped synchronizes a running Scan with Workflow's
// caller-owned cancellation transaction. Workflow locks the node before this
// method locks the Scan; this method never calls back into Workflow.
func (repository *GORMRepository) SafeToCancelWorkflowNodeScoped(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	if ctx == nil {
		return false, scanRepositoryInvalid(errors.New("semantic link GORM scan cancellation context is nil"))
	}
	parsed, err := foundation.ParseID(string(nodeRunID))
	if err != nil || parsed != nodeRunID {
		return false, scanRepositoryInvalid(errors.New("semantic link scan cancellation node is invalid"))
	}
	if err := repository.ready(ctx); err != nil {
		return false, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_QUERY_FAILED")
	}
	row, err := gormRawRow(ctx, transaction, `
		SELECT scan.id::text,scan.workspace_id::text,scan.status,scan.version
		FROM graph.semantic_link_scan scan
		JOIN workflow.node_run node ON node.run_id=scan.workflow_run_id
		WHERE node.id=$1
		FOR UPDATE OF scan`, string(nodeRunID))
	if err != nil {
		return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_QUERY_FAILED")
	}

	var scanID, workspaceID string
	var status graphdomain.SemanticLinkScanStatus
	var version int64
	err = row.Scan(&scanID, &workspaceID, &status, &version)
	if gormGraphNoRows(err) {
		return true, nil
	}
	if err != nil {
		return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_QUERY_FAILED")
	}

	switch status {
	case graphdomain.SemanticLinkScanStatusPending, graphdomain.SemanticLinkScanStatusRunning:
		var updatedStatus graphdomain.SemanticLinkScanStatus
		row, err := gormRawRow(ctx, transaction, `
			UPDATE graph.semantic_link_scan
			SET status='CANCELLED',last_error=NULL,version=version+1,
			    updated_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP
			WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
			RETURNING status`, scanID, workspaceID, version)
		if err != nil {
			return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_FAILED")
		}
		if err := row.Scan(&updatedStatus); err != nil {
			return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CANCEL_FAILED")
		}
		if updatedStatus != graphdomain.SemanticLinkScanStatusCancelled {
			return false, scanRepositoryConsistency(errors.New("semantic link scan cancellation returned an invalid status"))
		}
		return true, nil
	case graphdomain.SemanticLinkScanStatusCancelled:
		return true, nil
	case graphdomain.SemanticLinkScanStatusSucceeded, graphdomain.SemanticLinkScanStatusFailed:
		return false, foundation.NewError(
			foundation.ErrorVersionConflict,
			"GRAPH_SEMANTIC_LINK_SCAN_ALREADY_TERMINAL",
			false,
			errors.New("semantic link scan completed before cancellation"),
		)
	default:
		return false, scanRepositoryConsistency(errors.New("semantic link scan cancellation observed an invalid status"))
	}
}
