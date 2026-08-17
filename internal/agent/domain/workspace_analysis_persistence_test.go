package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisPersistenceConstantsMatchV1Schema(t *testing.T) {
	if WorkspaceAnalysisCandidateSchemaID != "agent.workspace-analysis-candidate" || WorkspaceAnalysisCandidateSchemaVersion != 1 ||
		ResultTypeWorkspaceAnalysisPlan != "workspace_analysis_plan" || ResultTypeWorkspaceAnalysisAnswer != "workspace_analysis_answer" ||
		workspaceAnalysisMinimumOutputTokens != 1281 || WorkspaceAnalysisV1MaxRunOutputTokens != 5376 {
		t.Fatal("workspace analysis persistence constants drifted from v1")
	}
}

func TestWorkspaceAnalysisRunValidatesLifecycleAndTerminalMatrix(t *testing.T) {
	run := validWorkspaceAnalysisPersistenceRun()
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		t.Fatalf("queued run rejected: %v", err)
	}
	run.Status = WorkspaceAnalysisRunRunning
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		t.Fatalf("running run rejected: %v", err)
	}

	tests := []struct {
		status WorkspaceAnalysisRunStatus
		reason WorkspaceAnalysisRunTerminationReason
	}{
		{WorkspaceAnalysisRunRefused, WorkspaceAnalysisRunEvidenceInsufficient},
		{WorkspaceAnalysisRunRefused, WorkspaceAnalysisRunCitationInvalid},
		{WorkspaceAnalysisRunRefused, WorkspaceAnalysisRunFaithfulnessRejected},
		{WorkspaceAnalysisRunRefused, WorkspaceAnalysisRunModelRefused},
		{WorkspaceAnalysisRunClarificationRequired, WorkspaceAnalysisRunNeedsClarification},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunBudgetExhausted},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunReceiptInvalid},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunResultUnknown},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunDeadlineExceeded},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunModelFailed},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunToolFailed},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunRuntimeFailed},
		{WorkspaceAnalysisRunCancelled, WorkspaceAnalysisRunCancellation},
	}
	for _, test := range tests {
		t.Run(string(test.status)+"/"+string(test.reason), func(t *testing.T) {
			terminal := terminalWorkspaceAnalysisPersistenceRun(test.status, test.reason)
			if err := ValidateWorkspaceAnalysisRun(terminal); err != nil {
				t.Fatalf("terminal run rejected: %v", err)
			}
		})
	}

	succeeded := terminalWorkspaceAnalysisPersistenceRun(WorkspaceAnalysisRunSucceeded, WorkspaceAnalysisRunCompleted)
	succeeded.ValidationReceiptID = workspaceAnalysisPersistenceIDPointer(20)
	succeeded.ReviewModelRunID = workspaceAnalysisPersistenceIDPointer(21)
	succeeded.Settled = WorkspaceAnalysisBudgetAmount{ModelCalls: 3, ToolCalls: 4, SourceReads: 1, InputTokens: 600, OutputTokens: 300}
	if err := ValidateWorkspaceAnalysisRun(succeeded); err != nil {
		t.Fatalf("successful run rejected: %v", err)
	}
	succeededAtDeadline := cloneWorkspaceAnalysisPersistenceRun(succeeded)
	succeededAtDeadline.UpdatedAt = succeededAtDeadline.DeadlineAt
	succeededAtDeadline.CompletedAt = workspaceAnalysisPersistenceTimePointer(succeededAtDeadline.DeadlineAt)
	if err := ValidateWorkspaceAnalysisRun(succeededAtDeadline); err != nil {
		t.Fatalf("successful run at deadline rejected: %v", err)
	}
	succeededAfterDeadline := cloneWorkspaceAnalysisPersistenceRun(succeededAtDeadline)
	afterDeadline := succeededAfterDeadline.DeadlineAt.Add(time.Nanosecond)
	succeededAfterDeadline.UpdatedAt = afterDeadline
	succeededAfterDeadline.CompletedAt = workspaceAnalysisPersistenceTimePointer(afterDeadline)
	if err := ValidateWorkspaceAnalysisRun(succeededAfterDeadline); errorCode(err) != ErrorCodeWorkspaceAnalysisRunInvalid {
		t.Fatalf("successful run after deadline accepted: %v", err)
	}
	deadlineExceededAfterDeadline := terminalWorkspaceAnalysisPersistenceRun(WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunDeadlineExceeded)
	deadlineExceededAt := deadlineExceededAfterDeadline.DeadlineAt.Add(time.Nanosecond)
	deadlineExceededAfterDeadline.UpdatedAt = deadlineExceededAt
	deadlineExceededAfterDeadline.CompletedAt = workspaceAnalysisPersistenceTimePointer(deadlineExceededAt)
	if err := ValidateWorkspaceAnalysisRun(deadlineExceededAfterDeadline); err != nil {
		t.Fatalf("deadline-exceeded run after deadline rejected: %v", err)
	}

	invalid := []struct {
		name   string
		mutate func(*WorkspaceAnalysisRun)
	}{
		{name: "definition", mutate: func(value *WorkspaceAnalysisRun) { value.DefinitionKey = "agent-rag-answer" }},
		{name: "definition hash", mutate: func(value *WorkspaceAnalysisRun) { value.DefinitionHash = strings.Repeat("A", 64) }},
		{name: "policy", mutate: func(value *WorkspaceAnalysisRun) { value.PolicyVersion++ }},
		{name: "nodes", mutate: func(value *WorkspaceAnalysisRun) { value.Limits.Nodes-- }},
		{name: "minimum output", mutate: func(value *WorkspaceAnalysisRun) { value.Limits.Amount.OutputTokens = 1280 }},
		{name: "maximum output", mutate: func(value *WorkspaceAnalysisRun) { value.Limits.Amount.OutputTokens = 5377 }},
		{name: "deadline", mutate: func(value *WorkspaceAnalysisRun) { value.DeadlineAt = value.DeadlineAt.Add(time.Nanosecond) }},
		{name: "timeout snapshot", mutate: func(value *WorkspaceAnalysisRun) { value.Timeouts.PlanModelTimeout = 0 }},
		{name: "budget overflow", mutate: func(value *WorkspaceAnalysisRun) {
			value.Status = WorkspaceAnalysisRunRunning
			value.Reserved.ModelCalls = 2
			value.Settled.ModelCalls = 2
		}},
		{name: "queued budget", mutate: func(value *WorkspaceAnalysisRun) { value.Settled.ToolCalls = 1 }},
		{name: "active reason", mutate: func(value *WorkspaceAnalysisRun) { value.TerminationReason = WorkspaceAnalysisRunModelFailed }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneWorkspaceAnalysisPersistenceRun(validWorkspaceAnalysisPersistenceRun())
			test.mutate(&candidate)
			if err := ValidateWorkspaceAnalysisRun(candidate); errorCode(err) != ErrorCodeWorkspaceAnalysisRunInvalid {
				t.Fatalf("invalid run accepted or wrong error: %v", err)
			}
		})
	}

	mismatched := terminalWorkspaceAnalysisPersistenceRun(WorkspaceAnalysisRunCancelled, WorkspaceAnalysisRunModelFailed)
	if err := ValidateWorkspaceAnalysisRun(mismatched); errorCode(err) != ErrorCodeWorkspaceAnalysisRunInvalid {
		t.Fatalf("mismatched terminal matrix err=%v", err)
	}
	succeeded.ValidationReceiptID = nil
	if err := ValidateWorkspaceAnalysisRun(succeeded); errorCode(err) != ErrorCodeWorkspaceAnalysisRunInvalid {
		t.Fatalf("success without proof err=%v", err)
	}
}

