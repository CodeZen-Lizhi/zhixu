//go:build integration

package synthesispostgres

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestSynthesisExecutionRejectsForgedInputAndTerminalState(t *testing.T) {
	f := newSynthesisRuntimeFixtureWithWorker(t, false)
	f.source(t, "Source processing cannot be made successful without a Workflow receipt.")
	f.dispatch(t, 1)
	var processing processingModel
	if err := f.database.Where("workspace_id=?", string(f.workspace)).Take(&processing).Error; err != nil {
		t.Fatal(err)
	}
	// All required keys exist, but JSON null used to turn the CHECK predicate
	// into SQL NULL. The nullable shape constraint must reject that document.
	err := f.database.Exec(`UPDATE organizing.synthesis_execution
		SET input_document=jsonb_build_object('processing_id',processing_id::text,'workflow_run_id',workflow_run_id::text,
		'source_event','{}'::jsonb,'notes','[]'::jsonb,'sources','[{}]'::jsonb,'request_hash',NULL),
		input_hash=?,version=version+1,updated_at=clock_timestamp()
		WHERE processing_id=?`, runtimeHash("forged input"), processing.ID).Error
	if platformpostgres.SQLState(err) != "23514" || platformpostgres.ConstraintName(err) != "synthesis_execution_input_shape" {
		t.Fatalf("JSON null bypassed frozen input shape: %v", err)
	}
	err = f.database.Exec(`UPDATE organizing.synthesis_processing
		SET status='NO_CHANGE',version=version+1,updated_at=now(),completed_at=now() WHERE id=?`, processing.ID).Error
	if platformpostgres.SQLState(err) != "23514" {
		t.Fatalf("unproved processing success was accepted: %v", err)
	}
	err = f.database.Exec(`TRUNCATE organizing.synthesis_processing,organizing.synthesis_execution,
		organizing.synthesis_model_step,organizing.synthesis_retry_receipt`).Error
	if platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("synthesis history could be truncated: %v", err)
	}
	current, err := f.store.GetSynthesisProcessing(t.Context(), f.workspace, foundation.ID(processing.ID))
	if err != nil || current.Status != organizingapp.SynthesisProcessingPending || current.Version != 1 {
		t.Fatalf("rejected SQL changed authoritative processing: %+v %v", current, err)
	}
	f.count(t, "organizing.synthesis_execution", 1)
	f.count(t, "core.source_version", 1)
}

func TestSynthesisExplicitPublicationRecoveryKeepsCommittedProposal(t *testing.T) {
	f := newSynthesisRuntimeFixture(t, runtimeFactOutput, runtimeFactReview)
	f.publisher.loseResponse.Store(true)
	source := f.source(t, "Entries expire after five minutes.")
	f.dispatch(t, 1)
	failed := f.wait(t, source, organizingapp.SynthesisProcessingFailed)
	if failed.Failure == nil || failed.Failure.Code != "SYNTHESIS_FIXTURE_RESPONSE_LOST" || !failed.Failure.Retryable || f.provider.CallCount() != 2 {
		t.Fatalf("publication response loss=%+v calls=%d", failed, f.provider.CallCount())
	}
	notes, err := f.notes.ListCandidates(t.Context(), f.workspace)
	if err != nil || len(notes) != 1 {
		t.Fatalf("committed candidate missing: notes=%d err=%v", len(notes), err)
	}
	before, err := f.notes.GetNote(t.Context(), f.workspace, notes[0].Note.ID)
	if err != nil || before.Publication == nil {
		t.Fatalf("committed Proposal missing: %+v %v", before, err)
	}
	f.artifacts.mu.Lock()
	delete(f.artifacts.values, source.SourceVersionID)
	f.artifacts.mu.Unlock()
	f.model.disabled.Store(true)
	retried, err := f.processing.RetryProcessing(t.Context(), organizingapp.RetrySynthesisCommand{
		WorkspaceID: f.workspace, ProcessingID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "recover-committed-publication",
	})
	if err != nil || retried.Replayed || retried.Processing.WorkflowRunID == failed.WorkflowRunID {
		t.Fatalf("explicit publication recovery failed to start: %+v %v", retried, err)
	}
	completed := f.wait(t, source, organizingapp.SynthesisProcessingSucceeded)
	execution, err := f.store.LoadSynthesisExecution(t.Context(), f.workspace, failed.ID, completed.WorkflowRunID)
	if err != nil || !execution.ApplyRecovery || execution.Input != nil || execution.Generation != nil || execution.Semantic != nil || execution.Applied == nil {
		t.Fatalf("publication recovery reopened model input: %+v %v", execution, err)
	}
	after, err := f.notes.GetNote(t.Context(), f.workspace, before.Note.ID)
	if err != nil || after.Publication == nil || after.Publication.ProposalID != before.Publication.ProposalID || after.CurrentRevision.ID != before.CurrentRevision.ID || f.provider.CallCount() != 2 {
		t.Fatalf("recovery duplicated Proposal, revision or model work: %+v calls=%d err=%v", after, f.provider.CallCount(), err)
	}
	f.count(t, "organizing.synthesis_execution", 2)
	f.count(t, "organizing.synthesis_retry_receipt", 1)
	f.count(t, "organizing.synthesis_apply_receipt", 1)
	f.count(t, "organizing.synthesis_revision", 1)
	f.count(t, "organizing.synthesis_model_step", 2)
	f.count(t, "agent.model_run", 2)
	f.count(t, "change_control.proposal", 1)
	f.count(t, "change_control.proposal_commit", 0)
	f.count(t, "core.source_version", 1)
}

