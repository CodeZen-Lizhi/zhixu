package main

import (
	"context"
	"testing"
	"time"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewDocumentHistoryHandlerRequiresEveryProductionDependency(t *testing.T) {
	workspaces := documentHistoryWorkspaceRepositoryFake{}
	proposals := &changecontrolapplication.Service{}
	lookup := &changecontrolpostgres.Repository{}
	for _, test := range []struct {
		name       string
		pool       *pgxpool.Pool
		workspaces gitcli.WorkspaceRepository
		proposals  *changecontrolapplication.Service
		lookup     *changecontrolpostgres.Repository
	}{
		{name: "database", workspaces: workspaces, proposals: proposals, lookup: lookup},
		{name: "workspace repository", pool: &pgxpool.Pool{}, proposals: proposals, lookup: lookup},
		{name: "proposal service", pool: &pgxpool.Pool{}, workspaces: workspaces, lookup: lookup},
		{name: "proposal lookup", pool: &pgxpool.Pool{}, workspaces: workspaces, proposals: proposals},
	} {
		t.Run(test.name, func(t *testing.T) {
			if handler, err := newDocumentHistoryHandler(test.pool, test.workspaces, test.proposals, test.lookup, time.Second); err == nil || handler != nil {
				t.Fatalf("handler=%#v err=%v", handler, err)
			}
		})
	}
}

func TestNewDocumentHistoryHandlerComposesProductionBoundaries(t *testing.T) {
	handler, err := newDocumentHistoryHandler(
		&pgxpool.Pool{},
		documentHistoryWorkspaceRepositoryFake{},
		&changecontrolapplication.Service{},
		&changecontrolpostgres.Repository{},
		time.Second,
	)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

type documentHistoryWorkspaceRepositoryFake struct{}

func (documentHistoryWorkspaceRepositoryFake) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return workspacedomain.Workspace{}, nil
}
