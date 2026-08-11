package domain

import "testing"

func TestActivationGlobalTransitionsAndCommitSide(t *testing.T) {
	legal := [][2]RolloutPhase{
		{RolloutPhaseIdle, RolloutPhasePreparing},
		{RolloutPhaseFailed, RolloutPhasePreparing},
		{RolloutPhasePreparing, RolloutPhaseArming},
		{RolloutPhasePreparing, RolloutPhaseFailed},
		{RolloutPhaseArming, RolloutPhaseActivating},
		{RolloutPhaseArming, RolloutPhaseFailed},
		{RolloutPhaseActivating, RolloutPhaseIdle},
	}
	for _, transition := range legal {
		if err := ValidateActivationTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}
	for _, transition := range [][2]RolloutPhase{
		{RolloutPhasePreparing, RolloutPhaseActivating},
		{RolloutPhaseActivating, RolloutPhaseFailed},
		{RolloutPhaseActivating, RolloutPhaseArming},
		{RolloutPhaseFailed, RolloutPhaseIdle},
	} {
		if err := ValidateActivationTransition(transition[0], transition[1]); err == nil {
			t.Fatalf("transition %q -> %q unexpectedly allowed", transition[0], transition[1])
		}
	}
	for phase, want := range map[RolloutPhase]ActivationCommitSide{
		RolloutPhaseIdle: ActivationCommitSideNone, RolloutPhasePreparing: ActivationCommitSidePreCommit,
		RolloutPhaseArming: ActivationCommitSidePreCommit, RolloutPhaseFailed: ActivationCommitSidePreCommit,
		RolloutPhaseActivating: ActivationCommitSidePostCommit,
	} {
		if got := (RolloutState{Phase: phase}).CommitSide(); got != want {
			t.Fatalf("phase %q commit side=%q want=%q", phase, got, want)
		}
	}
}

func TestParticipantTransitionsReserveActivationForAtomicAcknowledge(t *testing.T) {
	for _, transition := range [][2]ParticipantPhase{
		{ParticipantPhasePreparing, ParticipantPhasePrepared},
		{ParticipantPhasePrepared, ParticipantPhaseArmed},
		{ParticipantPhaseArmed, ParticipantPhaseActivated},
		{ParticipantPhaseActivated, ParticipantPhaseRetired},
		{ParticipantPhasePreparing, ParticipantPhaseFailed},
		{ParticipantPhasePrepared, ParticipantPhaseAborted},
	} {
		if err := ValidateParticipantTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}
	for _, transition := range [][2]ParticipantPhase{
		{ParticipantPhasePrepared, ParticipantPhaseActivated},
		{ParticipantPhaseActivated, ParticipantPhaseFailed},
		{ParticipantPhaseRetired, ParticipantPhasePreparing},
	} {
		if err := ValidateParticipantTransition(transition[0], transition[1]); err == nil {
			t.Fatalf("transition %q -> %q unexpectedly allowed", transition[0], transition[1])
		}
	}
}
