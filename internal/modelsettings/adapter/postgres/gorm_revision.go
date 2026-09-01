package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"gorm.io/gorm"
)

func gormLoadRevision(ctx context.Context, database *gorm.DB, revision int64) (persistedRevision, error) {
	if revision == 0 {
		return persistedRevision{revision: 0, settings: domain.CanonicalDisabledSettings()}, nil
	}
	if revision < 0 {
		return persistedRevision{}, invalid(errors.New("model settings revision is negative"))
	}
	row, err := gormRawRow(ctx, database, `SELECT `+revisionColumns+`
FROM ops.model_settings_revisions WHERE revision=?`, revision)
	if err != nil {
		return persistedRevision{}, classifyGORM(ctx, err)
	}
	persisted, err := scanRevision(row)
	if gormNoRows(err) {
		return persistedRevision{}, foundationRevisionMissing()
	}
	if err != nil {
		return persistedRevision{}, classifyGORM(ctx, err)
	}
	return persisted, nil
}

// SaveDesired appends one immutable revision and advances desired under the singleton row lock.
func (repository *GORMRepository) SaveDesired(ctx context.Context, command application.SaveCommand) (snapshot domain.Snapshot, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.Snapshot{}, err
	}
	if command.ExpectedRevision < 0 || command.Settings.ValidateStructural() != nil ||
		command.ChatSecret.Validate() != nil || command.EmbeddingSecret.Validate() != nil || !canonicalActor(command.CreatedBy) {
		return domain.Snapshot{}, invalid(errors.New("model settings save command is invalid"))
	}
	if err = validateSecretTargets(command.Settings, command.ChatSecret, command.EmbeddingSecret); err != nil {
		return domain.Snapshot{}, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if state.phase != string(domain.RolloutPhaseIdle) && state.phase != string(domain.RolloutPhaseFailed) {
			return rolloutInProgress(errors.New("model settings rollout is in progress"))
		}
		if state.desiredRevision != command.ExpectedRevision {
			return revisionConflict(errors.New("model settings desired revision changed"))
		}
		previous, previousErr := gormLoadRevision(callbackCtx, database, state.desiredRevision)
		if previousErr != nil {
			return previousErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `SELECT nextval('ops.model_settings_revision_seq')`)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		var revision int64
		if rowErr = row.Scan(&revision); rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		chatEnvelope, envelopeErr := resolveEnvelope(repository.sealer, previous, revision, command.Settings, domain.SecretPurposeChat, command.ChatSecret)
		if envelopeErr != nil {
			return envelopeErr
		}
		embeddingEnvelope, envelopeErr := resolveEnvelope(repository.sealer, previous, revision, command.Settings, domain.SecretPurposeEmbedding, command.EmbeddingSecret)
		if envelopeErr != nil {
			return envelopeErr
		}
		row, rowErr = gormRawRow(callbackCtx, database, `INSERT INTO ops.model_settings_revisions(
revision,chat_provider,chat_api_style,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
chat_secret_key_id,chat_secret_nonce,chat_secret_ciphertext,
embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
embedding_max_response_bytes,embedding_secret_key_id,embedding_secret_nonce,embedding_secret_ciphertext,
created_at,created_by)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,clock_timestamp(),?)
RETURNING revision`,
			revision,
			string(command.Settings.Chat.Provider), string(command.Settings.Chat.APIStyle), command.Settings.Chat.BaseURL, command.Settings.Chat.Model,
			command.Settings.Chat.ModelVersion, command.Settings.Chat.AdapterVersion, command.Settings.Chat.Timeout.Microseconds(),
			command.Settings.Chat.MaxRequestBytes, command.Settings.Chat.MaxResponseBytes,
			nullString(chatEnvelope.KeyID), nullBytes(chatEnvelope.Nonce), nullBytes(chatEnvelope.Ciphertext),
			string(command.Settings.Embedding.Provider), command.Settings.Embedding.BaseURL, command.Settings.Embedding.Model,
			command.Settings.Embedding.Dimensions, string(command.Settings.Embedding.Normalization),
			string(command.Settings.Embedding.DistanceMetric), command.Settings.Embedding.MaxBatchSize,
			command.Settings.Embedding.MaxInputBytes, command.Settings.Embedding.MaxBatchInputBytes,
			command.Settings.Embedding.Timeout.Microseconds(), command.Settings.Embedding.MaxResponseBytes,
			nullString(embeddingEnvelope.KeyID), nullBytes(embeddingEnvelope.Nonce), nullBytes(embeddingEnvelope.Ciphertext),
			command.CreatedBy,
		)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		if rowErr = row.Scan(&revision); rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		result := database.WithContext(callbackCtx).Exec(`UPDATE ops.model_settings_state
SET desired_revision=?,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, revision)
		if result.Error != nil {
			return classifyGORM(callbackCtx, result.Error)
		}
		if appendErr := repository.audit.AppendModelSettingsChangeScoped(callbackCtx, scope, application.ModelSettingsChange{
			Action: application.ModelSettingsAuditActionUpdated, Revision: revision,
			ChatProvider: command.Settings.Chat.Provider, ChatAPIStyle: command.Settings.Chat.APIStyle, EmbeddingProvider: command.Settings.Embedding.Provider,
			ChatKeyConfigured: chatEnvelope.Configured(), EmbeddingKeyConfigured: embeddingEnvelope.Configured(),
		}); appendErr != nil {
			return appendErr
		}
		snapshot, rowErr = gormSnapshotTx(callbackCtx, database, defaultSnapshotStaleAfter)
		return rowErr
	})
	if err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

// ResolveDraft resolves explicit secret actions without inserting a revision.
func (repository *GORMRepository) ResolveDraft(ctx context.Context, command application.DraftCommand) (resolved domain.ResolvedSettings, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.ResolvedSettings{}, err
	}
	if command.ExpectedRevision < 0 || command.Settings.ValidateStructural() != nil ||
		command.ChatSecret.Validate() != nil || command.EmbeddingSecret.Validate() != nil {
		return domain.ResolvedSettings{}, invalid(errors.New("model settings draft command is invalid"))
	}
	if err = validateSecretTargets(command.Settings, command.ChatSecret, command.EmbeddingSecret); err != nil {
		return domain.ResolvedSettings{}, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR SHARE`)
		if stateErr != nil {
			return stateErr
		}
		if state.desiredRevision != command.ExpectedRevision {
			return revisionConflict(errors.New("model settings desired revision changed"))
		}
		previous, previousErr := gormLoadRevision(callbackCtx, database, state.desiredRevision)
		if previousErr != nil {
			return previousErr
		}
		chatSecret, secretErr := resolveDraftSecret(repository.sealer, previous, command.Settings, domain.SecretPurposeChat, command.ChatSecret)
		if secretErr != nil {
			return secretErr
		}
		embeddingSecret, secretErr := resolveDraftSecret(repository.sealer, previous, command.Settings, domain.SecretPurposeEmbedding, command.EmbeddingSecret)
		if secretErr != nil {
			chatSecret.Destroy()
			return secretErr
		}
		resolved = domain.ResolvedSettings{
			Revision: state.desiredRevision, Settings: command.Settings,
			ChatAPIKey: chatSecret, EmbeddingAPIKey: embeddingSecret,
		}
		return nil
	})
	if err != nil {
		resolved.ChatAPIKey.Destroy()
		resolved.EmbeddingAPIKey.Destroy()
		return domain.ResolvedSettings{}, err
	}
	return resolved, nil
}

// LoadRevision decrypts one fixed immutable revision. Revision zero is canonical disabled.
func (repository *GORMRepository) LoadRevision(ctx context.Context, revision int64) (domain.ResolvedSettings, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ResolvedSettings{}, err
	}
	if revision < 0 {
		return domain.ResolvedSettings{}, invalid(errors.New("model settings load revision command is invalid"))
	}
	persisted, err := gormLoadRevision(ctx, repository.database, revision)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	return resolvePersisted(repository.sealer, persisted)
}
