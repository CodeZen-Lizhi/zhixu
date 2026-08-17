package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestExecutionServiceWorkspaceAnalysisContractInvalidClosesReceiptFailure(t *testing.T) {
	contract := workspaceAnalysisGitExecutionContract()
	rawOutput := json.RawMessage(`contract-invalid-output-canary`)
	executor := &executionTestExecutor{result: ExecutorResult{Output: rawOutput}}
	service, repository, policy := newExecutionTestService(t, contract, executor)
	command := workspaceAnalysisGitExecutionCommand(policy)

	_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
	if errorCode(err) != errorCodeOutputInvalid || executor.calls != 1 || len(repository.receiptFailures) != 1 ||
		len(repository.receiptFinalized) != 0 || len(repository.workspaceFinalized) != 0 ||
		len(repository.finalized) != 0 || len(repository.unknown) != 0 {
		t.Fatalf("error=%v executor=%d failures=%d receipts=%d workspace_terminal=%d generic_failed=%d unknown=%d",
			err, executor.calls, len(repository.receiptFailures), len(repository.receiptFinalized),
			len(repository.workspaceFinalized), len(repository.finalized), len(repository.unknown))
	}
	finalization := repository.receiptFailures[0]
	if finalization.Call.Status != domain.CallSucceeded || finalization.Call.ResponseHash != hashBytes(rawOutput) ||
		finalization.Call.ResponseBytes != int64(len(rawOutput)) ||
		finalization.Failure.FailureCode != domain.ResultReceiptFailureContractInvalid ||
		finalization.Failure.ObservedOutputHash != finalization.Call.ResponseHash ||
		finalization.Failure.ObservedOutputBytes == nil || *finalization.Failure.ObservedOutputBytes != int64(len(rawOutput)) ||
		strings.Contains(string(finalization.Call.ResponseSummary), string(rawOutput)) {
		t.Fatalf("receipt failure finalization=%+v", finalization)
	}
	assertWorkspaceAnalysisReceiptFailureTerminal(t, err, repository.receiptFailure)
	if _, found := WorkspaceAnalysisToolTerminalFromError(err); found {
		t.Fatalf("receipt failure was exposed as FAILED/UNKNOWN Call terminal: %v", err)
	}
}

