package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func TestServiceFormattingDoesNotExpandDependencies(t *testing.T) {
	const canary = "service-format-secret-canary"
	service := Service{
		revisions: &serviceTestRepository{},
		validator: ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
			return errors.New(canary)
		}),
		tester: &serviceTestConnectionTester{err: errors.New(canary)},
	}
	formatted := fmt.Sprintf("%+v %#v", service, service)
	if strings.Contains(formatted, canary) {
		t.Fatalf("service formatting expanded private dependencies: %q", formatted)
	}
}

func TestServiceSaveCanonicalizesAndValidatesProjectedSecrets(t *testing.T) {
	previous := configuredServiceSettings()
	repository := &serviceTestRepository{snapshot: domain.Snapshot{
		DesiredRevision: 3,
		DesiredSettings: domain.SettingsSummary{
			Settings: previous, Secrets: domain.SecretConfiguration{ChatConfigured: true},
		},
	}}
	var validated domain.Settings
	var secrets domain.SecretConfiguration
	validator := ValidatorFunc(func(_ context.Context, settings domain.Settings, configuration domain.SecretConfiguration) error {
		validated = settings
		secrets = configuration
		return nil
	})
	service, err := NewService(repository, validator, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Chat.BaseURL = "HTTPS://Models.Example.Test:443/v1/"
	result, err := service.Save(context.Background(), SaveCommand{
		ExpectedRevision: 3, Settings: next, ChatSecret: domain.KeepSecret(),
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "service-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if validated.Chat.BaseURL != "https://models.example.test/v1" || repository.saved.Settings.Chat.BaseURL != validated.Chat.BaseURL {
		t.Fatalf("canonical URL validator=%q repository=%q", validated.Chat.BaseURL, repository.saved.Settings.Chat.BaseURL)
	}
	if !secrets.ChatConfigured || secrets.EmbeddingConfigured {
		t.Fatalf("projected secrets=%+v", secrets)
	}
	if result.DesiredRevision != 4 {
		t.Fatalf("saved revision=%d", result.DesiredRevision)
	}
}

func TestServiceSaveRejectsSecretEndpointRebindBeforeValidation(t *testing.T) {
	previous := configuredServiceSettings()
	repository := &serviceTestRepository{snapshot: domain.Snapshot{
		DesiredRevision: 3,
		DesiredSettings: domain.SettingsSummary{
			Settings: previous, Secrets: domain.SecretConfiguration{ChatConfigured: true},
		},
	}}
	validatorCalls := 0
	service, err := NewService(repository, ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
		validatorCalls++
		return nil
	}), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Chat.BaseURL = "https://other.example.test/v1"
	_, err = service.Save(context.Background(), SaveCommand{
		ExpectedRevision: 3, Settings: next, ChatSecret: domain.KeepSecret(),
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "service-test",
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeSecretTargetChanged {
		t.Fatalf("error=%v", err)
	}
	if validatorCalls != 0 || repository.saveCalls != 0 {
		t.Fatalf("validation calls=%d save calls=%d", validatorCalls, repository.saveCalls)
	}
}

type serviceTestRepository struct {
	RevisionStore
	snapshot  domain.Snapshot
	resolved  domain.ResolvedSettings
	saved     SaveCommand
	saveCalls int
}

func (repository *serviceTestRepository) Snapshot(context.Context, time.Duration) (domain.Snapshot, error) {
	return repository.snapshot, nil
}

func (repository *serviceTestRepository) SaveDesired(_ context.Context, command SaveCommand) (domain.Snapshot, error) {
	repository.saved = command
	repository.saveCalls++
	result := repository.snapshot
	result.DesiredRevision = command.ExpectedRevision + 1
	return result, nil
}

func (repository *serviceTestRepository) ResolveDraft(context.Context, DraftCommand) (domain.ResolvedSettings, error) {
	return repository.resolved, nil
}

func (repository *serviceTestRepository) LoadRevision(context.Context, int64) (domain.ResolvedSettings, error) {
	return repository.resolved, nil
}

type serviceTestConnectionTester struct {
	err      error
	target   ConnectionTarget
	resolved domain.ResolvedSettings
}

func (tester *serviceTestConnectionTester) TestResolvedConnection(_ context.Context, target ConnectionTarget, resolved domain.ResolvedSettings) error {
	tester.target = target
	tester.resolved = resolved
	return tester.err
}

func TestSettingsManagerTestOwnsResolvedSecretLifecycle(t *testing.T) {
	t.Parallel()

	settings := configuredServiceSettings()
	chatSecret, err := domain.NewSecret("chat-connection-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	embeddingSecret, err := domain.NewSecret("embedding-connection-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	repository := &serviceTestRepository{
		snapshot: domain.Snapshot{DesiredRevision: 3, DesiredSettings: domain.SettingsSummary{
			Settings: settings, Secrets: domain.SecretConfiguration{ChatConfigured: true},
		}},
		resolved: domain.ResolvedSettings{Revision: 3, Settings: settings, ChatAPIKey: chatSecret, EmbeddingAPIKey: embeddingSecret},
	}
	tester := &serviceTestConnectionTester{}
	manager, err := NewSettingsManager(repository, ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
		return nil
	}), tester, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Test(context.Background(), TestCommand{Target: ConnectionTargetChat, Draft: DraftCommand{
		ExpectedRevision: 3, Settings: settings, ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.ClearSecret(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != ConnectionTargetChat || result.Provider != string(settings.Chat.Provider) || result.Model != settings.Chat.Model {
		t.Fatalf("result = %+v", result)
	}
	if tester.target != ConnectionTargetChat || !tester.resolved.ChatAPIKey.Configured() || !tester.resolved.EmbeddingAPIKey.Configured() {
		t.Fatalf("tester received target=%q resolved=%+v", tester.target, tester.resolved)
	}
	assertSecretBufferCleared(t, repository.resolved.ChatAPIKey)
	assertSecretBufferCleared(t, repository.resolved.EmbeddingAPIKey)
}

func TestSettingsManagerTestDestroysResolvedSecretsOnProviderFailure(t *testing.T) {
	t.Parallel()

	settings := configuredServiceSettings()
	chatSecret, err := domain.NewSecret("failed-chat-connection-secret")
	if err != nil {
		t.Fatal(err)
	}
	embeddingSecret, err := domain.NewSecret("failed-embedding-connection-secret")
	if err != nil {
		t.Fatal(err)
	}
	repository := &serviceTestRepository{
		snapshot: domain.Snapshot{DesiredRevision: 3, DesiredSettings: domain.SettingsSummary{
			Settings: settings, Secrets: domain.SecretConfiguration{ChatConfigured: true},
		}},
		resolved: domain.ResolvedSettings{Revision: 3, Settings: settings, ChatAPIKey: chatSecret, EmbeddingAPIKey: embeddingSecret},
	}
	providerErr := errors.New("provider unavailable")
	manager, err := NewSettingsManager(repository, ValidatorFunc(func(context.Context, domain.Settings, domain.SecretConfiguration) error {
		return nil
	}), &serviceTestConnectionTester{err: providerErr}, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Test(context.Background(), TestCommand{Target: ConnectionTargetChat, Draft: DraftCommand{
		ExpectedRevision: 3, Settings: settings, ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.ClearSecret(),
	}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider failure", err)
	}
	assertSecretBufferCleared(t, repository.resolved.ChatAPIKey)
	assertSecretBufferCleared(t, repository.resolved.EmbeddingAPIKey)
}

func assertSecretBufferCleared(t *testing.T, secret domain.Secret) {
	t.Helper()
	value := secret.Bytes()
	defer clear(value)
	for _, current := range value {
		if current != 0 {
			t.Fatal("resolved secret buffer was not cleared")
		}
	}
}

func configuredServiceSettings() domain.Settings {
	settings := domain.CanonicalDisabledSettings()
	settings.Chat.Provider = domain.ChatProviderOpenAICompatible
	settings.Chat.BaseURL = "https://models.example.test/v1"
	settings.Chat.Model = "chat-model"
	settings.Chat.ModelVersion = "v1"
	return settings
}
