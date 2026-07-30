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

func TestControllerDelegatesRolloutToCoordinatorSessions(t *testing.T) {
	t.Parallel()
	log := []string{}
	session := &fakeRolloutSession{
		id: testRolloutID, log: &log,
		resolved: modeldomain.ResolvedSettings{Revision: 2, Settings: modeldomain.CanonicalDisabledSettings()},
	}
	coordinator := &fakeRolloutCoordinator{log: &log, session: session}
	stdout := &bytes.Buffer{}
	control := testController(coordinator, &fakePreflighter{log: &log}, stdout)
	ctx := context.Background()

	commands := []command{
		{name: commandRecover},
		{name: commandBegin},
		{name: commandPreflight, rolloutID: testRolloutID, role: modeldomain.RuntimeRoleAPI},
		{name: commandDrain, rolloutID: testRolloutID},
		{name: commandWaitQuiesced, rolloutID: testRolloutID, waitTimeout: 7 * time.Second, pollInterval: 250 * time.Millisecond},
		{name: commandWaitPrepared, rolloutID: testRolloutID, waitTimeout: 8 * time.Second, pollInterval: 500 * time.Millisecond},
		{name: commandCommit, rolloutID: testRolloutID},
		{name: commandAbort, rolloutID: testRolloutID},
	}
	for _, command := range commands {
		if err := control.execute(ctx, command); err != nil {
			t.Fatalf("execute %s: %v", command.name, err)
		}
	}
	if got := stdout.String(); got != string(testRolloutID)+"\n" {
		t.Fatalf("begin stdout = %q", got)
	}
	want := []string{
		"recover", "begin",
		"open", "load-target", "preflight:api", "renew-validating",
		"open", "start-draining",
		"open", "wait-quiesced:7s:250ms",
		"open", "wait-prepared:8s:500ms",
		"open", "commit",
		"open", "abort:" + rolloutAbortedCode,
	}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("operation order mismatch:\n got: %#v\nwant: %#v", log, want)
	}
}

func TestControllerPreflightDestroysLoadedSecrets(t *testing.T) {
	t.Parallel()
	chat, err := modeldomain.NewSecret("preflight-chat-secret")
	if err != nil {
		t.Fatal(err)
	}
	embedding, err := modeldomain.NewSecret("preflight-embedding-secret")
	if err != nil {
		t.Fatal(err)
	}
	log := []string{}
	session := &fakeRolloutSession{
		id: testRolloutID, log: &log,
		resolved: modeldomain.ResolvedSettings{
			Revision: 2, Settings: modeldomain.CanonicalDisabledSettings(),
			ChatAPIKey: chat, EmbeddingAPIKey: embedding,
		},
	}
	control := testController(
		&fakeRolloutCoordinator{log: &log, session: session},
		&fakePreflighter{log: &log, err: errors.New("preflight failed")},
		&bytes.Buffer{},
	)
	err = control.execute(context.Background(), command{name: commandPreflight, rolloutID: testRolloutID, role: modeldomain.RuntimeRoleWorker})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	for name, secret := range map[string]modeldomain.Secret{"chat": session.resolved.ChatAPIKey, "embedding": session.resolved.EmbeddingAPIKey} {
		value := secret.Bytes()
		if bytes.Contains(value, []byte("preflight")) || !allZero(value) {
			t.Fatalf("%s secret was not destroyed", name)
		}
	}
}

