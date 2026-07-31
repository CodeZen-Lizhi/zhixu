package domain

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateSwitchTransitionRequiresOrderedSingleGrantPhases(t *testing.T) {
	valid := [][2]SwitchPhase{
		{SwitchPhaseValidating, SwitchPhaseQuiescing},
		{SwitchPhaseQuiescing, SwitchPhaseRevoking},
		{SwitchPhaseRevoking, SwitchPhaseApplyingGrant},
		{SwitchPhaseApplyingGrant, SwitchPhasePreparing},
		{SwitchPhasePreparing, SwitchPhaseVerifying},
		{SwitchPhaseVerifying, SwitchPhaseCommitting},
		{SwitchPhaseCommitting, SwitchPhaseActivating},
		{SwitchPhasePreparing, SwitchPhaseRollingBack},
		{SwitchPhaseRollingBack, SwitchPhaseRecovering},
	}
	for _, transition := range valid {
		if err := ValidateSwitchTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}
	assertWorkspaceDomainCode(t, ValidateSwitchTransition(SwitchPhaseValidating, SwitchPhaseCommitting), ErrorCodeSwitchPhaseConflict)
	assertWorkspaceDomainCode(t, ValidateSwitchTransition(SwitchPhaseQuiescing, SwitchPhaseRollingBack), ErrorCodeSwitchPhaseConflict)
}

func TestValidateTerminalTransitionRequiresSafeFinalPhase(t *testing.T) {
	for _, fixture := range []struct {
		phase     SwitchPhase
		result    SwitchResult
		errorCode string
	}{
		{SwitchPhaseActivating, SwitchResultSucceeded, ""},
		{SwitchPhaseValidating, SwitchResultRejected, "WORKSPACE_PATH_MISSING"},
		{SwitchPhaseQuiescing, SwitchResultCancelled, ""},
		{SwitchPhaseRecovering, SwitchResultRolledBack, "WORKSPACE_TARGET_UNAVAILABLE"},
		{SwitchPhaseRecovering, SwitchResultFailed, "WORKSPACE_RECOVERY_FAILED"},
	} {
		if err := ValidateTerminalTransition(fixture.phase, fixture.result, fixture.errorCode); err != nil {
			t.Fatalf("terminal %q/%q: %v", fixture.phase, fixture.result, err)
		}
	}
	assertWorkspaceDomainCode(t, ValidateTerminalTransition(SwitchPhaseCommitting, SwitchResultSucceeded, ""), ErrorCodeSwitchPhaseConflict)
	assertWorkspaceDomainCode(t, ValidateTerminalTransition(SwitchPhaseRecovering, SwitchResultFailed, "invalid-code"), ErrorCodeSwitchPhaseConflict)
}

func TestValidateRuntimeTransitionSupportsCancellationAndActivation(t *testing.T) {
	for _, transition := range [][2]RuntimePhase{
		{RuntimePhaseActive, RuntimePhaseQuiescing},
		{RuntimePhaseQuiescing, RuntimePhaseQuiesced},
		{RuntimePhaseQuiesced, RuntimePhaseActive},
		{RuntimePhasePrepared, RuntimePhaseVerifying},
		{RuntimePhaseVerifying, RuntimePhaseActive},
		{RuntimePhaseActive, RuntimePhaseUnavailable},
	} {
		if err := ValidateRuntimeTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("runtime transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}
	assertWorkspaceDomainCode(t, ValidateRuntimeTransition(RuntimePhaseActive, RuntimePhasePrepared), ErrorCodeRuntimeConflict)
}

func assertWorkspaceDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