func TestWorkspaceAnalysisRunRejectsUntrustedPricedBudget(t *testing.T) {
	run := validWorkspaceAnalysisPersistenceRun()
	maximum, zero := int64(1000), int64(0)
	run.Limits.Amount.CostMicrounits = &maximum
	run.Reserved.CostMicrounits = &zero
	run.Settled.CostMicrounits = &zero
	if err := ValidateWorkspaceAnalysisRun(run); errorCode(err) != ErrorCodeWorkspaceAnalysisRunInvalid {
		t.Fatalf("untrusted priced budget err=%v", err)
	}
}

func TestWorkspaceAnalysisRunTransitionsMatchAppendOnlyLifecycle(t *testing.T) {
	accepted := [][2]WorkspaceAnalysisRunStatus{
		{WorkspaceAnalysisRunQueued, WorkspaceAnalysisRunQueued},
		{WorkspaceAnalysisRunQueued, WorkspaceAnalysisRunRunning},
		{WorkspaceAnalysisRunQueued, WorkspaceAnalysisRunFailed},
		{WorkspaceAnalysisRunRunning, WorkspaceAnalysisRunRunning},
		{WorkspaceAnalysisRunRunning, WorkspaceAnalysisRunSucceeded},
		{WorkspaceAnalysisRunRunning, WorkspaceAnalysisRunCancelled},
	}
	for _, transition := range accepted {
		if err := ValidateWorkspaceAnalysisRunTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("transition %s -> %s rejected: %v", transition[0], transition[1], err)
		}
	}
	for _, transition := range [][2]WorkspaceAnalysisRunStatus{
		{WorkspaceAnalysisRunRunning, WorkspaceAnalysisRunQueued},
		{WorkspaceAnalysisRunSucceeded, WorkspaceAnalysisRunSucceeded},
		{WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunRunning},
		{"future", WorkspaceAnalysisRunRunning},
	} {
		if err := ValidateWorkspaceAnalysisRunTransition(transition[0], transition[1]); errorCode(err) != ErrorCodeWorkspaceAnalysisRunTransitionInvalid {
			t.Fatalf("transition %s -> %s err=%v", transition[0], transition[1], err)
		}
	}
}

func TestWorkspaceAnalysisBudgetReservationValidatesSettlementAndUnknownCharge(t *testing.T) {
	model := validWorkspaceAnalysisModelReservation()
	if err := ValidateWorkspaceAnalysisBudgetReservation(model); err != nil {
		t.Fatalf("model reservation rejected: %v", err)
	}
	settled := cloneWorkspaceAnalysisReservation(model)
	settled.Status = WorkspaceAnalysisBudgetSettled
	settled.Settled = WorkspaceAnalysisBudgetAmount{ModelCalls: 1, InputTokens: 300, OutputTokens: 120}
	settled.SettledAt = workspaceAnalysisPersistenceTimePointer(model.CreatedAt.Add(time.Second))
	if err := ValidateWorkspaceAnalysisBudgetReservation(settled); err != nil {
		t.Fatalf("settled reservation rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorkspaceAnalysisBudgetReservation)
	}{
		{name: "model call count", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.ModelCalls = 0 }},
		{name: "model tool call", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.ToolCalls = 1 }},
		{name: "model source read", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.SourceReads = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalidSettlement := cloneWorkspaceAnalysisReservation(settled)
			test.mutate(&invalidSettlement)
			if err := ValidateWorkspaceAnalysisBudgetReservation(invalidSettlement); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationInvalid {
				t.Fatalf("invalid model settlement err=%v", err)
			}
		})
	}
	unknown := cloneWorkspaceAnalysisReservation(model)
	unknown.Status = WorkspaceAnalysisBudgetUnknownCharged
	unknown.Settled = cloneWorkspaceAnalysisAmount(model.Reserved)
	unknown.SettledAt = workspaceAnalysisPersistenceTimePointer(model.CreatedAt.Add(time.Second))
	if err := ValidateWorkspaceAnalysisBudgetReservation(unknown); err != nil {
		t.Fatalf("unknown-charged reservation rejected: %v", err)
	}
	unknown.Settled.OutputTokens--
	if err := ValidateWorkspaceAnalysisBudgetReservation(unknown); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationInvalid {
		t.Fatalf("partial unknown charge err=%v", err)
	}

	tool := validWorkspaceAnalysisToolReservation(true)
	if err := ValidateWorkspaceAnalysisBudgetReservation(tool); err != nil {
		t.Fatalf("source-read reservation rejected: %v", err)
	}
	tool.Reserved.InputTokens = 1
	if err := ValidateWorkspaceAnalysisBudgetReservation(tool); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationInvalid {
		t.Fatalf("tool token reservation err=%v", err)
	}
	tool = validWorkspaceAnalysisToolReservation(true)
	tool.Status = WorkspaceAnalysisBudgetSettled
	tool.Settled = WorkspaceAnalysisBudgetAmount{ToolCalls: 1, SourceReads: 1}
	tool.SettledAt = workspaceAnalysisPersistenceTimePointer(tool.CreatedAt.Add(time.Second))
	if err := ValidateWorkspaceAnalysisBudgetReservation(tool); err != nil {
		t.Fatalf("settled source-read reservation rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorkspaceAnalysisBudgetReservation)
	}{
		{name: "tool call count", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.ToolCalls = 0 }},
		{name: "tool source read", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.SourceReads = 0 }},
		{name: "tool model call", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.ModelCalls = 1 }},
		{name: "tool input tokens", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.InputTokens = 1 }},
		{name: "tool output tokens", mutate: func(value *WorkspaceAnalysisBudgetReservation) { value.Settled.OutputTokens = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalidSettlement := cloneWorkspaceAnalysisReservation(tool)
			test.mutate(&invalidSettlement)
			if err := ValidateWorkspaceAnalysisBudgetReservation(invalidSettlement); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationInvalid {
				t.Fatalf("invalid tool settlement err=%v", err)
			}
		})
	}

	priced := validWorkspaceAnalysisModelReservation()
	reservedCost := int64(100)
	priced.Reserved.CostMicrounits = &reservedCost
	if err := ValidateWorkspaceAnalysisBudgetReservation(priced); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationInvalid {
		t.Fatalf("untrusted reservation cost err=%v", err)
	}
}

