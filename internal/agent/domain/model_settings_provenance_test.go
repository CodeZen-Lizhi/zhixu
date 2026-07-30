package domain

import "testing"

func TestValidateModelRunModelSettingsRevisionSemantics(t *testing.T) {
	t.Parallel()
	for _, revision := range []int64{0, 8} {
		run := validModelRun()
		run.ModelSettingsRevision = &revision
		if err := ValidateModelRun(run); err != nil {
			t.Fatalf("revision %d rejected: %v", revision, err)
		}
	}
	negative := int64(-1)
	run := validModelRun()
	run.ModelSettingsRevision = &negative
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("negative revision error=%v", err)
	}
}
