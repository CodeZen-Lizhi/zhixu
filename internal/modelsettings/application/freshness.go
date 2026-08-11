package application

import "time"

// DefaultRuntimeFreshWithin is the server-owned freshness window shared by
// managed runtime ownership, activation, and Workflow Claim admission.
const DefaultRuntimeFreshWithin = 20 * time.Second

// ValidRuntimeFreshWithin reports whether a database freshness comparison can
// safely use the supplied duration.
func ValidRuntimeFreshWithin(value time.Duration) bool {
	return value >= time.Second && value <= 5*time.Minute && value%time.Microsecond == 0
}
