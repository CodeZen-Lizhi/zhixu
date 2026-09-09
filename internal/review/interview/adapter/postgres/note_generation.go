package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewworkflow "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

func (repository *GORMNotePreparationRepository) BeginNoteGeneration(ctx context.Context, id foundation.ID, execution workflowapp.ExecutionContext) (interviewapp.NotePreparation, error) {
	var result interviewapp.NotePreparation
	var recovery bool
	err := repository.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadNotePreparation(tx, execution.WorkspaceID, id, false)
		if err != nil {
			return err
		}
		p, err := row.value()
		if err != nil {
			return err
		}
		result = p
		if p.WorkflowRunID != execution.RunID || p.NodeRunID != execution.NodeRunID {
			return notePreparationConflict("note preparation does not own this execution")
		}
		if err := repository.lockLive(ctx, scope, p, execution.NodeAttemptID); err != nil {
			return err
		}
		row, err = loadNotePreparation(tx, p.WorkspaceID, p.ID, true)
		if err != nil {
			return err
		}
		p, err = row.value()
		if err != nil {
			return err
		}
		result = p
		if p.Status == interviewapp.NotePreparationReady {
			record, err := repository.dependencies.ModelRuns.GetModelRunRecordScoped(ctx, scope, p.WorkspaceID, p.ModelRunID, false)
			if err != nil {
				return err
			}
			if record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.FinalResultType != agentdomain.ResultTypeSynthesisNoteInterviewPlan {
				return notePreparationConflict("ready note interview model receipt is invalid")
			}
			return interviewapp.ValidateNoteModelOutput(p, record, p.ModelOutput)
		}
		if p.Status == interviewapp.NotePreparationGenerating {
			if p.NodeAttemptID != execution.NodeAttemptID {
				recovery = true
				return repository.failScoped(ctx, scope, tx, p, interviewapp.ErrorCodeNoteRecoveryRequired, false, true, repository.dependencies.Clock.Now())
			}
			if !reflect.DeepEqual(p.ModelSettingsRevision, execution.ModelSettingsRevision) {
				return notePreparationConflict("note preparation model settings changed")
			}
			return nil
		}
		if p.Status != interviewapp.NotePreparationQueued {
			return notePreparationConflict("note preparation is already terminal")
		}
		p.Status = interviewapp.NotePreparationGenerating
		p.NodeAttemptID = execution.NodeAttemptID
		p.ModelSettingsRevision = execution.ModelSettingsRevision
		p.Version++
		p.UpdatedAt = noteCanonicalTime(repository.dependencies.Clock.Now(), p.UpdatedAt)
		if err := p.Validate(); err != nil {
			return err
		}
		if err := updateNotePreparation(tx, row, p); err != nil {
			return err
		}
		result = p
		return nil
	})
	if err == nil && recovery {
		return result, noteGenerationRecovery()
	}
	return result, err
}

func (repository *GORMNotePreparationRepository) BindNoteModelRun(ctx context.Context, requested interviewapp.NotePreparation, run agentdomain.ModelRun) (interviewapp.NotePreparation, error) {
	result := requested
	err := repository.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := repository.lockLive(ctx, scope, requested, requested.NodeAttemptID); err != nil {
			return err
		}
		row, err := loadNotePreparation(tx, requested.WorkspaceID, requested.ID, true)
		if err != nil {
			return err
		}
		p, err := row.value()
		if err != nil {
			return err
		}
		result = p
		if p.Status != interviewapp.NotePreparationGenerating || p.NodeAttemptID != requested.NodeAttemptID || p.Version != requested.Version {
			return notePreparationConflict("note preparation model binding is stale")
		}
		stored, err := repository.dependencies.ModelRuns.GetModelRunScoped(ctx, scope, p.WorkspaceID, run.ID, true)
		if err != nil {
			return err
		}
		if interviewapp.ValidateNoteModelBinding(p, stored) != nil || stored.Status != agentdomain.ModelRunRunning || stored.Version != run.Version || stored.Model != run.Model || stored.Profile != run.Profile {
			return notePreparationConflict("note preparation model run is invalid")
		}
		if p.ModelRunID != "" {
			if p.ModelRunID != run.ID {
				return notePreparationConflict("note preparation already owns another model run")
			}
			return nil
		}
		p.ModelRunID = run.ID
		p.Version++
		p.UpdatedAt = noteCanonicalTime(repository.dependencies.Clock.Now(), p.UpdatedAt)
		if err := updateNotePreparation(tx, row, p); err != nil {
			return err
		}
		result = p
		return nil
	})
	return result, err
}

