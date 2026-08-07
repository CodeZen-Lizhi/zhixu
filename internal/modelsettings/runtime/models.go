// Package runtime connects managed model settings to the existing model factories.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// ConnectionTargetChat identifies the Chat connection test.
	ConnectionTargetChat = modelsettingsapplication.ConnectionTargetChat
	// ConnectionTargetEmbedding identifies the Embedding connection test.
	ConnectionTargetEmbedding = modelsettingsapplication.ConnectionTargetEmbedding
	managedOllamaBaseURL      = "http://127.0.0.1:11434"
	configuredSecretProbe     = "model-settings-configured-secret"
)

// Models 是进程内唯一冻结的模型运行时及其非敏感配置 revision。
type Models struct {
	runtime  *platformmodels.ModelRuntime
	revision int64
}

// Chat 返回同一次生产构造冻结的 Chat Adapter 与 Contract。
func (models *Models) Chat() platformmodels.ChatCapability {
	if models == nil || models.runtime == nil {
		return (*platformmodels.ModelRuntime)(nil).Chat()
	}
	return models.runtime.Chat()
}

// Embedding 返回同一次生产构造冻结的 Embedding Adapter 与 Contract。
func (models *Models) Embedding() platformmodels.EmbeddingCapability {
	if models == nil || models.runtime == nil {
		return (*platformmodels.ModelRuntime)(nil).Embedding()
	}
	return models.runtime.Embedding()
}

// Revision 返回该运行时绑定的 managed revision；static 与 disabled bootstrap 为 0。
func (models *Models) Revision() int64 {
	if models == nil {
		return 0
	}
	return models.revision
}

// String 返回不含 Endpoint 或 Credential 的运行时摘要。
func (models *Models) String() string {
	if models == nil {
		return "Models{Revision:0 ModelRuntime{unavailable}}"
	}
	return fmt.Sprintf("Models{Revision:%d %s}", models.revision, models.runtime)
}

// GoString 避免调试格式展开 Adapter 私有字段。
func (models *Models) GoString() string { return models.String() }

// Validator validates managed settings through the same Config and factory paths used at runtime.
type Validator struct{ base config.Config }

var _ modelsettingsapplication.Validator = Validator{}

// NewValidator creates the shared managed-settings validator.
func NewValidator(base config.Config) Validator {
	return Validator{base: WithoutModelCredentials(base)}
}

// ValidateModelSettings validates one non-secret revision plus explicit secret presence.
func (validator Validator) ValidateModelSettings(_ context.Context, settings modelsettingsdomain.Settings, secrets modelsettingsdomain.SecretConfiguration) error {
	cfg, err := overlaySummary(validator.base, settings, secrets)
	if err != nil {
		return err
	}
	_, err = platformmodels.NewConfiguredModelRuntime(cfg)
	return err
}

// Build 使用同一生产 Factory 和可选项目 telemetry 构造指定 revision 的 Chat 与 Embedding Adapter。
func Build(base config.Config, resolved modelsettingsdomain.ResolvedSettings, telemetry ...platformmodels.ModelTelemetry) (*Models, error) {
	if resolved.Revision < 0 {
		return nil, invalid(errors.New("model runtime revision is invalid"))
	}
	cfg, err := overlayConfig(base, resolved)
	if err != nil {
		return nil, err
	}
	return newModels(cfg, resolved.Revision, telemetry...)
}

func newModels(cfg config.Config, revision int64, telemetry ...platformmodels.ModelTelemetry) (*Models, error) {
	defer forgetModelCredentials(&cfg)
	runtime, err := platformmodels.NewConfiguredModelRuntime(cfg, telemetry...)
	if err != nil {
		return nil, err
	}
	return &Models{runtime: runtime, revision: revision}, nil
}

// WithoutModelCredentials 返回移除模型 Credential 的长生命周期配置副本。
func WithoutModelCredentials(cfg config.Config) config.Config {
	forgetModelCredentials(&cfg)
	return cfg
}

