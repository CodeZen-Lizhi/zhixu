package models

import (
	"errors"
	"fmt"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
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
	chat      ChatCapability
	embedding EmbeddingCapability
}

// NewConfiguredModelRuntime 校验模型配置，并使用可选项目 telemetry 各构造一次已启用 Adapter。
func NewConfiguredModelRuntime(cfg config.Config, telemetry ...ModelTelemetry) (*ModelRuntime, error) {
	if err := cfg.ValidateModels(); err != nil {
		return nil, fmt.Errorf("model runtime configuration is invalid: %w", err)
	}
	runtime := &ModelRuntime{
		chat:      ChatCapability{state: CapabilityDisabled},
		embedding: EmbeddingCapability{state: CapabilityDisabled},
	}
	if cfg.ChatProvider != config.ChatProviderDisabled {
		chat, err := NewConfiguredChatModel(cfg, telemetry...)
		if err != nil {
			return nil, err
		}
		contractProvider, ok := chat.(interface{ Contract() ChatContract })
		if !ok {
			return nil, errors.New("configured chat adapter contract is unavailable")
		}
		runtime.chat = ChatCapability{state: CapabilityConfigured, model: chat, contract: contractProvider.Contract()}
	}
	if cfg.EmbeddingProvider != config.EmbeddingProviderDisabled {
		embedder, err := NewConfiguredEmbedder(cfg)
		if err != nil {
			return nil, err
		}
		if embedder == nil {
			return nil, errors.New("configured embedding adapter is unavailable")
		}
		binding := embedder.Contract()
		if err := retrievaldomain.ValidateEmbeddingContract(binding); err != nil {
			return nil, errors.New("configured embedding adapter contract is invalid")
		}
		runtime.embedding = EmbeddingCapability{
			state: CapabilityConfigured, embedder: embedder,
			contract: EmbeddingRuntimeContract{binding: binding, timeout: cfg.EmbeddingTimeout, maxResponseBytes: cfg.EmbeddingMaxResponseBytes},
		}
	}
	return runtime, nil
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
	return fmt.Sprintf("ModelRuntime{%s %s}", runtime.chat, runtime.embedding)
}

// GoString applies the endpoint- and credential-safe representation to %#v.
func (runtime *ModelRuntime) GoString() string { return runtime.String() }