func TestControllerPreservesStableRecoverAndWaitCodes(t *testing.T) {
	t.Parallel()
	log := []string{}
	conflict := foundation.NewError(
		foundation.ErrorVersionConflict,
		modeldomain.ErrorCodeRolloutConflict,
		false,
		errors.New("active rollout"),
	)
	coordinator := &fakeRolloutCoordinator{log: &log, recoverErr: conflict}
	control := testController(coordinator, &fakePreflighter{log: &log}, &bytes.Buffer{})
	if got := stableErrorCode(control.execute(context.Background(), command{name: commandRecover})); got != modeldomain.ErrorCodeRolloutInProgress {
		t.Fatalf("recover error code = %q", got)
	}

	session := &fakeRolloutSession{
		id: testRolloutID, log: &log,
		waitQuiescedErr: foundation.NewError(
			foundation.ErrorRetryableFailure,
			modeldomain.ErrorCodeRolloutWaitTimeout,
			true,
			errors.New("wait expired"),
		),
	}
	coordinator.session = session
	coordinator.recoverErr = nil
	err := control.execute(context.Background(), command{
		name: commandWaitQuiesced, rolloutID: testRolloutID,
		waitTimeout: 20 * time.Millisecond, pollInterval: time.Millisecond,
	})
	if got := stableErrorCode(err); got != "MODELCTL_WAIT_TIMEOUT" {
		t.Fatalf("wait error code = %q", got)
	}

	coordinator.openErr = conflict
	err = control.execute(context.Background(), command{name: commandDrain, rolloutID: testRolloutID})
	if got := stableErrorCode(err); got != "MODELCTL_ROLLOUT_STATE_INVALID" {
		t.Fatalf("open error code = %q", got)
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

func TestControllerDoesNotOwnRolloutStateMachine(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("control.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"AdvanceRolloutCommand",
		"RenewRolloutCommand",
		"CommitRolloutCommand",
		"FailRolloutCommand",
		"PauseQueue(",
		"ResumeQueue(",
		"ExpectedPhase",
		"FreshWithin",
	} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatalf("control.go contains coordinator-owned operation %q", forbidden)
		}
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

func testController(coordinator rolloutCoordinator, preflighter revisionPreflighter, stdout *bytes.Buffer) *controller {
	return &controller{coordinator: coordinator, preflighter: preflighter, stdout: stdout}
}

func allZero(value []byte) bool {
	for _, character := range value {
		if character != 0 {
			return false
		}
	}
	return true
}

type fakeRolloutCoordinator struct {
	log        *[]string
	session    rolloutSession
	beginErr   error
	openErr    error
	recoverErr error
}

func (coordinator *fakeRolloutCoordinator) Begin(context.Context) (rolloutSession, error) {
	*coordinator.log = append(*coordinator.log, "begin")
	return coordinator.session, coordinator.beginErr
}

func (coordinator *fakeRolloutCoordinator) Open(_ context.Context, id foundation.ID) (rolloutSession, error) {
	*coordinator.log = append(*coordinator.log, "open")
	if id != testRolloutID {
		return nil, stateError(errors.New("unexpected rollout id"))
	}
	return coordinator.session, coordinator.openErr
}

func (coordinator *fakeRolloutCoordinator) Recover(context.Context) (modeldomain.RolloutState, bool, error) {
	*coordinator.log = append(*coordinator.log, "recover")
	return modeldomain.RolloutState{}, false, coordinator.recoverErr
}

type fakeRolloutSession struct {
	id              foundation.ID
	log             *[]string
	resolved        modeldomain.ResolvedSettings
	loadErr         error
	renewErr        error
	drainErr        error
	waitQuiescedErr error
	waitPreparedErr error
	commitErr       error
	abortErr        error
}

func (session *fakeRolloutSession) ID() foundation.ID { return session.id }

func (session *fakeRolloutSession) LoadTarget(context.Context) (modeldomain.ResolvedSettings, error) {
	*session.log = append(*session.log, "load-target")
	return session.resolved, session.loadErr
}

func (session *fakeRolloutSession) RenewValidating(context.Context) error {
	*session.log = append(*session.log, "renew-validating")
	return session.renewErr
}

func (session *fakeRolloutSession) StartDraining(context.Context) error {
	*session.log = append(*session.log, "start-draining")
	return session.drainErr
}

func (session *fakeRolloutSession) WaitQuiesced(_ context.Context, options modelapplication.WaitOptions) error {
	*session.log = append(*session.log, "wait-quiesced:"+options.Timeout.String()+":"+options.PollInterval.String())
	return session.waitQuiescedErr
}

func (session *fakeRolloutSession) WaitPrepared(_ context.Context, options modelapplication.WaitOptions) error {
	*session.log = append(*session.log, "wait-prepared:"+options.Timeout.String()+":"+options.PollInterval.String())
	return session.waitPreparedErr
}

func (session *fakeRolloutSession) Commit(context.Context) error {
	*session.log = append(*session.log, "commit")
	return session.commitErr
}

func (session *fakeRolloutSession) Abort(_ context.Context, errorCode string) error {
	*session.log = append(*session.log, "abort:"+errorCode)
	return session.abortErr
}

type fakePreflighter struct {
	log *[]string
	err error
}

func (preflighter *fakePreflighter) Preflight(_ context.Context, role modeldomain.RuntimeRole, _ modeldomain.ResolvedSettings) error {
	*preflighter.log = append(*preflighter.log, "preflight:"+string(role))
	return preflighter.err
}
