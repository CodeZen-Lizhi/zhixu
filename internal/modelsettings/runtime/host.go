package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// ErrorCodeRuntimeSwitching 表示进程正在切换默认 generation，新的 acquisition 暂停。
	ErrorCodeRuntimeSwitching = "MODEL_RUNTIME_SWITCHING"
	// ErrorCodeRuntimeRevisionUnavailable 表示指定 revision 当前无法构造或取得。
	ErrorCodeRuntimeRevisionUnavailable = "MODEL_RUNTIME_REVISION_UNAVAILABLE"
	// ErrorCodeRuntimeBindingMismatch 表示持久 runtime binding 与当前进程不一致。
	ErrorCodeRuntimeBindingMismatch = "MODEL_RUNTIME_BINDING_MISMATCH"
	// ErrorCodeRuntimeEmbeddingContractMismatch 表示 generation 不兼容持久 Embedding Version。
	ErrorCodeRuntimeEmbeddingContractMismatch = "MODEL_RUNTIME_EMBEDDING_CONTRACT_MISMATCH"
	// ErrorCodeRuntimeNotReady 表示进程尚未安装可服务的 generation。
	ErrorCodeRuntimeNotReady = modelsettingsdomain.ErrorCodeRuntimeNotReady
)

// RuntimeMode 区分 static 启动配置与 managed revision。
type RuntimeMode string

const (
	RuntimeModeStatic  RuntimeMode = "static"
	RuntimeModeManaged RuntimeMode = "managed"
)

// RuntimeRole 复用模型设置领域拥有的进程角色。
type RuntimeRole = modelsettingsdomain.RuntimeRole

const (
	RuntimeRoleAPI    = modelsettingsdomain.RuntimeRoleAPI
	RuntimeRoleWorker = modelsettingsdomain.RuntimeRoleWorker
)

// RuntimeBinding 是一个不可变 generation 的非敏感进程身份。
type RuntimeBinding struct {
	Mode       RuntimeMode
	Role       RuntimeRole
	Revision   int64
	InstanceID foundation.ID
}

// String 返回不包含内部 instance id 的安全摘要。
func (binding RuntimeBinding) String() string {
	return fmt.Sprintf("RuntimeBinding{Mode:%q Role:%q Revision:%d}", binding.Mode, binding.Role, binding.Revision)
}

// GoString 避免调试格式展开内部 instance id。
func (binding RuntimeBinding) GoString() string { return binding.String() }

type runtimeTargetKind uint8

const (
	runtimeTargetCurrent runtimeTargetKind = iota + 1
	runtimeTargetAttempt
	runtimeTargetRevision
	runtimeTargetEmbedding
)

// RuntimeTarget 是 Current、Attempt binding、exact revision 或 Embedding contract 的封闭选择值。
type RuntimeTarget struct {
	kind      runtimeTargetKind
	binding   RuntimeBinding
	revision  int64
	embedding retrievaldomain.EmbeddingVersion
}

// CurrentRuntime 选择本进程当前默认 generation。
func CurrentRuntime() RuntimeTarget {
	return RuntimeTarget{kind: runtimeTargetCurrent}
}

// AttemptRuntime 选择 Workflow Attempt 已冻结的完整 runtime binding。
func AttemptRuntime(binding RuntimeBinding) RuntimeTarget {
	return RuntimeTarget{kind: runtimeTargetAttempt, binding: binding, revision: binding.Revision}
}

// RevisionRuntime 选择持久 Index/Embedding provenance 指定的 revision。
func RevisionRuntime(revision int64) RuntimeTarget {
	return RuntimeTarget{kind: runtimeTargetRevision, revision: revision}
}

// EmbeddingRuntime 选择与持久 Embedding Version 完整兼容的 generation。
// revision 只作为历史重建 hint；不同 revision 的相同 Contract 可以复用。
func EmbeddingRuntime(version retrievaldomain.EmbeddingVersion) RuntimeTarget {
	copy := version
	if version.ModelSettingsRevision != nil {
		revision := *version.ModelSettingsRevision
		copy.ModelSettingsRevision = &revision
	}
	return RuntimeTarget{kind: runtimeTargetEmbedding, embedding: copy}
}

// String 返回不展开 process instance、Embedding ID 或 generation payload 的 target 摘要。
func (target RuntimeTarget) String() string {
	switch target.kind {
	case runtimeTargetCurrent:
		return "RuntimeTarget{Kind:current}"
	case runtimeTargetAttempt:
		return fmt.Sprintf("RuntimeTarget{Kind:attempt Binding:%s}", target.binding)
	case runtimeTargetRevision:
		return fmt.Sprintf("RuntimeTarget{Kind:revision Revision:%d}", target.revision)
	case runtimeTargetEmbedding:
		return fmt.Sprintf(
			"RuntimeTarget{Kind:embedding Provider:%q Model:%q Dimensions:%d}",
			target.embedding.Provider,
			target.embedding.Model,
			target.embedding.Dimensions,
		)
	default:
		return "RuntimeTarget{Kind:invalid}"
	}
}

// GoString 避免调试格式展开 target 的持久身份。
func (target RuntimeTarget) GoString() string { return target.String() }

// GenerationFactory 构造、预检并释放一个角色私有的 immutable generation payload。
// Build 返回错误时必须自行清理尚未完整返回的部分资源。
type GenerationFactory[T any] interface {
	Build(context.Context, int64) (T, error)
	Probe(context.Context, T) error
	Close(T)
}

