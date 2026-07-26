package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

const (
	generationReceiptMissingCode         = "ARTIFACT_GENERATION_RECEIPT_MISSING"
	generationModelOutcomeUnknownCode    = "ARTIFACT_GENERATION_MODEL_OUTCOME_UNKNOWN"
	generationReceiptMissingSummary      = "workflow terminated without an artifact generation receipt"
	generationModelOutcomeUnknownSummary = "model execution outcome cannot be safely retried"
)

// SectionGenerationTerminalHook 在 Workflow 当前事务内收口未完成的 Artifact Generation。
type SectionGenerationTerminalHook struct {
	agent   agentapplication.ModelRunTxFinalizer
	profile agentdomain.ModelProfileRef
}

// NewSectionGenerationTerminalHook 构造只读取权威 Generation、Workflow 与 Agent 事实的终态 Hook。
func NewSectionGenerationTerminalHook(
	agent agentapplication.ModelRunTxFinalizer,
	profile agentdomain.ModelProfileRef,
) (*SectionGenerationTerminalHook, error) {
	if nilGenerationDependency(agent) || profile.Validate() != nil {
		return nil, generationCapabilityError(errors.New("artifact generation terminal dependencies are incomplete"))
	}
	return &SectionGenerationTerminalHook{agent: agent, profile: profile}, nil
}

// OnWorkflowNodeTerminal 只处理绑定 Artifact Generation 的 Node；调用方独占事务提交与回滚。
func (hook *SectionGenerationTerminalHook) OnWorkflowNodeTerminal(
	ctx context.Context,
	transaction any,
	event workflowapplication.WorkflowNodeTerminalEvent,
) error {
	if hook == nil || nilGenerationDependency(hook.agent) || hook.profile.Validate() != nil {
		return generationCapabilityError(errors.New("artifact generation terminal hook is unavailable"))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || nilGenerationDependency(tx) {
		return generationContextError(foundation.ErrorInvalidInput, errors.New("artifact generation terminal transaction is invalid"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !validID(event.WorkflowRunID) || !validID(event.NodeRunID) || event.WorkflowRunID == event.NodeRunID ||
		(event.NodeAttemptID != "" && (!validID(event.NodeAttemptID) || event.NodeAttemptID == event.WorkflowRunID || event.NodeAttemptID == event.NodeRunID)) ||
		event.TerminalAt.IsZero() {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal event is invalid"))
	}

	generation, found, err := loadSectionGenerationForTerminal(ctx, tx, event.WorkflowRunID, event.NodeRunID)
	if err != nil || !found {
		return err
	}
	if generation.Status != artifactapplication.SectionGenerationPending {
		return nil
	}

	record, err := hook.loadTerminalModelRun(ctx, tx, generation, event.NodeAttemptID)
	if err != nil {
		return err
	}
	resolution, err := resolveSectionGenerationTerminal(event, record)
	if err != nil {
		return err
	}
	terminal := terminalSectionGeneration(generation, record, resolution, event.TerminalAt)
	if err := artifactapplication.ValidateSectionGeneration(terminal); err != nil {
		return generationContextError(foundation.ErrorConsistencyViolation, fmt.Errorf("validate artifact generation terminal: %w", err))
	}
	return updateTerminalSectionGeneration(ctx, tx, generation, terminal)
}

func (hook *SectionGenerationTerminalHook) loadTerminalModelRun(
	ctx context.Context,
	tx pgx.Tx,
	generation artifactapplication.SectionGeneration,
	attemptID foundation.ID,
) (*agentapplication.ModelRunRecord, error) {
	if attemptID == "" {
		return nil, nil
	}
	run, found, err := hook.agent.GetModelRunByAttemptTx(ctx, tx, generation.WorkspaceID, attemptID, true)
	if err != nil {
		return nil, mapGenerationTerminalDependencyError(err)
	}
	if !found {
		return nil, nil
	}
	record, err := hook.agent.GetModelRunRecordTx(ctx, tx, generation.WorkspaceID, run.ID, true)
	if err != nil {
		return nil, mapGenerationTerminalDependencyError(err)
	}
	if !reflect.DeepEqual(run, record.Run) {
		return nil, generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation model run changed while terminalizing"))
	}
	if err := validateTerminalModelRunRecord(record, generation, attemptID, hook.profile); err != nil {
		return nil, err
	}
	return &record, nil
}

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

func loadSectionGenerationForTerminal(
	ctx context.Context,
	tx pgx.Tx,
	workflowRunID, nodeRunID foundation.ID,
) (artifactapplication.SectionGeneration, bool, error) {
	return loadSectionGeneration(ctx, tx, `workflow_run_id=$1 AND node_run_id=$2`, true, string(workflowRunID), string(nodeRunID))
}

func updateTerminalSectionGeneration(
	ctx context.Context,
	tx pgx.Tx,
	pending, terminal artifactapplication.SectionGeneration,
) error {
	var modelRunID any
	if terminal.ModelRunID != nil {
		modelRunID = string(*terminal.ModelRunID)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE learning.artifact_section_generation
		SET status=$1,model_run_id=$2,failure_class=$3,error_code=$4,error_summary=$5,
		    version=$6,updated_at=$7,terminal_at=$7
		WHERE id=$8 AND workspace_id=$9 AND status='PENDING' AND version=$10`,
		string(terminal.Status), modelRunID, terminal.FailureClass, terminal.ErrorCode, terminal.ErrorSummary,
		terminal.Version, terminal.UpdatedAt.UTC(), string(pending.ID), string(pending.WorkspaceID), pending.Version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal compare-and-swap failed"))
	}
	return nil
}

func mapGenerationTerminalDependencyError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return foundation.NewError(classified.Kind, artifactworkflow.ErrorCodeContextInvalid, classified.Retryable, err)
	}
	return generationCapabilityError(err)
}

var _ workflowapplication.WorkflowTerminalHook = (*SectionGenerationTerminalHook)(nil)
