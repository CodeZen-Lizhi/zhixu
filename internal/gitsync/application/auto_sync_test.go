package application

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	autoCommitA foundation.ID = "78100000-0000-4000-8000-000000000001"
	autoCommitB foundation.ID = "78100000-0000-4000-8000-000000000002"
	autoCommitC foundation.ID = "78100000-0000-4000-8000-000000000003"
)

func TestAutoSyncSchedulerCreatesIdempotentRunsAndDefersActiveWorkspace(t *testing.T) {
	source := autoCandidateSourceStub{candidates: []AutoSyncCandidate{
		{WorkspaceID: executorWorkspaceID, ProposalCommitID: autoCommitA, ConfigRevision: 4},
		{WorkspaceID: executorWorkspaceID, ProposalCommitID: autoCommitB, ConfigRevision: 4},
		{WorkspaceID: executorWorkspaceID, ProposalCommitID: autoCommitC, ConfigRevision: 5},
	}}
	runs := &automaticRunCreatorStub{results: []automaticRunResult{
		{},
		{receipt: RunReceipt{Replayed: true}},
		{err: foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRunActive, false, errors.New("active"))},
	}}
	scheduler, err := NewAutoSyncScheduler(source, runs)
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.ScheduleBatch(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if result != (AutoSyncBatchResult{Scanned: 3, Scheduled: 1, Replayed: 1, Deferred: 1}) {
		t.Fatalf("result=%+v", result)
	}
	for index, command := range runs.commands {
		if command.Trigger != domain.TriggerAutomatic || command.WorkspaceID != executorWorkspaceID ||
			command.ExpectedConfigRevision != source.candidates[index].ConfigRevision ||
			command.IdempotencyKey != autoSyncKeyPrefix+string(source.candidates[index].ProposalCommitID)+":config:"+strconv.FormatInt(source.candidates[index].ConfigRevision, 10) {
			t.Fatalf("command[%d]=%+v", index, command)
		}
	}
}

func TestAutoSyncSchedulerRejectsInvalidCandidateAndUnexpectedFailure(t *testing.T) {
	t.Run("invalid candidate", func(t *testing.T) {
		scheduler, err := NewAutoSyncScheduler(
			autoCandidateSourceStub{candidates: []AutoSyncCandidate{{WorkspaceID: executorWorkspaceID}}},
			&automaticRunCreatorStub{},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = scheduler.ScheduleBatch(context.Background(), 1)
		if code, ok := errorCode(err); !ok || code != domain.ErrorCodeCorrupt {
			t.Fatalf("err=%v code=%q", err, code)
		}
	})
	t.Run("unexpected create failure", func(t *testing.T) {
		cause := foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("database"))
		scheduler, err := NewAutoSyncScheduler(
			autoCandidateSourceStub{candidates: []AutoSyncCandidate{{WorkspaceID: executorWorkspaceID, ProposalCommitID: autoCommitA, ConfigRevision: 1}}},
			&automaticRunCreatorStub{results: []automaticRunResult{{err: cause}}},
		)
		if err != nil {
			t.Fatal(err)
		}
		result, err := scheduler.ScheduleBatch(context.Background(), 1)
		if !errors.Is(err, cause) || result.Scanned != 1 || result.Scheduled != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

type autoCandidateSourceStub struct {
	candidates []AutoSyncCandidate
	err        error
}

func (source autoCandidateSourceStub) ListAutoSyncCandidates(context.Context, int) ([]AutoSyncCandidate, error) {
	return append([]AutoSyncCandidate(nil), source.candidates...), source.err
}

type automaticRunResult struct {
	receipt RunReceipt
	err     error
}

type automaticRunCreatorStub struct {
	commands []CreateRunCommand
	results  []automaticRunResult
}

func (creator *automaticRunCreatorStub) CreateRun(_ context.Context, command CreateRunCommand) (RunReceipt, error) {
	creator.commands = append(creator.commands, command)
	index := len(creator.commands) - 1
	if index >= len(creator.results) {
		return RunReceipt{}, nil
	}
	return creator.results[index].receipt, creator.results[index].err
}
