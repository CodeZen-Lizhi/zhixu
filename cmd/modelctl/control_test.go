package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const testRolloutID foundation.ID = "10000000-0000-4000-8000-000000000001"

func TestParseCommandAcceptsApprovedContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		arguments []string
		want      command
	}{
		{name: "recover", arguments: []string{"recover", "--stale"}, want: command{name: commandRecover, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "begin", arguments: []string{"begin"}, want: command{name: commandBegin, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "preflight api", arguments: []string{"preflight", "--role", "api", "--rollout-id", string(testRolloutID)}, want: command{name: commandPreflight, rolloutID: testRolloutID, role: modeldomain.RuntimeRoleAPI, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "preflight worker", arguments: []string{"preflight", "--rollout-id", string(testRolloutID), "--role", "worker"}, want: command{name: commandPreflight, rolloutID: testRolloutID, role: modeldomain.RuntimeRoleWorker, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "drain", arguments: []string{"drain", "--rollout-id", string(testRolloutID)}, want: command{name: commandDrain, rolloutID: testRolloutID, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "wait quiesced", arguments: []string{"wait-quiesced", "--rollout-id", string(testRolloutID), "--timeout", "7s", "--poll-interval", "250ms"}, want: command{name: commandWaitQuiesced, rolloutID: testRolloutID, waitTimeout: 7 * time.Second, pollInterval: 250 * time.Millisecond}},
		{name: "wait prepared", arguments: []string{"wait-prepared", "--rollout-id", string(testRolloutID)}, want: command{name: commandWaitPrepared, rolloutID: testRolloutID, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "commit", arguments: []string{"commit", "--rollout-id", string(testRolloutID)}, want: command{name: commandCommit, rolloutID: testRolloutID, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
		{name: "abort", arguments: []string{"abort", "--rollout-id", string(testRolloutID)}, want: command{name: commandAbort, rolloutID: testRolloutID, waitTimeout: defaultWaitTimeout, pollInterval: defaultPollInterval}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseCommand(test.arguments)
			if err != nil {
				t.Fatalf("parse command: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command mismatch:\n got: %#v\nwant: %#v", got, test.want)
			}
		})
	}
}

func TestParseCommandRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		nil,
		{"status"},
		{"recover"},
		{"recover", "--stale", "extra"},
		{"begin", "--rollout-id", string(testRolloutID)},
		{"preflight", "--role", "api"},
		{"preflight", "--role", "scheduler", "--rollout-id", string(testRolloutID)},
		{"drain", "--rollout-id", "not-a-uuid"},
		{"wait-quiesced", "--rollout-id", string(testRolloutID), "--timeout", "0s"},
		{"wait-quiesced", "--rollout-id", string(testRolloutID), "--timeout", "11m"},
		{"wait-quiesced", "--rollout-id", string(testRolloutID), "--timeout", "1m", "--poll-interval", "11s"},
		{"wait-prepared", "--rollout-id", string(testRolloutID), "--timeout", "1s", "--poll-interval", "2s"},
		{"commit"},
	}
	for _, arguments := range tests {
		if _, err := parseCommand(arguments); stableErrorCode(err) != "MODELCTL_USAGE_INVALID" {
			t.Fatalf("parseCommand(%q) error code = %q, want MODELCTL_USAGE_INVALID", arguments, stableErrorCode(err))
		}
	}
}

func TestControllerRecoverUsesHotActivationBoundary(t *testing.T) {
	t.Parallel()
	recovery := &fakeActivationRecovery{
		result: modeldomain.ActivationRecovery{
			State:  modeldomain.RolloutState{ID: testRolloutID, Phase: modeldomain.RolloutPhaseActivating, Version: 8},
			Action: modeldomain.ActivationRecoveryAdoptedPostCommit,
		},
	}
	control := testController(recovery)
	if err := control.execute(context.Background(), command{name: commandRecover}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovery.calls != 1 {
		t.Fatalf("RecoverActivation calls = %d, want 1", recovery.calls)
	}
	want := modelapplication.RecoverActivationCommand{LeaseDuration: activationRecoveryLease}
	if recovery.command != want {
		t.Fatalf("recovery command = %#v, want %#v", recovery.command, want)
	}
}

func TestControllerRejectsLegacyRolloutCommandsWithoutMutation(t *testing.T) {
	t.Parallel()
	recovery := &fakeActivationRecovery{}
	control := testController(recovery)
	commands := []command{
		{name: commandBegin},
		{name: commandPreflight, rolloutID: testRolloutID, role: modeldomain.RuntimeRoleAPI},
		{name: commandDrain, rolloutID: testRolloutID},
		{name: commandWaitQuiesced, rolloutID: testRolloutID, waitTimeout: time.Second, pollInterval: time.Millisecond},
		{name: commandWaitPrepared, rolloutID: testRolloutID, waitTimeout: time.Second, pollInterval: time.Millisecond},
		{name: commandCommit, rolloutID: testRolloutID},
		{name: commandAbort, rolloutID: testRolloutID},
	}
	for _, command := range commands {
		if got := stableErrorCode(control.execute(context.Background(), command)); got != legacyRolloutErrorCode {
			t.Fatalf("execute %s error code = %q, want %q", command.name, got, legacyRolloutErrorCode)
		}
	}
	if recovery.calls != 0 {
		t.Fatalf("legacy commands invoked activation recovery %d times", recovery.calls)
	}
}

func TestRunCommandRejectsLegacyRolloutBeforeLoadingProcessDependencies(t *testing.T) {
	t.Setenv("ZHIXU_MODEL_SETTINGS_MODE", "")
	if got := stableErrorCode(runCommand(context.Background(), []string{"begin"}, &bytes.Buffer{})); got != legacyRolloutErrorCode {
		t.Fatalf("runCommand legacy error code = %q, want %q", got, legacyRolloutErrorCode)
	}
}

func TestControllerPreservesActivationRecoveryError(t *testing.T) {
	t.Parallel()
	want := foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		modeldomain.ErrorCodeUnavailable,
		true,
		errors.New("database unavailable"),
	)
	recovery := &fakeActivationRecovery{err: want}
	err := testController(recovery).execute(context.Background(), command{name: commandRecover})
	if !errors.Is(err, want) || stableErrorCode(err) != modeldomain.ErrorCodeUnavailable {
		t.Fatalf("recover error = %v", err)
	}
}

func TestControllerRequiresRecoveryBoundary(t *testing.T) {
	t.Parallel()
	for _, control := range []*controller{nil, {stdout: &bytes.Buffer{}}, {activations: &fakeActivationRecovery{}}} {
		if got := stableErrorCode(control.execute(context.Background(), command{name: commandRecover})); got != "MODELCTL_ROLLOUT_STATE_INVALID" {
			t.Fatalf("controller error code = %q", got)
		}
	}
	control := testController(&fakeActivationRecovery{})
	if got := stableErrorCode(control.execute(nil, command{name: commandRecover})); got != "MODELCTL_ROLLOUT_STATE_INVALID" {
		t.Fatalf("nil context error code = %q", got)
	}
}

func TestModelctlDoesNotReferenceLegacyRolloutWriter(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"control.go", "main.go"} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"NewRolloutCoordinator",
			"RolloutSession",
			"BeginRollout(",
			"RenewValidating(",
			"StartDraining(",
			"WaitQuiesced(",
			"WaitPrepared(",
			"CommitRollout(",
			"FailRollout(",
		} {
			if bytes.Contains(source, []byte(forbidden)) {
				t.Fatalf("%s contains legacy rollout writer %q", file, forbidden)
			}
		}
	}
}

func TestModelctlExitCodeDistinguishesCommittedQueuePause(t *testing.T) {
	err := foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		modeldomain.ErrorCodeCommittedQueuePaused,
		true,
		errors.New("queue resume unavailable"),
	)
	if got := modelctlExitCode(err); got != committedQueuePausedExitCode {
		t.Fatalf("exit code=%d", got)
	}
	if got := modelctlExitCode(errors.New("ordinary failure")); got != 1 {
		t.Fatalf("ordinary exit code=%d", got)
	}
}

func TestStableErrorCodeDoesNotExposeCause(t *testing.T) {
	t.Parallel()
	secret := "do-not-print-this-secret"
	err := foundation.NewError(foundation.ErrorDependencyUnavailable, "MODEL_TEST_FAILED", false, errors.New(secret))
	if got := stableErrorCode(err); got != "MODEL_TEST_FAILED" || strings.Contains(got, secret) {
		t.Fatalf("stable error code = %q", got)
	}
	if got := stableErrorCode(errors.New(secret)); got != "MODELCTL_FAILED" {
		t.Fatalf("unclassified error code = %q", got)
	}
}

func testController(recovery activationRecovery) *controller {
	return &controller{activations: recovery, stdout: &bytes.Buffer{}}
}

type fakeActivationRecovery struct {
	calls   int
	command modelapplication.RecoverActivationCommand
	result  modeldomain.ActivationRecovery
	err     error
}

func (recovery *fakeActivationRecovery) RecoverActivation(
	_ context.Context,
	command modelapplication.RecoverActivationCommand,
) (modeldomain.ActivationRecovery, error) {
	recovery.calls++
	recovery.command = command
	return recovery.result, recovery.err
}
