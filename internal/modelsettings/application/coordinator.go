package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	defaultRolloutLeaseDuration = 2 * time.Minute
	defaultRolloutRenewEvery    = 30 * time.Second
	maximumRolloutWait          = 10 * time.Minute
	maximumRolloutPollInterval  = 10 * time.Second
)

// RolloutControl is the narrow state-machine boundary consumed by the coordinator.
type RolloutControl interface {
	Snapshot(context.Context) (domain.Snapshot, error)
	LoadRevision(context.Context, int64) (domain.ResolvedSettings, error)
	BeginRollout(context.Context, BeginRolloutCommand) (domain.RolloutState, error)
	RenewRollout(context.Context, RenewRolloutCommand) (domain.RolloutState, error)
	AdvanceRollout(context.Context, AdvanceRolloutCommand) (domain.RolloutState, error)
	FailRollout(context.Context, FailRolloutCommand) (domain.RolloutState, error)
	CommitRollout(context.Context, CommitRolloutCommand) (domain.RolloutState, error)
	RecoverExpiredRollout(context.Context) (domain.RolloutState, bool, error)
}

// QueueController pauses and resumes new workflow claims around process replacement.
type QueueController interface {
	PauseQueue(context.Context) error
	ResumeQueue(context.Context) error
}

// RolloutCoordinatorOptions owns lease and freshness policy outside the CLI.
type RolloutCoordinatorOptions struct {
	LeaseDuration      time.Duration
	RuntimeFreshWithin time.Duration
	RenewEvery         time.Duration
	Clock              foundation.Clock
}

// WaitOptions bounds one runtime readiness poll without exposing rollout phases.
type WaitOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
}

// RolloutCoordinator opens resumable rollout sessions backed by PostgreSQL facts.
type RolloutCoordinator struct {
	control            RolloutControl
	queue              QueueController
	ids                foundation.IDGenerator
	leaseDuration      time.Duration
	runtimeFreshWithin time.Duration
	renewEvery         time.Duration
	clock              foundation.Clock
}

// String returns a dependency-only summary without expanding service configuration.
func (coordinator RolloutCoordinator) String() string {
	return fmt.Sprintf("RolloutCoordinator{configured:%t}",
		!nilInterface(coordinator.control) && !nilInterface(coordinator.queue) && !nilInterface(coordinator.ids))
}

// GoString uses the same safe dependency-only summary.
func (coordinator RolloutCoordinator) GoString() string { return coordinator.String() }

// NewRolloutCoordinator creates the modelctl application boundary.
func NewRolloutCoordinator(
	control RolloutControl,
	queue QueueController,
	ids foundation.IDGenerator,
	options RolloutCoordinatorOptions,
) (*RolloutCoordinator, error) {
	if nilInterface(control) || nilInterface(queue) || nilInterface(ids) {
		return nil, unavailable(errors.New("model settings rollout coordinator dependency is unavailable"))
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = defaultRolloutLeaseDuration
	}
	if options.RuntimeFreshWithin == 0 {
		options.RuntimeFreshWithin = DefaultRuntimeFreshWithin
	}
	if options.RenewEvery == 0 {
		options.RenewEvery = defaultRolloutRenewEvery
	}
	if nilInterface(options.Clock) {
		options.Clock = foundation.SystemClock{}
	}
	if !validLease(options.LeaseDuration) || !ValidRuntimeFreshWithin(options.RuntimeFreshWithin) ||
		options.RenewEvery < time.Second || options.RenewEvery >= options.LeaseDuration {
		return nil, invalid(errors.New("model settings rollout coordinator policy is invalid"))
	}
	return &RolloutCoordinator{
		control: control, queue: queue, ids: ids,
		leaseDuration: options.LeaseDuration, runtimeFreshWithin: options.RuntimeFreshWithin,
		renewEvery: options.RenewEvery, clock: options.Clock,
	}, nil
}

// Begin fixes the current desired revision and returns its resumable session.
func (coordinator *RolloutCoordinator) Begin(ctx context.Context) (*RolloutSession, error) {
	if err := coordinator.ready(ctx); err != nil {
		return nil, err
	}
	id, err := coordinator.ids.New()
	if err != nil {
		return nil, err
	}
	if !validID(id) {
		return nil, unavailable(errors.New("model settings rollout identifier generation failed"))
	}
	state, err := coordinator.control.BeginRollout(ctx, BeginRolloutCommand{RolloutID: id, LeaseDuration: coordinator.leaseDuration})
	if err != nil {
		return nil, err
	}
	if err := validateSessionState(state, id, domain.RolloutPhaseValidating); err != nil {
		return nil, err
	}
	return &RolloutSession{coordinator: coordinator, id: id}, nil
}

