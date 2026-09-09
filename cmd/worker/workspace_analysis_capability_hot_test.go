package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkerWorkspaceAnalysisCapabilityFollowsCurrentModelGeneration(t *testing.T) {
	cfg := workspaceAnalysisCapabilityTestConfig(t)
	disabled := workspaceAnalysisCapabilityTestGeneration(t, cfg, 0, false, time.Second)
	source := &workspaceAnalysisCapabilityCurrentStub{generation: disabled}
	advertiser, store := workspaceAnalysisCapabilityTestAdvertiser(t, cfg, disabled, source)
	store.beforeWrite = func(string) {
		if source.active != 1 {
			t.Fatal("capability write outlived its current generation lease")
		}
	}
	if advertiser.advertisement.Contract.DefinitionVersion != 2 || advertiser.advertisement.Contract.PolicyVersion != 2 {
		t.Fatal("managed startup lost the fixed v2 contract")
	}
	if err := advertiser.Advertise(t.Context()); err == nil || len(store.calls) != 0 {
		t.Fatal("disabled startup advertised a capability")
	}
	// The same advertiser survives startup without a model or a startup
	// definition registry. A later immutable generation is enough to enable it.
	source.generation = workspaceAnalysisCapabilityTestGeneration(t, cfg, 1, true, time.Second)
	if err := advertiser.Renew(t.Context()); err != nil || store.advertises != 1 {
		t.Fatalf("later model activation did not advertise: %v", err)
	}
	identity := store.advertisement
	if err := advertiser.Renew(t.Context()); err != nil || store.heartbeats != 1 {
		t.Fatalf("current generation did not renew: %v", err)
	}
	before := len(store.calls)
	source.generation = workspaceAnalysisCapabilityTestGeneration(t, cfg, 2, false, time.Second)
	if err := advertiser.Renew(t.Context()); err == nil || len(store.calls) != before {
		t.Fatal("disabled generation renewed the old advertisement")
	}
	// A temporary capability loss must not permanently release the worker ID.
	store.expired = true
	source.generation = workspaceAnalysisCapabilityTestGeneration(t, cfg, 3, true, time.Second)
	if err := advertiser.Renew(t.Context()); err != nil || store.advertises != 2 || store.releases != 0 || store.advertisement != identity {
		t.Fatalf("re-enabled generation could not revive its expired exact advertisement: %v", err)
	}
	before = len(store.calls)
	source.generation = workspaceAnalysisCapabilityTestGeneration(t, cfg, 4, true, 2*time.Second)
	if err := advertiser.Renew(t.Context()); err == nil || len(store.calls) != before {
		t.Fatal("a model timeout beyond the Worker budget renewed the old capability")
	}
	source.generation = workspaceAnalysisCapabilityTestGeneration(t, cfg, 5, true, time.Second)
	if err := advertiser.Renew(t.Context()); err != nil {
		t.Fatalf("compatible model replacement did not recover: %v", err)
	}
	if source.active != 0 || source.acquisitions != source.releases {
		t.Fatal("a capability check leaked a runtime lease")
	}
}

