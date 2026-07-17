package domain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestApplyCounterTransitionFreezesAttemptDispatchRetrySemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    NodeCounters
		transition CounterTransition
		want       NodeCounters
	}{
		{name: "initial dispatch", transition: CounterInitialDispatch, want: NodeCounters{DispatchNo: 1}},
		{name: "river redelivery is free", current: NodeCounters{AttemptNo: 1, DispatchNo: 1}, transition: CounterRiverRedelivery, want: NodeCounters{AttemptNo: 1, DispatchNo: 1}},
		{name: "lease acquire increments attempt only", current: NodeCounters{DispatchNo: 1}, transition: CounterLeaseAcquired, want: NodeCounters{AttemptNo: 1, DispatchNo: 1}},
		{name: "lease reclaim increments attempt only", current: NodeCounters{AttemptNo: 1, DispatchNo: 1}, transition: CounterLeaseReclaimed, want: NodeCounters{AttemptNo: 2, DispatchNo: 1}},
		{name: "business retry increments retry and dispatch", current: NodeCounters{AttemptNo: 2, DispatchNo: 1}, transition: CounterBusinessRetry, want: NodeCounters{AttemptNo: 2, DispatchNo: 2, RetryNo: 1}},
		{name: "human resume increments dispatch only", current: NodeCounters{AttemptNo: 2, DispatchNo: 2, RetryNo: 1}, transition: CounterHumanResume, want: NodeCounters{AttemptNo: 2, DispatchNo: 3, RetryNo: 1}},
		{name: "pause resume increments dispatch only", current: NodeCounters{AttemptNo: 2, DispatchNo: 3, RetryNo: 1}, transition: CounterPauseResume, want: NodeCounters{AttemptNo: 2, DispatchNo: 4, RetryNo: 1}},
		{name: "recovery republish increments dispatch only", current: NodeCounters{AttemptNo: 2, DispatchNo: 4, RetryNo: 1}, transition: CounterRecoveryRepublish, want: NodeCounters{AttemptNo: 2, DispatchNo: 5, RetryNo: 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ApplyCounterTransition(test.current, test.transition)
			if err != nil {
				t.Fatalf("ApplyCounterTransition() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ApplyCounterTransition() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestApplyCounterTransitionRejectsInvalidCounterHistory(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		current    NodeCounters
		transition CounterTransition
	}{
		{current: NodeCounters{AttemptNo: -1}, transition: CounterLeaseAcquired},
		{current: NodeCounters{DispatchNo: 1}, transition: CounterInitialDispatch},
		{current: NodeCounters{}, transition: CounterBusinessRetry},
		{current: NodeCounters{}, transition: CounterTransition("future")},
	} {
		if _, err := ApplyCounterTransition(test.current, test.transition); err == nil {
			t.Errorf("ApplyCounterTransition(%+v, %q) accepted invalid history", test.current, test.transition)
		}
	}
}

func TestClassifyFailureUsesFrozenPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input FailureInput
		class FailureClass
		code  string
	}{
		{name: "trusted explicit wins", input: FailureInput{Explicit: &FailureEnvelope{Class: FailureClassManualRecovery, ErrorKind: foundation.ErrorManualRecoveryRequired, Code: "RESULT_UNKNOWN", Summary: "manual recovery"}, Err: foundation.NewError(foundation.ErrorRetryableFailure, "DEPENDENCY_BUSY", true, errors.New("secret"))}, class: FailureClassManualRecovery, code: "RESULT_UNKNOWN"},
		{name: "lease code overrides broad kind", input: FailureInput{Err: foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("owner"))}, class: FailureClassLeaseLost, code: "WORKFLOW_LEASE_LOST"},
		{name: "writeback lease code uses the workflow lease class", input: FailureInput{Err: foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, errors.New("owner"))}, class: FailureClassLeaseLost, code: "WRITEBACK_LEASE_LOST"},
		{name: "proven cancellation precedes kind", input: FailureInput{Err: foundation.NewError(foundation.ErrorRetryableFailure, "CALL_ABORTED", true, context.Canceled), CancellationProven: true}, class: FailureClassCancelled, code: "CALL_ABORTED"},
		{name: "manual kind", input: FailureInput{Err: foundation.NewError(foundation.ErrorManualRecoveryRequired, "RESULT_UNKNOWN", false, errors.New("unknown"))}, class: FailureClassManualRecovery, code: "RESULT_UNKNOWN"},
		{name: "consistency defaults non retryable", input: FailureInput{Err: foundation.NewError(foundation.ErrorConsistencyViolation, "CONSISTENCY_REJECTED", false, errors.New("conflict"))}, class: FailureClassNonRetryable, code: "CONSISTENCY_REJECTED"},
		{name: "retryable flag", input: FailureInput{Err: foundation.NewError(foundation.ErrorDependencyUnavailable, "DEPENDENCY_DOWN", true, errors.New("down"))}, class: FailureClassRetryable, code: "DEPENDENCY_DOWN"},
		{name: "non retryable kind", input: FailureInput{Err: foundation.NewError(foundation.ErrorNonRetryableFailure, "INPUT_REJECTED", false, errors.New("bad"))}, class: FailureClassNonRetryable, code: "INPUT_REJECTED"},
		{name: "unknown defaults non retryable", input: FailureInput{Err: errors.New("raw secret")}, class: FailureClassNonRetryable, code: "WORKFLOW_EXECUTION_FAILED"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClassifyFailure(test.input)
			if err != nil {
				t.Fatalf("ClassifyFailure() error = %v", err)
			}
			if got.Class != test.class || got.Code != test.code {
				t.Fatalf("ClassifyFailure() = %+v, want class=%q code=%q", got, test.class, test.code)
			}
			if strings.Contains(got.Summary, "secret") || strings.Contains(got.Summary, "owner") || strings.Contains(got.Summary, "raw") {
				t.Fatalf("ClassifyFailure() leaked cause in summary %q", got.Summary)
			}
		})
	}
}

