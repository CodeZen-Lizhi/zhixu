package postgres

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const revisionColumns = `revision,
chat_provider,chat_api_style,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
chat_secret_key_id,chat_secret_nonce,chat_secret_ciphertext,
embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
embedding_max_response_bytes,embedding_secret_key_id,embedding_secret_nonce,
embedding_secret_ciphertext,created_at,created_by`

type persistedRevision struct {
	revision        int64
	settings        domain.Settings
	chatSecret      domain.EncryptedSecret
	embeddingSecret domain.EncryptedSecret
	createdAt       time.Time
	createdBy       string
}

func scanRevision(row interface{ Scan(...any) error }) (persistedRevision, error) {
	var persisted persistedRevision
	var chatProvider, chatAPIStyle, embeddingProvider, normalization, distance string
	var chatTimeout, embeddingTimeout int64
	var chatKeyID, embeddingKeyID sql.NullString
	if err := row.Scan(
		&persisted.revision,
		&chatProvider, &chatAPIStyle, &persisted.settings.Chat.BaseURL, &persisted.settings.Chat.Model,
		&persisted.settings.Chat.ModelVersion, &persisted.settings.Chat.AdapterVersion,
		&chatTimeout, &persisted.settings.Chat.MaxRequestBytes, &persisted.settings.Chat.MaxResponseBytes,
		&chatKeyID, &persisted.chatSecret.Nonce, &persisted.chatSecret.Ciphertext,
		&embeddingProvider, &persisted.settings.Embedding.BaseURL, &persisted.settings.Embedding.Model,
		&persisted.settings.Embedding.Dimensions, &normalization, &distance,
		&persisted.settings.Embedding.MaxBatchSize, &persisted.settings.Embedding.MaxInputBytes,
		&persisted.settings.Embedding.MaxBatchInputBytes, &embeddingTimeout,
		&persisted.settings.Embedding.MaxResponseBytes, &embeddingKeyID,
		&persisted.embeddingSecret.Nonce, &persisted.embeddingSecret.Ciphertext,
		&persisted.createdAt, &persisted.createdBy,
	); err != nil {
		return persistedRevision{}, err
	}
	persisted.settings.Chat.Provider = domain.ChatProvider(chatProvider)
	persisted.settings.Chat.APIStyle = domain.ChatAPIStyle(chatAPIStyle)
	persisted.settings.Chat.Timeout = time.Duration(chatTimeout) * time.Microsecond
	persisted.settings.Embedding.Provider = domain.EmbeddingProvider(embeddingProvider)
	persisted.settings.Embedding.Normalization = domain.EmbeddingNormalization(normalization)
	persisted.settings.Embedding.DistanceMetric = domain.DistanceMetric(distance)
	persisted.settings.Embedding.Timeout = time.Duration(embeddingTimeout) * time.Microsecond
	if chatKeyID.Valid {
		persisted.chatSecret.KeyID = chatKeyID.String
	}
	if embeddingKeyID.Valid {
		persisted.embeddingSecret.KeyID = embeddingKeyID.String
	}
	if persisted.revision <= 0 || persisted.settings.ValidateStructural() != nil || persisted.chatSecret.Validate() != nil ||
		persisted.embeddingSecret.Validate() != nil || persisted.createdBy == "" || persisted.createdAt.IsZero() {
		return persistedRevision{}, corrupt(errors.New("model settings revision is invalid"))
	}
	if (persisted.settings.Chat.Provider != domain.ChatProviderOpenAICompatible && persisted.chatSecret.Configured()) ||
		(persisted.settings.Embedding.Provider != domain.EmbeddingProviderOpenAICompatible && persisted.embeddingSecret.Configured()) {
		return persistedRevision{}, corrupt(errors.New("model settings revision secret target is invalid"))
	}
	persisted.createdAt = persisted.createdAt.UTC()
	return persisted, nil
}

func (persisted persistedRevision) summary() domain.SettingsSummary {
	return domain.SettingsSummary{
		Settings: persisted.settings,
		Secrets: domain.SecretConfiguration{
			ChatConfigured: persisted.chatSecret.Configured(), EmbeddingConfigured: persisted.embeddingSecret.Configured(),
		},
	}
}

func resolvePersisted(sealer application.SecretSealer, persisted persistedRevision) (domain.ResolvedSettings, error) {
	result := domain.ResolvedSettings{Revision: persisted.revision, Settings: persisted.settings}
	var err error
	if persisted.chatSecret.Configured() {
		result.ChatAPIKey, err = sealer.Open(persisted.chatSecret, secretContext(persisted.revision, persisted.settings, domain.SecretPurposeChat))
		if err != nil {
			return domain.ResolvedSettings{}, err
		}
	}
	if persisted.embeddingSecret.Configured() {
		result.EmbeddingAPIKey, err = sealer.Open(persisted.embeddingSecret, secretContext(persisted.revision, persisted.settings, domain.SecretPurposeEmbedding))
		if err != nil {
			result.ChatAPIKey.Destroy()
			return domain.ResolvedSettings{}, err
		}
	}
	return result, nil
}

