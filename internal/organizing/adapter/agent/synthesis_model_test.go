package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestSynthesisModelsUseSeparateRecordedEinoAttemptsAndPrivateLabels(t *testing.T) {
	harness := newSynthesisHarness(t, " \n"+synthesisFactOutput+"\n", synthesisFactReview)
	input, generation, validation := synthesisFixtureInput(t)
	result, err := harness.model.GenerateSynthesisForExecution(context.Background(), generation, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result)
	if err != nil {
		t.Fatal(err)
	}
	if harness.provider.CallCount() != 2 || harness.scheduler.calls.Load() != 2 || !receipt.Accepted || receipt.CheckCount != 1 ||
		receipt.ModelRunID == result.ModelRunID || receipt.GenerationModelRunID != result.ModelRunID || receipt.RequestHash != input.RequestHash || receipt.GenerationOutputHash != result.OutputHash {
		t.Fatalf("calls=%d scheduler=%d receipt=%+v", harness.provider.CallCount(), harness.scheduler.calls.Load(), receipt)
	}
	if result.Notes[0].Delta.Operations[0].Item.ID == "" || result.Notes[0].Delta.Operations[0].Item.Fact.Sources[0] != input.Sources[0].Reference {
		t.Fatal("generated statement was not bound to a server identity and exact original source")
	}
	for _, id := range []foundation.ID{result.ModelRunID, receipt.ModelRunID} {
		record, err := harness.store.GetModelRun(context.Background(), generation.WorkspaceID, id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.Retrieval.IsBound() || len(record.Calls) != 1 || record.Calls[0].CallNo != 1 || record.Calls[0].Status != agentdomain.ModelCallSucceeded || record.Calls[0].Usage.TotalTokens != 15 {
			t.Fatalf("audit=%+v", record)
		}
	}
	for _, request := range harness.provider.Calls() {
		var visible strings.Builder
		for _, message := range request.Messages {
			visible.WriteString(message.Content)
		}
		for _, secret := range []string{string(input.ProcessingID), string(input.WorkflowRunID), string(input.NodeAttemptID), string(input.SourceEvent.ID), string(input.Sources[0].Reference.Source.SourceVersionID), string(input.Sources[0].Reference.SourceSpanID), input.RequestHash, input.Sources[0].Reference.ExcerptHash} {
			if strings.Contains(visible.String(), secret) {
				t.Fatal("Provider input exposed an authoritative identity or hash")
			}
		}
		if !strings.Contains(visible.String(), "S001") || !strings.Contains(visible.String(), "RAW-EXCERPT-ONLY") {
			t.Fatal("Provider did not receive the exact bounded original excerpt")
		}
	}
	persisted, err := json.Marshal(struct {
		Runs  any
		Calls any
		Steps any
	}{harness.store.runs, harness.store.calls, harness.store.steps})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "RAW-EXCERPT-ONLY") {
		t.Fatal("source excerpt leaked into durable model metadata or result")
	}
}

func TestSynthesisSemanticRejectionAndTransportReplayDoNotCallProviderAgain(t *testing.T) {
	rejected := strings.ReplaceAll(synthesisFactReview, "SUPPORTED", "UNSUPPORTED")
	harness := newSynthesisHarness(t, synthesisFactOutput, rejected)
	input, generation, validation := synthesisFixtureInput(t)
	result, err := harness.model.GenerateSynthesisForExecution(context.Background(), generation, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result)
	if synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisSemanticRejected || receipt.Accepted {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	validation.NodeAttemptID = synthesisTestID(40)
	replayed, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result)
	if synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisSemanticRejected || replayed != receipt || harness.provider.CallCount() != 2 || harness.scheduler.calls.Load() != 2 {
		t.Fatalf("replay=%+v calls=%d err=%v", replayed, harness.provider.CallCount(), err)
	}
	stored, err := harness.store.GetModelRun(context.Background(), validation.WorkspaceID, receipt.ModelRunID)
	if err != nil || stored.Run.Status != agentdomain.ModelRunSucceeded || stored.Run.NodeAttemptID == validation.NodeAttemptID {
		t.Fatalf("review result lost its original completed attempt: %#v %v", stored, err)
	}
}

func TestSynthesisGenerationRecoversExactCommitAndKeepsItemIdentities(t *testing.T) {
	harness := newSynthesisHarness(t, synthesisFactOutput)
	harness.store.loseCompleteResponse = true
	input, execution, _ := synthesisFixtureInput(t)
	first, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	allocated := harness.ids.count.Load()
	input.NodeAttemptID, execution.NodeAttemptID = synthesisTestID(41), synthesisTestID(41)
	changedSettings := int64(5)
	execution.ModelSettingsRevision = &changedSettings
	again, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, first) || harness.ids.count.Load() != allocated || harness.provider.CallCount() != 1 || harness.scheduler.calls.Load() != 1 {
		t.Fatal("recovery allocated new item identities, rebound model settings, or invoked the Provider")
	}
}

