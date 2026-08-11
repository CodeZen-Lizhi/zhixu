package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/gin-gonic/gin"
)

const (
	testSessionID = foundation.ID("71000000-0000-4000-8000-000000000001")
	testTokenID   = foundation.ID("71000000-0000-4000-8000-000000000002")
)

type fakeManager struct {
	snapshot   func(context.Context) (modelsettingsdomain.Snapshot, error)
	save       func(context.Context, modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error)
	test       func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error)
	activation func(context.Context, modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error)
}

func (manager *fakeManager) Snapshot(ctx context.Context) (modelsettingsdomain.Snapshot, error) {
	if manager == nil || manager.snapshot == nil {
		return modelsettingsdomain.Snapshot{}, errors.New("unexpected Snapshot call")
	}
	return manager.snapshot(ctx)
}

func (manager *fakeManager) Save(ctx context.Context, command modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
	if manager == nil || manager.save == nil {
		return modelsettingsdomain.Snapshot{}, errors.New("unexpected Save call")
	}
	return manager.save(ctx, command)
}

func (manager *fakeManager) Test(ctx context.Context, command modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
	if manager == nil || manager.test == nil {
		return modelsettingsapplication.TestResult{}, errors.New("unexpected Test call")
	}
	return manager.test(ctx, command)
}

func (manager *fakeManager) StartActivation(ctx context.Context, command modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error) {
	if manager == nil || manager.activation == nil {
		return modelsettingsapplication.StartActivationResult{}, errors.New("unexpected StartActivation call")
	}
	return manager.activation(ctx, command)
}

func TestNewHandlerRejectsMissingDependenciesAndInvalidTimeout(t *testing.T) {
	t.Parallel()
	manager := managerForSnapshot(disabledSnapshot())
	if _, err := NewHandler(nil, Options{}); err == nil {
		t.Fatal("nil manager was accepted")
	}
	var typedNil *fakeManager
	if _, err := NewHandler(typedNil, Options{}); err == nil {
		t.Fatal("typed nil manager was accepted")
	}
	if _, err := NewHandler(manager, Options{Timeout: 6 * time.Minute}); err == nil {
		t.Fatal("unbounded timeout was accepted")
	}
}

func TestGetReturnsDesiredAndActiveSummariesWithoutSecrets(t *testing.T) {
	t.Parallel()
	snapshot := configuredSnapshot()
	handler := mustHandler(t, managerForSnapshot(snapshot), Options{})
	response := serve(t, handler, "GET", "/api/v1/settings/models", "", "")
	if response.Code != 200 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	assertNoStore(t, response)
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["desired_revision"] != float64(2) || body["active_revision"] != float64(1) || body["restart_required"] != false || body["apply_required"] != true {
		t.Fatalf("revision projection = %#v", body)
	}
	desired := body["desired_settings"].(map[string]any)
	desiredChat := desired["chat"].(map[string]any)
	if desiredChat["provider"] != "openai-compatible" || desiredChat["api_key_configured"] != true {
		t.Fatalf("desired chat = %#v", desiredChat)
	}
	active := body["active_settings"].(map[string]any)
	activeChat := active["chat"].(map[string]any)
	if activeChat["provider"] != "disabled" || activeChat["api_key_configured"] != false {
		t.Fatalf("active chat = %#v", activeChat)
	}
	rollout := body["rollout"].(map[string]any)
	if rollout["target_revision"] != nil || rollout["last_error_code"] != nil || rollout["retryable"] != false {
		t.Fatalf("idle rollout = %#v", rollout)
	}
	assertBodyExcludes(t, response.Body.String(), "chat-secret-canary", "embedding-secret-canary", "instance_id", "ciphertext", "nonce")
}

func TestManagerContextFailureMapsToDependencyUnavailable(t *testing.T) {
	t.Parallel()
	manager := &fakeManager{snapshot: func(context.Context) (modelsettingsdomain.Snapshot, error) {
		return modelsettingsdomain.Snapshot{}, context.DeadlineExceeded
	}}
	handler := mustHandler(t, manager, Options{})
	response := serve(t, handler, "GET", "/api/v1/settings/models", "", "")
	if response.Code != 503 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	assertProblem(t, response, modelsettingsdomain.ErrorCodeUnavailable)
	assertNoStore(t, response)
}

