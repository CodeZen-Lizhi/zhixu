//go:build !unix

package localmodelruntime

import (
	"context"
	"errors"
)

// ExecChildRunner is unavailable outside the Unix container target.
type ExecChildRunner struct{}

// Start fails closed because process-group lifecycle requires Unix signals.
func (ExecChildRunner) Start(context.Context) (Child, error) {
	return nil, errors.New("local model runtime child process requires a Unix platform")
}
