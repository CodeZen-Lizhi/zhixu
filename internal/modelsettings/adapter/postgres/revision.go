package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
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

func loadRevision(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, revision int64) (persistedRevision, error) {
	if revision == 0 {
		return persistedRevision{revision: 0, settings: domain.CanonicalDisabledSettings()}, nil
	}
	if revision < 0 {
		return persistedRevision{}, invalid(errors.New("model settings revision is negative"))
	}
	persisted, err := scanRevision(queryer.QueryRow(ctx, `SELECT `+revisionColumns+`
FROM ops.model_settings_revisions WHERE revision=$1`, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRevision{}, foundationRevisionMissing()
	}
	if err != nil {
		return persistedRevision{}, classify(err)
	}
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

// SaveDesired appends one immutable revision and advances desired under the singleton row lock.
func (repository *Repository) SaveDesired(ctx context.Context, command application.SaveCommand) (domain.Snapshot, error) {
	if repository == nil || nilInterface(repository.db) || nilInterface(repository.sealer) || nilInterface(repository.audit) {
		return domain.Snapshot{}, unavailable(errors.New("model settings save boundary is unavailable"))
	}
	if ctx == nil || command.ExpectedRevision < 0 || command.Settings.ValidateStructural() != nil ||
		command.ChatSecret.Validate() != nil || command.EmbeddingSecret.Validate() != nil || !canonicalActor(command.CreatedBy) {
		return domain.Snapshot{}, invalid(errors.New("model settings save command is invalid"))
	}
	if err := validateSecretTargets(command.Settings, command.ChatSecret, command.EmbeddingSecret); err != nil {
		return domain.Snapshot{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Snapshot{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if state.phase != string(domain.RolloutPhaseIdle) && state.phase != string(domain.RolloutPhaseFailed) {
		return domain.Snapshot{}, rolloutInProgress(errors.New("model settings rollout is in progress"))
	}
	if state.desiredRevision != command.ExpectedRevision {
		return domain.Snapshot{}, revisionConflict(errors.New("model settings desired revision changed"))
	}
	previous, err := loadRevision(ctx, tx, state.desiredRevision)
	if err != nil {
		return domain.Snapshot{}, err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT nextval('ops.model_settings_revision_seq')`).Scan(&revision); err != nil {
		return domain.Snapshot{}, classify(err)
	}
	chatEnvelope, err := resolveEnvelope(repository.sealer, previous, revision, command.Settings, domain.SecretPurposeChat, command.ChatSecret)
	if err != nil {
		return domain.Snapshot{}, err
	}
	embeddingEnvelope, err := resolveEnvelope(repository.sealer, previous, revision, command.Settings, domain.SecretPurposeEmbedding, command.EmbeddingSecret)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO ops.model_settings_revisions(
revision,chat_provider,chat_api_style,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
chat_secret_key_id,chat_secret_nonce,chat_secret_ciphertext,
embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
embedding_max_response_bytes,embedding_secret_key_id,embedding_secret_nonce,embedding_secret_ciphertext,
created_at,created_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,clock_timestamp(),$28)
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
	).Scan(&revision); err != nil {
		return domain.Snapshot{}, classify(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=$1,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, revision); err != nil {
		return domain.Snapshot{}, classify(err)
	}
	if err := repository.audit.AppendModelSettingsChangeTx(ctx, tx, application.ModelSettingsChange{
		Action: application.ModelSettingsAuditActionUpdated, Revision: revision,
		ChatProvider: command.Settings.Chat.Provider, ChatAPIStyle: command.Settings.Chat.APIStyle, EmbeddingProvider: command.Settings.Embedding.Provider,
		ChatKeyConfigured: chatEnvelope.Configured(), EmbeddingKeyConfigured: embeddingEnvelope.Configured(),
	}); err != nil {
		return domain.Snapshot{}, err
	}
	snapshot, err := snapshotTx(ctx, tx, defaultSnapshotStaleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

// ResolveDraft resolves explicit secret actions without inserting a revision.
func (repository *Repository) ResolveDraft(ctx context.Context, command application.DraftCommand) (domain.ResolvedSettings, error) {
	if repository == nil || nilInterface(repository.db) || nilInterface(repository.sealer) {
		return domain.ResolvedSettings{}, unavailable(errors.New("model settings draft boundary is unavailable"))
	}
	if ctx == nil || command.ExpectedRevision < 0 || command.Settings.ValidateStructural() != nil ||
		command.ChatSecret.Validate() != nil || command.EmbeddingSecret.Validate() != nil {
		return domain.ResolvedSettings{}, invalid(errors.New("model settings draft command is invalid"))
	}
	if err := validateSecretTargets(command.Settings, command.ChatSecret, command.EmbeddingSecret); err != nil {
		return domain.ResolvedSettings{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.ResolvedSettings{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR SHARE`)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	if state.desiredRevision != command.ExpectedRevision {
		return domain.ResolvedSettings{}, revisionConflict(errors.New("model settings desired revision changed"))
	}
	previous, err := loadRevision(ctx, tx, state.desiredRevision)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	chatSecret, err := resolveDraftSecret(repository.sealer, previous, command.Settings, domain.SecretPurposeChat, command.ChatSecret)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	embeddingSecret, err := resolveDraftSecret(repository.sealer, previous, command.Settings, domain.SecretPurposeEmbedding, command.EmbeddingSecret)
	if err != nil {
		chatSecret.Destroy()
		return domain.ResolvedSettings{}, err
	}
	if err := commit(tx, ctx); err != nil {
		chatSecret.Destroy()
		embeddingSecret.Destroy()
		return domain.ResolvedSettings{}, err
	}
	return domain.ResolvedSettings{Revision: state.desiredRevision, Settings: command.Settings, ChatAPIKey: chatSecret, EmbeddingAPIKey: embeddingSecret}, nil
}

// LoadRevision decrypts one fixed immutable revision. Revision zero is canonical disabled.
func (repository *Repository) LoadRevision(ctx context.Context, revision int64) (domain.ResolvedSettings, error) {
	if repository == nil || nilInterface(repository.db) || nilInterface(repository.sealer) {
		return domain.ResolvedSettings{}, unavailable(errors.New("model settings revision boundary is unavailable"))
	}
	if ctx == nil || revision < 0 {
		return domain.ResolvedSettings{}, invalid(errors.New("model settings load revision command is invalid"))
	}
	persisted, err := loadRevision(ctx, repository.db, revision)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	return resolvePersisted(repository.sealer, persisted)
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
