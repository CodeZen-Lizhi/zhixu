package domain

import "testing"

func TestValidateRunTransitionUsesIndependentRunTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     RunStatus
		to       RunStatus
		accepted bool
	}{
		{name: "pending starts", from: RunStatusPending, to: RunStatusRunning, accepted: true},
		{name: "running waits for human", from: RunStatusRunning, to: RunStatusWaitingForHuman, accepted: true},
		{name: "running waits for retry", from: RunStatusRunning, to: RunStatusRetryWait, accepted: true},
		{name: "running pauses", from: RunStatusRunning, to: RunStatusPaused, accepted: true},
		{name: "paused resumes running", from: RunStatusPaused, to: RunStatusRunning, accepted: true},
		{name: "running succeeds", from: RunStatusRunning, to: RunStatusSucceeded, accepted: true},
		{name: "terminal cannot reopen", from: RunStatusSucceeded, to: RunStatusRunning, accepted: false},
		{name: "unknown source rejected", from: RunStatus("future"), to: RunStatusRunning, accepted: false},
		{name: "self transition rejected", from: RunStatusRunning, to: RunStatusRunning, accepted: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRunTransition(test.from, test.to)
			if (err == nil) != test.accepted {
				t.Fatalf("ValidateRunTransition(%q, %q) error = %v, accepted = %t", test.from, test.to, err, test.accepted)
			}
		})
	}
}

func TestValidateNodeTransitionUsesIndependentNodeTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     NodeStatus
		to       NodeStatus
		accepted bool
	}{
		{name: "pending claims", from: NodeStatusPending, to: NodeStatusRunning, accepted: true},
		{name: "pending pauses", from: NodeStatusPending, to: NodeStatusPaused, accepted: true},
		{name: "running waits for human", from: NodeStatusRunning, to: NodeStatusWaitingForHuman, accepted: true},
		{name: "running schedules retry", from: NodeStatusRunning, to: NodeStatusRetryWait, accepted: true},
		{name: "retry claims", from: NodeStatusRetryWait, to: NodeStatusRunning, accepted: true},
		{name: "paused restores human wait", from: NodeStatusPaused, to: NodeStatusWaitingForHuman, accepted: true},
		{name: "human node completes", from: NodeStatusWaitingForHuman, to: NodeStatusSucceeded, accepted: true},
		{name: "pending cannot succeed", from: NodeStatusPending, to: NodeStatusSucceeded, accepted: false},
		{name: "terminal cannot reopen", from: NodeStatusCancelled, to: NodeStatusPending, accepted: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateNodeTransition(test.from, test.to)
			if (err == nil) != test.accepted {
				t.Fatalf("ValidateNodeTransition(%q, %q) error = %v, accepted = %t", test.from, test.to, err, test.accepted)
			}
		})
	}
}

func TestValidateAttemptTransitionOnlyTerminatesRunningAttempt(t *testing.T) {
	t.Parallel()

	for _, target := range []AttemptStatus{
		AttemptStatusSucceeded,
		AttemptStatusWaitingForHuman,
		AttemptStatusRetryScheduled,
		AttemptStatusFailed,
		AttemptStatusManualRecovery,
		AttemptStatusLeaseLost,
		AttemptStatusCancelled,
	} {
		if err := ValidateAttemptTransition(AttemptStatusRunning, target); err != nil {
			t.Errorf("running -> %s error = %v", target, err)
		}
		if err := ValidateAttemptTransition(target, AttemptStatusRunning); err == nil {
			t.Errorf("terminal %s reopened without error", target)
		}
	}
}

func TestLegacyValidateTransitionRemainsNodeCompatible(t *testing.T) {
	t.Parallel()

	if err := ValidateTransition(StatusPending, StatusRunning); err != nil {
		t.Fatalf("ValidateTransition() error = %v", err)
	}
	if err := ValidateTransition(StatusPending, StatusSucceeded); err == nil {
		t.Fatal("ValidateTransition() accepted invalid legacy node transition")
	}
}

func TestTransitionTablesCoverEveryStatusPair(t *testing.T) {
	t.Parallel()

	runStatuses := []RunStatus{RunStatusPending, RunStatusRunning, RunStatusWaitingForHuman, RunStatusRetryWait, RunStatusPaused, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled}
	for _, from := range runStatuses {
		for _, to := range runStatuses {
			_, accepted := runTransitions[from][to]
			if got := ValidateRunTransition(from, to); (got == nil) != accepted {
				t.Errorf("Run %s -> %s error = %v, accepted = %t", from, to, got, accepted)
			}
		}
	}

	nodeStatuses := []NodeStatus{NodeStatusPending, NodeStatusRunning, NodeStatusWaitingForHuman, NodeStatusRetryWait, NodeStatusPaused, NodeStatusSucceeded, NodeStatusFailed, NodeStatusCancelled}
	for _, from := range nodeStatuses {
		for _, to := range nodeStatuses {
			_, accepted := nodeTransitions[from][to]
			if got := ValidateNodeTransition(from, to); (got == nil) != accepted {
				t.Errorf("Node %s -> %s error = %v, accepted = %t", from, to, got, accepted)
			}
		}
	}

	attemptStatuses := []AttemptStatus{AttemptStatusRunning, AttemptStatusSucceeded, AttemptStatusWaitingForHuman, AttemptStatusRetryScheduled, AttemptStatusFailed, AttemptStatusManualRecovery, AttemptStatusLeaseLost, AttemptStatusCancelled}
	for _, from := range attemptStatuses {
		for _, to := range attemptStatuses {
			_, accepted := attemptTransitions[from][to]
			if got := ValidateAttemptTransition(from, to); (got == nil) != accepted {
				t.Errorf("Attempt %s -> %s error = %v, accepted = %t", from, to, got, accepted)
			}
		}
	}
}

func TestTerminalStatusPredicates(t *testing.T) {
	t.Parallel()

	for _, status := range []RunStatus{RunStatusSucceeded, RunStatusFailed, RunStatusCancelled} {
		if !IsTerminalRunStatus(status) {
			t.Errorf("Run status %s is not terminal", status)
		}
	}
	if IsTerminalRunStatus(RunStatusPaused) {
		t.Fatal("paused Run reported terminal")
	}
	for _, status := range []NodeStatus{NodeStatusSucceeded, NodeStatusFailed, NodeStatusCancelled} {
		if !IsTerminalNodeStatus(status) {
			t.Errorf("Node status %s is not terminal", status)
		}
	}
	if IsTerminalNodeStatus(NodeStatusRetryWait) {
		t.Fatal("retry_wait Node reported terminal")
	}
}
