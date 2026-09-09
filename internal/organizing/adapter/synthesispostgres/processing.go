package synthesispostgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (store *Store) FindSynthesisProcessingScoped(ctx context.Context, scope foundation.TransactionScope, event organizingdomain.SynthesisSourceReady) (organizingapp.SynthesisProcessing, bool, error) {
	key, err := event.ProcessingKey()
	if err != nil {
		return organizingapp.SynthesisProcessing{}, false, err
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return organizingapp.SynthesisProcessing{}, false, err
	}
	var row processingModel
	err = tx.Where("workspace_id=? AND processing_key=?", string(event.Source.WorkspaceID), key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return organizingapp.SynthesisProcessing{}, false, nil
	}
	if err != nil {
		return organizingapp.SynthesisProcessing{}, false, classify(ctx, err)
	}
	result, err := row.projection()
	return result, true, err
}

func (store *Store) HasActiveSynthesisProcessingScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID foundation.ID) (bool, error) {
	if !validID(workspaceID) {
		return false, invalid("synthesis processing Workspace is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return false, err
	}
	// Serialize intake within a Workspace, not across the external model call.
	// The partial unique index is the final guard against a concurrent dispatcher.
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "synthesis-intake:"+string(workspaceID)).Error; err != nil {
		return false, classify(ctx, err)
	}
	var count int64
	err = tx.Model(&processingModel{}).Where("workspace_id=? AND status IN ?", string(workspaceID), []string{string(organizingapp.SynthesisProcessingPending), string(organizingapp.SynthesisProcessingRunning)}).Count(&count).Error
	return count > 0, classify(ctx, err)
}

func (store *Store) CreateSynthesisProcessingScoped(ctx context.Context, scope foundation.TransactionScope, command organizingworkflow.SynthesisCreateProcessing) error {
	p := command.Processing
	if p.Status != organizingapp.SynthesisProcessingPending || p.Version != 1 || !validID(p.ID) || p.WorkflowRunID != command.Start.Run.ID ||
		command.Start.Run.WorkspaceID != p.SourceEvent.Source.WorkspaceID || command.Start.Job.JobID < 1 || p.ModelRunID != "" || len(p.RevisionIDs) != 0 || p.Failure != nil || p.CompletedAt != nil || p.CreatedAt.IsZero() {
		return invalid("synthesis processing creation is invalid")
	}
	input, err := organizingworkflow.DecodeSynthesisStartInput(command.Start.Run.Input)
	if err != nil || input.ProcessingID != p.ID || input.ExecutionNo != 1 || input.ApplyRecovery {
		return invalid("synthesis processing start input changed")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return err
	}
	row, err := modelProcessing(p)
	if err != nil {
		return err
	}
	if err := tx.Create(&row).Error; err != nil {
		return classify(ctx, err)
	}
	execution := executionModel{WorkflowRunID: string(p.WorkflowRunID), WorkspaceID: string(p.SourceEvent.Source.WorkspaceID), ProcessingID: string(p.ID), ExecutionNo: 1,
		Version: 1, CreatedAt: canonical(p.CreatedAt), UpdatedAt: canonical(p.CreatedAt)}
	return classify(ctx, tx.Create(&execution).Error)
}

