package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// workerRuntimeGeneration keeps every model-bound Workflow executor with the
// Models instance that constructed it. A Host lease therefore protects both
// from retirement until the Workflow Attempt has settled.
type workerRuntimeGeneration struct {
	models    *modelsettingsruntime.Models
	executors *workflowapplication.ExecutorRegistry
	sources   sourceProcessingComponents
}

type workerUnavailableExecutor struct {
	code string
}

func (executor workerUnavailableExecutor) Execute(context.Context, workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	return workflowapplication.ExecutionResult{}, foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		executor.code,
		false,
		errors.New("model-dependent workflow capability is unavailable"),
	)
}

type workerRuntimeGenerationBuilder func(context.Context, *modelsettingsruntime.Models) (*workerRuntimeGeneration, error)

type workerRuntimeGenerationFactory struct {
	base            config.Config
	revisions       modelsettingsapplication.RevisionLoader
	buildGeneration workerRuntimeGenerationBuilder
}

func (factory *workerRuntimeGenerationFactory) Build(ctx context.Context, revision int64) (*workerRuntimeGeneration, error) {
	if factory == nil || workerRuntimeNilDependency(factory.revisions) || factory.buildGeneration == nil || ctx == nil || revision <= 0 {
		return nil, workerRuntimeRevisionError(errors.New("worker runtime generation build input is invalid"))
	}
	resolved, err := factory.revisions.LoadRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	defer resolved.ChatAPIKey.Destroy()
	defer resolved.EmbeddingAPIKey.Destroy()
	if resolved.Revision != revision {
		return nil, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			modelsettingsdomain.ErrorCodeCorrupt,
			false,
			errors.New("loaded worker runtime revision does not match target"),
		)
	}
	models, err := modelsettingsruntime.Build(factory.base, resolved)
	if err != nil {
		return nil, err
	}
	generation, err := factory.buildGeneration(ctx, models)
	if err != nil {
		return nil, errors.Join(err, models.Close())
	}
	if generation == nil || generation.executors == nil || generation.models != nil {
		return nil, errors.Join(
			workerRuntimeNotReadyError(errors.New("worker runtime generation components are unavailable")),
			models.Close(),
		)
	}
	generation.models = models
	return generation, nil
}

func (factory *workerRuntimeGenerationFactory) Probe(ctx context.Context, generation *workerRuntimeGeneration) error {
	if factory == nil || ctx == nil || generation == nil || generation.models == nil || generation.executors == nil {
		return workerRuntimeNotReadyError(errors.New("worker runtime generation probe is unavailable"))
	}
	chat := generation.models.Chat()
	if chat.State() == platformmodels.CapabilityConfigured {
		prober, ok := chat.Model().(platformmodels.ChatConnectionProber)
		if !ok || prober == nil {
			return foundation.NewError(
				foundation.ErrorConsistencyViolation,
				modelsettingsdomain.ErrorCodeUnavailable,
				false,
				errors.New("worker chat generation probe is unavailable"),
			)
		}
		if err := prober.ProbeConnection(ctx); err != nil {
			return err
		}
	}
	embedding := generation.models.Embedding()
	if embedding.State() == platformmodels.CapabilityConfigured {
		if embedding.Embedder() == nil {
			return foundation.NewError(
				foundation.ErrorConsistencyViolation,
				modelsettingsdomain.ErrorCodeUnavailable,
				false,
				errors.New("worker embedding generation probe is unavailable"),
			)
		}
		if _, err := embedding.Embedder().Embed(ctx, retrievalapplication.EmbedRequest{Inputs: []string{"test"}}); err != nil {
			return err
		}
	}
	return nil
}

func (factory *workerRuntimeGenerationFactory) Close(generation *workerRuntimeGeneration) {
	if generation != nil && generation.models != nil {
		_ = generation.models.Close()
	}
}

// workerRuntimeExecutorAcquirer is installed into RuntimeNodeWorker before its
// Host exists, then bound exactly once before the River client is started.
type workerRuntimeExecutorAcquirer struct {
	mu   sync.RWMutex
	host *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]
}