// GenerationHold pins a local-model demand while one immutable generation is
// still available to API/Worker consumers. Release is idempotent.
type GenerationHold interface {
	Renew(context.Context) error
	Release(context.Context) error
}

// GenerationLifecycle is optional for static/online-only deployments. Managed
// model hosts use it to publish generation holds without exposing database
// details to RuntimeHost callers.
type GenerationLifecycle interface {
	Acquire(context.Context, RuntimeBinding, localmodelruntime.Requirement) (GenerationHold, error)
}

// InitialRuntime 是构造成功后转交给 Host 的初始 serving generation。
type InitialRuntime[T any] struct {
	Binding     RuntimeBinding
	Value       T
	Unavailable bool
}

// RuntimeHostOptions 提供初始 serving generation 与后续 revision factory。
type RuntimeHostOptions[T any] struct {
	Initial InitialRuntime[T]
	Factory GenerationFactory[T]
	// EmbeddingCompatible 必须是只读取 generation 的快速纯函数；Host 会在 acquisition 锁内调用它。
	EmbeddingCompatible func(T, retrievaldomain.EmbeddingVersion) error
	LocalDemand         func(T) (localmodelruntime.Requirement, error)
	Lifecycle           GenerationLifecycle
	BuildTimeout        time.Duration
	HistoricalLimit     int
}

// RuntimeAcquirer 是业务消费者唯一需要依赖的 runtime surface。
type RuntimeAcquirer[T any] interface {
	Acquire(context.Context, RuntimeTarget) (RuntimeLease[T], error)
}

// RuntimeAdmitter fences work before it persists a claim, then resolves the
// durable target through the returned admission token.
type RuntimeAdmitter[T any] interface {
	Admit(context.Context) (RuntimeAdmission[T], error)
}

// RuntimeHost 拥有 candidate、active、retiring 与 historical generations。
// Generation payload 构造后不会原地修改；生命周期状态仅由 Host 持有。
type RuntimeHost[T any] struct {
	mu sync.Mutex

	identity            RuntimeBinding
	factory             GenerationFactory[T]
	embeddingCompatible func(T, retrievaldomain.EmbeddingVersion) error
	localDemand         func(T) (localmodelruntime.Requirement, error)
	lifecycle           GenerationLifecycle
	buildTimeout        time.Duration
	historicalLimit     int
	lifetimeContext     context.Context
	cancelLifetime      context.CancelFunc

	active     *runtimeGeneration[T]
	candidate  *candidateGeneration[T]
	retiring   map[uint64]*runtimeGeneration[T]
	historical map[int64]*runtimeGeneration[T]

	builds       map[int64]*generationBuild
	preparations map[int64]*generationPreparation

	gateClosed   bool
	gateRevision int64
	gateOpened   chan struct{}
	closed       bool
	nextID       uint64
}

// String 返回不展开 factory、generation payload 或 process instance 的 Host 摘要。
func (host *RuntimeHost[T]) String() string {
	if host == nil {
		return "RuntimeHost{unavailable}"
	}
	return fmt.Sprintf("RuntimeHost{Mode:%q Role:%q}", host.identity.Mode, host.identity.Role)
}

// GoString 避免调试格式展开 Host 内部资源。
func (host *RuntimeHost[T]) GoString() string { return host.String() }

type runtimeGeneration[T any] struct {
	id      uint64
	binding RuntimeBinding
	value   T
	ready   bool
	hold    GenerationHold

	references int
	closeOnce  sync.Once
}

func (generation *runtimeGeneration[T]) close(factory GenerationFactory[T]) {
	if generation == nil || factory == nil {
		return
	}
	generation.closeOnce.Do(func() {
		if generation.hold != nil {
			ctx, cancel := context.WithTimeout(context.Background(), generationHoldReleaseTimeout)
			_ = generation.hold.Release(ctx)
			cancel()
		}
		factory.Close(generation.value)
	})
}

type candidateGeneration[T any] struct {
	generation               *runtimeGeneration[T]
	discardHistoricalOnAbort bool
}

type generationBuild struct {
	done chan struct{}
	err  error
}

type generationPreparation struct {
	done    chan struct{}
	err     error
	aborted bool
}

const (
	defaultRuntimeBuildTimeout    = 30 * time.Second
	defaultRuntimeHistoricalLimit = 8
	generationHoldReleaseTimeout  = 5 * time.Second
)

// NewRuntimeHost 创建以一个已构造 generation 开始服务的进程内 Host。
// 成功后 Host 接管 Initial.Value；返回错误时其所有权仍属于调用方。
func NewRuntimeHost[T any](options RuntimeHostOptions[T]) (*RuntimeHost[T], error) {
	if options.Factory == nil {
		return nil, runtimeBindingError(errors.New("runtime generation factory is unavailable"))
	}
	if err := validateInitialRuntimeBinding(options.Initial.Binding); err != nil {
		return nil, err
	}
	buildTimeout := options.BuildTimeout
	if buildTimeout == 0 {
		buildTimeout = defaultRuntimeBuildTimeout
	}
	if buildTimeout < 0 {
		return nil, runtimeBindingError(errors.New("runtime build timeout is invalid"))
	}
	historicalLimit := options.HistoricalLimit
	if historicalLimit == 0 {
		historicalLimit = defaultRuntimeHistoricalLimit
	}
	if historicalLimit < 0 {
		return nil, runtimeBindingError(errors.New("runtime historical limit is invalid"))
	}
	lifetimeContext, cancelLifetime := context.WithCancel(context.Background())
	host := &RuntimeHost[T]{
		identity:            options.Initial.Binding,
		factory:             options.Factory,
		embeddingCompatible: options.EmbeddingCompatible,
		localDemand:         options.LocalDemand,
		lifecycle:           options.Lifecycle,
		buildTimeout:        buildTimeout,
		historicalLimit:     historicalLimit,
		lifetimeContext:     lifetimeContext,
		cancelLifetime:      cancelLifetime,
		retiring:            make(map[uint64]*runtimeGeneration[T]),
		historical:          make(map[int64]*runtimeGeneration[T]),
		builds:              make(map[int64]*generationBuild),
		preparations:        make(map[int64]*generationPreparation),
		nextID:              1,
	}
	host.active = &runtimeGeneration[T]{
		id: host.nextID, binding: options.Initial.Binding, value: options.Initial.Value,
		ready: !options.Initial.Unavailable,
	}
	if err := host.attachHold(context.Background(), host.active); err != nil {
		cancelLifetime()
		return nil, err
	}
	return host, nil
}

