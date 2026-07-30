package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const coordinatorTestRolloutID foundation.ID = "10000000-0000-4000-8000-000000000001"

func TestRolloutCoordinatorOwnsSafePhaseAndQueueOrder(t *testing.T) {
	log := []string{}
	control := &coordinatorTestControl{log: &log, snapshot: domain.Snapshot{
		DesiredRevision: 2, ActiveRevision: 1,
		Rollout: domain.RolloutState{Phase: domain.RolloutPhaseIdle, Version: 1},
	}}
	queue := &coordinatorTestQueue{log: &log}
	coordinator := newTestRolloutCoordinator(t, control, queue)
	ctx := context.Background()

	session, err := coordinator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if session.ID() != coordinatorTestRolloutID {
		t.Fatalf("rollout id=%q", session.ID())
	}
	resolved, err := session.LoadTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved.ChatAPIKey.Destroy()
	resolved.EmbeddingAPIKey.Destroy()
	if err := session.RenewValidating(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.StartDraining(ctx); err != nil {
		t.Fatal(err)
	}
	control.snapshot.Runtime = domain.RuntimeSummaries{
		API:    domain.RuntimeSummary{AppliedRevision: 1, Phase: domain.RuntimePhaseQuiescing, Fresh: true},
		Worker: domain.RuntimeSummary{AppliedRevision: 1, Phase: domain.RuntimePhaseQuiesced, Fresh: true},
	}
	if err := session.WaitQuiesced(ctx, WaitOptions{Timeout: time.Second, PollInterval: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	control.snapshot.Runtime = domain.RuntimeSummaries{
		API:    domain.RuntimeSummary{AppliedRevision: 2, Phase: domain.RuntimePhasePrepared, Fresh: true},
		Worker: domain.RuntimeSummary{AppliedRevision: 2, Phase: domain.RuntimePhasePrepared, Fresh: true},
	}
	if err := session.WaitPrepared(ctx, WaitOptions{Timeout: time.Second, PollInterval: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"begin", "snapshot", "renew:validating", "load", "snapshot", "renew:validating",
		"snapshot", "advance:validating->draining", "pause",
		"snapshot", "renew:draining", "snapshot", "advance:draining->applying",
		"snapshot", "renew:applying", "snapshot", "advance:applying->verifying",
		"snapshot", "renew:verifying", "commit", "resume",
	}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("operation order:\n got: %#v\nwant: %#v", log, want)
	}
	if control.snapshot.ActiveRevision != 2 || control.snapshot.Rollout.Phase != domain.RolloutPhaseIdle {
		t.Fatalf("committed snapshot=%+v", control.snapshot)
	}
}

func TestRolloutSessionStartDrainingRestoresQueueOnFailures(t *testing.T) {
	tests := []struct {
		name       string
		pauseErr   error
		advanceErr error
		wantLog    []string
	}{
		{
			name: "pause", pauseErr: errors.New("pause unavailable"),
			wantLog: []string{"snapshot", "advance:validating->draining", "pause", "fail", "resume"},
		},
		{
			name: "advance", advanceErr: errors.New("advance unavailable"),
			wantLog: []string{"snapshot", "advance:validating->draining"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := []string{}
			control := &coordinatorTestControl{
				log: &log, advanceErr: test.advanceErr,
				snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseValidating),
			}
			queue := &coordinatorTestQueue{log: &log, pauseErr: test.pauseErr}
			coordinator := newTestRolloutCoordinator(t, control, queue)
			session, err := coordinator.Open(context.Background(), coordinatorTestRolloutID)
			if err != nil {
				t.Fatal(err)
			}
			log = log[:0]
			err = session.StartDraining(context.Background())
			if err == nil {
				t.Fatal("expected draining failure")
			}
			if !reflect.DeepEqual(log, test.wantLog) {
				t.Fatalf("operation order=%#v want=%#v", log, test.wantLog)
			}
			wantPhase := domain.RolloutPhaseValidating
			if test.pauseErr != nil {
				wantPhase = domain.RolloutPhaseFailed
			}
			if control.snapshot.Rollout.Phase != wantPhase {
				t.Fatalf("phase=%q want=%q", control.snapshot.Rollout.Phase, wantPhase)
			}
		})
	}
}

func TestRolloutSessionCommitReportsDurableCommitWhenQueueResumeFails(t *testing.T) {
	log := []string{}
	control := &coordinatorTestControl{log: &log, snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseVerifying)}
	queue := &coordinatorTestQueue{log: &log, resumeErr: errors.New("queue resume unavailable")}
	coordinator := newTestRolloutCoordinator(t, control, queue)
	session, err := coordinator.Open(context.Background(), coordinatorTestRolloutID)
	if err != nil {
		t.Fatal(err)
	}
	log = log[:0]

	err = session.Commit(context.Background())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeCommittedQueuePaused || !classified.Retryable {
		t.Fatalf("commit error=%v", err)
	}
	if control.snapshot.ActiveRevision != 2 || control.snapshot.Rollout.Phase != domain.RolloutPhaseIdle {
		t.Fatalf("durable commit was not preserved: %+v", control.snapshot)
	}
	want := []string{"snapshot", "renew:verifying", "commit", "resume"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("operations=%#v want=%#v", log, want)
	}
}

func TestRolloutSessionStartDrainingClosesFenceBeforeQueuePause(t *testing.T) {
	log := []string{}
	control := &coordinatorTestControl{log: &log, snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseValidating)}
	queue := &coordinatorTestQueue{
		log: &log,
		beforePause: func() error {
			if control.snapshot.Rollout.Phase != domain.RolloutPhaseDraining {
				return fmt.Errorf("queue pause observed phase %q", control.snapshot.Rollout.Phase)
			}
			return nil
		},
	}
	coordinator := newTestRolloutCoordinator(t, control, queue)
	session, err := coordinator.Open(context.Background(), coordinatorTestRolloutID)
	if err != nil {
		t.Fatal(err)
	}
	log = log[:0]

	if err := session.StartDraining(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"snapshot", "advance:validating->draining", "pause"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("operation order=%#v want=%#v", log, want)
	}
}

