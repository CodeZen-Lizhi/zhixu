package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMSettingsAuditAppender adapts the Audit scoped appender to the
// deliberately redacted Model Settings contract.
type GORMSettingsAuditAppender struct {
	appender auditapplication.ScopedAppender
}

// NewGORMSettingsAuditAppender creates the opaque transaction adapter.
func NewGORMSettingsAuditAppender(appender auditapplication.ScopedAppender) (*GORMSettingsAuditAppender, error) {
	if nilInterface(appender) {
		return nil, unavailable(errors.New("model settings GORM audit appender is nil"))
	}
	return &GORMSettingsAuditAppender{appender: appender}, nil
}

// WithGORMAuditAppender adapts the shared Audit store at construction.
func WithGORMAuditAppender(appender auditapplication.ScopedAppender) GORMOption {
	settingsAppender, err := NewGORMSettingsAuditAppender(appender)
	return func(repository *GORMRepository) error {
		if err != nil {
			return err
		}
		return WithGORMScopedSettingsAuditAppender(settingsAppender)(repository)
	}
}

var _ application.ScopedSettingsAuditAppender = (*GORMSettingsAuditAppender)(nil)

// AppendModelSettingsChangeScoped verifies the revision in the caller's live
// transaction before appending one exact redacted Audit event in that scope.
func (appender *GORMSettingsAuditAppender) AppendModelSettingsChangeScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	change application.ModelSettingsChange,
) error {
	if appender == nil || nilInterface(appender.appender) {
		return unavailable(errors.New("model settings GORM audit boundary is unavailable"))
	}
	if ctx == nil || !validModelSettingsChange(change) {
		return invalid(errors.New("model settings audit change is invalid"))
	}
	database, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return unavailable(fmt.Errorf("model settings audit transaction is unavailable: %w", err))
	}
	createdBy, occurredAt, err := verifyPersistedGORMModelSettingsChange(ctx, database, change)
	if err != nil {
		return err
	}
	event, err := newModelSettingsAuditEvent(change, createdBy, occurredAt)
	if err != nil {
		return err
	}
	_, _, err = appender.appender.AppendScoped(ctx, scope, event)
	return err
}

func verifyPersistedGORMModelSettingsChange(
	ctx context.Context,
	database *gorm.DB,
	change application.ModelSettingsChange,
) (string, time.Time, error) {
	row, err := gormRawRow(ctx, database, `SELECT chat_provider,chat_api_style,embedding_provider,
chat_secret_key_id IS NOT NULL,embedding_secret_key_id IS NOT NULL,created_by,created_at
FROM ops.model_settings_revisions WHERE revision=?`, change.Revision)
	if err != nil {
		return "", time.Time{}, classifyGORM(ctx, err)
	}
	var (
		chatProvider, chatAPIStyle, embeddingProvider string
		chatConfigured, embeddingConfigured           bool
		createdBy                                     string
		createdAt                                     time.Time
	)
	err = row.Scan(
		&chatProvider, &chatAPIStyle, &embeddingProvider,
		&chatConfigured, &embeddingConfigured, &createdBy, &createdAt,
	)
	if gormNoRows(err) {
		return "", time.Time{}, corrupt(errors.New("model settings audit revision is missing"))
	}
	if err != nil {
		return "", time.Time{}, classifyGORM(ctx, err)
	}
	if chatProvider != string(change.ChatProvider) || chatAPIStyle != string(change.ChatAPIStyle) ||
		embeddingProvider != string(change.EmbeddingProvider) || chatConfigured != change.ChatKeyConfigured ||
		embeddingConfigured != change.EmbeddingKeyConfigured || !canonicalActor(createdBy) || createdAt.IsZero() {
		return "", time.Time{}, corrupt(errors.New("model settings audit revision binding is invalid"))
	}
	return createdBy, createdAt.UTC(), nil
}

func newModelSettingsAuditEvent(
	change application.ModelSettingsChange,
	createdBy string,
	occurredAt time.Time,
) (auditdomain.Event, error) {
	actorType, actorRef, err := modelSettingsAuditActor(createdBy)
	if err != nil {
		return auditdomain.Event{}, invalid(err)
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
		return auditdomain.Event{}, corrupt(errors.New("model settings audit metadata cannot be encoded"))
	}
	eventID, err := modelSettingsAuditID(change.Action, change.Revision)
	if err != nil {
		return auditdomain.Event{}, corrupt(err)
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
		return auditdomain.Event{}, invalid(errors.New("model settings audit event is invalid"))
	}
	return event, nil
}
