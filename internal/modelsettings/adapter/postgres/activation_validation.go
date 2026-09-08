package postgres

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const localPreparationLeaseDuration = 30 * time.Second

func sameActivation(state stateRecord, id foundation.ID, phase domain.RolloutPhase, version int64) bool {
	return state.rolloutID.Valid && state.rolloutID.String == string(id) && state.phase == string(phase) && state.version == version
}

func validFreshWithin(duration time.Duration) bool {
	return duration >= time.Second && duration <= 5*time.Minute && duration%time.Microsecond == 0
}

func deterministicLifecycleID(prefix string, rolloutID foundation.ID, revision int64) (foundation.ID, error) {
	digest := sha256.Sum256([]byte(prefix + "\x00" + string(rolloutID) + "\x00" + strconv.FormatInt(revision, 10)))
	var raw [16]byte
	copy(raw[:], digest[:16])
	raw[6] = (raw[6] & 0x0f) | 0x50 // UUID v5-shaped deterministic identifier.
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	return foundation.ParseID(value)
}