func (store *Store) RecordSkippedSynthesisSourceScoped(ctx context.Context, scope foundation.TransactionScope, processing organizingapp.SynthesisProcessing) error {
	if processing.Status != organizingapp.SynthesisProcessingSkipped || !validID(processing.ID) || processing.Version != 1 || processing.WorkflowRunID != "" || processing.ModelRunID != "" || processing.CompletedAt == nil || processing.Failure != nil || len(processing.RevisionIDs) != 0 {
		return invalid("synthesis skipped source receipt is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return err
	}
	row, err := modelProcessing(processing)
	if err != nil {
		return err
	}
	return classify(ctx, tx.Create(&row).Error)
}

func (store *Store) GetSynthesisProcessing(ctx context.Context, workspaceID, processingID foundation.ID) (organizingapp.SynthesisProcessing, error) {
	if err := store.ready(ctx); err != nil {
		return organizingapp.SynthesisProcessing{}, err
	}
	if !validID(workspaceID) || !validID(processingID) {
		return organizingapp.SynthesisProcessing{}, invalid("synthesis processing lookup is invalid")
	}
	row, err := loadProcessing(store.database.WithContext(ctx), workspaceID, processingID, false)
	if err != nil {
		return organizingapp.SynthesisProcessing{}, err
	}
	return row.projection()
}

func (store *Store) ListSynthesisProcessing(ctx context.Context, query organizingapp.SynthesisListQuery) (organizingapp.SynthesisProcessingPage, error) {
	if err := store.ready(ctx); err != nil {
		return organizingapp.SynthesisProcessingPage{}, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > organizingapp.MaxSynthesisListLimit || (query.BeforeTime == nil) != (query.BeforeID == "") || query.BeforeID != "" && !validID(query.BeforeID) {
		return organizingapp.SynthesisProcessingPage{}, invalid("synthesis processing page query is invalid")
	}
	statement := store.database.WithContext(ctx).Where("workspace_id=?", string(query.WorkspaceID)).Order("updated_at DESC,id DESC").Limit(query.Limit + 1)
	if query.BeforeTime != nil {
		statement = statement.Where("(updated_at,id) < (?,?)", canonical(*query.BeforeTime), string(query.BeforeID))
	}
	var rows []processingModel
	if err := statement.Find(&rows).Error; err != nil {
		return organizingapp.SynthesisProcessingPage{}, classify(ctx, err)
	}
	page := organizingapp.SynthesisProcessingPage{Items: make([]organizingapp.SynthesisProcessing, 0, min(len(rows), query.Limit))}
	if len(rows) > query.Limit {
		rows = rows[:query.Limit]
		page.NextTime = timePointer(rows[len(rows)-1].UpdatedAt)
		page.NextID = foundation.ID(rows[len(rows)-1].ID)
	}
	for _, row := range rows {
		value, err := row.projection()
		if err != nil {
			return organizingapp.SynthesisProcessingPage{}, err
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func (store *Store) LoadSynthesisExecution(ctx context.Context, workspaceID, processingID, runID foundation.ID) (organizingworkflow.SynthesisExecution, error) {
	if err := store.ready(ctx); err != nil {
		return organizingworkflow.SynthesisExecution{}, err
	}
	if !validID(workspaceID) || !validID(processingID) || !validID(runID) {
		return organizingworkflow.SynthesisExecution{}, invalid("synthesis execution lookup is invalid")
	}
	tx := store.database.WithContext(ctx)
	processing, err := loadProcessing(tx, workspaceID, processingID, false)
	if err != nil {
		return organizingworkflow.SynthesisExecution{}, err
	}
	row, err := loadExecution(tx, workspaceID, processingID, runID, false)
	if err != nil {
		return organizingworkflow.SynthesisExecution{}, err
	}
	projection, err := processing.projection()
	if err != nil {
		return organizingworkflow.SynthesisExecution{}, err
	}
	input, err := row.frozen()
	if err != nil {
		return organizingworkflow.SynthesisExecution{}, err
	}
	result := organizingworkflow.SynthesisExecution{Processing: projection, WorkflowRunID: runID, ExecutionNo: row.ExecutionNo, ApplyRecovery: row.ApplyRecovery, Input: input, CreatedAt: canonical(row.CreatedAt)}
	var steps []modelStepModel
	if err := tx.Where("workspace_id=? AND workflow_run_id=?", string(workspaceID), string(runID)).Order("created_at,id").Find(&steps).Error; err != nil {
		return organizingworkflow.SynthesisExecution{}, classify(ctx, err)
	}
	for _, step := range steps {
		value, err := step.projection()
		if err != nil {
			return organizingworkflow.SynthesisExecution{}, err
		}
		if value.Stage == organizingapp.SynthesisModelGenerate {
			result.Generation = &value
		} else {
			result.Semantic = &value
		}
	}
	if len(row.AppliedResult) > 0 {
		var applied organizingapp.SynthesisApplyResult
		if err := json.Unmarshal(row.AppliedResult, &applied); err != nil || applied.ProcessingID != processingID {
			return organizingworkflow.SynthesisExecution{}, invalid("synthesis application receipt cannot be decoded")
		}
		result.Applied = &applied
	}
	return result, nil
}

func (store *Store) FreezeSynthesisInput(ctx context.Context, execution workflowapp.ExecutionContext, input organizingworkflow.SynthesisFrozenInput) (organizingworkflow.SynthesisFrozenInput, error) {
	if input.Validate() != nil || input.WorkflowRunID != execution.RunID || input.SourceEvent.Source.WorkspaceID != execution.WorkspaceID || execution.NodeKind != organizingworkflow.SynthesisPrepareNodeKind {
		return organizingworkflow.SynthesisFrozenInput{}, invalid("synthesis input freeze is invalid")
	}
	var result organizingworkflow.SynthesisFrozenInput
	err := store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, _ *gorm.DB) error {
		tx, row, err := store.bindLive(ctx, scope, input.ProcessingID, execution)
		if err != nil {
			return err
		}
		if row.ApplyRecovery {
			return invalid("synthesis publication recovery cannot freeze new input")
		}
		if existing, err := row.frozen(); err != nil {
			return err
		} else if existing != nil {
			result = *existing
			return nil
		}
		processing, err := loadProcessing(tx, execution.WorkspaceID, input.ProcessingID, false)
		if err != nil {
			return err
		}
		projection, err := processing.projection()
		if err != nil {
			return err
		}
		if projection.SourceEvent != input.SourceEvent {
			return invalid("synthesis input changed the durable source event")
		}
		encoded, err := marshal(input)
		if err != nil {
			return err
		}
		updated := tx.Model(&executionModel{}).Where("workflow_run_id=? AND version=? AND input_document IS NULL", row.WorkflowRunID, row.Version).
			Updates(map[string]any{"input_document": encoded, "input_hash": input.RequestHash, "version": row.Version + 1, "updated_at": gorm.Expr("clock_timestamp()")})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis input freeze CAS failed")
		}
		updated = tx.Model(&processingModel{}).Where("id=? AND workspace_id=? AND version=?", processing.ID, processing.WorkspaceID, processing.Version).
			Updates(map[string]any{"status": string(organizingapp.SynthesisProcessingRunning), "version": processing.Version + 1, "updated_at": gorm.Expr("clock_timestamp()")})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis processing freeze CAS failed")
		}
		result = input
		return nil
	})
	return result, err
}

func (store *Store) PrepareSynthesisApplication(ctx context.Context, execution workflowapp.ExecutionContext, processingID foundation.ID) error {
	if execution.NodeKind != organizingworkflow.SynthesisApplyNodeKind {
		return invalid("synthesis application preparation has the wrong node")
	}
	return store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, _ *gorm.DB) error {
		tx, row, err := store.bindLive(ctx, scope, processingID, execution)
		if err != nil {
			return err
		}
		if row.AppliedResult != nil {
			return nil
		}
		if idValue(row.ApplyNodeRunID) == execution.NodeRunID && idValue(row.ApplyNodeAttemptID) == execution.NodeAttemptID {
			return nil
		}
		updated := tx.Model(&executionModel{}).Where("workflow_run_id=? AND version=?", row.WorkflowRunID, row.Version).
			Updates(map[string]any{"apply_node_run_id": string(execution.NodeRunID), "apply_node_attempt_id": string(execution.NodeAttemptID), "version": row.Version + 1, "updated_at": gorm.Expr("clock_timestamp()")})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis application attempt CAS failed")
		}
		return nil
	})
}

