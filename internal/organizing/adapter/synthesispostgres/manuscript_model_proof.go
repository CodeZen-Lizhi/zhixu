package synthesispostgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// VerifySynthesisGenerationForExecutionScoped 将当前调用方租约与不可变生成、语义身份分开处理。它既不要求旧生成尝试仍为 RUNNING，也不创建应用预留。执行上下文必须由可信运行时调用方提供。
func (store *Store) VerifySynthesisGenerationForExecutionScoped(ctx context.Context, scope foundation.TransactionScope, execution workflowapp.ExecutionContext, input organizingapp.SynthesisGenerationInput, generation organizingapp.SynthesisGenerationResult) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	if execution.NodeKind != organizingworkflow.SynthesisApplyNodeKind && execution.NodeKind != organizingworkflow.SynthesisMergeReviewNodeKind {
		return invalid("manuscript proof requires a merge or apply execution")
	}
	if input.Validate() != nil || generation.Validate(input) != nil || execution.WorkspaceID != input.SourceEvent.Source.WorkspaceID || execution.RunID != input.WorkflowRunID {
		return invalid("synthesis manuscript execution differs from frozen generation")
	}
	snapshot, found, err := store.dependencies.WorkflowFence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, workflowapp.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID})
	if err != nil {
		return err
	}
	if !found || snapshot.DefinitionID != execution.DefinitionID || snapshot.DefinitionVersion != execution.DefinitionVersion || execution.DefinitionVersion != organizingworkflow.SynthesisManuscriptDefinitionVersion || execution.DefinitionHash != store.manuscriptDefinitionHash || snapshot.NodeKey != execution.NodeKey || execution.NodeKey != execution.NodeKind || snapshot.AttemptNo != execution.AttemptNo || snapshot.AttemptLeaseOwner != execution.LeaseOwner || execution.InputSchemaVersion != organizingworkflow.SynthesisInputSchemaVersion {
		return invalid("typed manuscript execution differs from current runtime claim")
	}
	tx, row, err := store.bindLive(ctx, scope, input.ProcessingID, execution)
	if err != nil {
		return err
	}
	if row.ApplyRecovery {
		return invalid("synthesis recovery cannot prepare a new manuscript")
	}
	return store.verifyImmutableGenerationScoped(ctx, scope, tx, row, input, generation)
}

// VerifySynthesisManuscriptHistoryScoped 仅证明不可变模型事实；HumanAuthority 调用方另行锁定实际任务并验证权限。
func (store *Store) VerifySynthesisManuscriptHistoryScoped(ctx context.Context, scope foundation.TransactionScope, input organizingapp.SynthesisGenerationInput, generation organizingapp.SynthesisGenerationResult) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	if input.Validate() != nil || generation.Validate(input) != nil {
		return invalid("invalid frozen manuscript generation")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return err
	}
	row, err := loadExecution(tx, input.SourceEvent.Source.WorkspaceID, input.ProcessingID, input.WorkflowRunID, true)
	if err != nil {
		return err
	}
	if row.ApplyRecovery {
		return invalid("recovery execution cannot grant human authority")
	}
	return store.verifyImmutableGenerationScoped(ctx, scope, tx, row, input, generation)
}
