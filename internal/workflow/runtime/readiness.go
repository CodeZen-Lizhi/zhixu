// Package runtime owns process-local workflow runtime state.
package runtime

import "sync"

// ReadinessVersion identifies the public worker health response contract.
const ReadinessVersion = "v1"

const (
	CodeReady                       = "WORKER_READY"
	CodeShuttingDown                = "WORKER_SHUTTING_DOWN"
	CodeDatabaseUnavailable         = "WORKER_DATABASE_UNAVAILABLE"
	CodeRiverSchemaUnavailable      = "WORKER_RIVER_SCHEMA_UNAVAILABLE"
	CodeRiverNotStarted             = "WORKER_RIVER_NOT_STARTED"
	CodeDefinitionsUnavailable      = "WORKER_DEFINITIONS_UNAVAILABLE"
	CodeExecutorsUnavailable        = "WORKER_EXECUTORS_UNAVAILABLE"
	CodeDependenciesUnavailable     = "WORKER_DEPENDENCIES_UNAVAILABLE"
	CodeReindexDispatcherNotStarted = "WORKER_REINDEX_DISPATCHER_NOT_STARTED"
)

// ReadinessSnapshot is an immutable point-in-time view of worker readiness.
// Code and Version are derived from the boolean state and never contain an
// underlying dependency error.
type ReadinessSnapshot struct {
	DatabaseOK               bool
	RiverSchemaOK            bool
	RiverStarted             bool
	DefinitionsOK            bool
	ExecutorsOK              bool
	DependenciesOK           bool
	ReindexDispatcherStarted bool
	ShuttingDown             bool
	Code                     string
	Version                  string
}

// Ready reports whether the worker may accept workflow jobs.
func (s ReadinessSnapshot) Ready() bool {
	return !s.ShuttingDown &&
		s.DatabaseOK &&
		s.RiverSchemaOK &&
		s.RiverStarted &&
		s.DefinitionsOK &&
		s.ExecutorsOK &&
		s.DependenciesOK &&
		s.ReindexDispatcherStarted
}

// Readiness stores independently updated worker readiness checks.
type Readiness struct {
	mu       sync.RWMutex
	snapshot ReadinessSnapshot
}

// NewReadiness creates a not-ready worker snapshot.
func NewReadiness() *Readiness {
	return &Readiness{}
}

// SetDatabaseOK updates the database connectivity check.
func (r *Readiness) SetDatabaseOK(ok bool) {
	r.update(func(s *ReadinessSnapshot) { s.DatabaseOK = ok })
}

// SetRiverSchemaOK updates the River migration validation check.
func (r *Readiness) SetRiverSchemaOK(ok bool) {
	r.update(func(s *ReadinessSnapshot) { s.RiverSchemaOK = ok })
}

// SetRiverStarted updates the River client lifecycle check.
func (r *Readiness) SetRiverStarted(ok bool) {
	r.update(func(s *ReadinessSnapshot) { s.RiverStarted = ok })
}

// SetDefinitionsOK updates the frozen definition registry check.
func (r *Readiness) SetDefinitionsOK(ok bool) {
	r.update(func(s *ReadinessSnapshot) { s.DefinitionsOK = ok })
}

// SetExecutorsOK updates the frozen executor registry check.
func (r *Readiness) SetExecutorsOK(ok bool) {
	r.update(func(s *ReadinessSnapshot) { s.ExecutorsOK = ok })
}

// SetDependenciesOK updates the enabled-definition dependency check.
func (r *Readiness) SetDependenciesOK(ok bool) {
	r.update(func(s *ReadinessSnapshot) { s.DependenciesOK = ok })
}

// SetReindexDispatcherStarted updates the Reindex Dispatcher lifecycle check.
func (r *Readiness) SetReindexDispatcherStarted(started bool) {
	r.update(func(s *ReadinessSnapshot) { s.ReindexDispatcherStarted = started })
}

// BeginShutdown permanently removes this process from readiness.
func (r *Readiness) BeginShutdown() {
	r.update(func(s *ReadinessSnapshot) { s.ShuttingDown = true })
}

// Snapshot returns a consistent copy of the current readiness state.
func (r *Readiness) Snapshot() ReadinessSnapshot {
	if r == nil {
		return finalize(ReadinessSnapshot{})
	}
	r.mu.RLock()
	snapshot := r.snapshot
	r.mu.RUnlock()
	return finalize(snapshot)
}

func (r *Readiness) update(update func(*ReadinessSnapshot)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	update(&r.snapshot)
	r.mu.Unlock()
}

func finalize(snapshot ReadinessSnapshot) ReadinessSnapshot {
	snapshot.Version = ReadinessVersion
	switch {
	case snapshot.ShuttingDown:
		snapshot.Code = CodeShuttingDown
	case !snapshot.DatabaseOK:
		snapshot.Code = CodeDatabaseUnavailable
	case !snapshot.RiverSchemaOK:
		snapshot.Code = CodeRiverSchemaUnavailable
	case !snapshot.RiverStarted:
		snapshot.Code = CodeRiverNotStarted
	case !snapshot.DefinitionsOK:
		snapshot.Code = CodeDefinitionsUnavailable
	case !snapshot.ExecutorsOK:
		snapshot.Code = CodeExecutorsUnavailable
	case !snapshot.DependenciesOK:
		snapshot.Code = CodeDependenciesUnavailable
	case !snapshot.ReindexDispatcherStarted:
		snapshot.Code = CodeReindexDispatcherNotStarted
	default:
		snapshot.Code = CodeReady
	}
	return snapshot
}