func TestExecutionServiceWorkspaceAnalysisReceiptFailureOutputObservationBoundaries(t *testing.T) {
	t.Run("definition limit plus one is contract invalid", func(t *testing.T) {
		contract := workspaceAnalysisGitExecutionContract()
		rawOutput := json.RawMessage(bytes.Repeat([]byte("x"), int(contract.Definition.MaxOutputBytes+1)))
		executor := &executionTestExecutor{result: ExecutorResult{Output: rawOutput}}
		service, repository, policy := newExecutionTestService(t, contract, executor)

		_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
		if errorCode(err) != errorCodeOutputTooLarge || len(repository.receiptFailures) != 1 ||
			len(repository.workspaceFinalized) != 0 || len(repository.finalized) != 0 {
			t.Fatalf("error=%v failures=%d workspace_terminal=%d generic_failed=%d",
				err, len(repository.receiptFailures), len(repository.workspaceFinalized), len(repository.finalized))
		}
		finalization := repository.receiptFailures[0]
		if finalization.Call.Status != domain.CallSucceeded ||
			finalization.Call.ResponseBytes != contract.Definition.MaxOutputBytes+1 ||
			finalization.Failure.FailureCode != domain.ResultReceiptFailureContractInvalid {
			t.Fatalf("receipt failure finalization=%+v", finalization)
		}
		assertWorkspaceAnalysisReceiptFailureTerminal(t, err, repository.receiptFailure)

		firstAuthorization := repository.workspaceAuthorized[0]
		failure := repository.receiptFailure
		repository.workspaceAuthorize = &WorkspaceAnalysisToolAuthorizationResult{
			Call:           finalization.Call,
			OperationID:    firstAuthorization.OperationID,
			ReservationID:  firstAuthorization.ReservationID,
			Disposition:    WorkspaceAnalysisToolAuthorizationReplayFailure,
			ReceiptFailure: &failure,
		}
		_, replayErr := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
		if errorCode(replayErr) != errorCodeOutputTooLarge || executor.calls != 1 || len(repository.receiptFailures) != 1 ||
			repository.receiptLoads != 0 {
			t.Fatalf("replay error=%v executor=%d failure_uow=%d receipt_loads=%d",
				replayErr, executor.calls, len(repository.receiptFailures), repository.receiptLoads)
		}
		assertWorkspaceAnalysisReceiptFailureTerminal(t, replayErr, failure)
	})

	for _, test := range []struct {
		name   string
		output json.RawMessage
	}{
		{name: "empty observation", output: nil},
		{name: "observation limit plus one", output: json.RawMessage(bytes.Repeat([]byte("x"), int(domain.ResultReceiptFailureMaxObservedOutputBytes+1)))},
	} {
		t.Run(test.name+" stays ordinary failed", func(t *testing.T) {
			contract := workspaceAnalysisGitExecutionContract()
			executor := &executionTestExecutor{result: ExecutorResult{Output: test.output}}
			service, repository, policy := newExecutionTestService(t, contract, executor)

			_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
			if len(repository.receiptFailures) != 0 || len(repository.workspaceFinalized) != 1 ||
				repository.workspaceFinalized[0].Call.Status != domain.CallFailed {
				t.Fatalf("error=%v failures=%d workspace_terminal=%+v",
					err, len(repository.receiptFailures), repository.workspaceFinalized)
			}
			if _, found := WorkspaceAnalysisReceiptFailureTerminalFromError(err); found {
				t.Fatalf("ordinary failed observation exposed receipt failure terminal: %v", err)
			}
		})
	}

	t.Run("binding observation limit plus one stays ordinary failed", func(t *testing.T) {
		contract := workspaceAnalysisGitExecutionContract()
		receiptContract, found := domain.WorkspaceAnalysisResultReceiptContract(contract.Definition.Ref)
		if !found {
			t.Fatal("missing workspace analysis receipt contract")
		}
		executor := &executionTestExecutor{result: ExecutorResult{
			Output: workspaceAnalysisGitExecutionOutput(),
			PrivateBinding: &ExecutorPrivateBinding{
				Schema:   receiptContract.PrivateBindingSchema,
				Document: json.RawMessage(bytes.Repeat([]byte("x"), int(domain.ResultReceiptFailureMaxObservedBindingBytes+1))),
			},
		}}
		service, repository, policy := newExecutionTestService(t, contract, executor)

		_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
		if len(repository.receiptFailures) != 0 || len(repository.workspaceFinalized) != 1 ||
			repository.workspaceFinalized[0].Call.Status != domain.CallFailed {
			t.Fatalf("error=%v failures=%d workspace_terminal=%+v",
				err, len(repository.receiptFailures), repository.workspaceFinalized)
		}
		if _, found := WorkspaceAnalysisReceiptFailureTerminalFromError(err); found {
			t.Fatalf("oversized binding exposed receipt failure terminal: %v", err)
		}
	})
}

func TestExecutionServiceWorkspaceAnalysisBindingInvalidClosesReceiptFailure(t *testing.T) {
	contract := workspaceAnalysisGitExecutionContract()
	privateCanary := json.RawMessage(`private-binding-rejected-canary`)
	executor := &executionTestExecutor{result: ExecutorResult{
		Output: workspaceAnalysisGitExecutionOutput(),
		PrivateBinding: &ExecutorPrivateBinding{
			Schema:   domain.SchemaRef{ID: "tool.read_git_status.wrong_binding", Version: 1},
			Document: privateCanary,
		},
	}}
	service, repository, policy := newExecutionTestService(t, contract, executor)

	_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
	if errorCode(err) != errorCodeOutputInvalid || len(repository.receiptFailures) != 1 ||
		len(repository.receiptFinalized) != 0 || len(repository.workspaceFinalized) != 0 {
		t.Fatalf("error=%v failures=%d receipts=%d workspace_terminal=%d",
			err, len(repository.receiptFailures), len(repository.receiptFinalized), len(repository.workspaceFinalized))
	}
	finalization := repository.receiptFailures[0]
	if finalization.Call.Status != domain.CallSucceeded ||
		finalization.Failure.FailureCode != domain.ResultReceiptFailureBindingInvalid ||
		finalization.Failure.ObservedBindingHash != hashBytes(privateCanary) ||
		finalization.Failure.ObservedBindingBytes == nil ||
		*finalization.Failure.ObservedBindingBytes != int64(len(privateCanary)) {
		t.Fatalf("binding failure finalization=%+v", finalization)
	}
	for _, rendered := range []string{
		fmt.Sprintf("%+v", finalization.Failure),
		string(finalization.Call.ResponseSummary),
	} {
		if strings.Contains(rendered, string(privateCanary)) {
			t.Fatalf("receipt failure leaked rejected private binding: %s", rendered)
		}
	}
	assertWorkspaceAnalysisReceiptFailureTerminal(t, err, repository.receiptFailure)
}