func (acquirer *workerRuntimeExecutorAcquirer) bind(host *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]) error {
	if acquirer == nil || host == nil {
		return workerRuntimeNotReadyError(errors.New("worker runtime host is unavailable"))
	}
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	if acquirer.host != nil {
		return workerRuntimeBindingError(errors.New("worker runtime host is already bound"))
	}
	acquirer.host = host
	return nil
}

// Admit 在 Workflow Claim 前固定当前 Worker generation。
func (acquirer *workerRuntimeExecutorAcquirer) Admit(ctx context.Context) (riveradapter.RuntimeExecutorAdmission, error) {
	if acquirer == nil || ctx == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime executor admitter is unavailable"))
	}
	acquirer.mu.RLock()
	host := acquirer.host
	acquirer.mu.RUnlock()
	if host == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime host is not bound"))
	}
	admission, err := host.Admit(ctx)
	if err != nil {
		return nil, err
	}
	return &workerRuntimeExecutorAdmission{admission: admission}, nil
}

func (acquirer *workerRuntimeExecutorAcquirer) AcquireAttempt(ctx context.Context, attempt workflowdomain.NodeAttempt) (riveradapter.RuntimeExecutorLease, error) {
	if acquirer == nil || ctx == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime executor acquirer is unavailable"))
	}
	binding, err := workerRuntimeAttemptBinding(attempt)
	if err != nil {
		return nil, err
	}
	acquirer.mu.RLock()
	host := acquirer.host
	acquirer.mu.RUnlock()
	if host == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime host is not bound"))
	}
	return acquireWorkerRuntimeExecutor(ctx, host, binding)
}

type workerRuntimeAttemptAcquirer interface {
	Acquire(context.Context, modelsettingsruntime.RuntimeTarget) (modelsettingsruntime.RuntimeLease[*workerRuntimeGeneration], error)
}

func workerRuntimeAttemptBinding(attempt workflowdomain.NodeAttempt) (modelsettingsruntime.RuntimeBinding, error) {
	if attempt.ModelSettingsRevision == nil || attempt.ModelRuntimeInstanceID == nil || *attempt.ModelSettingsRevision < 0 {
		return modelsettingsruntime.RuntimeBinding{}, workerRuntimeBindingError(errors.New("workflow attempt has no managed worker runtime binding"))
	}
	instanceID := *attempt.ModelRuntimeInstanceID
	parsed, err := foundation.ParseID(string(instanceID))
	if err != nil || parsed != instanceID {
		return modelsettingsruntime.RuntimeBinding{}, workerRuntimeBindingError(errors.New("workflow attempt worker runtime instance is invalid"))
	}
	return modelsettingsruntime.RuntimeBinding{
		Mode:       modelsettingsruntime.RuntimeModeManaged,
		Role:       modelsettingsruntime.RuntimeRoleWorker,
		Revision:   *attempt.ModelSettingsRevision,
		InstanceID: instanceID,
	}, nil
}

func acquireWorkerRuntimeExecutor(ctx context.Context, acquirer workerRuntimeAttemptAcquirer, binding modelsettingsruntime.RuntimeBinding) (riveradapter.RuntimeExecutorLease, error) {
	if ctx == nil || acquirer == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime attempt acquirer is unavailable"))
	}
	lease, err := acquirer.Acquire(ctx, modelsettingsruntime.AttemptRuntime(binding))
	if err != nil {
		return nil, err
	}
	generation := lease.Value()
	if generation == nil || generation.executors == nil || lease.Binding() != binding {
		lease.Release()
		return nil, workerRuntimeBindingError(errors.New("workflow attempt acquired an inconsistent worker runtime generation"))
	}
	return &workerRuntimeExecutorLease{lease: lease}, nil
}

type workerRuntimeExecutorAdmission struct {
	mu        sync.Mutex
	admission modelsettingsruntime.RuntimeAdmission[*workerRuntimeGeneration]
}