func TestRolloutSessionWaitRequiresFreshMatchingRuntimes(t *testing.T) {
	log := []string{}
	control := &coordinatorTestControl{log: &log, snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseApplying)}
	control.snapshot.Runtime = domain.RuntimeSummaries{
		API:    domain.RuntimeSummary{AppliedRevision: 2, Phase: domain.RuntimePhasePrepared, Fresh: true},
		Worker: domain.RuntimeSummary{AppliedRevision: 2, Phase: domain.RuntimePhasePrepared, Fresh: false},
	}
	coordinator := newTestRolloutCoordinator(t, control, &coordinatorTestQueue{log: &log})
	session, err := coordinator.Open(context.Background(), coordinatorTestRolloutID)
	if err != nil {
		t.Fatal(err)
	}
	err = session.WaitPrepared(context.Background(), WaitOptions{Timeout: 15 * time.Millisecond, PollInterval: time.Millisecond})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeRolloutWaitTimeout || !classified.Retryable {
		t.Fatalf("wait error=%v", err)
	}
	if control.snapshot.Rollout.Phase != domain.RolloutPhaseApplying {
		t.Fatalf("timed out wait advanced to %q", control.snapshot.Rollout.Phase)
	}
}

func TestRolloutSessionWaitRenewsLeaseAndRejectsSlowerPolling(t *testing.T) {
	log := []string{}
	control := &coordinatorTestControl{log: &log, snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseApplying)}
	queue := &coordinatorTestQueue{log: &log}
	clock := &coordinatorSteppingClock{now: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	coordinator, err := NewRolloutCoordinator(control, queue, coordinatorTestIDs{id: coordinatorTestRolloutID}, RolloutCoordinatorOptions{
		LeaseDuration: 10 * time.Second, RuntimeFreshWithin: 5 * time.Second,
		RenewEvery: time.Second, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := coordinator.Open(context.Background(), coordinatorTestRolloutID)
	if err != nil {
		t.Fatal(err)
	}
	log = log[:0]
	err = session.WaitPrepared(context.Background(), WaitOptions{Timeout: 12 * time.Millisecond, PollInterval: time.Millisecond})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeRolloutWaitTimeout {
		t.Fatalf("wait error=%v", err)
	}
	renewals := 0
	for _, operation := range log {
		if operation == "renew:applying" {
			renewals++
		}
	}
	if renewals < 2 {
		t.Fatalf("lease renewals=%d operations=%#v", renewals, log)
	}

	err = session.WaitPrepared(context.Background(), WaitOptions{Timeout: 2 * time.Second, PollInterval: 1500 * time.Millisecond})
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeInvalid {
		t.Fatalf("slow polling error=%v", err)
	}
}

func TestRolloutCoordinatorRecoverOnlyResumesTerminalState(t *testing.T) {
	tests := []struct {
		name       string
		phase      domain.RolloutPhase
		recovered  bool
		wantResume bool
		wantCode   string
	}{
		{name: "idle", phase: domain.RolloutPhaseIdle, wantResume: true},
		{name: "failed", phase: domain.RolloutPhaseFailed, wantResume: true},
		{name: "recovered", phase: domain.RolloutPhaseFailed, recovered: true, wantResume: true},
		{name: "active", phase: domain.RolloutPhaseDraining, wantCode: domain.ErrorCodeRolloutConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := []string{}
			control := &coordinatorTestControl{log: &log, recovered: test.recovered, snapshot: rolloutCoordinatorSnapshot(test.phase)}
			if test.phase == domain.RolloutPhaseIdle {
				control.snapshot.Rollout = domain.RolloutState{Phase: domain.RolloutPhaseIdle, Version: 4}
			}
			coordinator := newTestRolloutCoordinator(t, control, &coordinatorTestQueue{log: &log})
			_, _, err := coordinator.Recover(context.Background())
			var classified *foundation.Error
			if test.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantCode != "" && (!errors.As(err, &classified) || classified.Code != test.wantCode) {
				t.Fatalf("recover error=%v", err)
			}
			want := []string{"recover"}
			if test.wantResume {
				want = append(want, "resume")
			}
			if !reflect.DeepEqual(log, want) {
				t.Fatalf("operations=%#v want=%#v", log, want)
			}
		})
	}
}

func TestRolloutCoordinatorOpenRejectsChangedBinding(t *testing.T) {
	log := []string{}
	control := &coordinatorTestControl{log: &log, snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseDraining)}
	coordinator := newTestRolloutCoordinator(t, control, &coordinatorTestQueue{log: &log})
	other := foundation.ID("20000000-0000-4000-8000-000000000002")
	_, err := coordinator.Open(context.Background(), other)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeRolloutConflict {
		t.Fatalf("open error=%v", err)
	}
}

