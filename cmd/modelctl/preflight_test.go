package main

import (
	"context"
	"testing"

	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

func TestModelPreflighterAcceptsCanonicalDisabledRevisionWithoutNetwork(t *testing.T) {
	t.Parallel()
	preflighter := modelPreflighter{tester: modelruntime.NewConnectionTester(config.Defaults())}
	resolved := modeldomain.ResolvedSettings{Revision: 0, Settings: modeldomain.CanonicalDisabledSettings()}
	for _, role := range []modeldomain.RuntimeRole{modeldomain.RuntimeRoleAPI, modeldomain.RuntimeRoleWorker} {
		if err := preflighter.Preflight(context.Background(), role, resolved); err != nil {
			t.Fatalf("preflight disabled %s: %v", role, err)
		}
	}
}

func TestModelPreflighterRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	preflighter := modelPreflighter{tester: modelruntime.NewConnectionTester(config.Defaults())}
	err := preflighter.Preflight(context.Background(), modeldomain.RuntimeRole("scheduler"), modeldomain.ResolvedSettings{
		Revision: 0, Settings: modeldomain.CanonicalDisabledSettings(),
	})
	if stableErrorCode(err) != modeldomain.ErrorCodeInvalid {
		t.Fatalf("preflight error = %v", err)
	}
}
