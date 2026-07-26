package main

import (
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

func TestArtifactRuntimeCatalogIsIndependentFrozenAndUsesConfiguredContract(t *testing.T) {
	contract := platformmodels.ChatContract{
		Provider: "openai-compatible",
		Model: agentdomain.ModelRef{
			AdapterName: "openai-compatible-http", AdapterVersion: "v1",
			ModelID: "artifact-composition", ModelVersion: "2026-07-26",
		},
		Timeout: 17 * time.Second,
	}
	catalog, err := newArtifactRuntimeCatalog(contract)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot(
		artifactworkflow.PromptRef(),
		artifactworkflow.SchemaRef(),
		artifactworkflow.ReducedSchemaRef(),
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Profile.Ref != agentworkflow.DefaultProfileRef() || snapshot.Profile.Model != contract.Model || snapshot.Profile.Timeout != contract.Timeout || snapshot.Profile.MaxOutputTokens != agentStructuredMaxOutputTokens {
		t.Fatalf("artifact profile=%+v contract=%+v", snapshot.Profile, contract)
	}
	if snapshot.Prompt.Ref != artifactworkflow.PromptRef() || snapshot.Schema.Ref != artifactworkflow.SchemaRef() || snapshot.ReducedSchema.Ref != artifactworkflow.ReducedSchemaRef() {
		t.Fatalf("artifact snapshot refs prompt=%+v schema=%+v reduced=%+v", snapshot.Prompt.Ref, snapshot.Schema.Ref, snapshot.ReducedSchema.Ref)
	}
	if err := artifactworkflow.RegisterRuntimeCatalog(catalog); err == nil {
		t.Fatal("artifact runtime catalog remained mutable after composition")
	}
}
