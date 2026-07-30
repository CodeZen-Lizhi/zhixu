package runtime

import (
	"context"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

// BootstrapDB is the shared PostgreSQL boundary required by settings revisions and append-only Audit.
type BootstrapDB interface {
	modelsettingspostgres.DB
	auditpostgres.DB
}

// BootstrapResult contains the shared managed-settings dependencies for one process.
type BootstrapResult struct {
	Service    *modelsettingsapplication.Service
	Manager    modelsettingsapplication.SettingsManager
	Repository *modelsettingspostgres.Repository
	Loaded     LoadedSettings
	KeyError   error
}

// Bootstrap builds the repository, key boundary, application service, and fixed process revision.
func Bootstrap(ctx context.Context, database BootstrapDB, cfg config.Config) (BootstrapResult, error) {
	sealer, keyErr := modelcrypto.NewSealerFromFile(cfg.ModelSettingsKeyFile)
	var secretSealer modelsettingsapplication.SecretSealer = sealer
	if keyErr != nil {
		secretSealer = NewUnavailableSecretSealer(keyErr)
	}
	auditStore, err := auditpostgres.NewStore(database)
	if err != nil {
		return BootstrapResult{}, err
	}
	repository, err := modelsettingspostgres.NewRepository(
		database,
		modelsettingspostgres.WithSecretSealer(secretSealer),
		modelsettingspostgres.WithAuditAppender(auditStore),
	)
	if err != nil {
		return BootstrapResult{}, err
	}
	validator := NewValidator(cfg)
	service, err := NewService(repository, validator, secretSealer)
	if err != nil {
		return BootstrapResult{}, err
	}
	manager, err := modelsettingsapplication.NewSettingsManager(repository, validator, NewConnectionTester(cfg), 0)
	if err != nil {
		return BootstrapResult{}, err
	}
	loaded, loadErr := LoadSettings(ctx, cfg, service)
	return BootstrapResult{Service: service, Manager: manager, Repository: repository, Loaded: loaded, KeyError: keyErr}, loadErr
}
