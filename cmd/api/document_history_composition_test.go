package main

import (
	"context"
	"testing"
	"time"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestNewDocumentHistoryHandlerRequiresEveryProductionDependency(t *testing.T) {
	workspaces := documentHistoryWorkspaceRepositoryFake{}
	proposals := &changecontrolapplication.Service{}
	lookup := &changecontrolpostgres.GORMRepository{}
	for _, test := range []struct {
		name       string
		pool       *platformpostgres.Pool
		workspaces gitcli.WorkspaceRepository
		proposals  *changecontrolapplication.Service
		lookup     *changecontrolpostgres.GORMRepository
	}{
		{name: "database", workspaces: workspaces, proposals: proposals, lookup: lookup},
		{name: "workspace repository", pool: apiConstructorPool(t), proposals: proposals, lookup: lookup},
		{name: "proposal service", pool: apiConstructorPool(t), workspaces: workspaces, lookup: lookup},
		{name: "proposal lookup", pool: apiConstructorPool(t), workspaces: workspaces, proposals: proposals},
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
		apiConstructorPool(t),
		documentHistoryWorkspaceRepositoryFake{},
		&changecontrolapplication.Service{},
		&changecontrolpostgres.GORMRepository{},
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