func resolveEnvelope(sealer application.SecretSealer, previous persistedRevision, revision int64, settings domain.Settings, purpose domain.SecretPurpose, action domain.SecretAction) (domain.EncryptedSecret, error) {
	previousEnvelope, previousProvider, previousBaseURL := previousTarget(previous, purpose)
	nextProvider, nextBaseURL := targetIdentity(settings, purpose)
	if err := domain.ValidateSecretChange(previousProvider, previousBaseURL, previousEnvelope.Configured(), nextProvider, nextBaseURL, action); err != nil {
		return domain.EncryptedSecret{}, err
	}
	switch action.Kind {
	case domain.SecretActionClear:
		return domain.EncryptedSecret{}, nil
	case domain.SecretActionReplace:
		return sealer.Seal(action.Value, secretContext(revision, settings, purpose))
	case domain.SecretActionKeep:
		if !previousEnvelope.Configured() {
			return domain.EncryptedSecret{}, nil
		}
		opened, err := sealer.Open(previousEnvelope, secretContext(previous.revision, previous.settings, purpose))
		if err != nil {
			return domain.EncryptedSecret{}, err
		}
		defer opened.Destroy()
		return sealer.Seal(opened, secretContext(revision, settings, purpose))
	default:
		return domain.EncryptedSecret{}, invalid(errors.New("model settings secret action is invalid"))
	}
}

func resolveDraftSecret(sealer application.SecretSealer, previous persistedRevision, settings domain.Settings, purpose domain.SecretPurpose, action domain.SecretAction) (domain.Secret, error) {
	previousEnvelope, previousProvider, previousBaseURL := previousTarget(previous, purpose)
	nextProvider, nextBaseURL := targetIdentity(settings, purpose)
	if err := domain.ValidateSecretChange(previousProvider, previousBaseURL, previousEnvelope.Configured(), nextProvider, nextBaseURL, action); err != nil {
		return domain.Secret{}, err
	}
	switch action.Kind {
	case domain.SecretActionClear:
		return domain.Secret{}, nil
	case domain.SecretActionReplace:
		value := action.Value.Bytes()
		defer clear(value)
		return domain.SecretFromBytes(value)
	case domain.SecretActionKeep:
		if !previousEnvelope.Configured() {
			return domain.Secret{}, nil
		}
		return sealer.Open(previousEnvelope, secretContext(previous.revision, previous.settings, purpose))
	default:
		return domain.Secret{}, invalid(errors.New("model settings secret action is invalid"))
	}
}

func previousTarget(persisted persistedRevision, purpose domain.SecretPurpose) (domain.EncryptedSecret, string, string) {
	provider, baseURL := targetIdentity(persisted.settings, purpose)
	if purpose == domain.SecretPurposeChat {
		return persisted.chatSecret, provider, baseURL
	}
	return persisted.embeddingSecret, provider, baseURL
}

func targetIdentity(settings domain.Settings, purpose domain.SecretPurpose) (string, string) {
	if purpose == domain.SecretPurposeChat {
		return string(settings.Chat.Provider), settings.Chat.BaseURL
	}
	return string(settings.Embedding.Provider), settings.Embedding.BaseURL
}

func secretContext(revision int64, settings domain.Settings, purpose domain.SecretPurpose) domain.SecretContext {
	provider, baseURL := targetIdentity(settings, purpose)
	return domain.SecretContext{
		Revision: revision, Purpose: purpose, SchemaVersion: domain.SecretSchemaVersion,
		Provider: provider, BaseURL: baseURL,
	}
}

func validateSecretTargets(settings domain.Settings, chatAction, embeddingAction domain.SecretAction) error {
	if settings.Chat.Provider != domain.ChatProviderOpenAICompatible && chatAction.Kind == domain.SecretActionReplace {
		return invalid(errors.New("chat provider cannot store a secret"))
	}
	if settings.Embedding.Provider != domain.EmbeddingProviderOpenAICompatible && embeddingAction.Kind == domain.SecretActionReplace {
		return invalid(errors.New("embedding provider cannot store a secret"))
	}
	return nil
}

func canonicalActor(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func foundationRevisionMissing() error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeActiveRevisionUnavailable, false, errors.New("model settings revision does not exist"))
}