// AcquireAttempt 使用 Claim 前固定的 admission 解析持久化 Attempt binding。
func (admission *workerRuntimeExecutorAdmission) AcquireAttempt(ctx context.Context, attempt workflowdomain.NodeAttempt) (riveradapter.RuntimeExecutorLease, error) {
	if admission == nil || ctx == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime executor admission is unavailable"))
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if admission.admission == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker runtime executor admission is released"))
	}
	binding, err := workerRuntimeAttemptBinding(attempt)
	if err != nil {
		return nil, err
	}
	return acquireWorkerRuntimeExecutor(ctx, admission.admission, binding)
}

// Release 幂等释放 Claim 入口 generation pin。
func (admission *workerRuntimeExecutorAdmission) Release() {
	if admission == nil {
		return
	}
	admission.mu.Lock()
	runtimeAdmission := admission.admission
	admission.admission = nil
	admission.mu.Unlock()
	if runtimeAdmission != nil {
		runtimeAdmission.Release()
	}
}

type workerRuntimeExecutorLease struct {
	lease modelsettingsruntime.RuntimeLease[*workerRuntimeGeneration]
}

func (lease *workerRuntimeExecutorLease) Executors() *workflowapplication.ExecutorRegistry {
	if lease == nil || lease.lease == nil {
		return nil
	}
	generation := lease.lease.Value()
	if generation == nil {
		return nil
	}
	return generation.executors
}

func (lease *workerRuntimeExecutorLease) Release() {
	if lease != nil && lease.lease != nil {
		lease.lease.Release()
	}
}

type workerSourceRefreshAcquirer struct {
	mu   sync.RWMutex
	host *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]
}

func (acquirer *workerSourceRefreshAcquirer) bind(host *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]) error {
	if acquirer == nil || host == nil {
		return workerRuntimeNotReadyError(errors.New("worker source refresh runtime host is unavailable"))
	}
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	if acquirer.host != nil {
		return workerRuntimeBindingError(errors.New("worker source refresh runtime host is already bound"))
	}
	acquirer.host = host
	return nil
}

func (acquirer *workerSourceRefreshAcquirer) Acquire(ctx context.Context) (retrievalapplication.SourceRefreshOperationLease, error) {
	if acquirer == nil || ctx == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker source refresh runtime acquirer is unavailable"))
	}
	acquirer.mu.RLock()
	host := acquirer.host
	acquirer.mu.RUnlock()
	if host == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker source refresh runtime host is not bound"))
	}
	lease, err := host.Acquire(ctx, modelsettingsruntime.CurrentRuntime())
	if err != nil {
		return nil, err
	}
	generation := lease.Value()
	if generation == nil || generation.sources.refresher == nil {
		lease.Release()
		return nil, workerRuntimeNotReadyError(errors.New("worker source refresh generation is unavailable"))
	}
	return &workerSourceRefreshLease{lease: lease}, nil
}

type workerSourceRefreshLease struct {
	lease modelsettingsruntime.RuntimeLease[*workerRuntimeGeneration]
}

func (lease *workerSourceRefreshLease) Runner() retrievalapplication.SourceRefreshOperationRunner {
	if lease == nil || lease.lease == nil {
		return nil
	}
	generation := lease.lease.Value()
	if generation == nil {
		return nil
	}
	return generation.sources.refresher
}

func (lease *workerSourceRefreshLease) Release() {
	if lease != nil && lease.lease != nil {
		lease.lease.Release()
	}
}

type workerReindexRuntimeStore interface {
	GetIndex(context.Context, foundation.ID, foundation.ID) (retrievaldomain.IndexVersion, error)
	GetEmbeddingVersion(context.Context, foundation.ID) (retrievaldomain.EmbeddingVersion, error)
	GetBuildStatus(context.Context, foundation.ID, foundation.ID) (retrievaldomain.BuildStatus, error)
}

type workerReindexProcessorAcquirer struct {
	mu         sync.RWMutex
	host       *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]
	store      workerReindexRuntimeStore
	contexts   retrievalapplication.ProcessorContextLoader
	retryDelay time.Duration
}

