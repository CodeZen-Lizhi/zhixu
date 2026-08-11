package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIEmbeddingRuntimeAcquirerUsesPersistedContractAndPinsLease(t *testing.T) {
	models, version := newAPIEmbeddingModelsAndVersion(t)
	runtimeLease := &apiModelsRuntimeLeaseFake{models: models}
	runtime := &apiModelsRuntimeAcquirerFake{lease: runtimeLease}
	acquirer, err := newAPIEmbeddingRuntimeAcquirer(runtime)
	if err != nil {
		t.Fatal(err)
	}

	lease, err := acquirer.Acquire(context.Background(), version)
	if err != nil {
		t.Fatal(err)
	}
	wantTarget := fmt.Sprintf(
		"RuntimeTarget{Kind:embedding Provider:%q Model:%q Dimensions:%d}",
		version.Provider,
		version.Model,
		version.Dimensions,
	)
	if runtime.calls != 1 || runtime.target.String() != wantTarget {
		t.Fatalf("acquisition calls=%d target=%s want=%s", runtime.calls, runtime.target, wantTarget)
	}
	if lease.Embedder() != models.Embedding().Embedder() {
		t.Fatal("lease did not expose the acquired generation embedder")
	}
	lease.Release()
	lease.Release()
	if runtimeLease.releases.Load() != 1 {
		t.Fatalf("runtime lease releases=%d want=1", runtimeLease.releases.Load())
	}
	if lease.Embedder() != nil {
		t.Fatal("released lease still exposed its embedder")
	}
}

func TestAPIEmbeddingRuntimeAcquirerReleasesMismatchedGeneration(t *testing.T) {
	models, version := newAPIEmbeddingModelsAndVersion(t)
	version.Model = "persisted-other-model"
	runtimeLease := &apiModelsRuntimeLeaseFake{models: models}
	acquirer, err := newAPIEmbeddingRuntimeAcquirer(&apiModelsRuntimeAcquirerFake{lease: runtimeLease})
	if err != nil {
		t.Fatal(err)
	}

	lease, err := acquirer.Acquire(context.Background(), version)
	if lease != nil || err == nil {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != modelsettingsruntime.ErrorCodeRuntimeEmbeddingContractMismatch || classified.Retryable {
		t.Fatalf("error=%v", err)
	}
	if runtimeLease.releases.Load() != 1 {
		t.Fatalf("runtime lease releases=%d want=1", runtimeLease.releases.Load())
	}
}

func TestAPIEmbeddingRuntimeAcquirerReleasesLeaseReturnedWithError(t *testing.T) {
	models, version := newAPIEmbeddingModelsAndVersion(t)
	runtimeLease := &apiModelsRuntimeLeaseFake{models: models}
	want := errors.New("runtime acquisition failed")
	acquirer, err := newAPIEmbeddingRuntimeAcquirer(&apiModelsRuntimeAcquirerFake{lease: runtimeLease, err: want})
	if err != nil {
		t.Fatal(err)
	}

	lease, err := acquirer.Acquire(context.Background(), version)
	if lease != nil || !errors.Is(err, want) {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	if runtimeLease.releases.Load() != 1 {
		t.Fatalf("runtime lease releases=%d want=1", runtimeLease.releases.Load())
	}
}

func TestAPIEmbeddingRuntimeAcquirerFailsClosedWithoutRuntimeLease(t *testing.T) {
	_, version := newAPIEmbeddingModelsAndVersion(t)
	acquirer, err := newAPIEmbeddingRuntimeAcquirer(&apiModelsRuntimeAcquirerFake{})
	if err != nil {
		t.Fatal(err)
	}

	lease, err := acquirer.Acquire(context.Background(), version)
	if lease != nil || err == nil {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != modelsettingsruntime.ErrorCodeRuntimeNotReady || !classified.Retryable {
		t.Fatalf("error=%v", err)
	}
	if acquirer, err := newAPIEmbeddingRuntimeAcquirer(nil); err == nil || acquirer != nil {
		t.Fatalf("nil runtime acquirer=%#v err=%v", acquirer, err)
	}
}

func TestAPIRetrievalCompositionUsesManagedEmbeddingAcquirerWithoutFixedEmbedder(t *testing.T) {
	pool := &pgxpool.Pool{}
	managed := apiCompatibleEmbeddingAcquirerFake{}
	handler, err := newRetrievalHandler(pool, nil, fakeSourceMaterialRepository{}, fakeFileScanner{}, managed)
	if err != nil || handler == nil {
		t.Fatalf("managed retrieval handler=%#v err=%v", handler, err)
	}
	if _, err := newAPISearchService(nil, nil, nil, managed, managed); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("multiple managed acquirers error=%v", err)
	}
}

func newAPIEmbeddingModelsAndVersion(t *testing.T) (*modelsettingsruntime.Models, retrievaldomain.EmbeddingVersion) {
	t.Helper()
	cfg := config.Defaults()
	cfg.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
	cfg.EmbeddingBaseURL = "https://models.example.test"
	cfg.EmbeddingAPIKey = "api-embedding-adapter-test-key"
	cfg.EmbeddingModel = "embed-v1"
	cfg.EmbeddingDimensions = 3
	cfg.EmbeddingNormalization = retrievaldomain.NormalizationL2
	cfg.EmbeddingDistanceMetric = retrievaldomain.DistanceCosine
	models := mustAPIModels(t, cfg)
	t.Cleanup(func() { _ = models.Close() })
	contract, ok := models.Embedding().Contract()
	if !ok {
		t.Fatal("configured embedding contract is unavailable")
	}
	binding := contract.Binding()
	revision := int64(7)
	return models, retrievaldomain.EmbeddingVersion{
		ID: foundation.ID("73000000-0000-4000-8000-000000000001"), Provider: binding.Provider,
		AdapterName: binding.AdapterName, AdapterVersion: binding.AdapterVersion, Model: binding.Model,
		Dimensions: binding.Dimensions, Normalization: binding.Normalization, DistanceMetric: binding.DistanceMetric,
		ConfigHash: binding.ConfigHash, ModelSettingsRevision: &revision,
		CreatedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
	}
}

type apiModelsRuntimeAcquirerFake struct {
	lease  modelsettingsruntime.RuntimeLease[*modelsettingsruntime.Models]
	err    error
	target modelsettingsruntime.RuntimeTarget
	calls  int
}

func (runtime *apiModelsRuntimeAcquirerFake) Acquire(
	_ context.Context,
	target modelsettingsruntime.RuntimeTarget,
) (modelsettingsruntime.RuntimeLease[*modelsettingsruntime.Models], error) {
	runtime.calls++
	runtime.target = target
	return runtime.lease, runtime.err
}

type apiModelsRuntimeLeaseFake struct {
	models   *modelsettingsruntime.Models
	releases atomic.Int32
}

func (lease *apiModelsRuntimeLeaseFake) Binding() modelsettingsruntime.RuntimeBinding {
	return modelsettingsruntime.RuntimeBinding{}
}

func (lease *apiModelsRuntimeLeaseFake) Value() *modelsettingsruntime.Models { return lease.models }

func (lease *apiModelsRuntimeLeaseFake) Release() { lease.releases.Add(1) }

type apiCompatibleEmbeddingAcquirerFake struct{}

func (apiCompatibleEmbeddingAcquirerFake) Acquire(
	context.Context,
	retrievaldomain.EmbeddingVersion,
) (retrievalapplication.CompatibleEmbeddingLease, error) {
	return nil, errors.New("test embedding runtime is unavailable")
}