func TestExecutionServiceWorkspaceAnalysisReceiptFailureReplaySkipsReceiptAndExecutor(t *testing.T) {
	contract := workspaceAnalysisGitExecutionContract()
	executor := &executionTestExecutor{result: ExecutorResult{Output: json.RawMessage(`contract-invalid-replay-canary`)}}
	service, repository, policy := newExecutionTestService(t, contract, executor)
	command := workspaceAnalysisGitExecutionCommand(policy)

	_, firstErr := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
	if _, found := WorkspaceAnalysisReceiptFailureTerminalFromError(firstErr); !found {
		t.Fatalf("first error missing receipt failure terminal: %v", firstErr)
	}
	firstAuthorization := repository.workspaceAuthorized[0]
	failure := repository.receiptFailure
	repository.workspaceAuthorize = &WorkspaceAnalysisToolAuthorizationResult{
		Call:           repository.receiptFailures[0].Call,
		OperationID:    firstAuthorization.OperationID,
		ReservationID:  firstAuthorization.ReservationID,
		Disposition:    WorkspaceAnalysisToolAuthorizationReplayFailure,
		ReceiptFailure: &failure,
	}

	_, replayErr := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
	if errorCode(replayErr) != errorCodeOutputInvalid || executor.calls != 1 || len(repository.receiptFailures) != 1 ||
		repository.receiptLoads != 0 || len(repository.workspaceFinalized) != 0 {
		t.Fatalf("replay error=%v executor=%d failure_uow=%d receipt_loads=%d workspace_terminal=%d",
			replayErr, executor.calls, len(repository.receiptFailures), repository.receiptLoads, len(repository.workspaceFinalized))
	}
	assertWorkspaceAnalysisReceiptFailureTerminal(t, replayErr, failure)
}

func TestExecutionServiceWorkspaceAnalysisReceiptFailureReplayRejectsOperationMismatch(t *testing.T) {
	contract := workspaceAnalysisGitExecutionContract()
	executor := &executionTestExecutor{result: ExecutorResult{Output: json.RawMessage(`contract-invalid-replay-operation-canary`)}}
	service, repository, policy := newExecutionTestService(t, contract, executor)
	command := workspaceAnalysisGitExecutionCommand(policy)

	_, firstErr := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
	if _, found := WorkspaceAnalysisReceiptFailureTerminalFromError(firstErr); !found {
		t.Fatalf("first error missing receipt failure terminal: %v", firstErr)
	}
	firstAuthorization := repository.workspaceAuthorized[0]
	failure := repository.receiptFailure
	failure.OperationID = foundation.ID("00000000-0000-4000-8000-000000000398")
	repository.workspaceAuthorize = &WorkspaceAnalysisToolAuthorizationResult{
		Call:           repository.receiptFailures[0].Call,
		OperationID:    firstAuthorization.OperationID,
		ReservationID:  firstAuthorization.ReservationID,
		Disposition:    WorkspaceAnalysisToolAuthorizationReplayFailure,
		ReceiptFailure: &failure,
	}

	_, replayErr := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
	if errorCode(replayErr) != errorCodeWorkspaceAnalysisAuthorizationInvalid || executor.calls != 1 ||
		len(repository.receiptFailures) != 1 || repository.receiptLoads != 0 {
		t.Fatalf("replay error=%v executor=%d failure_uow=%d receipt_loads=%d",
			replayErr, executor.calls, len(repository.receiptFailures), repository.receiptLoads)
	}
	if _, found := WorkspaceAnalysisReceiptFailureTerminalFromError(replayErr); found {
		t.Fatalf("operation-mismatched replay exposed receipt failure terminal: %v", replayErr)
	}
}

