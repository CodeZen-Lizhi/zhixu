package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisRunServiceCreatesFrozenQueuedRun(t *testing.T) {
	repository := &workspaceAnalysisRunPersistenceFake{}
	ids := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}
	config := workspaceAnalysisRunStartTestConfig()
	service, err := NewScopedWorkspaceAnalysisRunService(repository, ids, config)
	if err != nil {
		t.Fatal(err)
	}
	command := workspaceAnalysisRunStartTestCommand(false)
	transaction := &workspaceAnalysisRunTransaction{}
	run, err := service.StartWorkspaceAnalysisRunScoped(context.Background(), transaction, command)
	if err != nil {
		t.Fatal(err)
	}
	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(config.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if ids.calls != 1 || repository.insertCalls != 1 || repository.findCalls != 0 || repository.transaction != transaction ||
		run.ID != ids.id || run.Status != domain.WorkspaceAnalysisRunQueued || run.Version != 1 ||
		run.DefinitionHash != config.DefinitionHash || run.ToolCatalogHash != config.ToolCatalogHash ||
		run.ConfigRevision != config.ConfigRevision || !run.DeadlineAt.Equal(command.CreatedAt.Add(deadlines.RunDeadline())) ||
		run.Limits.Amount.OutputTokens != WorkspaceAnalysisV1MaxRunOutputTokens || run.Limits.Amount.InputTokens != WorkspaceAnalysisV1MaxRunInputTokens {
		t.Fatalf("run=%#v ids=%d insert=%d find=%d", run, ids.calls, repository.insertCalls, repository.findCalls)
	}
}

func TestWorkspaceAnalysisRunServiceAcceptsDatabaseTimeNormalization(t *testing.T) {
	repository := &workspaceAnalysisRunPersistenceFake{normalizeTimes: true}
	ids := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}
	service, err := NewScopedWorkspaceAnalysisRunService(repository, ids, workspaceAnalysisRunStartTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.StartWorkspaceAnalysisRunScoped(
		context.Background(),
		&workspaceAnalysisRunTransaction{},
		workspaceAnalysisRunStartTestCommand(false),
	)
	if err != nil {
		t.Fatal(err)
	}
	if run.CreatedAt.Location() == time.UTC || !run.CreatedAt.Equal(workspaceAnalysisRunStartTestCommand(false).CreatedAt) {
		t.Fatalf("normalized created_at=%s location=%s", run.CreatedAt, run.CreatedAt.Location())
	}
}

func TestWorkspaceAnalysisRunServiceReplayRequiresExactExistingBinding(t *testing.T) {
	config := workspaceAnalysisRunStartTestConfig()
	command := workspaceAnalysisRunStartTestCommand(false)
	seedRepository := &workspaceAnalysisRunPersistenceFake{}
	seedIDs := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}
	seedService, err := NewScopedWorkspaceAnalysisRunService(seedRepository, seedIDs, config)
	if err != nil {
		t.Fatal(err)
	}
	transaction := &workspaceAnalysisRunTransaction{}
	seeded, err := seedService.StartWorkspaceAnalysisRunScoped(context.Background(), transaction, command)
	if err != nil {
		t.Fatal(err)
	}
	seeded.Status = domain.WorkspaceAnalysisRunRunning
	seeded.Version = 2
	seeded.UpdatedAt = seeded.UpdatedAt.Add(time.Second)

	tests := []struct {
		name   string
		found  bool
		mutate func(*domain.WorkspaceAnalysisRun)
		valid  bool
	}{
		{name: "exact progressed run", found: true, valid: true},
		{name: "missing", found: false},
		{name: "historical frozen config remains replayable", found: true, valid: true, mutate: func(run *domain.WorkspaceAnalysisRun) {
			run.DefinitionHash = hashHex('c')
			run.ToolCatalogHash = hashHex('d')
			run.ConfigRevision++
		}},
		{name: "malformed definition hash", found: true, mutate: func(run *domain.WorkspaceAnalysisRun) { run.DefinitionHash = "invalid" }},
		{name: "answer drift", found: true, mutate: func(run *domain.WorkspaceAnalysisRun) { run.AnswerID = workspaceAnalysisRunApplicationID(21) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			existing := seeded
			if test.mutate != nil {
				test.mutate(&existing)
			}
			repository := &workspaceAnalysisRunPersistenceFake{existing: existing, found: test.found}
			ids := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(22)}
			service, err := NewScopedWorkspaceAnalysisRunService(repository, ids, config)
			if err != nil {
				t.Fatal(err)
			}
			replay := command
			replay.Replayed = true
			got, err := service.StartWorkspaceAnalysisRunScoped(context.Background(), transaction, replay)
			if test.valid {
				if err != nil || got.ID != seeded.ID || ids.calls != 0 || repository.insertCalls != 0 || repository.findCalls != 1 {
					t.Fatalf("run=%#v err=%v ids=%d insert=%d find=%d", got, err, ids.calls, repository.insertCalls, repository.findCalls)
				}
				return
			}
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
				classified.Code != ErrorCodeWorkspaceAnalysisRunStartConflict || ids.calls != 0 || repository.insertCalls != 0 {
				t.Fatalf("error=%#v ids=%d insert=%d", err, ids.calls, repository.insertCalls)
			}
		})
	}
}