func (repository *GORMNotePreparationRepository) CompleteNoteGeneration(ctx context.Context, command interviewapp.NoteGenerationCompletion) (interviewapp.NotePreparation, error) {
	result := command.Preparation
	err := repository.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		p := command.Preparation
		if p.Validate() != nil || p.Status != interviewapp.NotePreparationGenerating || command.Execution.WorkspaceID != p.WorkspaceID || command.Execution.RunID != p.WorkflowRunID || command.Execution.NodeRunID != p.NodeRunID || command.Execution.NodeAttemptID != p.NodeAttemptID || !reflect.DeepEqual(command.Execution.ModelSettingsRevision, p.ModelSettingsRevision) {
			return notePreparationConflict("note preparation completion input is invalid")
		}
		if err := repository.lockLive(ctx, scope, p, p.NodeAttemptID); err != nil {
			return err
		}
		row, err := loadNotePreparation(tx, p.WorkspaceID, p.ID, true)
		if err != nil {
			return err
		}
		stored, err := row.value()
		if err != nil {
			return err
		}
		result = stored
		if stored.Status == interviewapp.NotePreparationReady {
			if stored.ModelRunID != command.ModelRun.ID || string(stored.ModelOutput) != string(command.Output) || stored.SessionID == nil || *stored.SessionID != command.Start.Session.ID {
				return notePreparationConflict("note preparation completion replay differs")
			}
			return nil
		}
		if stored.Status != interviewapp.NotePreparationGenerating || stored.Version != p.Version || stored.NodeAttemptID != p.NodeAttemptID || stored.ModelRunID != p.ModelRunID || stored.RequestHash != p.RequestHash {
			return notePreparationConflict("note preparation completion is stale")
		}
		plan, err := interviewapp.DecodeNotePlan(command.Output, stored)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(plan, command.Plan) {
			return notePreparationConflict("note preparation plan changed after validation")
		}
		start, err := interviewapp.BuildNoteInterviewStart(stored, plan, command.At)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(start, command.Start) || validateStartRecord(start) != nil {
			return notePreparationConflict("note preparation session does not match the model plan")
		}
		record, err := repository.dependencies.ModelRuns.GetModelRunRecordScoped(ctx, scope, p.WorkspaceID, p.ModelRunID, true)
		if err != nil {
			return err
		}
		if record.Run.Status != agentdomain.ModelRunRunning || record.Run.Version != command.ModelRun.Version {
			return notePreparationConflict("note preparation model result is already terminal")
		}
		if err := interviewapp.ValidateNoteModelOutput(stored, record, command.Output); err != nil {
			return err
		}
		terminal := record.Run
		terminal.Status = agentdomain.ModelRunSucceeded
		terminal.FinalResultType = agentdomain.ResultTypeSynthesisNoteInterviewPlan
		at := noteCanonicalTime(command.At, terminal.UpdatedAt)
		terminal.UpdatedAt = at
		terminal.CompletedAt = &at
		terminal.Version++
		if _, _, err := repository.dependencies.ModelRuns.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: record.Run.Version, Run: terminal}); err != nil {
			return err
		}
		stored.Status = interviewapp.NotePreparationReady
		stored.SessionID = &start.Session.ID
		stored.ModelOutput = append(json.RawMessage(nil), command.Output...)
		stored.Version++
		stored.UpdatedAt = at
		// The session FK is deferred. Questions can now verify their immutable
		// preparation while all session and model facts remain in one commit.
		if err := updateNotePreparation(tx, row, stored); err != nil {
			return err
		}
		started, err := gormInterviewStart(ctx, tx, start)
		if err != nil {
			return err
		}
		if started.Session.ID != start.Session.ID {
			return notePreparationConflict("note preparation started a different session")
		}
		result = stored
		return nil
	})
	return result, err
}

