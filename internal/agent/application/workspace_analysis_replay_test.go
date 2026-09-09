package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisReplayServicePreservesPersistedVersions(t *testing.T) {
	for _, version := range []int64{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			run := workspaceAnalysisReplayTestRun(t, version)
			run.Status = domain.WorkspaceAnalysisRunRunning
			run.Version = 2
			run.UpdatedAt = run.UpdatedAt.Add(time.Second)
			repository := &workspaceAnalysisRunPersistenceFake{existing: run, found: true}
			service, err := NewScopedWorkspaceAnalysisRunReplayService(repository)
			if err != nil {
				t.Fatal(err)
			}
			scope := &workspaceAnalysisRunTransaction{}
			got, err := service.StartWorkspaceAnalysisRunScoped(t.Context(), scope, workspaceAnalysisRunStartTestCommand(true))
			if err != nil || !reflect.DeepEqual(got, run) || repository.insertCalls != 0 || repository.findCalls != 1 || repository.transaction != scope {
				t.Fatalf("persisted v%d replay changed its facts or transaction: %v", version, err)
			}
		})
	}
}

func TestWorkspaceAnalysisReplayServiceRejectsNewOrDriftedBindings(t *testing.T) {
	seeded := workspaceAnalysisReplayTestRun(t, 2)
	for _, test := range []struct {
		name   string
		found  bool
		mutate func(*domain.WorkspaceAnalysisRun)
	}{
		{name: "missing"},
		{name: "malformed stored run", found: true, mutate: func(run *domain.WorkspaceAnalysisRun) { run.DefinitionHash = "invalid" }},
		{name: "answer changed", found: true, mutate: func(run *domain.WorkspaceAnalysisRun) { run.AnswerID = workspaceAnalysisRunApplicationID(21) }},
		{name: "created time changed", found: true, mutate: func(run *domain.WorkspaceAnalysisRun) { run.CreatedAt = run.CreatedAt.Add(time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := seeded
			if test.mutate != nil {
				test.mutate(&run)
			}
			repository := &workspaceAnalysisRunPersistenceFake{existing: run, found: test.found}
			service, err := NewScopedWorkspaceAnalysisRunReplayService(repository)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.StartWorkspaceAnalysisRunScoped(t.Context(), &workspaceAnalysisRunTransaction{}, workspaceAnalysisRunStartTestCommand(true))
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisRunStartConflict || repository.insertCalls != 0 {
				t.Fatalf("drifted replay was accepted: %v", err)
			}
		})
	}

	repository := &workspaceAnalysisRunPersistenceFake{existing: seeded, found: true}
	service, err := NewScopedWorkspaceAnalysisRunReplayService(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.StartWorkspaceAnalysisRunScoped(t.Context(), &workspaceAnalysisRunTransaction{}, workspaceAnalysisRunStartTestCommand(false))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisRunStartInvalid || repository.insertCalls != 0 || repository.findCalls != 0 {
		t.Fatalf("replay-only service accepted a new dispatch: %v", err)
	}
	var absent *workspaceAnalysisRunPersistenceFake
	if _, err := NewScopedWorkspaceAnalysisRunReplayService(absent); err == nil {
		t.Fatal("typed-nil replay persistence was accepted")
	}
}

func TestWorkspaceAnalysisReplayServiceRejectsMissingScopeOrCancelledContext(t *testing.T) {
	repository := &workspaceAnalysisRunPersistenceFake{}
	service, err := NewScopedWorkspaceAnalysisRunReplayService(repository)
	if err != nil {
		t.Fatal(err)
	}
	var absent *workspaceAnalysisRunTransaction
	for _, scope := range []foundation.TransactionScope{nil, absent} {
		if _, err := service.StartWorkspaceAnalysisRunScoped(t.Context(), scope, workspaceAnalysisRunStartTestCommand(true)); err == nil {
			t.Fatal("replay accepted a missing caller-owned transaction")
		}
	}
	if _, err := service.StartWorkspaceAnalysisRunScoped(nil, &workspaceAnalysisRunTransaction{}, workspaceAnalysisRunStartTestCommand(true)); err == nil {
		t.Fatal("replay accepted a nil context")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.StartWorkspaceAnalysisRunScoped(ctx, &workspaceAnalysisRunTransaction{}, workspaceAnalysisRunStartTestCommand(true)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled replay lost its cause: %v", err)
	}
	if repository.findCalls != 0 || repository.insertCalls != 0 {
		t.Fatal("invalid replay reached persistence")
	}
}

func workspaceAnalysisReplayTestRun(t *testing.T, version int64) domain.WorkspaceAnalysisRun {
	t.Helper()
	repository := &workspaceAnalysisRunPersistenceFake{}
	ids := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}
	config := workspaceAnalysisRunStartTestConfig()
	var starter ScopedWorkspaceAnalysisRunStarter
	var err error
	if version == 1 {
		starter, err = NewScopedWorkspaceAnalysisRunService(repository, ids, config)
	} else {
		deadlines, deadlineErr := domain.DeriveWorkspaceAnalysisV2Deadlines(config.Timeouts)
		if deadlineErr != nil {
			t.Fatal(deadlineErr)
		}
		config.RuntimeLimits.RiverJobTimeout = deadlines.MinimumRiverJobTimeout()
		starter, err = NewScopedWorkspaceAnalysisRunServiceV2(repository, ids, config)
	}
	if err != nil {
		t.Fatal(err)
	}
	run, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &workspaceAnalysisRunTransaction{}, workspaceAnalysisRunStartTestCommand(false))
	if err != nil {
		t.Fatal(err)
	}
	return run
}
