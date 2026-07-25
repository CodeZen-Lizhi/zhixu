package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/go-chi/chi/v5"
)

const testOrigin = "http://127.0.0.1:8080"

type memoryRepository struct {
	sessions    map[string]domain.Session
	tokens      map[string]domain.APIToken
	currentTime time.Time
}

func (repository *memoryRepository) CreateSession(_ context.Context, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
	now := repository.now()
	session := domain.Session{ID: issue.ID, TokenHash: issue.TokenHash, CSRFHash: issue.CSRFHash, UserLabel: issue.UserLabel, Scopes: issue.Scopes, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(ttl)}
	if repository.sessions == nil {
		repository.sessions = map[string]domain.Session{}
	}
	repository.sessions[session.TokenHash] = session
	return session, nil
}

func (repository *memoryRepository) RotateSession(_ context.Context, previousID foundation.ID, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
	now := repository.now()
	session := domain.Session{ID: issue.ID, TokenHash: issue.TokenHash, CSRFHash: issue.CSRFHash, UserLabel: issue.UserLabel, Scopes: issue.Scopes, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(ttl)}
	for hash, current := range repository.sessions {
		if current.ID == previousID && current.RevokedAt == nil {
			current.RevokedAt = &now
			repository.sessions[hash] = current
			repository.sessions[session.TokenHash] = session
			return session, nil
		}
	}
	return domain.Session{}, errors.New("session not found")
}

func (repository *memoryRepository) AuthenticateSession(_ context.Context, hash string) (domain.Session, error) {
	at := repository.now()
	session, ok := repository.sessions[hash]
	if !ok || session.RevokedAt != nil || !session.ExpiresAt.After(at) {
		return domain.Session{}, errors.New("session unavailable")
	}
	session.LastSeenAt = at
	repository.sessions[hash] = session
	return session, nil
}

func (repository *memoryRepository) RevokeSession(_ context.Context, id foundation.ID) error {
	at := repository.now()
	for hash, session := range repository.sessions {
		if session.ID == id {
			session.RevokedAt = &at
			repository.sessions[hash] = session
			return nil
		}
	}
	return errors.New("session not found")
}

func (repository *memoryRepository) CreateAPIToken(_ context.Context, issue domain.APITokenIssue, ttl time.Duration) (domain.APIToken, error) {
	now := repository.now()
	token := domain.APIToken{ID: issue.ID, TokenHash: issue.TokenHash, Name: issue.Name, Scopes: issue.Scopes, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if repository.tokens == nil {
		repository.tokens = map[string]domain.APIToken{}
	}
	repository.tokens[token.TokenHash] = token
	return token, nil
}

func (repository *memoryRepository) ListAPITokens(_ context.Context, _ domain.APITokenListQuery) ([]domain.APIToken, bool, error) {
	result := make([]domain.APIToken, 0, len(repository.tokens))
	for _, token := range repository.tokens {
		result = append(result, token)
	}
	return result, false, nil
}

func (repository *memoryRepository) AuthenticateAPIToken(_ context.Context, hash string) (domain.APIToken, error) {
	at := repository.now()
	token, ok := repository.tokens[hash]
	if !ok || token.RevokedAt != nil || !token.ExpiresAt.After(at) {
		return domain.APIToken{}, errors.New("token unavailable")
	}
	token.LastUsedAt = &at
	repository.tokens[hash] = token
	return token, nil
}

func (repository *memoryRepository) RevokeAPIToken(_ context.Context, id foundation.ID) error {
	at := repository.now()
	for hash, token := range repository.tokens {
		if token.ID == id {
			token.RevokedAt = &at
			repository.tokens[hash] = token
			return nil
		}
	}
	return foundation.NewError(foundation.ErrorNotFound, application.ErrorCodeAPITokenNotFound, false, errors.New("token not found"))
}

func (repository *memoryRepository) now() time.Time {
	if !repository.currentTime.IsZero() {
		return repository.currentTime
	}
	return time.Now().UTC()
}

type fixedIDs struct{ next int }

func (generator *fixedIDs) New() (foundation.ID, error) {
	generator.next++
	return foundation.ParseID("a1000000-0000-4000-8000-00000000000" + string(rune('0'+generator.next)))
}

type fixedReader struct{ value byte }

func (reader *fixedReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = reader.value
		reader.value++
	}
	return len(target), nil
}