// overlayConfig replaces every model-owned Config field for the short-lived factory input.
func overlayConfig(base config.Config, resolved modelsettingsdomain.ResolvedSettings) (config.Config, error) {
	chatKey := secretValue(resolved.ChatAPIKey)
	embeddingKey := secretValue(resolved.EmbeddingAPIKey)
	return overlay(base, resolved.Settings, chatKey, embeddingKey)
}

func overlaySummary(base config.Config, settings modelsettingsdomain.Settings, secrets modelsettingsdomain.SecretConfiguration) (config.Config, error) {
	chatKey := ""
	if secrets.ChatConfigured {
		chatKey = configuredSecretProbe
	}
	embeddingKey := ""
	if secrets.EmbeddingConfigured {
		embeddingKey = configuredSecretProbe
	}
	return overlay(base, settings, chatKey, embeddingKey)
}

func overlay(base config.Config, settings modelsettingsdomain.Settings, chatKey, embeddingKey string) (config.Config, error) {
	if err := settings.ValidateStructural(); err != nil {
		return config.Config{}, err
	}
	if err := validateManagedEndpoints(settings); err != nil {
		return config.Config{}, err
	}
	base.ChatProvider = config.ChatProvider(settings.Chat.Provider)
	base.ChatBaseURL = settings.Chat.BaseURL
	base.ChatAPIKey = chatKey
	base.ChatModel = settings.Chat.Model
	base.ChatModelVersion = settings.Chat.ModelVersion
	base.ChatAdapterVersion = settings.Chat.AdapterVersion
	base.ChatTimeout = settings.Chat.Timeout
	base.ChatMaxRequestBytes = settings.Chat.MaxRequestBytes
	base.ChatMaxResponseBytes = settings.Chat.MaxResponseBytes

	base.EmbeddingProvider = config.EmbeddingProvider(settings.Embedding.Provider)
	base.EmbeddingBaseURL = settings.Embedding.BaseURL
	base.EmbeddingAPIKey = embeddingKey
	base.EmbeddingModel = settings.Embedding.Model
	base.EmbeddingDimensions = settings.Embedding.Dimensions
	base.EmbeddingNormalization = retrievaldomain.EmbeddingNormalization(settings.Embedding.Normalization)
	base.EmbeddingDistanceMetric = retrievaldomain.DistanceMetric(settings.Embedding.DistanceMetric)
	base.EmbeddingMaxBatchSize = settings.Embedding.MaxBatchSize
	base.EmbeddingMaxInputBytes = settings.Embedding.MaxInputBytes
	base.EmbeddingMaxBatchInputBytes = settings.Embedding.MaxBatchInputBytes
	base.EmbeddingTimeout = settings.Embedding.Timeout
	base.EmbeddingMaxResponseBytes = settings.Embedding.MaxResponseBytes
	if err := base.ValidateModels(); err != nil {
		return config.Config{}, invalid(err)
	}
	return base, nil
}

// ConnectionTester executes minimal requests through the production adapters.
type ConnectionTester struct {
	base      config.Config
	telemetry []platformmodels.ModelTelemetry
}

var _ modelsettingsapplication.ResolvedConnectionTester = (*ConnectionTester)(nil)

// NewConnectionTester 创建复用生产模型 telemetry 的目标连接测试器。
func NewConnectionTester(base config.Config, telemetry ...platformmodels.ModelTelemetry) *ConnectionTester {
	return &ConnectionTester{base: WithoutModelCredentials(base), telemetry: append([]platformmodels.ModelTelemetry(nil), telemetry...)}
}

// TestChat tests one resolved Chat target without persisting or touching Embedding settings.
func (tester *ConnectionTester) TestChat(ctx context.Context, settings modelsettingsdomain.ChatSettings, secret modelsettingsdomain.Secret) error {
	draft := modelsettingsdomain.CanonicalDisabledSettings()
	draft.Chat = settings
	return tester.TestResolvedConnection(ctx, ConnectionTargetChat, modelsettingsdomain.ResolvedSettings{Settings: draft, ChatAPIKey: secret})
}

