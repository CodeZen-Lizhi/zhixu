package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestCanonicalizeDefinitionSortsCopiesAndHashes(t *testing.T) {
	definition := validDefinition()
	definition.SensitiveFields = []string{"/token", "/authorization"}
	definition.AllowedWorkflows = []WorkflowBinding{{Key: "workflow-b", Version: 2}, {Key: "workflow-a", Version: 1}}

	canonical, err := CanonicalizeDefinition(definition)
	if err != nil {
		t.Fatalf("CanonicalizeDefinition: %v", err)
	}
	if canonical.DefinitionHash == "" || canonical.SensitiveFields[0] != "/authorization" || canonical.AllowedWorkflows[0].Key != "workflow-a" {
		t.Fatalf("canonical definition = %+v", canonical)
	}

	definition.InputSchemaDocument[0] = '['
	definition.SensitiveFields[0] = "/changed"
	definition.AllowedWorkflows[0].Key = "changed"
	if canonical.InputSchemaDocument[0] != '{' || canonical.SensitiveFields[0] != "/authorization" || canonical.AllowedWorkflows[0].Key != "workflow-a" {
		t.Fatal("canonical definition aliases caller-owned data")
	}

	replay := validDefinition()
	replay.SensitiveFields = []string{"/authorization", "/token"}
	replay.AllowedWorkflows = []WorkflowBinding{{Key: "workflow-a", Version: 1}, {Key: "workflow-b", Version: 2}}
	replayed, err := CanonicalizeDefinition(replay)
	if err != nil || replayed.DefinitionHash != canonical.DefinitionHash {
		t.Fatalf("replay hash = %q, err = %v, want %q", replayed.DefinitionHash, err, canonical.DefinitionHash)
	}
}

func TestCanonicalizeDefinitionRejectsUnsafeContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
	}{
		{name: "invalid name", mutate: func(value *Definition) { value.Ref.Name = "search_knowledge" }},
		{name: "duplicate schema json key", mutate: func(value *Definition) {
			value.InputSchemaDocument = json.RawMessage(`{"type":"object","type":"array"}`)
		}},
		{name: "unknown capability", mutate: func(value *Definition) { value.RequiredCapability = "ADMIN_MAINTENANCE" }},
		{name: "model write", mutate: func(value *Definition) {
			value.SideEffectLevel = SideEffectDomainWrite
			value.IdempotencyMode = IdempotencyRequired
		}},
		{name: "write without idempotency", mutate: func(value *Definition) {
			value.InvocationPolicy = InvocationTrustedWorkflowOnly
			value.SideEffectLevel = SideEffectDomainWrite
		}},
		{name: "timeout", mutate: func(value *Definition) { value.Timeout = MaxToolTimeout + time.Nanosecond }},
		{name: "retry", mutate: func(value *Definition) { value.RetryPolicy = RetryPolicy{MaxAttempts: 2} }},
		{name: "duplicate workflow", mutate: func(value *Definition) {
			value.AllowedWorkflows = append(value.AllowedWorkflows, value.AllowedWorkflows[0])
		}},
		{name: "duplicate sensitive field", mutate: func(value *Definition) { value.SensitiveFields = []string{"/token", "/token"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.mutate(&definition)
			if _, err := CanonicalizeDefinition(definition); err == nil {
				t.Fatal("expected definition rejection")
			}
		})
	}
}

func validDefinition() Definition {
	return Definition{
		Ref:                  ToolRef{Name: "SearchKnowledge", Version: 1},
		Description:          "Search approved knowledge.",
		InputSchema:          SchemaRef{ID: "tool.search_knowledge.input", Version: 1},
		InputSchemaDocument:  json.RawMessage(`{"additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"}`),
		OutputSchema:         SchemaRef{ID: "tool.search_knowledge.output", Version: 1},
		OutputSchemaDocument: json.RawMessage(`{"additionalProperties":false,"properties":{"items":{"type":"array"}},"required":["items"],"type":"object"}`),
		RequiredCapability:   capability.ReadLocal,
		SideEffectLevel:      SideEffectNone,
		InvocationPolicy:     InvocationModelRequestable,
		Timeout:              10 * time.Second,
		RetryPolicy:          RetryPolicy{MaxAttempts: 1},
		IdempotencyMode:      IdempotencyNone,
		AllowedWorkflows:     []WorkflowBinding{{Key: "agent-rag", Version: 1}},
		MaxInputBytes:        64 * 1024,
		MaxOutputBytes:       256 * 1024,
	}
}

func TestDefinitionLimitsRemainFinite(t *testing.T) {
	if MaxToolNameBytes < len("RunRegressionEvaluation") || MaxToolDocumentBytes <= MaxToolSchemaBytes || MaxToolTimeout <= 0 ||
		MaxToolRetryAttempts < 1 || MaxSensitiveFieldPaths < 1 || MaxAllowedWorkflowBindings < 1 {
		t.Fatal("tool definition limits are not finite")
	}
	if strings.Contains(string(capability.IndexMaintenance), "ADMIN") {
		t.Fatal("canonical maintenance capability contains legacy admin vocabulary")
	}
}

func TestDefinitionInvocationPolicySeparatesModelAndTrustedWorkflow(t *testing.T) {
	model := validDefinition()
	if !model.AllowsInvocation(InvocationSourceModelRequest) || !model.AllowsInvocation(InvocationSourceTrustedWorkflow) {
		t.Fatal("model-requestable tool rejected a valid invocation source")
	}
	model.InvocationPolicy = InvocationTrustedWorkflowOnly
	if model.AllowsInvocation(InvocationSourceModelRequest) || !model.AllowsInvocation(InvocationSourceTrustedWorkflow) {
		t.Fatal("trusted-only tool was exposed to the model or hidden from trusted workflow")
	}
	if model.AllowsInvocation("UNKNOWN") {
		t.Fatal("unknown invocation source was accepted")
	}
}
