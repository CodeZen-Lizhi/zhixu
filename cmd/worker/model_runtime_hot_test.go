package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
)

const workerRuntimeTestNodeKind = "worker.runtime.test"

type workerRuntimeTestExecutor struct{}

func (workerRuntimeTestExecutor) Execute(context.Context, workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	return workflowapplication.ExecutionResult{Output: json.RawMessage(`{}`)}, nil
}

type workerRuntimeTestFactory struct {
	builds atomic.Int64
	closes atomic.Int64
}

func (factory *workerRuntimeTestFactory) Build(context.Context, int64) (*workerRuntimeGeneration, error) {
	factory.builds.Add(1)
	return nil, errors.New("unexpected worker runtime generation build")
}

func (*workerRuntimeTestFactory) Probe(context.Context, *workerRuntimeGeneration) error { return nil }
func (factory *workerRuntimeTestFactory) Close(*workerRuntimeGeneration) {
	factory.closes.Add(1)
}

func TestWorkerRuntimeExecutorAcquirerUsesExactResidentAttemptBinding(t *testing.T) {
	registry := newWorkerRuntimeTestRegistry(t)
	instanceID := newWorkerRuntimeTestID(t)
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 0, InstanceID: instanceID,
			},
			Value: &workerRuntimeGeneration{executors: registry},
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	acquirer := &workerRuntimeExecutorAcquirer{}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	revision := int64(0)
	lease, err := acquirer.AcquireAttempt(context.Background(), workflowdomain.NodeAttempt{
		ModelSettingsRevision: &revision, ModelRuntimeInstanceID: &instanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Executors() != registry {
		t.Fatal("attempt lease did not expose the resident generation registry")
	}
	lease.Release()
	lease.Release()
	if lease.Executors() != nil {
		t.Fatal("released attempt lease still exposes its executor registry")
	}
	if got := factory.builds.Load(); got != 0 {
		t.Fatalf("generation build count = %d, want 0", got)
	}
}

func TestUnavailableInitialWorkerGenerationIsNotAcquirable(t *testing.T) {
	instanceID := newWorkerRuntimeTestID(t)
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 0, InstanceID: instanceID,
			},
			Value: &workerRuntimeGeneration{executors: newWorkerRuntimeTestRegistry(t)}, Unavailable: true,
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	_, err = host.Acquire(context.Background(), modelsettingsruntime.CurrentRuntime())
	assertWorkerRuntimeErrorCode(t, err, modelsettingsruntime.ErrorCodeRuntimeNotReady)
	acquirer := &workerRuntimeExecutorAcquirer{}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	revision := int64(0)
	_, err = acquirer.AcquireAttempt(context.Background(), workflowdomain.NodeAttempt{
		ModelSettingsRevision: &revision, ModelRuntimeInstanceID: &instanceID,
	})
	assertWorkerRuntimeErrorCode(t, err, modelsettingsruntime.ErrorCodeRuntimeRevisionUnavailable)
	if got := factory.builds.Load(); got != 0 {
		t.Fatalf("unavailable canonical generation triggered %d builds", got)
	}
}

func TestWorkerRuntimeExecutorAcquirerDoesNotRebuildRetiredRevisionZero(t *testing.T) {
	instanceID := newWorkerRuntimeTestID(t)
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 1, InstanceID: instanceID,
			},
			Value: &workerRuntimeGeneration{executors: newWorkerRuntimeTestRegistry(t)},
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	acquirer := &workerRuntimeExecutorAcquirer{}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	revision := int64(0)
	_, err = acquirer.AcquireAttempt(context.Background(), workflowdomain.NodeAttempt{
		ModelSettingsRevision: &revision, ModelRuntimeInstanceID: &instanceID,
	})
	assertWorkerRuntimeErrorCode(t, err, modelsettingsruntime.ErrorCodeRuntimeRevisionUnavailable)
	if got := factory.builds.Load(); got != 0 {
		t.Fatalf("generation build count = %d, want 0", got)
	}
}

