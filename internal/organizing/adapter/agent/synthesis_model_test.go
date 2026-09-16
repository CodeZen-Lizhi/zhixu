package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSynthesisPromptVersionsPreserveV1ReplayAndFenceAnchoredRuns(t *testing.T) {
	t.Run("legacy successful run replays with its original prompt", func(t *testing.T) {
		harness := newSynthesisHarness(t, synthesisFactOutput)
		input, execution, _ := synthesisFixtureInput(t)
		first, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := harness.store.GetModelRun(context.Background(), execution.WorkspaceID, first.ModelRunID)
		if err != nil || stored.Run.Prompt != synthesisPromptForVersion(organizingapp.SynthesisModelGenerate, organizingapp.SynthesisLegacyPromptVersion) {
			t.Fatalf("legacy model run=%+v err=%v", stored.Run, err)
		}
		input.NodeAttemptID, execution.NodeAttemptID = synthesisTestID(88), synthesisTestID(88)
		replayed, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
		if err != nil || !reflect.DeepEqual(replayed, first) || harness.provider.CallCount() != 1 {
			t.Fatalf("replayed=%+v calls=%d err=%v", replayed, harness.provider.CallCount(), err)
		}
	})

	t.Run("anchored generation and review use v2 and reject a v1 replay", func(t *testing.T) {
		harness := newSynthesisHarness(t, `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S002"]}]}]}`, strings.ReplaceAll(synthesisFactReview, "S001", "S002"))
		input, generation, validation := synthesisFixtureWithNote(t)
		input.Notes[0].Anchor = &organizingapp.SynthesisAnchorBinding{
			AnchorID:       synthesisTestID(89),
			ScopeVersion:   1,
			Scope:          domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"Interview"}, Description: "Redis revision"},
			AllowedSources: []domain.SynthesisSourceRef{input.Sources[1].Reference},
		}
		if err := input.Validate(); err != nil {
			t.Fatal(err)
		}
		result, err := harness.model.GenerateSynthesisForExecution(context.Background(), generation, input)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result)
		if err != nil {
			t.Fatal(err)
		}
		for _, modelRunID := range []foundation.ID{result.ModelRunID, receipt.ModelRunID} {
			stored, err := harness.store.GetModelRun(context.Background(), generation.WorkspaceID, modelRunID)
			if err != nil || stored.Run.Prompt.Version != organizingapp.SynthesisAnchoredPromptVersion {
				t.Fatalf("anchored model run=%+v err=%v", stored.Run, err)
			}
		}

		harness.store.mu.Lock()
		run := harness.store.runs[result.ModelRunID]
		run.Prompt.Version = organizingapp.SynthesisLegacyPromptVersion
		harness.store.runs[result.ModelRunID] = run
		harness.store.mu.Unlock()
		input.NodeAttemptID, generation.NodeAttemptID = synthesisTestID(90), synthesisTestID(90)
		if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), generation, input); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelReplayUnsafe || harness.provider.CallCount() != 2 {
			t.Fatalf("err=%v calls=%d", err, harness.provider.CallCount())
		}
	})
}

func TestSynthesisBodyRequestHashBindsPublicationAndKeepsLegacyIdentity(t *testing.T) {
	input, execution, _ := synthesisFixtureWithNote(t)
	payload, err := encodeSynthesisInput(synthesisInput(input))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, execution.NodeRunID, input, nil, payload)
	if err != nil || legacy != mustSynthesisRequestHashV1(t, organizingapp.SynthesisModelGenerate, execution.NodeRunID, input, nil, payload) {
		t.Fatalf("legacy hash=%s err=%v", legacy, err)
	}
	input.Notes[0].PublicationID = synthesisTestID(91)
	payload, err = encodeSynthesisInput(synthesisInput(input))
	if err != nil {
		t.Fatal(err)
	}
	first, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, execution.NodeRunID, input, nil, payload)
	if err != nil || synthesisPromptVersion(input) != organizingapp.SynthesisBodyPromptVersion {
		t.Fatalf("hash=%s version=%s err=%v", first, synthesisPromptVersion(input), err)
	}
	input.Notes[0].PublicationID = synthesisTestID(92)
	second, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, execution.NodeRunID, input, nil, payload)
	if err != nil || first == second {
		t.Fatalf("publication did not bind request hash: first=%s second=%s err=%v", first, second, err)
	}
}