func TestClassifyFailureValidatesTrustedRetryAfterAndRedactsSummary(t *testing.T) {
	t.Parallel()

	if _, err := ClassifyFailure(FailureInput{Err: errors.New("failure"), RetryAfter: -time.Second}); err == nil {
		t.Fatal("ClassifyFailure() accepted negative retry-after")
	}
	zero, err := ClassifyFailure(FailureInput{Err: errors.New("failure"), RetryAfter: 0})
	if err != nil || zero.RetryAfter != 0 {
		t.Fatalf("zero retry-after = %s, %v", zero.RetryAfter, err)
	}
	bounded, err := ClassifyFailure(FailureInput{Explicit: &FailureEnvelope{Class: FailureClassRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "DEPENDENCY_BUSY", Summary: "token=super-secret", RetryAfter: MaxTrustedRetryAfter + time.Hour}})
	if err != nil {
		t.Fatalf("ClassifyFailure() error = %v", err)
	}
	if bounded.RetryAfter != MaxTrustedRetryAfter {
		t.Fatalf("RetryAfter = %s, want %s", bounded.RetryAfter, MaxTrustedRetryAfter)
	}
	if bounded.Summary != RedactedFailureSummary {
		t.Fatalf("Summary = %q, want redacted summary", bounded.Summary)
	}
	long, err := ClassifyFailure(FailureInput{Err: errors.New("failure"), TrustedSummary: strings.Repeat("a", MaxFailureSummaryRunes+10)})
	if err != nil {
		t.Fatalf("ClassifyFailure() error = %v", err)
	}
	if len([]rune(long.Summary)) != MaxFailureSummaryRunes {
		t.Fatalf("summary runes = %d, want %d", len([]rune(long.Summary)), MaxFailureSummaryRunes)
	}
}

