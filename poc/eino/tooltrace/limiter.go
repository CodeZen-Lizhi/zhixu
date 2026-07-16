package tooltrace

import (
	"errors"
	"sync"
	"time"
)

// Limit defines a fixed-window request quota. A zero value disables limiting.
type Limit struct {
	Requests int
	Window   time.Duration
}

func (l Limit) validate() error {
	if l.Requests < 0 {
		return errors.New("requests cannot be negative")
	}
	if l.Requests == 0 && l.Window == 0 {
		return nil
	}
	if l.Requests <= 0 {
		return errors.New("requests must be positive when rate limiting is enabled")
	}
	if l.Window <= 0 {
		return errors.New("window must be positive when rate limiting is enabled")
	}
	return nil
}

// Clock allows rate-limit tests to advance time without sleeping.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type bucket struct {
	resetAt time.Time
	used    int
}

// Limiter is a deterministic, concurrency-safe fixed-window limiter.
type Limiter struct {
	mu      sync.Mutex
	clock   Clock
	buckets map[string]bucket
}

// NewLimiter creates a fixed-window limiter using the supplied clock.
func NewLimiter(clock Clock) *Limiter {
	if clock == nil {
		clock = systemClock{}
	}
	return &Limiter{clock: clock, buckets: make(map[string]bucket)}
}

// Allow consumes one quota unit. Disabled limits always allow execution.
func (l *Limiter) Allow(key string, limit Limit) bool {
	if limit.Requests == 0 && limit.Window == 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if len(l.buckets) >= 128 {
		for bucketKey, candidate := range l.buckets {
			if !now.Before(candidate.resetAt) {
				delete(l.buckets, bucketKey)
			}
		}
	}
	current, exists := l.buckets[key]
	if !exists || !now.Before(current.resetAt) {
		l.buckets[key] = bucket{resetAt: now.Add(limit.Window), used: 1}
		return true
	}
	if current.used >= limit.Requests {
		return false
	}
	current.used++
	l.buckets[key] = current
	return true
}