func TestSynthesisFailedModelDoesNotRepeatAfterLostWorkflowFailure(t *testing.T) {
	// Invalid output exhausts the bounded INITIAL/REPAIR/REDUCED sequence.
	// Extra valid output would expose any accidental paid replay after rescue.
	f := newSynthesisRuntimeFixtureWithWorker(t, false, `{}`, `{}`, `{}`, `{"notes":[]}`, runtimeNoChangeReview)
	source := f.source(t, "A source whose first model execution fails validation.")
	f.dispatch(t, 1)
	prepare, binding := f.claimNode(t, organizingworkflow.SynthesisPrepareNodeKind, 1)
	result, err := f.executor.Execute(t.Context(), synthesisRuntimeExecution(prepare))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Complete(t.Context(), workflowapp.CompleteDeliveryCommand{Binding: binding, Output: result.Output, OutputSchemaVersion: organizingworkflow.SynthesisOutputSchemaVersion}); err != nil {
		t.Fatal(err)
	}
	first, _ := f.claimNode(t, organizingworkflow.SynthesisGenerateNodeKind, 1)
	_, firstError := f.executor.Execute(t.Context(), synthesisRuntimeExecution(first))
	var firstFailure *foundation.Error
	if !errors.As(firstError, &firstFailure) || f.provider.CallCount() != 3 {
		t.Fatalf("expected bounded model failure: error=%v calls=%d", firstError, f.provider.CallCount())
	}
	var step modelStepModel
	if err := f.database.Where("workflow_run_id=?", string(first.Run.ID)).Take(&step).Error; err != nil || step.Status != string(organizingapp.SynthesisModelStepFailed) {
		t.Fatalf("failed model result was not durable: status=%s err=%v", step.Status, err)
	}
	// Do not acknowledge the failure to Workflow: the process has died after
	// committing its failed ModelRun. Let the real DB-time lease expire.
	timer := time.NewTimer(time.Until(first.Attempt.LeaseUntil) + 20*time.Millisecond)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-timer.C:
	}
	rescued, binding := f.claimNode(t, organizingworkflow.SynthesisGenerateNodeKind, 2)
	if !rescued.LeaseReclaimed || rescued.Attempt.ID == first.Attempt.ID {
		t.Fatalf("expected a new attempt after lease rescue: %+v", rescued.Attempt)
	}
	_, replayError := f.executor.Execute(t.Context(), synthesisRuntimeExecution(rescued))
	var replayFailure *foundation.Error
	if !errors.As(replayError, &replayFailure) || replayFailure.Code != firstFailure.Code || replayFailure.Retryable || f.provider.CallCount() != 3 {
		t.Fatalf("lease rescue repeated model work: error=%v calls=%d", replayError, f.provider.CallCount())
	}
	if _, err := f.coordinator.Fail(t.Context(), workflowapp.FailDeliveryCommand{Binding: binding, Failure: workflowdomain.FailureInput{Err: replayError}}); err != nil {
		t.Fatal(err)
	}
	failed := f.wait(t, source, organizingapp.SynthesisProcessingFailed)
	if failed.Failure == nil || failed.Failure.Code != firstFailure.Code {
		t.Fatalf("rescued execution lost the original model failure: %+v", failed.Failure)
	}
	f.count(t, "organizing.synthesis_model_step", 1)
	f.count(t, "agent.model_run", 1)
	var calls int64
	if err := f.database.Table("agent.model_call c").Joins("JOIN agent.model_run r ON r.id=c.model_run_id").Where("r.workspace_id=?", string(f.workspace)).Count(&calls).Error; err != nil || calls != 3 {
		t.Fatalf("recorded model calls=%d err=%v", calls, err)
	}
	f.count(t, "organizing.synthesis_revision", 0)
	f.count(t, "change_control.proposal", 0)
	f.count(t, "core.source_version", 1)
}