func (repository *GORMNotePreparationRepository) FailNoteGeneration(ctx context.Context, requested interviewapp.NotePreparation, code string, retryable, unknown bool, at time.Time) error {
	return repository.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		// Failure may follow cancellation or lease expiry, so lock the owner's
		// facts without requiring that the execution is still running.
		if requested.NodeAttemptID != "" {
			if _, found, err := repository.dependencies.Fence.LockToolExecutionPolicyScoped(ctx, scope, workflowapp.ToolExecutionPolicySnapshotRequest{WorkspaceID: requested.WorkspaceID, WorkflowRunID: requested.WorkflowRunID, NodeRunID: requested.NodeRunID, NodeAttemptID: requested.NodeAttemptID}); err != nil {
				return err
			} else if !found {
				return notePreparationConflict("note failure attempt is missing")
			}
		}
		if err := repository.checkBinding(ctx, scope, requested, requested.NodeAttemptID); err != nil {
			return err
		}
		row, err := loadNotePreparation(tx, requested.WorkspaceID, requested.ID, true)
		if err != nil {
			return err
		}
		p, err := row.value()
		if err != nil {
			return err
		}
		if p.NodeAttemptID != requested.NodeAttemptID {
			return notePreparationConflict("note failure attempt changed")
		}
		return repository.failScoped(ctx, scope, tx, p, code, retryable, unknown, at)
	})
}

func (repository *GORMNotePreparationRepository) failScoped(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, p interviewapp.NotePreparation, code string, retryable, unknown bool, at time.Time) error {
	if p.Status != interviewapp.NotePreparationQueued && p.Status != interviewapp.NotePreparationGenerating {
		return nil
	}
	row, err := notePreparationRow(p)
	if err != nil {
		return err
	}
	if p.NodeAttemptID != "" {
		run, found, err := repository.dependencies.ModelRuns.GetModelRunByAttemptScoped(ctx, scope, p.WorkspaceID, p.NodeAttemptID, true)
		if err != nil {
			return err
		}
		if found {
			if err := interviewapp.ValidateNoteModelBinding(p, run); err != nil {
				return err
			}
			p.ModelRunID = run.ID
			record, err := repository.dependencies.ModelRuns.GetModelRunRecordScoped(ctx, scope, p.WorkspaceID, run.ID, true)
			if err != nil {
				return err
			}
			hasActiveCall := false
			for _, call := range record.Calls {
				if call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
					unknown = true
					if call.Status == agentdomain.ModelCallStarted {
						hasActiveCall = true
					}
				}
			}
			if run.Status == agentdomain.ModelRunUnknown || run.Status == agentdomain.ModelRunSucceeded {
				unknown = true
			}
			if run.Status == agentdomain.ModelRunRunning && !hasActiveCall {
				terminal := run
				terminal.Status = agentdomain.ModelRunFailed
				if unknown {
					terminal.Status = agentdomain.ModelRunUnknown
				}
				terminal.FinalErrorCode = code
				terminal.Version++
				when := noteCanonicalTime(at, run.UpdatedAt)
				terminal.UpdatedAt = when
				terminal.CompletedAt = &when
				if _, _, err := repository.dependencies.ModelRuns.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: run.Version, Run: terminal}); err != nil {
					return err
				}
			}
		}
	}
	p.Status = interviewapp.NotePreparationFailed
	if code == interviewapp.ErrorCodeNotePlanUnavailable {
		p.Status = interviewapp.NotePreparationUnavailable
	}
	if unknown {
		p.Status = interviewapp.NotePreparationRecoveryRequired
		retryable = false
	}
	p.Failure = &interviewapp.NotePreparationFailure{Code: code, Retryable: retryable}
	p.Version++
	p.UpdatedAt = noteCanonicalTime(at, p.UpdatedAt)
	return updateNotePreparation(tx, row, p)
}