func TestWorkspaceAnalysisBudgetReservationTransitionAndOperationBinding(t *testing.T) {
	for _, target := range []WorkspaceAnalysisBudgetReservationStatus{WorkspaceAnalysisBudgetSettled, WorkspaceAnalysisBudgetUnknownCharged} {
		if err := ValidateWorkspaceAnalysisBudgetReservationTransition(WorkspaceAnalysisBudgetReserved, target); err != nil {
			t.Fatalf("target=%s err=%v", target, err)
		}
	}
	if err := ValidateWorkspaceAnalysisBudgetReservationTransition(WorkspaceAnalysisBudgetSettled, WorkspaceAnalysisBudgetReserved); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationTransitionInvalid {
		t.Fatalf("terminal reservation reopened: %v", err)
	}

	planContract, _ := workspaceAnalysisOperationContract(WorkspaceAnalysisOperationNodeRetrieveEvidence, WorkspaceAnalysisOperationRetrievalPlan, 1)
	operation := validStartedWorkspaceAnalysisOperation(planContract, 31)
	run := validWorkspaceAnalysisPersistenceRun()
	run.Status = WorkspaceAnalysisRunRunning
	run.ID = operation.AnalysisRunID
	reservation := validWorkspaceAnalysisModelReservation()
	reservation.WorkspaceID = run.WorkspaceID
	reservation.ID = *operation.BudgetReservationID
	reservation.AnalysisRunID = operation.AnalysisRunID
	reservation.OperationID = operation.ID
	reservation.ModelCallID = workspaceAnalysisPersistenceIDCopy(operation.Call.ID)
	reservation.Reserved.OutputTokens = WorkspaceAnalysisV1PlanMaxOutputTokens
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation); err != nil {
		t.Fatalf("plan reservation binding rejected: %v", err)
	}
	reservation.Reserved.OutputTokens++
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
		t.Fatalf("oversized plan reservation err=%v", err)
	}
	reservation.Reserved.OutputTokens = WorkspaceAnalysisV1PlanMaxOutputTokens
	settledOperation := mutateWorkspaceAnalysisOperation(operation, func(value *WorkspaceAnalysisOperation) {
		completedAt := value.UpdatedAt.Add(time.Second)
		value.Status = WorkspaceAnalysisOperationSucceeded
		value.Version++
		value.UpdatedAt = completedAt
		value.CompletedAt = &completedAt
		value.Result = &WorkspaceAnalysisOperationResultRef{
			Kind: WorkspaceAnalysisOperationResultModelCall,
			ID:   workspaceAnalysisPersistenceID(106),
			Hash: strings.Repeat("b", 64),
		}
	})
	settledReservation := cloneWorkspaceAnalysisReservation(reservation)
	settledReservation.Status = WorkspaceAnalysisBudgetSettled
	settledReservation.Settled = WorkspaceAnalysisBudgetAmount{ModelCalls: 1, InputTokens: 200, OutputTokens: 100}
	settledReservation.SettledAt = workspaceAnalysisPersistenceTimePointer(settledReservation.CreatedAt.Add(time.Second))
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, settledReservation, settledOperation); err != nil {
		t.Fatalf("settled operation reservation binding rejected: %v", err)
	}
	unknownReservation := cloneWorkspaceAnalysisReservation(settledReservation)
	unknownReservation.Status = WorkspaceAnalysisBudgetUnknownCharged
	unknownReservation.Settled = cloneWorkspaceAnalysisAmount(unknownReservation.Reserved)
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, unknownReservation, settledOperation); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
		t.Fatalf("successful operation accepted unknown charge: %v", err)
	}

	readContract, _ := workspaceAnalysisOperationContract(WorkspaceAnalysisOperationNodeReadEvidence, WorkspaceAnalysisOperationSourceRead, 1)
	readOperation := validStartedWorkspaceAnalysisOperation(readContract, 32)
	readOperation.AnalysisRunID = run.ID
	readReservation := validWorkspaceAnalysisToolReservation(true)
	readReservation.WorkspaceID = run.WorkspaceID
	readReservation.ID = *readOperation.BudgetReservationID
	readReservation.AnalysisRunID = readOperation.AnalysisRunID
	readReservation.OperationID = readOperation.ID
	readReservation.ToolCallID = workspaceAnalysisPersistenceIDCopy(readOperation.Call.ID)
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, readReservation, readOperation); err != nil {
		t.Fatalf("source-read reservation binding rejected: %v", err)
	}
	readReservation.Reserved.SourceReads = 0
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, readReservation, readOperation); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
		t.Fatalf("source-read without source budget err=%v", err)
	}
}

func TestWorkspaceAnalysisBudgetReservationBindingUsesExactV1OperationPolicy(t *testing.T) {
	tests := []struct {
		name     string
		nodeKey  WorkspaceAnalysisOperationNodeKey
		kind     WorkspaceAnalysisOperationKind
		expected int64
	}{
		{name: "plan", nodeKey: WorkspaceAnalysisOperationNodeRetrieveEvidence, kind: WorkspaceAnalysisOperationRetrievalPlan, expected: WorkspaceAnalysisV1PlanMaxOutputTokens},
		{name: "synthesis", nodeKey: WorkspaceAnalysisOperationNodeSynthesizeAnswer, kind: WorkspaceAnalysisOperationAnswerSynthesis, expected: WorkspaceAnalysisV1SynthesisMaxOutputTokens},
		{name: "review", nodeKey: WorkspaceAnalysisOperationNodeReviewPublish, kind: WorkspaceAnalysisOperationFaithfulnessReview, expected: WorkspaceAnalysisV1ReviewMaxOutputTokens},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, exists := workspaceAnalysisOperationContract(test.nodeKey, test.kind, 1)
			if !exists {
				t.Fatalf("operation contract missing for %s", test.kind)
			}
			operation := validStartedWorkspaceAnalysisOperation(contract, 80+index)
			run := validWorkspaceAnalysisPersistenceRun()
			run.Status = WorkspaceAnalysisRunRunning
			run.ID = operation.AnalysisRunID
			reservation := validWorkspaceAnalysisModelReservation()
			reservation.ID = *operation.BudgetReservationID
			reservation.WorkspaceID = run.WorkspaceID
			reservation.AnalysisRunID = run.ID
			reservation.OperationID = operation.ID
			reservation.ModelCallID = workspaceAnalysisPersistenceIDCopy(operation.Call.ID)
			reservation.Reserved.OutputTokens = test.expected

			if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation); err != nil {
				t.Fatalf("exact reservation rejected: %v", err)
			}
			reservation.Reserved.OutputTokens--
			if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
				t.Fatalf("smaller output reservation accepted: %v", err)
			}
			reservation.Reserved.OutputTokens = test.expected
			reservation.Reserved.InputTokens--
			if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetReservationInvalid {
				t.Fatalf("smaller input reservation accepted: %v", err)
			}
		})
	}

	contract, _ := workspaceAnalysisOperationContract(WorkspaceAnalysisOperationNodeRetrieveEvidence, WorkspaceAnalysisOperationRetrievalPlan, 1)
	operation := validStartedWorkspaceAnalysisOperation(contract, 90)
	run := validWorkspaceAnalysisPersistenceRun()
	run.Status = WorkspaceAnalysisRunRunning
	run.ID = operation.AnalysisRunID
	reservation := validWorkspaceAnalysisModelReservation()
	reservation.ID = *operation.BudgetReservationID
	reservation.WorkspaceID = run.WorkspaceID
	reservation.AnalysisRunID = run.ID
	reservation.OperationID = operation.ID
	reservation.ModelCallID = workspaceAnalysisPersistenceIDCopy(operation.Call.ID)
	reservation.Reserved.OutputTokens = WorkspaceAnalysisV1PlanMaxOutputTokens
	reservation.AnalysisRunID = workspaceAnalysisPersistenceID(999)
	if err := ValidateWorkspaceAnalysisBudgetReservationBinding(run, reservation, operation); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
		t.Fatalf("cross-run reservation accepted: %v", err)
	}
}

