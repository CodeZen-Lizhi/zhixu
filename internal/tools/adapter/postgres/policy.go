package postgres

import (
	"errors"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

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
