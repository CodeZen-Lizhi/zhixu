package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestTrustedWriteAuditPreservesOriginalAttemptAndReconcilesCanonicalReceipt(t *testing.T) {
	registry := NewContractRegistry()
	contract := trustedWriteExecutionContract()
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	repository := &executionTestRepository{}
	clock := foundation.FixedClock{Value: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
	service, err := NewTrustedWriteAuditService(registry, repository, &executionTestIDs{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	receiptID := foundation.ID("00000000-0000-4000-8000-000000000090")
	command := TrustedWriteAuditCommand{
		Identity: executionTestIdentity(), Tool: domain.ToolRef{Name: "ApplyApprovedPatch", Version: 1}, CallNo: 1,
		Arguments:      json.RawMessage(`{"writeback_execution_id":"00000000-0000-4000-8000-000000000090"}`),
		IdempotencyKey: "safe-writeback-tool:00000000-0000-4000-8000-000000000090:apply:v1", WritebackReceipt: receiptID,
	}
	started, err := service.EnsureStarted(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != domain.CallStarted || started.NodeAttemptID != command.Identity.NodeAttemptID || started.SideEffectType != "writeback_execution" || started.SideEffectID != string(receiptID) {
		t.Fatalf("started=%+v", started)
	}

	reclaimed := command
	reclaimed.Identity.NodeAttemptID = "00000000-0000-4000-8000-000000000055"
	reclaimed.Identity.LeaseOwner = "worker-2"
	reclaimed.Identity.LeaseFence = 2
	replayed, err := service.EnsureStarted(context.Background(), reclaimed)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != started.ID || replayed.NodeAttemptID != started.NodeAttemptID {
		t.Fatalf("replayed call lost original attempt: started=%+v replayed=%+v", started, replayed)
	}

	output := json.RawMessage(`{"writeback_execution_id":"00000000-0000-4000-8000-000000000090","result_ref":"writeback-execution:00000000-0000-4000-8000-000000000090","result_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"APPLIED"}`)
	succeeded, err := service.RecordSucceeded(context.Background(), TrustedWriteAuditReceipt{Command: reclaimed, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.Status != domain.CallSucceeded || succeeded.NodeAttemptID != started.NodeAttemptID || succeeded.ResultRef != "writeback-execution:"+string(receiptID) {
		t.Fatalf("succeeded=%+v", succeeded)
	}
	repository.trusted = &succeeded
	replayedSuccess, err := service.RecordSucceeded(context.Background(), TrustedWriteAuditReceipt{Command: reclaimed, Output: output})
	if err != nil || replayedSuccess.ID != succeeded.ID {
		t.Fatalf("replayed success=%+v err=%v", replayedSuccess, err)
	}
}

func TestTrustedWriteAuditRequiresHistoricalStartAfterSideEffect(t *testing.T) {
	registry := NewContractRegistry()
	if err := registry.RegisterContract(trustedWriteExecutionContract()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	service, err := NewTrustedWriteAuditService(registry, &executionTestRepository{}, &executionTestIDs{}, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RequireStarted(context.Background(), TrustedWriteAuditCommand{
		Identity: executionTestIdentity(), Tool: applyApprovedPatchRefForTest(), CallNo: 1,
		Arguments:        json.RawMessage(`{"writeback_execution_id":"00000000-0000-4000-8000-000000000090"}`),
		IdempotencyKey:   "safe-writeback-tool:00000000-0000-4000-8000-000000000090:apply:v1",
		WritebackReceipt: "00000000-0000-4000-8000-000000000090",
	})
	if errorCode(err) != errorCodeTrustedWriteAuditMissing {
		t.Fatalf("error=%v", err)
	}
}

func TestTrustedWriteAuditPreservesCurrentAttemptFailureAndDoesNotFinalize(t *testing.T) {
	registry := NewContractRegistry()
	contract := trustedWriteExecutionContract()
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	stale := foundation.NewError(foundation.ErrorVersionConflict, "TOOL_CONTEXT_STALE", false, errors.New("attempt lease is stale"))
	repository := &executionTestRepository{trustedLoadErr: stale}
	service, err := NewTrustedWriteAuditService(registry, repository, &executionTestIDs{}, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	command := TrustedWriteAuditCommand{
		Identity: executionTestIdentity(), Tool: applyApprovedPatchRefForTest(), CallNo: 1,
		Arguments:        json.RawMessage(`{"writeback_execution_id":"00000000-0000-4000-8000-000000000090"}`),
		IdempotencyKey:   "safe-writeback-tool:00000000-0000-4000-8000-000000000090:apply:v1",
		WritebackReceipt: "00000000-0000-4000-8000-000000000090",
	}
	if _, err := service.RequireStarted(context.Background(), command); errorCode(err) != "TOOL_CONTEXT_STALE" {
		t.Fatalf("RequireStarted error=%v", err)
	}
	if repository.trustedLoad.Identity != command.Identity || repository.trustedLoad.Tool != command.Tool || repository.trustedLoad.Capability != contract.Definition.RequiredCapability ||
		repository.trustedLoad.IdempotencyKey != command.IdempotencyKey || len(repository.trustedLoad.AllowedWorkflows) != len(contract.Definition.AllowedWorkflows) {
		t.Fatalf("trusted load command=%+v", repository.trustedLoad)
	}
	if _, err := service.RecordSucceeded(context.Background(), TrustedWriteAuditReceipt{Command: command, Output: json.RawMessage(`{}`)}); errorCode(err) != "TOOL_CONTEXT_STALE" {
		t.Fatalf("RecordSucceeded error=%v", err)
	}
	if len(repository.finalized) != 0 {
		t.Fatalf("stale attempt finalized calls=%d", len(repository.finalized))
	}
}

func applyApprovedPatchRefForTest() domain.ToolRef {
	return domain.ToolRef{Name: "ApplyApprovedPatch", Version: 1}
}