func TestWorkspaceAnalysisRunBudgetTotalsUseReservedPlusSettledSemantics(t *testing.T) {
	active := validWorkspaceAnalysisModelReservation()
	settled := validWorkspaceAnalysisToolReservation(false)
	settled.Status = WorkspaceAnalysisBudgetSettled
	settled.Settled = WorkspaceAnalysisBudgetAmount{ToolCalls: 1}
	settled.SettledAt = workspaceAnalysisPersistenceTimePointer(settled.CreatedAt.Add(time.Second))

	run := validWorkspaceAnalysisPersistenceRun()
	run.Status = WorkspaceAnalysisRunRunning
	run.Reserved = cloneWorkspaceAnalysisAmount(active.Reserved)
	run.Settled = cloneWorkspaceAnalysisAmount(settled.Settled)
	active.WorkspaceID, settled.WorkspaceID = run.WorkspaceID, run.WorkspaceID
	active.AnalysisRunID, settled.AnalysisRunID = run.ID, run.ID
	if err := ValidateWorkspaceAnalysisRunBudgetTotals(run, []WorkspaceAnalysisBudgetReservation{active, settled}); err != nil {
		t.Fatalf("budget totals rejected: %v", err)
	}

	drifted := cloneWorkspaceAnalysisPersistenceRun(run)
	drifted.Settled.ToolCalls = 0
	if err := ValidateWorkspaceAnalysisRunBudgetTotals(drifted, []WorkspaceAnalysisBudgetReservation{active, settled}); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
		t.Fatalf("drifted totals err=%v", err)
	}
	if err := ValidateWorkspaceAnalysisRunBudgetTotals(run, []WorkspaceAnalysisBudgetReservation{active, active, settled}); errorCode(err) != ErrorCodeWorkspaceAnalysisBudgetBindingMismatch {
		t.Fatalf("duplicate reservation err=%v", err)
	}
}

func TestWorkspaceAnalysisPlannerModelResultValidatesAndBindsReceipt(t *testing.T) {
	fixture := validWorkspaceAnalysisModelResultFixture(t, WorkspaceAnalysisOperationRetrievalPlan)
	if err := ValidateWorkspaceAnalysisModelResult(fixture.result); err != nil {
		t.Fatalf("planner model result rejected: %v", err)
	}
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, fixture.operation, fixture.modelRun, fixture.modelCall, nil,
	); err != nil {
		t.Fatalf("started planner result binding rejected: %v", err)
	}

	nonCanonical := cloneWorkspaceAnalysisModelResult(fixture.result)
	nonCanonical.Document = append([]byte(" "), nonCanonical.Document...)
	nonCanonical.DocumentHash = workspaceAnalysisDocumentHash(nonCanonical.Document)
	nonCanonical.DocumentBytes = int64(len(nonCanonical.Document))
	if err := ValidateWorkspaceAnalysisModelResult(nonCanonical); errorCode(err) != ErrorCodeWorkspaceAnalysisModelResultInvalid {
		t.Fatalf("non-canonical planner document accepted: %v", err)
	}

	succeeded := succeededWorkspaceAnalysisModelResultOperation(fixture.operation, fixture.result)
	if succeeded.Result == nil || succeeded.Result.ID != fixture.result.ID || succeeded.Result.ID == fixture.modelCall.ID {
		t.Fatalf("succeeded operation did not reference the independent receipt: result=%#v call_id=%s", succeeded.Result, fixture.modelCall.ID)
	}
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, succeeded, fixture.modelRun, fixture.modelCall, nil,
	); err != nil {
		t.Fatalf("succeeded planner result binding rejected: %v", err)
	}

	wrongReceipt := mutateWorkspaceAnalysisOperation(succeeded, func(value *WorkspaceAnalysisOperation) {
		value.Result.ID = workspaceAnalysisPersistenceID(798)
	})
	if err := ValidateWorkspaceAnalysisOperation(wrongReceipt); err != nil {
		t.Fatalf("independent alternate receipt fixture is invalid: %v", err)
	}
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, wrongReceipt, fixture.modelRun, fixture.modelCall, nil,
	); errorCode(err) != ErrorCodeWorkspaceAnalysisModelResultBindingMismatch {
		t.Fatalf("operation accepted a different receipt id: %v", err)
	}

	callAsResult := mutateWorkspaceAnalysisOperation(succeeded, func(value *WorkspaceAnalysisOperation) {
		value.Result.ID = fixture.modelCall.ID
	})
	if err := ValidateWorkspaceAnalysisOperation(callAsResult); errorCode(err) != ErrorCodeWorkspaceAnalysisOperationInvalid {
		t.Fatalf("operation accepted ModelCall id as receipt id: %v", err)
	}

	replacement := mutateWorkspaceAnalysisOperation(succeeded, func(value *WorkspaceAnalysisOperation) {
		value.LatestNodeAttemptID = workspaceAnalysisOperationIDPointer(990)
		value.Version++
		value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	})
	if *replacement.FirstNodeAttemptID == *replacement.LatestNodeAttemptID {
		t.Fatal("replacement fixture did not advance LatestNodeAttemptID")
	}
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, replacement, fixture.modelRun, fixture.modelCall, nil,
	); err != nil {
		t.Fatalf("replacement rejected the receipt bound to the first attempt: %v", err)
	}
}

