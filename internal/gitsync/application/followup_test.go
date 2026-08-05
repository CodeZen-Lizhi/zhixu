package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const followupIndexID foundation.ID = "40000000-0000-4000-8000-000000000001"

type followupStoreStub struct {
	run           domain.SyncRun
	completeErr   error
	completeCalls int
	failCalls     int
}

func (store *followupStoreStub) BeginIndexFollowup(context.Context, domain.OutboxLease, int64) (domain.SyncRun, bool, error) {
	store.run.IndexStatus = domain.IndexRunning
	store.run.Version++
	return store.run, true, nil
}

func (store *followupStoreStub) CompleteIndexFollowup(_ context.Context, _ domain.OutboxLease, _ int64, indexVersionID foundation.ID, _ bool) (domain.SyncRun, error) {
	store.completeCalls++
	if store.completeErr != nil {
		return domain.SyncRun{}, store.completeErr
	}
	store.run.IndexStatus = domain.IndexSucceeded
	store.run.IndexVersionID = indexVersionID
	store.run.Version++
	return store.run, nil
}

func (store *followupStoreStub) FailIndexFollowup(_ context.Context, _ domain.OutboxLease, _ int64, code string, retryable bool) (domain.SyncRun, error) {
	store.failCalls++
	store.run.IndexStatus = domain.IndexFailed
	store.run.IndexErrorCode = code
	store.run.IndexRetryable = retryable
	store.run.Version++
	return store.run, nil
}

type captureStub struct {
	result  ExternalChangeResult
	err     error
	command ExternalChangeCommand
}

func (capture *captureStub) CaptureGitFastForward(_ context.Context, command ExternalChangeCommand) (ExternalChangeResult, error) {
	capture.command = command
	return capture.result, capture.err
}

func TestFollowupExecutorPersistsIndependentSuccess(t *testing.T) {
	store := &followupStoreStub{run: followupRun()}
	capture := &captureStub{result: ExternalChangeResult{IndexVersionID: followupIndexID}}
	executor, err := NewFollowupExecutor(store, capture)
	if err != nil {
		t.Fatal(err)
	}
	run, err := executor.Execute(context.Background(), followupLease())
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexSucceeded || run.IndexVersionID != followupIndexID ||
		capture.command.BeforeCommit != run.ExpectedHeadOID || capture.command.AfterCommit != run.VerifiedHeadOID {
		t.Fatalf("run=%+v command=%+v", run, capture.command)
	}
}

func TestFollowupExecutorPersistsExplicitNoIndexSuccess(t *testing.T) {
	store := &followupStoreStub{run: followupRun()}
	capture := &captureStub{result: ExternalChangeResult{NoIndexRequired: true}}
	executor, err := NewFollowupExecutor(store, capture)
	if err != nil {
		t.Fatal(err)
	}
	run, err := executor.Execute(context.Background(), followupLease())
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexSucceeded || run.IndexVersionID != "" || store.failCalls != 0 {
		t.Fatalf("run=%+v fail_calls=%d", run, store.failCalls)
	}
}

func TestFollowupExecutorConvergesInvalidSuccessToIndexFailure(t *testing.T) {
	store := &followupStoreStub{run: followupRun()}
	executor, err := NewFollowupExecutor(store, &captureStub{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := executor.Execute(context.Background(), followupLease())
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexFailed || run.IndexErrorCode != domain.ErrorCodeCorrupt ||
		store.completeCalls != 0 || store.failCalls != 1 {
		t.Fatalf("run=%+v complete_calls=%d fail_calls=%d", run, store.completeCalls, store.failCalls)
	}
}

func TestFollowupExecutorConvergesCompletionFailure(t *testing.T) {
	store := &followupStoreStub{
		run: followupRun(),
		completeErr: foundation.NewError(
			foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("index receipt unavailable"),
		),
	}
	executor, err := NewFollowupExecutor(store, &captureStub{result: ExternalChangeResult{IndexVersionID: followupIndexID}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := executor.Execute(context.Background(), followupLease())
	if err != nil {
		t.Fatal(err)
	}
	if run.IndexStatus != domain.IndexFailed || run.IndexErrorCode != domain.ErrorCodeUnavailable || !run.IndexRetryable ||
		store.completeCalls != 1 || store.failCalls != 1 {
		t.Fatalf("run=%+v complete_calls=%d fail_calls=%d", run, store.completeCalls, store.failCalls)
	}
}

func followupRun() domain.SyncRun {
	now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	return domain.SyncRun{
		ID: executorRunID, WorkspaceID: executorWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: domain.TriggerManual,
		IdempotencyKey: "followup-run", RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status: domain.RunSucceeded, Direction: domain.DirectionPull, FailureClass: domain.FailureNone,
		ExpectedHeadOID:   "1111111111111111111111111111111111111111",
		ExpectedRemoteOID: "2222222222222222222222222222222222222222",
		VerifiedHeadOID:   "2222222222222222222222222222222222222222",
		VerifiedRemoteOID: "2222222222222222222222222222222222222222",
		IndexStatus:       domain.IndexPending, AttemptCount: 1, Version: 4,
		CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
	}
}

func followupLease() domain.OutboxLease {
	return domain.OutboxLease{
		ID: executorAttemptID, WorkspaceID: executorWorkspaceID, RunID: executorRunID,
		Kind: domain.OutboxIndexFollowup, Owner: "worker-1", AttemptCount: 1,
		Version: 2, LeaseUntil: time.Now().Add(time.Minute),
	}
}