func mustSynthesisRequestHashV1(t *testing.T, stage organizingapp.SynthesisModelStage, nodeID foundation.ID, input organizingapp.SynthesisGenerationInput, generated *organizingapp.SynthesisGenerationResult, payload []byte) string {
	t.Helper()
	value, err := synthesisModelRequestHashV1(stage, nodeID, input, generated, payload)
	if err != nil {
		t.Fatal(err)
	}
	return value
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

func TestSynthesisFrozenSemanticFormatUsesExactMessagesAndReplays(t *testing.T) {
	harness := newSynthesisHarness(t, synthesisFactOutput, synthesisFactReview)
	input, generation, validation := synthesisFixtureInput(t)
	input.SemanticPromptVersion = organizingapp.SynthesisSemanticFormatPromptVersion
	result, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result)
	if err != nil {
		t.Fatal(err)
	}
	calls := harness.provider.Calls()
	if calls[0].PromptRef.Version != organizingapp.SynthesisLegacyPromptVersion || calls[1].PromptRef.Version != organizingapp.SynthesisSemanticFormatPromptVersion || calls[1].SchemaRef != synthesisSchema(organizingapp.SynthesisModelValidate) {
		t.Fatalf("wrong frozen runtime: %+v / %+v", calls[0].PromptRef, calls[1].PromptRef)
	}
	schema, _ := json.Marshal(synthesisSemanticSchema())
	var messages strings.Builder
	for _, message := range calls[1].Messages {
		messages.WriteString(message.Content)
	}
	for _, required := range []string{string(schema), `"index", "verdict", "sources"`, `"source", "verdict"`, `"sources":[]`, `"source_verdicts"`, "If user_goal", "If refresh_updates", "Publication proves neither", "independent semantic reviewer"} {
		if !strings.Contains(messages.String(), required) {
			t.Fatalf("missing semantic contract %q", required)
		}
	}
	replayed, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result)
	if err != nil || replayed != receipt || harness.provider.CallCount() != 2 {
		t.Fatalf("READY replay changed: %v", err)
	}
	input.SemanticPromptVersion = ""
	if _, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result); err == nil || harness.provider.CallCount() != 2 {
		t.Fatal("new result replayed as legacy or called provider")
	}
	input.SemanticPromptVersion = "v7"
	if _, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input); err == nil || harness.provider.CallCount() != 2 {
		t.Fatal("forged semantic version admitted")
	}
}

func TestSynthesisSemanticFormatHashSeparatesOnlyNewValidation(t *testing.T) {
	input, execution, _ := synthesisFixtureInput(t)
	for _, version := range []string{"v1", "v2", "v3", "v4", "v5"} {
		t.Run(version, func(t *testing.T) {
			legacy := input
			switch version {
			case "v2":
				legacy.Notes = []organizingapp.SynthesisGenerationNote{{Anchor: &organizingapp.SynthesisAnchorBinding{}}}
			case "v3":
				legacy.Goal = &organizingapp.SynthesisGoalBinding{}
			case "v4":
				legacy.Notes = []organizingapp.SynthesisGenerationNote{{PublicationID: synthesisTestID(800)}}
			case "v5":
				legacy.BodyRefresh = &organizingapp.SynthesisBodyRefreshBinding{}
			}
			for _, stage := range []organizingapp.SynthesisModelStage{organizingapp.SynthesisModelGenerate, organizingapp.SynthesisModelValidate} {
				old, err := synthesisLegacyModelRequestHash(stage, execution.NodeRunID, legacy, nil, []byte(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				got, err := synthesisModelRequestHash(stage, execution.NodeRunID, legacy, nil, []byte(`{}`))
				if err != nil || got != old {
					t.Fatal("historical hash changed")
				}
				current := legacy
				current.SemanticPromptVersion = organizingapp.SynthesisSemanticFormatPromptVersion
				got, err = synthesisModelRequestHash(stage, execution.NodeRunID, current, nil, []byte(`{}`))
				if err != nil || (got == old) != (stage == organizingapp.SynthesisModelGenerate) {
					t.Fatal("semantic version identity mismatch")
				}
				if synthesisPrompt(stage, current).Version == organizingapp.SynthesisSemanticFormatPromptVersion && stage != organizingapp.SynthesisModelValidate {
					t.Fatal("generation upgraded")
				}
			}
		})
	}
}

func TestSynthesisLegacySemanticFailureCannotGainPaidRetryByChangingVersion(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "unknown"}[unknown], func(t *testing.T) {
			outputs := []string{synthesisFactOutput, `{}`, `{}`, `{}`}
			if unknown {
				outputs = []string{synthesisFactOutput, synthesisFactReview}
			}
			harness := newSynthesisHarness(t, outputs...)
			input, generate, validate := synthesisFixtureInput(t)
			result, err := harness.model.GenerateSynthesisForExecution(t.Context(), generate, input)
			if err != nil {
				t.Fatal(err)
			}
			harness.store.dropComplete = unknown
			if _, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validate, input, result); err == nil {
				t.Fatal("failure not exposed")
			}
			calls := harness.provider.CallCount()
			harness.store.dropComplete = false
			for _, version := range []string{"", organizingapp.SynthesisSemanticFormatPromptVersion, organizingapp.SynthesisSourceIdentitySemanticPromptVersion} {
				input.SemanticPromptVersion = version
				if version == organizingapp.SynthesisSourceIdentitySemanticPromptVersion {
					input.GenerationPromptVersion = organizingapp.SynthesisSourceIdentityLegacyPromptVersion
				}
				if _, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validate, input, result); err == nil || harness.provider.CallCount() != calls {
					t.Fatal("failed or unknown stage called provider again")
				}
			}
		})
	}
}

