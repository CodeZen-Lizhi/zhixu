package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

type revisionServiceStub struct {
	snapshot domain.Snapshot
	resolved domain.ResolvedSettings
	loadErr  error
	loaded   int64
}

func (stub *revisionServiceStub) Snapshot(context.Context) (domain.Snapshot, error) {
	return stub.snapshot, nil
}
func (stub *revisionServiceStub) LoadRevision(_ context.Context, revision int64) (domain.ResolvedSettings, error) {
	stub.loaded = revision
	return stub.resolved, stub.loadErr
}

func TestLoadSettingsUsesActiveRevisionAndFailsClosed(t *testing.T) {
	t.Parallel()

	base := config.Defaults()
	base.ModelSettingsMode = config.ModelSettingsModeManaged
	base.ModelSettingsKeyFile = "/run/secrets/model-settings.key"
	stub := &revisionServiceStub{snapshot: domain.Snapshot{ActiveRevision: 4}, loadErr: errors.New("key unavailable")}
	loaded, err := LoadSettings(context.Background(), base, stub)
	if err == nil || stub.loaded != 4 || loaded.Revision != 4 || loaded.InitialPhase != domain.RuntimePhaseUnavailable {
		t.Fatalf("loaded=%+v revision=%d error=%v", loaded, stub.loaded, err)
	}
	if loaded.Models == nil || loaded.Models.Chat().Model() != nil || loaded.Models.Embedding().Embedder() != nil {
		t.Fatalf("fallback runtime = %#v", loaded.Models)
	}
	if loaded.Models.Revision() != loaded.Revision {
		t.Fatalf("runtime revision=%d loaded revision=%d", loaded.Models.Revision(), loaded.Revision)
	}
}

func TestLoadSettingsPreservesStaticConfiguredModels(t *testing.T) {
	t.Parallel()

	base := config.Defaults()
	base.ChatProvider = config.ChatProviderOpenAICompatible
	base.ChatBaseURL = "https://static-models.example.test"
	base.ChatAPIKey = "static-chat-secret-canary"
	base.ChatModel = "static-chat-v1"
	base.ChatModelVersion = "static-chat-v1"
	loaded, err := LoadSettings(context.Background(), base, nil)
	if err != nil {
		t.Fatal(err)
	}
	contract, ok := loaded.Models.Chat().Contract()
	if loaded.Revision != 0 || loaded.InitialPhase != "" || !ok || loaded.Models.Chat().Model() == nil || contract.Model.ModelID != base.ChatModel {
		t.Fatalf("loaded = %+v contract=%#v", loaded, contract)
	}
	if loaded.Models.Embedding().Embedder() != nil {
		t.Fatalf("unexpected embedding = %#v", loaded.Models.Embedding())
	}
	if loaded.Models.Revision() != 0 {
		t.Fatalf("static runtime revision = %d", loaded.Models.Revision())
	}
}

func TestLoadSettingsPinsCandidateTarget(t *testing.T) {
	t.Parallel()

	rolloutID := foundation.ID("10000000-0000-4000-8000-000000000001")
	base := config.Defaults()
	base.ModelSettingsMode = config.ModelSettingsModeManaged
	base.ModelSettingsKeyFile = "/run/secrets/model-settings.key"
	base.ModelSettingsRolloutID = string(rolloutID)
	base.ModelSettingsPrepared = true
	stub := &revisionServiceStub{
		snapshot: domain.Snapshot{ActiveRevision: 3, Rollout: domain.RolloutState{ID: rolloutID, TargetRevision: 5, Phase: domain.RolloutPhaseApplying}},
		resolved: domain.ResolvedSettings{Revision: 5, Settings: domain.CanonicalDisabledSettings()},
	}
	loaded, err := LoadSettings(context.Background(), base, stub)
	if err != nil {
		t.Fatal(err)
	}
	if stub.loaded != 5 || loaded.Revision != 5 || loaded.InitialPhase != domain.RuntimePhasePrepared || loaded.RolloutID == nil || *loaded.RolloutID != rolloutID {
		t.Fatalf("loaded=%+v revision=%d", loaded, stub.loaded)
	}
	if loaded.Models.Revision() != 5 {
		t.Fatalf("runtime revision = %d", loaded.Models.Revision())
	}
}

func TestLoadSettingsFailsClosedWhenRepositoryReturnsAnotherRevision(t *testing.T) {
	t.Parallel()

	base := config.Defaults()
	base.ModelSettingsMode = config.ModelSettingsModeManaged
	base.ModelSettingsKeyFile = "/run/secrets/model-settings.key"
	stub := &revisionServiceStub{
		snapshot: domain.Snapshot{ActiveRevision: 4},
		resolved: domain.ResolvedSettings{Revision: 5, Settings: domain.CanonicalDisabledSettings()},
	}
	loaded, err := LoadSettings(context.Background(), base, stub)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != domain.ErrorCodeCorrupt {
		t.Fatalf("error = %v", err)
	}
	if loaded.Revision != 4 || loaded.Models == nil || loaded.Models.Revision() != 4 || loaded.InitialPhase != domain.RuntimePhaseUnavailable {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestLoadSettingsRejectsUnfixedCandidate(t *testing.T) {
	t.Parallel()

	base := config.Defaults()
	base.ModelSettingsMode = config.ModelSettingsModeManaged
	base.ModelSettingsKeyFile = "/run/secrets/model-settings.key"
	base.ModelSettingsRolloutID = "10000000-0000-4000-8000-000000000001"
	base.ModelSettingsPrepared = true
	_, err := LoadSettings(context.Background(), base, &revisionServiceStub{snapshot: domain.Snapshot{Rollout: domain.RolloutState{Phase: domain.RolloutPhaseIdle}}})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeRolloutConflict {
		t.Fatalf("error = %v", err)
	}
}
