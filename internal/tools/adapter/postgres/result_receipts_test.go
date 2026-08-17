package postgres

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestActiveReceiptExecutionFenceRequiresExactLeaseIdentity(t *testing.T) {
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	lease := now.Add(time.Minute)
	owner := "worker-1"
	attemptID := "90000000-0000-4000-8000-000000000005"
	call := domain.ToolCall{
		WorkspaceID:   "90000000-0000-4000-8000-000000000001",
		WorkflowRunID: "90000000-0000-4000-8000-000000000002",
		NodeRunID:     "90000000-0000-4000-8000-000000000003",
		NodeAttemptID: foundation.ID(attemptID),
	}
	identity := domain.TrustedExecutionIdentity{
		WorkspaceID: call.WorkspaceID, DefinitionID: "90000000-0000-4000-8000-000000000007",
		DefinitionVersion: 1, DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkflowRunID: call.WorkflowRunID,
		NodeRunID:     call.NodeRunID, NodeAttemptID: call.NodeAttemptID,
		NodeKey: "inspect_workspace", LeaseOwner: owner, LeaseFence: 2,
	}
	locked := receiptCompletionLocks{
		fence: receiptExecutionFence{
			workflowDefinitionID: identity.DefinitionID, workflowStatus: "running", databaseNow: now,
			nodeKey: identity.NodeKey, nodeStatus: "running",
			nodeAttempt: identity.LeaseFence, nodeOwner: &owner, nodeLease: &lease,
			attemptStatus: "running", attemptNo: identity.LeaseFence, attemptOwner: &owner, attemptLease: &lease,
		},
		analysisRun: receiptAnalysisRun{
			status: "running", definitionVersion: identity.DefinitionVersion, definitionHash: identity.DefinitionHash,
		},
		operation: receiptOperation{nodeKey: identity.NodeKey, latestAttemptID: &attemptID},
	}
	if !activeReceiptExecutionFence(locked, call, identity) {
		t.Fatal("exact active receipt fence was rejected")
	}

	staleOwner := identity
	staleOwner.LeaseOwner = "worker-0"
	if activeReceiptExecutionFence(locked, call, staleOwner) {
		t.Fatal("stale lease owner was accepted")
	}
	staleFence := identity
	staleFence.LeaseFence--
	if activeReceiptExecutionFence(locked, call, staleFence) {
		t.Fatal("stale lease fence was accepted")
	}
	otherAttempt := identity
	otherAttempt.NodeAttemptID = "90000000-0000-4000-8000-000000000006"
	if activeReceiptExecutionFence(locked, call, otherAttempt) {
		t.Fatal("different node attempt was accepted")
	}
	otherDefinition := identity
	otherDefinition.DefinitionID = "90000000-0000-4000-8000-000000000008"
	if activeReceiptExecutionFence(locked, call, otherDefinition) {
		t.Fatal("different workflow definition was accepted")
	}
	otherDefinition = identity
	otherDefinition.DefinitionVersion++
	if activeReceiptExecutionFence(locked, call, otherDefinition) {
		t.Fatal("different workflow definition version was accepted")
	}
	otherDefinition = identity
	otherDefinition.DefinitionHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if activeReceiptExecutionFence(locked, call, otherDefinition) {
		t.Fatal("different workflow definition hash was accepted")
	}
}
