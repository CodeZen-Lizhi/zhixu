package application

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestDynamicToolCannotBypassOperationBudgetThroughGenericExecute(t *testing.T) {
	contract := workspaceAnalysisSearchExecutionContract()
	contract.Definition.Ref.Version = 3
	contract.Definition.OutputSchema.Version = 3
	contract.Definition.AllowedWorkflows[0].Version = 2
	executor := &executionTestExecutor{}
	service, repository, policy := newExecutionTestService(t, contract, executor)
	command := executionTestCommand("SearchKnowledge", json.RawMessage(`{}`))
	command.Identity.DefinitionVersion = 2
	command.Identity.NodeKey = "decide_next"
	command.Invocation = domain.InvocationSourceTrustedWorkflow
	policy.policy.Identity = command.Identity
	_, err := service.Execute(context.Background(), command)
	if errorCode(err) != errorCodeWorkspaceAnalysisToolCommandInvalid || executor.calls != 0 || len(repository.started) != 0 || len(repository.workspaceAuthorized) != 0 {
		t.Fatalf("generic execution bypassed dynamic operation authority: %v", err)
	}
}
