package domain

import "testing"

func TestValidateTransition(t *testing.T) {
	for _, tc := range []struct {
		from, to Status
		valid    bool
	}{
		{StatusPending, StatusRunning, true}, {StatusRunning, StatusWaitingForHuman, true},
		{StatusWaitingForHuman, StatusRunning, true}, {StatusRunning, StatusSucceeded, true},
		{StatusPending, StatusSucceeded, false}, {StatusSucceeded, StatusRunning, false},
	} {
		if got := ValidateTransition(tc.from, tc.to); (got == nil) != tc.valid {
			t.Errorf("%s -> %s error=%v", tc.from, tc.to, got)
		}
	}
}