func TestNewScopedWorkspaceAnalysisRunServiceRejectsUnsafeReadinessAndHashes(t *testing.T) {
	repository := &workspaceAnalysisRunPersistenceFake{}
	ids := &workspaceAnalysisRunIDGenerator{id: workspaceAnalysisRunApplicationID(20)}
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisRunStartConfig)
		code   string
	}{
		{name: "uppercase definition hash", mutate: func(config *WorkspaceAnalysisRunStartConfig) { config.DefinitionHash = hashHex('A') }, code: ErrorCodeWorkspaceAnalysisRunStartInvalid},
		{name: "negative revision", mutate: func(config *WorkspaceAnalysisRunStartConfig) { config.ConfigRevision = -1 }, code: ErrorCodeWorkspaceAnalysisRunStartInvalid},
		{name: "short river timeout", mutate: func(config *WorkspaceAnalysisRunStartConfig) { config.RuntimeLimits.RiverJobTimeout = time.Second }, code: ErrorCodeWorkspaceAnalysisRuntimeNotReady},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := workspaceAnalysisRunStartTestConfig()
			test.mutate(&config)
			_, err := NewScopedWorkspaceAnalysisRunService(repository, ids, config)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != test.code {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisRunExecutionQueryRejectsInvalidOrReusedIdentity(t *testing.T) {
	valid := WorkspaceAnalysisRunExecutionQuery{
		WorkspaceID: workspaceAnalysisRunApplicationID(1), WorkflowRunID: workspaceAnalysisRunApplicationID(2),
		ConversationID: workspaceAnalysisRunApplicationID(3), QuestionID: workspaceAnalysisRunApplicationID(4),
		AnswerID: workspaceAnalysisRunApplicationID(5),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisRunExecutionQuery)
	}{
		{name: "malformed", mutate: func(query *WorkspaceAnalysisRunExecutionQuery) { query.WorkflowRunID = "bad" }},
		{name: "reused", mutate: func(query *WorkspaceAnalysisRunExecutionQuery) { query.AnswerID = query.QuestionID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			test.mutate(&query)
			err := query.Validate()
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisRunExecutionInvalid {
				t.Fatalf("Validate error = %#v", err)
			}
		})
	}
}

type workspaceAnalysisRunPersistenceFake struct {
	existing       domain.WorkspaceAnalysisRun
	found          bool
	transaction    foundation.TransactionScope
	normalizeTimes bool
	insertCalls    int
	findCalls      int
}

func (fake *workspaceAnalysisRunPersistenceFake) InsertWorkspaceAnalysisRunScoped(_ context.Context, transaction foundation.TransactionScope, run domain.WorkspaceAnalysisRun) (domain.WorkspaceAnalysisRun, error) {
	fake.transaction = transaction
	fake.insertCalls++
	if fake.normalizeTimes {
		location := time.FixedZone("db", 8*60*60)
		run.CreatedAt = run.CreatedAt.In(location)
		run.UpdatedAt = run.UpdatedAt.In(location)
		run.DeadlineAt = run.DeadlineAt.In(location)
	}
	fake.existing = run
	fake.found = true
	return run, nil
}

func (fake *workspaceAnalysisRunPersistenceFake) FindWorkspaceAnalysisRunScoped(_ context.Context, transaction foundation.TransactionScope, _, _ foundation.ID) (domain.WorkspaceAnalysisRun, bool, error) {
	fake.transaction = transaction
	fake.findCalls++
	return fake.existing, fake.found, nil
}

type workspaceAnalysisRunIDGenerator struct {
	id    foundation.ID
	calls int
}

func (generator *workspaceAnalysisRunIDGenerator) New() (foundation.ID, error) {
	generator.calls++
	return generator.id, nil
}

type workspaceAnalysisRunTransaction struct{}

func (*workspaceAnalysisRunTransaction) TransactionScope() {}

func workspaceAnalysisRunStartTestConfig() WorkspaceAnalysisRunStartConfig {
	return WorkspaceAnalysisRunStartConfig{
		DefinitionHash:                  hashHex('a'),
		ToolCatalogHash:                 hashHex('b'),
		ConfigRevision:                  7,
		SynthesisProfileMaxOutputTokens: int(WorkspaceAnalysisV1SynthesisMaxOutputTokens),
		Timeouts: WorkspaceAnalysisV1Timeouts{
			PlanModelTimeout: 2 * time.Second, SynthesisModelTimeout: 3 * time.Second, ReviewModelTimeout: 2 * time.Second,
			GitToolTimeout: time.Second, SearchToolTimeout: time.Second, SourceReadToolTimeout: time.Second,
			ValidateCitationToolTimeout: time.Second,
		},
		RuntimeLimits: WorkspaceAnalysisRuntimeLimits{
			RiverJobTimeout: time.Minute, LeaseDuration: 30 * time.Second, HeartbeatInterval: 5 * time.Second,
		},
	}
}

func workspaceAnalysisRunStartTestCommand(replayed bool) WorkspaceAnalysisRunStartCommand {
	return WorkspaceAnalysisRunStartCommand{
		WorkspaceID: workspaceAnalysisRunApplicationID(1), ConversationID: workspaceAnalysisRunApplicationID(2),
		QuestionID: workspaceAnalysisRunApplicationID(3), AnswerID: workspaceAnalysisRunApplicationID(4),
		WorkflowRunID: workspaceAnalysisRunApplicationID(5),
		CreatedAt:     time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC), Replayed: replayed,
	}
}

func workspaceAnalysisRunApplicationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8a000000-0000-4000-8000-%012d", value))
}

func hashHex(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