func TestSynthesisSourceIdentityVersionsBindMessagesHashesAndSchemas(t *testing.T) {
	harness := newSynthesisHarness(t, synthesisFactOutput, synthesisFactReview)
	input, generation, validation := synthesisFixtureInput(t)
	input.GenerationPromptVersion, input.SemanticPromptVersion = organizingapp.SynthesisSourceIdentityLegacyPromptVersion, organizingapp.SynthesisSourceIdentitySemanticPromptVersion
	result, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result)
	if err != nil {
		t.Fatal(err)
	}
	calls := harness.provider.Calls()
	if calls[0].PromptRef.Version != "v7" || calls[1].PromptRef.Version != "v7" {
		t.Fatal("new versions were not used")
	}
	for _, call := range calls {
		if !strings.Contains(call.Messages[0].Content, "Every distinct S-label denotes a distinct source reference") {
			t.Fatal("trusted identity rule missing")
		}
	}
	if !strings.Contains(calls[1].Messages[0].Content, "NO_CHANGE must be UNSUPPORTED") || !strings.Contains(calls[1].Messages[0].Content, "Body refresh requests are a separate case") {
		t.Fatal("review omission/refresh contract missing")
	}
	if replay, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result); err != nil || replay != receipt || harness.provider.CallCount() != 2 {
		t.Fatal("new READY replay changed")
	}
	for _, pair := range [][2]string{{"v7", "v6"}, {"v8", "v7"}, {"", "v7"}, {"v7", ""}} {
		input.GenerationPromptVersion, input.SemanticPromptVersion = pair[0], pair[1]
		if _, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input); err == nil || harness.provider.CallCount() != 2 {
			t.Fatal("forged version pair called provider")
		}
	}
	// 即使实际生成提示词使用更新版本，Schema 选择和历史哈希封装仍以原始结构为准。
	input, _, _ = synthesisFixtureInput(t)
	for _, original := range []string{"v1", "v2", "v3", "v4", "v5"} {
		shaped := input
		switch original {
		case "v2":
			shaped.Notes = []organizingapp.SynthesisGenerationNote{{Anchor: &organizingapp.SynthesisAnchorBinding{}}}
		case "v3":
			shaped.Goal = &organizingapp.SynthesisGoalBinding{}
		case "v4":
			shaped.Notes = []organizingapp.SynthesisGenerationNote{{PublicationID: synthesisTestID(800)}}
		case "v5":
			shaped.BodyRefresh = &organizingapp.SynthesisBodyRefreshBinding{}
		}
		oldSchema := synthesisSchemaForInput(organizingapp.SynthesisModelGenerate, shaped)
		shaped.SemanticPromptVersion = organizingapp.SynthesisSemanticFormatPromptVersion
		for _, stage := range []organizingapp.SynthesisModelStage{organizingapp.SynthesisModelGenerate, organizingapp.SynthesisModelValidate} {
			oldHash, err := synthesisModelRequestHash(stage, generation.NodeRunID, shaped, nil, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			current := shaped
			current.GenerationPromptVersion = organizingapp.SynthesisSourceIdentityGenerationVersion(original)
			current.SemanticPromptVersion = organizingapp.SynthesisSourceIdentitySemanticPromptVersion
			newHash, err := synthesisModelRequestHash(stage, generation.NodeRunID, current, nil, []byte(`{}`))
			if err != nil || (oldHash == newHash) != (original == "v5" && stage == organizingapp.SynthesisModelGenerate) {
				t.Fatalf("wrong stage hash binding: %s %s %v", original, stage, err)
			}
			if synthesisSchemaForInput(organizingapp.SynthesisModelGenerate, current) != oldSchema {
				t.Fatalf("schema changed for %s", original)
			}
		}
	}
}

