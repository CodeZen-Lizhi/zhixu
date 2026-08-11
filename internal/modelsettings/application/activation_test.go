package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func TestServiceStartActivationRequiresExactTargetAndStateVersion(t *testing.T) {
	repository := &activationServiceTestRepository{serviceTestRepository: &serviceTestRepository{}}
	service, err := NewService(repository, ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
		return nil
	}), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rolloutID := mustActivationTestID(t, "a1000000-0000-4000-8000-000000000301")
	want := StartActivationResult{State: domain.RolloutState{ID: rolloutID, TargetRevision: 7, Phase: domain.RolloutPhasePreparing, Version: 12}}
	repository.startResult = want
	command := StartActivationCommand{
		RolloutID: rolloutID, TargetRevision: 7, ExpectedDesiredRevision: 7,
		ExpectedStateVersion: 11, LeaseDuration: 30 * time.Second, FreshWithin: 20 * time.Second,
	}
	got, err := service.StartActivation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || repository.startCalls != 1 || repository.startCommand != command {
		t.Fatalf("result=%+v calls=%d command=%+v", got, repository.startCalls, repository.startCommand)
	}
	command.ExpectedDesiredRevision = 8
	_, err = service.StartActivation(context.Background(), command)
	assertActivationApplicationCode(t, err, domain.ErrorCodeInvalid)
	if repository.startCalls != 1 {
		t.Fatalf("invalid exact target reached store: calls=%d", repository.startCalls)
	}
}

func TestServiceParticipantActivationRequiresAtomicAcknowledgement(t *testing.T) {
	repository := &activationServiceTestRepository{serviceTestRepository: &serviceTestRepository{}}
	service, err := NewService(repository, ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
		return nil
	}), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.TransitionParticipant(context.Background(), ParticipantTransitionCommand{
		RolloutID: mustActivationTestID(t, "a1000000-0000-4000-8000-000000000302"),
		Role:      domain.RuntimeRoleAPI, InstanceID: mustActivationTestID(t, "a2000000-0000-4000-8000-000000000302"),
		TargetRevision: 7, ExpectedPhase: domain.ParticipantPhaseArmed, ExpectedVersion: 3,
		NextPhase: domain.ParticipantPhaseActivated,
	})
	assertActivationApplicationCode(t, err, domain.ErrorCodeInvalid)
}

func TestServiceRestoreRuntimeAvailabilityRequiresExactPositiveBinding(t *testing.T) {
	repository := &activationServiceTestRepository{serviceTestRepository: &serviceTestRepository{}}
	service, err := NewService(repository, ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
		return nil
	}), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	command := RestoreRuntimeAvailabilityCommand{
		Role: domain.RuntimeRoleAPI, InstanceID: mustActivationTestID(t, "a2000000-0000-4000-8000-000000000303"), AppliedRevision: 7,
	}
	want := domain.RuntimeRecord{
		Role: command.Role, InstanceID: command.InstanceID, AppliedRevision: command.AppliedRevision, Phase: domain.RuntimePhaseActive,
	}
	repository.restoreResult = want
	got, err := service.RestoreRuntimeAvailability(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || repository.restoreCalls != 1 || repository.restoreCommand != command {
		t.Fatalf("result=%+v calls=%d command=%+v", got, repository.restoreCalls, repository.restoreCommand)
	}
	command.AppliedRevision = 0
	_, err = service.RestoreRuntimeAvailability(context.Background(), command)
	assertActivationApplicationCode(t, err, domain.ErrorCodeInvalid)
	if repository.restoreCalls != 1 {
		t.Fatalf("invalid restore reached store: calls=%d", repository.restoreCalls)
	}
}

type activationServiceTestRepository struct {
	*serviceTestRepository
	ActivationStore
	ParticipantStore
	startCommand   StartActivationCommand
	startResult    StartActivationResult
	startCalls     int
	restoreCommand RestoreRuntimeAvailabilityCommand
	restoreResult  domain.RuntimeRecord
	restoreCalls   int
}

func (repository *activationServiceTestRepository) StartActivation(_ context.Context, command StartActivationCommand) (StartActivationResult, error) {
	repository.startCalls++
	repository.startCommand = command
	return repository.startResult, nil
}

func (repository *activationServiceTestRepository) RestoreRuntimeAvailability(
	_ context.Context,
	command RestoreRuntimeAvailabilityCommand,
) (domain.RuntimeRecord, error) {
	repository.restoreCalls++
	repository.restoreCommand = command
	return repository.restoreResult, nil
}

func mustActivationTestID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertActivationApplicationCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
