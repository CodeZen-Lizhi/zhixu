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
	if err := validateModelProof(model, step, step.OutputHash, int64(len(step.Output))); err != nil {
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
			if err := validateModelProof(record, step, step.OutputHash, int64(len(step.Output))); err == nil {
				t.Fatal("mismatched recorded invocation was accepted as output proof")
			}
		})
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
