package runtime

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

// RevisionService is the read boundary required during managed process composition.
type RevisionService interface {
	Snapshot(context.Context) (modelsettingsdomain.Snapshot, error)
	LoadRevision(context.Context, int64) (modelsettingsdomain.ResolvedSettings, error)
}

// LoadedSettings is one fixed model revision and its initial runtime ownership.
type LoadedSettings struct {
	Models       *Models
	Revision     int64
	RolloutID    *foundation.ID
	InitialPhase modelsettingsdomain.RuntimePhase
}

// LoadSettings 为普通进程加载 active revision，为候选进程加载固定 rollout target，并透传可选模型 telemetry。
func LoadSettings(ctx context.Context, base config.Config, service RevisionService, telemetry ...platformmodels.ModelTelemetry) (LoadedSettings, error) {
	if base.ModelSettingsMode != config.ModelSettingsModeManaged {
		models, err := newModels(base, 0, telemetry...)
		if err != nil {
			return LoadedSettings{}, err
		}
		return LoadedSettings{Models: models, Revision: 0}, nil
	}
	if service == nil {
		return LoadedSettings{}, foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeUnavailable, true, errors.New("model settings revision service is unavailable"))
	}
	snapshot, err := service.Snapshot(ctx)
	if err != nil {
		return LoadedSettings{}, err
	}
	revision := snapshot.ActiveRevision
	phase := modelsettingsdomain.RuntimePhaseActive
	var rolloutID *foundation.ID
	if base.ModelSettingsRolloutID != "" {
		parsed, parseErr := foundation.ParseID(base.ModelSettingsRolloutID)
		if parseErr != nil || !base.ModelSettingsPrepared {
			return LoadedSettings{}, invalid(errors.New("managed candidate rollout binding is invalid"))
		}
		if snapshot.Rollout.ID != parsed || (snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseApplying && snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseVerifying) {
			return LoadedSettings{}, foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeRolloutConflict, false, errors.New("managed candidate rollout is not applying"))
		}
		revision = snapshot.Rollout.TargetRevision
		rolloutID = &parsed
		phase = modelsettingsdomain.RuntimePhasePrepared
	}
	resolved, err := service.LoadRevision(ctx, revision)
	if err != nil {
		if rolloutID != nil {
			return LoadedSettings{}, err
		}
		return unavailableLoaded(base, revision, err, telemetry...)
	}
	defer resolved.ChatAPIKey.Destroy()
	defer resolved.EmbeddingAPIKey.Destroy()
	if resolved.Revision != revision {
		mismatch := foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeCorrupt, false, errors.New("loaded model revision does not match the selected revision"))
		if rolloutID != nil {
			return LoadedSettings{}, mismatch
		}
		return unavailableLoaded(base, revision, mismatch, telemetry...)
	}
	models, err := Build(base, resolved, telemetry...)
	if err != nil {
		if rolloutID != nil {
			return LoadedSettings{}, err
		}
		return unavailableLoaded(base, revision, err, telemetry...)
	}
	return LoadedSettings{Models: models, Revision: revision, RolloutID: rolloutID, InitialPhase: phase}, nil
}

func unavailableLoaded(base config.Config, revision int64, cause error, telemetry ...platformmodels.ModelTelemetry) (LoadedSettings, error) {
	models, disabledErr := Build(base, modelsettingsdomain.ResolvedSettings{Revision: revision, Settings: modelsettingsdomain.CanonicalDisabledSettings()}, telemetry...)
	if disabledErr != nil {
		return LoadedSettings{}, cause
	}
	return LoadedSettings{Models: models, Revision: revision, InitialPhase: modelsettingsdomain.RuntimePhaseUnavailable}, cause
}

// NewService creates the shared application service from stable runtime dependencies.
func NewService(repository modelsettingsapplication.RevisionStore, validator modelsettingsapplication.Validator, _ modelsettingsapplication.SecretSealer) (*modelsettingsapplication.Service, error) {
	return modelsettingsapplication.NewService(repository, validator, 0)
}