func TestWorkerRuntimeExecutorAcquirerRejectsIncompleteOrForeignBinding(t *testing.T) {
	acquirer := &workerRuntimeExecutorAcquirer{}
	_, err := acquirer.AcquireAttempt(context.Background(), workflowdomain.NodeAttempt{})
	assertWorkerRuntimeErrorCode(t, err, modelsettingsruntime.ErrorCodeRuntimeBindingMismatch)

	instanceID := newWorkerRuntimeTestID(t)
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 3, InstanceID: instanceID,
			},
			Value: &workerRuntimeGeneration{executors: newWorkerRuntimeTestRegistry(t)},
		},
		Factory: &workerRuntimeTestFactory{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	if err := acquirer.bind(host); err == nil {
		t.Fatal("second worker runtime host bind succeeded")
	}
	foreignID := newWorkerRuntimeTestID(t)
	revision := int64(3)
	_, err = acquirer.AcquireAttempt(context.Background(), workflowdomain.NodeAttempt{
		ModelSettingsRevision: &revision, ModelRuntimeInstanceID: &foreignID,
	})
	assertWorkerRuntimeErrorCode(t, err, modelsettingsruntime.ErrorCodeRuntimeBindingMismatch)
}

type workerSourceRefreshRunnerStub struct {
	started chan struct{}
	resume  chan struct{}
}

func (runner *workerSourceRefreshRunnerStub) Refresh(context.Context, retrievalapplication.SourceRefreshRequest) (retrievalapplication.SourceRefreshResult, error) {
	close(runner.started)
	<-runner.resume
	return retrievalapplication.SourceRefreshResult{}, nil
}

func (*workerSourceRefreshRunnerStub) RefreshBatch(context.Context, retrievalapplication.BatchSourceRefreshRequest) (retrievalapplication.BatchSourceRefreshResult, error) {
	return retrievalapplication.BatchSourceRefreshResult{}, nil
}

func TestWorkerSourceRefreshFacadeHoldsGenerationUntilOperationReturns(t *testing.T) {
	runner := &workerSourceRefreshRunnerStub{started: make(chan struct{}), resume: make(chan struct{})}
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 1, InstanceID: newWorkerRuntimeTestID(t),
			},
			Value: &workerRuntimeGeneration{sources: sourceProcessingComponents{refresher: runner}},
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	acquirer := &workerSourceRefreshAcquirer{}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	refresher, err := retrievalapplication.NewDynamicSourceRefresher(acquirer)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, refreshErr := refresher.Refresh(context.Background(), retrievalapplication.SourceRefreshRequest{})
		result <- refreshErr
	}()
	<-runner.started
	host.Close()
	if got := factory.closes.Load(); got != 0 {
		t.Fatalf("generation close count while refresh is running = %d, want 0", got)
	}
	close(runner.resume)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if got := factory.closes.Load(); got != 1 {
		t.Fatalf("generation close count after refresh settlement = %d, want 1", got)
	}
}

type workerReindexRuntimeStoreStub struct {
	index          retrievaldomain.IndexVersion
	embedding      retrievaldomain.EmbeddingVersion
	buildStatus    retrievaldomain.BuildStatus
	indexCalls     atomic.Int64
	embeddingCalls atomic.Int64
	statusCalls    atomic.Int64
}

func (store *workerReindexRuntimeStoreStub) GetIndex(context.Context, foundation.ID, foundation.ID) (retrievaldomain.IndexVersion, error) {
	store.indexCalls.Add(1)
	return store.index, nil
}

func (store *workerReindexRuntimeStoreStub) GetEmbeddingVersion(context.Context, foundation.ID) (retrievaldomain.EmbeddingVersion, error) {
	store.embeddingCalls.Add(1)
	return store.embedding, nil
}

func (store *workerReindexRuntimeStoreStub) GetBuildStatus(context.Context, foundation.ID, foundation.ID) (retrievaldomain.BuildStatus, error) {
	store.statusCalls.Add(1)
	return store.buildStatus, nil
}

type workerReindexContextStub struct{}