func TestWorkerWorkspaceAnalysisCapabilityFailsClosedForCurrentRuntimeChanges(t *testing.T) {
	cfg := workspaceAnalysisCapabilityTestConfig(t)
	ready := workspaceAnalysisCapabilityTestGeneration(t, cfg, 1, true, time.Second)
	disabled := workspaceAnalysisCapabilityTestGeneration(t, cfg, 2, false, time.Second)
	for _, test := range []struct {
		name   string
		change func(*workspaceAnalysisCapabilityCurrentStub)
	}{
		{"unknown generation", func(source *workspaceAnalysisCapabilityCurrentStub) { source.generation = nil }},
		{"disabled model with stale ready metadata", func(source *workspaceAnalysisCapabilityCurrentStub) { source.generation.models = disabled.models }},
		{"unavailable composition", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.generation.workspaceAnalysis.capability = agentCapabilityStatus{}
		}},
		{"missing tool contracts", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.generation.workspaceAnalysis.contracts = nil
		}},
		{"tool contracts without executors", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.generation.workspaceAnalysis.executors = source.generation.workspaceAnalysis.contracts
		}},
		{"unfrozen tool registry", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.generation.workspaceAnalysis.executors = toolsapplication.NewExecutionRegistry()
		}},
		{"missing workflow node", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.generation.executors = newWorkerRuntimeTestRegistry(t)
		}},
		{"placeholder workflow nodes", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.generation.executors, _ = workspaceAnalysisCapabilityTestWorkflow(t, source.generation.workspaceAnalysis.contracts,
				workerUnavailableExecutor{code: agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable})
		}},
		{"binding drift", func(source *workspaceAnalysisCapabilityCurrentStub) { source.revisionOffset = 1 }},
		{"acquisition failure with lease", func(source *workspaceAnalysisCapabilityCurrentStub) {
			source.err = errors.New("current runtime unavailable")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *ready
			source := &workspaceAnalysisCapabilityCurrentStub{generation: &copy}
			advertiser, store := workspaceAnalysisCapabilityTestAdvertiser(t, cfg, ready, source)
			if err := advertiser.Advertise(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := len(store.calls)
			test.change(source)
			if err := advertiser.Renew(t.Context()); err == nil || len(store.calls) != before {
				t.Fatal("invalid current runtime renewed an old advertisement")
			}
			if source.active != 0 || source.acquisitions != source.releases {
				t.Fatal("rejected current runtime leaked its lease")
			}
		})
	}
}

