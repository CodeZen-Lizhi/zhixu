// Package operability owns shared Worker/River tuning invariants used by
// configuration loading and transport construction.
package operability

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	maximumQueueLength  = 64
	maximumQueueWorkers = 10_000
)

var queueNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:[_-]?[a-z0-9]+)*$`)

// ValidateRiverOptions validates the shared producer/consumer queue and River
// lifecycle tuning contract.
func ValidateRiverOptions(queue string, maxWorkers int, jobTimeout, rescueStuckJobsAfter, softStopTimeout time.Duration) error {
	if queue == "" {
		return errors.New("worker_queue must not be empty")
	}
	if len(queue) > maximumQueueLength {
		return fmt.Errorf("worker_queue must not exceed %d bytes", maximumQueueLength)
	}
	if !queueNamePattern.MatchString(queue) {
		return errors.New("worker_queue must contain lowercase letters and numbers separated by underscores or hyphens")
	}
	if maxWorkers < 1 || maxWorkers > maximumQueueWorkers {
		return fmt.Errorf("worker_max_workers must be between 1 and %d", maximumQueueWorkers)
	}
	if jobTimeout <= 0 {
		return errors.New("worker_job_timeout must be positive")
	}
	if rescueStuckJobsAfter <= jobTimeout {
		return errors.New("worker_rescue_stuck_jobs_after must be greater than worker_job_timeout")
	}
	if softStopTimeout <= 0 {
		return errors.New("worker_soft_stop_timeout must be positive")
	}
	return nil
}