func TestExecutionServiceWorkspaceAnalysisReceiptFailureRejectsOperationDrift(t *testing.T) {
	contract := workspaceAnalysisGitExecutionContract()
	executor := &executionTestExecutor{result: ExecutorResult{Output: json.RawMessage(`contract-invalid-operation-canary`)}}
	service, repository, policy := newExecutionTestService(t, contract, executor)
	wrongOperationID := foundation.ID("00000000-0000-4000-8000-000000000399")
	repository.receiptFailureOperationID = &wrongOperationID

	_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
	if errorCode(err) != errorCodeFinalizationUnknown || len(repository.receiptFailures) != 1 {
		t.Fatalf("error=%v failures=%d", err, len(repository.receiptFailures))
	}
	if _, found := WorkspaceAnalysisReceiptFailureTerminalFromError(err); found {
		t.Fatalf("operation drift forged receipt failure terminal: %v", err)
	}
	if _, found := WorkspaceAnalysisToolTerminalFromError(err); found {
		t.Fatalf("operation drift forged tool terminal: %v", err)
	}
}

func TestExecutionServiceWorkspaceAnalysisReceiptFailureRequiresIDAndRepositoryBeforeAuthorization(t *testing.T) {
	t.Run("failure id unavailable", func(t *testing.T) {
		contract := workspaceAnalysisGitExecutionContract()
		executor := &executionTestExecutor{result: ExecutorResult{Output: json.RawMessage(`contract-invalid-id-canary`)}}
		service, repository, policy := newExecutionTestService(t, contract, executor)
		service.ids = &executionTestFailingIDs{failAt: 4}

		_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
		if errorCode(err) != errorCodeWorkspaceAnalysisAuthorizationUnavailable || executor.calls != 0 ||
			len(repository.workspaceAuthorized) != 0 || len(repository.receiptFailures) != 0 {
			t.Fatalf("error=%v executor=%d authorized=%d failures=%d",
				err, executor.calls, len(repository.workspaceAuthorized), len(repository.receiptFailures))
		}
	})

	t.Run("failure repository unavailable", func(t *testing.T) {
		contract := workspaceAnalysisGitExecutionContract()
		executor := &executionTestExecutor{result: ExecutorResult{Output: json.RawMessage(`contract-invalid-repository-canary`)}}
		service, repository, policy := newExecutionTestService(t, contract, executor)
		service.calls = &executionTestRepositoryWithoutReceiptFailure{delegate: repository}

		_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
		if errorCode(err) != errorCodeResultPersistenceUnavailable || executor.calls != 0 ||
			len(repository.workspaceAuthorized) != 0 || len(repository.receiptFailures) != 0 {
			t.Fatalf("error=%v executor=%d authorized=%d failures=%d",
				err, executor.calls, len(repository.workspaceAuthorized), len(repository.receiptFailures))
		}
	})
}

func TestExecutionServiceLegacyInvalidOutputStillFinalizesFailedCall(t *testing.T) {
	contract := testContract("SearchKnowledge", 1)
	executor := &executionTestExecutor{result: ExecutorResult{Output: json.RawMessage(`legacy-invalid-output-canary`)}}
	service, repository, _ := newExecutionTestService(t, contract, executor)

	_, err := service.Execute(context.Background(), executionTestCommand(contract.Definition.Ref.Name, json.RawMessage(`{}`)))
	if errorCode(err) != errorCodeOutputInvalid || len(repository.finalized) != 1 ||
		repository.finalized[0].Status != domain.CallFailed || len(repository.receiptFailures) != 0 {
		t.Fatalf("error=%v finalized=%+v receipt_failures=%d", err, repository.finalized, len(repository.receiptFailures))
	}
}

