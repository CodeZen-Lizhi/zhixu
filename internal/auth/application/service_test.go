package application

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type fakeRepository struct {
	sessions    map[string]domain.Session
	tokens      map[string]domain.APIToken
	currentTime time.Time
}

type corruptIssueRepository struct {
	fakeRepository
	corruptSession bool
	corruptToken   bool
}

func (repository *corruptIssueRepository) CreateSession(ctx context.Context, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
	session, err := repository.fakeRepository.CreateSession(ctx, issue, ttl)
	if err == nil && repository.corruptSession {
		session.UserLabel = "different-owner"
	}
	return session, err
}

func (repository *corruptIssueRepository) CreateAPIToken(ctx context.Context, issue domain.APITokenIssue, ttl time.Duration) (domain.APIToken, error) {
	token, err := repository.fakeRepository.CreateAPIToken(ctx, issue, ttl)
	if err == nil && repository.corruptToken {
		token.Scopes = []capability.Capability{capability.WriteKnowledge}
	}
	return token, err
}

func (repository *fakeRepository) CreateSession(_ context.Context, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
	now := repository.now()
	session := domain.Session{ID: issue.ID, TokenHash: issue.TokenHash, CSRFHash: issue.CSRFHash, UserLabel: issue.UserLabel, Scopes: issue.Scopes, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(ttl)}
	if repository.sessions == nil {
		repository.sessions = map[string]domain.Session{}
	}
	repository.sessions[session.TokenHash] = session
	return session, nil
}

func (repository *fakeRepository) RotateSession(_ context.Context, previousID foundation.ID, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
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
	return domain.Session{}, errors.New("missing")
}

func (repository *fakeRepository) AuthenticateSession(_ context.Context, hash string) (domain.Session, error) {
	at := repository.now()
	session, ok := repository.sessions[hash]
	if !ok || session.RevokedAt != nil || !session.ExpiresAt.After(at) {
		return domain.Session{}, unauthorized(errors.New("not found"))
	}
	session.LastSeenAt = at
	repository.sessions[hash] = session
	return session, nil
}

func (repository *fakeRepository) RevokeSession(_ context.Context, id foundation.ID) error {
	at := repository.now()
	for hash, session := range repository.sessions {
		if session.ID == id {
			session.RevokedAt = &at
			repository.sessions[hash] = session
			return nil
		}
	}
	return errors.New("missing")
}