func TestRequiredSessionAllowsCookieAndRejectsBearerEvenWithCookie(t *testing.T) {
	t.Parallel()
	handler := mustHandler(t, managerForSnapshot(disabledSnapshot()), Options{RequireSession: true})
	wrapped := withAuthentication(t, handler)

	sessionRequest := httptest.NewRequest("GET", "/api/v1/settings/models", nil)
	sessionRequest.AddCookie(&nethttp.Cookie{Name: authhttp.SessionCookieName, Value: "session-cookie"})
	sessionResponse := httptest.NewRecorder()
	wrapped.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != 200 {
		t.Fatalf("session status = %d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}

	bearerRequest := httptest.NewRequest("GET", "/api/v1/settings/models", nil)
	bearerRequest.Header.Set("Authorization", "Bearer api-token")
	bearerRequest.AddCookie(&nethttp.Cookie{Name: authhttp.SessionCookieName, Value: "session-cookie"})
	bearerResponse := httptest.NewRecorder()
	wrapped.ServeHTTP(bearerResponse, bearerRequest)
	if bearerResponse.Code != 403 {
		t.Fatalf("bearer status = %d body=%s", bearerResponse.Code, bearerResponse.Body.String())
	}
	assertProblem(t, bearerResponse, errorCodeSessionRequired)
	assertNoStore(t, bearerResponse)
}

func TestRequiredSessionSaveUsesSessionPrincipalActor(t *testing.T) {
	t.Parallel()
	current := disabledSnapshot()
	manager := managerForSnapshot(current)
	manager.save = func(_ context.Context, command modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
		if command.CreatedBy != "SESSION:"+string(testSessionID) {
			t.Fatalf("CreatedBy = %q", command.CreatedBy)
		}
		result := current
		result.DesiredRevision = 1
		return result, nil
	}
	handler := mustHandler(t, manager, Options{RequireSession: true})
	wrapped := withAuthentication(t, handler)
	request := httptest.NewRequest("PUT", "/api/v1/settings/models", strings.NewReader(disabledUpdateBody(0, `{"action":"clear"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.Header.Set(authhttp.CSRFHeaderName, "csrf")
	request.AddCookie(&nethttp.Cookie{Name: authhttp.SessionCookieName, Value: "session-cookie"})
	response := httptest.NewRecorder()
	wrapped.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	assertNoStore(t, response)
}

func TestUpdateStrictJSONBoundary(t *testing.T) {
	t.Parallel()
	valid := disabledUpdateBody(0, `{"action":"clear"}`)
	tests := []struct {
		name        string
		body        string
		contentType string
		status      int
		code        string
	}{
		{name: "unknown root", body: strings.Replace(valid, `"expected_revision":0`, `"expected_revision":0,"unknown":true`, 1), contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "duplicate root", body: strings.Replace(valid, `"expected_revision":0`, `"expected_revision":0,"expected_revision":1`, 1), contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "duplicate nested", body: strings.Replace(valid, `"provider":"disabled"`, `"provider":"disabled","provider":"disabled"`, 1), contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "multiple documents", body: valid + `{}`, contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "missing embedding", body: fmt.Sprintf(`{"expected_revision":0,"chat":%s}`, disabledChatDraft(`{"action":"clear"}`)), contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "null chat", body: strings.Replace(valid, `"chat":`+disabledChatDraft(`{"action":"clear"}`), `"chat":null`, 1), contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "null optional value", body: disabledUpdateBody(0, `{"action":"keep","value":null}`), contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "oversized", body: valid + strings.Repeat(" ", maxRequestBodyBytes), contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "wrong media type", body: valid, contentType: "text/plain", status: 415, code: errorCodeUnsupportedMedia},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := managerForSnapshot(disabledSnapshot())
			saveCalls := 0
			manager.save = func(context.Context, modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
				saveCalls++
				return disabledSnapshot(), nil
			}
			handler := mustHandler(t, manager, Options{})
			response := serve(t, handler, "PUT", "/api/v1/settings/models", test.body, test.contentType)
			if response.Code != test.status {
				t.Fatalf("status = %d want %d body=%s", response.Code, test.status, response.Body.String())
			}
			assertProblem(t, response, test.code)
			assertNoStore(t, response)
			if saveCalls != 0 {
				t.Fatalf("Save calls = %d", saveCalls)
			}
		})
	}
}

func TestUpdateMapsKeepReplaceAndClearWithoutReturningSecret(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		actionJSON string
		wantKind   modelsettingsdomain.SecretActionKind
		wantValue  string
	}{
		{name: "keep", actionJSON: `{"action":"keep"}`, wantKind: modelsettingsdomain.SecretActionKeep},
		{name: "replace", actionJSON: `{"action":"replace","value":"chat-replacement-canary"}`, wantKind: modelsettingsdomain.SecretActionReplace, wantValue: "chat-replacement-canary"},
		{name: "clear", actionJSON: `{"action":"clear"}`, wantKind: modelsettingsdomain.SecretActionClear},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := configuredSnapshot()
			manager := managerForSnapshot(current)
			manager.save = func(_ context.Context, command modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
				if command.ExpectedRevision != 2 || command.CreatedBy != localDevelopmentActor {
					t.Fatalf("command metadata = %+v", command)
				}
				if command.ChatSecret.Kind != test.wantKind {
					t.Fatalf("chat action = %s", command.ChatSecret.Kind)
				}
				secretBytes := command.ChatSecret.Value.Bytes()
				defer clear(secretBytes)
				if string(secretBytes) != test.wantValue {
					t.Fatalf("chat replacement mismatch")
				}
				if command.EmbeddingSecret.Kind != modelsettingsdomain.SecretActionClear {
					t.Fatalf("embedding action = %s", command.EmbeddingSecret.Kind)
				}
				if command.Settings.Chat.Timeout != current.DesiredSettings.Settings.Chat.Timeout ||
					command.Settings.Chat.MaxRequestBytes != current.DesiredSettings.Settings.Chat.MaxRequestBytes ||
					command.Settings.Embedding.MaxBatchSize != current.DesiredSettings.Settings.Embedding.MaxBatchSize {
					t.Fatal("hidden runtime limits were not preserved")
				}
				result := current
				result.DesiredRevision = 3
				return result, nil
			}
			handler := mustHandler(t, manager, Options{})
			body := configuredUpdateBody(test.actionJSON, `{"action":"clear"}`)
			response := serve(t, handler, "PUT", "/api/v1/settings/models", body, "application/json")
			if response.Code != 200 {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			assertBodyExcludes(t, response.Body.String(), "chat-replacement-canary", `"api_key"`, `"value"`)
		})
	}
}

func TestUpdateSecretByteBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		secretSize int
		wantStatus int
		wantCalls  int
	}{
		{name: "exact limit", secretSize: maxSecretBytes, wantStatus: 200, wantCalls: 1},
		{name: "one byte beyond", secretSize: maxSecretBytes + 1, wantStatus: 400, wantCalls: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := configuredSnapshot()
			saveCalls := 0
			manager := managerForSnapshot(current)
			manager.save = func(_ context.Context, command modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
				saveCalls++
				value := command.ChatSecret.Value.Bytes()
				defer clear(value)
				if len(value) != test.secretSize {
					t.Fatalf("secret bytes = %d", len(value))
				}
				return current, nil
			}
			handler := mustHandler(t, manager, Options{})
			secret := strings.Repeat("k", test.secretSize)
			body := configuredUpdateBody(fmt.Sprintf(`{"action":"replace","value":%q}`, secret), `{"action":"clear"}`)
			response := serve(t, handler, "PUT", "/api/v1/settings/models", body, "application/json")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d want %d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if saveCalls != test.wantCalls {
				t.Fatalf("Save calls = %d want %d", saveCalls, test.wantCalls)
			}
		})
	}
}

func TestUpdateAcceptsMaximumChatAndEmbeddingSecretsTogether(t *testing.T) {
	t.Parallel()
	current := configuredSnapshot()
	manager := managerForSnapshot(current)
	manager.save = func(_ context.Context, command modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
		chat := command.ChatSecret.Value.Bytes()
		embedding := command.EmbeddingSecret.Value.Bytes()
		defer clear(chat)
		defer clear(embedding)
		if len(chat) != maxSecretBytes || len(embedding) != maxSecretBytes {
			t.Fatalf("secret sizes chat=%d embedding=%d", len(chat), len(embedding))
		}
		return current, nil
	}
	handler := mustHandler(t, manager, Options{})
	secret := strings.Repeat("k", maxSecretBytes)
	action := fmt.Sprintf(`{"action":"replace","value":%q}`, secret)
	body := configuredUpdateBody(action, action)
	if len(body) <= 32*1024 || len(body) > maxRequestBodyBytes {
		t.Fatalf("test payload bytes = %d", len(body))
	}
	response := serve(t, handler, "PUT", "/api/v1/settings/models", body, "application/json")
	if response.Code != 200 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestUpdateConflictReturnsOnlyCurrentRevision(t *testing.T) {
	t.Parallel()
	initial := configuredSnapshot()
	latest := initial
	latest.DesiredRevision = 3
	snapshotCalls := 0
	manager := &fakeManager{}
	manager.snapshot = func(context.Context) (modelsettingsdomain.Snapshot, error) {
		snapshotCalls++
		if snapshotCalls == 1 {
			return initial, nil
		}
		return latest, nil
	}
	manager.save = func(context.Context, modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
		return modelsettingsdomain.Snapshot{}, foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeRevisionConflict, false, errors.New("https://private.example.test secret-conflict-canary"))
	}
	handler := mustHandler(t, manager, Options{})
	response := serve(t, handler, "PUT", "/api/v1/settings/models", configuredUpdateBody(`{"action":"keep"}`, `{"action":"keep"}`), "application/json")
	if response.Code != 409 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if len(problem.Details) != 1 || problem.Details["current_revision"] != float64(3) {
		t.Fatalf("details = %#v", problem.Details)
	}
	assertBodyExcludes(t, response.Body.String(), "private.example.test", "secret-conflict-canary", "expected_revision", "provider")
}

func TestUpdateRejectsStaleRevisionBeforeSave(t *testing.T) {
	t.Parallel()
	current := configuredSnapshot()
	current.DesiredRevision = 3
	saveCalls := 0
	manager := managerForSnapshot(current)
	manager.save = func(context.Context, modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
		saveCalls++
		return modelsettingsdomain.Snapshot{}, errors.New("unexpected Save call")
	}
	handler := mustHandler(t, manager, Options{})
	response := serve(t, handler, "PUT", "/api/v1/settings/models", configuredUpdateBody(`{"action":"keep"}`, `{"action":"keep"}`), "application/json")
	if response.Code != nethttp.StatusConflict {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if len(problem.Details) != 1 || problem.Details["current_revision"] != float64(3) {
		t.Fatalf("details = %#v", problem.Details)
	}
	if saveCalls != 0 {
		t.Fatalf("Save calls = %d", saveCalls)
	}
	assertNoStore(t, response)
}

func TestStartActivationStrictJSONBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		body        string
		contentType string
		status      int
		code        string
	}{
		{name: "unknown field", body: `{"expected_revision":2,"rollout_id":"71000000-0000-4000-8000-000000000099"}`, contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "duplicate field", body: `{"expected_revision":2,"expected_revision":2}`, contentType: "application/json", status: 400, code: errorCodeInvalidJSON},
		{name: "missing field", body: `{}`, contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "null field", body: `{"expected_revision":null}`, contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "string field", body: `{"expected_revision":"2"}`, contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "negative field", body: `{"expected_revision":-1}`, contentType: "application/json", status: 400, code: modelsettingsdomain.ErrorCodeInvalid},
		{name: "wrong media type", body: `{"expected_revision":2}`, contentType: "text/plain", status: 415, code: errorCodeUnsupportedMedia},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			activationCalls := 0
			manager := managerForSnapshot(configuredSnapshot())
			manager.activation = func(context.Context, modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error) {
				activationCalls++
				return modelsettingsapplication.StartActivationResult{}, nil
			}
			handler := mustHandler(t, manager, Options{})
			response := serve(t, handler, nethttp.MethodPost, "/api/v1/settings/models/activations", test.body, test.contentType)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			assertProblem(t, response, test.code)
			assertNoStore(t, response)
			if activationCalls != 0 {
				t.Fatalf("StartActivation calls=%d", activationCalls)
			}
		})
	}
}

func TestStartActivationPersistsExactTargetAndReturnsAuthoritativeSnapshot(t *testing.T) {
	t.Parallel()
	before := configuredSnapshot()
	before.Rollout.Version = 7
	after := before
	snapshotCalls := 0
	manager := &fakeManager{}
	manager.snapshot = func(context.Context) (modelsettingsdomain.Snapshot, error) {
		snapshotCalls++
		if snapshotCalls == 1 {
			return before, nil
		}
		return after, nil
	}
	manager.activation = func(ctx context.Context, command modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("activation context unexpectedly cancelled: %v", err)
		}
		if _, err := foundation.ParseID(string(command.RolloutID)); err != nil {
			t.Fatalf("server rollout ID=%q: %v", command.RolloutID, err)
		}
		if command.TargetRevision != 2 || command.ExpectedDesiredRevision != 2 || command.ExpectedStateVersion != 7 ||
			command.LeaseDuration != defaultActivationLease || command.FreshWithin != defaultActivationFreshness {
			t.Fatalf("activation command=%+v", command)
		}
		after.Rollout = modelsettingsdomain.RolloutState{
			ID: command.RolloutID, TargetRevision: 2, PreviousActiveRevision: 1,
			Phase: modelsettingsdomain.RolloutPhasePreparing, Version: 8,
		}
		after.Participants.API = modelsettingsdomain.ParticipantSummary{
			Present: true, TargetRevision: 2, Phase: modelsettingsdomain.ParticipantPhasePreparing, Fresh: true,
		}
		after.ApplyRequired = true
		return modelsettingsapplication.StartActivationResult{State: after.Rollout}, nil
	}
	handler := mustHandler(t, manager, Options{})
	response := serve(t, handler, nethttp.MethodPost, "/api/v1/settings/models/activations", `{"expected_revision":2}`, "application/json")
	if response.Code != nethttp.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertNoStore(t, response)
	var body settingsResponse
	decodeResponse(t, response, &body)
	if body.Rollout.ID == nil || *body.Rollout.ID != string(after.Rollout.ID) || body.Rollout.Version != 8 ||
		body.Rollout.Phase != modelsettingsdomain.RolloutPhasePreparing || body.Rollout.TargetRevision == nil || *body.Rollout.TargetRevision != 2 {
		t.Fatalf("rollout=%+v", body.Rollout)
	}
	if !body.Participants.API.Present || body.Participants.API.TargetRevision == nil || *body.Participants.API.TargetRevision != 2 ||
		body.Participants.API.Phase == nil || *body.Participants.API.Phase != modelsettingsdomain.ParticipantPhasePreparing ||
		body.Participants.Worker.Present || body.Participants.Worker.TargetRevision != nil || body.Participants.Worker.Phase != nil || body.Participants.Worker.Fresh {
		t.Fatalf("participants=%+v", body.Participants)
	}
	if !body.ApplyRequired || body.RestartRequired {
		t.Fatalf("apply_required=%t restart_required=%t", body.ApplyRequired, body.RestartRequired)
	}
	assertBodyExcludes(t, response.Body.String(), "chat-secret-canary", "embedding-secret-canary", "instance_id", "ciphertext", "nonce", "Authorization")
}

func TestStartActivationIsIdempotentForLiveAndAlreadyActiveTargets(t *testing.T) {
	t.Parallel()
	existingID := foundation.ID("71000000-0000-4000-8000-000000000090")
	tests := []struct {
		name     string
		snapshot modelsettingsdomain.Snapshot
	}{
		{
			name: "same target live",
			snapshot: func() modelsettingsdomain.Snapshot {
				snapshot := configuredSnapshot()
				snapshot.Rollout = modelsettingsdomain.RolloutState{ID: existingID, TargetRevision: 2, PreviousActiveRevision: 1, Phase: modelsettingsdomain.RolloutPhasePreparing, Version: 9}
				return snapshot
			}(),
		},
		{
			name: "already active healthy",
			snapshot: func() modelsettingsdomain.Snapshot {
				snapshot := configuredSnapshot()
				snapshot.ActiveRevision = snapshot.DesiredRevision
				snapshot.ActiveSettings = snapshot.DesiredSettings
				snapshot.Runtime.API.AppliedRevision = snapshot.ActiveRevision
				snapshot.Runtime.Worker.AppliedRevision = snapshot.ActiveRevision
				snapshot.Rollout = modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle, Version: 9}
				snapshot.ApplyRequired = false
				return snapshot
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			manager := managerForSnapshot(test.snapshot)
			manager.activation = func(_ context.Context, command modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error) {
				calls++
				return modelsettingsapplication.StartActivationResult{State: test.snapshot.Rollout, Replayed: true}, nil
			}
			handler := mustHandler(t, manager, Options{})
			response := serve(t, handler, nethttp.MethodPost, "/api/v1/settings/models/activations", `{"expected_revision":2}`, "application/json")
			if response.Code != nethttp.StatusAccepted || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
			assertNoStore(t, response)
		})
	}
}

func TestStartActivationConflictReturnsOnlyCurrentRevision(t *testing.T) {
	t.Parallel()
	initial := configuredSnapshot()
	latest := initial
	latest.DesiredRevision = 3
	latest.Rollout.Version++
	snapshotCalls := 0
	manager := &fakeManager{}
	manager.snapshot = func(context.Context) (modelsettingsdomain.Snapshot, error) {
		snapshotCalls++
		if snapshotCalls == 1 {
			return initial, nil
		}
		return latest, nil
	}
	manager.activation = func(context.Context, modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error) {
		return modelsettingsapplication.StartActivationResult{}, foundation.NewError(
			foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeActivationConflict, false,
			errors.New("https://private.example.test activation-secret-canary instance_id=71000000-0000-4000-8000-000000000099"),
		)
	}
	handler := mustHandler(t, manager, Options{})
	response := serve(t, handler, nethttp.MethodPost, "/api/v1/settings/models/activations", `{"expected_revision":2}`, "application/json")
	if response.Code != nethttp.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if problem.ErrorCode != modelsettingsdomain.ErrorCodeActivationConflict || len(problem.Details) != 1 || problem.Details["current_revision"] != float64(3) {
		t.Fatalf("problem=%+v", problem)
	}
	assertNoStore(t, response)
	assertBodyExcludes(t, response.Body.String(), "private.example.test", "activation-secret-canary", "instance_id", "71000000-0000-4000-8000-000000000099")
}

func TestStartActivationRequestCancellationDoesNotCancelDurableStart(t *testing.T) {
	t.Parallel()
	current := configuredSnapshot()
	manager := managerForSnapshot(current)
	manager.activation = func(ctx context.Context, command modelsettingsapplication.StartActivationCommand) (modelsettingsapplication.StartActivationResult, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("durable activation inherited request cancellation: %v", err)
		}
		return modelsettingsapplication.StartActivationResult{State: current.Rollout}, nil
	}
	handler := mustHandler(t, manager, Options{})
	router := gin.New()
	handler.Routes(router.Group("/api/v1"))
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	request := httptest.NewRequest(nethttp.MethodPost, "/api/v1/settings/models/activations", strings.NewReader(`{"expected_revision":2}`)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != nethttp.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSnapshotParticipantProjectionKeepsServingAndCandidateSeparate(t *testing.T) {
	t.Parallel()
	snapshot := configuredSnapshot()
	snapshot.Rollout = modelsettingsdomain.RolloutState{
		ID: foundation.ID("71000000-0000-4000-8000-000000000091"), TargetRevision: 2,
		PreviousActiveRevision: 1, Phase: modelsettingsdomain.RolloutPhaseFailed,
		LastErrorCode: modelsettingsdomain.ErrorCodeActivationPrepareFailed, Version: 10,
	}
	snapshot.Participants.API = modelsettingsdomain.ParticipantSummary{
		Present: true, TargetRevision: 2, Phase: modelsettingsdomain.ParticipantPhaseFailed,
		Fresh: true, LastErrorCode: modelsettingsdomain.ErrorCodeActivationPrepareFailed, ErrorRetryable: true,
	}
	handler := mustHandler(t, managerForSnapshot(snapshot), Options{})
	response := serve(t, handler, nethttp.MethodGet, "/api/v1/settings/models", "", "")
	if response.Code != nethttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body settingsResponse
	decodeResponse(t, response, &body)
	if body.Runtime.API.AppliedRevision != 1 || body.Runtime.API.Phase != modelsettingsdomain.RuntimePhaseActive || !body.Runtime.API.Fresh {
		t.Fatalf("serving runtime=%+v", body.Runtime.API)
	}
	if !body.Participants.API.Present || body.Participants.API.Phase == nil || *body.Participants.API.Phase != modelsettingsdomain.ParticipantPhaseFailed ||
		body.Participants.API.LastErrorCode == nil || *body.Participants.API.LastErrorCode != modelsettingsdomain.ErrorCodeActivationPrepareFailed || !body.Participants.API.Retryable {
		t.Fatalf("candidate participant=%+v", body.Participants.API)
	}
	if body.Participants.Worker != (participantResponse{}) {
		t.Fatalf("absent worker=%+v", body.Participants.Worker)
	}
}

func TestConnectionTestResolvesOneTargetAndDestroysTemporarySecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		body           string
		target         string
		wantProvider   string
		wantModel      string
		wantActionKind modelsettingsdomain.SecretActionKind
	}{
		{name: "chat", body: fmt.Sprintf(`{"target":"chat","chat":%s}`, configuredChatDraft(`{"action":"replace","value":"draft-chat-canary"}`)), target: "chat", wantProvider: "openai-compatible", wantModel: "chat-v2", wantActionKind: modelsettingsdomain.SecretActionReplace},
		{name: "embedding", body: fmt.Sprintf(`{"target":"embedding","embedding":%s}`, configuredEmbeddingDraft(`{"action":"replace","value":"draft-embedding-canary"}`)), target: "embedding", wantProvider: "openai-compatible", wantModel: "embed-v2", wantActionKind: modelsettingsdomain.SecretActionReplace},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := configuredSnapshot()
			var capturedSecret modelsettingsdomain.Secret
			manager := managerForSnapshot(current)
			manager.test = func(_ context.Context, command modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
				if command.Target != modelsettingsapplication.ConnectionTarget(test.target) {
					t.Fatalf("target = %q", command.Target)
				}
				if command.Draft.ExpectedRevision != current.DesiredRevision {
					t.Fatalf("expected revision = %d", command.Draft.ExpectedRevision)
				}
				selected := command.Draft.ChatSecret
				other := command.Draft.EmbeddingSecret
				if test.target == "embedding" {
					selected, other = command.Draft.EmbeddingSecret, command.Draft.ChatSecret
				}
				if selected.Kind != test.wantActionKind || other.Kind != modelsettingsdomain.SecretActionKeep {
					t.Fatalf("secret actions selected=%s other=%s", selected.Kind, other.Kind)
				}
				capturedSecret = selected.Value
				return modelsettingsapplication.TestResult{
					Target:   command.Target,
					Provider: test.wantProvider,
					Model:    test.wantModel,
					APIStyle: func() modelsettingsdomain.ChatAPIStyle {
						if command.Target == modelsettingsapplication.ConnectionTargetChat {
							return modelsettingsdomain.ChatAPIStyleChatCompletions
						}
						return ""
					}(),
					EndpointPath: func() string {
						if command.Target == modelsettingsapplication.ConnectionTargetChat {
							return "/v1/chat/completions"
						}
						return "/v1/embeddings"
					}(),
				}, nil
			}
			handler := mustHandler(t, manager, Options{})
			response := serve(t, handler, "POST", "/api/v1/settings/models/test", test.body, "application/json")
			if response.Code != 200 {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			var result testResponse
			decodeResponse(t, response, &result)
			if result.Target != test.target || result.Provider != test.wantProvider || result.Model != test.wantModel || result.Status != "ok" {
				t.Fatalf("result = %+v", result)
			}
			assertSecretZeroed(t, capturedSecret)
			assertBodyExcludes(t, response.Body.String(), "draft-chat-canary", "draft-embedding-canary")
		})
	}
}

func TestConnectionTestBindsStrictDraftCommandToManager(t *testing.T) {
	t.Parallel()
	current := configuredSnapshot()
	manager := managerForSnapshot(current)
	var capturedSecret modelsettingsdomain.Secret
	manager.test = func(_ context.Context, command modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
		if command.Target != modelsettingsapplication.ConnectionTargetChat || command.Draft.ExpectedRevision != current.DesiredRevision {
			t.Fatalf("test command identity=%+v", command)
		}
		if command.Draft.Settings.Chat != (modelsettingsdomain.ChatSettings{
			Provider: modelsettingsdomain.ChatProviderOpenAICompatible, APIStyle: modelsettingsdomain.ChatAPIStyleResponses, BaseURL: "https://test-chat.example.test/v1",
			Model: "test-chat", ModelVersion: "2026-08", AdapterVersion: "v2",
			Timeout: current.DesiredSettings.Settings.Chat.Timeout, MaxRequestBytes: current.DesiredSettings.Settings.Chat.MaxRequestBytes,
			MaxResponseBytes: current.DesiredSettings.Settings.Chat.MaxResponseBytes,
		}) {
			t.Fatalf("chat draft=%+v", command.Draft.Settings.Chat)
		}
		if command.Draft.Settings.Embedding != current.DesiredSettings.Settings.Embedding {
			t.Fatalf("unselected embedding changed: %+v", command.Draft.Settings.Embedding)
		}
		if command.Draft.ChatSecret.Kind != modelsettingsdomain.SecretActionReplace || command.Draft.EmbeddingSecret.Kind != modelsettingsdomain.SecretActionKeep {
			t.Fatalf("secret actions chat=%s embedding=%s", command.Draft.ChatSecret.Kind, command.Draft.EmbeddingSecret.Kind)
		}
		capturedSecret = command.Draft.ChatSecret.Value
		return modelsettingsapplication.TestResult{Target: command.Target, Provider: string(command.Draft.Settings.Chat.Provider), Model: command.Draft.Settings.Chat.Model,
			APIStyle: command.Draft.Settings.Chat.APIStyle, EndpointPath: "/v1/responses"}, nil
	}
	handler := mustHandler(t, manager, Options{})
	body := `{"target":"chat","chat":{"provider":"openai-compatible","api_style":"responses","base_url":"https://test-chat.example.test/v1","model":"test-chat","model_version":"2026-08","adapter_version":"v2","api_key":{"action":"replace","value":"draft-command-canary"}}}`
	response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
	if response.Code != nethttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result testResponse
	decodeResponse(t, response, &result)
	if result != (testResponse{Target: "chat", Status: connectionTestSuccessStatus, Provider: "openai-compatible", Model: "test-chat", APIStyle: modelsettingsdomain.ChatAPIStyleResponses, EndpointPath: "/v1/responses"}) {
		t.Fatalf("response=%+v", result)
	}
	assertSecretZeroed(t, capturedSecret)
	assertBodyExcludes(t, response.Body.String(), "draft-command-canary")
}

func TestConnectionTestRejectsInvalidTargetUnionBeforeManagerTest(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"unknown target":     `{"target":"other"}`,
		"unknown root field": fmt.Sprintf(`{"target":"chat","chat":%s,"unexpected":true}`, configuredChatDraft(`{"action":"keep"}`)),
		"duplicate target":   fmt.Sprintf(`{"target":"chat","target":"chat","chat":%s}`, configuredChatDraft(`{"action":"keep"}`)),
		"missing chat":       `{"target":"chat"}`,
		"both drafts":        fmt.Sprintf(`{"target":"chat","chat":%s,"embedding":%s}`, configuredChatDraft(`{"action":"keep"}`), configuredEmbeddingDraft(`{"action":"keep"}`)),
		"disabled chat":      fmt.Sprintf(`{"target":"chat","chat":%s}`, disabledChatDraft(`{"action":"clear"}`)),
		"disabled embedding": fmt.Sprintf(`{"target":"embedding","embedding":%s}`, disabledEmbeddingDraft()),
		"null other draft":   fmt.Sprintf(`{"target":"chat","chat":%s,"embedding":null}`, configuredChatDraft(`{"action":"keep"}`)),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			testCalls := 0
			manager := managerForSnapshot(configuredSnapshot())
			manager.test = func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
				testCalls++
				return modelsettingsapplication.TestResult{}, nil
			}
			handler := mustHandler(t, manager, Options{})
			response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
			if response.Code != 400 {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			if testCalls != 0 {
				t.Fatalf("Test calls = %d", testCalls)
			}
		})
	}
}

func TestConnectionTestErrorMappingIsStableAndRedacted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		target     string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "provider auth", target: connectionTargetChat, err: foundation.NewError(foundation.ErrorNonRetryableFailure, platformmodels.ErrorCodeChatUnauthorized, false, errors.New("https://private.example.test secret-error-canary")), wantStatus: 502, wantCode: platformmodels.ErrorCodeChatUnauthorized},
		{name: "rate limited", target: connectionTargetChat, err: foundation.NewError(foundation.ErrorRetryableFailure, platformmodels.ErrorCodeChatRateLimited, true, errors.New("provider rate detail canary")), wantStatus: 502, wantCode: platformmodels.ErrorCodeChatRateLimited},
		{name: "provider unavailable", target: connectionTargetEmbedding, err: foundation.NewError(foundation.ErrorRetryableFailure, platformmodels.ErrorCodeEmbeddingUnavailable, true, errors.New("provider unavailable detail canary")), wantStatus: 502, wantCode: platformmodels.ErrorCodeEmbeddingUnavailable},
		{name: "redirect rejected", target: connectionTargetChat, err: foundation.NewError(foundation.ErrorNonRetryableFailure, platformmodels.ErrorCodeChatRedirectRejected, false, errors.New("redirect location canary")), wantStatus: 502, wantCode: platformmodels.ErrorCodeChatRedirectRejected},
		{name: "invalid or oversized response", target: connectionTargetEmbedding, err: foundation.NewError(foundation.ErrorConsistencyViolation, retrievaldomain.ErrorCodeEmbedResultInvalid, false, errors.New("oversized provider body canary")), wantStatus: 502, wantCode: retrievaldomain.ErrorCodeEmbedResultInvalid},
		{name: "model mismatch", target: connectionTargetChat, err: foundation.NewError(foundation.ErrorConsistencyViolation, platformmodels.ErrorCodeChatResponseModelMismatch, false, errors.New("unexpected model canary")), wantStatus: 502, wantCode: platformmodels.ErrorCodeChatResponseModelMismatch},
		{name: "timeout", target: connectionTargetEmbedding, err: foundation.NewError(foundation.ErrorRetryableFailure, platformmodels.ErrorCodeEmbeddingTimeout, true, errors.New("bounded network timeout")), wantStatus: 504, wantCode: platformmodels.ErrorCodeEmbeddingTimeout},
		{name: "dependency", err: foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeUnavailable, true, errors.New("database DSN canary")), wantStatus: 503, wantCode: modelsettingsdomain.ErrorCodeUnavailable},
		{name: "business invalid", err: foundation.NewError(foundation.ErrorInvalidInput, modelsettingsdomain.ErrorCodeInvalid, false, errors.New("invalid endpoint canary")), wantStatus: 400, wantCode: modelsettingsdomain.ErrorCodeInvalid},
		{name: "unknown", err: errors.New("unknown secret cause canary"), wantStatus: 500, wantCode: errorCodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := test.target
			if target == "" {
				target = connectionTargetChat
			}
			manager := managerForSnapshot(configuredSnapshot())
			manager.test = func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
				return modelsettingsapplication.TestResult{}, test.err
			}
			handler := mustHandler(t, manager, Options{})
			body := fmt.Sprintf(`{"target":"chat","chat":%s}`, configuredChatDraft(`{"action":"keep"}`))
			if target == connectionTargetEmbedding {
				body = fmt.Sprintf(`{"target":"embedding","embedding":%s}`, configuredEmbeddingDraft(`{"action":"keep"}`))
			}
			response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d want %d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			assertProblem(t, response, test.wantCode)
			assertNoStore(t, response)
			assertBodyExcludes(t, response.Body.String(), "private.example.test", "secret-error-canary", "provider rate detail canary", "provider unavailable detail canary", "redirect location canary", "oversized provider body canary", "unexpected model canary", "database DSN canary", "invalid endpoint canary", "unknown secret cause canary")
		})
	}
}

func TestConnectionTestReturnsTargetBoundSafeProviderDiagnostic(t *testing.T) {
	t.Parallel()
	diagnostic := &platformmodels.ConnectionDiagnostic{
		Stage:              platformmodels.ConnectionStageProviderResponse,
		ProviderHTTPStatus: nethttp.StatusUnauthorized,
		ProviderErrorCode:  "invalid_api_key",
		ProviderErrorType:  "authentication_error",
		ProviderMessage:    "Invalid API key",
		ProviderRequestID:  "req_chat_401",
	}
	manager := managerForSnapshot(configuredSnapshot())
	manager.test = func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
		return modelsettingsapplication.TestResult{}, foundation.NewError(
			foundation.ErrorNonRetryableFailure,
			platformmodels.ErrorCodeChatUnauthorized,
			false,
			diagnostic,
		)
	}
	handler := mustHandler(t, manager, Options{})
	body := fmt.Sprintf(`{"target":"chat","chat":%s}`, configuredChatDraft(`{"action":"keep"}`))
	response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
	if response.Code != nethttp.StatusBadGateway {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	want := map[string]any{
		"target": "chat", "stage": "provider_response", "provider_http_status": float64(401),
		"provider_error_code": "invalid_api_key", "provider_error_type": "authentication_error",
		"provider_message": "Invalid API key", "provider_request_id": "req_chat_401",
	}
	if !reflect.DeepEqual(problem.Details, want) || problem.Message != "模型 Provider 返回错误" {
		t.Fatalf("problem=%#v want details=%#v", problem, want)
	}
	assertNoStore(t, response)
	assertBodyExcludes(t, response.Body.String(), "https://chat.example.test/v1", "draft-chat-canary", "Authorization", "Bearer")
}

func TestConnectionTestReturnsTransportDiagnosticWithoutChangingTimeoutStatus(t *testing.T) {
	t.Parallel()
	manager := managerForSnapshot(configuredSnapshot())
	manager.test = func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
		return modelsettingsapplication.TestResult{}, foundation.NewError(
			foundation.ErrorRetryableFailure,
			platformmodels.ErrorCodeEmbeddingTimeout,
			true,
			&platformmodels.ConnectionDiagnostic{Stage: platformmodels.ConnectionStageTimeout, TransportError: "deadline exceeded"},
		)
	}
	handler := mustHandler(t, manager, Options{})
	body := fmt.Sprintf(`{"target":"embedding","embedding":%s}`, configuredEmbeddingDraft(`{"action":"keep"}`))
	response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
	if response.Code != nethttp.StatusGatewayTimeout {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	want := map[string]any{"target": "embedding", "stage": "timeout", "transport_error": "deadline exceeded"}
	if !reflect.DeepEqual(problem.Details, want) || !problem.Retryable {
		t.Fatalf("problem=%#v want details=%#v", problem, want)
	}
}

func TestConnectionTestReturnsStableValidationReasonWithoutProviderOutput(t *testing.T) {
	t.Parallel()
	manager := managerForSnapshot(configuredSnapshot())
	manager.test = func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
		return modelsettingsapplication.TestResult{}, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			platformmodels.ErrorCodeChatResponseInvalid,
			false,
			&platformmodels.ConnectionDiagnostic{
				Stage:              platformmodels.ConnectionStageResponseValidation,
				ProviderHTTPStatus: nethttp.StatusOK,
				ProviderMessage:    "provider-output-secret-canary",
				TransportError:     "provider-transport-secret-canary",
				ValidationReason:   platformmodels.ConnectionValidationFinishReasonLength,
			},
		)
	}
	handler := mustHandler(t, manager, Options{})
	body := fmt.Sprintf(`{"target":"chat","chat":%s}`, configuredChatDraft(`{"action":"keep"}`))
	response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
	if response.Code != nethttp.StatusBadGateway {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	want := map[string]any{"target": "chat", "stage": "response_validation", "validation_reason": "finish_reason_length"}
	if !reflect.DeepEqual(problem.Details, want) || problem.Message != "模型 Provider 响应校验失败" {
		t.Fatalf("problem=%#v want details=%#v", problem, want)
	}
	assertBodyExcludes(t, response.Body.String(), "provider-output-secret-canary", "provider-transport-secret-canary", "https://chat.example.test/v1")
}

func TestConnectionTestRevisionConflictIncludesCurrentRevision(t *testing.T) {
	t.Parallel()
	initial := configuredSnapshot()
	latest := initial
	latest.DesiredRevision = 4
	calls := 0
	manager := &fakeManager{}
	manager.snapshot = func(context.Context) (modelsettingsdomain.Snapshot, error) {
		calls++
		if calls == 1 {
			return initial, nil
		}
		return latest, nil
	}
	manager.test = func(context.Context, modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
		return modelsettingsapplication.TestResult{}, foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeRevisionConflict, false, errors.New("revision changed"))
	}
	handler := mustHandler(t, manager, Options{})
	body := fmt.Sprintf(`{"target":"chat","chat":%s}`, configuredChatDraft(`{"action":"keep"}`))
	response := serve(t, handler, "POST", "/api/v1/settings/models/test", body, "application/json")
	if response.Code != 409 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if len(problem.Details) != 1 || problem.Details["current_revision"] != float64(4) {
		t.Fatalf("details = %#v", problem.Details)
	}
}

func disabledSnapshot() modelsettingsdomain.Snapshot {
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	summary := modelsettingsdomain.SettingsSummary{Settings: settings}
	return modelsettingsdomain.Snapshot{
		DesiredSettings: summary,
		ActiveSettings:  summary,
		Runtime: modelsettingsdomain.RuntimeSummaries{
			API:    modelsettingsdomain.RuntimeSummary{AppliedRevision: 0, Phase: modelsettingsdomain.RuntimePhaseActive, Fresh: true},
			Worker: modelsettingsdomain.RuntimeSummary{AppliedRevision: 0, Phase: modelsettingsdomain.RuntimePhaseActive, Fresh: true},
		},
		Rollout:             modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle, Version: 1},
		ChatCapability:      modelsettingsdomain.CapabilityDisabled,
		EmbeddingCapability: modelsettingsdomain.CapabilityDisabled,
	}
}

func configuredSnapshot() modelsettingsdomain.Snapshot {
	snapshot := disabledSnapshot()
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	settings.Chat.Provider = modelsettingsdomain.ChatProviderOpenAICompatible
	settings.Chat.BaseURL = "https://chat.example.test/v1"
	settings.Chat.Model = "chat-v2"
	settings.Chat.ModelVersion = "2026-07"
	settings.Embedding.Provider = modelsettingsdomain.EmbeddingProviderOpenAICompatible
	settings.Embedding.BaseURL = "https://embedding.example.test/v1"
	settings.Embedding.Model = "embed-v2"
	settings.Embedding.Dimensions = 3
	snapshot.DesiredRevision = 2
	snapshot.ActiveRevision = 1
	snapshot.DesiredSettings = modelsettingsdomain.SettingsSummary{
		Settings: settings,
		Secrets:  modelsettingsdomain.SecretConfiguration{ChatConfigured: true, EmbeddingConfigured: true},
	}
	snapshot.Runtime.API.AppliedRevision = 1
	snapshot.Runtime.Worker.AppliedRevision = 1
	snapshot.RestartRequired = true
	return snapshot
}

func managerForSnapshot(snapshot modelsettingsdomain.Snapshot) *fakeManager {
	return &fakeManager{snapshot: func(context.Context) (modelsettingsdomain.Snapshot, error) { return snapshot, nil }}
}

func mustHandler(t *testing.T, manager modelsettingsapplication.SettingsManager, options Options) *Handler {
	t.Helper()
	handler, err := NewHandler(manager, options)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serve(t *testing.T, handler *Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	handler.Routes(router.Group("/api/v1"))
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func withAuthentication(t *testing.T, handler *Handler) nethttp.Handler {
	t.Helper()
	authHandler, err := authhttp.NewHandler(fakeAuthService{}, authhttp.Options{AllowedOrigins: []string{"http://127.0.0.1:8080"}})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(authHandler.Middleware)
	handler.Routes(protected)
	return router
}

type fakeAuthService struct{}

func (fakeAuthService) ExchangeBootstrap(context.Context, string) (authapplication.SessionCredential, error) {
	return authapplication.SessionCredential{}, errors.New("unexpected ExchangeBootstrap call")
}
func (fakeAuthService) AuthenticateSession(context.Context, string, string, bool) (authdomain.Principal, error) {
	return authdomain.Principal{Kind: authdomain.PrincipalSession, ID: testSessionID, Scopes: []capability.Capability{capability.ManageSystemSettings}}, nil
}
func (fakeAuthService) CurrentSession(context.Context, string, string, bool) (authdomain.SessionInfo, error) {
	return authdomain.SessionInfo{}, errors.New("unexpected CurrentSession call")
}
func (fakeAuthService) RotateSession(context.Context, authdomain.Principal, string, string, bool) (authapplication.SessionCredential, error) {
	return authapplication.SessionCredential{}, errors.New("unexpected RotateSession call")
}
func (fakeAuthService) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: testTokenID, Scopes: []capability.Capability{capability.ManageSystemSettings}}, nil
}
func (fakeAuthService) CreateAPIToken(context.Context, authdomain.Principal, string, []capability.Capability, time.Duration) (authapplication.APITokenCredential, error) {
	return authapplication.APITokenCredential{}, errors.New("unexpected CreateAPIToken call")
}
func (fakeAuthService) ListAPITokens(context.Context, authdomain.Principal, authdomain.APITokenListQuery) (authdomain.APITokenListPage, error) {
	return authdomain.APITokenListPage{}, errors.New("unexpected ListAPITokens call")
}
func (fakeAuthService) RevokeSession(context.Context, authdomain.Principal, foundation.ID) error {
	return errors.New("unexpected RevokeSession call")
}
func (fakeAuthService) RevokeAPIToken(context.Context, authdomain.Principal, foundation.ID) error {
	return errors.New("unexpected RevokeAPIToken call")
}

func disabledUpdateBody(revision int64, chatAction string) string {
	return fmt.Sprintf(`{"expected_revision":%d,"chat":%s,"embedding":%s}`, revision, disabledChatDraft(chatAction), disabledEmbeddingDraft())
}

func configuredUpdateBody(chatAction, embeddingAction string) string {
	return fmt.Sprintf(`{"expected_revision":2,"chat":%s,"embedding":%s}`, configuredChatDraft(chatAction), configuredEmbeddingDraft(embeddingAction))
}

func disabledChatDraft(action string) string {
	return fmt.Sprintf(`{"provider":"disabled","api_style":"chat_completions","base_url":"","model":"","model_version":"","adapter_version":"v1","api_key":%s}`, action)
}

func disabledEmbeddingDraft() string {
	return `{"provider":"disabled","base_url":"","model":"","dimensions":0,"normalization":"l2","distance_metric":"cosine","api_key":{"action":"clear"}}`
}

func configuredChatDraft(action string) string {
	return fmt.Sprintf(`{"provider":"openai-compatible","api_style":"chat_completions","base_url":"https://chat.example.test/v1","model":"chat-v2","model_version":"2026-07","adapter_version":"v1","api_key":%s}`, action)
}

func configuredEmbeddingDraft(action string) string {
	return fmt.Sprintf(`{"provider":"openai-compatible","base_url":"https://embedding.example.test/v1","model":"embed-v2","dimensions":3,"normalization":"l2","distance_metric":"cosine","api_key":%s}`, action)
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v body=%s", err, response.Body.String())
	}
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if problem.ErrorCode != code {
		t.Fatalf("error_code = %q want %q body=%s", problem.ErrorCode, code, response.Body.String())
	}
}

func assertNoStore(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func assertBodyExcludes(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(body, value) {
			t.Fatalf("response leaked %q: %s", value, body)
		}
	}
}

func assertSecretZeroed(t *testing.T, secret modelsettingsdomain.Secret) {
	t.Helper()
	value := secret.Bytes()
	defer clear(value)
	for _, current := range value {
		if current != 0 {
			t.Fatal("temporary secret buffer was not destroyed")
		}
	}
}