func (store *Store) RecordSynthesisApplication(ctx context.Context, execution workflowapp.ExecutionContext, result organizingapp.SynthesisApplyResult) error {
	if execution.NodeKind != organizingworkflow.SynthesisApplyNodeKind || !validID(result.ProcessingID) || result.Changed != (len(result.RevisionIDs) > 0) || len(result.RevisionIDs) > organizingapp.MaxSynthesisGeneratedNotes {
		return invalid("synthesis application result is invalid")
	}
	result.Replayed = false // replay disposition is not part of the immutable result.
	encoded, err := marshal(result)
	if err != nil {
		return err
	}
	return store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, _ *gorm.DB) error {
		tx, row, err := store.bindLive(ctx, scope, result.ProcessingID, execution)
		if err != nil {
			return err
		}
		if row.AppliedResult != nil {
			var existing organizingapp.SynthesisApplyResult
			if json.Unmarshal(row.AppliedResult, &existing) != nil {
				return invalid("synthesis applied result is invalid")
			}
			existing.Replayed = false
			prior, err := marshal(existing)
			if err != nil {
				return err
			}
			if string(prior) != string(encoded) {
				return conflict("synthesis application replay changed its result")
			}
			return nil
		}
		if idValue(row.ApplyNodeRunID) != execution.NodeRunID || idValue(row.ApplyNodeAttemptID) != execution.NodeAttemptID {
			return conflict("synthesis application is not reserved for this attempt")
		}
		updated := tx.Model(&executionModel{}).Where("workflow_run_id=? AND version=? AND applied_result IS NULL", row.WorkflowRunID, row.Version).
			Updates(map[string]any{"applied_result": encoded, "version": row.Version + 1, "updated_at": gorm.Expr("clock_timestamp()")})
		if updated.Error != nil {
			return classify(ctx, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conflict("synthesis application result CAS failed")
		}
		return nil
	})
}

