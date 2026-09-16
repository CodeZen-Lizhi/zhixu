package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// 每个实例属于一次已认证调用；人工决定不使用可变共享主体、伪造 ExecutionContext 或工作流租约。
type synthesisManuscriptHuman struct {
	runtime *SynthesisManuscriptRuntime
	caller  app.SynthesisManuscriptCaller
}

func (r *SynthesisManuscriptRuntime) HumanReview(caller app.SynthesisManuscriptCaller, coordinator *workflowapp.RuntimeHumanCoordinator) (*app.SynthesisManuscriptReviewService, error) {
	if err := caller.Authorize(); err != nil {
		return nil, err
	}
	h := &synthesisManuscriptHuman{runtime: r, caller: caller}
	d := r.dependencies.Storage
	d.Baselines = h
	d.Proof = h
	store, err := NewGORMSynthesisManuscriptStore(r.dependencies.Pool, d)
	if err != nil {
		return nil, err
	}
	reviews, err := NewGORMSynthesisManuscriptReviewStore(store, h)
	if err != nil {
		return nil, err
	}
	return app.NewSynthesisManuscriptReviewService(reviews, h, coordinator, caller)
}

func (h *synthesisManuscriptHuman) ReadSynthesisManuscriptHumanTask(ctx context.Context, workspace, processing foundation.ID) (app.SynthesisManuscriptHumanBinding, bool, error) {
	var b app.SynthesisManuscriptHumanBinding
	var submitted bool
	err := h.runtime.dependencies.Candidates.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		var row struct {
			TaskID        string
			RunID         string
			NodeRunID     string
			TargetVersion int64
			Status        string
		}
		result := tx.Raw(`SELECT h.id AS task_id,h.run_id,h.node_run_id,h.target_version,h.status
   FROM organizing.synthesis_processing p JOIN workflow.run r ON r.id=p.workflow_run_id AND r.workspace_id=p.workspace_id
   JOIN workflow.node_run n ON n.run_id=r.id AND n.node_key=? JOIN workflow.human_task h ON h.node_run_id=n.id AND h.run_id=r.id
   WHERE p.workspace_id=? AND p.id=? AND h.status IN ('pending','submitted')`, organizingworkflow.SynthesisMergeReviewNodeKind, string(workspace), string(processing)).Scan(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorNotFound, "SYNTHESIS_MANUSCRIPT_REVIEW_NOT_PENDING", false, errors.New("processing has no active manuscript review task"))
		}
		b = app.SynthesisManuscriptHumanBinding{WorkspaceID: workspace, ProcessingID: processing, HumanTaskID: foundation.ID(row.TaskID), RunID: foundation.ID(row.RunID), NodeRunID: foundation.ID(row.NodeRunID), TargetVersion: row.TargetVersion}
		submitted = row.Status == "submitted"
		return h.AuthorizeSynthesisManuscriptHumanScoped(ctx, scope, b)
	})
	return b, submitted, err
}

