package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// ResolveToolPolicy reads the Workflow-owned execution facts through its scoped
// port, then makes the Tools-owned admission projection without opening another
// transaction or querying Workflow tables directly.
func (repository *GORMRepository) ResolveToolPolicy(ctx context.Context, identity toolsdomain.TrustedExecutionIdentity) (toolsapplication.WorkflowToolPolicy, error) {
	if err := repository.ready(ctx); err != nil {
		return toolsapplication.WorkflowToolPolicy{}, err
	}
	if err := gormValidatePolicyIdentity(identity); err != nil {
		return toolsapplication.WorkflowToolPolicy{}, err
	}
	var result toolsapplication.WorkflowToolPolicy
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ *gorm.DB, scope foundation.TransactionScope) error {
		policy, err := repository.gormResolveToolPolicyScoped(callbackCtx, scope, identity)
		if err != nil {
			return err
		}
		result = policy
		return nil
	})
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, classifyGORMTools(ctx, err)
	}
	return clonePolicy(result), nil
}

func (repository *GORMRepository) gormResolveToolPolicyScoped(ctx context.Context, scope foundation.TransactionScope, identity toolsdomain.TrustedExecutionIdentity) (toolsapplication.WorkflowToolPolicy, error) {
	snapshot, found, err := repository.policySnapshot.LockToolExecutionPolicyScoped(ctx, scope, workflowapplication.ToolExecutionPolicySnapshotRequest{
		WorkspaceID: identity.WorkspaceID, WorkflowRunID: identity.WorkflowRunID,
		NodeRunID: identity.NodeRunID, NodeAttemptID: identity.NodeAttemptID,
	})
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, classifyGORMTools(ctx, err)
	}
	if !found {
		return toolsapplication.WorkflowToolPolicy{}, stale(errors.New("workflow Tool execution facts are absent or invalid"))
	}
	return gormToolPolicyFromSnapshot(snapshot, identity)
}

func gormValidatePolicyIdentity(identity toolsdomain.TrustedExecutionIdentity) error {
	if err := identity.Validate(); err != nil || identity.LeaseOwner != strings.TrimSpace(identity.LeaseOwner) {
		if err != nil {
			return err
		}
		return foundation.NewError(foundation.ErrorInvalidInput, toolsdomain.ErrorCodeExecutionIdentityInvalid, false, errors.New("tool lease owner is not canonical"))
	}
	return nil
}

func gormToolPolicyFromSnapshot(snapshot workflowapplication.ToolExecutionPolicySnapshot, identity toolsdomain.TrustedExecutionIdentity) (toolsapplication.WorkflowToolPolicy, error) {
	if snapshot.DefinitionID != identity.DefinitionID || snapshot.WorkspaceID != identity.WorkspaceID ||
		snapshot.WorkflowRunID != identity.WorkflowRunID || snapshot.NodeRunID != identity.NodeRunID ||
		snapshot.NodeAttemptID != identity.NodeAttemptID || snapshot.DefinitionVersion != identity.DefinitionVersion ||
		snapshot.NodeKey != identity.NodeKey {
		return toolsapplication.WorkflowToolPolicy{}, stale(errors.New("workflow Tool policy binding changed"))
	}
	graph, err := workflowapplication.DecodeCanonicalGraph([]byte(snapshot.DefinitionGraph))
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, consistency(err)
	}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, consistency(err)
	}
	if graphHash != identity.DefinitionHash {
		return toolsapplication.WorkflowToolPolicy{}, stale(errors.New("workflow definition graph hash changed"))
	}
	node, err := exactPolicyNode(graph, identity.NodeKey)
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, err
	}
	if snapshot.NodeKind != node.Kind || snapshot.WorkflowStatus != workflowdomain.RunStatusRunning ||
		snapshot.PauseRequested || snapshot.CancelRequested ||
		snapshot.NodeStatus != workflowdomain.NodeStatusRunning || snapshot.AttemptStatus != workflowdomain.AttemptStatusRunning ||
		int64(snapshot.NodeAttempt) != int64(snapshot.AttemptNo) || int64(snapshot.AttemptNo) != identity.LeaseFence ||
		!snapshot.NodeLeaseOwnerSet || !snapshot.AttemptLeaseOwnerSet ||
		!snapshot.NodeLeaseUntilSet || !snapshot.AttemptLeaseUntilSet ||
		snapshot.NodeLeaseOwner != identity.LeaseOwner || snapshot.AttemptLeaseOwner != identity.LeaseOwner ||
		!snapshot.NodeLeaseUntil.Equal(snapshot.AttemptLeaseUntil) || !snapshot.NodeLeaseUntil.After(snapshot.DatabaseNow) {
		return toolsapplication.WorkflowToolPolicy{}, stale(errors.New("workflow attempt lease is stale"))
	}
	return toolsapplication.WorkflowToolPolicy{
		Identity: identity, WorkflowKey: snapshot.DefinitionKey, NodeKind: snapshot.NodeKind,
		Permissions:    append([]capability.Capability(nil), node.RequiredPermissions...),
		AllowedTools:   append([]toolsdomain.ToolRef(nil), node.AllowedTools...),
		AttemptLeaseTo: snapshot.AttemptLeaseUntil,
	}, nil
}

var _ toolsapplication.WorkflowPolicyReader = (*GORMRepository)(nil)
