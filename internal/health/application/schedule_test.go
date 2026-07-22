package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestScheduleServiceDefaultsCreateToDisabled(t *testing.T) {
	state := &scheduleStateFake{}
	service, err := NewScheduleService(state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), ScheduleCreateCommand{WorkspaceID: testID(80), Scope: domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: testID(80), Version: 1, SchemaVersion: "health-scope/workspace/v1"}, IdempotencyKey: "schedule-create-80"})
	if err != nil {
		t.Fatal(err)
	}
	if state.created.Cadence != domain.ScheduleCadenceDisabled || state.created.Timezone != "UTC" || state.created.MaxItems != domain.MaxScheduleItems || state.created.NextRunAt != nil {
		t.Fatalf("created=%#v", state.created)
	}
}

func TestScheduleDispatcherStartsOneBoundScanPerClaim(t *testing.T) {
	workspaceID := testID(81)
	due := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	claim := scheduleClaimFixture(workspaceID, testID(82), due)
	state := &scheduleStateFake{claimBatches: [][]DueScheduleClaim{{claim}}}
	descriptor := Descriptor{ID: "health.detector.a", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}
	registry, _ := NewRegistry([]Detector{descriptorDetector{descriptor}}, nil)
	scans := &scheduledScanFake{}
	service, err := NewScheduleDispatcher(state, scans, registry)
	if err != nil {
		t.Fatal(err)
	}
	count, err := service.DispatchDue(context.Background(), 10)
	if err != nil || count != 1 || scans.command.IdempotencyKey == "" || !scans.command.PreventScopeConcurrency || len(scans.command.Coverage) != 1 || state.acks != 1 || state.releases != 0 {
		t.Fatalf("count=%d command=%#v acks=%d releases=%d err=%v", count, scans.command, state.acks, state.releases, err)
	}
	if scans.command.IdempotencyKey != scheduleScanIdempotencyKey(claim.Schedule.ID, due) {
		t.Fatalf("idempotency key=%q", scans.command.IdempotencyKey)
	}
}

func TestScheduleDispatcherRetriesClaimWhenScanStartFails(t *testing.T) {
	workspaceID := testID(83)
	due := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	state := &scheduleStateFake{claimBatches: [][]DueScheduleClaim{{scheduleClaimFixture(workspaceID, testID(84), due)}}}
	registry, _ := NewRegistry([]Detector{descriptorDetector{Descriptor{ID: "health.detector.retry", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}}}, nil)
	service, err := NewScheduleDispatcher(state, failingScheduledScanFake{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := service.DispatchDue(context.Background(), 1); count != 0 || err == nil || state.releases != 1 || state.acks != 0 {
		t.Fatalf("count=%d releases=%d acks=%d err=%v", count, state.releases, state.acks, err)
	}
}

func TestSmartCollectionScheduleDefersRevisionBindingAndReleasesStaleClaim(t *testing.T) {
	workspaceID, collectionID := testID(110), testID(111)
	due := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	claim := scheduleClaimFixture(workspaceID, testID(112), due)
	claim.Schedule.Scope = domain.ScanScope{
		Type: domain.ScanScopeTypeSmartCollection, Ref: collectionID, Version: 3,
		SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64),
	}
	state := &scheduleStateFake{claimBatches: [][]DueScheduleClaim{{claim}}}
	descriptor := Descriptor{ID: "health.detector.collection", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeSmartCollection}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}
	registry, _ := NewRegistry([]Detector{descriptorDetector{descriptor}}, nil)
	scans := &scheduledScanFake{err: scanScopeStale(errors.New("collection version changed"))}
	service, err := NewScheduleDispatcher(state, scans, registry)
	if err != nil {
		t.Fatal(err)
	}
	if count, dispatchErr := service.DispatchDue(context.Background(), 1); count != 0 || !hasScanCode(dispatchErr, domain.ErrorCodeScanScopeStale) || state.acks != 0 || state.releases != 1 {
		t.Fatalf("count=%d acks=%d releases=%d err=%v", count, state.acks, state.releases, dispatchErr)
	}
	if !scans.command.BindCurrentReadModel || scans.command.Scope.ReadModelRevision != "" || !scans.command.PreventScopeConcurrency {
		t.Fatalf("schedule scan command did not defer revision binding: %#v", scans.command)
	}
}

func TestSmartCollectionScheduleFreezesCurrentRevisionAndExactCountAtDueStart(t *testing.T) {
	workspaceID, collectionID := testID(73), testID(74)
	due := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	claim := scheduleClaimFixture(workspaceID, testID(75), due)
	claim.Schedule.Scope = domain.ScanScope{
		Type: domain.ScanScopeTypeSmartCollection, Ref: collectionID, Version: 4,
		SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64),
	}
	state := &scheduleStateFake{claimBatches: [][]DueScheduleClaim{{claim}}}
	descriptor := Descriptor{ID: "health.detector.collection", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeSmartCollection}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}
	registry, _ := NewRegistry([]Detector{descriptorDetector{descriptor}}, nil)
	starter := &scanStartFake{}
	membership := &smartCollectionMembershipFake{binding: SmartCollectionBinding{
		WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 4,
		QueryHash: claim.Schedule.Scope.Hash, ReadModelRevision: strings.Repeat("b", 64), ExactCount: 2,
	}}
	scans, err := NewSmartCollectionScanService(starter, &scanStateFake{}, registry, membership)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewScheduleDispatcher(state, scans, registry)
	if err != nil {
		t.Fatal(err)
	}
	count, err := service.DispatchDue(context.Background(), 1)
	if err != nil || count != 1 || state.acks != 1 || state.releases != 0 {
		t.Fatalf("count=%d acks=%d releases=%d err=%v", count, state.acks, state.releases, err)
	}
	if starter.request.Scope.ReadModelRevision != membership.binding.ReadModelRevision || starter.request.Scope.ExactCount != membership.binding.ExactCount {
		t.Fatalf("scope=%#v binding=%#v", starter.request.Scope, membership.binding)
	}
	if claim.Schedule.Scope.ReadModelRevision != "" || claim.Schedule.Scope.ExactCount != 0 {
		t.Fatalf("schedule was mutated with per-run binding: %#v", claim.Schedule.Scope)
	}
}

