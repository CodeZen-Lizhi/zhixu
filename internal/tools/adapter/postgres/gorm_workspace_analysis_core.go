package postgres

import (
	"context"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func gormWorkspaceAnalysisIdentity(identity domain.TrustedExecutionIdentity) agentapplication.WorkspaceAnalysisToolExecutionIdentity {
	return agentapplication.WorkspaceAnalysisToolExecutionIdentity{
		WorkspaceID: identity.WorkspaceID, DefinitionID: identity.DefinitionID, DefinitionVersion: identity.DefinitionVersion,
		DefinitionHash: identity.DefinitionHash, WorkflowRunID: identity.WorkflowRunID,
		NodeKey: agentdomain.WorkspaceAnalysisOperationNodeKey(identity.NodeKey), NodeRunID: identity.NodeRunID,
		NodeAttemptID: identity.NodeAttemptID, LeaseOwner: identity.LeaseOwner, LeaseFence: identity.LeaseFence,
	}
}

func (repository *GORMWorkspaceAnalysisRepository) lockExecution(ctx context.Context, scope foundation.TransactionScope, identity domain.TrustedExecutionIdentity) (workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot, agentapplication.WorkspaceAnalysisToolExecutionIdentity, error) {
	if err := identity.Validate(); err != nil {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, err
	}
	snapshot, found, err := repository.executionFence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, workflowapplication.WorkspaceAnalysisExecutionFenceRequest{
		WorkspaceID: identity.WorkspaceID, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID, NodeAttemptID: identity.NodeAttemptID,
	})
	if err != nil {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, classifyGORMTools(ctx, err)
	}
	if !found || snapshot.DefinitionID != identity.DefinitionID || snapshot.DefinitionVersion != identity.DefinitionVersion ||
		snapshot.WorkspaceID != identity.WorkspaceID || snapshot.WorkflowRunID != identity.WorkflowRunID ||
		snapshot.NodeRunID != identity.NodeRunID || snapshot.NodeAttemptID != identity.NodeAttemptID || snapshot.NodeKey != identity.NodeKey {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, stale(errors.New("Workspace Analysis execution fence is stale"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, gormToolsUnavailable(err)
	}
	row, err := gormToolsRawRow(transaction.WithContext(ctx), "SELECT clock_timestamp()")
	if err != nil {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, classifyGORMTools(ctx, err)
	}
	var databaseNow time.Time
	if err := row.Scan(&databaseNow); err != nil {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, classifyGORMTools(ctx, err)
	}
	if !activeGORMWorkspaceAnalysisFence(snapshot, identity, databaseNow.UTC()) {
		return workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot{}, agentapplication.WorkspaceAnalysisToolExecutionIdentity{}, stale(errors.New("Workspace Analysis execution fence is inactive"))
	}
	return snapshot, gormWorkspaceAnalysisIdentity(identity), nil
}

func activeGORMWorkspaceAnalysisFence(
	snapshot workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot,
	identity domain.TrustedExecutionIdentity,
	databaseNow time.Time,
) bool {
	return snapshot.WorkflowStatus == workflowdomain.RunStatusRunning &&
		!snapshot.PauseRequested && !snapshot.CancelRequested &&
		snapshot.NodeStatus == workflowdomain.NodeStatusRunning &&
		snapshot.AttemptStatus == workflowdomain.AttemptStatusRunning &&
		snapshot.NodeAttempt == snapshot.AttemptNo && int64(snapshot.AttemptNo) == identity.LeaseFence &&
		snapshot.NodeLeaseOwnerSet && snapshot.AttemptLeaseOwnerSet &&
		snapshot.NodeLeaseUntilSet && snapshot.AttemptLeaseUntilSet &&
		snapshot.NodeLeaseOwner == identity.LeaseOwner && snapshot.AttemptLeaseOwner == identity.LeaseOwner &&
		snapshot.NodeLeaseUntil.Equal(snapshot.AttemptLeaseUntil) && snapshot.AttemptLeaseUntil.After(databaseNow)
}

func gormWorkspaceAnalysisPolicyFromFence(
	snapshot workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot,
	identity domain.TrustedExecutionIdentity,
) (toolsapplication.WorkflowToolPolicy, error) {
	graph, err := workflowapplication.DecodeCanonicalGraph([]byte(snapshot.DefinitionGraph))
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, consistency(err)
	}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, consistency(err)
	}
	if graphHash != identity.DefinitionHash {
		return toolsapplication.WorkflowToolPolicy{}, stale(errors.New("Workspace Analysis Workflow graph hash changed"))
	}
	node, err := exactPolicyNode(graph, identity.NodeKey)
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, err
	}
	return toolsapplication.WorkflowToolPolicy{
		Identity: identity, WorkflowKey: snapshot.DefinitionKey, NodeKind: node.Kind,
		Permissions:    append([]capability.Capability(nil), node.RequiredPermissions...),
		AllowedTools:   append([]domain.ToolRef(nil), node.AllowedTools...),
		AttemptLeaseTo: snapshot.AttemptLeaseUntil,
	}, nil
}