// Open restores one rollout session from its persisted identifier.
func (coordinator *RolloutCoordinator) Open(ctx context.Context, id foundation.ID) (*RolloutSession, error) {
	if err := coordinator.ready(ctx); err != nil {
		return nil, err
	}
	if !validID(id) {
		return nil, invalid(errors.New("model settings rollout identifier is invalid"))
	}
	snapshot, err := coordinator.control.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateOpenState(snapshot.Rollout, id); err != nil {
		return nil, err
	}
	return &RolloutSession{coordinator: coordinator, id: id}, nil
}

// Recover fails an expired rollout and resumes the queue only in a terminal state.
func (coordinator *RolloutCoordinator) Recover(ctx context.Context) (domain.RolloutState, bool, error) {
	if err := coordinator.ready(ctx); err != nil {
		return domain.RolloutState{}, false, err
	}
	state, recovered, err := coordinator.control.RecoverExpiredRollout(ctx)
	if err != nil {
		return domain.RolloutState{}, false, err
	}
	if state.Phase != domain.RolloutPhaseIdle && state.Phase != domain.RolloutPhaseFailed {
		return state, recovered, rolloutStateError(errors.New("an unexpired model settings rollout is active"))
	}
	if err := coordinator.queue.ResumeQueue(ctx); err != nil {
		return state, recovered, dependencyError(err)
	}
	return state, recovered, nil
}

func (coordinator *RolloutCoordinator) ready(ctx context.Context) error {
	if ctx == nil {
		return invalid(errors.New("model settings rollout context is nil"))
	}
	if coordinator == nil || nilInterface(coordinator.control) || nilInterface(coordinator.queue) ||
		nilInterface(coordinator.ids) || nilInterface(coordinator.clock) {
		return unavailable(errors.New("model settings rollout coordinator is unavailable"))
	}
	return nil
}

// RolloutSession hides expected phases and CAS commands from modelctl.
type RolloutSession struct {
	coordinator *RolloutCoordinator
	id          foundation.ID
}

// String returns a safe summary without expanding coordinator dependencies or the rollout ID.
func (session RolloutSession) String() string {
	return fmt.Sprintf("RolloutSession{configured:%t}", session.coordinator != nil && validID(session.id))
}

// GoString uses the same safe summary.
func (session RolloutSession) GoString() string { return session.String() }

// ID returns the fixed persisted rollout identifier.
func (session *RolloutSession) ID() foundation.ID {
	if session == nil {
		return ""
	}
	return session.id
}

// State returns the current state only while this session still owns the binding.
func (session *RolloutSession) State(ctx context.Context) (domain.RolloutState, error) {
	if err := session.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	snapshot, err := session.coordinator.control.Snapshot(ctx)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := validateOpenState(snapshot.Rollout, session.id); err != nil {
		return domain.RolloutState{}, err
	}
	return snapshot.Rollout, nil
}

// LoadTarget renews validating ownership and decrypts only the fixed target revision.
func (session *RolloutSession) LoadTarget(ctx context.Context) (domain.ResolvedSettings, error) {
	state, err := session.requirePhase(ctx, domain.RolloutPhaseValidating)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	if _, err := session.renew(ctx, domain.RolloutPhaseValidating); err != nil {
		return domain.ResolvedSettings{}, err
	}
	resolved, err := session.coordinator.control.LoadRevision(ctx, state.TargetRevision)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	if resolved.Revision != state.TargetRevision {
		resolved.ChatAPIKey.Destroy()
		resolved.EmbeddingAPIKey.Destroy()
		return domain.ResolvedSettings{}, rolloutStateError(errors.New("model settings rollout target changed while loading"))
	}
	return resolved, nil
}

// RenewValidating extends the lease after one role's external preflight succeeds.
func (session *RolloutSession) RenewValidating(ctx context.Context) error {
	if _, err := session.requirePhase(ctx, domain.RolloutPhaseValidating); err != nil {
		return err
	}
	_, err := session.renew(ctx, domain.RolloutPhaseValidating)
	return err
}

