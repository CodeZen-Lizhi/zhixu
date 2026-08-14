package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRunBudgetLedgerPreservesDownstreamReservationsForAgentCalls(t *testing.T) {
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{
		MaxTotalModelCalls: 7, MaxAgentIterations: 2, MaxToolCalls: 2, MaxInputTokens: 70, MaxOutputTokens: 70,
	})

	first, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Settle(&RunBudgetUsage{InputTokens: 4, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	second, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Settle(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 1, ReservedOutputTokens: 1}); errorCode(err) != ErrorCodeRunBudgetLedgerIterations {
		t.Fatalf("third agent authorization error = %v, code=%q", err, errorCode(err))
	}

	for _, phase := range downstreamBudgetPhases {
		authorization, err := ledger.AuthorizeDownstreamCall(phase)
		if err != nil {
			t.Fatalf("AuthorizeDownstreamCall(%s) error = %v", phase, err)
		}
		if err := authorization.Settle(nil); err != nil {
			t.Fatalf("Settle(%s) error = %v", phase, err)
		}
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 7 || snapshot.AgentIterations != 2 || snapshot.InputTokens != 64 || snapshot.OutputTokens != 63 ||
		snapshot.ReservedModelCalls != 0 || snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRunBudgetLedgerAuthorizesOnePlanAndAssignsStableCallNumbers(t *testing.T) {
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{
		MaxTotalModelCalls: 7, MaxAgentIterations: 1, MaxToolCalls: 0, MaxInputTokens: 70, MaxOutputTokens: 70,
	})
	plan, err := ledger.AuthorizePlanCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CallNo != 1 || plan.Phase != RunBudgetPhasePlan {
		t.Fatalf("plan authorization=%+v", plan)
	}
	if err := plan.Settle(&RunBudgetUsage{InputTokens: 2, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	agent, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if agent.CallNo != 2 || agent.Phase != RunBudgetPhaseAgent {
		t.Fatalf("agent authorization=%+v", agent)
	}
	if _, err := ledger.AuthorizePlanCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 1, ReservedOutputTokens: 1}); errorCode(err) != ErrorCodeRunBudgetLedgerPhase {
		t.Fatalf("duplicate plan error=%v", err)
	}
}

func TestRunBudgetLedgerSettlesActualUsageAndRejectsDuplicateOrOverLimitUsage(t *testing.T) {
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{
		MaxTotalModelCalls: 6, MaxAgentIterations: 1, MaxToolCalls: 0, MaxInputTokens: 60, MaxOutputTokens: 60,
	})
	authorization, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorization.Settle(&RunBudgetUsage{InputTokens: 2, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	if err := authorization.Settle(nil); errorCode(err) != ErrorCodeRunBudgetLedgerSettlement {
		t.Fatalf("duplicate settle error = %v, code=%q", err, errorCode(err))
	}

	overLimit, err := ledger.AuthorizeDownstreamCall(RunBudgetPhaseAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if err := overLimit.Settle(&RunBudgetUsage{InputTokens: 19, OutputTokens: 1}); errorCode(err) != ErrorCodeRunBudgetLedgerSettlement {
		t.Fatalf("over-limit settle error = %v, code=%q", err, errorCode(err))
	}
	snapshot := ledger.Snapshot()
	if snapshot.InputTokens != 12 || snapshot.OutputTokens != 13 || snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("over-limit settlement did not charge its authorization maximum: %+v", snapshot)
	}
}

func TestRunBudgetLedgerSettlesReasoningUsageFromUnreservedRunCapacity(t *testing.T) {
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{
		MaxTotalModelCalls: 6, MaxAgentIterations: 1, MaxToolCalls: 0,
		MaxInputTokens: 10_000, MaxOutputTokens: 10_000,
	})
	authorization, err := ledger.AuthorizePlanCall(RunBudgetReservation{
		ModelCalls: 1, ReservedInputTokens: 512, ReservedOutputTokens: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorization.Settle(&RunBudgetUsage{InputTokens: 391, OutputTokens: 1_276}); err != nil {
		t.Fatalf("reasoning-inclusive settlement error = %v, code=%q", err, errorCode(err))
	}
	snapshot := ledger.Snapshot()
	if snapshot.InputTokens != 391 || snapshot.OutputTokens != 1_276 ||
		snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRunBudgetLedgerRejectsDeadlineAndToolOverages(t *testing.T) {
	clock := &runBudgetTestClock{value: time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)}
	deadline := clock.Now().Add(20 * time.Millisecond)
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{
		MaxTotalModelCalls: 5, MaxAgentIterations: 1, MaxToolCalls: 1, MaxInputTokens: 50, MaxOutputTokens: 50, Deadline: deadline, Clock: clock,
	})
	if err := ledger.AuthorizeToolCall(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AuthorizeToolCall(); errorCode(err) != ErrorCodeRunBudgetLedgerToolCalls {
		t.Fatalf("second tool authorization error = %v, code=%q", err, errorCode(err))
	}
	clock.Advance(30 * time.Millisecond)
	if _, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 1, ReservedOutputTokens: 1}); errorCode(err) != ErrorCodeRunBudgetLedgerDeadline || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, code=%q", err, errorCode(err))
	}
}

