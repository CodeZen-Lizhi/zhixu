package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

func TestBuildReplacesAllStaticModelFieldsWithoutExposingConfig(t *testing.T) {
	t.Parallel()

	base := config.Defaults()
	base.ChatProvider = config.ChatProviderOpenAICompatible
	base.ChatBaseURL = "https://old.example.test"
	base.ChatAPIKey = "old-chat-secret"
	base.ChatModel = "old-chat"
	base.ChatModelVersion = "old-version"
	base.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
	base.EmbeddingBaseURL = "https://old.example.test"
	base.EmbeddingAPIKey = "old-embedding-secret"
	base.EmbeddingModel = "old-embedding"
	base.EmbeddingDimensions = 12

	resolved := domain.ResolvedSettings{Revision: 0, Settings: domain.CanonicalDisabledSettings()}
	models, err := Build(base, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if models.Chat().Model() != nil || models.Chat().State() != platformmodels.CapabilityDisabled {
		t.Fatalf("chat settings were not replaced: %#v", models.Chat())
	}
	if models.Embedding().Embedder() != nil || models.Embedding().State() != platformmodels.CapabilityDisabled {
		t.Fatalf("embedding settings were not replaced: %#v", models.Embedding())
	}
	formatted := fmt.Sprintf("%v %#v", models, models)
	for _, forbidden := range []string{base.ChatAPIKey, base.EmbeddingAPIKey, base.ChatBaseURL, base.EmbeddingBaseURL} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("model runtime leaked %q", forbidden)
		}
	}
}

func TestValidatorUsesProductionFactoryRules(t *testing.T) {
	t.Parallel()

	settings := domain.CanonicalDisabledSettings()
	settings.Embedding.Provider = domain.EmbeddingProviderOpenAICompatible
	settings.Embedding.BaseURL = "https://models.example.test"
	settings.Embedding.Model = "embedding-v1"
	settings.Embedding.Dimensions = 3
	validator := NewValidator(config.Defaults())
	err := validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{})
	assertInvalid(t, err)
	if err := validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{EmbeddingConfigured: true}); err != nil {
		t.Fatal(err)
	}
}

func TestValidatorRestrictsManagedOllamaToRelay(t *testing.T) {
	t.Parallel()

	settings := domain.CanonicalDisabledSettings()
	settings.Embedding.Provider = domain.EmbeddingProviderOllama
	settings.Embedding.BaseURL = "https://ollama.example.test"
	settings.Embedding.Model = "nomic-embed-text"
	settings.Embedding.Dimensions = 768
	validator := NewValidator(config.Defaults())
	assertInvalid(t, validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{}))
	settings.Embedding.BaseURL = managedOllamaBaseURL
	if err := validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidatorRestrictsManagedLoopbackToExactRelay(t *testing.T) {
	t.Parallel()

	validator := NewValidator(config.Defaults())
	for _, baseURL := range []string{
		"http://localhost:11434",
		"http://127.0.0.1:11435",
		"http://127.0.0.2:11434",
		"http://[::1]:11434",
		"http://127.0.0.1:11434/v1",
	} {
		settings := domain.CanonicalDisabledSettings()
		settings.Chat.Provider = domain.ChatProviderOpenAICompatible
		settings.Chat.BaseURL = baseURL
		settings.Chat.Model = "chat-v1"
		settings.Chat.ModelVersion = "chat-v1"
		assertInvalid(t, validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{}))
	}
	settings := domain.CanonicalDisabledSettings()
	settings.Chat.Provider = domain.ChatProviderOpenAICompatible
	settings.Chat.BaseURL = managedOllamaBaseURL
	settings.Chat.Model = "chat-v1"
	settings.Chat.ModelVersion = "chat-v1"
	if err := validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidatorRequiresHTTPSForManagedOpenAIEmbedding(t *testing.T) {
	t.Parallel()

	settings := domain.CanonicalDisabledSettings()
	settings.Embedding.Provider = domain.EmbeddingProviderOpenAICompatible
	settings.Embedding.BaseURL = managedOllamaBaseURL
	settings.Embedding.Model = "embedding-v1"
	settings.Embedding.Dimensions = 3
	validator := NewValidator(config.Defaults())
	assertInvalid(t, validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{EmbeddingConfigured: true}))
	settings.Embedding.BaseURL = "https://models.example.test"
	if err := validator.ValidateModelSettings(context.Background(), settings, domain.SecretConfiguration{EmbeddingConfigured: true}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAllowsCanonicalDisabledRevision(t *testing.T) {
	t.Parallel()

	models, err := Build(config.Defaults(), domain.ResolvedSettings{Settings: domain.CanonicalDisabledSettings()})
	if err != nil {
		t.Fatal(err)
	}
	if models.Chat().Model() != nil || models.Embedding().Embedder() != nil {
		t.Fatalf("disabled models = %#v", models)
	}
	assertInvalid(t, NewConnectionTester(config.Defaults()).TestResolvedConnection(context.Background(), ConnectionTargetChat, domain.ResolvedSettings{Settings: domain.CanonicalDisabledSettings()}))
}

func TestBuildBindsNonSensitiveRevisionAndClearsBaseCredentials(t *testing.T) {
	t.Parallel()

	base := config.Defaults()
	base.ChatAPIKey = "stale-chat-secret"
	base.EmbeddingAPIKey = "stale-embedding-secret"
	base.DatabasePassword = "database-secret"
	models, err := Build(base, domain.ResolvedSettings{Revision: 7, Settings: domain.CanonicalDisabledSettings()})
	if err != nil {
		t.Fatal(err)
	}
	if models.Revision() != 7 {
		t.Fatalf("revision = %d", models.Revision())
	}
	sanitized := WithoutModelCredentials(base)
	if sanitized.ChatAPIKey != "" || sanitized.EmbeddingAPIKey != "" || sanitized.DatabasePassword != base.DatabasePassword {
		t.Fatalf("sanitized config = %s", sanitized)
	}
}

func assertInvalid(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != domain.ErrorCodeInvalid {
		t.Fatalf("error = %v", err)
	}
}
