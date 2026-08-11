package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	defaultWaitTimeout      = 90 * time.Second
	defaultPollInterval     = time.Second
	maximumWaitTimeout      = 10 * time.Minute
	maximumPollInterval     = 10 * time.Second
	activationRecoveryLease = 2 * time.Minute
	legacyRolloutErrorCode  = "MODELCTL_LEGACY_ROLLOUT_UNSUPPORTED"
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

type activationRecovery interface {
	RecoverActivation(context.Context, modelapplication.RecoverActivationCommand) (modeldomain.ActivationRecovery, error)
}

type controller struct {
	activations activationRecovery
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
	if isLegacyRolloutCommand(command.name) {
		return legacyRolloutError()
	}
	switch command.name {
	case commandRecover:
		return control.recover(ctx)
	default:
		return usageError(errors.New("modelctl command is unknown"))
	}
}

func isLegacyRolloutCommand(name commandName) bool {
	switch name {
	case commandBegin, commandPreflight, commandDrain, commandWaitQuiesced, commandWaitPrepared, commandCommit, commandAbort:
		return true
	default:
		return false
	}
}

func (control *controller) recover(ctx context.Context) error {
	_, err := control.activations.RecoverActivation(ctx, modelapplication.RecoverActivationCommand{
		LeaseDuration: activationRecoveryLease,
	})
	return err
}

func (control *controller) ready(ctx context.Context) error {
	if ctx == nil || control == nil || control.activations == nil || control.stdout == nil {
		return stateError(errors.New("modelctl controller is unavailable"))
	}
	return nil
}

func usageError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "MODELCTL_USAGE_INVALID", false, cause)
}

func stateError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "MODELCTL_ROLLOUT_STATE_INVALID", false, cause)
}

func legacyRolloutError() error {
	return foundation.NewError(
		foundation.ErrorInvalidInput,
		legacyRolloutErrorCode,
		false,
		errors.New("legacy model settings rollout commands are disabled; use Settings Apply or recover --stale"),
	)
}