func TestSynthesisUnknownResultsFailClosedWithoutAnotherPaidCall(t *testing.T) {
	for _, scenario := range []string{"business commit unknown", "model call persistence unknown"} {
		t.Run(scenario, func(t *testing.T) {
			harness := newSynthesisHarness(t, synthesisFactOutput)
			if scenario == "business commit unknown" {
				harness.store.dropComplete = true
			} else {
				harness.store.failCallCompletion = true
			}
			input, execution, _ := synthesisFixtureInput(t)
			if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); err == nil {
				t.Fatal("unknown result was reported as success")
			}
			if harness.provider.CallCount() != 1 {
				t.Fatalf("calls=%d", harness.provider.CallCount())
			}
			harness.store.dropComplete = false
			if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelReplayUnsafe {
				t.Fatalf("replay err=%v", err)
			}
			if harness.provider.CallCount() != 1 {
				t.Fatal("unknown result retried the Provider")
			}
		})
	}
}

func TestSynthesisRepairAndExhaustionUseTheSharedThreePhaseBudget(t *testing.T) {
	invalid := strings.Replace(synthesisFactOutput, `"applicability":"",`, "", 1)
	for _, test := range []struct {
		name    string
		outputs []string
		success bool
	}{
		{"repair", []string{invalid, synthesisFactOutput}, true},
		{"reduced", []string{invalid, invalid, synthesisFactOutput}, true},
		{"exhausted", []string{invalid, invalid, invalid}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newSynthesisHarness(t, test.outputs...)
			input, execution, _ := synthesisFixtureInput(t)
			_, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
			if test.success && err != nil || !test.success && synthesisTestCode(err) != agentdomain.ErrorCodeValidationExhausted {
				t.Fatalf("err=%v", err)
			}
			phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
			if harness.provider.CallCount() != len(test.outputs) || harness.scheduler.calls.Load() != 1 {
				t.Fatal("bounded phase schedule did not execute exactly once")
			}
			for index, request := range harness.provider.Calls() {
				if request.Phase != phases[index] {
					t.Fatalf("phase=%s", request.Phase)
				}
			}
			for _, calls := range harness.store.calls {
				for index, call := range calls {
					if call.CallNo != index+1 || call.Phase != phases[index] {
						t.Fatal("audit lost phase order")
					}
				}
			}
		})
	}
}

func TestSynthesisRejectsForgedGeneratedResultsAndSharedSemanticAttempt(t *testing.T) {
	harness := newSynthesisHarness(t, synthesisFactOutput)
	input, execution, validation := synthesisFixtureInput(t)
	result, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), execution, input, result); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelContextInvalid {
		t.Fatalf("shared attempt err=%v", err)
	}
	result.Notes[0].Delta.Operations[0].Item.Fact.Text = "Unsupported edited statement."
	if _, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelReplayUnsafe {
		t.Fatalf("forged output err=%v", err)
	}
	if harness.provider.CallCount() != 1 {
		t.Fatal("a forged or incorrectly bound review reached the Provider")
	}
}

func TestSynthesisUnknownSourceAndCrossNoteTargetsCannotBecomeEvidence(t *testing.T) {
	for _, output := range []string{
		strings.ReplaceAll(synthesisFactOutput, "S001", "S002"),
		`{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I128","alternative":null,"sources":["S002"]}]}]}`,
		`{"notes":[{"note":"N024","operations":[]}]}`,
	} {
		harness := newSynthesisHarness(t, output)
		input, execution, _ := synthesisFixtureWithNote(t)
		if strings.Contains(output, `"note":""`) {
			input, execution, _ = synthesisFixtureInput(t)
		}
		if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
			t.Fatalf("err=%v", err)
		}
		if harness.provider.CallCount() != 1 {
			t.Fatal("binding failure retried a provider call")
		}
		for _, record := range harness.store.steps {
			if record.Status != organizingapp.SynthesisModelStepFailed || record.Generation != nil {
				t.Fatal("unbound model content was persisted as ready")
			}
		}
	}
}

