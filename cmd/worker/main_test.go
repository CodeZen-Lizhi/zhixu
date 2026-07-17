package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	workflowhealth "github.com/CodeZen-Lizhi/zhixu/internal/workflow/httphealth"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

func TestNewWorkerComponentsRequiresDatabase(t *testing.T) {
	components, err := newWorkerComponents(nil, config.Defaults(), nil, nil)
	if err == nil || components.safeWriteback != nil {
		t.Fatalf("components=%#v err=%v", components, err)
	}
}

func TestRecordShutdownMetricMapsLifecycleModesToBoundedLabels(t *testing.T) {
	metrics := observability.NewMemoryMetrics()
	if err := recordShutdownMetric(metrics, shutdownGraceful, "success"); err != nil {
		t.Fatal(err)
	}
	if err := recordShutdownMetric(metrics, shutdownEmergency, "failure"); err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("measurements=%d", len(snapshot))
	}
	if got := snapshot[0].Labels.Map()["shutdown_kind"]; got != "graceful" {
		t.Fatalf("graceful label=%q", got)
	}
	if got := snapshot[1].Labels.Map()["shutdown_kind"]; got != "forced" {
		t.Fatalf("forced label=%q", got)
	}
}

func TestStartWorkerHealthServerServesReadinessAndCloses(t *testing.T) {
	readiness := workflowruntime.NewReadiness()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetRiverStarted(true)
	readiness.SetDefinitionsOK(true)
	readiness.SetExecutorsOK(true)
	readiness.SetDependenciesOK(true)

	health, err := startWorkerHealthServer("127.0.0.1:0", workflowhealth.NewHandler(readiness))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://" + health.address + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", response.StatusCode)
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := health.server.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-health.errors:
		if err != nil {
			t.Fatalf("serve err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("health server did not stop")
	}
	if _, err := http.Get("http://" + health.address + "/livez"); err == nil {
		t.Fatal("health server still accepts requests after shutdown")
	}
}
