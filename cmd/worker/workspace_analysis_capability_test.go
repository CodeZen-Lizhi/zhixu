package main

import (
	"errors"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

func TestNewWorkerWorkspaceAnalysisCapabilityStaysOffByDefault(t *testing.T) {
	t.Parallel()

	capability, err := newWorkerWorkspaceAnalysisCapability(nil, config.Defaults(), workerComponents{}, nil)
	if err != nil || capability != nil {
		t.Fatalf("default-off capability=%#v err=%v", capability, err)
	}
}

func TestNewWorkerWorkspaceAnalysisCapabilityFailsClosedWhenEnabledWithoutRuntime(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.WorkspaceAnalysisWorkerEnabled = true
	capability, err := newWorkerWorkspaceAnalysisCapability(nil, cfg, workerComponents{}, nil)
	if err == nil || capability != nil {
		t.Fatalf("enabled incomplete capability=%#v err=%v", capability, err)
	}
}

func TestNewWorkerWorkspaceAnalysisCapabilityDoesNotAdvertiseOptionalCompositionFailure(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.WorkspaceAnalysisWorkerEnabled = true
	components := workerComponents{
		workspaceAnalysisCapability: agentCapabilityStatus{code: agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable},
	}
	capability, err := newWorkerWorkspaceAnalysisCapability(nil, cfg, components, nil)
	if capability != nil {
		t.Fatalf("unavailable optional composition must not advertise: %#v", capability)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable {
		t.Fatalf("error=%v", err)
	}
}