func TestScheduleDispatcherReplaysSameScanAfterAckFailure(t *testing.T) {
	workspaceID := testID(85)
	due := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	claim := scheduleClaimFixture(workspaceID, testID(86), due)
	state := &scheduleStateFake{
		claimBatches: [][]DueScheduleClaim{{claim}, {claim}},
		ackErrors:    []error{errors.New("ack response was lost"), nil},
	}
	registry, _ := NewRegistry([]Detector{descriptorDetector{Descriptor{ID: "health.detector.ack", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}}}, nil)
	scans := &scheduledScanFake{}
	service, err := NewScheduleDispatcher(state, scans, registry)
	if err != nil {
		t.Fatal(err)
	}
	if count, dispatchErr := service.DispatchDue(context.Background(), 1); count != 0 || dispatchErr == nil {
		t.Fatalf("first count=%d err=%v", count, dispatchErr)
	}
	firstKey := scans.commands[0].IdempotencyKey
	if count, dispatchErr := service.DispatchDue(context.Background(), 1); count != 1 || dispatchErr != nil {
		t.Fatalf("second count=%d err=%v", count, dispatchErr)
	}
	if len(scans.commands) != 2 || scans.commands[1].IdempotencyKey != firstKey || state.acks != 2 || state.releases != 1 {
		t.Fatalf("commands=%#v acks=%d releases=%d", scans.commands, state.acks, state.releases)
	}
}

func TestScheduleDispatcherReleasesWithContextWithoutCancel(t *testing.T) {
	workspaceID := testID(87)
	due := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	state := &scheduleStateFake{claimBatches: [][]DueScheduleClaim{{scheduleClaimFixture(workspaceID, testID(88), due)}}}
	registry, _ := NewRegistry([]Detector{descriptorDetector{Descriptor{ID: "health.detector.cancel", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	service, err := NewScheduleDispatcher(state, cancellingScheduledScanFake{cancel: cancel}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if count, dispatchErr := service.DispatchDue(ctx, 1); count != 0 || dispatchErr == nil || state.releaseContextErr != nil {
		t.Fatalf("count=%d release context err=%v dispatch err=%v", count, state.releaseContextErr, dispatchErr)
	}
}

type scheduleStateFake struct {
	created           ScheduleCreateCommand
	claimBatches      [][]DueScheduleClaim
	claimCalls        int
	ackErrors         []error
	acks              int
	releases          int
	releaseContextErr error
}

func (state *scheduleStateFake) Create(_ context.Context, command ScheduleCreateCommand) (domain.Schedule, error) {
	state.created = command
	return domain.Schedule{}, nil
}
func (state *scheduleStateFake) Update(context.Context, ScheduleUpdateCommand) (domain.Schedule, error) {
	return domain.Schedule{}, nil
}
func (state *scheduleStateFake) Get(context.Context, foundation.ID, foundation.ID) (domain.Schedule, error) {
	return domain.Schedule{}, nil
}
func (state *scheduleStateFake) ClaimDue(context.Context, int) ([]DueScheduleClaim, error) {
	if state.claimCalls >= len(state.claimBatches) {
		return nil, nil
	}
	claims := state.claimBatches[state.claimCalls]
	state.claimCalls++
	return claims, nil
}
func (state *scheduleStateFake) AcknowledgeDue(context.Context, DueScheduleClaim) error {
	index := state.acks
	state.acks++
	if index < len(state.ackErrors) {
		return state.ackErrors[index]
	}
	return nil
}
func (state *scheduleStateFake) ReleaseDue(ctx context.Context, _ DueScheduleClaim) error {
	state.releases++
	state.releaseContextErr = ctx.Err()
	return nil
}

type scheduledScanFake struct {
	command  ScanStartCommand
	commands []ScanStartCommand
	err      error
}

func (fake *scheduledScanFake) Start(_ context.Context, command ScanStartCommand) (ScanStartResult, error) {
	fake.command = command
	fake.commands = append(fake.commands, command)
	return ScanStartResult{}, fake.err
}

type failingScheduledScanFake struct{}

func (failingScheduledScanFake) Start(context.Context, ScanStartCommand) (ScanStartResult, error) {
	return ScanStartResult{}, errors.New("transient scan start failure")
}

type cancellingScheduledScanFake struct {
	cancel context.CancelFunc
}

func (fake cancellingScheduledScanFake) Start(context.Context, ScanStartCommand) (ScanStartResult, error) {
	fake.cancel()
	return ScanStartResult{}, context.Canceled
}

func scheduleClaimFixture(workspaceID, scheduleID foundation.ID, due time.Time) DueScheduleClaim {
	next := due.Add(24 * time.Hour)
	return DueScheduleClaim{
		Schedule: domain.Schedule{
			ID: scheduleID, WorkspaceID: workspaceID,
			Scope:   domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
			Cadence: domain.ScheduleCadenceDaily, Timezone: "UTC", MaxItems: 100,
			NextRunAt: &next, Version: 2,
			CreatedAt: due.Add(-time.Hour), UpdatedAt: due,
		},
		DueAt: due, ClaimedAt: due, LeaseUntil: due.Add(2 * time.Minute),
	}
}