// TestEmbedding tests one resolved Embedding target without persisting or touching Chat settings.
func (tester *ConnectionTester) TestEmbedding(ctx context.Context, settings modelsettingsdomain.EmbeddingSettings, secret modelsettingsdomain.Secret) error {
	draft := modelsettingsdomain.CanonicalDisabledSettings()
	draft.Embedding = settings
	return tester.TestResolvedConnection(ctx, ConnectionTargetEmbedding, modelsettingsdomain.ResolvedSettings{Settings: draft, EmbeddingAPIKey: secret})
}

// TestConnection is the compatibility entry point for non-HTTP callers.
func (tester *ConnectionTester) TestConnection(ctx context.Context, target string, resolved modelsettingsdomain.ResolvedSettings) error {
	return tester.TestResolvedConnection(ctx, modelsettingsapplication.ConnectionTarget(target), resolved)
}

// TestResolvedConnection validates one Chat or Embedding draft through its production adapter.
func (tester *ConnectionTester) TestResolvedConnection(ctx context.Context, target modelsettingsapplication.ConnectionTarget, resolved modelsettingsdomain.ResolvedSettings) error {
	if tester == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeUnavailable, true, errors.New("model connection tester is unavailable"))
	}
	models, err := Build(tester.base, resolved, tester.telemetry...)
	if err != nil {
		return err
	}
	switch target {
	case ConnectionTargetChat:
		chat := models.Chat()
		if chat.Model() == nil {
			return invalid(errors.New("chat provider is disabled"))
		}
		contract, ok := chat.Contract()
		if !ok {
			return foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeUnavailable, false, errors.New("chat adapter contract is unavailable"))
		}
		_, err = chat.Model().Chat(ctx, agentapplication.ChatRequest{
			Phase:      agentdomain.ModelCallInitial,
			ProfileRef: agentdomain.ModelProfileRef{ID: "model-settings-connection", Version: "v1"},
			PromptRef:  agentdomain.PromptRef{ID: "model-settings-connection", Version: "v1"},
			SchemaRef:  agentdomain.SchemaRef{ID: "model-settings-connection", Version: "v1"},
			Model:      contract.Model,
			Messages: []agentapplication.ChatMessage{
				{Role: agentapplication.MessageRoleSystem, Content: "Return JSON that matches the supplied schema."},
				{Role: agentapplication.MessageRoleUser, Content: "Return ok=true."},
			},
			OutputSchema:    []byte(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
			MaxOutputTokens: 16,
		})
		return err
	case ConnectionTargetEmbedding:
		embedding := models.Embedding()
		if embedding.Embedder() == nil {
			return invalid(errors.New("embedding provider is disabled"))
		}
		_, err = embedding.Embedder().Embed(ctx, retrievalapplication.EmbedRequest{Inputs: []string{"zhixu model connection test"}})
		return err
	default:
		return invalid(errors.New("model connection target is invalid"))
	}
}

func secretValue(secret modelsettingsdomain.Secret) string {
	value := secret.Bytes()
	defer clear(value)
	return string(value)
}

func validateManagedEndpoints(settings modelsettingsdomain.Settings) error {
	if settings.Chat.Provider == modelsettingsdomain.ChatProviderOpenAICompatible {
		parsed, err := url.Parse(settings.Chat.BaseURL)
		if err != nil || parsed.Scheme != "https" && settings.Chat.BaseURL != managedOllamaBaseURL {
			return invalid(errors.New("managed chat endpoint must use https or the fixed loopback relay"))
		}
	}
	switch settings.Embedding.Provider {
	case modelsettingsdomain.EmbeddingProviderOpenAICompatible:
		parsed, err := url.Parse(settings.Embedding.BaseURL)
		if err != nil || parsed.Scheme != "https" {
			return invalid(errors.New("managed openai-compatible embedding endpoint must use https"))
		}
	case modelsettingsdomain.EmbeddingProviderOllama:
		if settings.Embedding.BaseURL != managedOllamaBaseURL {
			return invalid(errors.New("managed ollama must use the fixed loopback relay"))
		}
	}
	return nil
}

func forgetModelCredentials(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.ChatAPIKey = ""
	cfg.EmbeddingAPIKey = ""
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, modelsettingsdomain.ErrorCodeInvalid, false, cause)
}