func (workerReindexContextStub) LoadProcessorContext(context.Context, retrievaldomain.DeliveryFence) (retrievalapplication.ProcessorContextLoadResult, error) {
	return retrievalapplication.ProcessorContextLoadResult{}, nil
}

type workerReindexVectorStub struct{}

func (workerReindexVectorStub) BuildNextVectorBatch(context.Context, retrievalapplication.BuildNextVectorBatchRequest) (retrievalapplication.BuildNextVectorBatchResult, error) {
	return retrievalapplication.BuildNextVectorBatchResult{}, nil
}

func TestWorkerReindexAdmissionPinsGenerationUntilRelease(t *testing.T) {
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 1, InstanceID: newWorkerRuntimeTestID(t),
			},
			Value: &workerRuntimeGeneration{},
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	acquirer, err := newWorkerReindexProcessorAcquirer(&workerReindexRuntimeStoreStub{}, workerReindexContextStub{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	admission, err := acquirer.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	host.Close()
	if got := factory.closes.Load(); got != 0 {
		t.Fatalf("generation close count while Reindex admission is held = %d, want 0", got)
	}
	admission.Release()
	admission.Release()
	if got := factory.closes.Load(); got != 1 {
		t.Fatalf("generation close count after Reindex admission release = %d, want 1", got)
	}
}

func TestWorkerReindexAcquirerUsesPersistedEmbeddingContract(t *testing.T) {
	workspaceID := newWorkerRuntimeTestID(t)
	indexID := newWorkerRuntimeTestID(t)
	embeddingID := newWorkerRuntimeTestID(t)
	revision := int64(1)
	fusion, err := configuredRRF(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	store := &workerReindexRuntimeStoreStub{
		index: retrievaldomain.IndexVersion{
			ID: indexID, WorkspaceID: workspaceID, EmbeddingVersionID: &embeddingID,
			TokenizerID: retrievalapplication.FTSOnlyTokenizerID, TokenizerVersion: retrievalapplication.FTSOnlyTokenizerVersion,
			TokenizerConfigHash: retrievalapplication.FTSOnlyTokenizerConfigHash, FusionConfig: fusion,
		},
		embedding: retrievaldomain.EmbeddingVersion{
			ID: embeddingID, Provider: "openai-compatible", AdapterName: "eino-openai", AdapterVersion: "v1",
			Model: "embedding-v1", Dimensions: 3, Normalization: retrievaldomain.NormalizationL2,
			DistanceMetric: retrievaldomain.DistanceCosine, ConfigHash: strings.Repeat("a", 64),
			ModelSettingsRevision: &revision, CreatedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
		},
		buildStatus: retrievaldomain.BuildStatus{WorkspaceID: workspaceID, IndexVersionID: indexID, VectorPendingCount: 1},
	}
	factory := &workerRuntimeTestFactory{}
	var compatibleCalls atomic.Int64
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: revision, InstanceID: newWorkerRuntimeTestID(t),
			},
			Value: &workerRuntimeGeneration{sources: workerReindexTestSources(fusion, &embeddingID)},
		},
		Factory: factory,
		EmbeddingCompatible: func(*workerRuntimeGeneration, retrievaldomain.EmbeddingVersion) error {
			compatibleCalls.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	acquirer, err := newWorkerReindexProcessorAcquirer(store, workerReindexContextStub{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	admission, err := acquirer.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	lease, err := admission.Acquire(context.Background(), retrievaldomain.Delivery{
		WorkspaceID: workspaceID, IndexVersionID: &indexID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Processor() == nil {
		t.Fatal("persisted reindex binding returned a nil processor")
	}
	lease.Release()
	lease.Release()
	if lease.Processor() != nil {
		t.Fatal("released reindex lease still exposes its processor")
	}
	if store.indexCalls.Load() != 1 || store.embeddingCalls.Load() != 1 || store.statusCalls.Load() != 1 || compatibleCalls.Load() != 1 {
		t.Fatalf("index calls=%d embedding calls=%d status calls=%d compatibility calls=%d, want 1/1/1/1", store.indexCalls.Load(), store.embeddingCalls.Load(), store.statusCalls.Load(), compatibleCalls.Load())
	}
	if got := factory.builds.Load(); got != 0 {
		t.Fatalf("historical compatible contract triggered %d generation builds", got)
	}
}

func TestWorkerReindexAcquirerDoesNotRebuildEmbeddingWhenNoVectorWorkRemains(t *testing.T) {
	workspaceID := newWorkerRuntimeTestID(t)
	indexID := newWorkerRuntimeTestID(t)
	embeddingID := newWorkerRuntimeTestID(t)
	revisionZero := int64(0)
	fusion, err := configuredRRF(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	store := &workerReindexRuntimeStoreStub{
		index: retrievaldomain.IndexVersion{
			ID: indexID, WorkspaceID: workspaceID, EmbeddingVersionID: &embeddingID,
			TokenizerID: retrievalapplication.FTSOnlyTokenizerID, TokenizerVersion: retrievalapplication.FTSOnlyTokenizerVersion,
			TokenizerConfigHash: retrievalapplication.FTSOnlyTokenizerConfigHash, FusionConfig: fusion,
		},
		embedding: retrievaldomain.EmbeddingVersion{
			ID: embeddingID, Provider: "openai-compatible", AdapterName: "eino-openai", AdapterVersion: "v1",
			Model: "retired-embedding", Dimensions: 3, Normalization: retrievaldomain.NormalizationL2,
			DistanceMetric: retrievaldomain.DistanceCosine, ConfigHash: strings.Repeat("c", 64),
			ModelSettingsRevision: &revisionZero, CreatedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
		},
		buildStatus: retrievaldomain.BuildStatus{WorkspaceID: workspaceID, IndexVersionID: indexID, VectorPendingCount: 0},
	}
	factory := &workerRuntimeTestFactory{}
	var compatibleCalls atomic.Int64
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 1, InstanceID: newWorkerRuntimeTestID(t),
			},
			Value: &workerRuntimeGeneration{sources: workerReindexTestSources(fusion, &embeddingID)},
		},
		Factory: factory,
		EmbeddingCompatible: func(*workerRuntimeGeneration, retrievaldomain.EmbeddingVersion) error {
			compatibleCalls.Add(1)
			return errors.New("current runtime is intentionally incompatible")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	acquirer, err := newWorkerReindexProcessorAcquirer(store, workerReindexContextStub{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	admission, err := acquirer.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	lease, err := admission.Acquire(context.Background(), retrievaldomain.Delivery{WorkspaceID: workspaceID, IndexVersionID: &indexID})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if compatibleCalls.Load() != 0 || factory.builds.Load() != 0 {
		t.Fatalf("no-vector recovery compatibility calls=%d builds=%d, want 0/0", compatibleCalls.Load(), factory.builds.Load())
	}
}

func TestWorkerReindexAcquirerFailsClosedForUnreconstructableHistoricalEmbedding(t *testing.T) {
	workspaceID := newWorkerRuntimeTestID(t)
	indexID := newWorkerRuntimeTestID(t)
	embeddingID := newWorkerRuntimeTestID(t)
	revisionZero := int64(0)
	store := &workerReindexRuntimeStoreStub{
		index: retrievaldomain.IndexVersion{ID: indexID, WorkspaceID: workspaceID, EmbeddingVersionID: &embeddingID},
		embedding: retrievaldomain.EmbeddingVersion{
			ID: embeddingID, Provider: "openai-compatible", AdapterName: "eino-openai", AdapterVersion: "v1",
			Model: "embedding-v0", Dimensions: 3, Normalization: retrievaldomain.NormalizationL2,
			DistanceMetric: retrievaldomain.DistanceCosine, ConfigHash: strings.Repeat("b", 64),
			ModelSettingsRevision: &revisionZero, CreatedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
		},
		buildStatus: retrievaldomain.BuildStatus{WorkspaceID: workspaceID, IndexVersionID: indexID, VectorPendingCount: 1},
	}
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 1, InstanceID: newWorkerRuntimeTestID(t),
			},
			Value: &workerRuntimeGeneration{},
		},
		Factory: factory,
		EmbeddingCompatible: func(*workerRuntimeGeneration, retrievaldomain.EmbeddingVersion) error {
			return errors.New("incompatible historical embedding")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	acquirer, err := newWorkerReindexProcessorAcquirer(store, workerReindexContextStub{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquirer.bind(host); err != nil {
		t.Fatal(err)
	}
	admission, err := acquirer.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	_, err = admission.Acquire(context.Background(), retrievaldomain.Delivery{WorkspaceID: workspaceID, IndexVersionID: &indexID})
	assertWorkerRuntimeErrorCode(t, err, modelsettingsruntime.ErrorCodeRuntimeEmbeddingContractMismatch)
	if got := factory.builds.Load(); got != 0 {
		t.Fatalf("unreconstructable revision zero triggered %d generation builds", got)
	}
}

func workerReindexTestSources(fusion json.RawMessage, embeddingID *foundation.ID) sourceProcessingComponents {
	options := retrievalapplication.DefaultFTSOnlyProcessorOptions(time.Second)
	options.EmbeddingVersionID = cloneWorkerRuntimeID(embeddingID)
	options.FusionConfig = append(json.RawMessage(nil), fusion...)
	return sourceProcessingComponents{
		workspace: &workspaceapplication.Service{}, ingestion: &ingestionapplication.Service{},
		retrieval: &retrievalapplication.Service{}, vectors: workerReindexVectorStub{},
		regression: &retrievalapplication.RegressionService{}, processorOptions: options,
	}
}

type workerRuntimeRevisionLoaderStub struct {
	loads    atomic.Int64
	resolved modelsettingsdomain.ResolvedSettings
}

func (*workerRuntimeRevisionLoaderStub) Snapshot(context.Context) (modelsettingsdomain.Snapshot, error) {
	return modelsettingsdomain.Snapshot{}, nil
}

func (loader *workerRuntimeRevisionLoaderStub) LoadRevision(_ context.Context, _ int64) (modelsettingsdomain.ResolvedSettings, error) {
	loader.loads.Add(1)
	return loader.resolved, nil
}

func TestWorkerRuntimeGenerationFactoryBuildsModelsAndRegistryTogether(t *testing.T) {
	loader := &workerRuntimeRevisionLoaderStub{resolved: modelsettingsdomain.ResolvedSettings{
		Revision: 7, Settings: modelsettingsdomain.CanonicalDisabledSettings(),
	}}
	registry := newWorkerRuntimeTestRegistry(t)
	var registryBuilds atomic.Int64
	factory := &workerRuntimeGenerationFactory{
		base: config.Defaults(), revisions: loader,
		buildGeneration: func(_ context.Context, models *modelsettingsruntime.Models) (*workerRuntimeGeneration, error) {
			registryBuilds.Add(1)
			if models == nil || models.Revision() != 7 {
				t.Fatalf("executor builder models revision = %v, want 7", models)
			}
			return &workerRuntimeGeneration{executors: registry}, nil
		},
	}
	generation, err := factory.Build(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if generation.models == nil || generation.models.Revision() != 7 || generation.executors != registry {
		t.Fatalf("generation = %#v, want revision 7 and exact registry", generation)
	}
	if err := factory.Probe(context.Background(), generation); err != nil {
		t.Fatal(err)
	}
	factory.Close(generation)
	if got := loader.loads.Load(); got != 1 {
		t.Fatalf("revision load count = %d, want 1", got)
	}
	if got := registryBuilds.Load(); got != 1 {
		t.Fatalf("executor registry build count = %d, want 1", got)
	}
	if _, err := factory.Build(context.Background(), 0); err == nil {
		t.Fatal("managed revision zero rebuild succeeded")
	}
	if got := loader.loads.Load(); got != 1 {
		t.Fatalf("revision zero reached loader; load count = %d, want 1", got)
	}
}

func newWorkerRuntimeTestRegistry(t *testing.T) *workflowapplication.ExecutorRegistry {
	t.Helper()
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(workerRuntimeTestNodeKind, 1, workerRuntimeTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	return registry
}

func newWorkerRuntimeTestID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertWorkerRuntimeErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}
