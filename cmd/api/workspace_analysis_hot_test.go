package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
)

func TestAPIWorkspaceAnalysisStarterFollowsCurrentModels(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	disabled := apiWorkspaceAnalysisHotModels(t, cfg, 0, false, 0)
	runtime := &apiWorkspaceAnalysisCurrentRuntimeFake{models: disabled}
	repository := &apiWorkspaceAnalysisRunRepositoryFake{}
	starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, disabled, runtime)
	if err != nil || starter == nil || runtime.calls != 0 {
		t.Fatalf("disabled startup did not retain a model-independent starter: %v", err)
	}
	scope := &apiWorkspaceAnalysisTestScope{}
	repository.before = func(_ string, got foundation.TransactionScope) {
		if got != scope || runtime.active != 1 {
			t.Fatal("start escaped its caller transaction or current generation lease")
		}
	}
	if _, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), scope, apiWorkspaceAnalysisHotCommand(0)); err == nil || repository.readyCalls != 0 || repository.inserts != 0 {
		t.Fatal("disabled current model allowed a new analysis run")
	}
	runtime.models = apiWorkspaceAnalysisHotModels(t, cfg, 1, true, time.Second)
	first, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), scope, apiWorkspaceAnalysisHotCommand(0))
	if err != nil || first.DefinitionVersion != 2 || first.PolicyVersion != 2 || first.Timeouts.PlanModelTimeout != time.Second {
		t.Fatalf("later activation did not start v2 with the current timeout: %v", err)
	}
	if repository.contract.DefinitionVersion != 2 || repository.contract.PolicyVersion != 2 ||
		repository.contract.DefinitionHash != first.DefinitionHash || repository.contract.ToolCatalogHash != first.ToolCatalogHash {
		t.Fatal("new run did not check its exact v2 capability contract")
	}
	runtime.models = apiWorkspaceAnalysisHotModels(t, cfg, 2, true, 2*time.Second)
	second, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), scope, apiWorkspaceAnalysisHotCommand(10))
	if err != nil || second.Timeouts.PlanModelTimeout != 2*time.Second || second.Timeouts.SynthesisModelTimeout != 2*time.Second || second.Timeouts.ReviewModelTimeout != 2*time.Second {
		t.Fatalf("replacement model reused the startup timeout: %v", err)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV2Deadlines(second.Timeouts)
	if err != nil || !second.DeadlineAt.Equal(second.CreatedAt.Add(deadlines.RunDeadline())) ||
		!reflect.DeepEqual(repository.runs[first.QuestionID], first) || !second.DeadlineAt.After(first.DeadlineAt) {
		t.Fatal("new timeout did not freeze a new deadline without changing the prior run")
	}
	runtime.models = apiWorkspaceAnalysisHotModels(t, cfg, 3, false, 0)
	if _, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), scope, apiWorkspaceAnalysisHotCommand(20)); err == nil || repository.readyCalls != 2 || repository.inserts != 2 {
		t.Fatal("disabled replacement continued starting analysis runs")
	}
	if runtime.active != 0 || runtime.calls != runtime.releases {
		t.Fatal("current model lease was not released after every dispatch attempt")
	}
}

func TestAPIWorkspaceAnalysisStarterReplaysWithoutCurrentModels(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	disabled := apiWorkspaceAnalysisHotModels(t, cfg, 0, false, 0)
	for _, version := range []int64{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repository := &apiWorkspaceAnalysisRunRepositoryFake{}
			command := apiWorkspaceAnalysisHotCommand(0)
			seed := apiWorkspaceAnalysisSeedRun(t, repository, command, version)
			repository.readyCalls, repository.inserts = 0, 0
			runtime := &apiWorkspaceAnalysisCurrentRuntimeFake{models: disabled, err: errors.New("live runtime unavailable")}
			starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, disabled, runtime)
			if err != nil {
				t.Fatal(err)
			}
			command.Replayed = true
			got, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, command)
			if err != nil || !reflect.DeepEqual(got, seed) || runtime.calls != 0 || repository.readyCalls != 0 || repository.inserts != 0 || repository.finds != 1 {
				t.Fatalf("v%d replay depended on current availability or rewrote historical facts: %v", version, err)
			}
		})
	}
}

