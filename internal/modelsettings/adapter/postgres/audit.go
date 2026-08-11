package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
)

const (
	modelSettingsAuditResourceType = "model_settings_revision"
	localDevelopmentActor          = "local-development"
)

// SettingsAuditAppender adapts the shared append-only Audit store to the redacted model-settings contract.
type SettingsAuditAppender struct{ appender auditapplication.Appender }

// NewSettingsAuditAppender creates the transaction-scoped model-settings Audit adapter.
func NewSettingsAuditAppender(appender auditapplication.Appender) (*SettingsAuditAppender, error) {
	if nilInterface(appender) {
		return nil, unavailable(errors.New("model settings audit appender is nil"))
	}
	return &SettingsAuditAppender{appender: appender}, nil
}

var _ application.SettingsAuditAppender = (*SettingsAuditAppender)(nil)

// AppendModelSettingsChangeTx verifies the persisted revision and appends one exact, redacted Audit event.
func (appender *SettingsAuditAppender) AppendModelSettingsChangeTx(ctx context.Context, transaction any, change application.ModelSettingsChange) error {
	if appender == nil || nilInterface(appender.appender) {
		return unavailable(errors.New("model settings audit boundary is unavailable"))
	}
	if ctx == nil || !validModelSettingsChange(change) {
		return invalid(errors.New("model settings audit change is invalid"))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || nilInterface(tx) {
		return unavailable(errors.New("model settings audit transaction is unavailable"))
	}
	createdBy, occurredAt, err := verifyPersistedModelSettingsChange(ctx, tx, change)
	if err != nil {
		return err
	}
	actorType, actorRef, err := modelSettingsAuditActor(createdBy)
	if err != nil {
		return invalid(err)
	}
	metadata, err := json.Marshal(struct {
		Revision               int64  `json:"revision"`
		ChatProvider           string `json:"chat_provider"`
		ChatAPIStyle           string `json:"chat_api_style"`
		ChatKeyConfigured      bool   `json:"chat_api_key_configured"`
		EmbeddingProvider      string `json:"embedding_provider"`
		EmbeddingKeyConfigured bool   `json:"embedding_api_key_configured"`
	}{
		Revision: change.Revision, ChatProvider: string(change.ChatProvider), ChatAPIStyle: string(change.ChatAPIStyle),
		ChatKeyConfigured: change.ChatKeyConfigured, EmbeddingProvider: string(change.EmbeddingProvider),
		EmbeddingKeyConfigured: change.EmbeddingKeyConfigured,
	})
	if err != nil {
		return corrupt(errors.New("model settings audit metadata cannot be encoded"))
	}
	eventID, err := modelSettingsAuditID(change.Action, change.Revision)
	if err != nil {
		return corrupt(err)
	}
	event, err := auditdomain.NewEvent(auditdomain.Event{
		ID: eventID, ActorType: actorType, ActorRef: actorRef,
		Action: string(change.Action), ResourceType: modelSettingsAuditResourceType,
		ResourceRef:    fmt.Sprintf("model_settings_revision:%d", change.Revision),
		Outcome:        auditdomain.OutcomeSucceeded,
		IdempotencyKey: fmt.Sprintf("model_settings.update:%d", change.Revision),
		Correlation:    json.RawMessage(`{}`), Metadata: metadata,
		SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt:    occurredAt.UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		return invalid(errors.New("model settings audit event is invalid"))
	}
	_, _, err = appender.appender.AppendTx(ctx, tx, event)
	return err
}

func verifyPersistedModelSettingsChange(ctx context.Context, tx pgx.Tx, change application.ModelSettingsChange) (string, time.Time, error) {
	var (
		chatProvider, chatAPIStyle, embeddingProvider string
		chatConfigured, embedConfigured               bool
		createdBy                                     string
		createdAt                                     time.Time
	)
	err := tx.QueryRow(ctx, `SELECT chat_provider,chat_api_style,embedding_provider,
chat_secret_key_id IS NOT NULL,embedding_secret_key_id IS NOT NULL,created_by,created_at
FROM ops.model_settings_revisions WHERE revision=$1`, change.Revision).Scan(
		&chatProvider, &chatAPIStyle, &embeddingProvider, &chatConfigured, &embedConfigured, &createdBy, &createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, corrupt(errors.New("model settings audit revision is missing"))
	}
	if err != nil {
		return "", time.Time{}, classify(err)
	}
	if chatProvider != string(change.ChatProvider) || chatAPIStyle != string(change.ChatAPIStyle) || embeddingProvider != string(change.EmbeddingProvider) ||
		chatConfigured != change.ChatKeyConfigured || embedConfigured != change.EmbeddingKeyConfigured ||
		!canonicalActor(createdBy) || createdAt.IsZero() {
		return "", time.Time{}, corrupt(errors.New("model settings audit revision binding is invalid"))
	}
	return createdBy, createdAt.UTC(), nil
}

func validModelSettingsChange(change application.ModelSettingsChange) bool {
	if change.Action != application.ModelSettingsAuditActionUpdated || change.Revision <= 0 {
		return false
	}
	if change.ChatProvider != domain.ChatProviderDisabled && change.ChatProvider != domain.ChatProviderOpenAICompatible {
		return false
	}
	if change.ChatAPIStyle != domain.ChatAPIStyleChatCompletions && change.ChatAPIStyle != domain.ChatAPIStyleResponses {
		return false
	}
	switch change.EmbeddingProvider {
	case domain.EmbeddingProviderDisabled, domain.EmbeddingProviderOpenAICompatible, domain.EmbeddingProviderOllama:
	default:
		return false
	}
	return (!change.ChatKeyConfigured || change.ChatProvider == domain.ChatProviderOpenAICompatible) &&
		(!change.EmbeddingKeyConfigured || change.EmbeddingProvider == domain.EmbeddingProviderOpenAICompatible)
}

func modelSettingsAuditID(action application.ModelSettingsAuditAction, revision int64) (foundation.ID, error) {
	digest := sha256.Sum256([]byte("model-settings-audit/v1\x00" + string(action) + "\x00" + strconv.FormatInt(revision, 10)))
	raw := digest[:16]
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	return foundation.ParseID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32])
}

func modelSettingsAuditActor(createdBy string) (auditdomain.ActorType, string, error) {
	if createdBy == localDevelopmentActor {
		return auditdomain.ActorAnonymous, localDevelopmentActor, nil
	}
	if strings.HasPrefix(createdBy, "SESSION:") {
		id, err := foundation.ParseID(strings.TrimPrefix(createdBy, "SESSION:"))
		if err != nil {
			return "", "", errors.New("model settings session actor is invalid")
		}
		return auditdomain.ActorUser, string(id), nil
	}
	return auditdomain.ActorSystem, createdBy, nil
}