// Run owns the host lifetime until cancellation. Close remains safe to call
// independently and wakes every concurrent Run call.
func (host *RuntimeHost[T]) Run(ctx context.Context) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	if ctx == nil {
		return runtimeBindingError(errors.New("runtime host context is nil"))
	}
	select {
	case <-ctx.Done():
		host.Close()
		return nil
	case <-host.lifetimeContext.Done():
		return nil
	}
}

// Close stops acquisition and releases every generation after its final lease.
func (host *RuntimeHost[T]) Close() {
	host.close()
}

// RuntimeLease 把 binding 与角色 payload 固定在一次完整模型操作内。
type RuntimeLease[T any] interface {
	Binding() RuntimeBinding
	Value() T
	Release()
}

// RuntimeAdmission fences a not-yet-persisted operation before it claims work.
// Acquire may resolve the operation's durable target after the global gate has
// closed; Release is idempotent and drops the pin on the entry generation.
type RuntimeAdmission[T any] interface {
	Acquire(context.Context, RuntimeTarget) (RuntimeLease[T], error)
	Release()
}

type runtimeAdmission[T any] struct {
	mu         sync.Mutex
	host       *RuntimeHost[T]
	generation *runtimeGeneration[T]
}

type runtimeLease[T any] struct {
	host       *RuntimeHost[T]
	generation atomic.Pointer[runtimeGeneration[T]]
}

func newRuntimeLease[T any](host *RuntimeHost[T], generation *runtimeGeneration[T]) RuntimeLease[T] {
	lease := &runtimeLease[T]{host: host}
	lease.generation.Store(generation)
	return lease
}

// Admit enters the default admission gate and pins the serving generation.
// Activation never waits for admitted operations, while a closed gate prevents
// new callers from reaching their persistent Claim.
func (host *RuntimeHost[T]) Admit(ctx context.Context) (RuntimeAdmission[T], error) {
	if host == nil {
		return nil, runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	if ctx == nil {
		return nil, runtimeBindingError(errors.New("runtime admission context is nil"))
	}
	for {
		host.mu.Lock()
		if host.closed {
			host.mu.Unlock()
			return nil, runtimeNotReadyError(errors.New("runtime host is closed"))
		}
		if host.gateClosed {
			opened := host.gateOpened
			host.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, runtimeSwitchingError(ctx.Err())
			case <-opened:
				continue
			}
		}
		generation := host.active
		if generation == nil || !generation.ready {
			host.mu.Unlock()
			return nil, runtimeNotReadyError(errors.New("runtime active generation is unavailable"))
		}
		generation.references++
		host.mu.Unlock()
		return &runtimeAdmission[T]{host: host, generation: generation}, nil
	}
}

