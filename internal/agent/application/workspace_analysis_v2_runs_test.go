package application

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisV2RunServiceAcceptsDatabaseTimeNormalization(t *testing.T) {
	repository := &workspaceAnalysisRunPersistenceFake{normalizeTimes: true}
	ids := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}
	config := workspaceAnalysisRunStartTestConfig()
	config.RuntimeLimits.RiverJobTimeout = 10 * time.Minute
	service, err := NewScopedWorkspaceAnalysisRunServiceV2(repository, ids, config)
	if err != nil {
		t.Fatal(err)
	}
	command := workspaceAnalysisRunStartTestCommand(false)
	run, err := service.StartWorkspaceAnalysisRunScoped(context.Background(), &workspaceAnalysisRunTransaction{}, command)
	if err != nil {
		t.Fatalf("same-instant database timestamps rejected a new v2 run: %v", err)
	}
	if run.CreatedAt.Location() == time.UTC || !run.CreatedAt.Equal(command.CreatedAt) || run.DefinitionVersion != 2 ||
		run.PolicyVersion != 2 || run.Status != domain.WorkspaceAnalysisRunQueued || ids.calls != 1 || repository.insertCalls != 1 {
		t.Fatal("normalized v2 run lost its frozen creation binding")
	}
}

func TestWorkspaceAnalysisV2RunServiceRejectsPersistedRunDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.WorkspaceAnalysisRun)
	}{
		{name: "different instant", mutate: func(run *domain.WorkspaceAnalysisRun) {
			run.CreatedAt = run.CreatedAt.Add(time.Microsecond)
			run.UpdatedAt = run.UpdatedAt.Add(time.Microsecond)
			run.DeadlineAt = run.DeadlineAt.Add(time.Microsecond)
		}},
		{name: "config revision", mutate: func(run *domain.WorkspaceAnalysisRun) { run.ConfigRevision++ }},
		{name: "catalog", mutate: func(run *domain.WorkspaceAnalysisRun) { run.ToolCatalogHash = hashHex('c') }},
		{name: "already running", mutate: func(run *domain.WorkspaceAnalysisRun) {
			run.Status, run.Version = domain.WorkspaceAnalysisRunRunning, 2
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &workspaceAnalysisV2MutatingRunPersistence{mutate: test.mutate}
			config := workspaceAnalysisRunStartTestConfig()
			config.RuntimeLimits.RiverJobTimeout = 10 * time.Minute
			service, err := NewScopedWorkspaceAnalysisRunServiceV2(repository, &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}, config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.StartWorkspaceAnalysisRunScoped(context.Background(), &workspaceAnalysisRunTransaction{}, workspaceAnalysisRunStartTestCommand(false)); err == nil {
				t.Fatal("persisted v2 binding drift was accepted")
			}
		})
	}
}

type workspaceAnalysisV2MutatingRunPersistence struct {
	workspaceAnalysisRunPersistenceFake
	mutate func(*domain.WorkspaceAnalysisRun)
}

func (repository *workspaceAnalysisV2MutatingRunPersistence) InsertWorkspaceAnalysisRunScoped(ctx context.Context, scope foundation.TransactionScope, run domain.WorkspaceAnalysisRun) (domain.WorkspaceAnalysisRun, error) {
	persisted, err := repository.workspaceAnalysisRunPersistenceFake.InsertWorkspaceAnalysisRunScoped(ctx, scope, run)
	if err == nil {
		repository.mutate(&persisted)
	}
	return persisted, err
}