func newWorkerReindexProcessorAcquirer(
	store workerReindexRuntimeStore,
	contexts retrievalapplication.ProcessorContextLoader,
	retryDelay time.Duration,
) (*workerReindexProcessorAcquirer, error) {
	if workerRuntimeNilDependency(store) || workerRuntimeNilDependency(contexts) || retryDelay <= 0 || retryDelay > retrievalapplication.MaxDeliveryRetryDelay {
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex runtime dependencies are unavailable"))
	}
	return &workerReindexProcessorAcquirer{store: store, contexts: contexts, retryDelay: retryDelay}, nil
}

func (acquirer *workerReindexProcessorAcquirer) bind(host *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]) error {
	if acquirer == nil || host == nil {
		return workerRuntimeNotReadyError(errors.New("worker reindex runtime host is unavailable"))
	}
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	if acquirer.host != nil {
		return workerRuntimeBindingError(errors.New("worker reindex runtime host is already bound"))
	}
	acquirer.host = host
	return nil
}

func (acquirer *workerReindexProcessorAcquirer) Admit(ctx context.Context) (reindexriver.CompatibleProcessorAdmission, error) {
	if acquirer == nil || ctx == nil || workerRuntimeNilDependency(acquirer.store) || workerRuntimeNilDependency(acquirer.contexts) {
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex runtime acquirer is unavailable"))
	}
	acquirer.mu.RLock()
	host := acquirer.host
	acquirer.mu.RUnlock()
	if host == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex runtime host is not bound"))
	}
	runtimeAdmission, err := host.Admit(ctx)
	if err != nil {
		return nil, err
	}
	return &workerReindexProcessorAdmission{acquirer: acquirer, runtimeAdmission: runtimeAdmission}, nil
}

type workerReindexProcessorAdmission struct {
	mu               sync.Mutex
	acquirer         *workerReindexProcessorAcquirer
	runtimeAdmission modelsettingsruntime.RuntimeAdmission[*workerRuntimeGeneration]
}

func (admission *workerReindexProcessorAdmission) Acquire(ctx context.Context, delivery retrievaldomain.Delivery) (reindexriver.CompatibleProcessorLease, error) {
	if admission == nil || ctx == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex runtime admission is unavailable"))
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	acquirer := admission.acquirer
	runtimeAdmission := admission.runtimeAdmission
	if acquirer == nil || runtimeAdmission == nil {
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex runtime admission is released"))
	}
	if !workerRuntimeID(delivery.WorkspaceID) {
		return nil, workerRuntimeBindingError(errors.New("reindex delivery workspace binding is invalid"))
	}

	target := modelsettingsruntime.CurrentRuntime()
	var index *retrievaldomain.IndexVersion
	if delivery.IndexVersionID != nil {
		if !workerRuntimeID(*delivery.IndexVersionID) {
			return nil, workerRuntimeBindingError(errors.New("reindex delivery index binding is invalid"))
		}
		loaded, err := acquirer.store.GetIndex(ctx, delivery.WorkspaceID, *delivery.IndexVersionID)
		if err != nil {
			return nil, err
		}
		if loaded.ID != *delivery.IndexVersionID || loaded.WorkspaceID != delivery.WorkspaceID {
			return nil, workerRuntimeBindingError(errors.New("reindex index does not match the claimed delivery"))
		}
		index = &loaded
		if loaded.EmbeddingVersionID != nil {
			status, err := acquirer.store.GetBuildStatus(ctx, loaded.WorkspaceID, loaded.ID)
			if err != nil {
				return nil, err
			}
			if status.WorkspaceID != loaded.WorkspaceID || status.IndexVersionID != loaded.ID || status.VectorPendingCount < 0 {
				return nil, workerRuntimeBindingError(errors.New("reindex build status does not match the persisted index"))
			}
			version, err := acquirer.store.GetEmbeddingVersion(ctx, *loaded.EmbeddingVersionID)
			if err != nil {
				return nil, err
			}
			if version.ID != *loaded.EmbeddingVersionID {
				return nil, workerRuntimeBindingError(errors.New("reindex embedding version does not match the persisted index"))
			}
			if status.VectorPendingCount > 0 {
				target = modelsettingsruntime.EmbeddingRuntime(version)
			}
		}
	}

	runtimeLease, err := runtimeAdmission.Acquire(ctx, target)
	if err != nil {
		return nil, err
	}
	generation := runtimeLease.Value()
	if generation == nil || generation.sources.workspace == nil || generation.sources.ingestion == nil ||
		generation.sources.retrieval == nil || generation.sources.regression == nil {
		runtimeLease.Release()
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex generation source graph is unavailable"))
	}
	options := generation.sources.processorOptions
	if index != nil {
		options.EmbeddingVersionID = cloneWorkerRuntimeID(index.EmbeddingVersionID)
		options.TokenizerID = index.TokenizerID
		options.TokenizerVersion = index.TokenizerVersion
		options.TokenizerConfigHash = index.TokenizerConfigHash
		options.FusionConfig = append(json.RawMessage(nil), index.FusionConfig...)
	}
	options.RetryDelay = acquirer.retryDelay
	if options.EmbeddingVersionID != nil && generation.sources.vectors == nil {
		runtimeLease.Release()
		return nil, workerRuntimeNotReadyError(errors.New("worker reindex vector graph is unavailable"))
	}
	processor, err := retrievalapplication.NewProcessor(retrievalapplication.ProcessorDependencies{
		Contexts: acquirer.contexts, Capture: generation.sources.workspace, Ingestion: generation.sources.ingestion,
		Retrieval: generation.sources.retrieval, Vectors: generation.sources.vectors, Regression: generation.sources.regression,
	}, options)
	if err != nil {
		runtimeLease.Release()
		return nil, err
	}
	return &workerReindexProcessorLease{runtimeLease: runtimeLease, processor: processor}, nil
}