func TestWorkerWorkspaceAnalysisCapabilityWaitsForRuntimeGateWithoutRenewing(t *testing.T) {
	cfg := workspaceAnalysisCapabilityTestConfig(t)
	ready := workspaceAnalysisCapabilityTestGeneration(t, cfg, 1, true, time.Second)
	source := &workspaceAnalysisCapabilityCurrentStub{generation: ready}
	advertiser, store := workspaceAnalysisCapabilityTestAdvertiser(t, cfg, ready, source)
	if err := advertiser.Advertise(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(store.calls)
	source.gated = true
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := advertiser.Renew(ctx); !errors.Is(err, context.DeadlineExceeded) || len(store.calls) != before {
		t.Fatalf("closed acquisition gate refreshed a capability or lost its deadline: %v", err)
	}
	source.gated = false
	if err := advertiser.Renew(t.Context()); err != nil {
		t.Fatalf("reopened gate did not resume renewal: %v", err)
	}
}

func TestWorkerWorkspaceAnalysisCapabilityPinsRealHostUntilAdvertisementCompletes(t *testing.T) {
	cfg := workspaceAnalysisCapabilityTestConfig(t)
	generation := workspaceAnalysisCapabilityTestGeneration(t, cfg, 1, true, time.Second)
	factory := &workerRuntimeTestFactory{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*workerRuntimeGeneration]{
		Initial: modelsettingsruntime.InitialRuntime[*workerRuntimeGeneration]{
			Binding: modelsettingsruntime.RuntimeBinding{
				Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker,
				Revision: 1, InstanceID: newWorkerRuntimeTestID(t),
			}, Value: generation,
		}, Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	advertiser, store := workspaceAnalysisCapabilityTestAdvertiser(t, cfg, generation, host)
	store.beforeWrite = func(string) {
		host.Close()
		if factory.closes.Load() != 0 {
			t.Fatal("Host retired the leased model while its capability was being written")
		}
	}
	if err := advertiser.Advertise(t.Context()); err != nil {
		t.Fatal(err)
	}
	if factory.closes.Load() != 1 {
		t.Fatal("completed advertisement retained a closed Host generation")
	}
	before := len(store.calls)
	if err := advertiser.Renew(t.Context()); err == nil || len(store.calls) != before {
		t.Fatal("a closed Host continued renewing its old advertisement")
	}
	store.beforeWrite = nil
	if err := advertiser.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := advertiser.Release(t.Context()); err != nil || store.releases != 1 {
		t.Fatalf("shutdown release was not idempotent: %v", err)
	}
}

func TestWorkerWorkspaceAnalysisCapabilityRetainsStaticConstructor(t *testing.T) {
	cfg := workspaceAnalysisCapabilityTestConfig(t)
	cfg.ModelSettingsMode = config.ModelSettingsModeStatic
	cfg.ModelSettingsKeyFile = ""
	generation := workspaceAnalysisCapabilityTestGeneration(t, cfg, 0, true, time.Second)
	_, definitions := workspaceAnalysisCapabilityTestWorkflow(t, generation.workspaceAnalysis.contracts, workerRuntimeTestExecutor{})
	components := workspaceAnalysisCapabilityTestComponents(t, generation)
	components.executors, components.definitions = generation.executors, definitions
	var absent *modelsettingsruntime.RuntimeHost[*workerRuntimeGeneration]
	for _, runtimes := range [][]workerRuntimeAttemptAcquirer{nil, {absent}} {
		advertiser, err := newWorkerWorkspaceAnalysisCapability(workspaceAnalysisCapabilityTestPool(t), cfg, components, generation.models, runtimes...)
		if err != nil || advertiser == nil {
			t.Fatalf("static constructor changed its compatibility: %v", err)
		}
		store := &workspaceAnalysisCapabilityStoreStub{}
		advertiser.service, err = agentapplication.NewWorkspaceAnalysisCapabilityService(store)
		if err != nil {
			t.Fatal(err)
		}
		if err := advertiser.Advertise(t.Context()); err != nil || store.advertises != 1 {
			t.Fatalf("static capability did not advertise: %v", err)
		}
	}
}

func workspaceAnalysisCapabilityTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.ModelSettingsMode = config.ModelSettingsModeManaged
	cfg.ModelSettingsKeyFile = "/run/secrets/capability-constructor.key"
	cfg.WorkspaceAnalysisWorkerEnabled = true
	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2()
	if err != nil {
		t.Fatal(err)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV2Deadlines(agentdomain.WorkspaceAnalysisV2Timeouts{
		PlanModelTimeout: time.Second, SynthesisModelTimeout: time.Second, ReviewModelTimeout: time.Second,
		GitToolTimeout: snapshot.ReadGitStatusTimeout, SearchToolTimeout: snapshot.SearchKnowledgeTimeout,
		SourceReadToolTimeout: snapshot.ReadSourceTimeout, ValidateCitationToolTimeout: snapshot.ValidateCitationTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkerJobTimeout = deadlines.MinimumRiverJobTimeout()
	return cfg
}

func workspaceAnalysisCapabilityTestGeneration(t *testing.T, cfg config.Config, revision int64, enabled bool, timeout time.Duration) *workerRuntimeGeneration {
	t.Helper()
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	if enabled {
		settings.Chat.Provider = modelsettingsdomain.ChatProviderOpenAICompatible
		settings.Chat.BaseURL = modelsettingsdomain.ManagedOllamaBaseURL
		settings.Chat.Model = "capability-constructor"
		settings.Chat.ModelVersion = fmt.Sprintf("revision-%d", revision)
		settings.Chat.Timeout = timeout
	}
	// The production model factory only builds clients here; no model, tool,
	// database or network operation is used by these lifecycle unit tests.
	models, err := modelsettingsruntime.Build(cfg, modelsettingsdomain.ResolvedSettings{Revision: revision, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close() })
	contracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	toolExecutors := toolsapplication.NewExecutionRegistry()
	for _, tool := range snapshot.Tools {
		ref := toolsdomain.ToolRef{Name: tool.Name, Version: tool.Version}
		contract, err := contracts.ResolveContract(ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := toolExecutors.RegisterContract(contract); err != nil {
			t.Fatal(err)
		}
		if err := toolExecutors.RegisterExecutor(ref, workspaceAnalysisCapabilityToolStub{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := toolExecutors.Freeze(); err != nil {
		t.Fatal(err)
	}
	executors, _ := workspaceAnalysisCapabilityTestWorkflow(t, contracts, workerRuntimeTestExecutor{})
	return &workerRuntimeGeneration{
		models: models, executors: executors,
		workspaceAnalysis: newWorkerWorkspaceAnalysisGeneration(agentCapabilityStatus{available: enabled}, toolRuntimeComponents{
			contracts: contracts, executions: toolExecutors, execution: &toolsapplication.ExecutionService{},
			workspaceAnalysisCapability: agentCapabilityStatus{available: true},
		}),
	}
}

func workspaceAnalysisCapabilityTestWorkflow(t *testing.T, contracts *toolsapplication.Registry, executor workflowapplication.Executor) (*workflowapplication.ExecutorRegistry, *workflowapplication.DefinitionRegistry) {
	t.Helper()
	catalog, err := workflowapplication.NewValidationCatalog([]int{1, 2}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	for _, node := range definition.Graph.Nodes {
		if err := executors.Register(node.Kind, node.InputSchemaVersion, executor); err != nil {
			t.Fatal(err)
		}
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, contracts)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	return executors, definitions
}

func workspaceAnalysisCapabilityTestComponents(t *testing.T, generation *workerRuntimeGeneration) workerComponents {
	t.Helper()
	return workerComponents{
		workerID: newWorkerRuntimeTestID(t), workspaceAnalysisCapability: generation.workspaceAnalysis.capability,
		tools: toolRuntimeComponents{
			contracts: generation.workspaceAnalysis.contracts, executions: generation.workspaceAnalysis.executors,
			execution: &toolsapplication.ExecutionService{}, workspaceAnalysisCapability: agentCapabilityStatus{available: true},
		},
	}
}

func workspaceAnalysisCapabilityTestAdvertiser(t *testing.T, cfg config.Config, initial *workerRuntimeGeneration, source workerRuntimeAttemptAcquirer) (*workerWorkspaceAnalysisCapability, *workspaceAnalysisCapabilityStoreStub) {
	t.Helper()
	advertiser, err := newWorkerWorkspaceAnalysisCapability(workspaceAnalysisCapabilityTestPool(t), cfg,
		workspaceAnalysisCapabilityTestComponents(t, initial), initial.models, source)
	if err != nil {
		t.Fatal(err)
	}
	store := &workspaceAnalysisCapabilityStoreStub{}
	advertiser.service, err = agentapplication.NewWorkspaceAnalysisCapabilityService(store)
	if err != nil {
		t.Fatal(err)
	}
	return advertiser, store
}

func workspaceAnalysisCapabilityTestPool(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	pool, err := platformpostgres.Open(t.Context(), "postgres://postgres@127.0.0.1:1/capability_constructor?sslmode=disable", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type workspaceAnalysisCapabilityToolStub struct{}

func (workspaceAnalysisCapabilityToolStub) Execute(context.Context, toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	return toolsapplication.ExecutorResult{}, errors.New("capability checks must not execute a tool")
}

type workspaceAnalysisCapabilityCurrentStub struct {
	generation                     *workerRuntimeGeneration
	err                            error
	gated                          bool
	revisionOffset                 int64
	acquisitions, releases, active int
}

func (source *workspaceAnalysisCapabilityCurrentStub) Acquire(ctx context.Context, target modelsettingsruntime.RuntimeTarget) (modelsettingsruntime.RuntimeLease[*workerRuntimeGeneration], error) {
	if target.String() != modelsettingsruntime.CurrentRuntime().String() {
		return nil, errors.New("capability selected a historical runtime")
	}
	if source.gated {
		<-ctx.Done()
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsruntime.ErrorCodeRuntimeSwitching, true, ctx.Err())
	}
	source.acquisitions++
	source.active++
	revision := source.revisionOffset
	if source.generation != nil && source.generation.models != nil {
		revision += source.generation.models.Revision()
	}
	return &workspaceAnalysisCapabilityLeaseStub{
		source: source, generation: source.generation,
		binding: modelsettingsruntime.RuntimeBinding{Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleWorker, Revision: revision},
	}, source.err
}

type workspaceAnalysisCapabilityLeaseStub struct {
	source     *workspaceAnalysisCapabilityCurrentStub
	generation *workerRuntimeGeneration
	binding    modelsettingsruntime.RuntimeBinding
	released   bool
}

func (lease *workspaceAnalysisCapabilityLeaseStub) Binding() modelsettingsruntime.RuntimeBinding {
	return lease.binding
}
func (lease *workspaceAnalysisCapabilityLeaseStub) Value() *workerRuntimeGeneration {
	return lease.generation
}
func (lease *workspaceAnalysisCapabilityLeaseStub) Release() {
	if !lease.released {
		lease.released = true
		lease.source.active--
		lease.source.releases++
	}
}

type workspaceAnalysisCapabilityStoreStub struct {
	advertisement                    agentapplication.WorkspaceAnalysisWorkerAdvertisement
	exists, expired, released        bool
	calls                            []string
	advertises, heartbeats, releases int
	beforeWrite                      func(string)
}

func (store *workspaceAnalysisCapabilityStoreStub) before(kind string) {
	store.calls = append(store.calls, kind)
	if store.beforeWrite != nil {
		store.beforeWrite(kind)
	}
}

func (store *workspaceAnalysisCapabilityStoreStub) AdvertiseWorkspaceAnalysisWorker(_ context.Context, advertisement agentapplication.WorkspaceAnalysisWorkerAdvertisement) (agentapplication.WorkspaceAnalysisWorkerCapability, error) {
	store.before("advertise")
	if store.released || store.exists && advertisement != store.advertisement {
		return agentapplication.WorkspaceAnalysisWorkerCapability{}, errors.New("immutable capability conflict")
	}
	store.exists, store.expired, store.advertisement = true, false, advertisement
	store.advertises++
	return agentapplication.WorkspaceAnalysisWorkerCapability{WorkspaceAnalysisWorkerAdvertisement: advertisement}, nil
}

func (store *workspaceAnalysisCapabilityStoreStub) HeartbeatWorkspaceAnalysisWorker(_ context.Context, advertisement agentapplication.WorkspaceAnalysisWorkerAdvertisement) (agentapplication.WorkspaceAnalysisWorkerCapability, error) {
	store.before("heartbeat")
	if !store.exists || store.expired || store.released || advertisement != store.advertisement {
		return agentapplication.WorkspaceAnalysisWorkerCapability{}, errors.New("capability lease is absent or expired")
	}
	store.heartbeats++
	return agentapplication.WorkspaceAnalysisWorkerCapability{WorkspaceAnalysisWorkerAdvertisement: advertisement}, nil
}

func (store *workspaceAnalysisCapabilityStoreStub) ReleaseWorkspaceAnalysisWorker(_ context.Context, advertisement agentapplication.WorkspaceAnalysisWorkerAdvertisement) (agentapplication.WorkspaceAnalysisWorkerCapability, error) {
	store.before("release")
	if !store.exists || store.released || advertisement != store.advertisement {
		return agentapplication.WorkspaceAnalysisWorkerCapability{}, errors.New("capability release conflict")
	}
	store.released = true
	store.releases++
	return agentapplication.WorkspaceAnalysisWorkerCapability{WorkspaceAnalysisWorkerAdvertisement: advertisement}, nil
}
