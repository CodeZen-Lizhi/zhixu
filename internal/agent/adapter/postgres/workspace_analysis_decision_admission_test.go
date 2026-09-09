package postgres

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestWorkspaceAnalysisV2DecisionAdmissionReservesPublicationAndUsesDurableOrdinal(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	locked := workspaceAnalysisModelLocks{analysisRun: domain.WorkspaceAnalysisRun{
		DefinitionVersion: 2, PolicyVersion: 2, DeadlineAt: now.Add(time.Hour),
		Timeouts: domain.WorkspaceAnalysisV1Timeouts{PlanModelTimeout: time.Minute},
		Limits:   domain.WorkspaceAnalysisBudgetLimits{Amount: domain.WorkspaceAnalysisBudgetAmount{ModelCalls: 14, InputTokens: 917504, OutputTokens: 11264}},
	}}
	locked.fence.databaseNow, locked.operation.id = now, workspaceAnalysisModelTestID(90)
	command := application.AuthorizeWorkspaceAnalysisModelCallCommand{
		OperationKey: domain.WorkspaceAnalysisOperationKey{NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext, Kind: domain.WorkspaceAnalysisOperationDecision, Ordinal: 12},
		Call:         domain.ModelCall{MaxOutputTokens: 512},
	}
	locked.analysisRun.Settled = domain.WorkspaceAnalysisBudgetAmount{ModelCalls: 11, InputTokens: 11 * 65536, OutputTokens: 11 * 512}
	if err := validateWorkspaceAnalysisV2ModelAdmission(locked, command); err != nil {
		t.Fatalf("twelfth exact decision: %v", err)
	}
	for _, mutate := range []func(*workspaceAnalysisModelLocks, *application.AuthorizeWorkspaceAnalysisModelCallCommand){
		func(_ *workspaceAnalysisModelLocks, c *application.AuthorizeWorkspaceAnalysisModelCallCommand) {
			c.OperationKey.Ordinal = 13
		},
		func(l *workspaceAnalysisModelLocks, _ *application.AuthorizeWorkspaceAnalysisModelCallCommand) {
			l.analysisRun.Settled.ModelCalls++
		},
		func(l *workspaceAnalysisModelLocks, _ *application.AuthorizeWorkspaceAnalysisModelCallCommand) {
			l.analysisRun.Settled.InputTokens++
		},
		func(l *workspaceAnalysisModelLocks, _ *application.AuthorizeWorkspaceAnalysisModelCallCommand) {
			l.analysisRun.Settled.OutputTokens++
		},
	} {
		copyLocks, copyCommand := locked, command
		mutate(&copyLocks, &copyCommand)
		denial, ok := application.WorkspaceAnalysisAdmissionDenialFromError(validateWorkspaceAnalysisV2ModelAdmission(copyLocks, copyCommand))
		if !ok || denial.OperationID != locked.operation.id || denial.Reason != domain.WorkspaceAnalysisRunBudgetExhausted || denial.Requested.InputTokens != 65536 || denial.Requested.OutputTokens != 512 {
			t.Fatal("budget refusal lost its exact pending operation and requested allowance")
		}
	}
	locked.analysisRun.DeadlineAt = now.Add(time.Minute + 5*time.Second)
	if err := validateWorkspaceAnalysisV2ModelAdmission(locked, command); err != nil {
		t.Fatal(err)
	}
	locked.analysisRun.DeadlineAt = locked.analysisRun.DeadlineAt.Add(-time.Microsecond)
	denial, ok := application.WorkspaceAnalysisAdmissionDenialFromError(validateWorkspaceAnalysisV2ModelAdmission(locked, command))
	if !ok || denial.Reason != domain.WorkspaceAnalysisRunDeadlineExceeded {
		t.Fatal("database deadline refusal is not typed")
	}
}
