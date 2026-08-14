package runtime

import (
	"context"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

// BootstrapDB is the shared PostgreSQL boundary required by settings revisions and append-only Audit.
type BootstrapDB interface {
	modelsettingspostgres.DB
	auditpostgres.DB
}

// BootstrapResult contains the shared managed-settings dependencies for one process.
type BootstrapResult struct {
	Service         *modelsettingsapplication.Service
	Manager         modelsettingsapplication.SettingsManager
	Repository      *modelsettingspostgres.Repository
	Loaded          LoadedSettings
	KeyError        error
	LocalModelStore localmodelruntime.LifecycleStore
}

// Bootstrap builds the repository, key boundary, application service, and fixed process revision.
func Bootstrap(ctx context.Context, database BootstrapDB, cfg config.Config, telemetry ...platformmodels.ModelTelemetry) (BootstrapResult, error) {
	sealer, keyErr := modelcrypto.NewSealerFromFile(cfg.ModelSettingsKeyFile)
	var secretSealer modelsettingsapplication.SecretSealer = sealer
	if keyErr != nil {
		secretSealer = NewUnavailableSecretSealer(keyErr)
	}
	auditStore, err := auditpostgres.NewStore(database)
	if err != nil {
		return BootstrapResult{}, err
	}
	var resultStore localmodelruntime.LifecycleStore
	var lifecycleOption modelsettingspostgres.Option
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		lifecycleStore, lifecycleErr := localmodelruntime.NewPostgresStore(database)
		if lifecycleErr != nil {
			return BootstrapResult{}, lifecycleErr
		}
		resultStore = lifecycleStore
		lifecycleOption = modelsettingspostgres.WithLocalModelLifecycle(lifecycleStore)
	}
	options := []modelsettingspostgres.Option{
		modelsettingspostgres.WithSecretSealer(secretSealer),
		modelsettingspostgres.WithAuditAppender(auditStore),
	}
	if lifecycleOption != nil {
		options = append(options, lifecycleOption)
	}
	repository, err := modelsettingspostgres.NewRepository(database, options...)
	if err != nil {
		return BootstrapResult{}, err
	}
	validator := NewValidator(cfg)
	service, err := NewService(repository, validator, secretSealer)
	if err != nil {
		return BootstrapResult{}, err
	}
	var testLifecycle modelsettingsapplication.TestLifecycle
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		lifecycle, lifecycleErr := NewTestLifecycle(resultStore)
		if lifecycleErr != nil {
			return BootstrapResult{}, lifecycleErr
		}
		testLifecycle = lifecycle
		// API/Worker create role-scoped generation adapters after their process
		// instance identity is known; the store itself is shared and read-only.
	}
	manager, err := modelsettingsapplication.NewSettingsManagerWithLifecycle(repository, validator, NewConnectionTester(cfg, telemetry...), testLifecycle, 0)
	if err != nil {
		return BootstrapResult{}, err
	}
	loaded, loadErr := LoadSettings(ctx, cfg, service, telemetry...)
	return BootstrapResult{Service: service, Manager: manager, Repository: repository, Loaded: loaded, KeyError: keyErr, LocalModelStore: resultStore}, loadErr
}