func (admission *workerReindexProcessorAdmission) Release() {
	if admission == nil {
		return
	}
	admission.mu.Lock()
	runtimeAdmission := admission.runtimeAdmission
	admission.runtimeAdmission = nil
	admission.acquirer = nil
	admission.mu.Unlock()
	if runtimeAdmission != nil {
		runtimeAdmission.Release()
	}
}

type workerReindexProcessorLease struct {
	mu           sync.RWMutex
	runtimeLease modelsettingsruntime.RuntimeLease[*workerRuntimeGeneration]
	processor    retrievalapplication.ProcessorRunner
}

func (lease *workerReindexProcessorLease) Processor() retrievalapplication.ProcessorRunner {
	if lease == nil {
		return nil
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	return lease.processor
}

func (lease *workerReindexProcessorLease) Release() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	runtimeLease := lease.runtimeLease
	lease.runtimeLease = nil
	lease.processor = nil
	lease.mu.Unlock()
	if runtimeLease != nil {
		runtimeLease.Release()
	}
}

func cloneWorkerRuntimeID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func workerRuntimeID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func workerRuntimeNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func workerRuntimeNotReadyError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsruntime.ErrorCodeRuntimeNotReady, true, cause)
}

func workerRuntimeRevisionError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsruntime.ErrorCodeRuntimeRevisionUnavailable, true, cause)
}

func workerRuntimeBindingError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsruntime.ErrorCodeRuntimeBindingMismatch, false, cause)
}

var _ modelsettingsruntime.GenerationFactory[*workerRuntimeGeneration] = (*workerRuntimeGenerationFactory)(nil)
var _ riveradapter.RuntimeExecutorAcquirer = (*workerRuntimeExecutorAcquirer)(nil)
var _ riveradapter.RuntimeExecutorAdmitter = (*workerRuntimeExecutorAcquirer)(nil)
var _ riveradapter.RuntimeExecutorAdmission = (*workerRuntimeExecutorAdmission)(nil)
var _ riveradapter.RuntimeExecutorLease = (*workerRuntimeExecutorLease)(nil)
var _ retrievalapplication.SourceRefreshOperationAcquirer = (*workerSourceRefreshAcquirer)(nil)
var _ retrievalapplication.SourceRefreshOperationLease = (*workerSourceRefreshLease)(nil)
var _ reindexriver.CompatibleProcessorAcquirer = (*workerReindexProcessorAcquirer)(nil)
var _ reindexriver.CompatibleProcessorAdmission = (*workerReindexProcessorAdmission)(nil)
var _ reindexriver.CompatibleProcessorLease = (*workerReindexProcessorLease)(nil)