func (store *Store) FindSynthesisRetryScoped(ctx context.Context, scope foundation.TransactionScope, command organizingapp.RetrySynthesisCommand, requestHash string) (organizingapp.RetrySynthesisResult, bool, error) {
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, false, err
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "synthesis-retry:"+string(command.WorkspaceID)+":"+command.IdempotencyKey).Error; err != nil {
		return organizingapp.RetrySynthesisResult{}, false, classify(ctx, err)
	}
	var row retryModel
	err = tx.Where("workspace_id=? AND idempotency_key=?", string(command.WorkspaceID), command.IdempotencyKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return organizingapp.RetrySynthesisResult{}, false, nil
	}
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, false, classify(ctx, err)
	}
	if row.RequestHash != requestHash || row.ProcessingID != string(command.ProcessingID) {
		return organizingapp.RetrySynthesisResult{}, false, conflict("synthesis retry key belongs to another request")
	}
	var result organizingapp.RetrySynthesisResult
	if json.Unmarshal(row.Response, &result) != nil || result.Processing.ID != command.ProcessingID || string(result.Processing.WorkflowRunID) != row.WorkflowRunID {
		return organizingapp.RetrySynthesisResult{}, false, invalid("synthesis retry receipt is invalid")
	}
	result.Replayed = true
	return result, true, nil
}

func (store *Store) LockSynthesisProcessingForRetryScoped(ctx context.Context, scope foundation.TransactionScope, command organizingapp.RetrySynthesisCommand) (organizingapp.SynthesisProcessing, int, error) {
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return organizingapp.SynthesisProcessing{}, 0, err
	}
	active, err := store.HasActiveSynthesisProcessingScoped(ctx, scope, command.WorkspaceID)
	if err != nil {
		return organizingapp.SynthesisProcessing{}, 0, err
	}
	if active {
		return organizingapp.SynthesisProcessing{}, 0, conflict("another synthesis source is still running in this Workspace")
	}
	row, err := loadProcessing(tx, command.WorkspaceID, command.ProcessingID, true)
	if err != nil {
		return organizingapp.SynthesisProcessing{}, 0, err
	}
	if row.Version != command.ExpectedVersion {
		return organizingapp.SynthesisProcessing{}, 0, conflict("synthesis processing version changed")
	}
	if row.Status != string(organizingapp.SynthesisProcessingFailed) || !row.Retryable {
		return organizingapp.SynthesisProcessing{}, 0, foundation.NewError(foundation.ErrorVersionConflict, organizingworkflow.ErrorCodeSynthesisRetryUnsafe, false, errors.New("synthesis processing cannot be retried safely"))
	}
	var latest executionModel
	if err := tx.Where("workspace_id=? AND processing_id=?", row.WorkspaceID, row.ID).Order("execution_no DESC").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&latest).Error; err != nil {
		return organizingapp.SynthesisProcessing{}, 0, classify(ctx, err)
	}
	if latest.ExecutionNo >= organizingworkflow.SynthesisMaxAttempts {
		return organizingapp.SynthesisProcessing{}, 0, foundation.NewError(foundation.ErrorNonRetryableFailure, organizingworkflow.ErrorCodeSynthesisExecutionBudget, false, errors.New("synthesis source retry budget is exhausted"))
	}
	var uncertain int64
	if err := tx.Model(&modelStepModel{}).Where("workspace_id=? AND processing_id=? AND status IN ?", row.WorkspaceID, row.ID, []string{string(organizingapp.SynthesisModelStepRunning), string(organizingapp.SynthesisModelStepRecoveryRequired)}).Count(&uncertain).Error; err != nil {
		return organizingapp.SynthesisProcessing{}, 0, classify(ctx, err)
	}
	if uncertain > 0 {
		return organizingapp.SynthesisProcessing{}, 0, recovery("synthesis source has an unresolved model attempt")
	}
	processing, err := row.projection()
	return processing, latest.ExecutionNo + 1, err
}

