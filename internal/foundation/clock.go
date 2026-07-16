// Package foundation contains the small shared kernel used across modules.
package foundation

import "time"

// Clock supplies UTC timestamps at application boundaries.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the process wall clock.
type SystemClock struct{}

// Now returns the current time normalized to UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is a deterministic Clock for tests and replayable operations.
type FixedClock struct{ Value time.Time }

// Now returns the configured instant normalized to UTC.
func (c FixedClock) Now() time.Time { return c.Value.UTC() }