func TestSynthesisDistinctSourceCapacityBeforePaidCalls(t *testing.T) {
	for _, tc := range []struct {
		name             string
		notes, items     int
		conflict, reject bool
	}{
		{"nine targets", 9, 1, false, true},
		{"operation capacity", 1, 65, false, true},
		{"semantic capacity", 8, 33, true, true},
		{"eight targets and replay", 8, 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, execution, validation := synthesisFixtureWithNote(t)
			input.GenerationPromptVersion = organizingapp.SynthesisSourceIdentityLegacyPromptVersion
			input.SemanticPromptVersion = organizingapp.SynthesisSourceIdentitySemanticPromptVersion
			input.Sources[1].Text = input.Sources[0].Text
			input.Sources[1].Reference.ExcerptHash = input.Sources[0].Reference.ExcerptHash
			template := input.Notes[0]
			input.Notes = nil
			outputs := []string{}
			for n := 0; n < tc.notes; n++ {
				candidate := template
				candidate.Note.ID = synthesisTestID(1000 + n*10)
				candidate.Note.DocumentID = synthesisTestID(1001 + n*10)
				candidate.Note.CurrentRevisionID = synthesisTestID(1002 + n*10)
				candidate.Note.TopicKey = fmt.Sprintf("topic %d", n)
				candidate.Revision.ID = candidate.Note.CurrentRevisionID
				candidate.Revision.NoteID = candidate.Note.ID
				candidate.Revision.DocumentID = candidate.Note.DocumentID
				candidate.Revision.ArticleRevisionID = synthesisTestID(1003 + n*10)
				candidate.Revision.Items = nil
				candidate.Revision.Delta.Operations = template.Revision.Delta.Operations[:1]
				for i := 0; i < tc.items; i++ {
					item := template.Revision.Items[0]
					if tc.conflict {
						item = template.Revision.Items[1]
					}
					if tc.conflict {
						conflict := *item.Conflict
						conflict.Subject = fmt.Sprintf("subject %d", i)
						item.Conflict = &conflict
					} else {
						fact := *item.Fact
						fact.Text = fmt.Sprintf("assertion %d", i)
						item.Fact = &fact
					}
					item.ID = synthesisTestID(2000 + n*200 + i)
					candidate.Revision.Items = append(candidate.Revision.Items, item)
				}
				body, err := domain.RenderSynthesisMarkdown(candidate.Note.WorkspaceID, candidate.Note.ID, candidate.Note.Title, candidate.Revision.Items)
				if err != nil {
					t.Fatal(err)
				}
				candidate.Revision.ContentHash = synthesisHash([]byte(body))
				candidate.Revision.Hash, err = domain.ComputeSynthesisRevisionHash(candidate.Revision)
				if err != nil {
					t.Fatal(err)
				}
				input.Notes = append(input.Notes, candidate)
				outputs = append(outputs, fmt.Sprintf(`{"note":"N%03d","operations":[{"op":"ADD_SUPPORT","target":"I%03d","alternative":null,"sources":["S002"]}]}`, n+1, 1))
			}
			if err := input.Validate(); err != nil {
				t.Fatal(err)
			}
			reviews := []string{}
			for n := 0; n < tc.notes; n++ {
				reviews = append(reviews, fmt.Sprintf(`{"index":%d,"verdict":"SUPPORTED","sources":[{"source":"S002","verdict":"SUPPORTED"}]}`, n+1))
			}
			harness := newSynthesisHarness(t, `{"notes":[`+strings.Join(outputs, ",")+`]}`, `{"checks":[`+strings.Join(reviews, ",")+`]}`)
			result, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
			if tc.reject {
				if synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelInputTooLarge || harness.provider.CallCount() != 0 || len(harness.store.runs) != 0 || len(harness.store.calls) != 0 || len(harness.store.steps) != 0 || harness.scheduler.calls.Load() != 0 {
					t.Fatalf("err=%v calls=%d runs=%d steps=%d", err, harness.provider.CallCount(), len(harness.store.runs), len(harness.store.steps))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result)
			if err != nil || !receipt.Accepted || receipt.CheckCount != 8 {
				t.Fatalf("review=%+v err=%v", receipt, err)
			}
			if len(result.Notes) != 8 {
				t.Fatalf("notes=%d", len(result.Notes))
			}
			for _, note := range result.Notes {
				if !reflect.DeepEqual(note.Delta.Operations[0].Sources, []domain.SynthesisSourceRef{input.Sources[1].Reference}) {
					t.Fatal("missing source")
				}
			}
			input.NodeAttemptID, execution.NodeAttemptID = synthesisTestID(9999), synthesisTestID(9999)
			replay, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
			if err != nil || !reflect.DeepEqual(replay, result) || harness.provider.CallCount() != 2 {
				t.Fatalf("replay err=%v calls=%d", err, harness.provider.CallCount())
			}
		})
	}
}

