package main

import (
	"context"
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestSourceReviewWorkerKeepsFixedGraphAndRebuildsModelCatalog(t *testing.T) {
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	executor := &organizingworkflow.SourceReviewExecutor{Model: unavailableSourceReviewModel{}}
	if err = registerSourceReviewWorkerExecutor(executors, executor); err != nil {
		t.Fatal(err)
	}
	if err = executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range organizingworkflow.SourceReviewDefinitions() {
		if err = definitions.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	if err = definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	if _, err = executor.Execute(context.Background(), workflowapp.ExecutionContext{}); err == nil {
		t.Fatal("missing owner was silently enabled")
	}
	ref := agentdomain.SchemaRef{ID: app.SynthesisSourceReviewSchema, Version: "v1"}
	prompt := agentdomain.PromptRef{ID: ref.ID, Version: ref.Version}
	for _, version := range []string{"model-v1", "model-v2"} {
		contract := platformmodels.ChatContract{Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "source-review", ModelVersion: version}, Timeout: time.Second}
		catalog, e := newSynthesisRuntimeCatalog(contract)
		if e != nil {
			t.Fatal(e)
		}
		snapshot, e := catalog.Snapshot(prompt, ref, ref, agentworkflow.DefaultProfileRef())
		if e != nil || snapshot.Profile.Model != contract.Model {
			t.Fatalf("rebuilt profile mismatch: %v", e)
		}
	}
}
