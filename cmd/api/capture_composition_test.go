package main

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestNewCaptureHandlerRequiresProductionDependencies(t *testing.T) {
	pool := apiConstructorPool(t)
	sources, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if handler, err := newCaptureHandler(nil, captureManagedContentWriterFake{}, sources, time.Second); err == nil || handler != nil {
		t.Fatalf("missing database handler=%#v err=%v", handler, err)
	}
	if handler, err := newCaptureHandler(pool, nil, sources, time.Second); err == nil || handler != nil {
		t.Fatalf("missing content writer handler=%#v err=%v", handler, err)
	}
}

func TestNewCaptureHandlerComposesProductionDependencies(t *testing.T) {
	pool := apiConstructorPool(t)
	sources, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newCaptureHandler(pool, captureManagedContentWriterFake{}, sources, time.Second)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

type captureManagedContentWriterFake struct{}

func (captureManagedContentWriterFake) StageManagedBytes(context.Context, foundation.ID, string, []byte, string) (workspacedomain.ManagedContentStage, error) {
	return workspacedomain.ManagedContentStage{}, nil
}

func (captureManagedContentWriterFake) PublishManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) (workspacedomain.ContentCapture, error) {
	return workspacedomain.ContentCapture{}, nil
}

func (captureManagedContentWriterFake) DiscardManagedBytes(context.Context, foundation.ID, workspacedomain.ManagedContentStage) error {
	return nil
}
