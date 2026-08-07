package main

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

func TestStructuredSchedulerSelectorsAreIndependent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		selectImplementation func(*config.Config)
		selectedIndex        int
	}{
		{name: "rag", selectImplementation: func(cfg *config.Config) { cfg.StructuredSchedulerRAG = config.StructuredSchedulerImplementationEino }, selectedIndex: 0},
		{name: "relation", selectImplementation: func(cfg *config.Config) {
			cfg.StructuredSchedulerRelation = config.StructuredSchedulerImplementationEino
		}, selectedIndex: 1},
		{name: "artifact", selectImplementation: func(cfg *config.Config) {
			cfg.StructuredSchedulerArtifact = config.StructuredSchedulerImplementationEino
		}, selectedIndex: 2},
		{name: "capture", selectImplementation: func(cfg *config.Config) {
			cfg.StructuredSchedulerCapture = config.StructuredSchedulerImplementationEino
		}, selectedIndex: 3},
		{name: "organizing", selectImplementation: func(cfg *config.Config) {
			cfg.StructuredSchedulerOrganizing = config.StructuredSchedulerImplementationEino
		}, selectedIndex: 4},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Defaults()
			test.selectImplementation(&cfg)
			implementations := []config.StructuredSchedulerImplementation{
				cfg.StructuredSchedulerRAG,
				cfg.StructuredSchedulerRelation,
				cfg.StructuredSchedulerArtifact,
				cfg.StructuredSchedulerCapture,
				cfg.StructuredSchedulerOrganizing,
			}
			for index, implementation := range implementations {
				scheduler, err := newStructuredPhaseScheduler(implementation)
				if err != nil {
					t.Fatalf("selector[%d] error=%v", index, err)
				}
				if (index == test.selectedIndex) != (scheduler != nil) {
					t.Fatalf("selector[%d]=%q scheduler=%T selected=%d", index, implementation, scheduler, test.selectedIndex)
				}
			}
		})
	}
}

func TestStructuredSchedulerFactoryRejectsUnknownImplementation(t *testing.T) {
	t.Parallel()
	_, err := newStructuredPhaseScheduler("automatic")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != "WORKER_STRUCTURED_SCHEDULER_INVALID" || classified.Retryable {
		t.Fatalf("error=%#v", err)
	}
}
