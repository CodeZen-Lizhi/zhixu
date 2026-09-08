package main

import (
	"context"
	"testing"
	"time"

	authoringapplication "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestNewOrganizingHandlerRequiresEveryOwnerBoundary(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces := fakeSourceMaterialRepository{}
	files := fakeFileScanner{}
	authoring := organizingAuthoringReaderFake{}
	workflows := &workflowapplication.Service{}
	tests := []struct {
		name       string
		ctx        context.Context
		pool       *platformpostgres.Pool
		workspaces fakeSourceMaterialRepository
		files      fakeFileScanner
		authoring  organizingAuthoringReaderFake
		missing    string
	}{
		{name: "context", pool: pool, workspaces: workspaces, files: files, authoring: authoring, missing: "context"},
		{name: "database", ctx: context.Background(), workspaces: workspaces, files: files, authoring: authoring, missing: "database"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if handler, err := newOrganizingHandler(test.ctx, test.pool, nil, test.workspaces, test.files, test.authoring, workflows, time.Second); err == nil || handler != nil {
				t.Fatalf("missing %s handler=%#v err=%v", test.missing, handler, err)
			}
		})
	}
	if handler, err := newOrganizingHandler(context.Background(), pool, nil, nil, files, authoring, workflows, time.Second); err == nil || handler != nil {
		t.Fatalf("missing Workspace repository handler=%#v err=%v", handler, err)
	}
	if handler, err := newOrganizingHandler(context.Background(), pool, nil, workspaces, nil, authoring, workflows, time.Second); err == nil || handler != nil {
		t.Fatalf("missing file scanner handler=%#v err=%v", handler, err)
	}
	if handler, err := newOrganizingHandler(context.Background(), pool, nil, workspaces, files, nil, workflows, time.Second); err == nil || handler != nil {
		t.Fatalf("missing Authoring reader handler=%#v err=%v", handler, err)
	}
	if handler, err := newOrganizingHandler(context.Background(), pool, nil, workspaces, files, authoring, nil, time.Second); err == nil || handler != nil {
		t.Fatalf("missing Workflow reader handler=%#v err=%v", handler, err)
	}
}

func TestNewOrganizingHumanTaskReviewProjectorRequiresDatabaseAndRuntime(t *testing.T) {
	pool := apiConstructorPool(t)
	runtime := &workflowpostgres.GORMRuntimeRepository{}
	if projector, err := newOrganizingHumanTaskReviewProjector(nil, runtime); err == nil || projector != nil {
		t.Fatalf("nil database projector=%#v error=%v", projector, err)
	}
	if projector, err := newOrganizingHumanTaskReviewProjector(pool, nil); err == nil || projector != nil {
		t.Fatalf("nil runtime projector=%#v error=%v", projector, err)
	}
	projector, err := newOrganizingHumanTaskReviewProjector(pool, runtime)
	if err != nil || projector == nil {
		t.Fatalf("projector=%#v error=%v", projector, err)
	}
}

type organizingAuthoringReaderFake struct{}

func (organizingAuthoringReaderFake) GetArticleRevisions(context.Context, authoringapplication.ArticleRevisionBatchQuery) ([]authoringapplication.ArticleRevisionSnapshot, error) {
	return []authoringapplication.ArticleRevisionSnapshot{}, nil
}

func (organizingAuthoringReaderFake) SearchArticleRevisions(context.Context, authoringapplication.ArticleRevisionSearchQuery) ([]authoringapplication.ArticleRevisionSearchHit, error) {
	return []authoringapplication.ArticleRevisionSearchHit{}, nil
}