func TestWorkspaceAnalysisReviewModelResultBindsCandidateIdentityAndHash(t *testing.T) {
	fixture := validWorkspaceAnalysisModelResultFixture(t, WorkspaceAnalysisOperationFaithfulnessReview)
	if fixture.candidate == nil {
		t.Fatal("review fixture is missing its candidate")
	}
	if err := ValidateWorkspaceAnalysisModelResult(fixture.result); err != nil {
		t.Fatalf("review model result rejected: %v", err)
	}
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, fixture.operation, fixture.modelRun, fixture.modelCall, fixture.candidate,
	); err != nil {
		t.Fatalf("started review result binding rejected: %v", err)
	}

	succeeded := succeededWorkspaceAnalysisModelResultOperation(fixture.operation, fixture.result)
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, succeeded, fixture.modelRun, fixture.modelCall, fixture.candidate,
	); err != nil {
		t.Fatalf("succeeded review result binding rejected: %v", err)
	}

	missingSubject := cloneWorkspaceAnalysisModelResult(fixture.result)
	missingSubject.SubjectCandidateID = nil
	if err := ValidateWorkspaceAnalysisModelResult(missingSubject); errorCode(err) != ErrorCodeWorkspaceAnalysisModelResultInvalid {
		t.Fatalf("review result without candidate identity accepted: %v", err)
	}

	wrongSubject := cloneWorkspaceAnalysisModelResult(fixture.result)
	wrongSubject.SubjectCandidateID = workspaceAnalysisPersistenceIDPointer(799)
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		wrongSubject, fixture.run, fixture.operation, fixture.modelRun, fixture.modelCall, fixture.candidate,
	); errorCode(err) != ErrorCodeWorkspaceAnalysisModelResultBindingMismatch {
		t.Fatalf("review result accepted another candidate id: %v", err)
	}

	wrongHash := cloneWorkspaceAnalysisModelResult(fixture.result)
	wrongHash.SubjectCandidateHash = strings.Repeat("c", 64)
	if err := ValidateWorkspaceAnalysisModelResultBinding(
		wrongHash, fixture.run, fixture.operation, fixture.modelRun, fixture.modelCall, fixture.candidate,
	); errorCode(err) != ErrorCodeWorkspaceAnalysisModelResultBindingMismatch {
		t.Fatalf("review result accepted another candidate hash: %v", err)
	}

	if err := ValidateWorkspaceAnalysisModelResultBinding(
		fixture.result, fixture.run, fixture.operation, fixture.modelRun, fixture.modelCall, nil,
	); errorCode(err) != ErrorCodeWorkspaceAnalysisModelResultBindingMismatch {
		t.Fatalf("review result accepted a missing candidate: %v", err)
	}
}

