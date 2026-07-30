package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	defaultWaitTimeout      = 90 * time.Second
	defaultPollInterval     = time.Second
	defaultPreflightTimeout = 75 * time.Second
	maximumWaitTimeout      = 10 * time.Minute
	maximumPollInterval     = 10 * time.Second
	rolloutAbortedCode      = "MODEL_SETTINGS_ROLLOUT_ABORTED"
)

type commandName string

const (
	commandRecover      commandName = "recover"
	commandBegin        commandName = "begin"
	commandPreflight    commandName = "preflight"
	commandDrain        commandName = "drain"
	commandWaitQuiesced commandName = "wait-quiesced"
	commandWaitPrepared commandName = "wait-prepared"
	commandCommit       commandName = "commit"
	commandAbort        commandName = "abort"
)

type command struct {
	name         commandName
	rolloutID    foundation.ID
	role         modeldomain.RuntimeRole
	waitTimeout  time.Duration
	pollInterval time.Duration
}

type rolloutSession interface {
	ID() foundation.ID
	LoadTarget(context.Context) (modeldomain.ResolvedSettings, error)
	RenewValidating(context.Context) error
	StartDraining(context.Context) error
	WaitQuiesced(context.Context, modelapplication.WaitOptions) error
	WaitPrepared(context.Context, modelapplication.WaitOptions) error
	Commit(context.Context) error
	Abort(context.Context, string) error
}

type rolloutCoordinator interface {
	Begin(context.Context) (rolloutSession, error)
	Open(context.Context, foundation.ID) (rolloutSession, error)
	Recover(context.Context) (modeldomain.RolloutState, bool, error)
}

type coordinatorAdapter struct {
	inner *modelapplication.RolloutCoordinator
}

var (
	_ rolloutCoordinator = coordinatorAdapter{}
	_ rolloutSession     = (*modelapplication.RolloutSession)(nil)
)

func newCoordinatorAdapter(inner *modelapplication.RolloutCoordinator) (coordinatorAdapter, error) {
	if inner == nil {
		return coordinatorAdapter{}, stateError(errors.New("model settings rollout coordinator is unavailable"))
	}
	return coordinatorAdapter{inner: inner}, nil
}

func (adapter coordinatorAdapter) Begin(ctx context.Context) (rolloutSession, error) {
	return adapter.inner.Begin(ctx)
}

func (adapter coordinatorAdapter) Open(ctx context.Context, id foundation.ID) (rolloutSession, error) {
	return adapter.inner.Open(ctx, id)
}

func (adapter coordinatorAdapter) Recover(ctx context.Context) (modeldomain.RolloutState, bool, error) {
	return adapter.inner.Recover(ctx)
}

type revisionPreflighter interface {
	Preflight(context.Context, modeldomain.RuntimeRole, modeldomain.ResolvedSettings) error
}

type controller struct {
	coordinator rolloutCoordinator
	preflighter revisionPreflighter
	stdout      io.Writer
}

