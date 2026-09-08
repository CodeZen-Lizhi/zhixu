package postgres

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func sameRollout(state stateRecord, id foundation.ID, phase domain.RolloutPhase) bool {
	return state.rolloutID.Valid && state.rolloutID.String == string(id) && state.phase == string(phase)
}

func activeRolloutPhase(phase domain.RolloutPhase) bool {
	switch phase {
	case domain.RolloutPhaseValidating, domain.RolloutPhaseDraining, domain.RolloutPhaseApplying, domain.RolloutPhaseVerifying:
		return true
	default:
		return false
	}
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validLease(duration time.Duration) bool {
	return duration >= time.Second && duration <= 10*time.Minute && duration%time.Microsecond == 0
}

func canonicalErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