func (store *Store) RecordSynthesisRetryScoped(ctx context.Context, scope foundation.TransactionScope, record organizingworkflow.SynthesisRetryRecord) (organizingapp.RetrySynthesisResult, error) {
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, err
	}
	command := record.Command
	row, err := loadProcessing(tx, command.WorkspaceID, command.ProcessingID, true)
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, err
	}
	input, err := organizingworkflow.DecodeSynthesisStartInput(record.Start.Run.Input)
	if err != nil || input.ProcessingID != command.ProcessingID || input.ApplyRecovery != record.ApplyRecovery || record.Start.Run.WorkspaceID != command.WorkspaceID || record.Start.Job.JobID < 1 || row.Version != command.ExpectedVersion || row.Status != string(organizingapp.SynthesisProcessingFailed) || !row.Retryable {
		return organizingapp.RetrySynthesisResult{}, conflict("synthesis retry binding changed")
	}
	updated := tx.Model(&processingModel{}).Where("id=? AND workspace_id=? AND version=?", row.ID, row.WorkspaceID, row.Version).
		Updates(map[string]any{"workflow_run_id": string(record.Start.Run.ID), "model_run_id": nil, "status": string(organizingapp.SynthesisProcessingPending), "error_code": nil, "retryable": false, "completed_at": nil, "version": row.Version + 1, "updated_at": canonical(record.CreatedAt)})
	if updated.Error != nil {
		return organizingapp.RetrySynthesisResult{}, classify(ctx, updated.Error)
	}
	if updated.RowsAffected != 1 {
		return organizingapp.RetrySynthesisResult{}, conflict("synthesis retry CAS failed")
	}
	execution := executionModel{WorkflowRunID: string(record.Start.Run.ID), WorkspaceID: row.WorkspaceID, ProcessingID: row.ID, ExecutionNo: input.ExecutionNo, ApplyRecovery: record.ApplyRecovery, Version: 1, CreatedAt: canonical(record.CreatedAt), UpdatedAt: canonical(record.CreatedAt)}
	if err := tx.Create(&execution).Error; err != nil {
		return organizingapp.RetrySynthesisResult{}, classify(ctx, err)
	}
	row.WorkflowRunID = optionalID(record.Start.Run.ID)
	row.ModelRunID = nil
	row.Status = string(organizingapp.SynthesisProcessingPending)
	row.ErrorCode = nil
	row.Retryable = false
	row.CompletedAt = nil
	row.Version++
	row.UpdatedAt = canonical(record.CreatedAt)
	processing, err := row.projection()
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, err
	}
	result := organizingapp.RetrySynthesisResult{Processing: processing}
	response, err := marshal(result)
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, err
	}
	receipt := retryModel{WorkspaceID: row.WorkspaceID, IdempotencyKey: command.IdempotencyKey, RequestHash: record.RequestHash, ProcessingID: row.ID, WorkflowRunID: execution.WorkflowRunID, Response: response, CreatedAt: canonical(record.CreatedAt)}
	if err := tx.Create(&receipt).Error; err != nil {
		return organizingapp.RetrySynthesisResult{}, classify(ctx, err)
	}
	return result, nil
}

var _ organizingworkflow.SynthesisProcessingStore = (*Store)(nil)
var _ organizingworkflow.SynthesisRetryStore = (*Store)(nil)
