package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestNewGORMRepositoryRequiresSecretAndAuditBoundaries(t *testing.T) {
	// Pool construction is lazy; the constructor must not issue any query.
	pool, err := platformpostgres.Open(context.Background(), "postgres://constructor@127.0.0.1:1/modelsettings_constructor?sslmode=disable&pool_min_conns=0", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = NewGORMRepository(pool)
	assertModelSettingsUnitErrorCode(t, err, domain.ErrorCodeUnavailable)
	repository, err := NewGORMRepository(pool, WithGORMSecretSealer(modelSettingsFormatSealer{}), WithGORMAuditAppender(noopAuditAppender{}))
	if err != nil {
		t.Fatal(err)
	}
	if repository == nil {
		t.Fatal("repository is nil")
	}
	_, err = NewGORMRepository(pool,
		WithGORMSecretSealer(modelSettingsFormatSealer{}), WithGORMSecretSealer(modelSettingsFormatSealer{}), WithGORMAuditAppender(noopAuditAppender{}),
	)
	assertModelSettingsUnitErrorCode(t, err, domain.ErrorCodeInvalid)
}

func TestRepositoryFormattingDoesNotExpandSecretDependencies(t *testing.T) {
	const canary = "repository-format-secret-canary"
	repository := GORMRepository{
		sealer: modelSettingsFormatSealer{canary: canary},
		audit:  modelSettingsFormatAudit{canary: canary},
	}
	formatted := fmt.Sprintf("%+v %#v", repository, repository)
	if strings.Contains(formatted, canary) {
		t.Fatalf("repository formatting expanded private dependencies: %q", formatted)
	}
}

func TestModelSettingsAuditIDIsStableAndRevisionBound(t *testing.T) {
	first, err := modelSettingsAuditID(application.ModelSettingsAuditActionUpdated, 7)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := modelSettingsAuditID(application.ModelSettingsAuditActionUpdated, 7)
	if err != nil {
		t.Fatal(err)
	}
	otherRevision, err := modelSettingsAuditID(application.ModelSettingsAuditActionUpdated, 8)
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated || first == otherRevision {
		t.Fatalf("audit IDs first=%q repeated=%q other=%q", first, repeated, otherRevision)
	}
	if parsed, err := foundation.ParseID(string(first)); err != nil || parsed != first {
		t.Fatalf("audit ID is not canonical: %q: %v", first, err)
	}
}

func TestModelSettingsAuditActorMapping(t *testing.T) {
	tests := []struct {
		createdBy string
		wantType  auditdomain.ActorType
		wantRef   string
		wantError bool
	}{
		{createdBy: localDevelopmentActor, wantType: auditdomain.ActorAnonymous, wantRef: localDevelopmentActor},
		{createdBy: "integration-test", wantType: auditdomain.ActorSystem, wantRef: "integration-test"},
		{createdBy: "SESSION:10000000-0000-4000-8000-000000000001", wantType: auditdomain.ActorUser, wantRef: "10000000-0000-4000-8000-000000000001"},
		{createdBy: "SESSION:not-an-id", wantError: true},
	}
	for _, test := range tests {
		gotType, gotRef, err := modelSettingsAuditActor(test.createdBy)
		if test.wantError != (err != nil) {
			t.Fatalf("actor %q error=%v", test.createdBy, err)
		}
		if !test.wantError && (gotType != test.wantType || gotRef != test.wantRef) {
			t.Fatalf("actor %q=(%q,%q), want (%q,%q)", test.createdBy, gotType, gotRef, test.wantType, test.wantRef)
		}
	}
}

func TestValidModelSettingsChangeRejectsSecretProviderMismatch(t *testing.T) {
	valid := application.ModelSettingsChange{
		Action: application.ModelSettingsAuditActionUpdated, Revision: 1,
		ChatProvider: domain.ChatProviderOpenAICompatible, ChatAPIStyle: domain.ChatAPIStyleChatCompletions, ChatKeyConfigured: true,
		EmbeddingProvider: domain.EmbeddingProviderOllama,
	}
	if !validModelSettingsChange(valid) {
		t.Fatal("valid model settings change was rejected")
	}
	invalidChat := valid
	invalidChat.ChatProvider = domain.ChatProviderDisabled
	if validModelSettingsChange(invalidChat) {
		t.Fatal("disabled chat provider accepted a configured key")
	}
	ollamaChat := valid
	ollamaChat.ChatProvider = domain.ChatProviderOllama
	ollamaChat.ChatAPIStyle = domain.ChatAPIStyleChatCompletions
	ollamaChat.ChatKeyConfigured = false
	if !validModelSettingsChange(ollamaChat) {
		t.Fatal("ollama chat provider was rejected")
	}
	ollamaChat.ChatKeyConfigured = true
	if validModelSettingsChange(ollamaChat) {
		t.Fatal("ollama chat provider accepted a configured key")
	}
	invalidEmbedding := valid
	invalidEmbedding.EmbeddingKeyConfigured = true
	if validModelSettingsChange(invalidEmbedding) {
		t.Fatal("ollama embedding provider accepted a configured key")
	}
	invalidAction := valid
	invalidAction.Action = "model_settings.apply"
	if validModelSettingsChange(invalidAction) {
		t.Fatal("unknown audit action was accepted")
	}
}

func TestSettingsAuditAppenderRequiresTransactionScope(t *testing.T) {
	adapter, err := NewGORMSettingsAuditAppender(noopAuditAppender{})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.AppendModelSettingsChangeScoped(context.Background(), nil, application.ModelSettingsChange{
		Action: application.ModelSettingsAuditActionUpdated, Revision: 1,
		ChatProvider: domain.ChatProviderDisabled, ChatAPIStyle: domain.ChatAPIStyleChatCompletions, EmbeddingProvider: domain.EmbeddingProviderDisabled,
	})
	assertModelSettingsUnitErrorCode(t, err, domain.ErrorCodeUnavailable)
}

type noopAuditAppender struct{}

func (noopAuditAppender) AppendScoped(context.Context, foundation.TransactionScope, auditdomain.Event) (auditdomain.Event, bool, error) {
	return auditdomain.Event{}, false, nil
}

type modelSettingsFormatSealer struct{ canary string }

func (modelSettingsFormatSealer) Seal(domain.Secret, domain.SecretContext) (domain.EncryptedSecret, error) {
	return domain.EncryptedSecret{}, nil
}

func (modelSettingsFormatSealer) Open(domain.EncryptedSecret, domain.SecretContext) (domain.Secret, error) {
	return domain.Secret{}, nil
}

type modelSettingsFormatAudit struct{ canary string }

func (modelSettingsFormatAudit) AppendModelSettingsChangeScoped(context.Context, foundation.TransactionScope, application.ModelSettingsChange) error {
	return nil
}

func assertModelSettingsUnitErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