// StartDraining closes the database enqueue fence before pausing new claims.
func (session *RolloutSession) StartDraining(ctx context.Context) error {
	if _, err := session.requirePhase(ctx, domain.RolloutPhaseValidating); err != nil {
		return err
	}
	state, err := session.coordinator.control.AdvanceRollout(ctx, AdvanceRolloutCommand{
		RolloutID: session.id, ExpectedPhase: domain.RolloutPhaseValidating,
		NextPhase: domain.RolloutPhaseDraining, LeaseDuration: session.coordinator.leaseDuration,
	})
	if err != nil {
		return err
	}
	if err := validateSessionState(state, session.id, domain.RolloutPhaseDraining); err != nil {
		if _, failErr := session.coordinator.control.FailRollout(ctx, FailRolloutCommand{RolloutID: session.id, ErrorCode: domain.ErrorCodeCorrupt}); failErr != nil {
			return failErr
		}
		if resumeErr := session.coordinator.queue.ResumeQueue(ctx); resumeErr != nil {
			return dependencyError(resumeErr)
		}
		return err
	}
	if pauseErr := session.coordinator.queue.PauseQueue(ctx); pauseErr != nil {
		failed, failErr := session.coordinator.control.FailRollout(ctx, FailRolloutCommand{
			RolloutID: session.id,
			ErrorCode: domain.ErrorCodeCorrupt,
		})
		if failErr != nil {
			return failErr
		}
		if err := validateSessionState(failed, session.id, domain.RolloutPhaseFailed); err != nil {
			return err
		}
		if resumeErr := session.coordinator.queue.ResumeQueue(ctx); resumeErr != nil {
			return dependencyError(resumeErr)
		}
		return dependencyError(pauseErr)
	}
	return nil
}

// WaitQuiesced waits for both old runtimes and advances draining to applying.
func (session *RolloutSession) WaitQuiesced(ctx context.Context, options WaitOptions) error {
	ready := func(snapshot domain.Snapshot) bool {
		api := snapshot.Runtime.API
		worker := snapshot.Runtime.Worker
		return api.Fresh && api.AppliedRevision == snapshot.ActiveRevision &&
			(api.Phase == domain.RuntimePhaseQuiescing || api.Phase == domain.RuntimePhaseQuiesced) &&
			worker.Fresh && worker.AppliedRevision == snapshot.ActiveRevision && worker.Phase == domain.RuntimePhaseQuiesced
	}
	if err := session.waitFor(ctx, domain.RolloutPhaseDraining, options, ready); err != nil {
		return err
	}
	state, err := session.coordinator.control.AdvanceRollout(ctx, AdvanceRolloutCommand{
		RolloutID: session.id, ExpectedPhase: domain.RolloutPhaseDraining,
		NextPhase: domain.RolloutPhaseApplying, LeaseDuration: session.coordinator.leaseDuration,
	})
	if err != nil {
		return err
	}
	return validateSessionState(state, session.id, domain.RolloutPhaseApplying)
}

// WaitPrepared waits for both candidate runtimes and advances applying to verifying.
func (session *RolloutSession) WaitPrepared(ctx context.Context, options WaitOptions) error {
	ready := func(snapshot domain.Snapshot) bool {
		target := snapshot.Rollout.TargetRevision
		api := snapshot.Runtime.API
		worker := snapshot.Runtime.Worker
		return api.Fresh && api.AppliedRevision == target && api.Phase == domain.RuntimePhasePrepared &&
			worker.Fresh && worker.AppliedRevision == target && worker.Phase == domain.RuntimePhasePrepared
	}
	if err := session.waitFor(ctx, domain.RolloutPhaseApplying, options, ready); err != nil {
		return err
	}
	state, err := session.coordinator.control.AdvanceRollout(ctx, AdvanceRolloutCommand{
		RolloutID: session.id, ExpectedPhase: domain.RolloutPhaseApplying,
		NextPhase: domain.RolloutPhaseVerifying, LeaseDuration: session.coordinator.leaseDuration,
	})
	if err != nil {
		return err
	}
	return validateSessionState(state, session.id, domain.RolloutPhaseVerifying)
}

// Commit publishes the fixed target and resumes queue claims after the database verifies freshness.
func (session *RolloutSession) Commit(ctx context.Context) error {
	if _, err := session.requirePhase(ctx, domain.RolloutPhaseVerifying); err != nil {
		return err
	}
	if _, err := session.renew(ctx, domain.RolloutPhaseVerifying); err != nil {
		return err
	}
	state, err := session.coordinator.control.CommitRollout(ctx, CommitRolloutCommand{
		RolloutID: session.id, FreshWithin: session.coordinator.runtimeFreshWithin,
	})
	if err != nil {
		return err
	}
	if state.Phase != domain.RolloutPhaseIdle || state.ID != "" {
		return rolloutStateError(errors.New("model settings rollout commit returned a non-idle state"))
	}
	if err := session.coordinator.queue.ResumeQueue(ctx); err != nil {
		return foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			domain.ErrorCodeCommittedQueuePaused,
			true,
			err,
		)
	}
	return nil
}