func TestWorkspaceAnalysisReceiptFailureTerminalIsSafeAndPreservesFoundationError(t *testing.T) {
	contract := workspaceAnalysisGitExecutionContract()
	rawOutput := json.RawMessage(`receipt-terminal-raw-canary`)
	service, repository, policy := newExecutionTestService(t, contract, &executionTestExecutor{result: ExecutorResult{Output: rawOutput}})

	_, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), workspaceAnalysisGitExecutionCommand(policy))
	terminal, found := WorkspaceAnalysisReceiptFailureTerminalFromError(err)
	if !found {
		t.Fatalf("missing receipt failure terminal: %v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNonRetryableFailure || classified.Code != errorCodeOutputInvalid {
		t.Fatalf("classified error=%+v err=%v", classified, err)
	}
	encoded, marshalErr := json.Marshal(terminal)
	if marshalErr != nil || string(encoded) != `{}` {
		t.Fatalf("terminal JSON=%s err=%v", encoded, marshalErr)
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("receipt failure terminal", "terminal", terminal)
	formatted := fmt.Sprint(terminal) + fmt.Sprintf("%+v", terminal) + fmt.Sprintf("%#v", terminal) + fmt.Sprintf("%#v", err)
	canaries := []string{
		string(terminal.OperationID), string(terminal.FailureID), terminal.ExpectedHash,
		string(rawOutput), repository.receiptFailure.ObservedBindingHash,
	}
	if terminal.ActualHash != nil {
		canaries = append(canaries, *terminal.ActualHash)
	}
	for _, canary := range canaries {
		if canary != "" && (strings.Contains(formatted, canary) || strings.Contains(logs.String(), canary)) {
			t.Fatalf("receipt failure terminal leaked %q through formatting/logging", canary)
		}
	}
}

func assertWorkspaceAnalysisReceiptFailureTerminal(
	t *testing.T,
	err error,
	failure domain.ResultReceiptFailure,
) {
	t.Helper()
	terminal, found := WorkspaceAnalysisReceiptFailureTerminalFromError(err)
	if !found || terminal.OperationID != failure.OperationID || terminal.FailureID != failure.ID ||
		terminal.Code != failure.FailureCode || terminal.ExpectedHash != failure.ExpectedOutputHash ||
		terminal.ActualHash == nil || *terminal.ActualHash != failure.ObservedOutputHash {
		t.Fatalf("terminal=%+v found=%t failure=%+v error=%v", terminal, found, failure, err)
	}
}

type executionTestFailingIDs struct {
	mu     sync.Mutex
	next   int
	failAt int
}

func (ids *executionTestFailingIDs) New() (foundation.ID, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	if ids.next == ids.failAt {
		return "", errors.New("id generator unavailable")
	}
	return foundation.ID("00000000-0000-4000-8000-" + leftPad12(ids.next+500)), nil
}

// executionTestRepositoryWithoutReceiptFailure 保留现有 Tool/receipt/Operation 端口，刻意不实现 failure UoW。
type executionTestRepositoryWithoutReceiptFailure struct {
	delegate *executionTestRepository
}

func (repository *executionTestRepositoryWithoutReceiptFailure) RecordRefused(ctx context.Context, command RecordRefusedCommand) (ToolCallMutationResult, error) {
	return repository.delegate.RecordRefused(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) StartCall(ctx context.Context, command StartCallCommand) (StartCallResult, error) {
	return repository.delegate.StartCall(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) FinalizeCall(ctx context.Context, command FinalizeCallCommand) (ToolCallMutationResult, error) {
	return repository.delegate.FinalizeCall(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) MarkUnknown(ctx context.Context, command MarkUnknownCommand) (ToolCallMutationResult, error) {
	return repository.delegate.MarkUnknown(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) RecoverStaleStarted(ctx context.Context, limit int) ([]domain.ToolCall, error) {
	return repository.delegate.RecoverStaleStarted(ctx, limit)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) ListTimeline(ctx context.Context, query ToolCallTimelineQuery) ([]domain.ToolCall, error) {
	return repository.delegate.ListTimeline(ctx, query)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) FinalizeCallWithReceipt(ctx context.Context, command FinalizeCallWithReceiptCommand) (ResultReceiptMutationResult, error) {
	return repository.delegate.FinalizeCallWithReceipt(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) LoadResultReceipt(ctx context.Context, command LoadResultReceiptCommand) (domain.ResultReceipt, error) {
	return repository.delegate.LoadResultReceipt(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) AuthorizeWorkspaceAnalysisToolCall(ctx context.Context, command AuthorizeWorkspaceAnalysisToolCallCommand) (WorkspaceAnalysisToolAuthorizationResult, error) {
	return repository.delegate.AuthorizeWorkspaceAnalysisToolCall(ctx, command)
}

func (repository *executionTestRepositoryWithoutReceiptFailure) FinalizeWorkspaceAnalysisToolCall(ctx context.Context, command FinalizeWorkspaceAnalysisToolCallCommand) (ToolCallMutationResult, error) {
	return repository.delegate.FinalizeWorkspaceAnalysisToolCall(ctx, command)
}
