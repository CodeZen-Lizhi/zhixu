package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

type testExecutor struct{}

func (testExecutor) Execute(_ context.Context, request ExecutorRequest) (ExecutorResult, error) {
	return ExecutorResult{Output: append(json.RawMessage(nil), request.Arguments...)}, nil
}

type nilTestExecutor struct{}

func (*nilTestExecutor) Execute(context.Context, ExecutorRequest) (ExecutorResult, error) {
	return ExecutorResult{}, nil
}

func TestContractRegistryFreezesAndReturnsDeepCopies(t *testing.T) {
	registry := NewContractRegistry()
	contract := testContract("SearchKnowledge", 1)
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatalf("RegisterContract: %v", err)
	}
	if _, err := registry.ResolveContract(contract.Definition.Ref); err == nil {
		t.Fatal("unfrozen registry resolved contract")
	}
	if err := registry.Freeze(); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	resolved, err := registry.ResolveContract(contract.Definition.Ref)
	if err != nil {
		t.Fatalf("ResolveContract: %v", err)
	}
	resolved.Definition.InputSchemaDocument[0] = '['
	resolved.Definition.AllowedWorkflows[0].Key = "mutated"
	again, err := registry.ResolveContract(contract.Definition.Ref)
	if err != nil || again.Definition.InputSchemaDocument[0] != '{' || again.Definition.AllowedWorkflows[0].Key == "mutated" {
		t.Fatalf("registry returned mutable contract: %+v, %v", again, err)
	}
	if err := registry.RegisterContract(testContract("ReadSource", 1)); err == nil {
		t.Fatal("frozen registry accepted registration")
	}
}

func TestExecutorPrivateBindingFormattingAndJSONNeverExposeDocument(t *testing.T) {
	const canary = "private-binding-canary"
	binding := ExecutorPrivateBinding{
		Schema:   domain.SchemaRef{ID: "tool.search_knowledge.private_binding", Version: 1},
		Document: json.RawMessage(`{"value":"` + canary + `"}`),
	}
	result := ExecutorResult{Output: json.RawMessage(`{}`), PrivateBinding: &binding}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		binding.String(), fmt.Sprintf("%+v", binding), fmt.Sprintf("%#v", binding),
		fmt.Sprintf("%+v", result), fmt.Sprintf("%#v", result), string(encoded),
	} {
		if strings.Contains(value, canary) {
			t.Fatalf("private binding leaked through formatting: %s", value)
		}
	}
}

func TestExecutionRegistryRequiresEveryEnabledExecutor(t *testing.T) {
	registry := NewExecutionRegistry()
	contract := testContract("SearchKnowledge", 1)
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatalf("RegisterContract: %v", err)
	}
	if err := registry.Freeze(); errorCode(err) != errorCodeExecutorRegistryIncomplete {
		t.Fatalf("Freeze code = %q, want %q", errorCode(err), errorCodeExecutorRegistryIncomplete)
	}
	if err := registry.RegisterExecutor(contract.Definition.Ref, testExecutor{}); err != nil {
		t.Fatalf("RegisterExecutor: %v", err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatalf("Freeze with executor: %v", err)
	}
	if _, err := registry.ResolveExecutor(contract.Definition.Ref); err != nil {
		t.Fatalf("ResolveExecutor: %v", err)
	}
}

func TestRegistryRejectsDuplicateMissingAndTypedNil(t *testing.T) {
	registry := NewExecutionRegistry()
	contract := testContract("SearchKnowledge", 1)
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterContract(contract); errorCode(err) != errorCodeContractDuplicate {
		t.Fatalf("duplicate code = %q", errorCode(err))
	}
	unknown := domain.ToolRef{Name: "ReadSource", Version: 1}
	if err := registry.RegisterExecutor(unknown, testExecutor{}); errorCode(err) != errorCodeContractNotFound {
		t.Fatalf("unknown executor code = %q", errorCode(err))
	}
	var typedNil *nilTestExecutor
	if err := registry.RegisterExecutor(contract.Definition.Ref, typedNil); errorCode(err) != errorCodeExecutorInvalid {
		t.Fatalf("typed nil code = %q", errorCode(err))
	}
	if err := registry.RegisterExecutor(contract.Definition.Ref, testExecutor{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterExecutor(contract.Definition.Ref, testExecutor{}); errorCode(err) != errorCodeExecutorDuplicate {
		t.Fatalf("duplicate executor code = %q", errorCode(err))
	}
}

func TestRegistryListsExactVersionsInStableOrder(t *testing.T) {
	registry := NewContractRegistry()
	for _, contract := range []Contract{testContract("SearchKnowledge", 2), testContract("ReadSource", 1), testContract("SearchKnowledge", 1)} {
		if err := registry.RegisterContract(contract); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	contracts, err := registry.ListContracts()
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ToolRef{{Name: "ReadSource", Version: 1}, {Name: "SearchKnowledge", Version: 1}, {Name: "SearchKnowledge", Version: 2}}
	for index := range want {
		if contracts[index].Definition.Ref != want[index] {
			t.Fatalf("contract %d = %+v, want %+v", index, contracts[index].Definition.Ref, want[index])
		}
	}
	if _, err := registry.ResolveContract(domain.ToolRef{Name: "SearchKnowledge", Version: 3}); errorCode(err) != errorCodeContractNotFound {
		t.Fatalf("latest drift code = %q", errorCode(err))
	}
}

func testContract(name string, version int64) Contract {
	definition := domainTestDefinition(name, version)
	decoder := func(raw []byte) (json.RawMessage, error) { return append(json.RawMessage(nil), raw...), nil }
	return Contract{Definition: definition, DecodeInput: decoder, DecodeOutput: decoder}
}

func domainTestDefinition(name string, version int64) domain.Definition {
	definition := domain.Definition{
		Ref: domain.ToolRef{Name: name, Version: version}, Description: "Bounded test tool.",
		InputSchema:          domain.SchemaRef{ID: "tool." + strings.ToLower(name) + ".input", Version: 1},
		InputSchemaDocument:  json.RawMessage(`{"additionalProperties":false,"properties":{},"type":"object"}`),
		OutputSchema:         domain.SchemaRef{ID: "tool." + strings.ToLower(name) + ".output", Version: 1},
		OutputSchemaDocument: json.RawMessage(`{"additionalProperties":false,"properties":{},"type":"object"}`),
		RequiredCapability:   "READ_LOCAL", SideEffectLevel: domain.SideEffectNone, InvocationPolicy: domain.InvocationModelRequestable,
		Timeout: time.Second, RetryPolicy: domain.RetryPolicy{MaxAttempts: 1}, IdempotencyMode: domain.IdempotencyNone,
		AllowedWorkflows: []domain.WorkflowBinding{{Key: "agent-rag", Version: 1}}, MaxInputBytes: 1024, MaxOutputBytes: 1024,
	}
	return definition
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	type coded interface{ Error() string }
	if _, ok := err.(coded); !ok {
		return ""
	}
	parts := strings.SplitN(err.Error(), ":", 2)
	return parts[0]
}
