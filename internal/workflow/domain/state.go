package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ValidateTransition rejects lifecycle transitions not declared by the workflow contract.
func ValidateTransition(from, to Status) error {
	allowed := map[Status]map[Status]bool{
		StatusPending:         {StatusRunning: true, StatusCancelled: true},
		StatusRunning:         {StatusWaitingForHuman: true, StatusSucceeded: true, StatusFailed: true, StatusCancelled: true},
		StatusWaitingForHuman: {StatusRunning: true, StatusCancelled: true},
	}
	if !allowed[from][to] {
		return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_INVALID_STATE_TRANSITION", false, errors.New("workflow state transition is not allowed"))
	}
	return nil
}
