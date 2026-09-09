package application

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestSynthesisModelStepRequiresExactResultAndTerminalUnion(t *testing.T) {
	newRecord := func() SynthesisModelStepRecord {
		input, result := synthesisContractFixture()
		output := json.RawMessage(`{"notes":[]}`)
		result.OutputHash = synthesisContractHash(string(output))
		completed := input.SourceEvent.CreatedAt.Add(time.Second)
		return SynthesisModelStepRecord{
			ID: synthesisContractID(50), WorkspaceID: input.SourceEvent.Source.WorkspaceID, ProcessingID: input.ProcessingID,
			WorkflowRunID: input.WorkflowRunID, NodeRunID: input.NodeRunID, NodeAttemptID: input.NodeAttemptID,
			Stage: SynthesisModelGenerate, RequestHash: synthesisContractHash("model request"), InputRequestHash: input.RequestHash,
			ModelRunID: result.ModelRunID, Status: SynthesisModelStepReady, Output: output, OutputHash: result.OutputHash,
			Generation: &result, Version: 3, CreatedAt: input.SourceEvent.CreatedAt, UpdatedAt: completed, CompletedAt: &completed,
		}
	}
	if err := newRecord().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*SynthesisModelStepRecord)
	}{
		{"unbound model run", func(record *SynthesisModelStepRecord) { record.ModelRunID = "" }},
		{"changed raw bytes", func(record *SynthesisModelStepRecord) { record.Output = append(record.Output, ' ') }},
		{"changed generation request", func(record *SynthesisModelStepRecord) { record.Generation.RequestHash = synthesisContractHash("other") }},
		{"changed generation model run", func(record *SynthesisModelStepRecord) { record.Generation.ModelRunID = synthesisContractID(88) }},
		{"changed generation output hash", func(record *SynthesisModelStepRecord) { record.Generation.OutputHash = synthesisContractHash("other") }},
		{"mixed result union", func(record *SynthesisModelStepRecord) { record.Semantic = &SynthesisSemanticReceipt{} }},
		{"running with output", func(record *SynthesisModelStepRecord) { record.Status = SynthesisModelStepRunning }},
		{"failed with output", func(record *SynthesisModelStepRecord) {
			record.Status = SynthesisModelStepFailed
			record.ErrorCode = "SYNTHESIS_MODEL_FAILED"
		}},
		{"generate with validation parent", func(record *SynthesisModelStepRecord) { record.GenerationOutputHash = synthesisContractHash("parent") }},
		{"wrong stage", func(record *SynthesisModelStepRecord) { record.Stage = "UNKNOWN" }},
		{"missing completion", func(record *SynthesisModelStepRecord) { record.CompletedAt = nil }},
		{"nanosecond timestamp", func(record *SynthesisModelStepRecord) { record.CreatedAt = record.CreatedAt.Add(time.Nanosecond) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := newRecord()
			test.mutate(&record)
			assertOrganizingApplicationError(t, record.Validate(), foundation.ErrorConsistencyViolation, ErrorCodeSynthesisModelStepInvalid)
		})
	}
}

func TestSynthesisSemanticReceiptCannotReuseTheGenerationModelRun(t *testing.T) {
	_, generation := synthesisContractFixture()
	receipt := SynthesisSemanticReceipt{
		ModelRunID: synthesisContractID(71), GenerationModelRunID: generation.ModelRunID, RequestHash: generation.RequestHash,
		GenerationOutputHash: generation.OutputHash, OutputHash: synthesisContractHash("independent review"), CheckCount: 1,
	}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt.Accepted = true
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt.ModelRunID = receipt.GenerationModelRunID
	if err := receipt.Validate(); err == nil {
		t.Fatal("a generator's model run was accepted as independent review")
	}
	receipt.ModelRunID, receipt.CheckCount = synthesisContractID(71), 0
	if err := receipt.Validate(); err == nil {
		t.Fatal("an empty semantic attestation was accepted")
	}
}