func TestRunBudgetLedgerConcurrentAuthorizationCannotExhaustReservedCapacity(t *testing.T) {
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{
		MaxTotalModelCalls: 6, MaxAgentIterations: 4, MaxToolCalls: 0, MaxInputTokens: 60, MaxOutputTokens: 60,
	})
	results := make(chan struct{}, 16)
	var successes int
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			authorization, err := ledger.AuthorizeAgentCall(RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10})
			if err == nil {
				if settleErr := authorization.Settle(nil); settleErr != nil {
					t.Errorf("Settle() error = %v", settleErr)
					return
				}
				results <- struct{}{}
			}
		}()
	}
	wait.Wait()
	close(results)
	for range results {
		successes++
	}
	if successes != 1 {
		t.Fatalf("successful agent authorizations = %d, want 1", successes)
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 1 || snapshot.AgentIterations != 1 || snapshot.InputTokens != 10 || snapshot.OutputTokens != 10 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestNewRunBudgetLedgerRejectsIncompleteReservations(t *testing.T) {
	config := validRunBudgetLedgerConfig()
	delete(config.DownstreamReservations, RunBudgetPhaseReview)
	if _, err := NewRunBudgetLedger(config); errorCode(err) != ErrorCodeRunBudgetLedgerInvalid {
		t.Fatalf("missing phase error = %v, code=%q", err, errorCode(err))
	}
}

func TestNewRunBudgetLedgerRejectsTokenOverflowConfiguration(t *testing.T) {
	config := validRunBudgetLedgerConfig()
	config.MaxInputTokens = maxRunBudgetLedgerTokens + 1
	if _, err := NewRunBudgetLedger(config); errorCode(err) != ErrorCodeRunBudgetLedgerInvalid {
		t.Fatalf("overflow configuration error = %v, code=%q", err, errorCode(err))
	}
}

func newTestRunBudgetLedger(t *testing.T, override RunBudgetLedgerConfig) *RunBudgetLedger {
	t.Helper()
	config := validRunBudgetLedgerConfig()
	if override.MaxTotalModelCalls != 0 {
		config.MaxTotalModelCalls = override.MaxTotalModelCalls
	}
	if override.MaxAgentIterations != 0 {
		config.MaxAgentIterations = override.MaxAgentIterations
	}
	config.MaxToolCalls = override.MaxToolCalls
	if override.MaxInputTokens != 0 {
		config.MaxInputTokens = override.MaxInputTokens
	}
	if override.MaxOutputTokens != 0 {
		config.MaxOutputTokens = override.MaxOutputTokens
	}
	if !override.Deadline.IsZero() {
		config.Deadline = override.Deadline
	}
	if override.Clock != nil {
		config.Clock = override.Clock
	}
	ledger, err := NewRunBudgetLedger(config)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

type runBudgetTestClock struct {
	mu    sync.Mutex
	value time.Time
}

func (clock *runBudgetTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.value
}

func (clock *runBudgetTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.value = clock.value.Add(duration)
}

func validRunBudgetLedgerConfig() RunBudgetLedgerConfig {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	return RunBudgetLedgerConfig{
		NodeAttemptID:      foundation.ID("10000000-0000-4000-8000-000000000001"),
		ModelRunID:         foundation.ID("20000000-0000-4000-8000-000000000001"),
		MaxTotalModelCalls: 8, MaxAgentIterations: 3, MaxToolCalls: 2,
		MaxInputTokens: 80, MaxOutputTokens: 80, Deadline: now.Add(time.Minute), Clock: foundation.FixedClock{Value: now},
		DownstreamReservations: map[RunBudgetPhase]RunBudgetReservation{
			RunBudgetPhaseAnswer:  {ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10},
			RunBudgetPhaseInitial: {ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10},
			RunBudgetPhaseRepair:  {ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10},
			RunBudgetPhaseReduced: {ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10},
			RunBudgetPhaseReview:  {ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10},
		},
	}
}
