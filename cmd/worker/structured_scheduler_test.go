package main

import "testing"

func TestStructuredSchedulerFactoryBuildsEinoRuntime(t *testing.T) {
	scheduler, err := newStructuredPhaseScheduler()
	if err != nil || scheduler == nil {
		t.Fatalf("structured scheduler=%T err=%v", scheduler, err)
	}
}

func TestRAGExecutionSchedulerBuildsEinoRuntime(t *testing.T) {
	scheduler, err := newRAGExecutionScheduler()
	if err != nil || scheduler == nil {
		t.Fatalf("rag scheduler=%T err=%v", scheduler, err)
	}
}