func TestSynthesisFusionTargetIsVisibleWithoutChangingHistoricalPayload(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "anchor", true: "published target"}[published], func(t *testing.T) {
			input, generation, validation := synthesisFixtureWithNote(t)
			target := &input.Notes[0]
			target.Anchor = &organizingapp.SynthesisAnchorBinding{AnchorID: synthesisTestID(601), ScopeVersion: 2, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"interview"}, Description: "Redis knowledge only"}, AllowedSources: []domain.SynthesisSourceRef{input.Sources[1].Reference}}
			if published {
				target.PublicationID = synthesisTestID(605)
			}
			// 历史载荷不投影 Fusion，尽管请求哈希和输出校验已经绑定了该精确触发条件。
			input.GenerationPromptVersion = organizingapp.SynthesisSourceIdentityGenerationVersion(organizingapp.SynthesisOriginalPromptVersion(input))
			input.SemanticPromptVersion = organizingapp.SynthesisSourceIdentitySemanticPromptVersion
			before, err := encodeSynthesisInput(synthesisInput(input))
			if err != nil {
				t.Fatal(err)
			}
			input.SourceEvent.Fusion = &domain.SynthesisFusionTrigger{RequestID: synthesisTestID(602), AnchorID: target.Anchor.AnchorID, NoteID: target.Note.ID, ProposalID: synthesisTestID(603), ScopeVersion: target.Anchor.ScopeVersion, AllowedSources: append([]domain.SynthesisSourceRef{}, target.Anchor.AllowedSources...)}
			old, err := encodeSynthesisInput(synthesisInput(input))
			if err != nil || string(old) != string(before) {
				t.Fatal("historical provider input changed")
			}
			historical := newSynthesisHarness(t, `{"notes":[]}`, `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[]}]}`)
			historicalResult, err := historical.model.GenerateSynthesisForExecution(t.Context(), generation, input)
			if err != nil {
				t.Fatal(err)
			}
			historicalReceipt, err := historical.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, historicalResult)
			if err != nil {
				t.Fatal(err)
			}
			historicalPlan, err := synthesisSemanticPlan(input, historicalResult)
			if err != nil || historicalPlan.FusionTarget != "" || historicalPlan.Scope != nil {
				t.Fatal("historical semantic payload changed")
			}
			replayedHistorical, err := historical.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, historicalResult)
			if err != nil || replayedHistorical != historicalReceipt || historical.provider.CallCount() != 2 {
				t.Fatal("historical fusion READY cannot replay")
			}
			oldHash, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, generation.NodeRunID, input, nil, old)
			if err != nil {
				t.Fatal(err)
			}
			input.GenerationPromptVersion, input.SemanticPromptVersion = organizingapp.LatestSynthesisPromptVersions(organizingapp.SynthesisOriginalPromptVersion(input), true)
			expectedVersion := organizingapp.SynthesisGenerationFormatFusionAnchoredPromptVersion
			expectedSchema := organizingapp.SynthesisRuntimeVersion
			if published {
				expectedVersion, expectedSchema = organizingapp.SynthesisGenerationFormatFusionBodyPromptVersion, organizingapp.SynthesisBodySchemaVersion
			}
			harness := newSynthesisHarness(t, `{"notes":[]}`, `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[]}]}`)
			generated, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, generated)
			if err != nil || !receipt.Accepted {
				t.Fatalf("no change review: %v", err)
			}
			calls := harness.provider.Calls()
			if calls[0].PromptRef.Version != expectedVersion || calls[0].SchemaRef.Version != expectedSchema || calls[1].PromptRef.Version != organizingapp.SynthesisFusionExistingSemanticPromptVersion {
				t.Fatal("wrong fusion prompt/schema")
			}
			for _, call := range calls {
				all := ""
				for _, message := range call.Messages {
					all += message.Content
				}
				if !strings.Contains(all, `"fusion_target":"N001"`) || !strings.Contains(all, "Redis knowledge only") {
					t.Fatal("target/scope absent from actual provider request")
				}
				for _, id := range []foundation.ID{input.SourceEvent.Fusion.RequestID, input.SourceEvent.Fusion.AnchorID, input.SourceEvent.Fusion.NoteID, input.SourceEvent.Fusion.ProposalID} {
					if strings.Contains(all, string(id)) {
						t.Fatal("raw fusion identity exposed")
					}
				}
			}
			if !strings.Contains(calls[0].Messages[0].Content, "Never create a new note") || !strings.Contains(calls[1].Messages[0].Content, "for THIS target") {
				t.Fatal("trusted target-only instructions missing")
			}
			plan, err := synthesisSemanticPlan(input, generated)
			if err != nil || plan.FusionTarget != "N001" || plan.Scope == nil || plan.Scope.Description != target.Anchor.Scope.Description || plan.Checks[0].Kind != "NO_CHANGE" {
				t.Fatal("target-specific NO_CHANGE lost scope")
			}
			payload, _ := encodeSynthesisInput(synthesisInput(input))
			newHash, err := synthesisModelRequestHash(organizingapp.SynthesisModelGenerate, generation.NodeRunID, input, nil, payload)
			if err != nil || newHash == oldHash {
				t.Fatal("new projection reused historical request identity")
			}
			replay, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
			if err != nil || !reflect.DeepEqual(replay, generated) || harness.provider.CallCount() != 2 {
				t.Fatal("READY replay called provider")
			}
		})
	}
}