func TestAPIWorkspaceAnalysisStaticStarterReplaysWhenChatDisabled(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	cfg.ModelSettingsMode, cfg.ModelSettingsKeyFile = config.ModelSettingsModeStatic, ""
	disabled := apiWorkspaceAnalysisHotModels(t, cfg, 0, false, 0)
	var absent *modelsettingsruntime.RuntimeHost[*modelsettingsruntime.Models]
	if starter, err := newAPIWorkspaceAnalysisRunStarter(apiConstructorPool(t), cfg, disabled, absent); err != nil || starter == nil {
		t.Fatalf("disabled static startup dropped the analysis replay route: %v", err)
	}
	for _, version := range []int64{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repository := &apiWorkspaceAnalysisRunRepositoryFake{}
			command := apiWorkspaceAnalysisHotCommand(0)
			seed := apiWorkspaceAnalysisSeedRun(t, repository, command, version)
			repository.readyCalls, repository.inserts = 0, 0
			starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, disabled, absent)
			if err != nil {
				t.Fatal(err)
			}
			scope := &apiWorkspaceAnalysisTestScope{}
			repository.before = func(operation string, got foundation.TransactionScope) {
				if operation != "find" || got != scope {
					t.Fatal("disabled static replay escaped the caller scope or touched new-start persistence")
				}
			}
			command.Replayed = true
			got, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), scope, command)
			if err != nil || !reflect.DeepEqual(got, seed) || repository.finds != 1 || repository.readyCalls != 0 || repository.inserts != 0 {
				t.Fatalf("disabled static v%d replay did not preserve the stored run: %v", version, err)
			}
			_, err = starter.StartWorkspaceAnalysisRunScoped(t.Context(), scope, apiWorkspaceAnalysisHotCommand(10))
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode ||
				repository.finds != 1 || repository.readyCalls != 0 || repository.inserts != 0 || !reflect.DeepEqual(repository.runs[command.QuestionID], seed) {
				t.Fatalf("disabled static model allowed a new start or changed history: %v", err)
			}
		})
	}
}

func TestAPIWorkspaceAnalysisStarterRejectsUnavailableCurrentRuntime(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	models := apiWorkspaceAnalysisHotModels(t, cfg, 1, true, time.Second)
	tooSlow := apiWorkspaceAnalysisHotModels(t, cfg, 2, true, 3*time.Second)
	for _, test := range []struct {
		name   string
		change func(*apiWorkspaceAnalysisCurrentRuntimeFake)
	}{
		{"missing lease", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.noLease = true }},
		{"typed-nil lease", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.typedNilLease = true }},
		{"lease returned with error", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.err = errors.New("acquisition failed") }},
		{"missing models", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.models = nil }},
		{"foreign revision", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.revisionOffset = 1 }},
		{"worker role", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) {
			runtime.role = modelsettingsruntime.RuntimeRoleWorker
		}},
		{"static binding", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) {
			runtime.mode = modelsettingsruntime.RuntimeModeStatic
		}},
		{"invalid instance", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.instanceID = "invalid" }},
		{"model exceeds worker timeout", func(runtime *apiWorkspaceAnalysisCurrentRuntimeFake) { runtime.models = tooSlow }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &apiWorkspaceAnalysisCurrentRuntimeFake{models: models}
			test.change(runtime)
			repository := &apiWorkspaceAnalysisRunRepositoryFake{}
			starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, models, runtime)
			if err != nil {
				t.Fatal(err)
			}
			_, err = starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(0))
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode || repository.readyCalls != 0 || repository.inserts != 0 || runtime.active != 0 {
				t.Fatalf("unavailable generation escaped the closed gate: %v", err)
			}
			if !runtime.noLease && !runtime.typedNilLease && runtime.releases != 1 {
				t.Fatal("failed acquisition leaked its returned lease")
			}
		})
	}
}

func TestAPIWorkspaceAnalysisStarterWaitsForGateAndPropagatesReadinessFailure(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	models := apiWorkspaceAnalysisHotModels(t, cfg, 1, true, time.Second)
	runtime := &apiWorkspaceAnalysisCurrentRuntimeFake{models: models, gated: true}
	repository := &apiWorkspaceAnalysisRunRepositoryFake{}
	starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, models, runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := starter.StartWorkspaceAnalysisRunScoped(ctx, &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(0)); !errors.Is(err, context.DeadlineExceeded) || repository.readyCalls != 0 || repository.inserts != 0 {
		t.Fatalf("gated acquisition started a run: %v", err)
	}
	runtime.gated = false
	repository.readyErr = errors.New("no matching worker capability")
	if _, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(0)); !errors.Is(err, repository.readyErr) || repository.inserts != 0 || runtime.active != 0 {
		t.Fatalf("readiness failure was lost or leaked its lease: %v", err)
	}
	cancelled, cancelAcquire := context.WithCancel(t.Context())
	defer cancelAcquire()
	runtime.onAcquire = cancelAcquire
	if _, err := starter.StartWorkspaceAnalysisRunScoped(cancelled, &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(0)); !errors.Is(err, context.Canceled) || repository.readyCalls != 1 || repository.inserts != 0 || runtime.active != 0 {
		t.Fatalf("cancelled acquisition reached persistence or leaked its lease: %v", err)
	}
}

