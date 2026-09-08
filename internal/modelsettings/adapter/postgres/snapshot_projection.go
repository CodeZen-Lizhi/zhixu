package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const defaultSnapshotStaleAfter = application.DefaultRuntimeFreshWithin

func runtimeReady(runtime domain.RuntimeSummary, revision int64) bool {
	return runtime.Fresh && runtime.Phase == domain.RuntimePhaseActive && runtime.AppliedRevision == revision
}

func chatCapability(active domain.SettingsSummary, runtimesReady bool) domain.Capability {
	if active.Settings.Chat.Provider == domain.ChatProviderDisabled {
		return domain.CapabilityDisabled
	}
	if !runtimesReady {
		return domain.CapabilityUnavailable
	}
	return domain.CapabilityConfigured
}

func embeddingCapability(active domain.SettingsSummary, runtimesReady bool) domain.Capability {
	if active.Settings.Embedding.Provider == domain.EmbeddingProviderDisabled {
		return domain.CapabilityDisabled
	}
	if !runtimesReady || active.Settings.Embedding.Provider == domain.EmbeddingProviderOpenAICompatible && !active.Secrets.EmbeddingConfigured {
		return domain.CapabilityUnavailable
	}
	return domain.CapabilityConfigured
}