func TestSynthesisFusionTargetRejectsInvalidBindingBeforeProvider(t *testing.T) {
	for _, mode := range []string{"missing trigger", "wrong note", "wrong anchor", "wrong scope", "wrong sources", "old generator", "ordinary generator", "new output note"} {
		t.Run(mode, func(t *testing.T) {
			input, generation, _ := synthesisFixtureWithNote(t)
			target := &input.Notes[0]
			target.Anchor = &organizingapp.SynthesisAnchorBinding{AnchorID: synthesisTestID(601), ScopeVersion: 2, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"interview"}, Description: "Redis only"}, AllowedSources: []domain.SynthesisSourceRef{input.Sources[1].Reference}}
			input.SourceEvent.Fusion = &domain.SynthesisFusionTrigger{RequestID: synthesisTestID(602), AnchorID: target.Anchor.AnchorID, NoteID: target.Note.ID, ProposalID: synthesisTestID(603), ScopeVersion: 2, AllowedSources: append([]domain.SynthesisSourceRef{}, target.Anchor.AllowedSources...)}
			input.GenerationPromptVersion, input.SemanticPromptVersion = organizingapp.LatestSynthesisPromptVersions(organizingapp.SynthesisOriginalPromptVersion(input), true)
			switch mode {
			case "old generator":
				input.GenerationPromptVersion = organizingapp.SynthesisFusionAnchoredPromptVersion
			case "ordinary generator":
				input.GenerationPromptVersion = organizingapp.SynthesisGenerationFormatAnchoredPromptVersion
			case "missing trigger":
				input.SourceEvent.Fusion = nil
			case "wrong note":
				input.SourceEvent.Fusion.NoteID = synthesisTestID(610)
			case "wrong anchor":
				input.SourceEvent.Fusion.AnchorID = synthesisTestID(611)
			case "wrong scope":
				input.SourceEvent.Fusion.ScopeVersion++
			case "wrong sources":
				input.SourceEvent.Fusion.AllowedSources[0].Title = "changed admission"
			}
			output := `{"notes":[{"note":"","topic_key":"mysql","title":"MySQL","aliases":[],"operations":[{"op":"ADD_FACT","statement":{"text":"MySQL indexing.","applicability":"","sources":["S002"]}}]}]}`
			harness := newSynthesisHarness(t, output)
			if _, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input); err == nil {
				t.Fatal("fusion escaped its bound target")
			}
			wantCalls := 0
			if mode == "new output note" {
				wantCalls = 1
			}
			if harness.provider.CallCount() != wantCalls {
				t.Fatalf("unexpected provider calls %d", harness.provider.CallCount())
			}
		})
	}
	// 普通 source-ready 流程保留创建主题笔记的既有能力。
	input, generation, _ := synthesisFixtureInput(t)
	input.GenerationPromptVersion, input.SemanticPromptVersion = organizingapp.LatestSynthesisPromptVersions(organizingapp.SynthesisOriginalPromptVersion(input), false)
	harness := newSynthesisHarness(t, synthesisFactOutput)
	if _, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input); err != nil {
		t.Fatalf("ordinary topic creation changed: %v", err)
	}
}

