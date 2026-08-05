package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitsyncapplication "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	gitsyncdomain "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

func TestDispatchGitSyncOutboxRedactsUnderlyingFailure(t *testing.T) {
	secret := "https://user:token@example.invalid/private.git"
	worker := &gitSyncWorkerFake{results: []gitSyncWorkerResult{{err: errors.New(secret)}}}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	processed, err := dispatchGitSyncOutbox(context.Background(), logger, worker, gitSyncDispatchPeriodicPhase)
	if err == nil || processed != 0 || worker.calls != 1 {
		t.Fatalf("processed=%d calls=%d err=%v", processed, worker.calls, err)
	}
	logged := output.String()
	if !strings.Contains(logged, `"error_code":"GIT_SYNC_OUTBOX_DISPATCH_FAILED"`) ||
		!strings.Contains(logged, `"processed_count":0`) || strings.Contains(logged, secret) {
		t.Fatalf("dispatch log=%s", logged)
	}
}

func TestGitSyncWorkerReadinessDistinguishesDisabledAndPartialComposition(t *testing.T) {
	disabled := workerComponents{gitSyncCapability: agentCapabilityStatus{code: gitsyncdomain.ErrorCodeUnavailable}}
	if !gitSyncWorkerReadiness(disabled) {
		t.Fatal("explicitly disabled Git sync must not make the worker unready")
	}
	if gitSyncWorkerReadiness(workerComponents{}) {
		t.Fatal("missing Git sync capability state reported ready")
	}
	if gitSyncWorkerReadiness(workerComponents{gitSyncCapability: agentCapabilityStatus{available: true}}) {
		t.Fatal("enabled Git sync without a worker reported ready")
	}
	configured := workerComponents{
		gitSyncWorker:     new(gitsyncapplication.Worker),
		gitSyncScheduler:  new(gitsyncapplication.AutoSyncScheduler),
		gitSyncCapability: agentCapabilityStatus{available: true},
	}
	if !gitSyncWorkerReadiness(configured) {
		t.Fatal("complete Git sync worker composition reported unavailable")
	}
}

func TestNewGitSyncWorkerRejectsInvalidConfiguredKeyWithoutLeakingPath(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "git-sync.key")
	if err := os.WriteFile(keyPath, []byte("not-a-valid-secret-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.GitSyncKeyFile = keyPath

	worker, scheduler, err := newGitSyncWorker(nil, cfg, nil, nil, sourceProcessingComponents{}, nil, "")
	if err == nil || worker != nil || scheduler != nil {
		t.Fatalf("worker=%v scheduler=%v err=%v", worker, scheduler, err)
	}
	if strings.Contains(err.Error(), keyPath) || strings.Contains(err.Error(), "not-a-valid-secret-key") {
		t.Fatalf("credential error leaked secret material: %v", err)
	}
}

func TestGitSyncDispatchHasIndependentBoundedBudget(t *testing.T) {
	if gitSyncScheduleBatchSize < 1 || gitSyncScheduleBatchSize > gitsyncapplication.MaxAutoSyncBatch {
		t.Fatalf("schedule batch size=%d", gitSyncScheduleBatchSize)
	}
	if gitSyncDispatchBatchSize < 1 || gitSyncDispatchBatchSize > 100 {
		t.Fatalf("batch size=%d", gitSyncDispatchBatchSize)
	}
	if gitSyncDispatchTimeout <= 0 || gitSyncDispatchTimeout > config.Defaults().WorkerHardStopTimeout {
		t.Fatalf("dispatch timeout=%s hard stop=%s", gitSyncDispatchTimeout, config.Defaults().WorkerHardStopTimeout)
	}
	if gitSyncScheduleTimeout <= 0 || gitSyncScheduleTimeout >= gitSyncDispatchTimeout {
		t.Fatalf("schedule timeout=%s dispatch timeout=%s", gitSyncScheduleTimeout, gitSyncDispatchTimeout)
	}
}

func TestDispatchGitSyncSchedulesBeforeDraining(t *testing.T) {
	events := make([]string, 0, 2)
	scheduler := &gitSyncSchedulerFake{events: &events, result: gitsyncapplication.AutoSyncBatchResult{Scanned: 1, Scheduled: 1}}
	worker := &orderedGitSyncWorkerFake{events: &events}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	batch, processed, err := dispatchGitSync(context.Background(), logger, scheduler, worker, gitSyncDispatchStartupPhase)
	if err != nil || batch != scheduler.result || processed != 0 {
		t.Fatalf("batch=%+v processed=%d err=%v", batch, processed, err)
	}
	if scheduler.limit != gitSyncScheduleBatchSize || len(events) != 2 || events[0] != "schedule" || events[1] != "drain" {
		t.Fatalf("limit=%d events=%v", scheduler.limit, events)
	}
	if !strings.Contains(output.String(), `"msg":"Git automatic sync scheduling completed"`) {
		t.Fatalf("dispatch log=%s", output.String())
	}
}

func TestDispatchGitSyncDrainsAfterRedactedSchedulerFailure(t *testing.T) {
	secret := "https://token@example.invalid/private.git"
	events := make([]string, 0, 2)
	scheduler := &gitSyncSchedulerFake{events: &events, err: errors.New(secret)}
	worker := &orderedGitSyncWorkerFake{events: &events}
	var output bytes.Buffer

	_, _, err := dispatchGitSync(
		context.Background(), slog.New(slog.NewJSONHandler(&output, nil)), scheduler, worker, gitSyncDispatchPeriodicPhase,
	)
	if err == nil || len(events) != 2 || events[0] != "schedule" || events[1] != "drain" {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), `"msg":"Git automatic sync scheduling failed"`) {
		t.Fatalf("dispatch log=%s", output.String())
	}
}

func TestGitSyncDispatchLoopCancelsInFlightWorkBeforeHardStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan string, 2)
	scheduler := channelGitSyncScheduler{events: events}
	worker := channelGitSyncWorker{events: events}
	stopped := startGitSyncDispatchLoop(
		ctx, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), scheduler, worker,
		time.Hour, gitSyncDispatchTimeout, func() bool { return true },
	)
	for _, want := range []string{"schedule", "drain"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event=%q want=%q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Git sync loop did not stop after context cancellation")
	}
}

type gitSyncSchedulerFake struct {
	events *[]string
	result gitsyncapplication.AutoSyncBatchResult
	err    error
	limit  int
}

func (fake *gitSyncSchedulerFake) ScheduleBatch(_ context.Context, limit int) (gitsyncapplication.AutoSyncBatchResult, error) {
	*fake.events = append(*fake.events, "schedule")
	fake.limit = limit
	return fake.result, fake.err
}

type orderedGitSyncWorkerFake struct{ events *[]string }

func (fake *orderedGitSyncWorkerFake) RunOnce(context.Context) (bool, error) {
	*fake.events = append(*fake.events, "drain")
	return false, nil
}

type channelGitSyncScheduler struct{ events chan<- string }

func (fake channelGitSyncScheduler) ScheduleBatch(context.Context, int) (gitsyncapplication.AutoSyncBatchResult, error) {
	fake.events <- "schedule"
	return gitsyncapplication.AutoSyncBatchResult{}, nil
}

type channelGitSyncWorker struct{ events chan<- string }

func (fake channelGitSyncWorker) RunOnce(ctx context.Context) (bool, error) {
	fake.events <- "drain"
	<-ctx.Done()
	return false, ctx.Err()
}
