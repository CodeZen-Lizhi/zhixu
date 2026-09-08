package postgres

import (
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	generationReceiptMissingCode         = "ARTIFACT_GENERATION_RECEIPT_MISSING"
	generationModelOutcomeUnknownCode    = "ARTIFACT_GENERATION_MODEL_OUTCOME_UNKNOWN"
	generationReceiptMissingSummary      = "workflow terminated without an artifact generation receipt"
	generationModelOutcomeUnknownSummary = "model execution outcome cannot be safely retried"
)

type sectionGenerationTerminalResolution struct {
	status  artifactapplication.SectionGenerationStatus
	class   workflowdomain.FailureClass
	code    string
	summary string
}

func resolveSectionGenerationTerminal(
	event workflowapplication.WorkflowNodeTerminalEvent,
	record *agentapplication.ModelRunRecord,
) (sectionGenerationTerminalResolution, error) {
	if event.Outcome == workflowapplication.WorkflowTerminalOutcomeSucceeded {
		return generationRecoveryResolution(generationReceiptMissingCode, generationReceiptMissingSummary), nil
	}
	if event.Outcome != workflowapplication.WorkflowTerminalOutcomeFailed && event.Outcome != workflowapplication.WorkflowTerminalOutcomeCancelled {
		return sectionGenerationTerminalResolution{}, generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal outcome is unsupported"))
	}
	if event.FailureClass == workflowdomain.FailureClassManualRecovery || event.FailureClass == workflowdomain.FailureClassLeaseLost {
		return generationRecoveryResolution(event.FailureCode, event.FailureSummary), nil
	}
	if record != nil {
		switch record.Run.Status {
		case agentdomain.ModelRunSucceeded:
			return generationRecoveryResolution(generationReceiptMissingCode, generationReceiptMissingSummary), nil
		case agentdomain.ModelRunRunning, agentdomain.ModelRunUnknown:
			return generationRecoveryResolution(generationModelOutcomeUnknownCode, generationModelOutcomeUnknownSummary), nil
		}
		for _, call := range record.Calls {
			if call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
				return generationRecoveryResolution(generationModelOutcomeUnknownCode, generationModelOutcomeUnknownSummary), nil
			}
		}
	}
	if event.FailureCode == "" || event.FailureSummary == "" {
		return sectionGenerationTerminalResolution{}, generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal failure is incomplete"))
	}
	if event.Outcome == workflowapplication.WorkflowTerminalOutcomeCancelled {
		return sectionGenerationTerminalResolution{
			status: artifactapplication.SectionGenerationCancelled, class: workflowdomain.FailureClassCancelled,
			code: event.FailureCode, summary: event.FailureSummary,
		}, nil
	}
	return sectionGenerationTerminalResolution{
		status: artifactapplication.SectionGenerationFailed, class: workflowdomain.FailureClassNonRetryable,
		code: event.FailureCode, summary: event.FailureSummary,
	}, nil
}

func generationRecoveryResolution(code, summary string) sectionGenerationTerminalResolution {
	if code == "" || summary == "" {
		code = generationModelOutcomeUnknownCode
		summary = generationModelOutcomeUnknownSummary
	}
	return sectionGenerationTerminalResolution{
		status: artifactapplication.SectionGenerationRecoveryRequired, class: workflowdomain.FailureClassManualRecovery,
		code: code, summary: summary,
	}
}

func validateTerminalModelRunRecord(
	record agentapplication.ModelRunRecord,
	generation artifactapplication.SectionGeneration,
	attemptID foundation.ID,
	profile agentdomain.ModelProfileRef,
) error {
	run := record.Run
	if run.WorkspaceID != generation.WorkspaceID || run.WorkflowRunID != generation.WorkflowRunID ||
		run.NodeRunID != generation.NodeRunID || run.NodeAttemptID != attemptID || run.Profile != profile ||
		run.Prompt != artifactworkflow.PromptRef() || run.ReducedSchema != artifactworkflow.ReducedSchemaRef() ||
		(run.Schema != artifactworkflow.SchemaRef() && run.Schema != artifactworkflow.ReducedSchemaRef()) {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal model run binding drifted"))
	}
	for index, call := range record.Calls {
		if call.ModelRunID != run.ID || call.CallNo != index+1 || call.Model != run.Model || call.Profile != run.Profile ||
			call.Prompt != run.Prompt || (call.Schema != run.Schema && call.Schema != run.ReducedSchema) {
			return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal model call history drifted"))
		}
	}
	return nil
}

func terminalSectionGeneration(
	pending artifactapplication.SectionGeneration,
	record *agentapplication.ModelRunRecord,
	resolution sectionGenerationTerminalResolution,
	at time.Time,
) artifactapplication.SectionGeneration {
	terminal := pending
	terminal.Status = resolution.status
	if record != nil {
		modelRunID := record.Run.ID
		terminal.ModelRunID = &modelRunID
	}
	terminal.FailureClass = string(resolution.class)
	terminal.ErrorCode = resolution.code
	terminal.ErrorSummary = resolution.summary
	terminal.Version = pending.Version + 1
	terminal.UpdatedAt = at.UTC()
	terminalAt := at.UTC()
	terminal.TerminalAt = &terminalAt
	return terminal
}

func mapGenerationTerminalDependencyError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return foundation.NewError(classified.Kind, artifactworkflow.ErrorCodeContextInvalid, classified.Retryable, err)
	}
	return generationCapabilityError(err)
}