func (repository *GORMNotePreparationRepository) OnWorkflowNodeTerminalScoped(ctx context.Context, scope foundation.TransactionScope, event workflowapp.WorkflowNodeTerminalEvent) error {
	if event.NodeKind != interviewapp.NotePreparationNodeKind {
		return nil
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	binding, err := repository.dependencies.Bindings.LoadRuntimeBindingScoped(ctx, scope, workflowapp.ScopedRuntimeBindingQuery{WorkspaceID: event.WorkspaceID, WorkflowRunID: event.WorkflowRunID, NodeRunID: event.NodeRunID, NodeAttemptID: event.NodeAttemptID})
	if err != nil {
		return err
	}
	input, err := interviewworkflow.DecodeNotePreparationInput(binding.RunInput)
	if err != nil {
		return err
	}
	row, err := loadNotePreparation(tx, event.WorkspaceID, input.PreparationID, true)
	if err != nil {
		return err
	}
	p, err := row.value()
	if err != nil {
		return err
	}
	if p.WorkflowRunID != event.WorkflowRunID || p.NodeRunID != event.NodeRunID {
		return notePreparationConflict("note terminal event changed its preparation")
	}
	if err := repository.checkBinding(ctx, scope, p, event.NodeAttemptID); err != nil {
		return err
	}
	if event.Outcome == workflowapp.WorkflowTerminalOutcomeSucceeded {
		receipt, err := strictjson.DecodeObject[interviewworkflow.NotePreparationReceipt]([]byte(event.TerminalOutput), strictjson.DefaultLimits(), nil)
		if err != nil || p.Status != interviewapp.NotePreparationReady || p.SessionID == nil || receipt.PreparationID != p.ID || receipt.SessionID != *p.SessionID || receipt.SchemaVersion != interviewapp.NotePreparationSchemaVersion {
			return notePreparationConflict("note terminal output has no exact session receipt")
		}
		return nil
	}
	if event.Outcome != workflowapp.WorkflowTerminalOutcomeFailed && event.Outcome != workflowapp.WorkflowTerminalOutcomeCancelled {
		return notePreparationConflict("note terminal event outcome is invalid")
	}
	code := event.FailureCode
	if code == "" {
		code = "INTERVIEW_NOTE_WORKFLOW_FAILED"
	}
	if event.Outcome == workflowapp.WorkflowTerminalOutcomeCancelled {
		code = "INTERVIEW_NOTE_WORKFLOW_CANCELLED"
	}
	return repository.failScoped(ctx, scope, tx, p, code, true, event.FailureClass == workflowdomain.FailureClassManualRecovery, event.TerminalAt)
}

func updateNotePreparation(tx *gorm.DB, old notePreparationModel, p interviewapp.NotePreparation) error {
	if err := p.Validate(); err != nil {
		return err
	}
	row, err := notePreparationRow(p)
	if err != nil {
		return err
	}
	result := tx.Model(&notePreparationModel{}).Where("id=? AND workspace_id=? AND version=?", old.ID, old.WorkspaceID, old.Version).Updates(map[string]any{
		"status": row.Status, "node_attempt_id": row.NodeAttemptID, "model_run_id": row.ModelRunID, "model_settings_revision": row.ModelSettingsRevision,
		"model_output": row.ModelOutput, "session_id": row.SessionID, "failure_code": row.FailureCode, "failure_retryable": row.FailureRetryable, "version": row.Version, "updated_at": row.UpdatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return notePreparationConflict("note preparation version changed")
	}
	return nil
}
func noteCanonicalTime(value, minimum time.Time) time.Time {
	value = value.UTC().Truncate(time.Microsecond)
	if value.Before(minimum) {
		return minimum
	}
	return value
}
func noteGenerationRecovery() error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, interviewapp.ErrorCodeNoteRecoveryRequired, false, errors.New("note interview result requires explicit recovery"))
}

var _ workflowapp.ScopedWorkflowTerminalHook = (*GORMNotePreparationRepository)(nil)