func (h *synthesisManuscriptHuman) AuthorizeSynthesisManuscriptHumanScoped(ctx context.Context, scope foundation.TransactionScope, b app.SynthesisManuscriptHumanBinding) error {
	if err := h.caller.Authorize(); err != nil {
		return err
	}
	if !validHumanBinding(b) {
		return manuscriptStoreInvalid("invalid human binding")
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	// 加锁顺序与 SubmitHuman 相同：run、nodes、task，随后才锁所属模块和模型记录。
	var row struct {
		WorkspaceID       string
		ProcessingID      string
		DefinitionKey     string
		DefinitionVersion int64
		Graph             []byte
		Input             []byte
		Status            string
		Controlled        bool
	}
	result := tx.Raw(`SELECT r.workspace_id,p.id AS processing_id,d.key AS definition_key,d.version AS definition_version,d.graph,r.input,r.status,(r.cancel_requested_at IS NOT NULL OR r.pause_requested_at IS NOT NULL) AS controlled
 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
 JOIN organizing.synthesis_processing p ON p.workflow_run_id=r.id AND p.workspace_id=r.workspace_id
 WHERE r.id=? FOR UPDATE OF r`, string(b.RunID)).Scan(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || row.WorkspaceID != string(b.WorkspaceID) || row.ProcessingID != string(b.ProcessingID) || row.DefinitionKey != organizingworkflow.SynthesisDefinitionKey || row.DefinitionVersion != organizingworkflow.SynthesisManuscriptDefinitionVersion {
		return manuscriptStoreConflict("human run binding changed")
	}
	graph, err := workflowapp.DecodeCanonicalGraph(row.Graph)
	if err != nil {
		return err
	}
	if err = workflowapp.AuthorizeWorkflowDefinition(graph, h.caller.Capabilities()); err != nil {
		return err
	}
	expected := organizingworkflow.SynthesisRegisteredDefinitions()[1].Graph
	if !reflect.DeepEqual(graph, expected) {
		return manuscriptStoreConflict("human run graph differs from registered definition")
	}
	start, err := organizingworkflow.DecodeSynthesisStartInput(row.Input)
	if err != nil || start.ProcessingID != b.ProcessingID {
		return manuscriptStoreConflict("human run input differs from processing")
	}
	var node struct {
		NodeKey  string
		NodeType string
		Status   string
	}
	result = tx.Raw(`SELECT node_key,node_type,status FROM workflow.node_run WHERE id=? AND run_id=? FOR UPDATE`, string(b.NodeRunID), string(b.RunID)).Scan(&node)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || node.NodeKey != organizingworkflow.SynthesisMergeReviewNodeKind || node.NodeType != node.NodeKey {
		return manuscriptStoreConflict("human node binding changed")
	}
	var task struct {
		Status        string
		Schema        []byte
		TargetVersion int64
		Unexpired     bool
	}
	result = tx.Raw(`SELECT status,expected_input_schema AS schema,target_version,(expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP) AS unexpired FROM workflow.human_task WHERE id=? AND run_id=? AND node_run_id=? FOR UPDATE`, string(b.HumanTaskID), string(b.RunID), string(b.NodeRunID)).Scan(&task)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || task.TargetVersion != b.TargetVersion || b.TargetVersion != 1 {
		return manuscriptStoreConflict("human target version changed")
	}
	if task.Status == "pending" {
		if node.Status != "waiting_for_human" || row.Status != "waiting_for_human" || row.Controlled || !task.Unexpired {
			return manuscriptStoreConflict("human task is not pending")
		}
	} else if task.Status != "submitted" || node.Status != "succeeded" {
		return manuscriptStoreConflict("human task is not available")
	}
	// 尝试记录包含精确的完整冻结生成结果；用它推导不可变任务 Schema 前，先与所属模块日志核验。
	var attemptRow manuscriptAttemptRow
	if err = tx.Where("workspace_id=? AND convert_from(payload,'UTF8')::jsonb->'command'->>'processing_id'=? AND convert_from(payload,'UTF8')::jsonb->'prepared'->'generation_input'->>'WorkflowRunID'=?", string(b.WorkspaceID), string(b.ProcessingID), string(b.RunID)).Order("id ASC").Take(&attemptRow).Error; err != nil {
		return manuscriptReadError(err)
	}
	d := h.runtime.dependencies.Storage
	d.Baselines = h
	d.Proof = h
	store, err := NewGORMSynthesisManuscriptStore(h.runtime.dependencies.Pool, d)
	if err != nil {
		return err
	}
	attempt, _, err := store.readAttempt(ctx, tx, b.WorkspaceID, foundation.ID(attemptRow.ID))
	if err != nil {
		return err
	}
	if err = h.runtime.dependencies.Models.VerifySynthesisManuscriptHistoryScoped(ctx, scope, attempt.Prepared.GenerationInput, attempt.Prepared.Generation); err != nil {
		return err
	}
	notes, err := manuscriptChangedNotes(attempt.Prepared.GenerationInput, attempt.Prepared.Generation)
	if err != nil {
		return err
	}
	if len(notes) == 0 {
		return manuscriptStoreConflict("human task has no changed targets")
	}
	schema, err := app.SynthesisManuscriptHumanSchema(b.ProcessingID, b.RunID, notes)
	if err != nil {
		return err
	}
	var actual, want any
	if json.Unmarshal(task.Schema, &actual) != nil || json.Unmarshal(schema, &want) != nil || !reflect.DeepEqual(actual, want) {
		return manuscriptStoreConflict("human schema differs from frozen target set")
	}
	_, err = h.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, b.WorkspaceID)
	return err
}

func (h *synthesisManuscriptHuman) VerifyPendingSynthesisManuscriptHumanScoped(ctx context.Context, scope foundation.TransactionScope, b app.SynthesisManuscriptHumanBinding, p app.SynthesisManuscriptPrepared) (app.SynthesisManuscriptAuthority, error) {
	if err := h.AuthorizeSynthesisManuscriptHumanScoped(ctx, scope, b); err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	tx = tx.WithContext(ctx)
	if err = lockManuscriptHuman(ctx, tx, b, true); err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	if p.GenerationInput.ProcessingID != b.ProcessingID || p.GenerationInput.WorkflowRunID != b.RunID || p.GenerationInput.SourceEvent.Source.WorkspaceID != b.WorkspaceID {
		return app.SynthesisManuscriptAuthority{}, manuscriptStoreConflict("prepared manuscript differs from task")
	}
	if err = h.runtime.dependencies.Models.VerifySynthesisManuscriptHistoryScoped(ctx, scope, p.GenerationInput, p.Generation); err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	loaded, err := h.runtime.dependencies.Executions.LoadSynthesisExecution(ctx, b.WorkspaceID, b.ProcessingID, b.RunID)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	if loaded.Semantic == nil || loaded.Semantic.Semantic == nil {
		return app.SynthesisManuscriptAuthority{}, manuscriptStoreConflict("semantic proof missing")
	}
	baseline := &synthesisManuscriptBaseline{store: h.runtime.dependencies.Candidates, roots: h.runtime.dependencies.Roots, input: p.GenerationInput, generation: p.Generation, semantic: *loaded.Semantic.Semantic}
	current, err := baseline.readOwners(ctx, scope, tx, p.MergeInput.Latest.NoteID)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	p.MergeInput.FileContent = ""
	p.MergeInput.FileExists = false
	if !reflect.DeepEqual(current, p) {
		return app.SynthesisManuscriptAuthority{}, manuscriptStoreConflict("manuscript baseline changed; cancel processing and explicitly retry")
	}
	return current.Authority, nil
}

// 人工调用不能准备尝试，也不能创建普通运行时证明；只能通过评审端口处理由真实合并节点准备的尝试。
func (h *synthesisManuscriptHuman) LoadSynthesisManuscriptBaseline(context.Context, app.PrepareSynthesisManuscript) (app.SynthesisManuscriptPrepared, error) {
	return app.SynthesisManuscriptPrepared{}, manuscriptStoreInvalid("human callers cannot prepare runtime attempts")
}
func (h *synthesisManuscriptHuman) VerifySynthesisManuscriptPreparedScoped(context.Context, foundation.TransactionScope, app.SynthesisManuscriptPrepared) (app.SynthesisManuscriptAuthority, error) {
	return app.SynthesisManuscriptAuthority{}, manuscriptStoreInvalid("human callers require the pending task authority port")
}
