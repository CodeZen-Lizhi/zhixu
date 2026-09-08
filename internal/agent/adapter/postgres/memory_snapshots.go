package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func scanRAGMemorySnapshot(row rowScanner) (domain.RAGMemorySnapshot, error) {
	var snapshot domain.RAGMemorySnapshot
	var id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID, claimantID, ownerID, taskScopeID string
	var status string
	var schemaVersion, digest, modelRunID, errorCode *string
	var itemCount *int
	var byteCount *int64
	if err := row.Scan(
		&id, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &claimantID,
		&snapshot.OwnerKind, &ownerID, &taskScopeID, &status, &schemaVersion, &digest, &itemCount, &byteCount,
		&modelRunID, &errorCode, &snapshot.CreatedAt, &snapshot.UpdatedAt,
	); err != nil {
		return domain.RAGMemorySnapshot{}, err
	}
	snapshot.ID, snapshot.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	snapshot.WorkflowRunID, snapshot.NodeRunID = foundation.ID(workflowRunID), foundation.ID(nodeRunID)
	snapshot.NodeAttemptID, snapshot.ClaimantID = foundation.ID(nodeAttemptID), foundation.ID(claimantID)
	snapshot.OwnerID, snapshot.TaskScopeID = foundation.ID(ownerID), foundation.ID(taskScopeID)
	snapshot.Status = domain.RAGMemorySnapshotStatus(status)
	if schemaVersion != nil {
		snapshot.Context.SchemaVersion = *schemaVersion
		snapshot.Context.SnapshotID = snapshot.ID
	}
	if digest != nil {
		snapshot.Context.Digest = *digest
	}
	if itemCount != nil {
		snapshot.Context.ItemCount = *itemCount
	}
	if byteCount != nil {
		snapshot.Context.ByteCount = *byteCount
	}
	if modelRunID != nil {
		snapshot.ModelRunID = foundation.ID(*modelRunID)
	}
	if errorCode != nil {
		snapshot.ErrorCode = *errorCode
	}
	if err := domain.ValidateRAGMemorySnapshot(snapshot); err != nil {
		return domain.RAGMemorySnapshot{}, consistency(err)
	}
	return snapshot, nil
}

func sameRAGMemorySnapshotExecution(left, right domain.RAGMemorySnapshot) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.NodeAttemptID == right.NodeAttemptID &&
		left.OwnerKind == right.OwnerKind && left.OwnerID == right.OwnerID && left.TaskScopeID == right.TaskScopeID
}

func validStableErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

const ragMemorySnapshotColumns = `
	id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,claimant_id::text,
	owner_kind,owner_id::text,task_scope_id::text,status,context_schema_version,context_digest,context_item_count,context_bytes,
	model_run_id::text,error_code,created_at,updated_at`