// Abort restores the previous active revision before resuming queue claims.
func (session *RolloutSession) Abort(ctx context.Context, errorCode string) error {
	state, err := session.State(ctx)
	if err != nil {
		return err
	}
	if !activeSessionPhase(state.Phase) || !canonicalErrorCode(errorCode) {
		return invalid(errors.New("model settings rollout abort command is invalid"))
	}
	failed, err := session.coordinator.control.FailRollout(ctx, FailRolloutCommand{RolloutID: session.id, ErrorCode: errorCode})
	if err != nil {
		return err
	}
	if err := validateSessionState(failed, session.id, domain.RolloutPhaseFailed); err != nil {
		return err
	}
	if err := session.coordinator.queue.ResumeQueue(ctx); err != nil {
		return dependencyError(err)
	}
	return nil
}

func (session *RolloutSession) waitFor(
	ctx context.Context,
	phase domain.RolloutPhase,
	options WaitOptions,
	ready func(domain.Snapshot) bool,
) error {
	if err := session.ready(ctx); err != nil {
		return err
	}
	if options.Timeout <= 0 || options.Timeout > maximumRolloutWait || options.PollInterval <= 0 ||
		options.PollInterval > maximumRolloutPollInterval || options.PollInterval > options.Timeout ||
		options.PollInterval > session.coordinator.renewEvery || ready == nil {
		return invalid(errors.New("model settings rollout wait bounds are invalid"))
	}
	waitCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	if _, err := session.requirePhase(waitCtx, phase); err != nil {
		return err
	}
	if _, err := session.renew(waitCtx, phase); err != nil {
		return err
	}
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()
	lastRenewal := session.coordinator.clock.Now()
	for {
		snapshot, err := session.coordinator.control.Snapshot(waitCtx)
		if err != nil {
			if waitCtx.Err() != nil {
				return rolloutWaitError(ctx, waitCtx.Err())
			}
			return err
		}
		if err := validateSessionState(snapshot.Rollout, session.id, phase); err != nil {
			return err
		}
		if ready(snapshot) {
			return nil
		}
		if session.coordinator.clock.Now().Sub(lastRenewal) >= session.coordinator.renewEvery {
			if _, err := session.renew(waitCtx, phase); err != nil {
				return err
			}
			lastRenewal = session.coordinator.clock.Now()
		}
		select {
		case <-waitCtx.Done():
			return rolloutWaitError(ctx, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (session *RolloutSession) requirePhase(ctx context.Context, phase domain.RolloutPhase) (domain.RolloutState, error) {
	if err := session.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	snapshot, err := session.coordinator.control.Snapshot(ctx)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := validateSessionState(snapshot.Rollout, session.id, phase); err != nil {
		return domain.RolloutState{}, err
	}
	return snapshot.Rollout, nil
}

func (session *RolloutSession) renew(ctx context.Context, phase domain.RolloutPhase) (domain.RolloutState, error) {
	state, err := session.coordinator.control.RenewRollout(ctx, RenewRolloutCommand{
		RolloutID: session.id, ExpectedPhase: phase, LeaseDuration: session.coordinator.leaseDuration,
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := validateSessionState(state, session.id, phase); err != nil {
		return domain.RolloutState{}, err
	}
	return state, nil
}

func (session *RolloutSession) ready(ctx context.Context) error {
	if session == nil || session.coordinator == nil || !validID(session.id) {
		return unavailable(errors.New("model settings rollout session is unavailable"))
	}
	return session.coordinator.ready(ctx)
}

func validateOpenState(state domain.RolloutState, id foundation.ID) error {
	if state.ID != id || !activeSessionPhase(state.Phase) && state.Phase != domain.RolloutPhaseFailed ||
		state.TargetRevision < 0 || state.PreviousActiveRevision < 0 {
		return rolloutStateError(errors.New("model settings rollout binding does not match"))
	}
	return nil
}

func validateSessionState(state domain.RolloutState, id foundation.ID, phase domain.RolloutPhase) error {
	if state.ID != id || state.Phase != phase || state.TargetRevision < 0 || state.PreviousActiveRevision < 0 {
		return rolloutStateError(errors.New("model settings rollout binding or phase changed"))
	}
	return nil
}

func activeSessionPhase(phase domain.RolloutPhase) bool {
	return phase == domain.RolloutPhaseValidating || phase == domain.RolloutPhaseDraining ||
		phase == domain.RolloutPhaseApplying || phase == domain.RolloutPhaseVerifying
}

func rolloutStateError(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRolloutConflict, false, cause)
}

func rolloutWaitError(parent context.Context, cause error) error {
	if parent != nil && parent.Err() != nil {
		return parent.Err()
	}
	return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeRolloutWaitTimeout, true, cause)
}

func dependencyError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return unavailable(err)
}
