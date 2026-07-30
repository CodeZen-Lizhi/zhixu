package main

import (
	"context"
	"errors"
	"testing"
)

func TestWorkerModelDrainStopsWaitsAndResumesLocalProducers(t *testing.T) {
	dispatcher := &fakeModelDispatcher{started: true}
	running := int64(1)
	var states []bool
	drain, err := newWorkerModelDrain(
		dispatcher,
		context.Background(),
		func(context.Context) (int64, error) { return running, nil },
		func(started bool) { states = append(states, started) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := drain.Hooks().Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if drain.ProducersEnabled() || dispatcher.started || dispatcher.stopCalls != 1 {
		t.Fatalf("drain state enabled=%t dispatcher=%+v", drain.ProducersEnabled(), dispatcher)
	}
	quiesced, err := drain.Hooks().IsQuiesced(context.Background())
	if err != nil || quiesced {
		t.Fatalf("running quiesced=%t err=%v", quiesced, err)
	}
	running = 0
	quiesced, err = drain.Hooks().IsQuiesced(context.Background())
	if err != nil || !quiesced {
		t.Fatalf("drained quiesced=%t err=%v", quiesced, err)
	}
	if err := drain.Hooks().Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !drain.ProducersEnabled() || !dispatcher.started || dispatcher.startCalls != 1 {
		t.Fatalf("resume state enabled=%t dispatcher=%+v", drain.ProducersEnabled(), dispatcher)
	}
	if len(states) != 2 || states[0] || !states[1] {
		t.Fatalf("dispatcher states=%v", states)
	}
}

func TestWorkerModelDrainFailsClosedOnStopResumeAndCountErrors(t *testing.T) {
	stopErr := errors.New("stop failed")
	dispatcher := &fakeModelDispatcher{started: true, stopErr: stopErr}
	drain, err := newWorkerModelDrain(dispatcher, context.Background(), func(context.Context) (int64, error) {
		return 0, errors.New("count failed")
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := drain.Begin(context.Background()); !errors.Is(err, stopErr) || drain.ProducersEnabled() {
		t.Fatalf("begin err=%v enabled=%t", err, drain.ProducersEnabled())
	}
	dispatcher.stopErr = nil
	dispatcher.started = false
	if _, err := drain.IsQuiesced(context.Background()); err == nil {
		t.Fatal("running job count failure was ignored")
	}
	dispatcher.startErr = errors.New("start failed")
	if err := drain.Resume(context.Background()); !errors.Is(err, dispatcher.startErr) || drain.ProducersEnabled() {
		t.Fatalf("resume err=%v enabled=%t", err, drain.ProducersEnabled())
	}
}

type fakeModelDispatcher struct {
	started    bool
	startCalls int
	stopCalls  int
	startErr   error
	stopErr    error
}

func (dispatcher *fakeModelDispatcher) Start(context.Context) error {
	dispatcher.startCalls++
	if dispatcher.startErr == nil {
		dispatcher.started = true
	}
	return dispatcher.startErr
}

func (dispatcher *fakeModelDispatcher) Stop(context.Context) error {
	dispatcher.stopCalls++
	if dispatcher.stopErr == nil {
		dispatcher.started = false
	}
	return dispatcher.stopErr
}

func (dispatcher *fakeModelDispatcher) Started() bool { return dispatcher.started }
