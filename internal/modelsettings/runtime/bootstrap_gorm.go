package runtime

import (
	"context"
	"errors"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

// GORMBootstrapResult contains the shared-pool dependencies for one process.
type GORMBootstrapResult struct {
	Service         *modelsettingsapplication.Service
	Manager         modelsettingsapplication.SettingsManager
	Repository      *modelsettingspostgres.GORMRepository
	Loaded          LoadedSettings
	KeyError        error
	LocalModelStore localmodelruntime.LifecycleStore
}

// BootstrapGORM builds Model Settings, Audit, and optional Local Runtime
// adapters from one platform Pool.
func BootstrapGORM(
	ctx context.Context,
	database *platformpostgres.Pool,
	cfg config.Config,
	telemetry ...platformmodels.ModelTelemetry,
) (GORMBootstrapResult, error) {
	if ctx == nil {
		return GORMBootstrapResult{}, errors.New("model settings GORM bootstrap context is nil")
	}
	if database == nil {
		return GORMBootstrapResult{}, errors.New("model settings GORM bootstrap database is nil")
	}

	sealer, keyErr := modelcrypto.NewSealerFromFile(cfg.ModelSettingsKeyFile)
	var secretSealer modelsettingsapplication.SecretSealer = sealer
	if keyErr != nil {
		secretSealer = NewUnavailableSecretSealer(keyErr)
	}
	auditStore, err := auditpostgres.NewGORMStore(database)
	if err != nil {
		return GORMBootstrapResult{}, err
	}

	options := []modelsettingspostgres.GORMOption{
		modelsettingspostgres.WithGORMSecretSealer(secretSealer),
		modelsettingspostgres.WithGORMAuditAppender(auditStore),
	}
	var resultStore localmodelruntime.LifecycleStore
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		lifecycleStore, lifecycleErr := localmodelruntime.NewGORMStore(database)
		if lifecycleErr != nil {
			return GORMBootstrapResult{}, lifecycleErr
		}
		resultStore = lifecycleStore
		options = append(options, modelsettingspostgres.WithGORMScopedLocalModelLifecycle(lifecycleStore))
	}

	repository, err := modelsettingspostgres.NewGORMRepository(database, options...)
	if err != nil {
		return GORMBootstrapResult{}, err
	}
	validator := NewValidator(cfg)
	service, err := NewService(repository, validator, secretSealer)
	if err != nil {
		return GORMBootstrapResult{}, err
	}
	var testLifecycle modelsettingsapplication.TestLifecycle
	if cfg.ModelSettingsMode == config.ModelSettingsModeManaged {
		lifecycle, lifecycleErr := NewTestLifecycle(resultStore)
		if lifecycleErr != nil {
			return GORMBootstrapResult{}, lifecycleErr
		}
		testLifecycle = lifecycle
	}
	manager, err := modelsettingsapplication.NewSettingsManagerWithLifecycle(
		repository,
		validator,
		NewConnectionTester(cfg, telemetry...),
		testLifecycle,
		0,
	)
	if err != nil {
		return GORMBootstrapResult{}, err
	}
	loaded, loadErr := LoadSettings(ctx, cfg, service, telemetry...)
	return GORMBootstrapResult{
		Service: service, Manager: manager, Repository: repository,
		Loaded: loaded, KeyError: keyErr, LocalModelStore: resultStore,
	}, loadErr
}
