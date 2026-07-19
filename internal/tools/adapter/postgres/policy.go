package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

type persistedPolicy struct {
	policy            toolsapplication.WorkflowToolPolicy
	definitionVersion int64
	definitionGraph   []byte
	runStatus         workflowdomain.RunStatus
	nodeStatus        workflowdomain.NodeStatus
	nodeLeaseOwner    string
	nodeLeaseUntil    time.Time
	attemptStatus     workflowdomain.AttemptStatus
	nodeAttemptNo     int64
	attemptNo         int64
	attemptLeaseOwner string
	attemptLeaseUntil time.Time
	databaseNow       time.Time
}

// ResolveToolPolicy 校验完整执行身份、活动租约和 Definition graph，并返回不可变策略副本。
func (repository *Repository) ResolveToolPolicy(ctx context.Context, identity toolsdomain.TrustedExecutionIdentity) (toolsapplication.WorkflowToolPolicy, error) {
	if err := identity.Validate(); err != nil || identity.LeaseOwner != strings.TrimSpace(identity.LeaseOwner) {
		if err != nil {
			return toolsapplication.WorkflowToolPolicy{}, err
		}
		return toolsapplication.WorkflowToolPolicy{}, foundation.NewError(foundation.ErrorInvalidInput, toolsdomain.ErrorCodeExecutionIdentityInvalid, false, errors.New("tool lease owner is not canonical"))
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	policy, err := loadPersistedPolicy(ctx, tx, identity)
	if err != nil {
		return toolsapplication.WorkflowToolPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsapplication.WorkflowToolPolicy{}, classify(err)
	}
	return clonePolicy(policy.policy), nil
}

func loadPersistedPolicy(ctx context.Context, tx pgx.Tx, identity toolsdomain.TrustedExecutionIdentity) (persistedPolicy, error) {
	var persisted persistedPolicy
	var definitionGraph []byte
	var runStatus, nodeStatus, attemptStatus string
	var nodeLeaseOwner, attemptLeaseOwner *string
	var nodeLeaseUntil, attemptLeaseUntil *time.Time
	err := tx.QueryRow(ctx, `
		SELECT d.key,d.version,d.graph,r.status,n.node_type,n.status,n.attempt,n.lease_owner,n.lease_until,
			a.status,a.attempt_no,a.lease_owner,a.lease_until,clock_timestamp()
		FROM workflow.run r
		JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
		JOIN workflow.node_run n ON n.run_id=r.id
		JOIN workflow.node_attempt a ON a.node_run_id=n.id
		WHERE r.workspace_id=$1 AND r.id=$2 AND r.definition_id=$3
		  AND n.id=$4 AND n.node_key=$5 AND a.id=$6
		FOR SHARE OF r,d,n,a`,
		string(identity.WorkspaceID), string(identity.WorkflowRunID), string(identity.DefinitionID),
		string(identity.NodeRunID), identity.NodeKey, string(identity.NodeAttemptID),
	).Scan(
		&persisted.policy.WorkflowKey, &persisted.definitionVersion, &definitionGraph, &runStatus,
		&persisted.policy.NodeKind, &nodeStatus, &persisted.nodeAttemptNo, &nodeLeaseOwner, &nodeLeaseUntil,
		&attemptStatus, &persisted.attemptNo, &attemptLeaseOwner, &attemptLeaseUntil, &persisted.databaseNow,
	)
	if noRows(err) {
		return persistedPolicy{}, stale(err)
	}
	if err != nil {
		return persistedPolicy{}, classify(err)
	}
	persisted.definitionGraph = append([]byte(nil), definitionGraph...)
	persisted.runStatus = workflowdomain.RunStatus(runStatus)
	persisted.nodeStatus = workflowdomain.NodeStatus(nodeStatus)
	persisted.attemptStatus = workflowdomain.AttemptStatus(attemptStatus)
	if nodeLeaseOwner != nil {
		persisted.nodeLeaseOwner = *nodeLeaseOwner
	}
	if nodeLeaseUntil != nil {
		persisted.nodeLeaseUntil = nodeLeaseUntil.UTC()
	}
	if attemptLeaseOwner != nil {
		persisted.attemptLeaseOwner = *attemptLeaseOwner
	}
	if attemptLeaseUntil != nil {
		persisted.attemptLeaseUntil = attemptLeaseUntil.UTC()
	}
	persisted.databaseNow = persisted.databaseNow.UTC()

	graph, err := workflowapplication.DecodeCanonicalGraph(definitionGraph)
	if err != nil {
		return persistedPolicy{}, consistency(err)
	}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return persistedPolicy{}, consistency(err)
	}
	if persisted.definitionVersion != identity.DefinitionVersion || graphHash != identity.DefinitionHash {
		return persistedPolicy{}, stale(errors.New("workflow definition version or graph hash changed"))
	}
	node, err := exactPolicyNode(graph, identity.NodeKey)
	if err != nil {
		return persistedPolicy{}, err
	}
	if persisted.policy.NodeKind != node.Kind || persisted.runStatus != workflowdomain.RunStatusRunning ||
		persisted.nodeStatus != workflowdomain.NodeStatusRunning || persisted.attemptStatus != workflowdomain.AttemptStatusRunning ||
		persisted.nodeAttemptNo != persisted.attemptNo || persisted.attemptNo != identity.LeaseFence ||
		persisted.nodeLeaseOwner != identity.LeaseOwner || persisted.attemptLeaseOwner != identity.LeaseOwner ||
		persisted.nodeLeaseUntil.IsZero() || persisted.attemptLeaseUntil.IsZero() ||
		!persisted.nodeLeaseUntil.Equal(persisted.attemptLeaseUntil) || !persisted.nodeLeaseUntil.After(persisted.databaseNow) {
		return persistedPolicy{}, stale(errors.New("workflow attempt lease is stale"))
	}
	persisted.policy.Identity = identity
	persisted.policy.Permissions = append([]capability.Capability(nil), node.RequiredPermissions...)
	persisted.policy.AllowedTools = append([]toolsdomain.ToolRef(nil), node.AllowedTools...)
	persisted.policy.AttemptLeaseTo = persisted.attemptLeaseUntil
	return persisted, nil
}