func (admission *runtimeAdmission[T]) Acquire(ctx context.Context, target RuntimeTarget) (RuntimeLease[T], error) {
	if admission == nil || ctx == nil {
		return nil, runtimeNotReadyError(errors.New("runtime admission is unavailable"))
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if admission.host == nil || admission.generation == nil {
		return nil, runtimeNotReadyError(errors.New("runtime admission is released"))
	}
	return admission.host.acquireAdmitted(ctx, target, admission.generation)
}

func (admission *runtimeAdmission[T]) Release() {
	if admission == nil {
		return
	}
	admission.mu.Lock()
	host := admission.host
	generation := admission.generation
	admission.host = nil
	admission.generation = nil
	admission.mu.Unlock()
	if host != nil && generation != nil {
		host.release(generation)
	}
}

// Binding 返回本 lease 冻结的 runtime binding；Release 后返回零值。
func (lease *runtimeLease[T]) Binding() RuntimeBinding {
	if lease == nil {
		return RuntimeBinding{}
	}
	generation := lease.generation.Load()
	if generation == nil {
		return RuntimeBinding{}
	}
	return generation.binding
}

// Value 返回本 lease 冻结的角色 payload；Release 后返回 T 的零值。
func (lease *runtimeLease[T]) Value() T {
	var zero T
	if lease == nil {
		return zero
	}
	generation := lease.generation.Load()
	if generation == nil {
		return zero
	}
	return generation.value
}

func (lease *runtimeLease[T]) String() string {
	if lease == nil {
		return "RuntimeLease{released}"
	}
	generation := lease.generation.Load()
	if generation == nil {
		return "RuntimeLease{released}"
	}
	return fmt.Sprintf("RuntimeLease{Binding:%s}", generation.binding)
}

func (lease *runtimeLease[T]) GoString() string { return lease.String() }

// Release 幂等释放 operation reference；最后一个 holder 释放后允许退役关闭。
func (lease *runtimeLease[T]) Release() {
	if lease == nil {
		return
	}
	generation := lease.generation.Swap(nil)
	if generation == nil || lease.host == nil {
		return
	}
	lease.host.release(generation)
}

// Acquire 为一次完整模型操作固定一个 generation。Gate 关闭时，默认与
// Embedding/Revision acquisition 等待重开；已持久化 Attempt binding 可按
// exact generation 继续。调用方取消会保留 context cause。
func (host *RuntimeHost[T]) Acquire(ctx context.Context, target RuntimeTarget) (RuntimeLease[T], error) {
	if host == nil {
		return nil, runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	if ctx == nil {
		return nil, runtimeBindingError(errors.New("runtime acquisition context is nil"))
	}
	for {
		host.mu.Lock()
		if host.closed {
			host.mu.Unlock()
			return nil, runtimeNotReadyError(errors.New("runtime host is closed"))
		}
		if host.gateClosed && !host.attemptMayAcquireLocked(target) {
			opened := host.gateOpened
			host.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, runtimeSwitchingError(ctx.Err())
			case <-opened:
				continue
			}
		}

		generation, revision, err := host.selectGenerationLocked(target)
		if err != nil {
			host.mu.Unlock()
			return nil, err
		}
		if generation != nil {
			generation.references++
			host.mu.Unlock()
			return newRuntimeLease(host, generation), nil
		}
		host.mu.Unlock()

		if err := host.ensureRevision(ctx, revision); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, runtimeRevisionError(ctxErr)
			}
			return nil, runtimeRevisionError(err)
		}
	}
}

// acquireAdmitted resolves durable provenance for an operation that entered
// before the gate closed. Current remains the generation pinned at admission;
// exact revision and Embedding targets may use or rebuild retained history.
func (host *RuntimeHost[T]) acquireAdmitted(
	ctx context.Context,
	target RuntimeTarget,
	entry *runtimeGeneration[T],
) (RuntimeLease[T], error) {
	for {
		host.mu.Lock()
		if host.closed {
			host.mu.Unlock()
			return nil, runtimeNotReadyError(errors.New("runtime host is closed"))
		}
		if entry == nil || entry.references <= 0 || !entry.ready {
			host.mu.Unlock()
			return nil, runtimeNotReadyError(errors.New("runtime admission generation is unavailable"))
		}

		var generation *runtimeGeneration[T]
		var revision int64
		var err error
		if target.kind == runtimeTargetCurrent {
			generation = entry
			revision = entry.binding.Revision
		} else {
			generation, revision, err = host.selectGenerationLocked(target)
		}
		if err != nil {
			host.mu.Unlock()
			return nil, err
		}
		if generation != nil {
			generation.references++
			host.mu.Unlock()
			return newRuntimeLease(host, generation), nil
		}
		host.mu.Unlock()

		if err := host.ensureRevision(ctx, revision); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, runtimeRevisionError(ctxErr)
			}
			return nil, runtimeRevisionError(err)
		}
	}
}

// attemptMayAcquireLocked permits a persisted Attempt binding to keep its exact
// generation during the short gate. The new candidate remains fenced until it
// is the active generation, preventing pre-commit work from using it.
func (host *RuntimeHost[T]) attemptMayAcquireLocked(target RuntimeTarget) bool {
	if target.kind != runtimeTargetAttempt {
		return false
	}
	if target.binding.Revision != host.gateRevision {
		return true
	}
	return host.active != nil && host.active.binding.Revision == target.binding.Revision
}

func (host *RuntimeHost[T]) selectGenerationLocked(target RuntimeTarget) (*runtimeGeneration[T], int64, error) {
	switch target.kind {
	case runtimeTargetCurrent:
		if host.active == nil || !host.active.ready {
			return nil, 0, runtimeNotReadyError(errors.New("runtime active generation is unavailable"))
		}
		return host.active, host.active.binding.Revision, nil
	case runtimeTargetAttempt:
		if err := validateAttemptRuntimeBinding(target.binding); err != nil {
			return nil, 0, err
		}
		if target.binding.Mode != host.identity.Mode || target.binding.Role != host.identity.Role ||
			target.binding.InstanceID != host.identity.InstanceID {
			return nil, 0, runtimeBindingError(errors.New("attempt runtime binding belongs to another process"))
		}
		generation := host.findReadyRevisionLocked(target.binding.Revision)
		if generation != nil && generation.binding != target.binding {
			return nil, 0, runtimeBindingError(errors.New("attempt runtime binding does not match the generation"))
		}
		return generation, target.binding.Revision, nil
	case runtimeTargetRevision:
		if target.revision < 0 || host.identity.Mode == RuntimeModeManaged && target.revision == 0 {
			return nil, 0, runtimeBindingError(errors.New("runtime target revision is invalid"))
		}
		if host.identity.Mode == RuntimeModeStatic && target.revision != 0 {
			return nil, 0, runtimeRevisionError(errors.New("static runtime cannot reconstruct another revision"))
		}
		return host.findReadyRevisionLocked(target.revision), target.revision, nil
	case runtimeTargetEmbedding:
		if err := retrievaldomain.ValidateEmbeddingVersion(target.embedding); err != nil {
			return nil, 0, runtimeEmbeddingError(err)
		}
		if host.embeddingCompatible == nil {
			return nil, 0, runtimeEmbeddingError(errors.New("runtime embedding compatibility resolver is unavailable"))
		}
		if generation := host.findCompatibleEmbeddingLocked(target.embedding); generation != nil {
			return generation, generation.binding.Revision, nil
		}
		if target.embedding.ModelSettingsRevision == nil || *target.embedding.ModelSettingsRevision <= 0 ||
			host.identity.Mode != RuntimeModeManaged {
			return nil, 0, runtimeEmbeddingError(errors.New("embedding version has no reconstructable managed revision"))
		}
		revision := *target.embedding.ModelSettingsRevision
		if host.findReadyRevisionLocked(revision) != nil {
			return nil, 0, runtimeEmbeddingError(errors.New("reconstructed runtime does not match the embedding version"))
		}
		return nil, revision, nil
	default:
		return nil, 0, runtimeBindingError(errors.New("runtime target is invalid"))
	}
}

