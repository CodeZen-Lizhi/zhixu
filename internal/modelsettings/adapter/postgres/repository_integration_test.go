//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryRevisionRolloutRuntimeAndEnqueueFence(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newModelSettingsTestDatabase(t, ctx)
	defer cleanup()
	repository, _, auditStore := newConfiguredModelSettingsRepository(t, pool, 7)

	snapshot, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DesiredRevision != 0 || snapshot.ActiveRevision != 0 || !snapshot.RestartRequired ||
		snapshot.ChatCapability != domain.CapabilityDisabled || snapshot.EmbeddingCapability != domain.CapabilityDisabled {
		t.Fatalf("unexpected bootstrap snapshot: %+v", snapshot)
	}
	registerActiveRuntimes(t, ctx, repository, 0)
	snapshot, err = repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RestartRequired {
		t.Fatalf("revision zero runtimes should be ready: %+v", snapshot.Runtime)
	}

	settings := configuredTestSettings()
	chatAction, err := domain.ReplaceSecret("chat-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	defer chatAction.Value.Destroy()
	snapshot, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 0, Settings: settings, ChatSecret: chatAction,
		EmbeddingSecret: domain.ClearSecret(), CreatedBy: "integration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DesiredRevision != 1 || snapshot.ActiveRevision != 0 || !snapshot.RestartRequired {
		t.Fatalf("first save snapshot desired=%d active=%d restart=%t", snapshot.DesiredRevision, snapshot.ActiveRevision, snapshot.RestartRequired)
	}
	firstNonce, firstCiphertext := storedChatEnvelope(t, ctx, pool, 1)
	if bytes.Contains(firstCiphertext, []byte("chat-secret-value")) {
		t.Fatal("stored ciphertext contains plaintext")
	}

	settings.Chat.ModelVersion = "2026-08"
	settings.Chat.APIStyle = domain.ChatAPIStyleResponses
	snapshot, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 1, Settings: settings, ChatSecret: domain.KeepSecret(),
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "integration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DesiredRevision != 2 {
		t.Fatalf("keep save revision=%d want=2", snapshot.DesiredRevision)
	}
	assertModelSettingsAudit(t, ctx, auditStore)
	secondNonce, secondCiphertext := storedChatEnvelope(t, ctx, pool, 2)
	if bytes.Equal(firstNonce, secondNonce) || bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("keep did not re-encrypt with a fresh nonce and revision AAD")
	}
	resolved, err := repository.LoadRevision(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resolved.ChatAPIKey.Bytes()); got != "chat-secret-value" {
		t.Fatalf("resolved chat secret=%q", got)
	}
	if resolved.Settings.Chat.APIStyle != domain.ChatAPIStyleResponses {
		t.Fatalf("resolved chat API style=%q", resolved.Settings.Chat.APIStyle)
	}
	resolved.ChatAPIKey.Destroy()
	resolved.EmbeddingAPIKey.Destroy()

	changedTarget := settings
	changedTarget.Chat.BaseURL = "https://other.example.test/v1"
	_, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 2, Settings: changedTarget, ChatSecret: domain.KeepSecret(),
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "integration-test",
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeSecretTargetChanged)
	_, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 1, Settings: settings, ChatSecret: domain.KeepSecret(),
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "integration-test",
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRevisionConflict)

	rolloutID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000001")
	rollout, err := repository.BeginRollout(ctx, application.BeginRolloutCommand{RolloutID: rolloutID, LeaseDuration: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rollout.Phase != domain.RolloutPhaseValidating || rollout.TargetRevision != 2 || rollout.PreviousActiveRevision != 0 {
		t.Fatalf("unexpected begun rollout: %s", rollout)
	}
	assertEnqueueAllowed(t, ctx, pool, repository)
	registerActiveRuntimes(t, ctx, repository, 0)
	_, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 2, Settings: settings, ChatSecret: domain.KeepSecret(),
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "integration-test",
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRolloutInProgress)

	rollout, err = repository.AdvanceRollout(ctx, application.AdvanceRolloutCommand{
		RolloutID: rolloutID, ExpectedPhase: domain.RolloutPhaseValidating,
		NextPhase: domain.RolloutPhaseDraining, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEnqueueBlocked(t, ctx, pool, repository)
	for _, runtime := range []struct {
		role       domain.RuntimeRole
		instanceID foundation.ID
	}{
		{domain.RuntimeRoleAPI, mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000001")},
		{domain.RuntimeRoleWorker, mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000002")},
	} {
		if _, err := repository.SetRuntimePhase(ctx, application.RuntimePhaseCommand{
			Role: runtime.role, InstanceID: runtime.instanceID, RolloutID: &rolloutID,
			ExpectedPhase: domain.RuntimePhaseActive, NextPhase: domain.RuntimePhaseQuiescing,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.SetRuntimePhase(ctx, application.RuntimePhaseCommand{
			Role: runtime.role, InstanceID: runtime.instanceID, RolloutID: &rolloutID,
			ExpectedPhase: domain.RuntimePhaseQuiescing, NextPhase: domain.RuntimePhaseQuiesced,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rollout, err = repository.AdvanceRollout(ctx, application.AdvanceRolloutCommand{
		RolloutID: rolloutID, ExpectedPhase: domain.RolloutPhaseDraining,
		NextPhase: domain.RolloutPhaseApplying, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	registerCandidateRuntimes(t, ctx, repository, rolloutID, 2)
	rollout, err = repository.AdvanceRollout(ctx, application.AdvanceRolloutCommand{
		RolloutID: rolloutID, ExpectedPhase: domain.RolloutPhaseApplying,
		NextPhase: domain.RolloutPhaseVerifying, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	rollout, err = repository.CommitRollout(ctx, application.CommitRolloutCommand{RolloutID: rolloutID, FreshWithin: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rollout.Phase != domain.RolloutPhaseIdle {
		t.Fatalf("committed rollout phase=%q", rollout.Phase)
	}
	snapshot, err = repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveRevision != 2 || snapshot.DesiredRevision != 2 || snapshot.RestartRequired ||
		snapshot.ChatCapability != domain.CapabilityConfigured || snapshot.EmbeddingCapability != domain.CapabilityConfigured {
		t.Fatalf("unexpected committed snapshot: %+v", snapshot)
	}
	assertEnqueueAllowed(t, ctx, pool, repository)

	abortID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000002")
	if _, err := repository.BeginRollout(ctx, application.BeginRolloutCommand{RolloutID: abortID, LeaseDuration: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceRollout(ctx, application.AdvanceRolloutCommand{
		RolloutID: abortID, ExpectedPhase: domain.RolloutPhaseValidating,
		NextPhase: domain.RolloutPhaseDraining, LeaseDuration: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []struct {
		role       domain.RuntimeRole
		instanceID foundation.ID
	}{
		{domain.RuntimeRoleAPI, mustModelSettingsID(t, "a3000000-0000-4000-8000-000000000001")},
		{domain.RuntimeRoleWorker, mustModelSettingsID(t, "a3000000-0000-4000-8000-000000000002")},
	} {
		if _, err := repository.SetRuntimePhase(ctx, application.RuntimePhaseCommand{
			Role: runtime.role, InstanceID: runtime.instanceID, RolloutID: &abortID,
			ExpectedPhase: domain.RuntimePhaseActive, NextPhase: domain.RuntimePhaseQuiescing,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.FailRollout(ctx, application.FailRolloutCommand{RolloutID: abortID, ErrorCode: "MODEL_SETTINGS_TEST_ABORTED"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rollout.Phase != domain.RolloutPhaseFailed || snapshot.Runtime.API.Phase != domain.RuntimePhaseActive ||
		snapshot.Runtime.Worker.Phase != domain.RuntimePhaseActive || snapshot.ActiveRevision != 2 {
		t.Fatalf("abort did not restore previous runtime ownership: %+v", snapshot)
	}
	assertEnqueueAllowed(t, ctx, pool, repository)

	expiredID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000003")
	if _, err := repository.BeginRollout(ctx, application.BeginRolloutCommand{RolloutID: expiredID, LeaseDuration: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()-interval '1 second',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	_, err = repository.RenewRollout(ctx, application.RenewRolloutCommand{
		RolloutID: expiredID, ExpectedPhase: domain.RolloutPhaseValidating, LeaseDuration: 30 * time.Second,
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRolloutLeaseExpired)
	recoveredState, recovered, err := repository.RecoverExpiredRollout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered || recoveredState.Phase != domain.RolloutPhaseFailed || recoveredState.LastErrorCode != domain.ErrorCodeRolloutLeaseExpired {
		t.Fatalf("expired recovery state=%s recovered=%t", recoveredState, recovered)
	}
	assertEnqueueAllowed(t, ctx, pool, repository)
}

func TestRepositoryCommitRolloutRequiresPreparedRuntimes(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newModelSettingsTestDatabase(t, ctx)
	defer cleanup()
	repository, _, _ := newConfiguredModelSettingsRepository(t, pool, 14)

	if _, err := repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 0,
		Settings:         domain.CanonicalDisabledSettings(),
		ChatSecret:       domain.KeepSecret(),
		EmbeddingSecret:  domain.KeepSecret(),
		CreatedBy:        "commit-guard-test",
	}); err != nil {
		t.Fatal(err)
	}
	rolloutID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000014")
	if _, err := repository.BeginRollout(ctx, application.BeginRolloutCommand{
		RolloutID: rolloutID, LeaseDuration: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	for _, transition := range []struct {
		from domain.RolloutPhase
		to   domain.RolloutPhase
	}{
		{from: domain.RolloutPhaseValidating, to: domain.RolloutPhaseDraining},
		{from: domain.RolloutPhaseDraining, to: domain.RolloutPhaseApplying},
		{from: domain.RolloutPhaseApplying, to: domain.RolloutPhaseVerifying},
	} {
		if _, err := repository.AdvanceRollout(ctx, application.AdvanceRolloutCommand{
			RolloutID: rolloutID, ExpectedPhase: transition.from,
			NextPhase: transition.to, LeaseDuration: 30 * time.Second,
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := repository.CommitRollout(ctx, application.CommitRolloutCommand{
		RolloutID: rolloutID, FreshWithin: 20 * time.Second,
	})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeNotPrepared)
	snapshot, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveRevision != 0 || snapshot.DesiredRevision != 1 || snapshot.Rollout.Phase != domain.RolloutPhaseVerifying {
		t.Fatalf("failed commit changed publication state: %+v", snapshot)
	}
}

func assertModelSettingsAudit(t *testing.T, ctx context.Context, store *auditpostgres.Store) {
	t.Helper()
	events, err := store.List(ctx, auditdomain.ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("model settings audit count=%d want=2", len(events))
	}
	latest := events[0]
	if latest.WorkspaceID != nil || latest.ActorType != auditdomain.ActorSystem || latest.ActorRef != "integration-test" ||
		latest.Action != string(application.ModelSettingsAuditActionUpdated) || latest.ResourceType != modelSettingsAuditResourceType ||
		latest.ResourceRef != "model_settings_revision:2" || latest.IdempotencyKey != "model_settings.update:2" ||
		latest.Outcome != auditdomain.OutcomeSucceeded {
		t.Fatalf("unexpected model settings audit event: %s", latest)
	}
	var metadata map[string]any
	if err := json.Unmarshal(latest.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 6 || metadata["revision"] != float64(2) || metadata["chat_provider"] != "openai-compatible" ||
		metadata["chat_api_style"] != "responses" ||
		metadata["chat_api_key_configured"] != true || metadata["embedding_provider"] != "ollama" ||
		metadata["embedding_api_key_configured"] != false {
		t.Fatalf("unexpected model settings audit metadata: %s", latest.Metadata)
	}
	unsafe := []string{"models.example.test", "chat-model", "chat-secret-value", "embed-model"}
	for _, value := range unsafe {
		if bytes.Contains(latest.Metadata, []byte(value)) {
			t.Fatalf("audit metadata contains unsafe model detail %q", value)
		}
	}
}

func TestRepositoryConcurrentDesiredSaveUsesExpectedRevision(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newModelSettingsTestDatabase(t, ctx)
	defer cleanup()
	repository, _, auditStore := newConfiguredModelSettingsRepository(t, pool, 8)
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, saveErr := repository.SaveDesired(ctx, application.SaveCommand{
				ExpectedRevision: 0, Settings: domain.CanonicalDisabledSettings(),
				ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.KeepSecret(), CreatedBy: "concurrent-test",
			})
			results <- saveErr
		}()
	}
	close(start)
	succeeded, conflicted := 0, 0
	for index := 0; index < 2; index++ {
		saveErr := <-results
		if saveErr == nil {
			succeeded++
			continue
		}
		var classified *foundation.Error
		if errors.As(saveErr, &classified) && classified.Code == domain.ErrorCodeRevisionConflict {
			conflicted++
			continue
		}
		t.Fatalf("unexpected concurrent save error: %v", saveErr)
	}
	var revisionCount, desiredRevision int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.model_settings_revisions`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT desired_revision FROM ops.model_settings_state WHERE singleton=true`).Scan(&desiredRevision); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || conflicted != 1 || revisionCount != 1 || desiredRevision != 1 {
		t.Fatalf("concurrent save succeeded=%d conflicted=%d revisions=%d desired=%d", succeeded, conflicted, revisionCount, desiredRevision)
	}
	events, err := auditStore.List(ctx, auditdomain.ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ResourceRef != "model_settings_revision:1" {
		t.Fatalf("concurrent save audit events=%v", events)
	}
}

func TestRepositoryEnqueueFenceSerializesDraining(t *testing.T) {
	pool, cleanup := newModelSettingsTestDatabase(t, context.Background())
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repository, _, _ := newConfiguredModelSettingsRepository(t, pool, 11)
	rolloutID := mustModelSettingsID(t, "a1000000-0000-4000-8000-000000000010")
	if _, err := repository.BeginRollout(ctx, application.BeginRolloutCommand{RolloutID: rolloutID, LeaseDuration: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if err := repository.CheckEnqueue(ctx, tx); err != nil {
		t.Fatalf("validating should continue serving the old active runtime: %v", err)
	}
	advanced := make(chan error, 1)
	go func() {
		_, advanceErr := repository.AdvanceRollout(ctx, application.AdvanceRolloutCommand{
			RolloutID: rolloutID, ExpectedPhase: domain.RolloutPhaseValidating,
			NextPhase: domain.RolloutPhaseDraining, LeaseDuration: 30 * time.Second,
		})
		advanced <- advanceErr
	}()
	select {
	case advanceErr := <-advanced:
		t.Fatalf("draining advanced before the fenced producer transaction completed: %v", advanceErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case advanceErr := <-advanced:
		if advanceErr != nil {
			t.Fatal(advanceErr)
		}
	case <-ctx.Done():
		t.Fatalf("draining did not acquire the state fence: %v", ctx.Err())
	}
	assertEnqueueBlocked(t, ctx, pool, repository)
}

func TestRepositoryRuntimeOwnershipAndStaleness(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newModelSettingsTestDatabase(t, ctx)
	defer cleanup()
	repository, _, _ := newConfiguredModelSettingsRepository(t, pool, 12)
	oldAPI := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000010")
	newAPI := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000011")
	worker := mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000012")
	for _, registration := range []application.RuntimeRegistration{
		{Role: domain.RuntimeRoleAPI, InstanceID: oldAPI, AppliedRevision: 0, Phase: domain.RuntimePhaseActive},
		{Role: domain.RuntimeRoleWorker, InstanceID: worker, AppliedRevision: 0, Phase: domain.RuntimePhaseActive},
		{Role: domain.RuntimeRoleAPI, InstanceID: newAPI, AppliedRevision: 0, Phase: domain.RuntimePhaseActive},
	} {
		if _, err := repository.RegisterRuntime(ctx, registration); err != nil {
			t.Fatal(err)
		}
	}
	_, err := repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{Role: domain.RuntimeRoleAPI, InstanceID: oldAPI})
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeRuntimeConflict)
	time.Sleep(1100 * time.Millisecond)
	snapshot, err := repository.Snapshot(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runtime.API.Fresh || snapshot.Runtime.Worker.Fresh || !snapshot.RestartRequired {
		t.Fatalf("stale runtime snapshot was reported ready: %+v", snapshot.Runtime)
	}
	if _, err := repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{Role: domain.RuntimeRoleAPI, InstanceID: newAPI}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.HeartbeatRuntime(ctx, application.RuntimeHeartbeat{Role: domain.RuntimeRoleWorker, InstanceID: worker}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repository.Snapshot(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Runtime.API.Fresh || !snapshot.Runtime.Worker.Fresh || snapshot.RestartRequired {
		t.Fatalf("current runtime owners were not reported ready: %+v", snapshot.Runtime)
	}
}

func TestRepositoryWrongKeyAndCiphertextSubstitutionFailClosed(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newModelSettingsTestDatabase(t, ctx)
	defer cleanup()
	repository, _, auditStore := newConfiguredModelSettingsRepository(t, pool, 9)
	chatAction, err := domain.ReplaceSecret("database-secret-canary")
	if err != nil {
		t.Fatal(err)
	}
	defer chatAction.Value.Destroy()
	settings := domain.CanonicalDisabledSettings()
	settings.Chat.Provider = domain.ChatProviderOpenAICompatible
	settings.Chat.BaseURL = "https://models.example.test/v1"
	settings.Chat.Model = "chat-model"
	settings.Chat.ModelVersion = "2026-07"
	if _, err := repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 0, Settings: settings, ChatSecret: chatAction,
		EmbeddingSecret: domain.KeepSecret(), CreatedBy: "tamper-test",
	}); err != nil {
		t.Fatal(err)
	}
	wrong, err := modelcrypto.NewSealer(bytes.Repeat([]byte{10}, 32))
	if err != nil {
		t.Fatal(err)
	}
	wrongRepository, err := NewRepository(pool, WithSecretSealer(wrong), WithAuditAppender(auditStore))
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrongRepository.LoadRevision(ctx, 1)
	assertSecretFailureWithoutCanary(t, err)
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_revisions
	SET chat_secret_ciphertext=set_byte(chat_secret_ciphertext,0,get_byte(chat_secret_ciphertext,0)#1)
	WHERE revision=1`); err == nil {
		t.Fatal("append-only revision allowed ciphertext mutation")
	}
	_, err = pool.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
	revision,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
	chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
	chat_secret_key_id,chat_secret_nonce,chat_secret_ciphertext,
	embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
	embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
	embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
	embedding_max_response_bytes,embedding_secret_key_id,embedding_secret_nonce,embedding_secret_ciphertext,
	created_at,created_by)
	SELECT 2,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
	chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
	chat_secret_key_id,chat_secret_nonce,chat_secret_ciphertext,
	embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
	embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
	embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
	embedding_max_response_bytes,embedding_secret_key_id,embedding_secret_nonce,embedding_secret_ciphertext,
	clock_timestamp(),'tamper-test' FROM ops.model_settings_revisions WHERE revision=1`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.LoadRevision(ctx, 2)
	assertSecretFailureWithoutCanary(t, err)
	var ciphertext []byte
	var leaked bool
	if err := pool.QueryRow(ctx, `SELECT chat_secret_ciphertext,
	strpos(concat_ws('|',chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
	embedding_provider,embedding_base_url,embedding_model,embedding_normalization,embedding_distance_metric,created_by),$1)>0
	FROM ops.model_settings_revisions WHERE revision=1`, "database-secret-canary").Scan(&ciphertext, &leaked); err != nil {
		t.Fatal(err)
	}
	if leaked || bytes.Contains(ciphertext, []byte("database-secret-canary")) {
		t.Fatal("model settings persistence leaked plaintext credential")
	}
}

func TestRepositoryAuditFailureRollsBackRevisionStateAndAudit(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newModelSettingsTestDatabase(t, ctx)
	defer cleanup()
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{13}, 32))
	if err != nil {
		t.Fatal(err)
	}
	auditStore, err := auditpostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	auditAdapter, err := NewSettingsAuditAppender(auditStore)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected post-audit failure")
	repository, err := NewRepository(pool,
		WithSecretSealer(sealer),
		WithSettingsAuditAppender(failAfterSettingsAuditAppender{delegate: auditAdapter, err: injected}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.SaveDesired(ctx, application.SaveCommand{
		ExpectedRevision: 0, Settings: domain.CanonicalDisabledSettings(),
		ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.KeepSecret(), CreatedBy: "rollback-test",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("save error=%v want injected audit failure", err)
	}
	var revisionCount, desiredRevision, stateVersion, auditCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.model_settings_revisions`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT desired_revision,version FROM ops.model_settings_state WHERE singleton=true`).Scan(&desiredRevision, &stateVersion); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event WHERE action=$1`, string(application.ModelSettingsAuditActionUpdated)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 0 || desiredRevision != 0 || stateVersion != 1 || auditCount != 0 {
		t.Fatalf("failed save persisted revisions=%d desired=%d state_version=%d audit=%d", revisionCount, desiredRevision, stateVersion, auditCount)
	}
}

type failAfterSettingsAuditAppender struct {
	delegate application.SettingsAuditAppender
	err      error
}

func (appender failAfterSettingsAuditAppender) AppendModelSettingsChangeTx(ctx context.Context, transaction any, change application.ModelSettingsChange) error {
	if err := appender.delegate.AppendModelSettingsChangeTx(ctx, transaction, change); err != nil {
		return err
	}
	return appender.err
}

func registerActiveRuntimes(t *testing.T, ctx context.Context, repository *Repository, revision int64) {
	t.Helper()
	for _, registration := range []application.RuntimeRegistration{
		{Role: domain.RuntimeRoleAPI, InstanceID: mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000001"), AppliedRevision: revision, Phase: domain.RuntimePhaseActive},
		{Role: domain.RuntimeRoleWorker, InstanceID: mustModelSettingsID(t, "a2000000-0000-4000-8000-000000000002"), AppliedRevision: revision, Phase: domain.RuntimePhaseActive},
	} {
		if _, err := repository.RegisterRuntime(ctx, registration); err != nil {
			t.Fatal(err)
		}
	}
}

func registerCandidateRuntimes(t *testing.T, ctx context.Context, repository *Repository, rolloutID foundation.ID, revision int64) {
	t.Helper()
	for _, registration := range []application.RuntimeRegistration{
		{Role: domain.RuntimeRoleAPI, InstanceID: mustModelSettingsID(t, "a3000000-0000-4000-8000-000000000001"), AppliedRevision: revision, RolloutID: &rolloutID, Phase: domain.RuntimePhasePrepared},
		{Role: domain.RuntimeRoleWorker, InstanceID: mustModelSettingsID(t, "a3000000-0000-4000-8000-000000000002"), AppliedRevision: revision, RolloutID: &rolloutID, Phase: domain.RuntimePhasePrepared},
	} {
		if _, err := repository.RegisterRuntime(ctx, registration); err != nil {
			t.Fatal(err)
		}
	}
}

func configuredTestSettings() domain.Settings {
	settings := domain.CanonicalDisabledSettings()
	settings.Chat.Provider = domain.ChatProviderOpenAICompatible
	settings.Chat.BaseURL = "https://models.example.test/v1"
	settings.Chat.Model = "chat-model"
	settings.Chat.ModelVersion = "2026-07"
	settings.Embedding.Provider = domain.EmbeddingProviderOllama
	settings.Embedding.BaseURL = "http://127.0.0.1:11434"
	settings.Embedding.Model = "embed-model"
	settings.Embedding.Dimensions = 768
	return settings
}

func storedChatEnvelope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, revision int64) ([]byte, []byte) {
	t.Helper()
	var nonce, ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT chat_secret_nonce,chat_secret_ciphertext
FROM ops.model_settings_revisions WHERE revision=$1`, revision).Scan(&nonce, &ciphertext); err != nil {
		t.Fatal(err)
	}
	return nonce, ciphertext
}

func assertEnqueueBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repository *Repository) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	assertModelSettingsErrorCode(t, repository.CheckEnqueue(ctx, tx), domain.ErrorCodeEnqueuePaused)
}

func assertEnqueueAllowed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repository *Repository) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := repository.CheckEnqueue(ctx, tx); err != nil {
		t.Fatal(err)
	}
}

func assertModelSettingsErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}

func assertSecretFailureWithoutCanary(t *testing.T, err error) {
	t.Helper()
	assertModelSettingsErrorCode(t, err, domain.ErrorCodeSecretUnavailable)
	if strings.Contains(err.Error(), "database-secret-canary") {
		t.Fatalf("secret error leaked plaintext: %v", err)
	}
}

func newConfiguredModelSettingsRepository(t *testing.T, pool *pgxpool.Pool, keyByte byte) (*Repository, *modelcrypto.Sealer, *auditpostgres.Store) {
	t.Helper()
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{keyByte}, 32))
	if err != nil {
		t.Fatal(err)
	}
	auditStore, err := auditpostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool, WithSecretSealer(sealer), WithAuditAppender(auditStore))
	if err != nil {
		t.Fatal(err)
	}
	return repository, sealer, auditStore
}

func mustModelSettingsID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newModelSettingsTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for model settings repository integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_model_settings_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	runner, err := migration.NewRunner(pool, projectmigrations.FS)
	if err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}