func newTestHandler(t *testing.T) (*Handler, *application.Service) {
	t.Helper()
	repository := &memoryRepository{}
	service, err := application.NewService(repository, &fixedIDs{}, foundation.FixedClock{Value: time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)}, application.Options{
		BootstrapToken: "bootstrap-token-with-at-least-32-bytes", Random: &fixedReader{value: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, Options{SecureCookie: true, AllowedOrigins: []string{testOrigin}})
	if err != nil {
		t.Fatal(err)
	}
	return handler, service
}

func TestBootstrapSetsSecureCookieAndRejectsBadOrigin(t *testing.T) {
	handler, _ := newTestHandler(t)
	router := chi.NewRouter()
	handler.OpenRoutes(router)

	bad := httptest.NewRequest(http.MethodPost, "/auth/sessions", nil)
	bad.Header.Set("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	bad.Header.Set("Origin", "https://evil.example")
	badResponse := httptest.NewRecorder()
	router.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusForbidden || !strings.Contains(badResponse.Body.String(), application.ErrorCodeCSRF) {
		t.Fatalf("bad origin status=%d body=%s", badResponse.Code, badResponse.Body.String())
	}
	padded := httptest.NewRequest(http.MethodPost, "/auth/sessions", nil)
	padded.Header.Set("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	padded.Header.Set("Origin", " "+testOrigin+" ")
	paddedResponse := httptest.NewRecorder()
	router.ServeHTTP(paddedResponse, padded)
	if paddedResponse.Code != http.StatusForbidden || !strings.Contains(paddedResponse.Body.String(), application.ErrorCodeCSRF) {
		t.Fatalf("padded origin status=%d body=%s", paddedResponse.Code, paddedResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/auth/sessions", nil)
	request.Header.Set("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	request.Header.Set("Origin", testOrigin)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != SessionCookieName || !cookie[0].HttpOnly || !cookie[0].Secure || cookie[0].SameSite != http.SameSiteStrictMode || cookie[0].Value == "" {
		t.Fatalf("cookie=%#v", cookie)
	}
	if strings.Contains(response.Body.String(), "bootstrap-token-with-at-least-32-bytes") || strings.Contains(response.Body.String(), cookie[0].Value) {
		t.Fatalf("response exposed a credential: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bootstrap response cache policy=%q", response.Header().Get("Cache-Control"))
	}

	duplicate := httptest.NewRequest(http.MethodPost, "/auth/sessions", nil)
	duplicate.Header.Add("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	duplicate.Header.Add("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	duplicateResponse := httptest.NewRecorder()
	router.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate bootstrap authorization status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
}

func TestAuthMutationEndpointsRejectUnexpectedRequestBodies(t *testing.T) {
	handler, service := newTestHandler(t)
	openRouter := chi.NewRouter()
	handler.OpenRoutes(openRouter)
	bootstrap := httptest.NewRequest(http.MethodPost, "/auth/sessions", strings.NewReader(`{}`))
	bootstrap.Header.Set("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	bootstrapResponse := httptest.NewRecorder()
	openRouter.ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusBadRequest || !strings.Contains(bootstrapResponse.Body.String(), application.ErrorCodeInvalid) || len(bootstrapResponse.Result().Cookies()) != 0 {
		t.Fatalf("bootstrap body status=%d cookies=%v body=%s", bootstrapResponse.Code, bootstrapResponse.Result().Cookies(), bootstrapResponse.Body.String())
	}
	chunkedBootstrap := httptest.NewRequest(http.MethodPost, "/auth/sessions", strings.NewReader(`{}`))
	chunkedBootstrap.ContentLength = -1
	chunkedBootstrap.Header.Set("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	chunkedResponse := httptest.NewRecorder()
	openRouter.ServeHTTP(chunkedResponse, chunkedBootstrap)
	if chunkedResponse.Code != http.StatusBadRequest || !strings.Contains(chunkedResponse.Body.String(), application.ErrorCodeInvalid) {
		t.Fatalf("chunked bootstrap body status=%d body=%s", chunkedResponse.Code, chunkedResponse.Body.String())
	}

	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	protected := chi.NewRouter()
	protected.Use(handler.Middleware)
	handler.ProtectedRoutes(protected)
	for _, endpoint := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "rotate", method: http.MethodPost, path: "/auth/session/rotate"},
		{name: "revoke session", method: http.MethodDelete, path: "/auth/session"},
		{name: "revoke api token", method: http.MethodDelete, path: "/auth/api-tokens/a1000000-0000-4000-8000-000000000099"},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`))
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: credential.Token})
			request.Header.Set("Origin", testOrigin)
			request.Header.Set(CSRFHeaderName, credential.CSRFToken)
			response := httptest.NewRecorder()
			protected.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), application.ErrorCodeInvalid) || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}

func TestBootstrapRejectsUnknownLengthBodyWithoutReading(t *testing.T) {
	handler, _ := newTestHandler(t)
	router := chi.NewRouter()
	handler.OpenRoutes(router)

	reader, writer := io.Pipe()
	defer writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/auth/sessions", reader)
	request.ContentLength = -1
	request.Header.Set("Authorization", "Bearer bootstrap-token-with-at-least-32-bytes")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(response, request)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		_ = writer.Close()
		<-done
		t.Fatal("unknown-length body blocked the authentication handler")
	}
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), application.ErrorCodeInvalid) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNewHandlerRejectsNonCanonicalAllowedOrigins(t *testing.T) {
	_, service := newTestHandler(t)
	for _, origin := range []string{
		"http://example.test:80",
		"https://example.test:443",
		"https://EXAMPLE.test",
		"http://127.000.000.001",
		"http://[0:0:0:0:0:0:0:1]:8080",
		"https://b\u00fccher.example",
	} {
		t.Run(origin, func(t *testing.T) {
			if _, err := NewHandler(service, Options{SecureCookie: true, AllowedOrigins: []string{origin}}); err == nil {
				t.Fatalf("NewHandler accepted non-canonical allowed origin %q", origin)
			}
		})
	}
}

func TestRequiredCapabilityDoesNotTreatGraphCommandsAsReadQueries(t *testing.T) {
	tests := []struct {
		path   string
		method string
		want   []capability.Capability
	}{
		{path: "/api/v1/graph/global", method: http.MethodPost, want: []capability.Capability{capability.ReadLocal}},
		{path: "/api/v1/collections/preview", method: http.MethodPost, want: []capability.Capability{capability.ReadLocal}},
		{path: "/api/v1/graph/candidates/10000000-0000-4000-8000-000000000001/decisions", method: http.MethodPost, want: []capability.Capability{capability.WriteProposal}},
		{path: "/api/v1/graph/candidate-scans", method: http.MethodPost, want: []capability.Capability{capability.ReadLocal, capability.WriteProposal}},
		{path: "/api/v1/source-versions/10000000-0000-4000-8000-000000000001/ingestion-attempts", method: http.MethodPost, want: []capability.Capability{capability.ReadLocal, capability.IndexMaintenance}},
		{path: "/api/v1/health/schedules", method: http.MethodPut, want: []capability.Capability{capability.ReadLocal, capability.WriteProposal}},
		{path: "/api/v1/workspaces", method: http.MethodPost, want: []capability.Capability{capability.WriteProposal}},
		{path: "/api/v1/workspaces/10000000-0000-4000-8000-000000000001/workflows", method: http.MethodPost, want: []capability.Capability{capability.WriteProposal}},
		{path: "/api/v1/workflows/10000000-0000-4000-8000-000000000001/pause", method: http.MethodPost, want: []capability.Capability{capability.WriteProposal}},
		{path: "/api/v1/proposals/10000000-0000-4000-8000-000000000001/approvals", method: http.MethodPost, want: []capability.Capability{capability.WriteKnowledge}},
		{path: "/api/v1/health/scans", method: http.MethodPost, want: []capability.Capability{capability.ReadLocal, capability.WriteProposal}},
		{path: "/api/v1/conversations/10000000-0000-4000-8000-000000000001/questions", method: http.MethodPost, want: []capability.Capability{capability.ReadLocal}},
		{path: "/api/v1/workspaces/10000000-0000-4000-8000-000000000001/timeline", method: http.MethodGet, want: []capability.Capability{capability.ReadLocal}},
		{path: "/api/v1/workspaces/10000000-0000-4000-8000-000000000001/timeline/10000000-0000-4000-8000-000000000002", method: http.MethodGet, want: []capability.Capability{capability.ReadLocal}},
		{path: "/api/v1/workspaces/10000000-0000-4000-8000-000000000001/impact-reports/10000000-0000-4000-8000-000000000002", method: http.MethodGet, want: []capability.Capability{capability.ReadLocal}},
		{path: "/api/v1/workspaces/10000000-0000-4000-8000-000000000001/timeline/10000000-0000-4000-8000-000000000002/impact-analysis", method: http.MethodPost, want: []capability.Capability{capability.WriteProposal}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, test.want) {
				t.Fatalf("capabilities=%q want=%q", got, test.want)
			}
		})
	}
	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/new-state-changing-endpoint", nil)
	if got := RequiredCapabilities(unknown); !slices.Equal(got, []capability.Capability{capability.WriteKnowledge}) {
		t.Fatalf("unknown mutation capabilities=%q want fail-closed %q", got, capability.WriteKnowledge)
	}
	for _, path := range []string{"/api/v1/auth/session", "/api/v1/auth/api-tokens/10000000-0000-4000-8000-000000000001"} {
		request := httptest.NewRequest(http.MethodDelete, path, nil)
		if got := RequiredCapabilities(request); len(got) != 0 {
			t.Fatalf("auth management path %s capabilities=%q want empty", path, got)
		}
	}
}

func TestMiddlewareEnforcesOriginCSRFAndCapability(t *testing.T) {
	handler, service := newTestHandler(t)
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(handler.Middleware)
	router.Get("/read", func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := PrincipalFromContext(request.Context()); !ok {
			t.Fatal("principal missing")
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	router.Post("/write", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })

	read := httptest.NewRequest(http.MethodGet, "/read", nil)
	read.AddCookie(&http.Cookie{Name: SessionCookieName, Value: credential.Token})
	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusNoContent {
		t.Fatalf("read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	for name, setup := range map[string]func(*http.Request){
		"missing origin": func(_ *http.Request) {},
		"missing csrf":   func(request *http.Request) { request.Header.Set("Origin", testOrigin) },
		"padded origin": func(request *http.Request) {
			request.Header.Set("Origin", " "+testOrigin+" ")
			request.Header.Set(CSRFHeaderName, credential.CSRFToken)
		},
		"duplicate origin": func(request *http.Request) {
			request.Header.Add("Origin", testOrigin)
			request.Header.Add("Origin", testOrigin)
			request.Header.Set(CSRFHeaderName, credential.CSRFToken)
		},
		"wrong csrf": func(request *http.Request) {
			request.Header.Set("Origin", testOrigin)
			request.Header.Set(CSRFHeaderName, "wrong")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/write", nil)
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: credential.Token})
			setup(request)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), application.ErrorCodeCSRF) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	validWrite := httptest.NewRequest(http.MethodPost, "/write", nil)
	validWrite.AddCookie(&http.Cookie{Name: SessionCookieName, Value: credential.Token})
	validWrite.Header.Set("Origin", testOrigin)
	validWrite.Header.Set(CSRFHeaderName, credential.CSRFToken)
	validResponse := httptest.NewRecorder()
	router.ServeHTTP(validResponse, validWrite)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("valid write status=%d body=%s", validResponse.Code, validResponse.Body.String())
	}
}

func TestCurrentSessionHonorsBearerPriority(t *testing.T) {
	handler, service := newTestHandler(t)
	session, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(context.Background(), session.Token, session.CSRFToken, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateAPIToken(context.Background(), owner, "read-only", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(handler.Middleware)
	handler.ProtectedRoutes(router)

	request := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	request.Header.Set("Authorization", "Bearer "+token.Plain)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), application.ErrorCodeForbidden) {
		t.Fatalf("mixed bearer/session status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMiddlewareRejectsDuplicateAuthorizationAndSupportsScopedBearer(t *testing.T) {
	handler, service := newTestHandler(t)
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(context.Background(), credential.Token, credential.CSRFToken, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateAPIToken(context.Background(), owner, "read-only", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(handler.Middleware)
	router.Get("/api/v1/read", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	router.Post("/api/v1/write", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })

	duplicate := httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
	duplicate.Header.Add("Authorization", "Bearer "+token.Plain)
	duplicate.Header.Add("Authorization", "Bearer "+token.Plain)
	duplicateResponse := httptest.NewRecorder()
	router.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate authorization status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
	blankAuthorization := httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
	blankAuthorization.Header.Set("Authorization", "")
	blankAuthorization.AddCookie(&http.Cookie{Name: SessionCookieName, Value: credential.Token})
	blankAuthorizationResponse := httptest.NewRecorder()
	router.ServeHTTP(blankAuthorizationResponse, blankAuthorization)
	if blankAuthorizationResponse.Code != http.StatusUnauthorized {
		t.Fatalf("blank authorization status=%d body=%s", blankAuthorizationResponse.Code, blankAuthorizationResponse.Body.String())
	}

	read := httptest.NewRequest(http.MethodGet, "/api/v1/read", nil)
	read.Header.Set("Authorization", "Bearer "+token.Plain)
	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusNoContent {
		t.Fatalf("scoped read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	write := httptest.NewRequest(http.MethodPost, "/api/v1/write", nil)
	write.Header.Set("Authorization", "Bearer "+token.Plain)
	writeResponse := httptest.NewRecorder()
	router.ServeHTTP(writeResponse, write)
	if writeResponse.Code != http.StatusForbidden || !strings.Contains(writeResponse.Body.String(), application.ErrorCodeForbidden) {
		t.Fatalf("scope escalation status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}
}

func TestRevokeMissingAPITokenReturnsNotFoundWithoutInvalidatingSession(t *testing.T) {
	handler, service := newTestHandler(t)
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(handler.Middleware)
	handler.ProtectedRoutes(router)

	request := httptest.NewRequest(http.MethodDelete, "/auth/api-tokens/a1000000-0000-4000-8000-000000000099", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: credential.Token})
	request.Header.Set("Origin", testOrigin)
	request.Header.Set(CSRFHeaderName, credential.CSRFToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), application.ErrorCodeAPITokenNotFound) {
		t.Fatalf("missing api token status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("WWW-Authenticate") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing api token headers=%v", response.Header())
	}
}