func (f *synthesisRuntimeFixture) claimNode(t *testing.T, kind string, deliveryNo int) (workflowapp.ClaimResult, workflowapp.DeliveryBinding) {
	t.Helper()
	var row struct {
		NodeRunID  string
		DispatchNo int
		RiverJobID int64
	}
	if err := f.database.Raw(`SELECT n.id::text AS node_run_id,n.dispatch_no,j.id AS river_job_id
		FROM workflow.node_run n JOIN workflow.run r ON r.id=n.run_id
		JOIN workflow.river_job j ON j.args->>'node_run_id'=n.id::text
		WHERE r.workspace_id=? AND n.node_type=? ORDER BY j.id DESC LIMIT 1`, string(f.workspace), kind).Scan(&row).Error; err != nil || row.NodeRunID == "" {
		t.Fatalf("queued synthesis node missing: %+v %v", row, err)
	}
	deliveryID := fmt.Sprintf("synthesis-fixture-%s-%d", row.NodeRunID, deliveryNo)
	claim, err := f.coordinator.Claim(t.Context(), workflowapp.ClaimCommand{
		NodeRunID: foundation.ID(row.NodeRunID), DispatchNo: row.DispatchNo, DeliveryID: deliveryID,
		RiverJobID: row.RiverJobID, RiverJobAttempt: deliveryNo, LeaseOwner: "synthesis-fixture", LeaseDuration: 2 * time.Second,
	})
	if err != nil || claim.Disposition != workflowapp.ClaimDispositionClaimed {
		t.Fatalf("claim synthesis node: disposition=%s err=%v", claim.Disposition, err)
	}
	return claim, workflowapp.DeliveryBinding{NodeRunID: claim.Node.ID, DispatchNo: claim.Node.DispatchNo, DeliveryID: deliveryID,
		Fence: workflowdomain.LeaseFence{Owner: claim.Attempt.LeaseOwner, AttemptNo: claim.Attempt.AttemptNo, NodeVersion: claim.Node.Version}}
}

func synthesisRuntimeExecution(claim workflowapp.ClaimResult) workflowapp.ExecutionContext {
	return workflowapp.ExecutionContext{
		WorkspaceID: claim.Run.WorkspaceID, DefinitionID: claim.Definition.ID, DefinitionVersion: claim.Definition.Version,
		DefinitionHash: claim.Definition.GraphHash, RunID: claim.Run.ID, NodeKey: claim.Node.NodeKey,
		NodeRunID: claim.Node.ID, NodeAttemptID: claim.Attempt.ID, NodeKind: claim.Node.NodeType,
		ModelSettingsRevision: claim.Attempt.ModelSettingsRevision, NodeVersion: claim.Node.Version,
		InputSchemaVersion: claim.Node.InputSchemaVersion, AttemptNo: claim.Attempt.AttemptNo,
		DispatchNo: claim.Node.DispatchNo, RetryNo: claim.Node.RetryNo, LeaseOwner: claim.Attempt.LeaseOwner,
		Input: claim.Node.Input,
	}
}
