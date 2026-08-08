// Package workspacecontrol owns one-shot host-side Workspace validation, grants,
// and durable runtime switching. It has no HTTP or browser-facing surface.
package workspacecontrol

import (
	"errors"

	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// SwitchCommand identifies one exact host root and its idempotent switch intent.
type SwitchCommand struct {
	Name           string
	RootPath       string
	IdempotencyKey string
	InitializeGit  bool
}

// SwitchOutcome is the authoritative result needed by the one-shot caller.
type SwitchOutcome struct {
	Operation       workspacedomain.SwitchOperation
	Workspace       workspacedomain.Workspace
	GrantGeneration int64
	Changed         bool
}

// Fault is a stable, path-safe Workspace control error.
type Fault struct {
	Code        string
	Message     string
	Retryable   bool
	OperationID string
	cause       error
}

func (fault *Fault) Error() string {
	if fault == nil || fault.Code == "" {
		return "workspace control failed"
	}
	return fault.Code
}

func (fault *Fault) Unwrap() error {
	if fault == nil {
		return nil
	}
	return fault.cause
}

// AsFault returns a stable fault produced by the Workspace control boundary.
func AsFault(err error) (*Fault, bool) {
	var fault *Fault
	ok := errors.As(err, &fault)
	return fault, ok
}
