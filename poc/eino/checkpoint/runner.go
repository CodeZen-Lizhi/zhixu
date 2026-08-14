package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	graphName        = "zhixu_checkpoint_interrupt_poc"
	approvalNodeName = "approval_gate"
)

var (
	// ErrCheckpointExists prevents a second start from replacing an active checkpoint.
	ErrCheckpointExists = errors.New("an active checkpoint already exists for this Attempt")
	// ErrCheckpointUnavailable means the scope expired or belongs to another Attempt/Fence.
	ErrCheckpointUnavailable = errors.New("checkpoint is unavailable for this Attempt")
	// ErrApprovalRejected means resume data did not approve the interrupted operation.
	ErrApprovalRejected = errors.New("checkpoint approval was rejected")
	// ErrUnexpectedInterrupt means Eino did not expose one resumable interrupt point.
	ErrUnexpectedInterrupt = errors.New("checkpoint graph returned an unexpected interrupt")
)

type interruptState struct {
	OriginalInput string
}

// Approval is the explicit resume payload supplied by the caller.
type Approval struct {
	Approved bool
}

// Interrupt identifies the Eino resume point and its opaque storage key.
type Interrupt struct {
	ID           string
	CheckpointID string
}

// Runner compiles one fixed interruptible graph for same-process PoC use.
type Runner struct {
	keyer    *Keyer
	store    *Store
	runnable compose.Runnable[string, string]
}

func init() {
	schema.RegisterName[*interruptState]("zhixu_checkpoint_interrupt_state_v1")
}

// NewRunner compiles the fixed same-Attempt interrupt/resume graph.
func NewRunner(keyer *Keyer, store *Store) (*Runner, error) {
	if keyer == nil || store == nil {
		return nil, ErrInvalidStoreConfig
	}
	graph := compose.NewGraph[string, string]()
	if err := graph.AddLambdaNode(approvalNodeName, approvalNode()); err != nil {
		return nil, fmt.Errorf("add checkpoint approval node: %w", err)
	}
	if err := graph.AddEdge(compose.START, approvalNodeName); err != nil {
		return nil, fmt.Errorf("connect checkpoint start: %w", err)
	}
	if err := graph.AddEdge(approvalNodeName, compose.END); err != nil {
		return nil, fmt.Errorf("connect checkpoint end: %w", err)
	}
	runnable, err := graph.Compile(context.Background(), compose.WithGraphName(graphName), compose.WithCheckPointStore(store))
	if err != nil {
		return nil, fmt.Errorf("compile checkpoint graph: %w", err)
	}
	return &Runner{keyer: keyer, store: store, runnable: runnable}, nil
}

// Start runs until the approval node creates a stateful interrupt.
func (runner *Runner) Start(ctx context.Context, scope AttemptScope, input string) (Interrupt, error) {
	if runner == nil || runner.keyer == nil || runner.store == nil || runner.store.lifecycle == nil || runner.runnable == nil {
		return Interrupt{}, ErrInvalidStoreConfig
	}
	if ctx == nil {
		return Interrupt{}, ErrContextRequired
	}
	if input == "" || len(input) > maxCheckpointBytes {
		return Interrupt{}, ErrCheckpointTooLarge
	}
	checkpointID, err := runner.keyer.ID(scope)
	if err != nil {
		return Interrupt{}, err
	}
	if err := runner.acquire(ctx); err != nil {
		return Interrupt{}, err
	}
	defer runner.release()
	if exists, err := runner.store.Exists(ctx, checkpointID); err != nil {
		return Interrupt{}, err
	} else if exists {
		return Interrupt{}, ErrCheckpointExists
	}

	_, err = runner.runnable.Invoke(ctx, input, compose.WithCheckPointID(checkpointID))
	info, interrupted := compose.ExtractInterruptInfo(err)
	if !interrupted || info == nil || len(info.InterruptContexts) != 1 || strings.TrimSpace(info.InterruptContexts[0].ID) == "" {
		if err != nil {
			return Interrupt{}, fmt.Errorf("invoke checkpoint graph: %w", err)
		}
		return Interrupt{}, ErrUnexpectedInterrupt
	}
	return Interrupt{ID: info.InterruptContexts[0].ID, CheckpointID: checkpointID}, nil
}

// Resume continues the same graph under the same Attempt/Fence and deletes its checkpoint on success.
func (runner *Runner) Resume(ctx context.Context, scope AttemptScope, interruptID string, approval Approval) (string, error) {
	if runner == nil || runner.keyer == nil || runner.store == nil || runner.store.lifecycle == nil || runner.runnable == nil {
		return "", ErrInvalidStoreConfig
	}
	if ctx == nil {
		return "", ErrContextRequired
	}
	if strings.TrimSpace(interruptID) == "" || len(interruptID) > maxScopeFieldBytes {
		return "", ErrUnexpectedInterrupt
	}
	checkpointID, err := runner.keyer.ID(scope)
	if err != nil {
		return "", err
	}
	if err := runner.acquire(ctx); err != nil {
		return "", err
	}
	defer runner.release()
	if exists, err := runner.store.Exists(ctx, checkpointID); err != nil {
		return "", err
	} else if !exists {
		return "", ErrCheckpointUnavailable
	}
	if !approval.Approved {
		return "", ErrApprovalRejected
	}

	resumeCtx := compose.ResumeWithData(ctx, interruptID, &approval)
	output, err := runner.runnable.Invoke(resumeCtx, "", compose.WithCheckPointID(checkpointID))
	if err != nil {
		return "", fmt.Errorf("resume checkpoint graph: %w", err)
	}
	if err := runner.store.Delete(context.WithoutCancel(ctx), checkpointID); err != nil {
		return "", fmt.Errorf("delete completed checkpoint: %w", err)
	}
	return output, nil
}

func (runner *Runner) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case runner.store.lifecycle <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (runner *Runner) release() {
	<-runner.store.lifecycle
}

func approvalNode() *compose.Lambda {
	return compose.InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, hasState, state := compose.GetInterruptState[*interruptState](ctx)
		if !wasInterrupted {
			return "", compose.StatefulInterrupt(ctx, "approval required", &interruptState{OriginalInput: input})
		}
		if !hasState || state == nil || state.OriginalInput == "" {
			return "", ErrCheckpointCorrupt
		}
		isResume, hasData, approval := compose.GetResumeContext[*Approval](ctx)
		if !isResume || !hasData || approval == nil {
			return "", ErrUnexpectedInterrupt
		}
		if !approval.Approved {
			return "", ErrApprovalRejected
		}
		return state.OriginalInput, nil
	})
}