func (host *RuntimeHost[T]) findCompatibleEmbeddingLocked(version retrievaldomain.EmbeddingVersion) *runtimeGeneration[T] {
	seen := make(map[*runtimeGeneration[T]]struct{})
	candidates := make([]*runtimeGeneration[T], 0, len(host.historical)+len(host.retiring)+1)
	if host.active != nil && host.active.ready {
		seen[host.active] = struct{}{}
		candidates = append(candidates, host.active)
	}
	for _, generation := range host.historical {
		if generation.ready {
			if _, duplicate := seen[generation]; !duplicate {
				seen[generation] = struct{}{}
				candidates = append(candidates, generation)
			}
		}
	}
	for _, generation := range host.retiring {
		if generation.ready {
			if _, duplicate := seen[generation]; !duplicate {
				seen[generation] = struct{}{}
				candidates = append(candidates, generation)
			}
		}
	}
	for _, generation := range candidates {
		if host.embeddingCompatible(generation.value, version) == nil {
			return generation
		}
	}
	return nil
}

func (host *RuntimeHost[T]) findReadyRevisionLocked(revision int64) *runtimeGeneration[T] {
	if host.active != nil && host.active.ready && host.active.binding.Revision == revision {
		return host.active
	}
	if generation := host.historical[revision]; generation != nil && generation.ready {
		return generation
	}
	var newest *runtimeGeneration[T]
	for _, generation := range host.retiring {
		if generation.ready && generation.binding.Revision == revision && (newest == nil || generation.id > newest.id) {
			newest = generation
		}
	}
	return newest
}

func (host *RuntimeHost[T]) ensureRevision(ctx context.Context, revision int64) error {
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return runtimeNotReadyError(errors.New("runtime host is closed"))
	}
	if host.findReadyRevisionLocked(revision) != nil {
		host.mu.Unlock()
		return nil
	}
	if host.identity.Mode != RuntimeModeManaged {
		host.mu.Unlock()
		return runtimeRevisionError(errors.New("static runtime cannot build managed history"))
	}
	if revision <= 0 {
		host.mu.Unlock()
		return runtimeRevisionError(errors.New("canonical disabled revision is no longer resident"))
	}
	call := host.builds[revision]
	if call == nil {
		call = &generationBuild{done: make(chan struct{})}
		host.builds[revision] = call
		go host.buildRevision(revision, call)
	}
	host.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-call.done:
		return call.err
	}
}

func (host *RuntimeHost[T]) buildRevision(revision int64, call *generationBuild) {
	ctx, cancel := context.WithTimeout(host.lifetimeContext, host.buildTimeout)
	defer cancel()
	value, err := host.factory.Build(ctx, revision)
	var built *runtimeGeneration[T]
	if err == nil {
		built = &runtimeGeneration[T]{binding: host.bindingForRevision(revision), value: value, ready: true}
		if err = host.attachHold(ctx, built); err != nil {
			built.close(host.factory)
			built = nil
		}
	}
	if err == nil {
		if err = host.factory.Probe(ctx, value); err != nil {
			built.close(host.factory)
			built = nil
		}
	}

	var cleanup []*runtimeGeneration[T]
	host.mu.Lock()
	if err == nil {
		switch {
		case host.closed:
			err = runtimeNotReadyError(errors.New("runtime host closed during generation build"))
			cleanup = append(cleanup, built)
		case host.findReadyRevisionLocked(revision) != nil:
			cleanup = append(cleanup, built)
		default:
			host.nextID++
			built.id = host.nextID
			host.historical[revision] = built
			cleanup = append(cleanup, host.pruneHistoricalLocked(revision)...)
		}
	}
	call.err = err
	delete(host.builds, revision)
	close(call.done)
	host.mu.Unlock()
	for _, generation := range cleanup {
		generation.close(host.factory)
	}
}

func (host *RuntimeHost[T]) attachHold(ctx context.Context, generation *runtimeGeneration[T]) error {
	if host == nil || generation == nil || host.lifecycle == nil || host.localDemand == nil {
		return nil
	}
	requirement, err := host.localDemand(generation.value)
	if err != nil {
		return err
	}
	if len(requirement.Models) == 0 {
		return nil
	}
	hold, err := host.lifecycle.Acquire(ctx, generation.binding, requirement)
	if err != nil {
		return err
	}
	generation.hold = hold
	return nil
}

