package postgres

import (
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestResolveSectionGenerationTerminalUsesOnlySafeRetryOutcomes(t *testing.T) {
	base := workflowapplication.WorkflowNodeTerminalEvent{
		Outcome: workflowapplication.WorkflowTerminalOutcomeFailed, FailureClass: workflowdomain.FailureClassRetryable,
		FailureCode: "DEPENDENCY_BUSY", FailureSummary: "dependency busy",
	}
	tests := []struct {
		name        string
		event       workflowapplication.WorkflowNodeTerminalEvent
		record      *agentapplication.ModelRunRecord
		wantStatus  artifactapplication.SectionGenerationStatus
		wantClass   workflowdomain.FailureClass
		wantCode    string
		wantSummary string
	}{
		{name: "exhausted retry without provider call", event: base, wantStatus: artifactapplication.SectionGenerationFailed, wantClass: workflowdomain.FailureClassNonRetryable, wantCode: "DEPENDENCY_BUSY", wantSummary: "dependency busy"},
		{name: "safe cancellation", event: terminalEventWith(base, workflowapplication.WorkflowTerminalOutcomeCancelled, workflowdomain.FailureClassCancelled), wantStatus: artifactapplication.SectionGenerationCancelled, wantClass: workflowdomain.FailureClassCancelled, wantCode: "DEPENDENCY_BUSY", wantSummary: "dependency busy"},
		{name: "manual recovery failure", event: terminalEventWith(base, workflowapplication.WorkflowTerminalOutcomeFailed, workflowdomain.FailureClassManualRecovery), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: "DEPENDENCY_BUSY", wantSummary: "dependency busy"},
		{name: "lease loss failure", event: terminalEventWith(base, workflowapplication.WorkflowTerminalOutcomeFailed, workflowdomain.FailureClassLeaseLost), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: "DEPENDENCY_BUSY", wantSummary: "dependency busy"},
		{name: "success without receipt", event: workflowapplication.WorkflowNodeTerminalEvent{Outcome: workflowapplication.WorkflowTerminalOutcomeSucceeded}, wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: generationReceiptMissingCode, wantSummary: generationReceiptMissingSummary},
		{name: "running model run", event: base, record: terminalResolutionRecord(agentdomain.ModelRunRunning), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: generationModelOutcomeUnknownCode, wantSummary: generationModelOutcomeUnknownSummary},
		{name: "unknown model run", event: base, record: terminalResolutionRecord(agentdomain.ModelRunUnknown), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: generationModelOutcomeUnknownCode, wantSummary: generationModelOutcomeUnknownSummary},
		{name: "successful model run without receipt", event: base, record: terminalResolutionRecord(agentdomain.ModelRunSucceeded), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: generationReceiptMissingCode, wantSummary: generationReceiptMissingSummary},
		{name: "started call", event: base, record: terminalResolutionRecordWithCall(agentdomain.ModelRunFailed, agentdomain.ModelCallStarted), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: generationModelOutcomeUnknownCode, wantSummary: generationModelOutcomeUnknownSummary},
		{name: "unknown call", event: base, record: terminalResolutionRecordWithCall(agentdomain.ModelRunFailed, agentdomain.ModelCallUnknown), wantStatus: artifactapplication.SectionGenerationRecoveryRequired, wantClass: workflowdomain.FailureClassManualRecovery, wantCode: generationModelOutcomeUnknownCode, wantSummary: generationModelOutcomeUnknownSummary},
		{name: "settled failed model run", event: base, record: terminalResolutionRecordWithCall(agentdomain.ModelRunFailed, agentdomain.ModelCallFailed), wantStatus: artifactapplication.SectionGenerationFailed, wantClass: workflowdomain.FailureClassNonRetryable, wantCode: "DEPENDENCY_BUSY", wantSummary: "dependency busy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveSectionGenerationTerminal(test.event, test.record)
			if err != nil {
				t.Fatal(err)
			}
			if got.status != test.wantStatus || got.class != test.wantClass || got.code != test.wantCode || got.summary != test.wantSummary {
				t.Fatalf("resolution=%+v", got)
			}
		})
	}
}

func TestValidateTerminalModelRunRecordRejectsBindingAndCallSequenceDrift(t *testing.T) {
	profile := terminalTestProfile()
	generation := artifactapplication.SectionGeneration{
		WorkspaceID: terminalTestID(1), WorkflowRunID: terminalTestID(2), NodeRunID: terminalTestID(3),
	}
	attemptID := terminalTestID(4)
	run := agentdomain.ModelRun{
		ID: terminalTestID(5), WorkspaceID: generation.WorkspaceID, WorkflowRunID: generation.WorkflowRunID,
		NodeRunID: generation.NodeRunID, NodeAttemptID: attemptID,
		Model:   agentdomain.ModelRef{AdapterName: "adapter", AdapterVersion: "v1", ModelID: "model", ModelVersion: "v1"},
		Profile: profile, Prompt: artifactworkflow.PromptRef(), Schema: artifactworkflow.SchemaRef(), ReducedSchema: artifactworkflow.ReducedSchemaRef(),
	}
	call := agentdomain.ModelCall{
		ID: terminalTestID(6), ModelRunID: run.ID, CallNo: 1, Model: run.Model, Profile: run.Profile,
		Prompt: run.Prompt, Schema: run.Schema,
	}
	record := agentapplication.ModelRunRecord{Run: run, Calls: []agentdomain.ModelCall{call}}
	if err := validateTerminalModelRunRecord(record, generation, attemptID, profile); err != nil {
		t.Fatal(err)
	}

	drifted := record
	drifted.Calls = append([]agentdomain.ModelCall(nil), record.Calls...)
	drifted.Calls = append(drifted.Calls, call)
	drifted.Calls[1].ID = terminalTestID(7)
	drifted.Calls[1].CallNo = 3
	if err := validateTerminalModelRunRecord(drifted, generation, attemptID, profile); err == nil {
		t.Fatal("gapped call sequence was accepted")
	}
	drifted = record
	drifted.Run.Profile.Version = "v2"
	if err := validateTerminalModelRunRecord(drifted, generation, attemptID, profile); err == nil {
		t.Fatal("drifted profile was accepted")
	}
}

func terminalEventWith(
	event workflowapplication.WorkflowNodeTerminalEvent,
	outcome workflowapplication.WorkflowTerminalOutcome,
	class workflowdomain.FailureClass,
) workflowapplication.WorkflowNodeTerminalEvent {
	event.Outcome = outcome
	event.FailureClass = class
	return event
}

func terminalResolutionRecord(status agentdomain.ModelRunStatus) *agentapplication.ModelRunRecord {
	return &agentapplication.ModelRunRecord{Run: agentdomain.ModelRun{ID: terminalTestID(20), Status: status}}
}

func terminalResolutionRecordWithCall(status agentdomain.ModelRunStatus, callStatus agentdomain.ModelCallStatus) *agentapplication.ModelRunRecord {
	record := terminalResolutionRecord(status)
	record.Calls = []agentdomain.ModelCall{{ID: terminalTestID(21), ModelRunID: record.Run.ID, CallNo: 1, Status: callStatus}}
	return record
}

func terminalTestProfile() agentdomain.ModelProfileRef {
	return agentdomain.ModelProfileRef{ID: "artifact-section", Version: "v1"}
}

func terminalTestID(value int) foundation.ID {
	return foundation.ID(time.Date(2026, 7, 26, 0, 0, value, 0, time.UTC).Format("20060102-1504-4050-8000-000000000000"))
}
