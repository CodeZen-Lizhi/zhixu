package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowhealth "github.com/CodeZen-Lizhi/zhixu/internal/workflow/httphealth"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

func TestNewWorkerComponentsRequiresDatabase(t *testing.T) {
	components, err := newWorkerComponents(nil, config.Defaults(), nil, nil)
	if err == nil || components.safeWriteback != nil {
		t.Fatalf("components=%#v err=%v", components, err)
	}
}

func TestConfiguredEmbedderSupportsDisabledOpenAIAndOllama(t *testing.T) {
	disabled, err := newConfiguredEmbedder(config.Defaults())
	if err != nil || disabled != nil {
		t.Fatalf("disabled embedder=%#v err=%v", disabled, err)
	}
	base := config.Defaults()
	base.EmbeddingModel = "embed-v1"
	base.EmbeddingDimensions = 3
	base.EmbeddingNormalization = retrievaldomain.NormalizationL2
	base.EmbeddingDistanceMetric = retrievaldomain.DistanceCosine
	base.EmbeddingMaxBatchSize = 8
	base.EmbeddingMaxInputBytes = 1024
	base.EmbeddingMaxBatchInputBytes = 8192
	base.EmbeddingTimeout = time.Second
	base.EmbeddingMaxResponseBytes = 1 << 20

	openAI := base
	openAI.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
	openAI.EmbeddingBaseURL = "https://models.example.test"
	openAI.EmbeddingAPIKey = "secret-canary"
	openAIEmbedder, err := newConfiguredEmbedder(openAI)
	if err != nil || openAIEmbedder.Contract().Provider != "openai-compatible" || strings.Contains(fmt.Sprintf("%#v", openAIEmbedder), openAI.EmbeddingAPIKey) {
		t.Fatalf("openai contract=%#v err=%v", openAIEmbedder, err)
	}

	ollama := base
	ollama.EmbeddingProvider = config.EmbeddingProviderOllama
	ollama.EmbeddingBaseURL = "http://127.0.0.1:11434"
	ollamaEmbedder, err := newConfiguredEmbedder(ollama)
	if err != nil || ollamaEmbedder.Contract().Provider != "ollama" {
		t.Fatalf("ollama contract=%#v err=%v", ollamaEmbedder, err)
	}
}

func TestConfiguredRRFUsesVersionedTypedLimits(t *testing.T) {
	cfg := config.Defaults()
	raw, err := configuredRRF(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rrf, err := retrievaldomain.ParseRRFConfig(raw)
	if err != nil || rrf.K != cfg.RetrievalRRFK || rrf.FusedCandidateLimit != cfg.RetrievalRRFFusedCandidateLimit {
		t.Fatalf("rrf=%#v raw=%s err=%v", rrf, raw, err)
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
	readiness.SetReindexDispatcherStarted(true)

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
