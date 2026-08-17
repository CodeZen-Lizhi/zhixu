package application

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisBudgetV1FreezesCountsAndTokenCeilings(t *testing.T) {
	t.Parallel()

	policy, err := WorkspaceAnalysisBudgetV1(8192)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceAnalysisBudgetPolicy{
		PolicyVersion:              1,
		MaxNodes:                   6,
		MaxModelCalls:              3,
		MaxToolCalls:               6,
		MaxSourceReads:             3,
		MaxToolConcurrency:         1,
		MaxInputTokensPerModelCall: 65_536,
		MaxRunInputTokens:          196_608,
		PlanMaxOutputTokens:        256,
		SynthesisMaxOutputTokens:   4096,
		ReviewMaxOutputTokens:      1024,
		MaxRunOutputTokens:         5376,
		MaxRunDuration:             time.Hour,
	}
	if !reflect.DeepEqual(policy, want) {
		t.Fatalf("policy=%+v, want=%+v", policy, want)
	}
	if WorkspaceAnalysisV1MaxRunInputTokens != WorkspaceAnalysisV1MaxModelCalls*WorkspaceAnalysisV1MaxInputTokensPerModelCall ||
		WorkspaceAnalysisV1MaxRunOutputTokens != WorkspaceAnalysisV1PlanMaxOutputTokens+WorkspaceAnalysisV1SynthesisMaxOutputTokens+WorkspaceAnalysisV1ReviewMaxOutputTokens {
		t.Fatal("run token ceilings drifted from their phase ceilings")
	}
	if WorkspaceAnalysisV1PlanMaxOutputTokens != int64(queryPlanMaxOutputTokens) ||
		WorkspaceAnalysisV1ReviewMaxOutputTokens != int64(faithfulnessOutputTokenFloor) {
		t.Fatal("workspace analysis phase ceilings drifted from the reused runners")
	}
}

func TestWorkspaceAnalysisBudgetV1NarrowsSynthesisAndRunOutputToProfile(t *testing.T) {
	t.Parallel()

	policy, err := WorkspaceAnalysisBudgetV1(2048)
	if err != nil {
		t.Fatal(err)
	}
	if policy.SynthesisMaxOutputTokens != 2048 || policy.MaxRunOutputTokens != 3328 {
		t.Fatalf("policy=%+v", policy)
	}
	for _, limit := range []int{0, -1, MaxOutputTokens + 1} {
		if _, err := WorkspaceAnalysisBudgetV1(limit); errorCode(err) != ErrorCodeWorkspaceAnalysisPolicyInvalid {
			t.Fatalf("limit=%d error=%v code=%q", limit, err, errorCode(err))
		}
	}
}

func TestDeriveWorkspaceAnalysisV1DeadlinesUsesExactSixNodeFormulas(t *testing.T) {
	t.Parallel()

	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(workspaceAnalysisV1TestTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{
		35 * time.Second,
		3 * time.Minute,
		70 * time.Second,
		5*time.Minute + 15*time.Second,
		35 * time.Second,
		3*time.Minute + 15*time.Second,
	}
	got := []time.Duration{
		deadlines.InspectWorkspaceDeadline(),
		deadlines.RetrieveEvidenceDeadline(),
		deadlines.ReadEvidenceDeadline(),
		deadlines.SynthesizeAnswerDeadline(),
		deadlines.ValidateCitationsDeadline(),
		deadlines.ReviewPublishDeadline(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("node deadlines=%v, want=%v", got, want)
	}
	var total time.Duration
	for _, deadline := range got {
		total += deadline
	}
	if deadlines.RunDeadline() != total || total != 13*time.Minute+50*time.Second {
		t.Fatalf("run deadline=%s total=%s", deadlines.RunDeadline(), total)
	}
	if deadlines.MinimumRiverJobTimeout() != 5*time.Minute+45*time.Second {
		t.Fatalf("minimum River job timeout=%s", deadlines.MinimumRiverJobTimeout())
	}
}

func TestDeriveWorkspaceAnalysisV1DeadlinesAcceptsExactOneHourAndRejectsOverflow(t *testing.T) {
	t.Parallel()

	timeouts := WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: time.Second, SynthesisModelTimeout: 58*time.Minute + 42*time.Second,
		ReviewModelTimeout: time.Second, GitToolTimeout: time.Second, SearchToolTimeout: time.Second,
		SourceReadToolTimeout: time.Second, ValidateCitationToolTimeout: time.Second,
	}
	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if deadlines.RunDeadline() != time.Hour {
		t.Fatalf("run deadline=%s, want=1h", deadlines.RunDeadline())
	}
	timeouts.SynthesisModelTimeout += time.Nanosecond
	if _, err := DeriveWorkspaceAnalysisV1Deadlines(timeouts); errorCode(err) != ErrorCodeWorkspaceAnalysisPolicyInvalid {
		t.Fatalf("over-cap error=%v code=%q", err, errorCode(err))
	}
}

func TestDeriveWorkspaceAnalysisV1DeadlinesRejectsEveryInvalidTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*WorkspaceAnalysisV1Timeouts)
	}{
		{name: "plan", change: func(v *WorkspaceAnalysisV1Timeouts) { v.PlanModelTimeout = 0 }},
		{name: "synthesis", change: func(v *WorkspaceAnalysisV1Timeouts) { v.SynthesisModelTimeout = -time.Second }},
		{name: "review", change: func(v *WorkspaceAnalysisV1Timeouts) { v.ReviewModelTimeout = 0 }},
		{name: "git", change: func(v *WorkspaceAnalysisV1Timeouts) { v.GitToolTimeout = 0 }},
		{name: "search", change: func(v *WorkspaceAnalysisV1Timeouts) { v.SearchToolTimeout = 0 }},
		{name: "read", change: func(v *WorkspaceAnalysisV1Timeouts) { v.SourceReadToolTimeout = 0 }},
		{name: "validate", change: func(v *WorkspaceAnalysisV1Timeouts) {
			v.ValidateCitationToolTimeout = WorkspaceAnalysisV1MaxRunDuration + time.Nanosecond
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			timeouts := workspaceAnalysisV1TestTimeouts()
			test.change(&timeouts)
			if _, err := DeriveWorkspaceAnalysisV1Deadlines(timeouts); errorCode(err) != ErrorCodeWorkspaceAnalysisPolicyInvalid {
				t.Fatalf("error=%v code=%q", err, errorCode(err))
			}
		})
	}
}

