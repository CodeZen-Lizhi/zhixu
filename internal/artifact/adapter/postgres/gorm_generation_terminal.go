package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMSectionGenerationTerminalHook closes pending Artifact generations in the
// Workflow caller's transaction scope.
type GORMSectionGenerationTerminalHook struct {
	database *gorm.DB
	agent    agentapplication.ScopedModelRunStore
	profile  agentdomain.ModelProfileRef
}

// NewGORMSectionGenerationTerminalHook constructs the scoped hook from
// the same platform pool used by Workflow and Agent composition.
func NewGORMSectionGenerationTerminalHook(
	pool *platformpostgres.Pool,
	agent agentapplication.ScopedModelRunStore,
	profile agentdomain.ModelProfileRef,
) (*GORMSectionGenerationTerminalHook, error) {
	if pool == nil || nilGenerationDependency(agent) || profile.Validate() != nil {
		return nil, generationCapabilityError(errors.New("artifact generation terminal dependencies are incomplete"))
	}
	database, err := pool.GORM()
	if err != nil || !validGORMArtifactDatabase(database) {
		return nil, generationCapabilityError(errors.New("artifact generation terminal database is unavailable"))
	}
	return &GORMSectionGenerationTerminalHook{database: database, agent: agent, profile: profile}, nil
}

// OnWorkflowNodeTerminalScoped synchronizes one terminal Workflow fact without
// taking ownership of commit or rollback.
func (hook *GORMSectionGenerationTerminalHook) OnWorkflowNodeTerminalScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	event workflowapplication.WorkflowNodeTerminalEvent,
) error {
	if hook == nil || !validGORMArtifactDatabase(hook.database) || nilGenerationDependency(hook.agent) || hook.profile.Validate() != nil {
		return generationCapabilityError(errors.New("artifact generation terminal hook is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if event.NodeKind != artifactworkflow.NodeKind {
		return nil
	}
	if !validID(event.WorkspaceID) || !validID(event.WorkflowRunID) || !validID(event.NodeRunID) ||
		event.WorkflowRunID == event.NodeRunID || event.WorkspaceID == event.WorkflowRunID || event.WorkspaceID == event.NodeRunID ||
		(event.NodeAttemptID != "" && (!validID(event.NodeAttemptID) || event.NodeAttemptID == event.WorkflowRunID || event.NodeAttemptID == event.NodeRunID || event.NodeAttemptID == event.WorkspaceID)) ||
		event.TerminalAt.IsZero() {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal event is invalid"))
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return generationContextError(foundation.ErrorInvalidInput, err)
	}
	tx = tx.WithContext(ctx)
	generation, found, err := gormLoadSectionGenerationForTerminal(ctx, tx, event.WorkspaceID, event.WorkflowRunID, event.NodeRunID)
	if err != nil || !found {
		return err
	}
	if generation.Status != artifactapplication.SectionGenerationPending {
		return nil
	}
	record, err := hook.loadTerminalModelRunScoped(ctx, scope, generation, event.NodeAttemptID)
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
	return gormUpdateTerminalSectionGeneration(ctx, tx, generation, terminal)
}

func (hook *GORMSectionGenerationTerminalHook) loadTerminalModelRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	generation artifactapplication.SectionGeneration,
	attemptID foundation.ID,
) (*agentapplication.ModelRunRecord, error) {
	if attemptID == "" {
		return nil, nil
	}
	run, found, err := hook.agent.GetModelRunByAttemptScoped(ctx, scope, generation.WorkspaceID, attemptID, true)
	if err != nil {
		return nil, mapGenerationTerminalDependencyError(err)
	}
	if !found {
		return nil, nil
	}
	record, err := hook.agent.GetModelRunRecordScoped(ctx, scope, generation.WorkspaceID, run.ID, true)
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

func gormLoadSectionGenerationForTerminal(
	ctx context.Context,
	tx *gorm.DB,
	workspaceID, workflowRunID, nodeRunID foundation.ID,
) (artifactapplication.SectionGeneration, bool, error) {
	row, err := gormRow(tx.WithContext(ctx), `SELECT `+sectionGenerationColumns+`
		FROM learning.artifact_section_generation
		WHERE workspace_id=?::uuid AND workflow_run_id=?::uuid AND node_run_id=?::uuid
		FOR UPDATE`, string(workspaceID), string(workflowRunID), string(nodeRunID))
	if err != nil {
		return artifactapplication.SectionGeneration{}, false, classifyGORM(ctx, err, "ARTIFACT_GENERATION_TERMINAL_QUERY_FAILED")
	}
	generation, err := scanSectionGeneration(row)
	if gormNoRows(err) {
		return artifactapplication.SectionGeneration{}, false, nil
	}
	if err != nil {
		return artifactapplication.SectionGeneration{}, false, classifyGORM(ctx, err, "ARTIFACT_GENERATION_TERMINAL_QUERY_FAILED")
	}
	return generation, true, nil
}

func gormUpdateTerminalSectionGeneration(
	ctx context.Context,
	tx *gorm.DB,
	pending, terminal artifactapplication.SectionGeneration,
) error {
	var modelRunID any
	if terminal.ModelRunID != nil {
		modelRunID = string(*terminal.ModelRunID)
	}
	result := tx.WithContext(ctx).Exec(`UPDATE learning.artifact_section_generation
		SET status=?,model_run_id=?::uuid,failure_class=?,error_code=?,error_summary=?,
		    version=?,updated_at=?::timestamptz,terminal_at=?::timestamptz
		WHERE id=?::uuid AND workspace_id=?::uuid AND status='PENDING' AND version=?`,
		string(terminal.Status), modelRunID, terminal.FailureClass, terminal.ErrorCode, terminal.ErrorSummary,
		terminal.Version, terminal.UpdatedAt.UTC(), terminal.UpdatedAt.UTC(),
		string(pending.ID), string(pending.WorkspaceID), pending.Version)
	if result.Error != nil {
		return classifyGORM(ctx, result.Error, "ARTIFACT_GENERATION_TERMINAL_UPDATE_FAILED")
	}
	if result.RowsAffected != 1 {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation terminal compare-and-swap failed"))
	}
	return nil
}

var _ workflowapplication.ScopedWorkflowTerminalHook = (*GORMSectionGenerationTerminalHook)(nil)
