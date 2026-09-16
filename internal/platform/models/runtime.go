package models

import (
	"errors"
	"fmt"
	"sync"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// CapabilityState is the frozen availability of one model capability.
type CapabilityState string

const (
	// CapabilityDisabled means the process intentionally has no adapter for the capability.
	CapabilityDisabled CapabilityState = "disabled"
	// CapabilityConfigured means the adapter and its contract were constructed together.
	CapabilityConfigured CapabilityState = "configured"
)

// ChatCapability keeps a Chat adapter inseparable from the contract it was built with.
type ChatCapability struct {
	state    CapabilityState
	model    agentapplication.ChatModel
	contract ChatContract
}

// State returns the frozen Chat availability; the zero value is disabled.
func (capability ChatCapability) State() CapabilityState {
	if capability.state == CapabilityConfigured {
		return CapabilityConfigured
	}
	return CapabilityDisabled
}

// Model returns the shared process adapter, or nil when Chat is disabled.
func (capability ChatCapability) Model() agentapplication.ChatModel { return capability.model }

// Contract returns the non-secret Chat contract when configured.
func (capability ChatCapability) Contract() (ChatContract, bool) {
	if capability.state != CapabilityConfigured || capability.model == nil {
		return ChatContract{}, false
	}
	return capability.contract, true
}

func (capability ChatCapability) String() string {
	if capability.state != CapabilityConfigured {
		return "ChatCapability{State:disabled}"
	}
	return fmt.Sprintf("ChatCapability{State:configured Provider:%q Model:%q}", capability.contract.Provider, capability.contract.Model.ModelID)
}

// GoString applies the endpoint- and credential-safe representation to %#v.
func (capability ChatCapability) GoString() string { return capability.String() }

// EmbeddingRuntimeContract adds transport budgets to the frozen vector binding.
type EmbeddingRuntimeContract struct {
	binding          retrievaldomain.EmbeddingContract
	timeout          time.Duration
	maxResponseBytes int64
}

// Binding returns the immutable vector identity used by retrieval persistence.
func (contract EmbeddingRuntimeContract) Binding() retrievaldomain.EmbeddingContract {
	return contract.binding
}

// Timeout returns the per-request Provider deadline.
func (contract EmbeddingRuntimeContract) Timeout() time.Duration { return contract.timeout }

// MaxResponseBytes returns the maximum accepted Provider response size.
func (contract EmbeddingRuntimeContract) MaxResponseBytes() int64 { return contract.maxResponseBytes }

func (contract EmbeddingRuntimeContract) String() string {
	return fmt.Sprintf(
		"EmbeddingRuntimeContract{Provider:%q Model:%q Dimensions:%d Timeout:%s MaxResponseBytes:%d}",
		contract.binding.Provider, contract.binding.Model, contract.binding.Dimensions, contract.timeout, contract.maxResponseBytes,
	)
}

// GoString prevents EndpointIdentity from being expanded by %#v.
func (contract EmbeddingRuntimeContract) GoString() string { return contract.String() }

// EmbeddingCapability keeps one shared Embedder inseparable from its runtime contract.
type EmbeddingCapability struct {
	state    CapabilityState
	embedder retrievalapplication.Embedder
	contract EmbeddingRuntimeContract
}

// State returns the frozen Embedding availability; the zero value is disabled.
func (capability EmbeddingCapability) State() CapabilityState {
	if capability.state == CapabilityConfigured {
		return CapabilityConfigured
	}
	return CapabilityDisabled
}

// Embedder returns the shared process adapter, or nil when Embedding is disabled.
func (capability EmbeddingCapability) Embedder() retrievalapplication.Embedder {
	return capability.embedder
}

// Contract returns the non-secret Embedding runtime contract when configured.
func (capability EmbeddingCapability) Contract() (EmbeddingRuntimeContract, bool) {
	if capability.state != CapabilityConfigured || capability.embedder == nil {
		return EmbeddingRuntimeContract{}, false
	}
	return capability.contract, true
}

func (capability EmbeddingCapability) String() string {
	if capability.state != CapabilityConfigured {
		return "EmbeddingCapability{State:disabled}"
	}
	return fmt.Sprintf("EmbeddingCapability{State:configured Provider:%q Model:%q}", capability.contract.binding.Provider, capability.contract.binding.Model)
}

// GoString applies the endpoint- and credential-safe representation to %#v.
func (capability EmbeddingCapability) GoString() string { return capability.String() }

// ModelRuntime is one immutable process-wide Chat and Embedding factory result.
// It intentionally stores neither config.Config nor raw endpoint or credential fields.
type ModelRuntime struct {
	chat                  ChatCapability
	runtimeChat           RuntimeChatCapability
	embedding             EmbeddingCapability
	chatByFunction        map[modelsettingsdomain.ReasoningFunction]ChatCapability
	runtimeChatByFunction map[modelsettingsdomain.ReasoningFunction]RuntimeChatCapability
	extraChats            []ChatCapability
	extraRuntimeChats     []RuntimeChatCapability
	closeOnce             sync.Once
	closeErr              error
}

type modelResourceCloser interface {
	closeModelResource() error
}

type modelRuntimeBuilders struct {
	chat        func(config.Config) (agentapplication.ChatModel, error)
	runtimeChat func(config.Config) (*EinoRuntimeChatModel, error)
	embedding   func(config.Config) (retrievalapplication.Embedder, error)
}

// NewConfiguredModelRuntime 校验模型配置，每个有效强度共享一组 Chat Adapter，Embedding 构造一次。
func NewConfiguredModelRuntime(cfg config.Config, telemetry ...ModelTelemetry) (*ModelRuntime, error) {
	return newConfiguredModelRuntime(cfg, modelRuntimeBuilders{
		chat: func(cfg config.Config) (agentapplication.ChatModel, error) {
			return NewConfiguredChatModel(cfg, telemetry...)
		},
		runtimeChat: func(cfg config.Config) (*EinoRuntimeChatModel, error) {
			return NewEinoRuntimeChatModel(openAIChatOptionsFromConfig(cfg))
		},
		embedding: NewConfiguredEmbedder,
	})
}

func newConfiguredModelRuntime(cfg config.Config, builders modelRuntimeBuilders) (*ModelRuntime, error) {
	if err := cfg.ValidateModels(); err != nil {
		return nil, fmt.Errorf("model runtime configuration is invalid: %w", err)
	}
	if builders.chat == nil || builders.runtimeChat == nil || builders.embedding == nil {
		return nil, errors.New("model runtime builders are unavailable")
	}
	runtime := &ModelRuntime{
		chat:        ChatCapability{state: CapabilityDisabled},
		runtimeChat: RuntimeChatCapability{state: CapabilityDisabled},
		embedding:   EmbeddingCapability{state: CapabilityDisabled},
	}
	if cfg.ChatProvider != config.ChatProviderDisabled {
		chat, err := builders.chat(cfg)
		if err != nil {
			return nil, errors.Join(err, closeModelResource(chat))
		}
		contractProvider, ok := chat.(interface{ Contract() ChatContract })
		if !ok {
			return nil, errors.Join(errors.New("configured chat adapter contract is unavailable"), closeModelResource(chat))
		}
		runtime.chat = ChatCapability{state: CapabilityConfigured, model: chat, contract: contractProvider.Contract()}
		runtimeModel, err := builders.runtimeChat(cfg)
		if err != nil {
			return nil, errors.Join(err, closeModelResource(runtimeModel), runtime.Close())
		}
		if runtimeModel == nil {
			return nil, errors.Join(errors.New("configured runtime chat adapter is unavailable"), runtime.Close())
		}
		runtime.runtimeChat = RuntimeChatCapability{
			state: CapabilityConfigured, model: runtimeModel, contract: runtimeModel.http.contractCopy(),
		}
	}
	if cfg.ChatProvider != config.ChatProviderDisabled {
		runtime.chatByFunction = make(map[modelsettingsdomain.ReasoningFunction]ChatCapability)
		runtime.runtimeChatByFunction = make(map[modelsettingsdomain.ReasoningFunction]RuntimeChatCapability)
		chats := map[string]ChatCapability{cfg.ChatReasoningEffort: runtime.chat}
		runtimeChats := map[string]RuntimeChatCapability{cfg.ChatReasoningEffort: runtime.runtimeChat}
		for _, function := range modelsettingsdomain.ReasoningFunctions() {
			effort := cfg.ChatReasoningEffortByFunction.Resolve(function, cfg.ChatReasoningEffort)
			if _, exists := chats[effort]; !exists {
				selected := cfg
				selected.ChatReasoningEffort = effort
				chat, err := builders.chat(selected)
				if err != nil {
					return nil, errors.Join(err, closeModelResource(chat), runtime.Close())
				}
				contractProvider, ok := chat.(interface{ Contract() ChatContract })
				if !ok {
					return nil, errors.Join(errors.New("function chat contract is unavailable"), closeModelResource(chat), runtime.Close())
				}
				capability := ChatCapability{state: CapabilityConfigured, model: chat, contract: contractProvider.Contract()}
				runtime.extraChats = append(runtime.extraChats, capability)
				runtimeModel, err := builders.runtimeChat(selected)
				if err != nil {
					return nil, errors.Join(err, closeModelResource(runtimeModel), runtime.Close())
				}
				if runtimeModel == nil {
					return nil, errors.Join(errors.New("function runtime chat is unavailable"), runtime.Close())
				}
				runtimeCapability := RuntimeChatCapability{state: CapabilityConfigured, model: runtimeModel, contract: runtimeModel.http.contractCopy()}
				runtime.extraRuntimeChats = append(runtime.extraRuntimeChats, runtimeCapability)
				chats[effort], runtimeChats[effort] = capability, runtimeCapability
			}
			runtime.chatByFunction[function], runtime.runtimeChatByFunction[function] = chats[effort], runtimeChats[effort]
		}
	}
	if cfg.EmbeddingProvider != config.EmbeddingProviderDisabled {
		embedder, err := builders.embedding(cfg)
		if err != nil {
			return nil, errors.Join(err, closeModelResource(embedder), runtime.Close())
		}
		if embedder == nil {
			return nil, errors.Join(errors.New("configured embedding adapter is unavailable"), runtime.Close())
		}
		binding := embedder.Contract()
		if err := retrievaldomain.ValidateEmbeddingContract(binding); err != nil {
			return nil, errors.Join(errors.New("configured embedding adapter contract is invalid"), closeModelResource(embedder), runtime.Close())
		}
		runtime.embedding = EmbeddingCapability{
			state: CapabilityConfigured, embedder: embedder,
			contract: EmbeddingRuntimeContract{binding: binding, timeout: cfg.EmbeddingTimeout, maxResponseBytes: cfg.EmbeddingMaxResponseBytes},
		}
	}
	return runtime, nil
}

func openAIChatOptionsFromConfig(cfg config.Config) OpenAIChatOptions {
	return OpenAIChatOptions{
		BaseURL: cfg.ChatBaseURL, APIKey: cfg.ChatAPIKey, Model: cfg.ChatModel, ModelVersion: cfg.ChatModelVersion,
		AdapterVersion: cfg.ChatAdapterVersion, Timeout: cfg.ChatTimeout,
		MaxRequestBytes: cfg.ChatMaxRequestBytes, MaxResponseBytes: cfg.ChatMaxResponseBytes,
		APIStyle: ChatAPIStyle(cfg.ChatAPIStyle), Provider: string(cfg.ChatProvider), ReasoningEffort: cfg.ChatReasoningEffort,
	}
}

// Close 幂等关闭本 runtime 自建 Adapter 拥有的 idle HTTP transports。
// 外部注入 client 的 RoundTripper 不属于 runtime，不会在此关闭。
func (runtime *ModelRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.closeOnce.Do(func() {
		for _, chat := range runtime.extraChats {
			runtime.closeErr = errors.Join(runtime.closeErr, closeModelResource(chat.model))
		}
		for _, chat := range runtime.extraRuntimeChats {
			runtime.closeErr = errors.Join(runtime.closeErr, closeModelResource(chat.model))
		}
		runtime.closeErr = errors.Join(runtime.closeErr,
			closeModelResource(runtime.chat.model),
			closeModelResource(runtime.runtimeChat.model),
			closeModelResource(runtime.embedding.embedder),
		)
	})
	return runtime.closeErr
}

func closeModelResource(resource any) error {
	closer, ok := resource.(modelResourceCloser)
	if !ok || closer == nil {
		return nil
	}
	return closer.closeModelResource()
}

// RuntimeChat 返回仅供 Eino Agent/Stream adapter 使用的基础设施 capability。
func (runtime *ModelRuntime) RuntimeChat() RuntimeChatCapability {
	if runtime == nil {
		return RuntimeChatCapability{state: CapabilityDisabled}
	}
	return runtime.runtimeChat
}

// Chat returns the frozen Chat capability.
func (runtime *ModelRuntime) Chat() ChatCapability {
	if runtime == nil {
		return ChatCapability{state: CapabilityDisabled}
	}
	return runtime.chat
}

// Embedding returns the frozen Embedding capability.
func (runtime *ModelRuntime) Embedding() EmbeddingCapability {
	if runtime == nil {
		return EmbeddingCapability{state: CapabilityDisabled}
	}
	return runtime.embedding
}

func (runtime *ModelRuntime) String() string {
	if runtime == nil {
		return "ModelRuntime{unavailable}"
	}
	return fmt.Sprintf("ModelRuntime{%s %s %s}", runtime.chat, runtime.runtimeChat, runtime.embedding)
}

// GoString applies the endpoint- and credential-safe representation to %#v.
func (runtime *ModelRuntime) GoString() string { return runtime.String() }

// ChatFor 返回可信功能的不可变有效能力。未知
// 功能直接拒绝，不静默使用全局默认值。
func (runtime *ModelRuntime) ChatFor(function modelsettingsdomain.ReasoningFunction) ChatCapability {
	if runtime == nil || !modelsettingsdomain.ValidReasoningFunction(function) {
		return ChatCapability{state: CapabilityDisabled}
	}
	if chat, ok := runtime.chatByFunction[function]; ok {
		return chat
	}
	return runtime.chat
}

// RuntimeChatFor 为普通和流式 Chat 选择相同的有效设置。
func (runtime *ModelRuntime) RuntimeChatFor(function modelsettingsdomain.ReasoningFunction) RuntimeChatCapability {
	if runtime == nil || !modelsettingsdomain.ValidReasoningFunction(function) {
		return RuntimeChatCapability{state: CapabilityDisabled}
	}
	if chat, ok := runtime.runtimeChatByFunction[function]; ok {
		return chat
	}
	return runtime.runtimeChat
}
