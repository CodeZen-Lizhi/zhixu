package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestTimelineProjectionDispatcherStopsAtLimitAndCountsOutcomes(t *testing.T) {
	port := &timelineProjectionPortStub{results: []TimelineProjectionResult{
		{SourceID: testID(71), EventID: testID(72), Outcome: TimelineProjectionProjected},
		{SourceID: testID(73), EventID: testID(74), Outcome: TimelineProjectionReplayed},
		{SourceID: testID(75), EventID: testID(76), Outcome: TimelineProjectionProjected},
	}}
	dispatcher, err := NewTimelineProjectionDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.DispatchBatch(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 2 || result.Projected != 1 || result.Replayed != 1 || result.Poisoned != 0 || port.calls != 2 {
		t.Fatalf("batch=%#v calls=%d", result, port.calls)
	}
}

func TestTimelineProjectionDispatcherReturnsPersistedPoisonResult(t *testing.T) {
	injected := foundation.NewError(foundation.ErrorManualRecoveryRequired, "KNOWLEDGE_TIMELINE_PROJECTION_POISONED", false, errors.New("source drift"))
	port := &timelineProjectionPortStub{
		results: []TimelineProjectionResult{{SourceID: testID(77), EventID: testID(78), Outcome: TimelineProjectionPoisoned}},
		errors:  []error{injected},
	}
	dispatcher, err := NewTimelineProjectionDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.DispatchBatch(context.Background(), 1)
	if !errors.Is(err, injected) || result.Processed != 1 || result.Poisoned != 1 {
		t.Fatalf("batch=%#v err=%v", result, err)
	}
}

func TestTimelineProjectionDispatcherRejectsInvalidBatchBoundary(t *testing.T) {
	dispatcher, err := NewTimelineProjectionDispatcher(&timelineProjectionPortStub{})
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, MaxTimelineProjectionBatch + 1} {
		if _, err := dispatcher.DispatchBatch(context.Background(), limit); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
	}
}

type timelineProjectionPortStub struct {
	results []TimelineProjectionResult
	errors  []error
	calls   int
}

func (stub *timelineProjectionPortStub) ProjectNext(context.Context) (TimelineProjectionResult, bool, error) {
	index := stub.calls
	stub.calls++
	if index >= len(stub.results) {
		return TimelineProjectionResult{}, false, nil
	}
	var err error
	if index < len(stub.errors) {
		err = stub.errors[index]
	}
	return stub.results[index], true, err
}