func parseCommand(arguments []string) (command, error) {
	if len(arguments) == 0 {
		return command{}, usageError(errors.New("modelctl command is required"))
	}
	parsed := command{name: commandName(arguments[0]), waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}
	set := flag.NewFlagSet(string(parsed.name), flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var rolloutID string
	var role string
	var stale bool
	switch parsed.name {
	case commandRecover:
		set.BoolVar(&stale, "stale", false, "recover an expired rollout")
	case commandBegin:
	case commandPreflight:
		set.StringVar(&rolloutID, "rollout-id", "", "fixed rollout identifier")
		set.StringVar(&role, "role", "", "runtime role: api or worker")
	case commandDrain, commandCommit, commandAbort:
		set.StringVar(&rolloutID, "rollout-id", "", "fixed rollout identifier")
	case commandWaitQuiesced, commandWaitPrepared:
		set.StringVar(&rolloutID, "rollout-id", "", "fixed rollout identifier")
		set.DurationVar(&parsed.waitTimeout, "timeout", defaultWaitTimeout, "maximum wait duration")
		set.DurationVar(&parsed.pollInterval, "poll-interval", defaultPollInterval, "poll interval")
	default:
		return command{}, usageError(errors.New("modelctl command is unknown"))
	}
	if err := set.Parse(arguments[1:]); err != nil || set.NArg() != 0 {
		return command{}, usageError(errors.New("modelctl arguments are invalid"))
	}
	if parsed.name == commandRecover {
		if !stale {
			return command{}, usageError(errors.New("recover requires --stale"))
		}
		return parsed, nil
	}
	if parsed.name == commandBegin {
		return parsed, nil
	}
	id, err := foundation.ParseID(rolloutID)
	if err != nil {
		return command{}, usageError(errors.New("rollout id is invalid"))
	}
	parsed.rolloutID = id
	if parsed.name == commandPreflight {
		parsed.role = modeldomain.RuntimeRole(role)
		if !modeldomain.ValidRuntimeRole(parsed.role) {
			return command{}, usageError(errors.New("runtime role is invalid"))
		}
	}
	if parsed.name == commandWaitQuiesced || parsed.name == commandWaitPrepared {
		if parsed.waitTimeout <= 0 || parsed.waitTimeout > maximumWaitTimeout || parsed.pollInterval <= 0 ||
			parsed.pollInterval > maximumPollInterval || parsed.pollInterval > parsed.waitTimeout {
			return command{}, usageError(errors.New("wait bounds are invalid"))
		}
	}
	return parsed, nil
}

func (control *controller) execute(ctx context.Context, command command) error {
	if err := control.ready(ctx); err != nil {
		return err
	}
	switch command.name {
	case commandRecover:
		return control.recover(ctx)
	case commandBegin:
		return control.begin(ctx)
	case commandPreflight:
		return control.preflight(ctx, command)
	case commandDrain:
		return control.withSession(ctx, command.rolloutID, func(session rolloutSession) error {
			return session.StartDraining(ctx)
		})
	case commandWaitQuiesced:
		return control.wait(ctx, command, func(session rolloutSession, options modelapplication.WaitOptions) error {
			return session.WaitQuiesced(ctx, options)
		})
	case commandWaitPrepared:
		return control.wait(ctx, command, func(session rolloutSession, options modelapplication.WaitOptions) error {
			return session.WaitPrepared(ctx, options)
		})
	case commandCommit:
		return control.withSession(ctx, command.rolloutID, func(session rolloutSession) error {
			return session.Commit(ctx)
		})
	case commandAbort:
		return control.withSession(ctx, command.rolloutID, func(session rolloutSession) error {
			return session.Abort(ctx, rolloutAbortedCode)
		})
	default:
		return usageError(errors.New("modelctl command is unknown"))
	}
}

func (control *controller) recover(ctx context.Context) error {
	_, _, err := control.coordinator.Recover(ctx)
	if hasErrorCode(err, modeldomain.ErrorCodeRolloutConflict) {
		return foundation.NewError(
			foundation.ErrorVersionConflict,
			modeldomain.ErrorCodeRolloutInProgress,
			false,
			err,
		)
	}
	return err
}

func (control *controller) begin(ctx context.Context) error {
	session, err := control.coordinator.Begin(ctx)
	if err != nil {
		return err
	}
	if session == nil {
		return stateError(errors.New("rollout begin returned no session"))
	}
	id := session.ID()
	if _, err := foundation.ParseID(string(id)); err != nil {
		return stateError(errors.New("rollout begin returned an invalid identifier"))
	}
	_, err = fmt.Fprintln(control.stdout, id)
	return err
}

func (control *controller) preflight(ctx context.Context, command command) error {
	preflightCtx, cancel := context.WithTimeout(ctx, defaultPreflightTimeout)
	defer cancel()
	session, err := control.open(preflightCtx, command.rolloutID)
	if err != nil {
		return err
	}
	resolved, err := session.LoadTarget(preflightCtx)
	if err != nil {
		return err
	}
	defer resolved.ChatAPIKey.Destroy()
	defer resolved.EmbeddingAPIKey.Destroy()
	if err := control.preflighter.Preflight(preflightCtx, command.role, resolved); err != nil {
		return err
	}
	return session.RenewValidating(preflightCtx)
}

func (control *controller) wait(
	ctx context.Context,
	command command,
	waiter func(rolloutSession, modelapplication.WaitOptions) error,
) error {
	if waiter == nil || command.waitTimeout <= 0 || command.waitTimeout > maximumWaitTimeout || command.pollInterval <= 0 ||
		command.pollInterval > maximumPollInterval || command.pollInterval > command.waitTimeout {
		return usageError(errors.New("wait bounds are invalid"))
	}
	session, err := control.open(ctx, command.rolloutID)
	if err != nil {
		return err
	}
	err = waiter(session, modelapplication.WaitOptions{Timeout: command.waitTimeout, PollInterval: command.pollInterval})
	if hasErrorCode(err, modeldomain.ErrorCodeRolloutWaitTimeout) {
		return waitError(err)
	}
	return err
}

func (control *controller) withSession(ctx context.Context, rolloutID foundation.ID, action func(rolloutSession) error) error {
	if action == nil {
		return stateError(errors.New("modelctl rollout action is unavailable"))
	}
	session, err := control.open(ctx, rolloutID)
	if err != nil {
		return err
	}
	return action(session)
}

func (control *controller) open(ctx context.Context, rolloutID foundation.ID) (rolloutSession, error) {
	session, err := control.coordinator.Open(ctx, rolloutID)
	if err != nil {
		if hasErrorCode(err, modeldomain.ErrorCodeRolloutConflict) {
			return nil, stateError(err)
		}
		return nil, err
	}
	if session == nil || session.ID() != rolloutID {
		return nil, stateError(errors.New("rollout session binding does not match"))
	}
	return session, nil
}

func (control *controller) ready(ctx context.Context) error {
	if ctx == nil || control == nil || control.coordinator == nil || control.preflighter == nil || control.stdout == nil {
		return stateError(errors.New("modelctl controller is unavailable"))
	}
	return nil
}

func hasErrorCode(err error, code string) bool {
	var classified *foundation.Error
	return code != "" && errors.As(err, &classified) && classified.Code == code
}

func usageError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "MODELCTL_USAGE_INVALID", false, cause)
}

func stateError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "MODELCTL_ROLLOUT_STATE_INVALID", false, cause)
}

func waitError(cause error) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "MODELCTL_WAIT_TIMEOUT", true, cause)
}