// RenewHolds refreshes every live local generation hold. It intentionally does
// not hold the Host mutex while making database calls.
func (host *RuntimeHost[T]) RenewHolds(ctx context.Context) error {
	if host == nil || ctx == nil {
		return runtimeBindingError(errors.New("runtime generation hold context is invalid"))
	}
	host.mu.Lock()
	holds := make([]GenerationHold, 0, 1)
	seen := make(map[*runtimeGeneration[T]]struct{})
	collect := func(generation *runtimeGeneration[T]) {
		if generation != nil && generation.hold != nil {
			if _, ok := seen[generation]; ok {
				return
			}
			seen[generation] = struct{}{}
			holds = append(holds, generation.hold)
		}
	}
	collect(host.active)
	if host.candidate != nil {
		collect(host.candidate.generation)
	}
	for _, generation := range host.historical {
		collect(generation)
	}
	for _, generation := range host.retiring {
		collect(generation)
	}
	host.mu.Unlock()
	for _, hold := range holds {
		if err := hold.Renew(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (host *RuntimeHost[T]) bindingForRevision(revision int64) RuntimeBinding {
	binding := host.identity
	binding.Revision = revision
	return binding
}

// prepare 构造并新鲜预检 exact revision；相同 revision 的并发调用合并为一次。
func (host *RuntimeHost[T]) prepare(ctx context.Context, revision int64) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	if ctx == nil || revision <= 0 {
		return runtimeBindingError(errors.New("runtime prepare input is invalid"))
	}
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return runtimeNotReadyError(errors.New("runtime host is closed"))
	}
	if host.identity.Mode != RuntimeModeManaged {
		host.mu.Unlock()
		return runtimeBindingError(errors.New("static runtime cannot prepare managed revisions"))
	}
	if host.candidate != nil {
		if host.candidate.generation.binding.Revision == revision {
			host.mu.Unlock()
			return nil
		}
		host.mu.Unlock()
		return runtimeLifecycleConflict(errors.New("another runtime candidate is already prepared"))
	}
	if host.active != nil && host.active.ready && host.active.binding.Revision == revision {
		host.mu.Unlock()
		return nil
	}
	call := host.preparations[revision]
	if call == nil {
		call = &generationPreparation{done: make(chan struct{})}
		host.preparations[revision] = call
		go host.runPreparation(revision, call)
	}
	host.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-call.done:
		return call.err
	}
}

func (host *RuntimeHost[T]) runPreparation(revision int64, call *generationPreparation) {
	ctx, cancel := context.WithTimeout(host.lifetimeContext, host.buildTimeout)
	defer cancel()
	err := host.prepareRevision(ctx, revision, call)
	if err != nil {
		err = runtimePrepareError(err)
	}
	host.mu.Lock()
	if err == nil && call.aborted {
		err = runtimeLifecycleConflict(errors.New("runtime preparation was aborted"))
	}
	call.err = err
	delete(host.preparations, revision)
	close(call.done)
	host.mu.Unlock()
}

func (host *RuntimeHost[T]) prepareRevision(ctx context.Context, revision int64, call *generationPreparation) error {
	host.mu.Lock()
	generation := host.findReadyRevisionLocked(revision)
	created := generation == nil
	if generation != nil {
		generation.references++ // Pin an existing generation across the fresh probe.
	}
	host.mu.Unlock()

	if generation == nil {
		if err := host.ensureRevision(ctx, revision); err != nil {
			return err
		}
		host.mu.Lock()
		generation = host.findReadyRevisionLocked(revision)
		if generation == nil {
			host.mu.Unlock()
			return runtimeRevisionError(errors.New("prepared runtime generation disappeared"))
		}
		generation.references++
		host.mu.Unlock()
	} else if err := host.factory.Probe(ctx, generation.value); err != nil {
		host.release(generation)
		return err
	}

	var closeGeneration *runtimeGeneration[T]
	host.mu.Lock()
	switch {
	case host.closed:
		if created && host.historical[revision] == generation {
			delete(host.historical, revision)
		}
		closeGeneration = host.releaseLocked(generation)
		host.mu.Unlock()
		if closeGeneration != nil {
			closeGeneration.close(host.factory)
		}
		return runtimeNotReadyError(errors.New("runtime host closed during prepare"))
	case call.aborted:
		if created && host.historical[revision] == generation {
			delete(host.historical, revision)
		}
		closeGeneration = host.releaseLocked(generation)
		host.mu.Unlock()
		if closeGeneration != nil {
			closeGeneration.close(host.factory)
		}
		return runtimeLifecycleConflict(errors.New("runtime preparation was aborted"))
	case host.candidate != nil && host.candidate.generation.binding.Revision != revision:
		if created && host.historical[revision] == generation {
			delete(host.historical, revision)
		}
		closeGeneration = host.releaseLocked(generation)
		host.mu.Unlock()
		if closeGeneration != nil {
			closeGeneration.close(host.factory)
		}
		return runtimeLifecycleConflict(errors.New("another runtime candidate won prepare"))
	default:
		host.candidate = &candidateGeneration[T]{
			generation:               generation,
			discardHistoricalOnAbort: created,
		}
		closeGeneration = host.releaseLocked(generation)
		host.mu.Unlock()
		if closeGeneration != nil {
			closeGeneration.close(host.factory)
		}
		return nil
	}
}

