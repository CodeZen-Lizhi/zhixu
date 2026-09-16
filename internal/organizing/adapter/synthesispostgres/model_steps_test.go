package synthesispostgres

import (
	"strings"
	"testing"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestSynthesisModelProofRequiresTheFinalExactStructuredResponse(t *testing.T) {
	model, step := synthesisModelProofFixture()
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisLegacyPromptVersion); err != nil {
		t.Fatalf("valid recorded output rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*agentapp.ModelRunRecord)
	}{
		{"earlier response", func(record *agentapp.ModelRunRecord) {
			last := record.Calls[0]
			last.ID = "10000000-0000-4000-8000-000000000007"
			last.CallNo, last.Phase = 2, agentdomain.ModelCallRepair
			last.ResponseHash = strings.Repeat("b", 64)
			record.Calls = append(record.Calls, last)
		}},
		{"response length", func(record *agentapp.ModelRunRecord) { record.Calls[0].ResponseBytes++ }},
		{"wrong phase", func(record *agentapp.ModelRunRecord) { record.Calls[0].Phase = agentdomain.ModelCallAnswer }},
		{"different run", func(record *agentapp.ModelRunRecord) {
			record.Run.ID = "10000000-0000-4000-8000-000000000008"
			record.Calls[0].ModelRunID = record.Run.ID
		}},
		{"different prompt", func(record *agentapp.ModelRunRecord) { record.Calls[0].Prompt.Version = "v2" }},
		{"failed recorded call", func(record *agentapp.ModelRunRecord) {
			record.Calls[0].Status = agentdomain.ModelCallFailed
			record.Calls[0].ErrorCode = "PROVIDER_FAILED"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, step := synthesisModelProofFixture()
			test.mutate(&record)
			if err := validateModelProof(record, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisLegacyPromptVersion); err == nil {
				t.Fatal("mismatched recorded invocation was accepted as output proof")
			}
		})
	}
}

func TestSynthesisModelProofRequiresTheFrozenPromptVersion(t *testing.T) {
	model, step := synthesisModelProofFixture()
	model.Run.Prompt.Version = organizingapp.SynthesisAnchoredPromptVersion
	model.Calls[0].Prompt = model.Run.Prompt
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisAnchoredPromptVersion); err != nil {
		t.Fatalf("anchored v2 proof rejected: %v", err)
	}
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisLegacyPromptVersion); err == nil {
		t.Fatal("anchored v2 proof was accepted under the unanchored v1 contract")
	}
}

func TestSynthesisGoalProofRequiresV3RatherThanLegacyPrompts(t *testing.T) {
	model, step := synthesisModelProofFixture()
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisGoalPromptVersion); err == nil {
		t.Fatal("v1 proof accepted for goal generation")
	}
	model.Run.Prompt.Version = organizingapp.SynthesisGoalPromptVersion
	model.Calls[0].Prompt = model.Run.Prompt
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisGoalPromptVersion); err != nil {
		t.Fatal(err)
	}
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisAnchoredPromptVersion); err == nil {
		t.Fatal("goal proof accepted for anchor generation")
	}
}

func TestSynthesisBodyProofRequiresGenerateV2AndReviewV1Schemas(t *testing.T) {
	for _, stage := range []organizingapp.SynthesisModelStage{organizingapp.SynthesisModelGenerate, organizingapp.SynthesisModelValidate} {
		model, step := synthesisModelProofFixture()
		step.Stage = stage
		model.Run.Prompt.Version = organizingapp.SynthesisBodyPromptVersion
		if stage == organizingapp.SynthesisModelGenerate {
			model.Run.Schema.Version = organizingapp.SynthesisBodySchemaVersion
		} else {
			model.Run.Prompt.ID = organizingapp.SynthesisSemanticPromptID
			model.Run.Schema.ID = "agent.synthesis-semantic-review"
		}
		model.Run.ReducedSchema = model.Run.Schema
		model.Calls[0].Prompt, model.Calls[0].Schema = model.Run.Prompt, model.Run.Schema
		if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisBodyPromptVersion); err != nil {
			t.Fatalf("%s proof rejected: %v", stage, err)
		}
		if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisAnchoredPromptVersion); err == nil {
			t.Fatalf("%s body proof accepted under old contract", stage)
		}
		if stage == organizingapp.SynthesisModelGenerate {
			model.Run.Schema.Version = organizingapp.SynthesisRuntimeVersion
		} else {
			model.Run.Schema.Version = organizingapp.SynthesisBodySchemaVersion
		}
		model.Run.ReducedSchema = model.Run.Schema
		model.Calls[0].Schema = model.Run.Schema
		if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisBodyPromptVersion); err == nil {
			t.Fatalf("%s accepted the other stage's schema version", stage)
		}
	}
}

