package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestCompositeWorkflowTerminalHookRunsInRegistrationOrderAndStopsOnError(t *testing.T) {
	event := WorkflowNodeTerminalEvent{
		WorkflowRunID:  foundation.ID("00000000-0000-4000-8000-000000000001"),
		NodeRunID:      foundation.ID("00000000-0000-4000-8000-000000000002"),
		NodeAttemptID:  foundation.ID("00000000-0000-4000-8000-000000000003"),
		Outcome:        WorkflowTerminalOutcomeFailed,
		FailureClass:   domain.FailureClassNonRetryable,
		FailureCode:    "ARTIFACT_GENERATION_FAILED",
		FailureSummary: "artifact generation failed",
		TerminalAt:     time.Date(2026, 7, 26, 5, 0, 0, 0, time.UTC),
	}
	transaction := &struct{ name string }{name: "runtime-tx"}
	order := make([]string, 0, 3)
	cause := errors.New("terminal projection failed")
	first := &workflowTerminalHookFake{name: "first", order: &order}
	second := &workflowTerminalHookFake{name: "second", order: &order, err: cause}
	third := &workflowTerminalHookFake{name: "third", order: &order}

	hook, err := NewCompositeWorkflowTerminalHook(first, second, third)
	if err != nil {
		t.Fatal(err)
	}
	err = hook.OnWorkflowNodeTerminal(context.Background(), transaction, event)
	if !errors.Is(err, cause) {
		t.Fatalf("OnWorkflowNodeTerminal() error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("hook order = %v", order)
	}
	for _, item := range []*workflowTerminalHookFake{first, second} {
		if item.transaction != transaction || item.event != event {
			t.Fatalf("%s received transaction=%p event=%+v", item.name, item.transaction, item.event)
		}
	}
	if third.calls != 0 {
		t.Fatalf("third hook calls = %d", third.calls)
	}
}

func TestCompositeWorkflowTerminalHookRejectsMissingRegistration(t *testing.T) {
	if _, err := NewCompositeWorkflowTerminalHook(); err == nil {
		t.Fatal("empty terminal hook registration was accepted")
	}
	var typedNil *workflowTerminalHookFake
	if _, err := NewCompositeWorkflowTerminalHook(typedNil); err == nil {
		t.Fatal("typed nil terminal hook was accepted")
	}
}

type workflowTerminalHookFake struct {
	name        string
	order       *[]string
	err         error
	calls       int
	transaction any
	event       WorkflowNodeTerminalEvent
}

func (hook *workflowTerminalHookFake) OnWorkflowNodeTerminal(_ context.Context, transaction any, event WorkflowNodeTerminalEvent) error {
	hook.calls++
	hook.transaction = transaction
	hook.event = event
	if hook.order != nil {
		*hook.order = append(*hook.order, hook.name)
	}
	return hook.err
}