// arm 关闭新的默认 acquisition；已返回的 lease 与已持久化 Attempt binding 不受影响。
func (host *RuntimeHost[T]) arm(revision int64) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return runtimeNotReadyError(errors.New("runtime host is closed"))
	}
	if host.gateClosed {
		if host.gateRevision == revision &&
			(host.candidate != nil && host.candidate.generation.binding.Revision == revision ||
				host.active != nil && host.active.binding.Revision == revision) {
			return nil
		}
		return runtimeLifecycleConflict(errors.New("runtime acquisition gate is armed for another revision"))
	}
	if host.candidate == nil {
		// A process that starts while the durable state is already activating
		// loads target as its initial active generation. It still needs a closed
		// admission gate until both roles acknowledge and finalization reaches idle.
		if host.active == nil || host.active.binding.Revision != revision {
			return runtimeLifecycleConflict(errors.New("runtime candidate is not prepared"))
		}
		host.gateClosed = true
		host.gateRevision = revision
		host.gateOpened = make(chan struct{})
		return nil
	}
	if host.candidate.generation.binding.Revision != revision {
		return runtimeLifecycleConflict(errors.New("runtime candidate is not prepared"))
	}
	host.gateClosed = true
	host.gateRevision = revision
	host.gateOpened = make(chan struct{})
	return nil
}

// activate 在关闭的 gate 内交换默认 generation；gate 由 reopen 在持久 applied 收敛后打开。
func (host *RuntimeHost[T]) activate(revision int64) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	var closeGeneration *runtimeGeneration[T]
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return runtimeNotReadyError(errors.New("runtime host is closed"))
	}
	if host.candidate == nil && host.active != nil && host.active.binding.Revision == revision {
		host.mu.Unlock()
		return nil
	}
	if !host.gateClosed || host.gateRevision != revision || host.candidate == nil ||
		host.candidate.generation.binding.Revision != revision {
		host.mu.Unlock()
		return runtimeLifecycleConflict(errors.New("runtime activation is not armed"))
	}

	next := host.candidate.generation
	previous := host.active
	host.candidate = nil
	if host.historical[revision] == next {
		delete(host.historical, revision)
	}
	host.active = next
	if previous != nil && previous != next {
		host.retiring[previous.id] = previous
		if previous.references == 0 && !host.generationHeldLocked(previous) {
			delete(host.retiring, previous.id)
			closeGeneration = previous
		}
	}
	host.mu.Unlock()
	if closeGeneration != nil {
		closeGeneration.close(host.factory)
	}
	return nil
}

// reopen 在 durable active/applied/idle 已收敛后开放新 acquisition。
func (host *RuntimeHost[T]) reopen(revision int64) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return runtimeNotReadyError(errors.New("runtime host is closed"))
	}
	if !host.gateClosed {
		if host.active != nil && host.active.binding.Revision == revision {
			return nil
		}
		return runtimeLifecycleConflict(errors.New("runtime acquisition gate is not armed for this revision"))
	}
	if host.gateRevision != revision || host.active == nil || host.active.binding.Revision != revision {
		return runtimeLifecycleConflict(errors.New("runtime active generation has not converged"))
	}
	host.openGateLocked()
	return nil
}

// abort 丢弃 commit 前 candidate，并在必要时恢复旧 generation admission。
func (host *RuntimeHost[T]) abort(revision int64) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	var closeGeneration *runtimeGeneration[T]
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return nil
	}
	if call := host.preparations[revision]; call != nil {
		call.aborted = true
	}
	if host.candidate == nil {
		if host.gateClosed && host.gateRevision == revision && host.active != nil && host.active.binding.Revision == revision {
			host.mu.Unlock()
			return runtimeLifecycleConflict(errors.New("committed runtime activation cannot be aborted"))
		}
		if host.gateClosed && host.gateRevision == revision {
			host.openGateLocked()
		}
		host.mu.Unlock()
		return nil
	}
	if host.candidate.generation.binding.Revision != revision {
		host.mu.Unlock()
		return runtimeLifecycleConflict(errors.New("runtime abort target does not match candidate"))
	}

	candidate := host.candidate
	host.candidate = nil
	if candidate.discardHistoricalOnAbort && host.historical[revision] == candidate.generation {
		delete(host.historical, revision)
	}
	if host.gateClosed && host.gateRevision == revision {
		host.openGateLocked()
	}
	if candidate.generation.references == 0 && !host.generationHeldLocked(candidate.generation) {
		delete(host.retiring, candidate.generation.id)
		closeGeneration = candidate.generation
	} else if !host.generationHeldLocked(candidate.generation) {
		host.retiring[candidate.generation.id] = candidate.generation
	}
	host.mu.Unlock()
	if closeGeneration != nil {
		closeGeneration.close(host.factory)
	}
	return nil
}

// close 停止新的 acquisition；仍被 lease 持有的 generation 延迟到最后一次 Release 再关闭。
func (host *RuntimeHost[T]) close() {
	if host == nil {
		return
	}
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return
	}
	host.closed = true
	host.cancelLifetime()
	if host.gateClosed {
		host.openGateLocked()
	}

	owned := make(map[*runtimeGeneration[T]]struct{})
	if host.active != nil {
		owned[host.active] = struct{}{}
	}
	if host.candidate != nil {
		owned[host.candidate.generation] = struct{}{}
	}
	for _, generation := range host.historical {
		owned[generation] = struct{}{}
	}
	for _, generation := range host.retiring {
		owned[generation] = struct{}{}
	}
	host.active = nil
	host.candidate = nil
	host.historical = make(map[int64]*runtimeGeneration[T])
	host.retiring = make(map[uint64]*runtimeGeneration[T])

	ready := make([]*runtimeGeneration[T], 0, len(owned))
	for generation := range owned {
		if generation.references == 0 {
			ready = append(ready, generation)
		}
	}
	host.mu.Unlock()
	for _, generation := range ready {
		generation.close(host.factory)
	}
}

