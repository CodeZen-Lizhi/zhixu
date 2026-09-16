package postgres

import (
	"context"
	"errors"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

func (s *GORMGoalSelectionStore) verifyLive(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row goalSelectionModel) error {
	if row.WorkflowRunID == nil || row.NodeRunID == nil || row.NodeAttemptID == nil {
		return goalSelectionRecovery()
	}
	state, found, err := s.fence.LockWorkspaceAnalysisExecutionScoped(ctx, scope, workflowapp.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: foundation.ID(row.WorkspaceID), WorkflowRunID: foundation.ID(*row.WorkflowRunID), NodeRunID: foundation.ID(*row.NodeRunID), NodeAttemptID: foundation.ID(*row.NodeAttemptID)})
	if err != nil {
		return err
	}
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return err
	}
	if !found || state.WorkflowStatus != workflowdomain.RunStatusRunning || state.CancelRequested || state.PauseRequested || state.NodeStatus != workflowdomain.NodeStatusRunning || state.AttemptStatus != workflowdomain.AttemptStatusRunning || state.NodeAttempt != state.AttemptNo || !state.NodeLeaseOwnerSet || !state.AttemptLeaseOwnerSet || state.NodeLeaseOwner != state.AttemptLeaseOwner || !state.NodeLeaseUntilSet || !state.AttemptLeaseUntilSet || !state.NodeLeaseUntil.After(now) || !state.AttemptLeaseUntil.After(now) {
		return goalSelectionRecovery()
	}
	return nil
}
func goalSelectionRecovery() error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "SYNTHESIS_GOAL_SELECTION_RECOVERY_REQUIRED", false, errors.New("goal selection execution is no longer active"))
}

// 仅有成功 ModelRun 不足以构成证明；所有已记录调用、初始请求字节及最终提供方字节必须属于此次精确尝试。
func (s *GORMGoalSelectionStore) verifyModel(ctx context.Context, scope foundation.TransactionScope, row goalSelectionModel, c app.CompleteGoalSelectionCommand) error {
	record, err := s.runs.GetModelRunRecordScoped(ctx, scope, c.WorkspaceID, c.ModelRunID, true)
	if err != nil {
		return err
	}
	run := record.Run
	schema := agentdomain.SchemaRef{ID: agentdomain.GoalSelectionSchemaID, Version: "v1"}
	prompt := agentdomain.PromptRef{ID: schema.ID, Version: schema.Version}
	if agentdomain.ValidateModelRun(run) != nil || run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeGoalSelection || run.WorkflowRunID != foundation.ID(stringValue(row.WorkflowRunID)) || run.NodeRunID != foundation.ID(stringValue(row.NodeRunID)) || run.NodeAttemptID != foundation.ID(stringValue(row.NodeAttemptID)) || run.Prompt != prompt || run.Schema != schema || run.ReducedSchema != schema || run.Retrieval.IsBound() || run.MemoryContext.IsBound() || len(record.Calls) < 1 || len(record.Calls) > agentapp.StructuredCallLimit {
		return goalConflict()
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for i, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != run.ID || call.CallNo != i+1 || call.Phase != phases[i] || call.Status != agentdomain.ModelCallSucceeded || call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt || call.Schema != run.Schema {
			return goalConflict()
		}
	}
	last := record.Calls[len(record.Calls)-1]
	if record.Calls[0].RequestHash != stringValue(row.ModelInputHash) || last.ResponseHash != sha256Hex(c.ModelOutput) || last.ResponseBytes != int64(len(c.ModelOutput)) {
		return goalConflict()
	}
	return nil
}