func (repository *fakeRepository) CreateAPIToken(_ context.Context, issue domain.APITokenIssue, ttl time.Duration) (domain.APIToken, error) {
	now := repository.now()
	token := domain.APIToken{ID: issue.ID, TokenHash: issue.TokenHash, Name: issue.Name, Scopes: issue.Scopes, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if repository.tokens == nil {
		repository.tokens = map[string]domain.APIToken{}
	}
	repository.tokens[token.TokenHash] = token
	return token, nil
}

func (repository *fakeRepository) ListAPITokens(_ context.Context, _ domain.APITokenListQuery) ([]domain.APIToken, bool, error) {
	result := make([]domain.APIToken, 0, len(repository.tokens))
	for _, token := range repository.tokens {
		result = append(result, token)
	}
	return result, false, nil
}

func (repository *fakeRepository) AuthenticateAPIToken(_ context.Context, hash string) (domain.APIToken, error) {
	at := repository.now()
	token, ok := repository.tokens[hash]
	if !ok || token.RevokedAt != nil || !token.ExpiresAt.After(at) {
		return domain.APIToken{}, unauthorized(errors.New("not found"))
	}
	token.LastUsedAt = &at
	repository.tokens[hash] = token
	return token, nil
}

func (repository *fakeRepository) RevokeAPIToken(_ context.Context, id foundation.ID) error {
	at := repository.now()
	for hash, token := range repository.tokens {
		if token.ID == id {
			token.RevokedAt = &at
			repository.tokens[hash] = token
			return nil
		}
	}
	return errors.New("missing")
}

func (repository *fakeRepository) now() time.Time {
	if !repository.currentTime.IsZero() {
		return repository.currentTime
	}
	return time.Now().UTC()
}

type sequenceIDs struct{ next int }

func (generator *sequenceIDs) New() (foundation.ID, error) {
	generator.next++
	return foundation.ID("a0000000-0000-4000-8000-00000000000" + string(rune('0'+generator.next))), nil
}

func TestBootstrapSessionCSRFAndAPITokenLifecycle(t *testing.T) {
	repository := &fakeRepository{}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	service, err := NewService(repository, &sequenceIDs{}, foundation.FixedClock{Value: now}, Options{
		BootstrapToken: "bootstrap-token-with-at-least-32-bytes", Random: credentialFixture(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExchangeBootstrap(context.Background(), "wrong"); errorCode(err) != ErrorCodeUnauthorized {
		t.Fatalf("wrong bootstrap code=%s err=%v", errorCode(err), err)
	}
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil || credential.Token == "" || credential.CSRFToken == "" {
		t.Fatalf("credential=%#v err=%v", credential, err)
	}
	if _, err := service.AuthenticateSession(context.Background(), credential.Token, "wrong", true); errorCode(err) != ErrorCodeCSRF {
		t.Fatalf("wrong csrf code=%s err=%v", errorCode(err), err)
	}
	principal, err := service.AuthenticateSession(context.Background(), credential.Token, credential.CSRFToken, true)
	if err != nil || principal.Kind != domain.PrincipalSession || !principal.Has(capability.WriteKnowledge) {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	automation, err := service.CreateAPIToken(context.Background(), principal, "read-only", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil || automation.Plain == "" {
		t.Fatalf("api token=%#v err=%v", automation, err)
	}
	apiPrincipal, err := service.AuthenticateAPIToken(context.Background(), automation.Plain)
	if err != nil || !apiPrincipal.Has(capability.ReadLocal) || apiPrincipal.Has(capability.WriteKnowledge) {
		t.Fatalf("api principal=%#v err=%v", apiPrincipal, err)
	}
	if err := service.RevokeAPIToken(context.Background(), principal, automation.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAPIToken(context.Background(), automation.Plain); errorCode(err) != ErrorCodeUnauthorized {
		t.Fatalf("revoked token code=%s err=%v", errorCode(err), err)
	}
}

func TestAPITokenCannotEscalateScopeOrCreateAnotherToken(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(repository, &sequenceIDs{}, foundation.FixedClock{Value: time.Now().UTC()}, Options{
		BootstrapToken: "bootstrap-token-with-at-least-32-bytes", Random: credentialFixture(9),
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(context.Background(), credential.Token, credential.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := service.CreateAPIToken(context.Background(), owner, "readonly", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateAPIToken(context.Background(), readOnly.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAPIToken(context.Background(), principal, "escalated", []capability.Capability{capability.WriteKnowledge}, time.Hour); errorCode(err) != ErrorCodeForbidden {
		t.Fatalf("escalation code=%s err=%v", errorCode(err), err)
	}
	if err := Authorize(principal, capability.WriteKnowledge); errorCode(err) != ErrorCodeForbidden {
		t.Fatalf("authorize code=%s err=%v", errorCode(err), err)
	}
}

func TestAuthenticationLifecycleUsesRepositoryClock(t *testing.T) {
	databaseNow := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{currentTime: databaseNow}
	service, err := NewService(repository, &sequenceIDs{}, foundation.FixedClock{Value: databaseNow.Add(-10 * 365 * 24 * time.Hour)}, Options{
		BootstrapToken: "bootstrap-token-with-at-least-32-bytes", SessionTTL: time.Hour, APITokenTTL: time.Hour, Random: credentialFixture(11),
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if !credential.Session.CreatedAt.Equal(databaseNow) || !credential.Session.ExpiresAt.Equal(databaseNow.Add(time.Hour)) {
		t.Fatalf("session lifecycle did not use repository clock: %#v", credential.Session)
	}
	owner, err := service.AuthenticateSession(context.Background(), credential.Token, credential.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateAPIToken(context.Background(), owner, "automation", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !token.Token.CreatedAt.Equal(databaseNow) || !token.Token.ExpiresAt.Equal(databaseNow.Add(time.Hour)) {
		t.Fatalf("api token lifecycle did not use repository clock: %#v", token.Token)
	}

	repository.currentTime = databaseNow.Add(2 * time.Hour)
	if _, err := service.AuthenticateSession(context.Background(), credential.Token, credential.CSRFToken, true); errorCode(err) != ErrorCodeUnauthorized {
		t.Fatalf("expired session used application clock code=%s err=%v", errorCode(err), err)
	}
	if _, err := service.AuthenticateAPIToken(context.Background(), token.Plain); errorCode(err) != ErrorCodeUnauthorized {
		t.Fatalf("expired api token used application clock code=%s err=%v", errorCode(err), err)
	}
}

func TestIssuedCredentialBindingMismatchFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	repository := &corruptIssueRepository{fakeRepository: fakeRepository{currentTime: now}, corruptSession: true}
	service, err := NewService(repository, &sequenceIDs{}, foundation.FixedClock{Value: now}, Options{
		BootstrapToken: "bootstrap-token-with-at-least-32-bytes", Random: credentialFixture(13),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes"); errorCode(err) != ErrorCodeUnavailable {
		t.Fatalf("corrupt session issue result code=%s err=%v", errorCode(err), err)
	}

	repository.corruptSession = false
	credential, err := service.ExchangeBootstrap(context.Background(), "bootstrap-token-with-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(context.Background(), credential.Token, credential.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}
	repository.corruptToken = true
	if _, err := service.CreateAPIToken(context.Background(), owner, "automation", []capability.Capability{capability.ReadLocal}, time.Hour); errorCode(err) != ErrorCodeUnavailable {
		t.Fatalf("corrupt api token issue result code=%s err=%v", errorCode(err), err)
	}
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func credentialFixture(seed byte) *bytes.Reader {
	buffer := make([]byte, 0, 256)
	for index := byte(0); index < 8; index++ {
		buffer = append(buffer, bytes.Repeat([]byte{seed + index}, credentialBytes)...)
	}
	return bytes.NewReader(buffer)
}
