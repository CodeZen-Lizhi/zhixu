package workflow

import (
	"encoding/json"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	toolagent "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/agent"
)

func TestRuntimeCatalogPublishesIndependentToolRequestSchema(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	ref := toolagent.SchemaRef()
	snapshot, err := catalog.Snapshot(DefaultPromptRef(), ref, ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Schema.Ref != (agentdomain.SchemaRef{ID: agentdomain.ToolRequestSchemaID, Version: agentdomain.OutputSchemaVersionV1}) {
		t.Fatalf("schema ref=%#v", snapshot.Schema.Ref)
	}
	good := []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{"query":"safe"},"reason":"need evidence"}`)
	decoded, err := snapshot.Schema.Decode(good)
	if err != nil {
		t.Fatalf("tool request decoder error = %v", err)
	}
	if string(decoded) != string(good) {
		t.Fatalf("decoder transformed document: got=%s want=%s", decoded, good)
	}
	var document map[string]any
	if err := json.Unmarshal(snapshot.Schema.JSONSchema, &document); err != nil {
		t.Fatal(err)
	}
	properties := document["properties"].(map[string]any)
	if len(properties) != 4 || document["additionalProperties"] != false {
		t.Fatalf("tool request schema=%s", snapshot.Schema.JSONSchema)
	}
	for _, forbidden := range []string{"result_type", "schema_id", "model_run_ref", "workspace_id", "capability", "approval_id", "credential", "endpoint", "timeout", "path", "command", "git_args"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("tool request schema exposes forbidden field %q", forbidden)
		}
	}
	snapshot.Schema.JSONSchema[0] = '['
	again, err := catalog.Snapshot(DefaultPromptRef(), ref, ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	if again.Schema.JSONSchema[0] != '{' {
		t.Fatal("catalog snapshot mutation changed registered tool request schema")
	}
}
