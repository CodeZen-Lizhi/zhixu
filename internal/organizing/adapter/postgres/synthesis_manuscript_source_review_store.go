package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMSynthesisManuscriptSourceReviewStore struct {
	control     *workflowapp.RuntimeCoordinator
	starter     workflowapp.ScopedRuntimeStarter
	definitions *workflowapp.DefinitionRegistry
	runtime     *SynthesisManuscriptRuntime
	runs        agentapp.ScopedModelRunFinalizer
	fence       workflowapp.ScopedWorkspaceAnalysisExecutionFence
	mapper      app.SynthesisSourceReviewParagraphMapper
}

func NewGORMSynthesisManuscriptSourceReviewStore(runtime *SynthesisManuscriptRuntime, runs agentapp.ScopedModelRunFinalizer, mapper app.SynthesisSourceReviewParagraphMapper) (*GORMSynthesisManuscriptSourceReviewStore, error) {
	if runtime == nil || isNilInterface(runs) || isNilInterface(mapper) {
		return nil, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(runtime.dependencies.Pool)
	if err != nil {
		return nil, err
	}
	return &GORMSynthesisManuscriptSourceReviewStore{runtime: runtime, runs: runs, fence: fence, mapper: mapper}, nil
}

type sourceReviewRow struct {
	ID, WorkspaceID, OriginProcessingID, OriginWorkflowRunID string
	AttemptNo                                                int
	SupersedesID                                             *string
	WorkflowRunID                                            *string
	Status                                                   string
	Version                                                  int64
	Snapshot                                                 []byte
	SnapshotHash                                             *string
	NodeRunID, NodeAttemptID, ModelRunID, RequestHash        *string
	Output                                                   []byte
	ReceiptHash, ErrorCode                                   *string
	Retryable                                                bool
	CreatedAt                                                time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt                                                time.Time `gorm:"autoUpdateTime:false"`
	CompletedAt                                              *time.Time
}

func (sourceReviewRow) TableName() string { return "organizing.synthesis_manuscript_source_review" }
func (r sourceReviewRow) project() (app.SynthesisManuscriptSourceReview, error) {
	out := app.SynthesisManuscriptSourceReview{AttemptNo: r.AttemptNo, SupersedesID: foundation.ID(stringValue(r.SupersedesID)), ID: foundation.ID(r.ID), WorkspaceID: foundation.ID(r.WorkspaceID), OriginProcessingID: foundation.ID(r.OriginProcessingID), OriginWorkflowRunID: foundation.ID(r.OriginWorkflowRunID), WorkflowRunID: foundation.ID(stringValue(r.WorkflowRunID)), Status: r.Status, Version: r.Version, CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt, ErrorCode: stringValue(r.ErrorCode), Retryable: r.Retryable, SnapshotHash: stringValue(r.SnapshotHash), ModelRunID: foundation.ID(stringValue(r.ModelRunID)), NodeRunID: foundation.ID(stringValue(r.NodeRunID)), NodeAttemptID: foundation.ID(stringValue(r.NodeAttemptID)), RequestHash: stringValue(r.RequestHash), Output: bytes.Clone(r.Output), ReceiptHash: stringValue(r.ReceiptHash)}
	if len(r.Snapshot) > 0 {
		if sha256Hex(r.Snapshot) != out.SnapshotHash {
			return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CORRUPT")
		}
		out.Snapshot = &app.SynthesisSourceReviewSnapshot{}
		if err := json.Unmarshal(r.Snapshot, out.Snapshot); err != nil {
			return out, err
		}
	}
	return out, nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) within(ctx context.Context, work func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	return s.runtime.dependencies.Candidates.within(ctx, foundation.TransactionOptions{}, work)
}
func readSourceReview(tx *gorm.DB, w, id foundation.ID, lock bool) (sourceReviewRow, error) {
	var row sourceReviewRow
	q := tx.Where("workspace_id=? AND id=?", string(w), string(id))
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, synthesisNotFound()
	}
	return row, err
}
func (s *GORMSynthesisManuscriptSourceReviewStore) GetSourceReview(ctx context.Context, w, id foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	if !validID(w) || !validID(id) {
		return app.SynthesisManuscriptSourceReview{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ID_INVALID")
	}
	r, e := readSourceReview(s.runtime.dependencies.Candidates.database.WithContext(ctx), w, id, false)
	if e != nil {
		return app.SynthesisManuscriptSourceReview{}, e
	}
	return r.project()
}
func (s *GORMSynthesisManuscriptSourceReviewStore) live(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, e workflowapp.ExecutionContext, row sourceReviewRow) error {
	if e.NodeKind == app.SynthesisSourceReviewRecover {
		return s.liveSourceReviewRecovery(ctx, scope, tx, e, row)
	}
	if e.WorkspaceID != foundation.ID(row.WorkspaceID) || e.RunID != foundation.ID(stringValue(row.WorkflowRunID)) || e.DefinitionVersion != 1 || e.NodeKey != e.NodeKind || e.InputSchemaVersion != 1 || (e.NodeKind != app.SynthesisSourceReviewPrepare && e.NodeKind != app.SynthesisSourceReviewModel && e.NodeKind != app.SynthesisSourceReviewApply) {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	f, found, err := s.fence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, workflowapp.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: e.WorkspaceID, WorkflowRunID: e.RunID, NodeRunID: e.NodeRunID, NodeAttemptID: e.NodeAttemptID})
	if err != nil {
		return err
	}
	var now time.Time
	if err = tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return err
	}
	if !found || f.DefinitionID != e.DefinitionID || f.DefinitionVersion != e.DefinitionVersion || f.NodeKey != e.NodeKey || f.WorkflowStatus != workflowdomain.RunStatusRunning || f.CancelRequested || f.PauseRequested || f.NodeStatus != workflowdomain.NodeStatusRunning || f.AttemptStatus != workflowdomain.AttemptStatusRunning || f.AttemptNo != e.AttemptNo || f.NodeAttempt != f.AttemptNo || f.AttemptLeaseOwner != e.LeaseOwner || f.NodeLeaseOwner != f.AttemptLeaseOwner || !f.NodeLeaseUntil.After(now) || !f.AttemptLeaseUntil.After(now) || now.Sub(row.CreatedAt) > 30*time.Minute {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	var bound bool
	if err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id WHERE r.id=? AND r.workspace_id=? AND d.key=? AND d.version=1 AND r.input->>'review_id'=?)`, row.WorkflowRunID, row.WorkspaceID, app.SynthesisSourceReviewDefinition, row.ID).Scan(&bound).Error; err != nil {
		return err
	}
	if !bound {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	return nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) update(tx *gorm.DB, row sourceReviewRow, values map[string]any) error {
	values["version"] = row.Version + 1
	values["updated_at"] = canonicalTime(s.runtime.dependencies.Storage.Clock.Now())
	if completed, ok := values["completed_at"]; ok {
		values["updated_at"] = completed
	}
	q := tx.Model(&sourceReviewRow{}).Where("id=? AND workspace_id=? AND version=?", row.ID, row.WorkspaceID, row.Version).Updates(values)
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected != 1 {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CONFLICT")
	}
	return nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) PrepareSourceReview(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := lockSourceReviewExecutionRow(tx, e, id)
		if err != nil {
			return err
		}
		if e.NodeKind != app.SynthesisSourceReviewPrepare {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
		}
		if err = s.live(ctx, scope, tx, e, row); err != nil {
			return err
		}
		if row.Status != "PENDING" {
			if len(row.Snapshot) > 0 {
				return nil
			}
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CONFLICT")
		}
		input, err := s.current(ctx, scope, tx, row)
		if err != nil {
			return err
		}
		payload, err := app.BuildSourceReviewPayload(input)
		if err != nil {
			return err
		}
		if len(payload) > agentapp.MaxStructuredInputBytes {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_INPUT_TOO_LARGE")
		}
		raw, err := json.Marshal(input.Snapshot)
		if err != nil {
			return err
		}
		return s.update(tx, row, map[string]any{"status": "PREPARED", "snapshot": raw, "snapshot_hash": sha256Hex(raw)})
	})
	if err != nil {
		return app.SynthesisManuscriptSourceReview{}, err
	}
	return s.GetSourceReview(ctx, e.WorkspaceID, id)
}
func (s *GORMSynthesisManuscriptSourceReviewStore) ReadSourceReviewInput(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID) (app.SynthesisSourceReviewInput, error) {
	var out app.SynthesisSourceReviewInput
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := lockSourceReviewExecutionRow(tx, e, id)
		if err != nil {
			return err
		}
		if e.NodeKind != app.SynthesisSourceReviewModel {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
		}
		if err = s.live(ctx, scope, tx, e, row); err != nil {
			return err
		}
		out, err = s.recheck(ctx, scope, tx, row)
		return err
	})
	return out, err
}
func (s *GORMSynthesisManuscriptSourceReviewStore) recheck(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row sourceReviewRow) (app.SynthesisSourceReviewInput, error) {
	value, err := row.project()
	if err != nil {
		return app.SynthesisSourceReviewInput{}, err
	}
	if value.Snapshot == nil {
		return app.SynthesisSourceReviewInput{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_NOT_PREPARED")
	}
	current, err := s.current(ctx, scope, tx, row)
	if err != nil {
		return current, err
	}
	if !reflect.DeepEqual(current.Snapshot, *value.Snapshot) {
		return current, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_TEXT")
	}
	return current, nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) ClaimSourceReview(ctx context.Context, e workflowapp.ExecutionContext, id, modelID foundation.ID, hash string) (app.SynthesisManuscriptSourceReview, error) {
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := lockSourceReviewExecutionRow(tx, e, id)
		if err != nil {
			return err
		}
		if e.NodeKind != app.SynthesisSourceReviewModel || !validID(modelID) || len(hash) != 64 {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
		}
		if err = s.live(ctx, scope, tx, e, row); err != nil {
			return err
		}
		if row.Status != "PREPARED" {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED")
		}
		if _, err = s.recheck(ctx, scope, tx, row); err != nil {
			return err
		}
		return s.update(tx, row, map[string]any{"status": "RUNNING", "model_run_id": string(modelID), "node_run_id": string(e.NodeRunID), "node_attempt_id": string(e.NodeAttemptID), "request_hash": hash})
	})
	if err != nil {
		return app.SynthesisManuscriptSourceReview{}, err
	}
	return s.GetSourceReview(ctx, e.WorkspaceID, id)
}
func (s *GORMSynthesisManuscriptSourceReviewStore) CompleteSourceReview(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID, output []byte) (app.SynthesisManuscriptSourceReview, error) {
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := lockSourceReviewExecutionRow(tx, e, id)
		if err != nil {
			return err
		}
		if e.NodeKind != app.SynthesisSourceReviewModel {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
		}
		if err = s.live(ctx, scope, tx, e, row); err != nil {
			return err
		}
		if len(row.Output) > 0 {
			if bytes.Equal(row.Output, output) {
				return nil
			}
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CONFLICT")
		}
		if row.Status != "RUNNING" || row.NodeAttemptID == nil || *row.NodeAttemptID != string(e.NodeAttemptID) {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED")
		}
		current, err := s.recheck(ctx, scope, tx, row)
		if err != nil {
			return err
		}
		_, accepted, err := app.BindSourceReviewOutput(output, current.Snapshot)
		if err != nil {
			return err
		}
		record, err := s.verifyModel(ctx, scope, row, output, false)
		if err != nil {
			return err
		}
		run := record.Run
		now := canonicalTime(s.runtime.dependencies.Storage.Clock.Now())
		if now.Before(run.CreatedAt) {
			now = run.CreatedAt
		}
		run.Status = agentdomain.ModelRunSucceeded
		run.FinalResultType = agentdomain.ResultTypeSynthesisManuscriptSourceReview
		run.UpdatedAt = now
		run.CompletedAt = &now
		run.Version++
		if _, _, err = s.runs.FinalizeModelRunScoped(ctx, scope, agentapp.FinalizeModelRunCommand{ExpectedVersion: run.Version - 1, Run: run}); err != nil {
			return err
		}
		status := "REVIEWED"
		values := map[string]any{"status": status, "output": bytes.Clone(output)}
		if !accepted {
			values["status"] = "REJECTED"
			values["completed_at"] = now
			values["error_code"] = "SYNTHESIS_SOURCE_REVIEW_UNSUPPORTED"
		}
		return s.update(tx, row, values)
	})
	if err != nil {
		return app.SynthesisManuscriptSourceReview{}, err
	}
	return s.GetSourceReview(ctx, e.WorkspaceID, id)
}
func (s *GORMSynthesisManuscriptSourceReviewStore) verifyModel(ctx context.Context, scope foundation.TransactionScope, row sourceReviewRow, output []byte, terminal bool) (agentapp.ModelRunRecord, error) {
	record, err := s.runs.GetModelRunRecordScoped(ctx, scope, foundation.ID(row.WorkspaceID), foundation.ID(stringValue(row.ModelRunID)), true)
	if err != nil {
		return record, err
	}
	r := record.Run
	schema := agentdomain.SchemaRef{ID: app.SynthesisSourceReviewSchema, Version: "v1"}
	prompt := agentdomain.PromptRef{ID: schema.ID, Version: schema.Version}
	validStatus := r.Status == agentdomain.ModelRunRunning
	if terminal {
		validStatus = r.Status == agentdomain.ModelRunSucceeded && r.FinalResultType == agentdomain.ResultTypeSynthesisManuscriptSourceReview
	}
	if !validStatus || r.WorkspaceID != foundation.ID(row.WorkspaceID) || r.WorkflowRunID != foundation.ID(stringValue(row.WorkflowRunID)) || r.NodeRunID != foundation.ID(stringValue(row.NodeRunID)) || r.NodeAttemptID != foundation.ID(stringValue(row.NodeAttemptID)) || r.Schema != schema || r.ReducedSchema != schema || r.Prompt != prompt || r.Retrieval.IsBound() || r.MemoryContext.IsBound() || len(record.Calls) < 1 || len(record.Calls) > agentapp.StructuredCallLimit {
		return record, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_MODEL_PROOF_INVALID")
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for i, c := range record.Calls {
		if agentdomain.ValidateModelCall(c) != nil || c.ModelRunID != r.ID || c.CallNo != i+1 || c.Phase != phases[i] || c.Status != agentdomain.ModelCallSucceeded || c.Model != r.Model || c.Profile != r.Profile || c.Prompt != prompt || c.Schema != schema {
			return record, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_MODEL_PROOF_INVALID")
		}
	}
	last := record.Calls[len(record.Calls)-1]
	if record.Calls[0].RequestHash != stringValue(row.RequestHash) || last.ResponseHash != sha256Hex(output) || last.ResponseBytes != int64(len(output)) {
		return record, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_MODEL_PROOF_INVALID")
	}
	return record, nil
}
