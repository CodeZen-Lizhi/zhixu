package main

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

func TestNewMemoryHandlerFailsClosedWithoutDatabase(t *testing.T) {
	handler, err := newMemoryHandler(nil, time.Second)
	if err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewMemoryHandlerComposesProductionDependencies(t *testing.T) {
	handler, err := newMemoryHandler(apiConstructorPool(t), time.Second)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewMemoryServiceFailsClosedWithoutDatabase(t *testing.T) {
	service, err := newMemoryService(nil)
	if err == nil || service != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}

func TestNewMemoryServiceComposesEffectiveContextDependency(t *testing.T) {
	service, err := newMemoryService(apiConstructorPool(t))
	if err != nil || service == nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}

func TestNewInterviewHandlerFailsClosedWithoutDatabase(t *testing.T) {
	handler, err := newInterviewHandler(nil, nil, nil, time.Second)
	if err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewInterviewHandlerFailsClosedWithoutCitationDependencies(t *testing.T) {
	handler, err := newInterviewHandler(apiConstructorPool(t), nil, nil, time.Second)
	if err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewInterviewServiceFailsClosedWithoutCitationDependencies(t *testing.T) {
	service, err := newInterviewService(nil, nil, nil)
	if err == nil || service != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}

func TestNewInterviewServiceComposesProductionDependencies(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newInterviewService(
		pool,
		workspaces,
		filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}},
	)
	if err != nil || service == nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}

func TestNewInterviewHandlerComposesProductionDependencies(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newInterviewHandler(
		pool,
		workspaces,
		filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}},
		time.Second,
	)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}