func TestAPIWorkspaceAnalysisStarterPinsRealHostUntilScopedStartCompletes(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	models := apiWorkspaceAnalysisHotModels(t, cfg, 1, true, time.Second)
	factory := &apiWorkspaceAnalysisRuntimeFactoryFake{}
	host, err := modelsettingsruntime.NewRuntimeHost(modelsettingsruntime.RuntimeHostOptions[*modelsettingsruntime.Models]{
		Initial: modelsettingsruntime.InitialRuntime[*modelsettingsruntime.Models]{
			Binding: modelsettingsruntime.RuntimeBinding{Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleAPI,
				Revision: 1, InstanceID: apiWorkspaceAnalysisHotCommand(100).WorkspaceID},
			Value: models,
		}, Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	repository := &apiWorkspaceAnalysisRunRepositoryFake{}
	starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, models, host)
	if err != nil {
		t.Fatal(err)
	}
	repository.before = func(string, foundation.TransactionScope) {
		host.Close()
		if factory.closes.Load() != 0 {
			t.Fatal("Host retired the current model before scoped start completed")
		}
	}
	if _, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(0)); err != nil || factory.closes.Load() != 1 || repository.inserts != 1 {
		t.Fatalf("scoped start did not release the pinned closed generation: %v", err)
	}
	if _, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(10)); err == nil || repository.inserts != 1 {
		t.Fatal("closed Host accepted a new analysis start")
	}
}

func TestNewAPIWorkspaceAnalysisRunStarterPreservesStaticAndManagedModes(t *testing.T) {
	cfg := apiWorkspaceAnalysisHotConfig(t)
	disabled := apiWorkspaceAnalysisHotModels(t, cfg, 0, false, 0)
	runtime := &apiWorkspaceAnalysisCurrentRuntimeFake{models: disabled}
	if starter, err := newAPIWorkspaceAnalysisRunStarter(apiConstructorPool(t), cfg, disabled, runtime); err != nil || starter == nil || runtime.calls != 0 {
		t.Fatalf("production managed constructor depended on the initial model: %v", err)
	}
	if _, err := newAPIWorkspaceAnalysisRunStarterWithRepository(&apiWorkspaceAnalysisRunRepositoryFake{}, cfg, disabled); err == nil {
		t.Fatal("managed startup accepted a missing live Host")
	}
	if _, err := newAPIWorkspaceAnalysisRunStarterWithRepository(&apiWorkspaceAnalysisRunRepositoryFake{}, cfg, disabled, runtime, runtime); err == nil {
		t.Fatal("managed startup accepted ambiguous runtime sources")
	}
	cfg.WorkspaceAnalysisAPIEnabled = false
	if _, err := newAPIWorkspaceAnalysisRunStarterWithRepository(&apiWorkspaceAnalysisRunRepositoryFake{}, cfg, disabled, runtime); err == nil {
		t.Fatal("live Host bypassed the static feature gate")
	}
	cfg.WorkspaceAnalysisAPIEnabled = true
	cfg.ModelSettingsMode, cfg.ModelSettingsKeyFile = config.ModelSettingsModeStatic, ""
	models := apiWorkspaceAnalysisHotModels(t, cfg, 0, true, time.Second)
	var absent *modelsettingsruntime.RuntimeHost[*modelsettingsruntime.Models]
	for _, sources := range [][]modelsettingsruntime.RuntimeAcquirer[*modelsettingsruntime.Models]{nil, {absent}} {
		repository := &apiWorkspaceAnalysisRunRepositoryFake{}
		starter, err := newAPIWorkspaceAnalysisRunStarterWithRepository(repository, cfg, models, sources...)
		if err != nil {
			t.Fatal(err)
		}
		run, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, apiWorkspaceAnalysisHotCommand(0))
		if err != nil || run.Timeouts.PlanModelTimeout != time.Second || repository.inserts != 1 {
			t.Fatalf("static constructor behavior changed: %v", err)
		}
	}
	if _, err := newAPIWorkspaceAnalysisRunStarterWithRepository(&apiWorkspaceAnalysisRunRepositoryFake{}, cfg, models, runtime); err == nil {
		t.Fatal("static startup accepted a managed runtime")
	}
}

func apiWorkspaceAnalysisHotConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.ModelSettingsMode = config.ModelSettingsModeManaged
	cfg.ModelSettingsKeyFile = "/run/secrets/api-capability-constructor.key"
	cfg.WorkspaceAnalysisAPIEnabled = true
	tools, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2()
	if err != nil {
		t.Fatal(err)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV2Deadlines(agentdomain.WorkspaceAnalysisV2Timeouts{
		PlanModelTimeout: 2 * time.Second, SynthesisModelTimeout: 2 * time.Second, ReviewModelTimeout: 2 * time.Second,
		GitToolTimeout: tools.ReadGitStatusTimeout, SearchToolTimeout: tools.SearchKnowledgeTimeout,
		SourceReadToolTimeout: tools.ReadSourceTimeout, ValidateCitationToolTimeout: tools.ValidateCitationTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkerJobTimeout = deadlines.MinimumRiverJobTimeout()
	return cfg
}

func apiWorkspaceAnalysisHotModels(t *testing.T, cfg config.Config, revision int64, enabled bool, timeout time.Duration) *modelsettingsruntime.Models {
	t.Helper()
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	if enabled {
		settings.Chat.Provider = modelsettingsdomain.ChatProviderOpenAICompatible
		settings.Chat.BaseURL = modelsettingsdomain.ManagedOllamaBaseURL
		settings.Chat.Model = "api-current-generation"
		settings.Chat.ModelVersion = fmt.Sprintf("revision-%d", revision)
		settings.Chat.Timeout = timeout
	}
	models, err := modelsettingsruntime.Build(cfg, modelsettingsdomain.ResolvedSettings{Revision: revision, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.Close() })
	return models
}

func apiWorkspaceAnalysisHotCommand(offset int) agentapplication.WorkspaceAnalysisRunStartCommand {
	id := func(value int) foundation.ID {
		return foundation.ID(fmt.Sprintf("9b000000-0000-4000-8000-%012d", offset+value))
	}
	return agentapplication.WorkspaceAnalysisRunStartCommand{
		WorkspaceID: id(1), ConversationID: id(2), QuestionID: id(3), AnswerID: id(4), WorkflowRunID: id(5),
		CreatedAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
	}
}

func apiWorkspaceAnalysisSeedRun(t *testing.T, repository *apiWorkspaceAnalysisRunRepositoryFake, command agentapplication.WorkspaceAnalysisRunStartCommand, version int64) agentdomain.WorkspaceAnalysisRun {
	t.Helper()
	config := agentapplication.WorkspaceAnalysisRunStartConfig{
		DefinitionHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ToolCatalogHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ConfigRevision:  71, SynthesisProfileMaxOutputTokens: 4096,
		Timeouts: agentapplication.WorkspaceAnalysisV1Timeouts{
			PlanModelTimeout: time.Second, SynthesisModelTimeout: time.Second, ReviewModelTimeout: time.Second,
			GitToolTimeout: time.Second, SearchToolTimeout: time.Second, SourceReadToolTimeout: time.Second, ValidateCitationToolTimeout: time.Second,
		}, RuntimeLimits: agentapplication.WorkspaceAnalysisRuntimeLimits{RiverJobTimeout: time.Hour, LeaseDuration: time.Minute, HeartbeatInterval: time.Second},
	}
	var starter agentapplication.ScopedWorkspaceAnalysisRunStarter
	var err error
	if version == 1 {
		starter, err = agentapplication.NewScopedWorkspaceAnalysisRunService(repository, foundation.NewUUIDGenerator(nil), config)
	} else {
		starter, err = agentapplication.NewScopedWorkspaceAnalysisRunServiceV2(repository, foundation.NewUUIDGenerator(nil), config)
	}
	if err != nil {
		t.Fatal(err)
	}
	run, err := starter.StartWorkspaceAnalysisRunScoped(t.Context(), &apiWorkspaceAnalysisTestScope{}, command)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type apiWorkspaceAnalysisTestScope struct{}

func (*apiWorkspaceAnalysisTestScope) TransactionScope() {}

type apiWorkspaceAnalysisRunRepositoryFake struct {
	runs                       map[foundation.ID]agentdomain.WorkspaceAnalysisRun
	readyCalls, inserts, finds int
	contract                   agentapplication.WorkspaceAnalysisCapabilityContract
	readyErr                   error
	before                     func(string, foundation.TransactionScope)
}

func (repository *apiWorkspaceAnalysisRunRepositoryFake) RequireWorkspaceAnalysisWorkerReadyScoped(_ context.Context, scope foundation.TransactionScope, contract agentapplication.WorkspaceAnalysisCapabilityContract) error {
	if repository.before != nil {
		repository.before("ready", scope)
	}
	repository.readyCalls++
	repository.contract = contract
	return repository.readyErr
}

func (repository *apiWorkspaceAnalysisRunRepositoryFake) InsertWorkspaceAnalysisRunScoped(_ context.Context, scope foundation.TransactionScope, run agentdomain.WorkspaceAnalysisRun) (agentdomain.WorkspaceAnalysisRun, error) {
	if repository.before != nil {
		repository.before("insert", scope)
	}
	if repository.runs == nil {
		repository.runs = make(map[foundation.ID]agentdomain.WorkspaceAnalysisRun)
	}
	repository.runs[run.QuestionID] = run
	repository.inserts++
	return run, nil
}

func (repository *apiWorkspaceAnalysisRunRepositoryFake) FindWorkspaceAnalysisRunScoped(_ context.Context, scope foundation.TransactionScope, workspaceID, questionID foundation.ID) (agentdomain.WorkspaceAnalysisRun, bool, error) {
	if repository.before != nil {
		repository.before("find", scope)
	}
	repository.finds++
	run, found := repository.runs[questionID]
	return run, found && run.WorkspaceID == workspaceID, nil
}

type apiWorkspaceAnalysisCurrentRuntimeFake struct {
	models                        *modelsettingsruntime.Models
	err                           error
	mode                          modelsettingsruntime.RuntimeMode
	role                          modelsettingsruntime.RuntimeRole
	instanceID                    foundation.ID
	revisionOffset                int64
	noLease, typedNilLease, gated bool
	calls, active, releases       int
	onAcquire                     func()
}

func (runtime *apiWorkspaceAnalysisCurrentRuntimeFake) Acquire(ctx context.Context, target modelsettingsruntime.RuntimeTarget) (modelsettingsruntime.RuntimeLease[*modelsettingsruntime.Models], error) {
	runtime.calls++
	if target.String() != modelsettingsruntime.CurrentRuntime().String() {
		return nil, errors.New("new start acquired a historical model")
	}
	if runtime.gated {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if runtime.noLease {
		return nil, runtime.err
	}
	if runtime.typedNilLease {
		return (*apiWorkspaceAnalysisModelsLeaseFake)(nil), runtime.err
	}
	binding := modelsettingsruntime.RuntimeBinding{Mode: modelsettingsruntime.RuntimeModeManaged, Role: modelsettingsruntime.RuntimeRoleAPI,
		InstanceID: apiWorkspaceAnalysisHotCommand(100).WorkspaceID, Revision: runtime.revisionOffset}
	if runtime.models != nil {
		binding.Revision += runtime.models.Revision()
	}
	if runtime.mode != "" {
		binding.Mode = runtime.mode
	}
	if runtime.role != "" {
		binding.Role = runtime.role
	}
	if runtime.instanceID != "" {
		binding.InstanceID = runtime.instanceID
	}
	runtime.active++
	if runtime.onAcquire != nil {
		runtime.onAcquire()
	}
	return &apiWorkspaceAnalysisModelsLeaseFake{runtime: runtime, binding: binding, models: runtime.models}, runtime.err
}

type apiWorkspaceAnalysisModelsLeaseFake struct {
	runtime *apiWorkspaceAnalysisCurrentRuntimeFake
	binding modelsettingsruntime.RuntimeBinding
	models  *modelsettingsruntime.Models
}

func (lease *apiWorkspaceAnalysisModelsLeaseFake) Binding() modelsettingsruntime.RuntimeBinding {
	return lease.binding
}
func (lease *apiWorkspaceAnalysisModelsLeaseFake) Value() *modelsettingsruntime.Models {
	return lease.models
}
func (lease *apiWorkspaceAnalysisModelsLeaseFake) Release() {
	lease.runtime.active--
	lease.runtime.releases++
}

type apiWorkspaceAnalysisRuntimeFactoryFake struct{ closes atomic.Int32 }

func (*apiWorkspaceAnalysisRuntimeFactoryFake) Build(context.Context, int64) (*modelsettingsruntime.Models, error) {
	return nil, errors.New("new start must acquire an already current generation")
}
func (*apiWorkspaceAnalysisRuntimeFactoryFake) Probe(context.Context, *modelsettingsruntime.Models) error {
	return nil
}
func (factory *apiWorkspaceAnalysisRuntimeFactoryFake) Close(models *modelsettingsruntime.Models) {
	factory.closes.Add(1)
	_ = models.Close()
}
