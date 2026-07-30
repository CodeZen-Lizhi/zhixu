package river

import (
	"slices"
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestExportDispatchUniquenessAllowsRecoveryAfterCompletedNoOp(t *testing.T) {
	t.Parallel()

	options := exportUniqueOpts()
	want := []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
		rivertype.JobStateRetryable,
	}
	if !options.ByArgs || !slices.Equal(options.ByState, want) {
		t.Fatalf("Export unique options = %#v", options)
	}
	if slices.Contains(options.ByState, rivertype.JobStateCompleted) {
		t.Fatal("completed no-op transport still blocks durable Export recovery")
	}
}