func TestValidateAttemptResultRequiresExactlyOneSuccessOrFailure(t *testing.T) {
	t.Parallel()

	failure := FailureEnvelope{Class: FailureClassNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure, Code: "FAILED", Summary: "FAILED"}
	for _, test := range []struct {
		name  string
		value AttemptResult
		valid bool
	}{
		{name: "success", value: AttemptResult{Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1}, valid: true},
		{name: "failure", value: AttemptResult{Failure: &failure}, valid: true},
		{name: "neither", value: AttemptResult{}, valid: false},
		{name: "both", value: AttemptResult{Output: json.RawMessage(`{}`), OutputSchemaVersion: 1, Failure: &failure}, valid: false},
		{name: "invalid output", value: AttemptResult{Output: json.RawMessage(`{`), OutputSchemaVersion: 1}, valid: false},
		{name: "missing schema", value: AttemptResult{Output: json.RawMessage(`{}`)}, valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAttemptResult(test.value)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateAttemptResult() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestCalculateRetryUsesBusinessRetryOnlyAndDeterministicJitter(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{MaxRetries: 3, BaseDelay: 10 * time.Second, MaxDelay: time.Minute}
	identity := RetryIdentity{RunID: "run-1", NodeKey: "extract"}
	first, err := CalculateRetry(policy, identity, 0, 0)
	if err != nil {
		t.Fatalf("CalculateRetry(first) error = %v", err)
	}
	again, err := CalculateRetry(policy, identity, 0, 0)
	if err != nil {
		t.Fatalf("CalculateRetry(replay) error = %v", err)
	}
	if first != again || first.NextRetryNo != 1 || first.Exhausted || first.Delay < 10*time.Second || first.Delay > 12*time.Second {
		t.Fatalf("first retry = %+v, replay = %+v", first, again)
	}
	second, err := CalculateRetry(policy, identity, 1, 45*time.Second)
	if err != nil {
		t.Fatalf("CalculateRetry(second) error = %v", err)
	}
	if second.NextRetryNo != 2 || second.Delay != 45*time.Second {
		t.Fatalf("second retry = %+v, want retry_no=2 delay=45s", second)
	}
	exhausted, err := CalculateRetry(policy, identity, 3, 0)
	if err != nil {
		t.Fatalf("CalculateRetry(exhausted) error = %v", err)
	}
	if !exhausted.Exhausted || exhausted.NextRetryNo != 3 || exhausted.Delay != 0 {
		t.Fatalf("exhausted retry = %+v", exhausted)
	}
}

func TestCalculateRetryRejectsInvalidPolicyAndSaturates(t *testing.T) {
	t.Parallel()

	invalid := []RetryPolicy{
		{MaxRetries: -1, BaseDelay: time.Second, MaxDelay: time.Second},
		{MaxRetries: 1, BaseDelay: 0, MaxDelay: time.Second},
		{MaxRetries: 1, BaseDelay: 2 * time.Second, MaxDelay: time.Second},
	}
	for _, policy := range invalid {
		if _, err := CalculateRetry(policy, RetryIdentity{RunID: "run", NodeKey: "node"}, 0, 0); err == nil {
			t.Errorf("CalculateRetry(%+v) accepted invalid policy", policy)
		}
	}
	if _, err := CalculateRetry(RetryPolicy{MaxRetries: 1, BaseDelay: time.Second, MaxDelay: time.Minute}, RetryIdentity{}, 0, 0); err == nil {
		t.Fatal("CalculateRetry() accepted empty identity")
	}
	if _, err := CalculateRetry(RetryPolicy{MaxRetries: 1, BaseDelay: time.Second, MaxDelay: time.Minute}, RetryIdentity{RunID: "run", NodeKey: "node"}, 0, -time.Second); err == nil {
		t.Fatal("CalculateRetry() accepted negative retry-after")
	}
}
