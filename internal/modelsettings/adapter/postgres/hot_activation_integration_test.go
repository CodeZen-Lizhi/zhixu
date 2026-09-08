//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestRepositoryHotActivationProtocolAndRecovery(t *testing.T) {
	ctx := context.Background()
	pool := newModelSettingsTestDatabase(t)
	repository, _, _ := newConfiguredModelSettingsRepository(t, pool, 21)
	degraded, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if degraded.DesiredRevision != degraded.ActiveRevision || !degraded.ApplyRequired || degraded.RestartRequired {
		t.Fatalf("active-equal degraded snapshot=%+v", degraded)
	}
	activeReplayCommand := application.StartActivationCommand{
		RolloutID:      mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000020"),
		TargetRevision: 0, ExpectedDesiredRevision: 0, ExpectedStateVersion: degraded.Rollout.Version,
		LeaseDuration: 30 * time.Second, FreshWithin: 20 * time.Second,
	}
	_, err = repository.StartActivation(ctx, activeReplayCommand)
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeNotReady)
	registerActiveRuntimes(t, ctx, repository, 0)
	activeReplay, err := repository.StartActivation(ctx, activeReplayCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !activeReplay.Replayed || activeReplay.State.Phase != domain.RolloutPhaseIdle || activeReplay.State.Version != degraded.Rollout.Version {
		t.Fatalf("already-active replay=%+v", activeReplay)
	}

	saved, err := repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 0, Settings: domain.CanonicalDisabledSettings(),
		ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.KeepSecret(), CreatedBy: "activation-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rolloutID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000021")
	started, err := repository.StartActivation(ctx, application.StartActivationCommand{
		RolloutID: rolloutID, TargetRevision: 1, ExpectedDesiredRevision: 1,
		ExpectedStateVersion: saved.Rollout.Version, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Replayed || started.State.Phase != domain.RolloutPhasePreparing || started.State.TargetRevision != 1 {
		t.Fatalf("started activation=%+v", started)
	}
	replay, err := repository.StartActivation(ctx, application.StartActivationCommand{
		RolloutID:      mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000099"),
		TargetRevision: 1, ExpectedDesiredRevision: 1, ExpectedStateVersion: 1, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.State.ID != rolloutID || replay.State.Version != started.State.Version {
		t.Fatalf("same-target replay=%+v", replay)
	}

	participantVersions := make(map[domain.RuntimeRole]int64, 2)
	for _, fixture := range []struct {
		role       domain.RuntimeRole
		instanceID string
	}{
		{domain.RuntimeRoleAPI, "a2000000-0000-4000-8000-000000000001"},
		{domain.RuntimeRoleWorker, "a2000000-0000-4000-8000-000000000002"},
	} {
		instanceID := mustModelSettingsID(t, fixture.instanceID)
		participant, err := repository.RegisterParticipant(ctx, application.ParticipantRegistration{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 1,
			InitialPhase: domain.ParticipantPhasePreparing, StaleAfter: 20 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		participant, err = repository.TransitionParticipant(ctx, application.ParticipantTransitionCommand{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 1,
			ExpectedPhase: domain.ParticipantPhasePreparing, ExpectedVersion: participant.Version,
			NextPhase: domain.ParticipantPhasePrepared,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.HeartbeatParticipant(ctx, application.ParticipantHeartbeat{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 1,
			ExpectedPhase: domain.ParticipantPhasePrepared, ExpectedVersion: participant.Version - 1,
		})
		assertModelSettingsErrorCode(t, err, domain.ErrorCodeParticipantConflict)
		participantVersions[fixture.role] = participant.Version
	}
	preparingSnapshot, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !preparingSnapshot.ApplyRequired || preparingSnapshot.RestartRequired ||
		!preparingSnapshot.Participants.API.Present || !preparingSnapshot.Participants.Worker.Present {
		t.Fatalf("preparing snapshot=%+v", preparingSnapshot)
	}
	arming, err := repository.AdvanceActivation(ctx, application.AdvanceActivationCommand{
		RolloutID: rolloutID, ExpectedPhase: domain.RolloutPhasePreparing, ExpectedVersion: started.State.Version,
		NextPhase: domain.RolloutPhaseArming, LeaseDuration: 30 * time.Second, FreshWithin: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEnqueueBlocked(t, ctx, pool, repository)
	for _, fixture := range []struct {
		role       domain.RuntimeRole
		instanceID string
	}{
		{domain.RuntimeRoleAPI, "a2000000-0000-4000-8000-000000000001"},
		{domain.RuntimeRoleWorker, "a2000000-0000-4000-8000-000000000002"},
	} {
		participant, err := repository.TransitionParticipant(ctx, application.ParticipantTransitionCommand{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: mustModelSettingsID(t, fixture.instanceID), TargetRevision: 1,
			ExpectedPhase: domain.ParticipantPhasePrepared, ExpectedVersion: participantVersions[fixture.role],
			NextPhase: domain.ParticipantPhaseArmed,
		})
		if err != nil {
			t.Fatal(err)
		}
		participantVersions[fixture.role] = participant.Version
	}
	committed, err := repository.CommitActivation(ctx, application.CommitActivationCommand{
		RolloutID: rolloutID, ExpectedVersion: arming.Version, FreshWithin: 20 * time.Second, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Phase != domain.RolloutPhaseActivating || committed.TargetRevision != 1 || committed.PreviousActiveRevision != 0 {
		t.Fatalf("committed activation=%s", committed)
	}
	if _, err := pool.DB().Exec(ctx, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()-interval '1 second',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	recovery, err := repository.RecoverActivation(ctx, application.RecoverActivationCommand{LeaseDuration: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Action != domain.ActivationRecoveryAdoptedPostCommit || recovery.State.Phase != domain.RolloutPhaseActivating {
		t.Fatalf("post-commit recovery=%+v", recovery)
	}
	for _, fixture := range []struct {
		role       domain.RuntimeRole
		instanceID string
	}{
		{domain.RuntimeRoleAPI, "a2000000-0000-4000-8000-000000000001"},
		{domain.RuntimeRoleWorker, "a2000000-0000-4000-8000-000000000002"},
	} {
		acknowledgement := application.ActivationAcknowledgement{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: mustModelSettingsID(t, fixture.instanceID), TargetRevision: 1,
			ExpectedStateVersion: recovery.State.Version, ExpectedParticipantVersion: participantVersions[fixture.role],
		}
		participant, err := repository.AcknowledgeActivation(ctx, acknowledgement)
		if err != nil {
			t.Fatal(err)
		}
		if participant.Phase != domain.ParticipantPhaseActivated {
			t.Fatalf("participant acknowledgement=%+v", participant)
		}
		replayed, err := repository.AcknowledgeActivation(ctx, acknowledgement)
		if err != nil {
			t.Fatal(err)
		}
		if replayed.Phase != domain.ParticipantPhaseActivated || replayed.Version != participant.Version {
			t.Fatalf("participant acknowledgement replay=%+v", replayed)
		}
	}
	apiInstanceID := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000001")
	if _, err := pool.DB().Exec(ctx, `UPDATE ops.model_settings_runtime
SET phase='unavailable',heartbeat_at=clock_timestamp()
WHERE role='api' AND instance_id=$1::uuid`, string(apiInstanceID)); err != nil {
		t.Fatal(err)
	}
	_, err = repository.RestoreRuntimeAvailability(ctx, application.RestoreRuntimeAvailabilityCommand{
		Role:            domain.RuntimeRoleAPI,
		InstanceID:      mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000009"),
		AppliedRevision: 1,
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeOwnershipLost)
	restored, err := repository.RestoreRuntimeAvailability(ctx, application.RestoreRuntimeAvailabilityCommand{
		Role: domain.RuntimeRoleAPI, InstanceID: apiInstanceID, AppliedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Phase != domain.RuntimePhaseActive || restored.AppliedRevision != 1 || restored.InstanceID != apiInstanceID {
		t.Fatalf("activating runtime availability restore=%+v", restored)
	}
	finalized, err := repository.FinalizeActivation(ctx, application.FinalizeActivationCommand{
		RolloutID: rolloutID, ExpectedVersion: recovery.State.Version, FreshWithin: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Phase != domain.RolloutPhaseIdle {
		t.Fatalf("finalized activation=%s", finalized)
	}
	finalSnapshot, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if finalSnapshot.ActiveRevision != 1 || finalSnapshot.DesiredRevision != 1 || finalSnapshot.ApplyRequired || finalSnapshot.RestartRequired {
		t.Fatalf("final snapshot=%+v", finalSnapshot)
	}
	if _, err := pool.DB().Exec(ctx, `UPDATE ops.model_settings_runtime
SET phase='unavailable',heartbeat_at=clock_timestamp()
WHERE role='api' AND instance_id=$1::uuid`, string(apiInstanceID)); err != nil {
		t.Fatal(err)
	}
	_, err = repository.RestoreRuntimeAvailability(ctx, application.RestoreRuntimeAvailabilityCommand{
		Role: domain.RuntimeRoleAPI, InstanceID: apiInstanceID, AppliedRevision: 2,
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeConflict)
	restored, err = repository.RestoreRuntimeAvailability(ctx, application.RestoreRuntimeAvailabilityCommand{
		Role: domain.RuntimeRoleAPI, InstanceID: apiInstanceID, AppliedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedRestore, err := repository.RestoreRuntimeAvailability(ctx, application.RestoreRuntimeAvailabilityCommand{
		Role: domain.RuntimeRoleAPI, InstanceID: apiInstanceID, AppliedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Phase != domain.RuntimePhaseActive || replayedRestore.Phase != domain.RuntimePhaseActive ||
		replayedRestore.AppliedAt != restored.AppliedAt {
		t.Fatalf("idle availability restore=%+v replay=%+v", restored, replayedRestore)
	}
	finalReplay, err := repository.StartActivation(ctx, application.StartActivationCommand{
		RolloutID:      mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000098"),
		TargetRevision: 1, ExpectedDesiredRevision: 1, ExpectedStateVersion: started.State.Version,
		LeaseDuration: 30 * time.Second, FreshWithin: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finalReplay.Replayed || finalReplay.State.Phase != domain.RolloutPhaseIdle || finalReplay.State.Version != finalized.Version {
		t.Fatalf("post-finalize response-loss replay=%+v", finalReplay)
	}

	saved, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 1, Settings: domain.CanonicalDisabledSettings(),
		ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.KeepSecret(), CreatedBy: "activation-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000022")
	preCommit, err := repository.StartActivation(ctx, application.StartActivationCommand{
		RolloutID: expiredID, TargetRevision: 2, ExpectedDesiredRevision: 2,
		ExpectedStateVersion: saved.Rollout.Version, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		role       domain.RuntimeRole
		instanceID string
	}{
		{domain.RuntimeRoleAPI, "a2000000-0000-4000-8000-000000000001"},
		{domain.RuntimeRoleWorker, "a2000000-0000-4000-8000-000000000002"},
	} {
		instanceID := mustModelSettingsID(t, fixture.instanceID)
		participant, registerErr := repository.RegisterParticipant(ctx, application.ParticipantRegistration{
			RolloutID: expiredID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 2,
			InitialPhase: domain.ParticipantPhasePreparing, StaleAfter: 20 * time.Second,
		})
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		if _, transitionErr := repository.TransitionParticipant(ctx, application.ParticipantTransitionCommand{
			RolloutID: expiredID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 2,
			ExpectedPhase: domain.ParticipantPhasePreparing, ExpectedVersion: participant.Version,
			NextPhase: domain.ParticipantPhasePrepared,
		}); transitionErr != nil {
			t.Fatal(transitionErr)
		}
	}
	if _, err := pool.DB().Exec(ctx, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()-interval '1 second',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	recovery, err = repository.RecoverActivation(ctx, application.RecoverActivationCommand{LeaseDuration: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if preCommit.State.Phase != domain.RolloutPhasePreparing || recovery.Action != domain.ActivationRecoveryFailedPreCommit ||
		recovery.State.Phase != domain.RolloutPhaseFailed || recovery.State.PreviousActiveRevision != 1 {
		t.Fatalf("pre-commit recovery=%+v", recovery)
	}
	failedSnapshot, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failedSnapshot.Participants.API.Phase != domain.ParticipantPhaseAborted ||
		failedSnapshot.Participants.Worker.Phase != domain.ParticipantPhaseAborted {
		t.Fatalf("pre-commit recovery participants=%+v", failedSnapshot.Participants)
	}
}

func TestRepositoryRuntimeTakeoverRequiresDatabaseTimeStaleness(t *testing.T) {
	ctx := context.Background()
	pool := newModelSettingsTestDatabase(t)
	repository, _, _ := newConfiguredModelSettingsRepository(t, pool, 22)
	oldOwner := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000221")
	newOwner := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000222")
	registration := application.RuntimeRegistration{
		Role: domain.RuntimeRoleAPI, AppliedRevision: 0, Phase: domain.RuntimePhaseActive,
		StaleAfter: application.DefaultRuntimeFreshWithin,
	}
	registration.InstanceID = oldOwner
	if _, err := repository.RegisterRuntime(ctx, registration); err != nil {
		t.Fatal(err)
	}
	registration.InstanceID = newOwner
	_, err := repository.RegisterRuntime(ctx, registration)
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeConflict)
	backdateModelSettingsTakeoverHeartbeats(t, ctx, pool, domain.RuntimeRoleAPI, "",
		application.DefaultRuntimeFreshWithin+time.Second)
	if _, err := repository.RegisterRuntime(ctx, registration); err != nil {
		t.Fatal(err)
	}
	_, err = repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{Role: domain.RuntimeRoleAPI, InstanceID: oldOwner})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeOwnershipLost)
}

func TestRepositoryParticipantTakeoverRebuildsWhileArming(t *testing.T) {
	ctx := context.Background()
	pool := newModelSettingsTestDatabase(t)
	repository, _, _ := newConfiguredModelSettingsRepository(t, pool, 23)
	registerActiveRuntimes(t, ctx, repository, 0)
	saved, err := repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 0, Settings: domain.CanonicalDisabledSettings(),
		ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.KeepSecret(), CreatedBy: "takeover-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rolloutID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000023")
	started, err := repository.StartActivation(ctx, application.StartActivationCommand{
		RolloutID: rolloutID, TargetRevision: 1, ExpectedDesiredRevision: 1,
		ExpectedStateVersion: saved.Rollout.Version, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	participants := make(map[domain.RuntimeRole]domain.ParticipantRecord, 2)
	for _, fixture := range []struct {
		role       domain.RuntimeRole
		instanceID string
	}{
		{domain.RuntimeRoleAPI, "a2000000-0000-4000-8000-000000000001"},
		{domain.RuntimeRoleWorker, "a2000000-0000-4000-8000-000000000002"},
	} {
		instanceID := mustModelSettingsID(t, fixture.instanceID)
		participant, registerErr := repository.RegisterParticipant(ctx, application.ParticipantRegistration{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 1,
			InitialPhase: domain.ParticipantPhasePreparing, StaleAfter: application.DefaultRuntimeFreshWithin,
		})
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		participant, err = repository.TransitionParticipant(ctx, application.ParticipantTransitionCommand{
			RolloutID: rolloutID, Role: fixture.role, InstanceID: instanceID, TargetRevision: 1,
			ExpectedPhase: domain.ParticipantPhasePreparing, ExpectedVersion: participant.Version,
			NextPhase: domain.ParticipantPhasePrepared,
		})
		if err != nil {
			t.Fatal(err)
		}
		participants[fixture.role] = participant
	}
	if _, err := repository.AdvanceActivation(ctx, application.AdvanceActivationCommand{
		RolloutID: rolloutID, ExpectedPhase: domain.RolloutPhasePreparing, ExpectedVersion: started.State.Version,
		NextPhase: domain.RolloutPhaseArming, LeaseDuration: 30 * time.Second,
		FreshWithin: application.DefaultRuntimeFreshWithin,
	}); err != nil {
		t.Fatal(err)
	}
	backdateModelSettingsTakeoverHeartbeats(t, ctx, pool, domain.RuntimeRoleAPI, string(rolloutID),
		application.DefaultRuntimeFreshWithin+time.Second)
	newAPI := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000023")
	if _, err := repository.RegisterRuntime(ctx, application.RuntimeRegistration{
		Role: domain.RuntimeRoleAPI, InstanceID: newAPI, AppliedRevision: 0,
		Phase: domain.RuntimePhaseActive, StaleAfter: application.DefaultRuntimeFreshWithin,
	}); err != nil {
		t.Fatal(err)
	}
	replacement, err := repository.RegisterParticipant(ctx, application.ParticipantRegistration{
		RolloutID: rolloutID, Role: domain.RuntimeRoleAPI, InstanceID: newAPI, TargetRevision: 1,
		InitialPhase: domain.ParticipantPhasePreparing, StaleAfter: application.DefaultRuntimeFreshWithin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Phase != domain.ParticipantPhasePreparing || replacement.Version != participants[domain.RuntimeRoleAPI].Version+1 {
		t.Fatalf("replacement participant=%+v", replacement)
	}
	replacement, err = repository.TransitionParticipant(ctx, application.ParticipantTransitionCommand{
		RolloutID: rolloutID, Role: domain.RuntimeRoleAPI, InstanceID: newAPI, TargetRevision: 1,
		ExpectedPhase: domain.ParticipantPhasePreparing, ExpectedVersion: replacement.Version,
		NextPhase: domain.ParticipantPhasePrepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionParticipant(ctx, application.ParticipantTransitionCommand{
		RolloutID: rolloutID, Role: domain.RuntimeRoleAPI, InstanceID: newAPI, TargetRevision: 1,
		ExpectedPhase: domain.ParticipantPhasePrepared, ExpectedVersion: replacement.Version,
		NextPhase: domain.ParticipantPhaseArmed,
	}); err != nil {
		t.Fatal(err)
	}
}

func backdateModelSettingsTakeoverHeartbeats(
	t *testing.T,
	ctx context.Context,
	pool *platformpostgres.Pool,
	role domain.RuntimeRole,
	rolloutID string,
	age time.Duration,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Only the isolated fixture transaction bypasses monotonic-heartbeat guards
	// so takeover tests do not wait for the server-owned freshness window.
	if _, err := tx.Exec(ctx, `ALTER TABLE ops.model_settings_runtime
DISABLE TRIGGER model_settings_runtime_guard`); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(ctx, `UPDATE ops.model_settings_runtime
SET applied_at=clock_timestamp()-($1::bigint*interval '1 microsecond'),
    heartbeat_at=clock_timestamp()-($1::bigint*interval '1 microsecond')
WHERE role=$2`, age.Microseconds(), string(role))
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected() != 1 {
		t.Fatalf("backdate %s runtime heartbeat affected %d rows", role, result.RowsAffected())
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE ops.model_settings_runtime
ENABLE TRIGGER model_settings_runtime_guard`); err != nil {
		t.Fatal(err)
	}
	if rolloutID != "" {
		if _, err := tx.Exec(ctx, `ALTER TABLE ops.model_settings_rollout_participant
DISABLE TRIGGER model_settings_participant_guard`); err != nil {
			t.Fatal(err)
		}
		result, err = tx.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET heartbeat_at=clock_timestamp()-($1::bigint*interval '1 microsecond')
WHERE rollout_id=$2::uuid AND role=$3`, age.Microseconds(), rolloutID, string(role))
		if err != nil {
			t.Fatal(err)
		}
		if result.RowsAffected() != 1 {
			t.Fatalf("backdate %s participant heartbeat affected %d rows", role, result.RowsAffected())
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE ops.model_settings_rollout_participant
ENABLE TRIGGER model_settings_participant_guard`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
