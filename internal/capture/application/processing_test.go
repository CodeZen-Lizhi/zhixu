package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestDispatcherBindsWorkflowIdempotencyToOutboxEvent(t *testing.T) {
	workspaceID := foundation.ID("53000000-0000-4000-8000-000000000001")
	captureID := foundation.ID("53000000-0000-4000-8000-000000000002")
	outbox := &dispatcherOutbox{leases: []OutboxLease{
		dispatcherLease(workspaceID, captureID, "53000000-0000-4000-8000-000000000003"),
		dispatcherLease(workspaceID, captureID, "53000000-0000-4000-8000-000000000004"),
	}}
	workflows := &dispatcherWorkflows{runIDs: []foundation.ID{
		"53000000-0000-4000-8000-000000000005", "53000000-0000-4000-8000-000000000006",
	}}
	dispatcher, err := NewOutboxDispatcher(DispatcherDependencies{
		Outbox: outbox, Workflows: workflows, Owner: "capture-test",
		LeaseDuration: time.Minute, RetryBase: time.Second, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.DispatchBatch(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed != 2 || result.Started != 2 || len(workflows.commands) != 2 || len(outbox.published) != 2 {
		t.Fatalf("result=%#v starts=%d published=%d", result, len(workflows.commands), len(outbox.published))
	}
	if workflows.commands[0].IdempotencyKey == workflows.commands[1].IdempotencyKey ||
		workflows.commands[0].IdempotencyKey != "capture-process:"+string(captureID)+":"+string(outbox.leases[0].ID) ||
		workflows.commands[1].IdempotencyKey != "capture-process:"+string(captureID)+":"+string(outbox.leases[1].ID) {
		t.Fatalf("idempotency keys = %q, %q", workflows.commands[0].IdempotencyKey, workflows.commands[1].IdempotencyKey)
	}
}

func TestDispatcherReschedulesOnlyRetryableStartFailure(t *testing.T) {
	workspaceID := foundation.ID("53000000-0000-4000-8000-000000000001")
	captureID := foundation.ID("53000000-0000-4000-8000-000000000002")
	outbox := &dispatcherOutbox{leases: []OutboxLease{dispatcherLease(workspaceID, captureID, "53000000-0000-4000-8000-000000000003")}}
	workflows := &dispatcherWorkflows{err: foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_BUSY", true, errors.New("busy"))}
	dispatcher, err := NewOutboxDispatcher(DispatcherDependencies{
		Outbox: outbox, Workflows: workflows, Owner: "capture-test",
		LeaseDuration: time.Minute, RetryBase: time.Second, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.DispatchBatch(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Retried != 1 || outbox.rescheduledCode != "WORKFLOW_BUSY" || outbox.poisonedCode != "" {
		t.Fatalf("result=%#v outbox=%#v", result, outbox)
	}
	if !outbox.recoveryContextHasDeadline || outbox.recoveryContextCanceled {
		t.Fatalf("recovery context deadline=%t canceled=%t", outbox.recoveryContextHasDeadline, outbox.recoveryContextCanceled)
	}
}

func TestDispatcherRecoverySurvivesCallerCancellation(t *testing.T) {
	workspaceID := foundation.ID("53000000-0000-4000-8000-000000000001")
	captureID := foundation.ID("53000000-0000-4000-8000-000000000002")
	outbox := &dispatcherOutbox{leases: []OutboxLease{dispatcherLease(workspaceID, captureID, "53000000-0000-4000-8000-000000000003")}}
	workflows := &dispatcherWorkflows{err: foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_BUSY", true, errors.New("busy"))}
	dispatcher, err := NewOutboxDispatcher(DispatcherDependencies{
		Outbox: outbox, Workflows: workflows, Owner: "capture-test",
		LeaseDuration: time.Minute, RetryBase: time.Second, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = dispatcher.DispatchBatch(ctx, 1)

	if outbox.rescheduledCode != "WORKFLOW_BUSY" || !outbox.recoveryContextHasDeadline || outbox.recoveryContextCanceled {
		t.Fatalf("outbox=%#v", outbox)
	}
}

type dispatcherOutbox struct {
	leases                     []OutboxLease
	next                       int
	published                  []foundation.ID
	rescheduledCode            string
	poisonedCode               string
	recoveryContextHasDeadline bool
	recoveryContextCanceled    bool
}

func (outbox *dispatcherOutbox) ClaimNext(context.Context, string, time.Duration) (OutboxLease, bool, error) {
	if outbox.next >= len(outbox.leases) {
		return OutboxLease{}, false, nil
	}
	lease := outbox.leases[outbox.next]
	outbox.next++
	return lease, true, nil
}

func (outbox *dispatcherOutbox) MarkPublished(_ context.Context, _ OutboxLease, runID foundation.ID) error {
	outbox.published = append(outbox.published, runID)
	return nil
}

func (outbox *dispatcherOutbox) Reschedule(ctx context.Context, _ OutboxLease, code string, _ time.Duration) error {
	outbox.rescheduledCode = code
	_, outbox.recoveryContextHasDeadline = ctx.Deadline()
	outbox.recoveryContextCanceled = ctx.Err() != nil
	return nil
}

func (outbox *dispatcherOutbox) Poison(ctx context.Context, _ OutboxLease, code string) error {
	outbox.poisonedCode = code
	_, outbox.recoveryContextHasDeadline = ctx.Deadline()
	outbox.recoveryContextCanceled = ctx.Err() != nil
	return nil
}

type dispatcherWorkflows struct {
	commands []workflowapp.StartCommand
	runIDs   []foundation.ID
	err      error
}

func (workflows *dispatcherWorkflows) StartCaptureWorkflow(_ context.Context, command workflowapp.StartCommand) (foundation.ID, error) {
	workflows.commands = append(workflows.commands, command)
	if workflows.err != nil {
		return "", workflows.err
	}
	return workflows.runIDs[len(workflows.commands)-1], nil
}

func dispatcherLease(workspaceID, captureID foundation.ID, id foundation.ID) OutboxLease {
	return OutboxLease{
		ID: id, WorkspaceID: workspaceID, CaptureID: captureID, Owner: "capture-test",
		AttemptCount: 1, Version: 2, LeaseUntil: time.Now().Add(time.Minute),
	}
}