func TestSynthesisByteTokenAndDeadlineBudgetsAreEnforced(t *testing.T) {
	for _, test := range []struct {
		name     string
		budget   func(*agentapp.RunBudget)
		expected string
		calls    int
	}{
		{"request", func(value *agentapp.RunBudget) { value.MaxRequestBytes = 1 }, "AGENT_REQUEST_BYTE_BUDGET_EXHAUSTED", 0},
		{"response", func(value *agentapp.RunBudget) { value.MaxResponseBytes = 1 }, "AGENT_RESPONSE_BYTE_BUDGET_EXHAUSTED", 1},
		{"tokens", func(value *agentapp.RunBudget) { value.MaxTotalTokens = 1 }, "AGENT_TOKEN_BUDGET_EXHAUSTED", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newSynthesisHarness(t, synthesisFactOutput)
			test.budget(&harness.model.dependencies.Budget)
			input, execution, _ := synthesisFixtureInput(t)
			if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); synthesisTestCode(err) != test.expected {
				t.Fatalf("err=%v", err)
			}
			if harness.provider.CallCount() != test.calls {
				t.Fatalf("calls=%d", harness.provider.CallCount())
			}
		})
	}
	harness := newSynthesisHarnessSteps(t, agentapp.DeterministicChatStep{WaitForCancel: true})
	harness.model.dependencies.Budget.Timeout = 15 * time.Millisecond
	input, execution, _ := synthesisFixtureInput(t)
	if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err=%v", err)
	}
	if harness.provider.CallCount() != 1 {
		t.Fatal("deadline automatically retried the Provider")
	}
}

func TestSynthesisCancellationRetainsCauseAndUnconfiguredCapabilityIsVisible(t *testing.T) {
	harness := newSynthesisHarnessSteps(t, agentapp.DeterministicChatStep{WaitForCancel: true})
	input, execution, _ := synthesisFixtureInput(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cause := errors.New("private cancellation cause")
	finished := make(chan error, 1)
	go func() { _, err := harness.model.GenerateSynthesisForExecution(ctx, execution, input); finished <- err }()
	select {
	case <-harness.provider.Started():
	case <-time.After(time.Second):
		t.Fatal("Provider was not called")
	}
	cancel(cause)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
			t.Fatalf("cancel err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the model")
	}
	if _, err := NewUnavailableSynthesisModel().GenerateSynthesisForExecution(context.Background(), execution, input); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisCapabilityUnavailable {
		t.Fatalf("capability err=%v", err)
	}
	dependencies := harness.model.dependencies
	dependencies.Scheduler = nil
	if _, err := NewSynthesisModel(dependencies); err == nil {
		t.Fatal("missing scheduler was accepted")
	}
}

func TestSynthesisNoChangeStillUsesIndependentReview(t *testing.T) {
	harness := newSynthesisHarness(t, `{"notes":[]}`, `{"checks":[{"index":1,"verdict":"UNSUPPORTED","sources":[]}]}`)
	input, execution, validation := synthesisFixtureInput(t)
	result, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result)
	if synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisSemanticRejected || receipt.Accepted || harness.provider.CallCount() != 2 {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	request := harness.provider.Calls()[1]
	found := false
	for _, message := range request.Messages {
		found = found || strings.Contains(message.Content, `"kind":"NO_CHANGE"`)
	}
	if !found {
		t.Fatal("empty delta skipped independent coverage review")
	}
}

func TestSynthesisAdapterSupportsConcurrentEinoRunsWithoutStateSharing(t *testing.T) {
	const count = 32
	outputs := make([]string, count)
	for index := range outputs {
		outputs[index] = synthesisFactOutput
	}
	harness := newSynthesisHarness(t, outputs...)
	input, execution, _ := synthesisFixtureInput(t)
	var group sync.WaitGroup
	failures := make(chan error, count)
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			current, currentExecution := input, execution
			base := 1000 + index*4
			current.ProcessingID, current.WorkflowRunID, current.NodeRunID, current.NodeAttemptID = synthesisTestID(base), synthesisTestID(base+1), synthesisTestID(base+2), synthesisTestID(base+3)
			currentExecution.RunID, currentExecution.NodeRunID, currentExecution.NodeAttemptID = current.WorkflowRunID, current.NodeRunID, current.NodeAttemptID
			_, err := harness.model.GenerateSynthesisForExecution(context.Background(), currentExecution, current)
			if err != nil {
				failures <- err
			}
		}(index)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if harness.provider.CallCount() != count || harness.scheduler.calls.Load() != count || len(harness.store.runs) != count {
		t.Fatal("concurrent runs lost a binding or reused another attempt")
	}
}

func TestSynthesisFrozenInputDriftIsRejectedBeforeProvider(t *testing.T) {
	harness := newSynthesisHarness(t, synthesisFactOutput)
	input, execution, _ := synthesisFixtureInput(t)
	input.Sources[0].Text += " changed"
	if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); synthesisTestCode(err) != domain.ErrorCodeSynthesisSourceInvalid {
		t.Fatalf("err=%v", err)
	}
	if harness.provider.CallCount() != 0 || len(harness.store.steps) != 0 {
		t.Fatal("changed source reached model execution")
	}
}