func (host *RuntimeHost[T]) release(generation *runtimeGeneration[T]) {
	if host == nil || generation == nil {
		return
	}
	host.mu.Lock()
	closeGeneration := host.releaseLocked(generation)
	cleanup := host.pruneHistoricalLocked(-1)
	host.mu.Unlock()
	if closeGeneration != nil {
		closeGeneration.close(host.factory)
	}
	for _, historical := range cleanup {
		historical.close(host.factory)
	}
}

func (host *RuntimeHost[T]) releaseLocked(generation *runtimeGeneration[T]) *runtimeGeneration[T] {
	if generation.references <= 0 {
		return nil
	}
	generation.references--
	if generation.references != 0 || host.generationHeldLocked(generation) {
		return nil
	}
	delete(host.retiring, generation.id)
	return generation
}

func (host *RuntimeHost[T]) generationHeldLocked(generation *runtimeGeneration[T]) bool {
	if generation == nil {
		return false
	}
	if host.active == generation || host.candidate != nil && host.candidate.generation == generation {
		return true
	}
	for _, historical := range host.historical {
		if historical == generation {
			return true
		}
	}
	return false
}

func (host *RuntimeHost[T]) pruneHistoricalLocked(protectedRevision int64) []*runtimeGeneration[T] {
	cleanup := make([]*runtimeGeneration[T], 0)
	for len(host.historical) > host.historicalLimit {
		var oldestRevision int64
		var oldest *runtimeGeneration[T]
		for revision, generation := range host.historical {
			if revision == protectedRevision || generation.references != 0 || host.active == generation ||
				host.candidate != nil && host.candidate.generation == generation || host.preparations[revision] != nil {
				continue
			}
			if oldest == nil || generation.id < oldest.id {
				oldestRevision = revision
				oldest = generation
			}
		}
		if oldest == nil {
			break
		}
		delete(host.historical, oldestRevision)
		cleanup = append(cleanup, oldest)
	}
	return cleanup
}

func (host *RuntimeHost[T]) openGateLocked() {
	if !host.gateClosed {
		return
	}
	close(host.gateOpened)
	host.gateClosed = false
	host.gateRevision = 0
	host.gateOpened = nil
}

func validateRuntimeBinding(binding RuntimeBinding) error {
	if !modelsettingsdomain.ValidRuntimeRole(binding.Role) || binding.Revision < 0 {
		return runtimeBindingError(errors.New("runtime binding role or revision is invalid"))
	}
	switch binding.Mode {
	case RuntimeModeStatic:
		if binding.Revision != 0 || binding.InstanceID != "" {
			return runtimeBindingError(errors.New("static runtime binding is invalid"))
		}
	case RuntimeModeManaged:
		if binding.Revision <= 0 {
			return runtimeBindingError(errors.New("managed runtime revision is invalid"))
		}
		parsed, err := foundation.ParseID(string(binding.InstanceID))
		if err != nil || parsed != binding.InstanceID {
			return runtimeBindingError(errors.New("managed runtime instance binding is invalid"))
		}
	default:
		return runtimeBindingError(errors.New("runtime binding mode is invalid"))
	}
	return nil
}

// validateAttemptRuntimeBinding permits canonical managed revision 0 only as
// an exact resident Attempt binding. Revision 0 has no persisted settings row,
// so ensureRevision still refuses to reconstruct it after retirement.
func validateAttemptRuntimeBinding(binding RuntimeBinding) error {
	if binding.Mode != RuntimeModeManaged || binding.Revision != 0 {
		return validateRuntimeBinding(binding)
	}
	if !modelsettingsdomain.ValidRuntimeRole(binding.Role) {
		return runtimeBindingError(errors.New("attempt runtime binding role is invalid"))
	}
	parsed, err := foundation.ParseID(string(binding.InstanceID))
	if err != nil || parsed != binding.InstanceID {
		return runtimeBindingError(errors.New("attempt runtime instance binding is invalid"))
	}
	return nil
}

// validateInitialRuntimeBinding permits only the managed bootstrap binding to
// use canonical disabled revision 0. Persisted Attempt and revision targets
// continue through validateRuntimeBinding and therefore remain strictly positive.
func validateInitialRuntimeBinding(binding RuntimeBinding) error {
	if binding.Mode != RuntimeModeManaged || binding.Revision != 0 {
		return validateRuntimeBinding(binding)
	}
	if !modelsettingsdomain.ValidRuntimeRole(binding.Role) {
		return runtimeBindingError(errors.New("runtime binding role is invalid"))
	}
	parsed, err := foundation.ParseID(string(binding.InstanceID))
	if err != nil || parsed != binding.InstanceID {
		return runtimeBindingError(errors.New("managed runtime instance binding is invalid"))
	}
	return nil
}

func runtimeSwitchingError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeRuntimeSwitching, true, cause)
}

func runtimeNotReadyError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeRuntimeNotReady, true, cause)
}

func runtimeRevisionError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeRuntimeRevisionUnavailable, true, cause)
}

func runtimeBindingError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeRuntimeBindingMismatch, false, cause)
}

func runtimeEmbeddingError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeRuntimeEmbeddingContractMismatch, false, cause)
}

func runtimePrepareError(cause error) error {
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeActivationPrepareFailed, true, cause)
}

func runtimeLifecycleConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeActivationConflict, true, cause)
}