func TestRolloutCoordinatorAndSessionFormattingDoNotExpandDependencies(t *testing.T) {
	const canary = "coordinator-format-secret-canary"
	log := []string{}
	control := &coordinatorTestControl{
		log: &log, advanceErr: errors.New(canary),
		snapshot: rolloutCoordinatorSnapshot(domain.RolloutPhaseValidating),
	}
	queue := &coordinatorTestQueue{log: &log, pauseErr: errors.New(canary)}
	coordinator := newTestRolloutCoordinator(t, control, queue)
	session, err := coordinator.Open(context.Background(), coordinatorTestRolloutID)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%+v %#v %+v %#v", *coordinator, *coordinator, *session, *session)
	if strings.Contains(formatted, canary) || strings.Contains(formatted, string(coordinatorTestRolloutID)) {
		t.Fatalf("rollout formatting expanded private dependencies: %q", formatted)
	}
}

func newTestRolloutCoordinator(t *testing.T, control *coordinatorTestControl, queue *coordinatorTestQueue) *RolloutCoordinator {
	t.Helper()
	coordinator, err := NewRolloutCoordinator(control, queue, coordinatorTestIDs{id: coordinatorTestRolloutID}, RolloutCoordinatorOptions{
		LeaseDuration: 10 * time.Second, RuntimeFreshWithin: 5 * time.Second,
		RenewEvery: time.Second, Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func rolloutCoordinatorSnapshot(phase domain.RolloutPhase) domain.Snapshot {
	return domain.Snapshot{
		DesiredRevision: 2, ActiveRevision: 1,
		Rollout: domain.RolloutState{
			ID: coordinatorTestRolloutID, TargetRevision: 2, PreviousActiveRevision: 1,
			Phase: phase, LeaseExpiresAt: time.Now().Add(time.Minute), Version: 2,
		},
	}
}

type coordinatorTestIDs struct {
	id  foundation.ID
	err error
}

func (ids coordinatorTestIDs) New() (foundation.ID, error) { return ids.id, ids.err }

type coordinatorSteppingClock struct{ now time.Time }

func (clock *coordinatorSteppingClock) Now() time.Time {
	now := clock.now
	clock.now = clock.now.Add(time.Second)
	return now
}

type coordinatorTestQueue struct {
	log         *[]string
	pauseErr    error
	resumeErr   error
	beforePause func() error
}

func (queue *coordinatorTestQueue) PauseQueue(context.Context) error {
	*queue.log = append(*queue.log, "pause")
	if queue.beforePause != nil {
		if err := queue.beforePause(); err != nil {
			return err
		}
	}
	return queue.pauseErr
}

func (queue *coordinatorTestQueue) ResumeQueue(context.Context) error {
	*queue.log = append(*queue.log, "resume")
	return queue.resumeErr
}

type coordinatorTestControl struct {
	log        *[]string
	snapshot   domain.Snapshot
	resolved   domain.ResolvedSettings
	recovered  bool
	advanceErr error
}

func (control *coordinatorTestControl) Snapshot(context.Context) (domain.Snapshot, error) {
	*control.log = append(*control.log, "snapshot")
	return control.snapshot, nil
}

func (control *coordinatorTestControl) LoadRevision(_ context.Context, revision int64) (domain.ResolvedSettings, error) {
	*control.log = append(*control.log, "load")
	if control.resolved.Revision == 0 {
		control.resolved = domain.ResolvedSettings{Revision: revision, Settings: domain.CanonicalDisabledSettings()}
	}
	return control.resolved, nil
}

func (control *coordinatorTestControl) BeginRollout(_ context.Context, command BeginRolloutCommand) (domain.RolloutState, error) {
	*control.log = append(*control.log, "begin")
	control.snapshot.Rollout = domain.RolloutState{
		ID: command.RolloutID, TargetRevision: control.snapshot.DesiredRevision,
		PreviousActiveRevision: control.snapshot.ActiveRevision,
		Phase:                  domain.RolloutPhaseValidating, LeaseExpiresAt: time.Now().Add(command.LeaseDuration), Version: 2,
	}
	return control.snapshot.Rollout, nil
}

func (control *coordinatorTestControl) RenewRollout(_ context.Context, command RenewRolloutCommand) (domain.RolloutState, error) {
	*control.log = append(*control.log, "renew:"+string(command.ExpectedPhase))
	return control.snapshot.Rollout, nil
}

func (control *coordinatorTestControl) AdvanceRollout(_ context.Context, command AdvanceRolloutCommand) (domain.RolloutState, error) {
	*control.log = append(*control.log, "advance:"+string(command.ExpectedPhase)+"->"+string(command.NextPhase))
	if control.advanceErr != nil {
		return domain.RolloutState{}, control.advanceErr
	}
	control.snapshot.Rollout.Phase = command.NextPhase
	return control.snapshot.Rollout, nil
}

func (control *coordinatorTestControl) FailRollout(_ context.Context, command FailRolloutCommand) (domain.RolloutState, error) {
	*control.log = append(*control.log, "fail")
	control.snapshot.Rollout.Phase = domain.RolloutPhaseFailed
	control.snapshot.Rollout.LastErrorCode = command.ErrorCode
	return control.snapshot.Rollout, nil
}

func (control *coordinatorTestControl) CommitRollout(context.Context, CommitRolloutCommand) (domain.RolloutState, error) {
	*control.log = append(*control.log, "commit")
	control.snapshot.ActiveRevision = control.snapshot.Rollout.TargetRevision
	control.snapshot.Rollout = domain.RolloutState{Phase: domain.RolloutPhaseIdle, Version: 3}
	return control.snapshot.Rollout, nil
}

func (control *coordinatorTestControl) RecoverExpiredRollout(context.Context) (domain.RolloutState, bool, error) {
	*control.log = append(*control.log, "recover")
	return control.snapshot.Rollout, control.recovered, nil
}
