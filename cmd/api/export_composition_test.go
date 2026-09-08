package main

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
)

func TestNewExportHandlerRequiresDatabaseAndWorkspaceRepository(t *testing.T) {
	cfg := config.Defaults()
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		pool       *platformpostgres.Pool
		workspaces workspaceruntimegrant.GORMRepositoryPort
	}{
		{name: "missing database", workspaces: workspaces},
		{name: "missing workspace repository", pool: pool},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, buildErr := newExportHandler(test.pool, cfg, test.workspaces)
			if buildErr == nil || handler != nil {
				t.Fatalf("handler=%#v err=%v", handler, buildErr)
			}
		})
	}
}

func TestNewExportHandlerComposesProductionDependencies(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newExportHandler(pool, config.Defaults(), workspaces)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}
