package runtime

import (
	"sync"
	"testing"
)

func TestReadinessTransitions(t *testing.T) {
	readiness := NewReadiness()
	if got := readiness.Snapshot(); got.Ready() || got.Code != CodeDatabaseUnavailable || got.Version != ReadinessVersion {
		t.Fatalf("initial snapshot = %+v", got)
	}

	steps := []struct {
		set      func(bool)
		wantCode string
	}{
		{readiness.SetDatabaseOK, CodeRiverSchemaUnavailable},
		{readiness.SetRiverSchemaOK, CodeRiverNotStarted},
		{readiness.SetRiverStarted, CodeDefinitionsUnavailable},
		{readiness.SetDefinitionsOK, CodeExecutorsUnavailable},
		{readiness.SetExecutorsOK, CodeDependenciesUnavailable},
		{readiness.SetDependenciesOK, CodeReady},
	}
	for _, step := range steps {
		step.set(true)
		if got := readiness.Snapshot(); got.Code != step.wantCode {
			t.Fatalf("transition code = %q, want %q", got.Code, step.wantCode)
		}
	}
	if !readiness.Snapshot().Ready() {
		t.Fatal("fully initialized readiness is not ready")
	}

	readiness.BeginShutdown()
	if got := readiness.Snapshot(); got.Ready() || got.Code != CodeShuttingDown {
		t.Fatalf("shutdown snapshot = %+v", got)
	}
}

func TestReadinessConcurrentUpdatesAndSnapshots(t *testing.T) {
	readiness := NewReadiness()
	setters := []func(bool){
		readiness.SetDatabaseOK,
		readiness.SetRiverSchemaOK,
		readiness.SetRiverStarted,
		readiness.SetDefinitionsOK,
		readiness.SetExecutorsOK,
		readiness.SetDependenciesOK,
	}

	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		for _, setter := range setters {
			wait.Add(1)
			go func(index int, set func(bool)) {
				defer wait.Done()
				for iteration := 0; iteration < 100; iteration++ {
					set((index+iteration)%2 == 0)
					snapshot := readiness.Snapshot()
					if snapshot.Code == "" || snapshot.Version != ReadinessVersion {
						t.Errorf("invalid concurrent snapshot = %+v", snapshot)
						return
					}
				}
			}(index, setter)
		}
	}
	wait.Wait()

	ready := NewReadiness()
	ready.SetDatabaseOK(true)
	ready.SetRiverSchemaOK(true)
	ready.SetRiverStarted(true)
	ready.SetDefinitionsOK(true)
	ready.SetExecutorsOK(true)
	ready.SetDependenciesOK(true)
	if got := ready.Snapshot(); !got.Ready() || got.Code != CodeReady {
		t.Fatalf("final snapshot = %+v", got)
	}

	readiness.BeginShutdown()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetRiverStarted(true)
	readiness.SetDefinitionsOK(true)
	readiness.SetExecutorsOK(true)
	readiness.SetDependenciesOK(true)
	if got := readiness.Snapshot(); got.Ready() || got.Code != CodeShuttingDown {
		t.Fatalf("shutdown snapshot became ready again = %+v", got)
	}
}

func TestNilReadinessIsSafelyNotReady(t *testing.T) {
	var readiness *Readiness
	if got := readiness.Snapshot(); got.Ready() || got.Code != CodeDatabaseUnavailable {
		t.Fatalf("nil snapshot = %+v", got)
	}
}