func TestSynthesisGenerationFormatSchemaReachesInitialAndRepair(t *testing.T) {
	for _, version := range []string{"v13", "v14", "v15", "v16", "v17", "v18"} {
		t.Run(version, func(t *testing.T) {
			malformed := strings.Replace(synthesisFactOutput, `"statement":`, `"fact":`, 1)
			harness := newSynthesisHarness(t, malformed, synthesisFactOutput)
			dependencies := harness.model.dependencies
			runner, err := agentapp.NewStructuredRunnerWithScheduler(harness.provider, dependencies.Catalog, dependencies.Budget, dependencies.Scheduler)
			if err != nil {
				t.Fatal(err)
			}
			schema := synthesisSchemaForPromptVersion(organizingapp.SynthesisModelGenerate, version)
			result, err := runner.Run(t.Context(), agentapp.StructuredRunRequest{ProfileRef: dependencies.ProfileRef, PromptRef: synthesisPromptForVersion(organizingapp.SynthesisModelGenerate, version), SchemaRef: schema, ReducedSchemaRef: schema, Input: []byte(`{"sources":[]}`)})
			if err != nil || result.CallCount != 2 {
				t.Fatalf("repair did not recover strict statement shape: %+v %v", result, err)
			}
			raw, _ := json.Marshal(synthesisDeltaSchemaVersion(version == "v16" || version == "v18"))
			for _, call := range harness.provider.Calls() {
				system := call.Messages[0].Content
				if !strings.Contains(system, string(raw)) || !strings.Contains(system, `Never use a "fact" key or flatten`) {
					t.Fatal("complete trusted schema/wire rule missing from actual request")
				}
				if call.SchemaRef != schema {
					t.Fatal("schema identity drift")
				}
			}
		})
	}
}

func TestSynthesisHistoricalFusionVersionsStillReplay(t *testing.T) {
	for _, version := range []string{"v11", "v12", "v17", "v18"} {
		published := version == "v12" || version == "v18"
		t.Run(version, func(t *testing.T) {
			input, generation, validation := synthesisFixtureWithNote(t)
			target := &input.Notes[0]
			target.Anchor = &organizingapp.SynthesisAnchorBinding{AnchorID: synthesisTestID(601), ScopeVersion: 2, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"interview"}, Description: "Redis only"}, AllowedSources: []domain.SynthesisSourceRef{input.Sources[1].Reference}}
			input.GenerationPromptVersion = organizingapp.SynthesisFusionAnchoredPromptVersion
			if published {
				target.PublicationID = synthesisTestID(605)
				input.GenerationPromptVersion = organizingapp.SynthesisFusionBodyPromptVersion
			}
			input.GenerationPromptVersion = version
			input.SemanticPromptVersion = organizingapp.SynthesisFusionSemanticPromptVersion
			input.SourceEvent.Fusion = &domain.SynthesisFusionTrigger{RequestID: synthesisTestID(602), AnchorID: target.Anchor.AnchorID, NoteID: target.Note.ID, ProposalID: synthesisTestID(603), ScopeVersion: 2, AllowedSources: target.Anchor.AllowedSources}
			harness := newSynthesisHarness(t, `{"notes":[]}`, `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[]}]}`)
			generated, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, generated)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
			if err != nil || !reflect.DeepEqual(generated, replay) {
				t.Fatal("historical generation replay changed")
			}
			reviewed, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, replay)
			if err != nil || reviewed != receipt || harness.provider.CallCount() != 2 {
				t.Fatal("historical review replay changed")
			}

			for _, message := range harness.provider.Calls()[1].Messages {
				if strings.Contains(message.Content, "existing_sources") {
					t.Fatal("old v8 payload or message changed")
				}
			}
			if (version == "v11" || version == "v12") && strings.Contains(harness.provider.Calls()[0].Messages[0].Content, "Output wire format:") {
				t.Fatal("historical system prompt changed")
			}
		})
	}
}