func exactPolicyNode(graph workflowdomain.CanonicalGraph, nodeKey string) (workflowdomain.NodeDefinition, error) {
	var result workflowdomain.NodeDefinition
	found := false
	for _, node := range graph.Nodes {
		if node.Key != nodeKey {
			continue
		}
		if found {
			return workflowdomain.NodeDefinition{}, consistency(errors.New("workflow graph contains duplicate node key"))
		}
		result = node
		found = true
	}
	if !found {
		return workflowdomain.NodeDefinition{}, stale(errors.New("workflow node is absent from definition graph"))
	}
	for index, permission := range result.RequiredPermissions {
		if !capability.IsKnown(permission) || (index > 0 && string(result.RequiredPermissions[index-1]) >= string(permission)) {
			return workflowdomain.NodeDefinition{}, consistency(errors.New("workflow node permissions are not canonical"))
		}
	}
	for index, ref := range result.AllowedTools {
		if ref.Validate() != nil || (index > 0 && !toolRefLess(result.AllowedTools[index-1], ref)) {
			return workflowdomain.NodeDefinition{}, consistency(errors.New("workflow node allowed tools are not canonical"))
		}
	}
	return result, nil
}

func validateStartPolicy(policy toolsapplication.WorkflowToolPolicy, command toolsapplication.StartCallCommand) error {
	call := command.Call
	if call.Tool == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, toolsdomain.ErrorCodeCallInvalid, false, errors.New("started tool call is unresolved"))
	}
	if !containsWorkflowBinding(command.AllowedWorkflows, policy.WorkflowKey, command.Identity.DefinitionVersion) {
		return denied(ErrorCodeWorkflowBindingDenied, errors.New("tool contract does not allow workflow definition"))
	}
	if !containsToolRef(policy.AllowedTools, *call.Tool) {
		return denied(ErrorCodeToolNotAllowed, errors.New("workflow node does not allow exact tool version"))
	}
	if call.Capability != "" && !containsCapability(policy.Permissions, call.Capability) {
		return denied(ErrorCodePermissionDenied, errors.New("workflow node lacks exact tool capability"))
	}
	return nil
}

func containsWorkflowBinding(values []toolsdomain.WorkflowBinding, key string, version int64) bool {
	if len(values) == 0 {
		return false
	}
	copyOfValues := append([]toolsdomain.WorkflowBinding(nil), values...)
	sort.Slice(copyOfValues, func(left, right int) bool {
		if copyOfValues[left].Key == copyOfValues[right].Key {
			return copyOfValues[left].Version < copyOfValues[right].Version
		}
		return copyOfValues[left].Key < copyOfValues[right].Key
	})
	found := false
	for index, value := range copyOfValues {
		if value.Key == "" || value.Key != strings.TrimSpace(value.Key) || value.Version < 1 ||
			(index > 0 && value == copyOfValues[index-1]) {
			return false
		}
		found = found || (value.Key == key && value.Version == version)
	}
	return found
}

func containsToolRef(values []toolsdomain.ToolRef, target toolsdomain.ToolRef) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsCapability(values []capability.Capability, target capability.Capability) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func toolRefLess(left, right toolsdomain.ToolRef) bool {
	if left.Name == right.Name {
		return left.Version < right.Version
	}
	return left.Name < right.Name
}

func clonePolicy(policy toolsapplication.WorkflowToolPolicy) toolsapplication.WorkflowToolPolicy {
	policy.Permissions = append([]capability.Capability(nil), policy.Permissions...)
	policy.AllowedTools = append([]toolsdomain.ToolRef(nil), policy.AllowedTools...)
	return policy
}