func TestWorkspaceAnalysisRuntimeReadinessUsesRiverMinimumAndRenewableLeaseMargin(t *testing.T) {
	t.Parallel()

	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(workspaceAnalysisV1TestTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	limits := WorkspaceAnalysisRuntimeLimits{
		RiverJobTimeout: deadlines.MinimumRiverJobTimeout(),
		LeaseDuration:   35 * time.Second, HeartbeatInterval: 10 * time.Second,
	}
	if err := deadlines.ValidateRuntimeReadiness(limits); err != nil {
		t.Fatalf("exact readiness boundary error=%v", err)
	}

	limits.RiverJobTimeout--
	assertWorkspaceAnalysisRuntimeNotReady(t, deadlines.ValidateRuntimeReadiness(limits))
	limits.RiverJobTimeout++
	limits.LeaseDuration--
	assertWorkspaceAnalysisRuntimeNotReady(t, deadlines.ValidateRuntimeReadiness(limits))

	// The renewable lease may be much shorter than the Node/River timeout; it
	// only needs the exact heartbeat cadence and five-second commit margin.
	limits.LeaseDuration = 2 * time.Minute
	limits.HeartbeatInterval = 30 * time.Second
	if err := deadlines.ValidateRuntimeReadiness(limits); err != nil {
		t.Fatalf("renewable short lease error=%v", err)
	}
}

func TestWorkspaceAnalysisRuntimeReadinessRejectsMissingOrInvalidCadence(t *testing.T) {
	t.Parallel()

	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(workspaceAnalysisV1TestTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	valid := WorkspaceAnalysisRuntimeLimits{
		RiverJobTimeout: deadlines.MinimumRiverJobTimeout(),
		LeaseDuration:   20 * time.Second, HeartbeatInterval: 5 * time.Second,
	}
	tests := []struct {
		name   string
		change func(*WorkspaceAnalysisRuntimeLimits)
	}{
		{name: "job", change: func(v *WorkspaceAnalysisRuntimeLimits) { v.RiverJobTimeout = 0 }},
		{name: "lease", change: func(v *WorkspaceAnalysisRuntimeLimits) { v.LeaseDuration = 0 }},
		{name: "heartbeat", change: func(v *WorkspaceAnalysisRuntimeLimits) { v.HeartbeatInterval = 0 }},
		{name: "strict third", change: func(v *WorkspaceAnalysisRuntimeLimits) {
			v.LeaseDuration = 9 * time.Second
			v.HeartbeatInterval = 3 * time.Second
		}},
		{name: "completion margin", change: func(v *WorkspaceAnalysisRuntimeLimits) {
			v.LeaseDuration = 20*time.Second - time.Nanosecond
			v.HeartbeatInterval = 5 * time.Second
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := valid
			test.change(&limits)
			assertWorkspaceAnalysisRuntimeNotReady(t, deadlines.ValidateRuntimeReadiness(limits))
		})
	}
	if err := (WorkspaceAnalysisV1Deadlines{}).ValidateRuntimeReadiness(valid); errorCode(err) != ErrorCodeWorkspaceAnalysisPolicyInvalid {
		t.Fatalf("zero deadlines error=%v code=%q", err, errorCode(err))
	}
}

func workspaceAnalysisV1TestTimeouts() WorkspaceAnalysisV1Timeouts {
	return WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: 2 * time.Minute, SynthesisModelTimeout: 5 * time.Minute, ReviewModelTimeout: 3 * time.Minute,
		GitToolTimeout: 30 * time.Second, SearchToolTimeout: 45 * time.Second,
		SourceReadToolTimeout: 20 * time.Second, ValidateCitationToolTimeout: 25 * time.Second,
	}
}

func assertWorkspaceAnalysisRuntimeNotReady(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisRuntimeNotReady ||
		classified.Kind != foundation.ErrorDependencyUnavailable || classified.Retryable {
		t.Fatalf("error=%v classified=%+v", err, classified)
	}
}