func TestSynthesisFusionExistingEvidenceEligibility(t *testing.T) {
	input, generation, validation := synthesisFixtureWithNote(t)
	target := &input.Notes[0]
	target.Anchor = &organizingapp.SynthesisAnchorBinding{AnchorID: synthesisTestID(601), ScopeVersion: 2, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"interview"}, Description: "Redis only"}, AllowedSources: []domain.SynthesisSourceRef{input.Sources[1].Reference}}
	input.SourceEvent.Fusion = &domain.SynthesisFusionTrigger{RequestID: synthesisTestID(602), AnchorID: target.Anchor.AnchorID, NoteID: target.Note.ID, ProposalID: synthesisTestID(603), ScopeVersion: 2, AllowedSources: target.Anchor.AllowedSources}
	input.GenerationPromptVersion, input.SemanticPromptVersion = organizingapp.LatestSynthesisPromptVersions(organizingapp.SynthesisOriginalPromptVersion(input), true)
	output := `{"notes":[{"note":"N001","operations":[{"op":"ADD_FACT","statement":{"text":"Entries expire after fifteen minutes.","applicability":"","sources":["S001"]}}]}]}`
	review := `{"checks":[{"index":1,"verdict":"UNSUPPORTED","sources":[{"source":"S001","verdict":"UNSUPPORTED"}]}]}`
	harness := newSynthesisHarness(t, output, review)
	generated, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, generated)
	if synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisSemanticRejected || receipt.Accepted {
		t.Fatalf("existing evidence bypassed review: %+v %v", receipt, err)
	}
	calls := harness.provider.Calls()
	if strings.Contains(calls[0].Messages[0].Content, "Existing-source eligibility") {
		t.Fatal("generation message changed")
	}
	for _, message := range calls[0].Messages {
		if strings.Contains(message.Content, `"existing_sources"`) {
			t.Fatal("generation payload changed")
		}
	}
	all := ""
	for _, message := range calls[1].Messages {
		all += message.Content
	}
	if !strings.Contains(all, `"existing_sources":["S001"]`) || !strings.Contains(all, `"allowed_sources":["S002"]`) || !strings.Contains(calls[1].Messages[0].Content, "Eligibility is not semantic support") {
		t.Fatal("actual semantic message lacks exact eligibility contract")
	}
	replay, err := harness.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, generated)
	if synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisSemanticRejected || replay != receipt || harness.provider.CallCount() != 2 {
		t.Fatal("rejected review was retried")
	}

	for _, scenario := range []string{"business commit unknown", "model call persistence unknown"} {
		t.Run(scenario, func(t *testing.T) {
			h := newSynthesisHarness(t, output, review)
			result, err := h.model.GenerateSynthesisForExecution(t.Context(), generation, input)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "business commit unknown" {
				h.store.dropComplete = true
			} else {
				h.store.failCallCompletion = true
			}
			if _, err := h.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result); err == nil {
				t.Fatal("unknown semantic reported success")
			}
			h.store.dropComplete = false
			if _, err := h.model.ValidateSynthesisSemanticsForExecution(t.Context(), validation, input, result); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelReplayUnsafe {
				t.Fatalf("unknown semantic replay: %v", err)
			}
			if h.provider.CallCount() != 2 {
				t.Fatal("unknown semantic made extra call")
			}
		})
	}

	// 投影排除未加载的精确引用、先前机器条目、其他候选，以及仅满足 incoming=false 的已加载来源。
	extra := synthesisFixtureSource(300, "Unrelated loaded history")
	input.Sources = append(input.Sources, extra)
	target = &input.Notes[0]
	target.Revision.Manuscript = &domain.SynthesisManuscript{Machine: domain.SynthesisManuscriptMachine{MachineItems: []domain.SynthesisItem{{ID: synthesisTestID(900), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Sources: []domain.SynthesisSourceRef{extra.Reference}}}}}}
	unloaded := input.Sources[0].Reference
	unloaded.Title = "exact tuple differs"
	target.Revision.Items[0].Fact.Sources = append(target.Revision.Items[0].Fact.Sources, unloaded)
	target.Supplements = []organizingapp.SynthesisSourceSupplement{
		{ItemID: target.Revision.Items[0].ID, Slot: "FACT", AlternativeIndex: -1, Reference: input.Sources[1].Reference},
		{ItemID: target.Revision.Items[0].ID, Slot: "CONFLICT", AlternativeIndex: 0, Reference: extra.Reference},
		{ItemID: synthesisTestID(999), Slot: "FACT", AlternativeIndex: -1, Reference: extra.Reference},
	}
	projected := synthesisInput(input)
	if got := synthesisExistingSourceLabels(projected.Notes[0]); !reflect.DeepEqual(got, []string{"S001", "S002"}) {
		t.Fatalf("wrong eligible source union: %v", got)
	}
	target.Revision.Items = nil
	target.Supplements = nil
	if got := synthesisExistingSourceLabels(synthesisInput(input).Notes[0]); got == nil || len(got) != 0 {
		t.Fatalf("empty current revision inherited evidence: %v", got)
	}
}
