package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SynthesisManuscriptRuntimeDependencies struct {
	Pool       *platformpostgres.Pool
	Candidates *GORMSynthesisStore
	Service    app.SynthesisDependencies
	Models     SynthesisManuscriptModelProof
	Executions interface {
		LoadSynthesisExecution(context.Context, foundation.ID, foundation.ID, foundation.ID) (organizingworkflow.SynthesisExecution, error)
	}
	Roots   app.SynthesisManuscriptRootReader
	Storage SynthesisManuscriptStoreDependencies
}

type SynthesisManuscriptRuntime struct {
	dependencies SynthesisManuscriptRuntimeDependencies
}

func NewSynthesisManuscriptRuntime(d SynthesisManuscriptRuntimeDependencies) (*SynthesisManuscriptRuntime, error) {
	if d.Pool == nil || d.Candidates == nil || isNilInterface(d.Models) || isNilInterface(d.Executions) || isNilInterface(d.Roots) {
		return nil, manuscriptStoreInvalid("manuscript runtime dependencies are required")
	}
	for _, dependency := range []any{d.Storage.Files, d.Storage.Mapper, d.Storage.Merge, d.Storage.IDs, d.Storage.Clock, d.Service.Sources, d.Service.Publications, d.Service.IDs, d.Service.Clock} {
		if isNilInterface(dependency) {
			return nil, manuscriptStoreInvalid("manuscript runtime owner dependency is missing")
		}
	}
	return &SynthesisManuscriptRuntime{dependencies: d}, nil
}

func (r *SynthesisManuscriptRuntime) invocation(ctx context.Context, execution workflowapp.ExecutionContext, input app.SynthesisGenerationInput, generation app.SynthesisGenerationResult) (*GORMSynthesisManuscriptStore, error) {
	loaded, err := r.dependencies.Executions.LoadSynthesisExecution(ctx, execution.WorkspaceID, input.ProcessingID, execution.RunID)
	if err != nil {
		return nil, err
	}
	if loaded.Semantic == nil || loaded.Semantic.Semantic == nil {
		return nil, manuscriptStoreInvalid("manuscript runtime has no semantic proof")
	}
	baseline := &synthesisManuscriptBaseline{store: r.dependencies.Candidates, models: r.dependencies.Models, roots: r.dependencies.Roots, execution: execution, input: input, generation: generation, semantic: *loaded.Semantic.Semantic}
	d := r.dependencies.Storage
	d.Baselines = baseline
	d.Proof = baseline
	return NewGORMSynthesisManuscriptStore(r.dependencies.Pool, d)
}

func manuscriptRuntimeKey(input app.SynthesisGenerationInput, note foundation.ID) string {
	return "manuscript-runtime:" + string(input.ProcessingID) + ":" + string(input.WorkflowRunID) + ":" + string(note)
}

func manuscriptChangedNotes(input app.SynthesisGenerationInput, generation app.SynthesisGenerationResult) ([]foundation.ID, error) {
	notes := []foundation.ID{}
	for _, generated := range generation.Notes {
		if generated.NoteID == "" {
			continue
		}
		for _, base := range input.Notes {
			if base.Note.ID == generated.NoteID {
				delta, err := app.SynthesisMachineDelta(input, generated, &base.Revision)
				if err != nil {
					return nil, err
				}
				if delta.Changed {
					notes = append(notes, base.Note.ID)
				}
			}
		}
	}
	return notes, nil
}

func (r *SynthesisManuscriptRuntime) PrepareManuscripts(ctx context.Context, execution workflowapp.ExecutionContext, input app.SynthesisGenerationInput, generation app.SynthesisGenerationResult) (*workflowapp.HumanWaitResult, error) {
	if execution.NodeKind != organizingworkflow.SynthesisMergeReviewNodeKind || execution.DefinitionVersion != organizingworkflow.SynthesisManuscriptDefinitionVersion {
		return nil, manuscriptStoreInvalid("manuscript preparation requires the merge execution")
	}
	notes, err := manuscriptChangedNotes(input, generation)
	if err != nil {
		return nil, err
	}
	if len(notes) == 0 {
		return nil, nil
	}
	store, err := r.invocation(ctx, execution, input, generation)
	if err != nil {
		return nil, err
	}
	conflict := false
	for _, note := range notes {
		attempt, err := store.Prepare(ctx, app.PrepareSynthesisManuscript{WorkspaceID: execution.WorkspaceID, NoteID: note, ProcessingID: input.ProcessingID, IdempotencyKey: manuscriptRuntimeKey(input, note)})
		if err != nil {
			return nil, err
		}
		if attempt.Preview.Review != nil {
			conflict = true
			continue
		}
		if _, err = store.SealClean(ctx, execution.WorkspaceID, attempt.ID); err != nil {
			return nil, err
		}
	}
	if conflict {
		taskID, err := r.dependencies.Storage.IDs.New()
		if err != nil {
			return nil, err
		}
		schema, err := app.SynthesisManuscriptHumanSchema(input.ProcessingID, input.WorkflowRunID, notes)
		if err != nil {
			return nil, err
		}
		return &workflowapp.HumanWaitResult{TaskID: taskID, ExpectedInputSchema: schema, TargetVersion: 1, ExpiresIn: 24 * time.Hour}, nil
	}
	return nil, nil
}