func TestWorkspaceAnalysisCandidateValidatesDocumentAndSafeFormatting(t *testing.T) {
	candidate := validWorkspaceAnalysisCandidate()
	if err := ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		t.Fatalf("candidate rejected: %v", err)
	}
	limits := DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(MaxWorkspaceAnalysisCandidateBytes)
	decoded, err := DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil || decoded.ModelRunRef != candidate.SynthesisModelRunID || decoded.Payload.CitationRefs[0] != "E1" {
		t.Fatalf("candidate strict decode failed: result=%#v error=%v", decoded, err)
	}
	if rendered := fmt.Sprintf("%#v", candidate); strings.Contains(rendered, "private-candidate-body") {
		t.Fatalf("candidate debug formatting leaked document: %s", rendered)
	}

	invalid := []struct {
		name   string
		mutate func(*WorkspaceAnalysisCandidate)
	}{
		{name: "schema", mutate: func(value *WorkspaceAnalysisCandidate) { value.SchemaID = WorkspaceAnalysisPlanSchemaID }},
		{name: "version", mutate: func(value *WorkspaceAnalysisCandidate) { value.SchemaVersion = 2 }},
		{name: "hash", mutate: func(value *WorkspaceAnalysisCandidate) { value.DocumentHash = strings.Repeat("a", 64) }},
		{name: "bytes", mutate: func(value *WorkspaceAnalysisCandidate) { value.DocumentBytes++ }},
		{name: "array", mutate: func(value *WorkspaceAnalysisCandidate) {
			value.Document = []byte(`[]`)
			value.DocumentBytes = int64(len(value.Document))
			value.DocumentHash = workspaceAnalysisDocumentHash(value.Document)
		}},
		{name: "duplicate key", mutate: func(value *WorkspaceAnalysisCandidate) {
			value.Document = []byte(`{"answer":"one","answer":"two"}`)
			value.DocumentBytes = int64(len(value.Document))
			value.DocumentHash = workspaceAnalysisDocumentHash(value.Document)
		}},
		{name: "missing nullable proposal", mutate: func(value *WorkspaceAnalysisCandidate) {
			value.Document = []byte(fmt.Sprintf(`{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","model_run_ref":%q,"payload":{"answer_markdown":"answer","citation_refs":["E1"]}}`, value.SynthesisModelRunID))
			value.DocumentBytes = int64(len(value.Document))
			value.DocumentHash = workspaceAnalysisDocumentHash(value.Document)
		}},
		{name: "proposal reference outside candidate", mutate: func(value *WorkspaceAnalysisCandidate) {
			document := WorkspaceAnalysisCandidateResult{
				ResultType: ResultTypeWorkspaceAnalysisCandidate, SchemaID: WorkspaceAnalysisCandidateSchemaID,
				SchemaVersion: workspaceAnalysisCandidateDocumentVersion, ModelRunRef: value.SynthesisModelRunID,
				Payload: WorkspaceAnalysisCandidatePayload{
					AnswerMarkdown: "answer", CitationRefs: []string{"E1"},
					ProposalSuggestion: &WorkspaceAnalysisCandidateProposal{Summary: "proposal", CitationRefs: []string{"E2"}},
				},
			}
			value.Document, _ = json.Marshal(document)
			value.DocumentBytes = int64(len(value.Document))
			value.DocumentHash = workspaceAnalysisDocumentHash(value.Document)
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			value := cloneWorkspaceAnalysisCandidate(candidate)
			test.mutate(&value)
			if err := ValidateWorkspaceAnalysisCandidate(value); errorCode(err) != ErrorCodeWorkspaceAnalysisCandidateInvalid {
				t.Fatalf("invalid candidate accepted or wrong error: %v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisCandidateBindsSynthesisOperationAndModelRun(t *testing.T) {
	run := validWorkspaceAnalysisPersistenceRun()
	run.Status = WorkspaceAnalysisRunRunning
	contract, _ := workspaceAnalysisOperationContract(WorkspaceAnalysisOperationNodeSynthesizeAnswer, WorkspaceAnalysisOperationAnswerSynthesis, 1)
	operation := validStartedWorkspaceAnalysisOperation(contract, 41)
	operation.AnalysisRunID = run.ID
	candidate := validWorkspaceAnalysisCandidate()
	candidate.WorkspaceID = run.WorkspaceID
	candidate.AnalysisRunID = run.ID
	candidate.AnswerID = run.AnswerID
	candidate.SynthesisOperationID = operation.ID
	candidate.NodeAttemptID = *operation.FirstNodeAttemptID
	candidate.SynthesisModelRunID = workspaceAnalysisPersistenceID(51)
	rebindWorkspaceAnalysisCandidateDocument(&candidate)

	modelRun := validModelRun()
	completed := candidate.CreatedAt.Add(-time.Second)
	modelRun.ID = candidate.SynthesisModelRunID
	modelRun.WorkspaceID = candidate.WorkspaceID
	modelRun.WorkflowRunID = run.WorkflowRunID
	modelRun.NodeAttemptID = candidate.NodeAttemptID
	modelRun.Schema = SchemaRef{ID: WorkspaceAnalysisCandidateSchemaID, Version: "1"}
	modelRun.ReducedSchema = modelRun.Schema
	modelRun.Status = ModelRunSucceeded
	modelRun.FinalResultType = ResultTypeWorkspaceAnalysisAnswer
	modelRun.UpdatedAt = completed
	modelRun.CompletedAt = &completed
	if err := ValidateWorkspaceAnalysisCandidateBinding(candidate, run, operation, modelRun); err != nil {
		t.Fatalf("candidate binding rejected: %v", err)
	}

	succeeded := mutateWorkspaceAnalysisOperation(operation, func(value *WorkspaceAnalysisOperation) {
		completedAt := value.UpdatedAt.Add(time.Second)
		value.Status = WorkspaceAnalysisOperationSucceeded
		value.Version++
		value.UpdatedAt = completedAt
		value.CompletedAt = &completedAt
		value.Result = &WorkspaceAnalysisOperationResultRef{
			Kind: WorkspaceAnalysisOperationResultCandidate,
			ID:   candidate.ID,
			Hash: candidate.DocumentHash,
		}
	})
	if err := ValidateWorkspaceAnalysisCandidateBinding(candidate, run, succeeded, modelRun); err != nil {
		t.Fatalf("completed candidate binding rejected: %v", err)
	}
	replacement := mutateWorkspaceAnalysisOperation(succeeded, func(value *WorkspaceAnalysisOperation) {
		value.LatestNodeAttemptID = workspaceAnalysisOperationIDPointer(991)
		value.Version++
		value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	})
	if *replacement.FirstNodeAttemptID == *replacement.LatestNodeAttemptID {
		t.Fatal("replacement fixture did not advance LatestNodeAttemptID")
	}
	if err := ValidateWorkspaceAnalysisCandidateBinding(candidate, run, replacement, modelRun); err != nil {
		t.Fatalf("replacement rejected the candidate bound to the first attempt: %v", err)
	}

	modelRun.FinalResultType = ResultTypeWorkspaceAnalysisPlan
	if err := ValidateWorkspaceAnalysisCandidateBinding(candidate, run, operation, modelRun); errorCode(err) != ErrorCodeWorkspaceAnalysisCandidateBindingMismatch {
		t.Fatalf("candidate accepted planner model run: %v", err)
	}
	modelRun.FinalResultType = ResultTypeWorkspaceAnalysisAnswer
	candidate.CreatedAt = modelRun.CompletedAt.Add(-time.Nanosecond)
	if err := ValidateWorkspaceAnalysisCandidateBinding(candidate, run, operation, modelRun); errorCode(err) != ErrorCodeWorkspaceAnalysisCandidateBindingMismatch {
		t.Fatalf("candidate accepted creation before model completion: %v", err)
	}
}

type workspaceAnalysisModelResultFixture struct {
	result    WorkspaceAnalysisModelResult
	run       WorkspaceAnalysisRun
	operation WorkspaceAnalysisOperation
	modelRun  ModelRun
	modelCall ModelCall
	candidate *WorkspaceAnalysisCandidate
}

func validWorkspaceAnalysisModelResultFixture(t *testing.T, kind WorkspaceAnalysisOperationKind) workspaceAnalysisModelResultFixture {
	t.Helper()

	run := validWorkspaceAnalysisPersistenceRun()
	run.Status = WorkspaceAnalysisRunRunning
	operationIndex := 61
	nodeKey := WorkspaceAnalysisOperationNodeRetrieveEvidence
	schema := SchemaRef{ID: WorkspaceAnalysisPlanSchemaID, Version: OutputSchemaVersionV1}
	resultType := ResultTypeWorkspaceAnalysisPlan
	phase := ModelCallPlan
	modelRunID := workspaceAnalysisPersistenceID(601)
	resultID := workspaceAnalysisPersistenceID(701)
	var candidate *WorkspaceAnalysisCandidate
	if kind == WorkspaceAnalysisOperationFaithfulnessReview {
		operationIndex = 62
		nodeKey = WorkspaceAnalysisOperationNodeReviewPublish
		schema = SchemaRef{ID: FaithfulnessReviewSchemaID, Version: OutputSchemaVersionV1}
		resultType = ResultTypeFaithfulnessReview
		phase = ModelCallReview
		modelRunID = workspaceAnalysisPersistenceID(621)
		resultID = workspaceAnalysisPersistenceID(721)
		value := validWorkspaceAnalysisCandidate()
		value.WorkspaceID = run.WorkspaceID
		value.AnalysisRunID = run.ID
		value.AnswerID = run.AnswerID
		candidate = &value
	}
	contract, exists := workspaceAnalysisOperationContract(nodeKey, kind, 1)
	if !exists {
		t.Fatalf("workspace analysis operation contract missing for %s", kind)
	}
	operation := validStartedWorkspaceAnalysisOperation(contract, operationIndex)
	operation.AnalysisRunID = run.ID

	resultCreatedAt := time.Date(2026, 8, 15, 10, 4, 0, 0, time.UTC)
	modelRunCompletedAt := resultCreatedAt.Add(-2 * time.Second)
	modelRun := validModelRun()
	modelRun.ID = modelRunID
	modelRun.WorkspaceID = run.WorkspaceID
	modelRun.WorkflowRunID = run.WorkflowRunID
	modelRun.NodeRunID = workspaceAnalysisPersistenceID(602 + operationIndex)
	modelRun.NodeAttemptID = *operation.FirstNodeAttemptID
	modelRun.Schema = schema
	modelRun.ReducedSchema = schema
	modelRun.Status = ModelRunSucceeded
	modelRun.FinalResultType = resultType
	modelRun.Version = 2
	modelRun.UpdatedAt = modelRunCompletedAt
	modelRun.CompletedAt = &modelRunCompletedAt

	var document []byte
	var err error
	if kind == WorkspaceAnalysisOperationRetrievalPlan {
		document, err = json.Marshal(WorkspaceAnalysisPlanResult{
			ResultType:    ResultTypeWorkspaceAnalysisPlan,
			SchemaID:      WorkspaceAnalysisPlanSchemaID,
			SchemaVersion: OutputSchemaVersionV1,
			ModelRunRef:   modelRun.ID,
			Payload: RAGQueryPlanPayload{
				Intent:                "find workspace policy",
				RequiresClarification: false,
				Rewrites:              []string{"workspace policy"},
				ClarificationReason:   "",
				ClarificationQuestion: "",
				SuggestedScopes:       []string{},
			},
		})
	} else {
		document, err = json.Marshal(FaithfulnessReviewResult{
			ResultType:    ResultTypeFaithfulnessReview,
			SchemaID:      FaithfulnessReviewSchemaID,
			SchemaVersion: OutputSchemaVersionV1,
			ModelRunRef:   modelRun.ID,
			Payload: FaithfulnessReviewPayload{
				Passed: true,
				Items: []FaithfulnessReviewItem{{
					AssertionID: "a-1",
					Verdict:     FaithfulnessSupported,
					CitationIDs: []string{"cite-1"},
					Reason:      "supported by evidence",
				}},
				Summary: "all factual assertions are supported",
			},
		})
	}
	if err != nil {
		t.Fatalf("marshal %s model result: %v", kind, err)
	}

	modelCallCompletedAt := resultCreatedAt.Add(-time.Second)
	modelCall := validStartedCall()
	modelCall.ID = operation.Call.ID
	modelCall.ModelRunID = modelRun.ID
	modelCall.Phase = phase
	modelCall.Schema = schema
	modelCall.Status = ModelCallSucceeded
	modelCall.ResponseHash = workspaceAnalysisDocumentHash(document)
	modelCall.ResponseBytes = int64(len(document))
	modelCall.Usage = TokenUsage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}
	modelCall.LatencyMillis = 1
	modelCall.Version = 2
	modelCall.StartedAt = *operation.StartedAt
	modelCall.CompletedAt = &modelCallCompletedAt

	result := WorkspaceAnalysisModelResult{
		ID:            resultID,
		WorkspaceID:   run.WorkspaceID,
		AnalysisRunID: run.ID,
		OperationID:   operation.ID,
		NodeAttemptID: *operation.FirstNodeAttemptID,
		ModelRunID:    modelRun.ID,
		ModelCallID:   modelCall.ID,
		OperationKind: kind,
		Schema:        schema,
		Document:      document,
		DocumentHash:  workspaceAnalysisDocumentHash(document),
		DocumentBytes: int64(len(document)),
		CreatedAt:     resultCreatedAt,
	}
	if candidate != nil {
		result.SubjectCandidateID = workspaceAnalysisPersistenceIDCopy(candidate.ID)
		result.SubjectCandidateHash = candidate.DocumentHash
	}
	return workspaceAnalysisModelResultFixture{
		result: result, run: run, operation: operation, modelRun: modelRun, modelCall: modelCall, candidate: candidate,
	}
}

func succeededWorkspaceAnalysisModelResultOperation(operation WorkspaceAnalysisOperation, result WorkspaceAnalysisModelResult) WorkspaceAnalysisOperation {
	return mutateWorkspaceAnalysisOperation(operation, func(value *WorkspaceAnalysisOperation) {
		completedAt := result.CreatedAt
		value.Status = WorkspaceAnalysisOperationSucceeded
		value.Version++
		value.UpdatedAt = completedAt
		value.CompletedAt = &completedAt
		value.Result = &WorkspaceAnalysisOperationResultRef{
			Kind: WorkspaceAnalysisOperationResultModelCall,
			ID:   result.ID,
			Hash: result.DocumentHash,
		}
	})
}

func cloneWorkspaceAnalysisModelResult(value WorkspaceAnalysisModelResult) WorkspaceAnalysisModelResult {
	value.Document = append([]byte(nil), value.Document...)
	if value.SubjectCandidateID != nil {
		value.SubjectCandidateID = workspaceAnalysisPersistenceIDCopy(*value.SubjectCandidateID)
	}
	return value
}

func validWorkspaceAnalysisPersistenceRun() WorkspaceAnalysisRun {
	createdAt := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	timeouts := workspaceAnalysisPersistenceTimeouts()
	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(timeouts)
	if err != nil {
		panic(err)
	}
	return WorkspaceAnalysisRun{
		ID:                workspaceAnalysisPersistenceID(1),
		WorkspaceID:       workspaceAnalysisPersistenceID(2),
		ConversationID:    workspaceAnalysisPersistenceID(3),
		QuestionID:        workspaceAnalysisPersistenceID(4),
		AnswerID:          workspaceAnalysisPersistenceID(5),
		WorkflowRunID:     workspaceAnalysisPersistenceID(6),
		DefinitionKey:     workspaceAnalysisDefinitionKey,
		DefinitionVersion: workspaceAnalysisDefinitionVersion,
		DefinitionHash:    strings.Repeat("a", 64),
		ToolCatalogHash:   strings.Repeat("b", 64),
		PolicyVersion:     WorkspaceAnalysisPolicyVersionV1,
		ConfigRevision:    1,
		DeadlineAt:        createdAt.Add(deadlines.RunDeadline()),
		Timeouts:          timeouts,
		Limits: WorkspaceAnalysisBudgetLimits{
			Nodes: WorkspaceAnalysisV1MaxNodes, ToolConcurrency: WorkspaceAnalysisV1MaxToolConcurrency,
			Amount: WorkspaceAnalysisBudgetAmount{
				ModelCalls: WorkspaceAnalysisV1MaxModelCalls, ToolCalls: WorkspaceAnalysisV1MaxToolCalls,
				SourceReads: WorkspaceAnalysisV1MaxSourceReads, InputTokens: WorkspaceAnalysisV1MaxRunInputTokens,
				OutputTokens: WorkspaceAnalysisV1MaxRunOutputTokens,
			},
		},
		Status: WorkspaceAnalysisRunQueued, Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func terminalWorkspaceAnalysisPersistenceRun(status WorkspaceAnalysisRunStatus, reason WorkspaceAnalysisRunTerminationReason) WorkspaceAnalysisRun {
	run := validWorkspaceAnalysisPersistenceRun()
	completedAt := run.CreatedAt.Add(time.Minute)
	run.Status = status
	run.TerminationReason = reason
	run.Version = 2
	run.UpdatedAt = completedAt
	run.CompletedAt = &completedAt
	return run
}

func validWorkspaceAnalysisModelReservation() WorkspaceAnalysisBudgetReservation {
	return WorkspaceAnalysisBudgetReservation{
		ID:            workspaceAnalysisPersistenceID(101),
		WorkspaceID:   workspaceAnalysisPersistenceID(102),
		AnalysisRunID: workspaceAnalysisPersistenceID(103),
		OperationID:   workspaceAnalysisPersistenceID(104),
		CallKind:      WorkspaceAnalysisOperationCallModel,
		ModelCallID:   workspaceAnalysisPersistenceIDPointer(105),
		Status:        WorkspaceAnalysisBudgetReserved,
		Reserved: WorkspaceAnalysisBudgetAmount{
			ModelCalls: 1, InputTokens: WorkspaceAnalysisV1MaxInputTokensPerModelCall, OutputTokens: 1024,
		},
		CreatedAt: time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC),
	}
}

func workspaceAnalysisPersistenceTimeouts() WorkspaceAnalysisV1Timeouts {
	return WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: 2 * time.Minute, SynthesisModelTimeout: 5 * time.Minute, ReviewModelTimeout: 3 * time.Minute,
		GitToolTimeout: 30 * time.Second, SearchToolTimeout: 45 * time.Second,
		SourceReadToolTimeout: 20 * time.Second, ValidateCitationToolTimeout: 25 * time.Second,
	}
}

func validWorkspaceAnalysisToolReservation(sourceRead bool) WorkspaceAnalysisBudgetReservation {
	reservation := WorkspaceAnalysisBudgetReservation{
		ID:            workspaceAnalysisPersistenceID(111),
		WorkspaceID:   workspaceAnalysisPersistenceID(112),
		AnalysisRunID: workspaceAnalysisPersistenceID(113),
		OperationID:   workspaceAnalysisPersistenceID(114),
		CallKind:      WorkspaceAnalysisOperationCallTool,
		ToolCallID:    workspaceAnalysisPersistenceIDPointer(115),
		Status:        WorkspaceAnalysisBudgetReserved,
		Reserved:      WorkspaceAnalysisBudgetAmount{ToolCalls: 1},
		CreatedAt:     time.Date(2026, 8, 15, 10, 2, 0, 0, time.UTC),
	}
	if sourceRead {
		reservation.Reserved.SourceReads = 1
	}
	return reservation
}

func validWorkspaceAnalysisCandidate() WorkspaceAnalysisCandidate {
	candidate := WorkspaceAnalysisCandidate{
		ID:                   workspaceAnalysisPersistenceID(201),
		WorkspaceID:          workspaceAnalysisPersistenceID(202),
		AnalysisRunID:        workspaceAnalysisPersistenceID(203),
		AnswerID:             workspaceAnalysisPersistenceID(204),
		SynthesisOperationID: workspaceAnalysisPersistenceID(205),
		NodeAttemptID:        workspaceAnalysisPersistenceID(206),
		SynthesisModelRunID:  workspaceAnalysisPersistenceID(207),
		SchemaID:             WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion:        WorkspaceAnalysisCandidateSchemaVersion,
		CreatedAt:            time.Date(2026, 8, 15, 10, 3, 0, 0, time.UTC),
	}
	rebindWorkspaceAnalysisCandidateDocument(&candidate)
	return candidate
}

func rebindWorkspaceAnalysisCandidateDocument(candidate *WorkspaceAnalysisCandidate) {
	document, err := json.Marshal(WorkspaceAnalysisCandidateResult{
		ResultType:    ResultTypeWorkspaceAnalysisCandidate,
		SchemaID:      WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion: workspaceAnalysisCandidateDocumentVersion,
		ModelRunRef:   candidate.SynthesisModelRunID,
		Payload: WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown:     "private-candidate-body",
			CitationRefs:       []string{"E1"},
			ProposalSuggestion: nil,
		},
	})
	if err != nil {
		panic(err)
	}
	candidate.Document = document
	candidate.DocumentHash = workspaceAnalysisDocumentHash(document)
	candidate.DocumentBytes = int64(len(document))
}

func workspaceAnalysisPersistenceID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8b000000-0000-4000-8000-%012d", value))
}

func workspaceAnalysisPersistenceIDPointer(value int) *foundation.ID {
	id := workspaceAnalysisPersistenceID(value)
	return &id
}

func workspaceAnalysisPersistenceIDCopy(value foundation.ID) *foundation.ID { return &value }

func workspaceAnalysisPersistenceTimePointer(value time.Time) *time.Time { return &value }

func cloneWorkspaceAnalysisAmount(value WorkspaceAnalysisBudgetAmount) WorkspaceAnalysisBudgetAmount {
	if value.CostMicrounits != nil {
		cost := *value.CostMicrounits
		value.CostMicrounits = &cost
	}
	return value
}

func cloneWorkspaceAnalysisPersistenceRun(value WorkspaceAnalysisRun) WorkspaceAnalysisRun {
	value.Limits.Amount = cloneWorkspaceAnalysisAmount(value.Limits.Amount)
	value.Reserved = cloneWorkspaceAnalysisAmount(value.Reserved)
	value.Settled = cloneWorkspaceAnalysisAmount(value.Settled)
	if value.ValidationReceiptID != nil {
		value.ValidationReceiptID = workspaceAnalysisPersistenceIDCopy(*value.ValidationReceiptID)
	}
	if value.ReviewModelRunID != nil {
		value.ReviewModelRunID = workspaceAnalysisPersistenceIDCopy(*value.ReviewModelRunID)
	}
	if value.CompletedAt != nil {
		value.CompletedAt = workspaceAnalysisPersistenceTimePointer(*value.CompletedAt)
	}
	return value
}

func cloneWorkspaceAnalysisReservation(value WorkspaceAnalysisBudgetReservation) WorkspaceAnalysisBudgetReservation {
	if value.ModelCallID != nil {
		value.ModelCallID = workspaceAnalysisPersistenceIDCopy(*value.ModelCallID)
	}
	if value.ToolCallID != nil {
		value.ToolCallID = workspaceAnalysisPersistenceIDCopy(*value.ToolCallID)
	}
	value.Reserved = cloneWorkspaceAnalysisAmount(value.Reserved)
	value.Settled = cloneWorkspaceAnalysisAmount(value.Settled)
	if value.SettledAt != nil {
		value.SettledAt = workspaceAnalysisPersistenceTimePointer(*value.SettledAt)
	}
	return value
}

func cloneWorkspaceAnalysisCandidate(value WorkspaceAnalysisCandidate) WorkspaceAnalysisCandidate {
	value.Document = append([]byte(nil), value.Document...)
	return value
}