func synthesisModelProofFixture() (agentapp.ModelRunRecord, organizingapp.SynthesisModelStepRecord) {
	at := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	run := agentdomain.ModelRun{
		ID: "10000000-0000-4000-8000-000000000001", WorkspaceID: "10000000-0000-4000-8000-000000000002",
		WorkflowRunID: "10000000-0000-4000-8000-000000000003", NodeRunID: "10000000-0000-4000-8000-000000000004",
		NodeAttemptID: "10000000-0000-4000-8000-000000000005", Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
		Model:   agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "test", ModelVersion: "v1"},
		Profile: agentdomain.ModelProfileRef{ID: "synthesis", Version: "v1"},
		Prompt:  agentdomain.PromptRef{ID: organizingapp.SynthesisDeltaPromptID, Version: organizingapp.SynthesisRuntimeVersion},
		Schema:  agentdomain.SchemaRef{ID: "agent.synthesis-delta", Version: organizingapp.SynthesisRuntimeVersion},
	}
	run.ReducedSchema = run.Schema
	step := organizingapp.SynthesisModelStepRecord{ModelRunID: run.ID, WorkspaceID: run.WorkspaceID, WorkflowRunID: run.WorkflowRunID,
		NodeRunID: run.NodeRunID, NodeAttemptID: run.NodeAttemptID, Stage: organizingapp.SynthesisModelGenerate,
		Output: []byte(`{"notes":[]}`), OutputHash: strings.Repeat("a", 64)}
	call := agentdomain.ModelCall{ID: "10000000-0000-4000-8000-000000000006", ModelRunID: run.ID, CallNo: 1,
		Phase: agentdomain.ModelCallInitial, Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema,
		MaxOutputTokens: 256, Status: agentdomain.ModelCallSucceeded, RequestHash: strings.Repeat("c", 64), RequestBytes: 64,
		ResponseHash: step.OutputHash, ResponseBytes: int64(len(step.Output)), Version: 2, StartedAt: at, CompletedAt: &at}
	return agentapp.ModelRunRecord{Run: run, Calls: []agentdomain.ModelCall{call}}, step
}

func TestSynthesisSemanticFormatProofRejectsMixedVersions(t *testing.T) {
	model, step := synthesisModelProofFixture()
	step.Stage = organizingapp.SynthesisModelValidate
	model.Run.Prompt.ID = organizingapp.SynthesisSemanticPromptID
	model.Run.Prompt.Version = organizingapp.SynthesisSemanticFormatPromptVersion
	model.Run.Schema.ID = "agent.synthesis-semantic-review"
	model.Run.ReducedSchema = model.Run.Schema
	model.Calls[0].Prompt, model.Calls[0].Schema = model.Run.Prompt, model.Run.Schema
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisSemanticFormatPromptVersion); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v1", "v2", "v3", "v4", "v5", "v7"} {
		if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), version); err == nil {
			t.Fatalf("v6 proof accepted as %s", version)
		}
	}
	model.Run.Prompt.Version = organizingapp.SynthesisLegacyPromptVersion
	model.Calls[0].Prompt = model.Run.Prompt
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output)), organizingapp.SynthesisSemanticFormatPromptVersion); err == nil {
		t.Fatal("legacy proof accepted as v6")
	}
	model.Run.Prompt = agentdomain.PromptRef{ID: organizingapp.SynthesisDeltaPromptID, Version: organizingapp.SynthesisSemanticFormatPromptVersion}
	model.Run.Schema.ID = "agent.synthesis-delta"
	model.Run.ReducedSchema = model.Run.Schema
	if validModelRuntime(model.Run, organizingapp.SynthesisModelGenerate, organizingapp.SynthesisSemanticFormatPromptVersion) {
		t.Fatal("v6 generation admitted")
	}
}