func (r *SynthesisManuscriptRuntime) ApplyManuscripts(ctx context.Context, execution workflowapp.ExecutionContext, input app.SynthesisGenerationInput, generation app.SynthesisGenerationResult) (app.SynthesisApplyResult, error) {
	if execution.NodeKind != organizingworkflow.SynthesisApplyNodeKind || execution.DefinitionVersion != organizingworkflow.SynthesisManuscriptDefinitionVersion {
		return app.SynthesisApplyResult{}, manuscriptStoreInvalid("manuscript candidate requires the apply execution")
	}
	notes, err := manuscriptChangedNotes(input, generation)
	if err != nil {
		return app.SynthesisApplyResult{}, err
	}
	store, err := r.invocation(ctx, execution, input, generation)
	if err != nil {
		return app.SynthesisApplyResult{}, err
	}
	candidates := make([]app.SynthesisManuscriptCandidate, 0, len(notes))
	manifest := app.SynthesisManuscriptReviewManifest{Ready: true}
	for _, note := range notes {
		// Apply 不能补建缺失的合并尝试，也不能静默封存冲突。
		var row manuscriptAttemptRow
		if err = store.db.WithContext(ctx).Where("workspace_id=? AND idempotency_key=?", string(execution.WorkspaceID), manuscriptRuntimeKey(input, note)).Take(&row).Error; err != nil {
			return app.SynthesisApplyResult{}, manuscriptReadError(err)
		}
		attempt, err := store.GetAttempt(ctx, execution.WorkspaceID, foundation.ID(row.ID))
		if err != nil {
			return app.SynthesisApplyResult{}, err
		}
		var receiptRow manuscriptReceiptRow
		if err = store.db.WithContext(ctx).Where("workspace_id=? AND attempt_id=?", string(execution.WorkspaceID), string(attempt.ID)).Take(&receiptRow).Error; err != nil {
			return app.SynthesisApplyResult{}, manuscriptReadError(err)
		}
		receipt, err := store.decodeReceipt(receiptRow, attempt)
		if err != nil {
			return app.SynthesisApplyResult{}, err
		}
		candidates = append(candidates, app.SynthesisManuscriptCandidate{NoteID: note, ReceiptID: receipt.ID, ReceiptHash: receipt.Hash})
		manifest.Targets = append(manifest.Targets, app.SynthesisManuscriptReviewTarget{Attempt: attempt, Preview: attempt.Preview, Receipt: &receipt})
		if receipt.Review != nil {
			binding := receipt.Review.Decisions[0].Command.Binding
			if manifest.Binding.HumanTaskID != "" && manifest.Binding != binding {
				return app.SynthesisApplyResult{}, manuscriptStoreConflict("receipts belong to different human tasks")
			}
			manifest.Binding = binding
		}
	}
	if manifest.Binding.HumanTaskID != "" {
		if manifest.Binding.WorkspaceID != execution.WorkspaceID || manifest.Binding.RunID != execution.RunID || manifest.Binding.ProcessingID != input.ProcessingID {
			return app.SynthesisApplyResult{}, manuscriptStoreConflict("human result belongs to another execution")
		}
		expected, err := app.SynthesisManuscriptOwnerResult(manifest)
		if err != nil {
			return app.SynthesisApplyResult{}, err
		}
		var task struct {
			Decision      []byte
			TargetVersion int64
		}
		row := store.db.WithContext(ctx).Raw(`SELECT h.decision,h.target_version FROM workflow.human_task h JOIN workflow.node_run n ON n.id=h.node_run_id AND n.run_id=h.run_id WHERE h.id=? AND h.run_id=? AND h.node_run_id=? AND h.status='submitted' AND n.status='succeeded'`, string(manifest.Binding.HumanTaskID), string(execution.RunID), string(manifest.Binding.NodeRunID)).Scan(&task)
		if row.Error != nil {
			return app.SynthesisApplyResult{}, row.Error
		}
		var actual, want any
		if row.RowsAffected != 1 || task.TargetVersion != manifest.Binding.TargetVersion || json.Unmarshal(task.Decision, &actual) != nil || json.Unmarshal(expected, &want) != nil || !reflect.DeepEqual(actual, want) {
			return app.SynthesisApplyResult{}, manuscriptStoreConflict("submitted task does not identify the actual owner receipts")
		}
	}
	// 每次调用使用副本，避免并发运行时修改共享所属模块对象。
	owner := *r.dependencies.Candidates
	owner.dependencies.Manuscripts = store
	serviceDeps := r.dependencies.Service
	serviceDeps.Store = &owner
	service, err := app.NewSynthesisService(serviceDeps)
	if err != nil {
		return app.SynthesisApplyResult{}, err
	}
	return service.ApplyManuscriptGeneration(ctx, input, generation, candidates)
}
