package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// ManagedModelsHostOptions adopts the already-loaded serving runtime and owns
// every later exact-revision build for one process role.
type ManagedModelsHostOptions struct {
	Base       config.Config
	Revisions  modelsettingsapplication.RevisionLoader
	Role       modelsettingsdomain.RuntimeRole
	InstanceID foundation.ID
	Loaded     LoadedSettings

	BuildTimeout    time.Duration
	HistoricalLimit int
}

// NewManagedModelsHost creates the process-local Chat+Embedding generation host.
// A successful call transfers ownership of Loaded.Models to the returned host.
func NewManagedModelsHost(options ManagedModelsHostOptions) (*RuntimeHost[*Models], error) {
	if nilRuntimeDependency(options.Revisions) || !modelsettingsdomain.ValidRuntimeRole(options.Role) ||
		!validRuntimeID(options.InstanceID) || options.Loaded.Models == nil ||
		options.Loaded.Revision != options.Loaded.Models.Revision() || options.Loaded.Revision < 0 ||
		options.Loaded.RolloutID != nil ||
		(options.Loaded.InitialPhase != modelsettingsdomain.RuntimePhaseActive &&
			options.Loaded.InitialPhase != modelsettingsdomain.RuntimePhaseUnavailable) {
		return nil, runtimeBindingError(errors.New("managed models host options are invalid"))
	}
	factory := &modelsGenerationFactory{
		base:      WithoutModelCredentials(options.Base),
		revisions: options.Revisions,
	}
	return NewRuntimeHost(RuntimeHostOptions[*Models]{
		Initial: InitialRuntime[*Models]{
			Binding: RuntimeBinding{
				Mode: RuntimeModeManaged, Role: options.Role,
				Revision: options.Loaded.Revision, InstanceID: options.InstanceID,
			},
			Value:       options.Loaded.Models,
			Unavailable: options.Loaded.InitialPhase == modelsettingsdomain.RuntimePhaseUnavailable,
		},
		Factory: factory,
		EmbeddingCompatible: func(models *Models, version retrievaldomain.EmbeddingVersion) error {
			return models.ValidateEmbeddingVersion(version)
		},
		BuildTimeout:    options.BuildTimeout,
		HistoricalLimit: options.HistoricalLimit,
	})
}

type modelsGenerationFactory struct {
	base      config.Config
	revisions modelsettingsapplication.RevisionLoader
}

func (factory *modelsGenerationFactory) Build(ctx context.Context, revision int64) (*Models, error) {
	if factory == nil || nilRuntimeDependency(factory.revisions) || ctx == nil || revision <= 0 {
		return nil, runtimeRevisionError(errors.New("model generation build input is invalid"))
	}
	resolved, err := factory.revisions.LoadRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	defer resolved.ChatAPIKey.Destroy()
	defer resolved.EmbeddingAPIKey.Destroy()
	if resolved.Revision != revision {
		return nil, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			modelsettingsdomain.ErrorCodeCorrupt,
			false,
			errors.New("loaded model generation revision does not match target"),
		)
	}
	return Build(factory.base, resolved)
}

func (factory *modelsGenerationFactory) Probe(ctx context.Context, models *Models) error {
	if factory == nil || ctx == nil || models == nil {
		return runtimeNotReadyError(errors.New("model generation probe is unavailable"))
	}
	chat := models.Chat()
	if chat.State() == platformmodels.CapabilityConfigured {
		prober, ok := chat.Model().(platformmodels.ChatConnectionProber)
		if !ok || prober == nil {
			return foundation.NewError(
				foundation.ErrorConsistencyViolation,
				modelsettingsdomain.ErrorCodeUnavailable,
				false,
				errors.New("chat generation probe is unavailable"),
			)
		}
		if err := prober.ProbeConnection(ctx); err != nil {
			return err
		}
	}
	embedding := models.Embedding()
	if embedding.State() == platformmodels.CapabilityConfigured {
		if embedding.Embedder() == nil {
			return foundation.NewError(
				foundation.ErrorConsistencyViolation,
				modelsettingsdomain.ErrorCodeUnavailable,
				false,
				errors.New("embedding generation probe is unavailable"),
			)
		}
		if _, err := embedding.Embedder().Embed(ctx, retrievalapplication.EmbedRequest{Inputs: []string{"test"}}); err != nil {
			return err
		}
	}
	return nil
}

func (factory *modelsGenerationFactory) Close(models *Models) {
	if models != nil {
		_ = models.Close()
	}
}
