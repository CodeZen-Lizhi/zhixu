//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

const integrationBootstrapToken = "auth-integration-bootstrap-token-with-at-least-32-characters"

func TestServicePostgreSQLCredentialLifecycleUsesDatabaseClockAndNeverPersistsPlaintext(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	prefix := "a7100001"
	prepareAuthPrefix(t, ctx, pool, prefix)

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Check(ctx); err != nil {
		t.Fatal(err)
	}
	ids := &authIntegrationIDs{prefix: prefix}
	databaseReference := databaseNow(t, ctx, pool)
	applicationClocks := []time.Time{
		databaseReference.Add(-20 * 365 * 24 * time.Hour),
		databaseReference.Add(20 * 365 * 24 * time.Hour),
	}
	plaintext := []string{integrationBootstrapToken}
	for index, applicationClock := range applicationClocks {
		service := newAuthIntegrationService(t, repository, ids, applicationClock)
		beforeIssue := databaseNow(t, ctx, pool)
		session, err := service.ExchangeBootstrap(ctx, integrationBootstrapToken)
		if err != nil {
			t.Fatal(err)
		}
		afterIssue := databaseNow(t, ctx, pool)
		assertDatabaseIssueTime(t, "session", session.Session.CreatedAt, session.Session.ExpiresAt, beforeIssue, afterIssue, time.Hour)
		if session.Session.TokenHash != credentialDigest(session.Token) || session.Session.CSRFHash != credentialDigest(session.CSRFToken) {
			t.Fatal("session response digests do not match returned plaintext credentials")
		}
		var storedSessionTokenHash, storedCSRFHash string
		if err := pool.QueryRow(ctx, `SELECT token_hash,csrf_hash FROM auth.session WHERE id=$1`, string(session.Session.ID)).
			Scan(&storedSessionTokenHash, &storedCSRFHash); err != nil {
			t.Fatal(err)
		}
		if storedSessionTokenHash != credentialDigest(session.Token) || storedCSRFHash != credentialDigest(session.CSRFToken) {
			t.Fatal("database session digests do not match returned plaintext credentials")
		}

		owner, err := service.AuthenticateSession(ctx, session.Token, session.CSRFToken, true)
		if err != nil {
			t.Fatal(err)
		}
		beforeTokenIssue := databaseNow(t, ctx, pool)
		token, err := service.CreateAPIToken(ctx, owner, fmt.Sprintf("clock-skew-%d", index), []capability.Capability{capability.ReadLocal}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		afterTokenIssue := databaseNow(t, ctx, pool)
		assertDatabaseIssueTime(t, "api token", token.Token.CreatedAt, token.Token.ExpiresAt, beforeTokenIssue, afterTokenIssue, time.Hour)
		if token.Token.TokenHash != credentialDigest(token.Plain) {
			t.Fatal("api token response digest does not match returned plaintext credential")
		}
		var storedTokenHash string
		if err := pool.QueryRow(ctx, `SELECT token_hash FROM auth.api_token WHERE id=$1`, string(token.Token.ID)).Scan(&storedTokenHash); err != nil {
			t.Fatal(err)
		}
		if storedTokenHash != credentialDigest(token.Plain) {
			t.Fatal("database api token digest does not match returned plaintext credential")
		}
		plaintext = append(plaintext, session.Token, session.CSRFToken, token.Plain)
	}

	var persistedRows string
	if err := pool.QueryRow(ctx, `SELECT concat(
		COALESCE((SELECT jsonb_agg(to_jsonb(session_row))::text FROM auth.session AS session_row WHERE left(id::text,8)=$1),''),
		COALESCE((SELECT jsonb_agg(to_jsonb(token_row))::text FROM auth.api_token AS token_row WHERE left(id::text,8)=$1),'')
	)`, prefix).Scan(&persistedRows); err != nil {
		t.Fatal(err)
	}
	for _, secret := range plaintext {
		if strings.Contains(persistedRows, secret) {
			t.Fatal("bootstrap, session, csrf, or api token plaintext was persisted")
		}
	}
}

func TestServicePostgreSQLAuthenticateRevokeRacesFailClosedAfterCommit(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	prefix := "a7100002"
	prepareAuthPrefix(t, ctx, pool, prefix)

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newAuthIntegrationService(t, repository, &authIntegrationIDs{prefix: prefix}, databaseNow(t, ctx, pool))
	ownerSession, err := service.ExchangeBootstrap(ctx, integrationBootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(ctx, ownerSession.Token, ownerSession.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}

	const raceRounds = 20
	for round := 0; round < raceRounds; round++ {
		token, err := service.CreateAPIToken(ctx, owner, fmt.Sprintf("revoke-race-%02d", round), []capability.Capability{capability.ReadLocal}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		authErr, revokeErr := runConcurrentErrors(
			func() error {
				_, authenticateErr := service.AuthenticateAPIToken(ctx, token.Plain)
				return authenticateErr
			},
			func() error { return service.RevokeAPIToken(ctx, owner, token.Token.ID) },
		)
		assertAllowedRaceAuthenticationResult(t, "api token", authErr)
		if revokeErr != nil {
			t.Fatalf("api token revoke round %d: %v", round, revokeErr)
		}
		if _, err := service.AuthenticateAPIToken(ctx, token.Plain); errorCode(err) != application.ErrorCodeUnauthorized {
			t.Fatalf("api token authenticated after revoke commit in round %d: code=%s err=%v", round, errorCode(err), err)
		}
	}

	for round := 0; round < raceRounds; round++ {
		session, err := service.ExchangeBootstrap(ctx, integrationBootstrapToken)
		if err != nil {
			t.Fatal(err)
		}
		principal, err := service.AuthenticateSession(ctx, session.Token, session.CSRFToken, true)
		if err != nil {
			t.Fatal(err)
		}
		authErr, revokeErr := runConcurrentErrors(
			func() error {
				_, authenticateErr := service.AuthenticateSession(ctx, session.Token, session.CSRFToken, true)
				return authenticateErr
			},
			func() error { return service.RevokeSession(ctx, principal, session.Session.ID) },
		)
		assertAllowedRaceAuthenticationResult(t, "session", authErr)
		if revokeErr != nil {
			t.Fatalf("session revoke round %d: %v", round, revokeErr)
		}
		if _, err := service.AuthenticateSession(ctx, session.Token, session.CSRFToken, true); errorCode(err) != application.ErrorCodeUnauthorized {
			t.Fatalf("session authenticated after revoke commit in round %d: code=%s err=%v", round, errorCode(err), err)
		}
	}
}

func TestServicePostgreSQLConcurrentSessionRotationAllowsOneWinner(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	prefix := "a7100003"
	prepareAuthPrefix(t, ctx, pool, prefix)

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newAuthIntegrationService(t, repository, &authIntegrationIDs{prefix: prefix}, databaseNow(t, ctx, pool))
	const rotationRounds = 20
	for round := 0; round < rotationRounds; round++ {
		old, err := service.ExchangeBootstrap(ctx, integrationBootstrapToken)
		if err != nil {
			t.Fatal(err)
		}
		principal, err := service.AuthenticateSession(ctx, old.Token, old.CSRFToken, true)
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		results := make(chan sessionRotationResult, 2)
		for contender := 0; contender < 2; contender++ {
			go func() {
				<-start
				credential, rotateErr := service.RotateSession(ctx, principal, old.Token, old.CSRFToken, true)
				results <- sessionRotationResult{credential: credential, err: rotateErr}
			}()
		}
		close(start)
		first, second := <-results, <-results
		outcomes := []sessionRotationResult{first, second}
		var winner *application.SessionCredential
		failures := 0
		for index := range outcomes {
			outcome := outcomes[index]
			if outcome.err == nil {
				if winner != nil {
					t.Fatalf("two rotations succeeded in round %d", round)
				}
				winner = &outcome.credential
				continue
			}
			failures++
			if errorCode(outcome.err) != application.ErrorCodeUnauthorized {
				t.Fatalf("unexpected losing rotation error in round %d: code=%s err=%v", round, errorCode(outcome.err), outcome.err)
			}
		}
		if winner == nil || failures != 1 {
			t.Fatalf("rotation round %d did not produce exactly one winner and one loser", round)
		}
		if _, err := service.AuthenticateSession(ctx, old.Token, old.CSRFToken, true); errorCode(err) != application.ErrorCodeUnauthorized {
			t.Fatalf("old session remained valid after rotation round %d: code=%s err=%v", round, errorCode(err), err)
		}
		if _, err := service.AuthenticateSession(ctx, winner.Token, winner.CSRFToken, true); err != nil {
			t.Fatalf("winning session was not usable in round %d: %v", round, err)
		}
	}

	var rowCount, activeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE revoked_at IS NULL)
		FROM auth.session WHERE left(id::text,8)=$1`, prefix).Scan(&rowCount, &activeCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != rotationRounds*2 || activeCount != rotationRounds {
		t.Fatalf("unexpected rotation persistence rows=%d active=%d", rowCount, activeCount)
	}
}

func TestServicePostgreSQLAPITokenKeysetPaginationUsesUUIDTieBreaker(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	prefix := "a7100004"
	prepareAuthPrefix(t, ctx, pool, prefix)

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newAuthIntegrationService(t, repository, &authIntegrationIDs{prefix: prefix}, databaseNow(t, ctx, pool))
	session, err := service.ExchangeBootstrap(ctx, integrationBootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(ctx, session.Token, session.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}

	expectedIDs := make([]string, 0, 101)
	for index := 0; index < 101; index++ {
		token, err := service.CreateAPIToken(ctx, owner, fmt.Sprintf("page-%03d", index), []capability.Capability{capability.ReadLocal}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		expectedIDs = append(expectedIDs, string(token.Token.ID))
	}
	tieTime := databaseNow(t, ctx, pool).Truncate(time.Microsecond)
	tag, err := pool.Exec(ctx, `UPDATE auth.api_token
		SET created_at=$2::timestamptz,expires_at=$2::timestamptz + INTERVAL '1 hour'
		WHERE left(id::text,8)=$1`, prefix, tieTime)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 101 {
		t.Fatalf("expected 101 token timestamps to be normalized, got %d", tag.RowsAffected())
	}

	first, err := service.ListAPITokens(ctx, owner, domain.APITokenListQuery{Limit: domain.MaxAPITokenListLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 100 || !first.HasMore {
		t.Fatalf("unexpected first page length=%d has_more=%v", len(first.Items), first.HasMore)
	}
	cursorTime := first.Items[len(first.Items)-1].CreatedAt
	second, err := service.ListAPITokens(ctx, owner, domain.APITokenListQuery{
		CursorTime: &cursorTime,
		CursorID:   first.Items[len(first.Items)-1].ID,
		Limit:      domain.MaxAPITokenListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.HasMore {
		t.Fatalf("unexpected second page length=%d has_more=%v", len(second.Items), second.HasMore)
	}

	actualIDs := make([]string, 0, 101)
	seen := make(map[foundation.ID]struct{}, 101)
	for _, item := range append(first.Items, second.Items...) {
		if !item.CreatedAt.Equal(tieTime) {
			t.Fatalf("api token %s did not retain the shared created_at tie", item.ID)
		}
		if _, exists := seen[item.ID]; exists {
			t.Fatalf("api token %s appeared on more than one page", item.ID)
		}
		seen[item.ID] = struct{}{}
		actualIDs = append(actualIDs, string(item.ID))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(expectedIDs)))
	if len(actualIDs) != len(expectedIDs) {
		t.Fatalf("pagination returned %d ids, expected %d", len(actualIDs), len(expectedIDs))
	}
	for index := range expectedIDs {
		if actualIDs[index] != expectedIDs[index] {
			t.Fatalf("uuid tie-break order mismatch at index %d", index)
		}
	}
}

func TestServicePostgreSQLAPITokenExpiredRevokedAndCorruptScopesFailClosed(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	prefix := "a7100005"
	prepareAuthPrefix(t, ctx, pool, prefix)

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newAuthIntegrationService(t, repository, &authIntegrationIDs{prefix: prefix}, databaseNow(t, ctx, pool))
	session, err := service.ExchangeBootstrap(ctx, integrationBootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.AuthenticateSession(ctx, session.Token, session.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}

	expired, err := service.CreateAPIToken(ctx, owner, "expired", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE auth.api_token
		SET created_at=CURRENT_TIMESTAMP - INTERVAL '2 hours',expires_at=CURRENT_TIMESTAMP - INTERVAL '1 hour'
		WHERE id=$1`, string(expired.Token.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, expired.Plain); errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("expired token code=%s err=%v", errorCode(err), err)
	}

	revoked, err := service.CreateAPIToken(ctx, owner, "revoked", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeAPIToken(ctx, owner, revoked.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, revoked.Plain); errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("revoked token code=%s err=%v", errorCode(err), err)
	}

	corrupt, err := service.CreateAPIToken(ctx, owner, "corrupt-scope", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE auth.api_token SET scopes='["NOT_A_CAPABILITY"]'::jsonb WHERE id=$1`, string(corrupt.Token.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, corrupt.Plain); errorCode(err) != application.ErrorCodeUnavailable {
		t.Fatalf("corrupt-scope token code=%s err=%v", errorCode(err), err)
	}
}

func TestRepositoryPostgreSQLAuthLifecycleAndFailClosedReads(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	cleanupAuthRows(t, ctx, pool)
	defer cleanupAuthRows(t, ctx, pool)

	now := databaseNow(t, ctx, pool)
	scopes, err := domain.CanonicalScopes([]capability.Capability{capability.WriteProposal, capability.ReadLocal})
	if err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "d1000000-0000-4000-8000-000000000001", TokenHash: strings.Repeat("a", 64), CSRFHash: strings.Repeat("b", 64),
		UserLabel: "owner", Scopes: scopes, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	persistedSession, err := repository.CreateSession(ctx, sessionIssue(session), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var persistedTokenHash, persistedCSRFHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash,csrf_hash FROM auth.session WHERE id=$1`, string(session.ID)).Scan(&persistedTokenHash, &persistedCSRFHash); err != nil {
		t.Fatal(err)
	}
	if persistedTokenHash != session.TokenHash || persistedCSRFHash != session.CSRFHash || persistedTokenHash == "plaintext-session" || persistedCSRFHash == "plaintext-csrf" {
		t.Fatalf("unexpected persisted credential material token=%q csrf=%q", persistedTokenHash, persistedCSRFHash)
	}

	authenticated, err := repository.AuthenticateSession(ctx, session.TokenHash)
	if err != nil || authenticated.ID != session.ID || authenticated.LastSeenAt.Before(persistedSession.LastSeenAt) {
		t.Fatalf("authenticated session=%#v err=%v", authenticated, err)
	}

	expired := session
	expired.ID = "d1000000-0000-4000-8000-000000000002"
	expired.TokenHash = strings.Repeat("c", 64)
	expired.CSRFHash = strings.Repeat("d", 64)
	expired.CreatedAt = now.Add(-2 * time.Hour)
	expired.LastSeenAt = expired.CreatedAt
	expired.ExpiresAt = now.Add(-time.Minute)
	if err := insertSession(ctx, pool, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(ctx, expired.TokenHash); errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("expired session code=%s err=%v", errorCode(err), err)
	}

	revoked := session
	revoked.ID = "d1000000-0000-4000-8000-000000000003"
	revoked.TokenHash = strings.Repeat("e", 64)
	revoked.CSRFHash = strings.Repeat("f", 64)
	revoked.CreatedAt = now.Add(-2 * time.Hour)
	revoked.LastSeenAt = revoked.CreatedAt
	revoked.ExpiresAt = now.Add(time.Hour)
	revokedAt := now.Add(-time.Minute)
	revoked.RevokedAt = &revokedAt
	if err := insertSession(ctx, pool, revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(ctx, revoked.TokenHash); errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("revoked session code=%s err=%v", errorCode(err), err)
	}

	corrupt := session
	corrupt.ID = "d1000000-0000-4000-8000-000000000004"
	corrupt.TokenHash = strings.Repeat("1", 64)
	corrupt.CSRFHash = strings.Repeat("2", 64)
	if err := insertSession(ctx, pool, corrupt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE auth.session SET scopes='["NOT_A_SCOPE"]'::jsonb WHERE id=$1`, string(corrupt.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(ctx, corrupt.TokenHash); errorCode(err) != application.ErrorCodeUnavailable {
		t.Fatalf("corrupt scope code=%s err=%v", errorCode(err), err)
	}

	token := domain.APIToken{
		ID: "d1000000-0000-4000-8000-000000000005", TokenHash: strings.Repeat("3", 64), Name: "automation", Scopes: scopes,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	persistedToken, err := repository.CreateAPIToken(ctx, tokenIssue(token), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, err := repository.AuthenticateAPIToken(ctx, token.TokenHash)
	if err != nil || apiToken.ID != token.ID || apiToken.LastUsedAt == nil || apiToken.LastUsedAt.Before(persistedToken.CreatedAt) {
		t.Fatalf("authenticated api token=%#v err=%v", apiToken, err)
	}
	listed, hasMore, err := repository.ListAPITokens(ctx, domain.APITokenListQuery{Limit: domain.MaxAPITokenListLimit})
	if err != nil || hasMore || len(listed) != 1 || listed[0].TokenHash != token.TokenHash {
		t.Fatalf("listed tokens=%#v hasMore=%v err=%v", listed, hasMore, err)
	}
	if err := repository.RevokeAPIToken(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateAPIToken(ctx, token.TokenHash); errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("revoked api token code=%s err=%v", errorCode(err), err)
	}
	if err := repository.RevokeAPIToken(ctx, "d1000000-0000-4000-8000-000000000099"); err == nil {
		t.Fatal("missing api token revocation unexpectedly succeeded")
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNotFound || classified.Code != application.ErrorCodeAPITokenNotFound {
			t.Fatalf("missing api token revocation error=%v", err)
		}
	}

	var plaintextCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.session WHERE token_hash IN ('plaintext-session','plaintext-csrf') OR csrf_hash IN ('plaintext-session','plaintext-csrf')`).Scan(&plaintextCount); err != nil {
		t.Fatal(err)
	}
	if plaintextCount != 0 {
		t.Fatalf("plaintext credential material persisted: %d rows", plaintextCount)
	}
}

func TestRepositoryPostgreSQLSessionRotationIsAtomicAndConcurrentAuthenticationUpdatesLastSeen(t *testing.T) {
	pool := authIntegrationPool(t)
	ctx := context.Background()
	cleanupAuthRows(t, ctx, pool)
	defer cleanupAuthRows(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := databaseNow(t, ctx, pool)
	scopes := []capability.Capability{capability.ReadLocal}
	old := domain.Session{ID: "d2000000-0000-4000-8000-000000000001", TokenHash: strings.Repeat("4", 64), CSRFHash: strings.Repeat("5", 64), UserLabel: "owner", Scopes: scopes, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	persistedOld, err := repository.CreateSession(ctx, sessionIssue(old), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for index := 1; index <= 4; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, authErr := repository.AuthenticateSession(ctx, old.TokenHash); authErr != nil {
				t.Errorf("concurrent authenticate %d: %v", index, authErr)
			}
		}()
	}
	wg.Wait()
	var lastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM auth.session WHERE id=$1`, string(old.ID)).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen.Before(persistedOld.CreatedAt) {
		t.Fatalf("last_seen_at did not retain the latest atomic update: %s", lastSeen)
	}

	rotated := old
	rotated.ID = "d2000000-0000-4000-8000-000000000002"
	rotated.TokenHash = strings.Repeat("6", 64)
	rotated.CSRFHash = strings.Repeat("7", 64)
	rotated.CreatedAt = now.Add(10 * time.Minute)
	rotated.LastSeenAt = rotated.CreatedAt
	rotated.ExpiresAt = rotated.CreatedAt.Add(time.Hour)
	if _, err := repository.RotateSession(ctx, old.ID, sessionIssue(rotated), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(ctx, old.TokenHash); errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("old session remained usable code=%s err=%v", errorCode(err), err)
	}
	if _, err := repository.AuthenticateSession(ctx, rotated.TokenHash); err != nil {
		t.Fatalf("rotated session unavailable: %v", err)
	}

	duplicate := rotated
	duplicate.ID = "d2000000-0000-4000-8000-000000000003"
	duplicate.CSRFHash = strings.Repeat("8", 64)
	duplicate.CreatedAt = now.Add(20 * time.Minute)
	duplicate.LastSeenAt = duplicate.CreatedAt
	duplicate.ExpiresAt = duplicate.CreatedAt.Add(time.Hour)
	if _, err := repository.RotateSession(ctx, rotated.ID, sessionIssue(duplicate), time.Hour); errorCode(err) == "" {
		t.Fatal("duplicate token hash rotation unexpectedly succeeded")
	}
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth.session WHERE id=$1`, string(rotated.ID)).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt != nil {
		t.Fatalf("failed rotation committed revocation at %v", revokedAt)
	}
	var duplicateCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.session WHERE id=$1`, string(duplicate.ID)).Scan(&duplicateCount); err != nil {
		t.Fatal(err)
	}
	if duplicateCount != 0 {
		t.Fatalf("failed rotation inserted a new session: %d", duplicateCount)
	}
}

type authIntegrationIDs struct {
	mu     sync.Mutex
	prefix string
	next   uint64
}

func (generator *authIntegrationIDs) New() (foundation.ID, error) {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	generator.next++
	return foundation.ParseID(fmt.Sprintf("%s-0000-4000-8000-%012x", generator.prefix, generator.next))
}

type sessionRotationResult struct {
	credential application.SessionCredential
	err        error
}

func newAuthIntegrationService(t *testing.T, repository *Repository, ids foundation.IDGenerator, clock time.Time) *application.Service {
	t.Helper()
	service, err := application.NewService(repository, ids, foundation.FixedClock{Value: clock}, application.Options{
		BootstrapToken: integrationBootstrapToken,
		SessionTTL:     time.Hour,
		APITokenTTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func prepareAuthPrefix(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	if err := deleteAuthPrefix(ctx, pool, prefix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := deleteAuthPrefix(cleanupCtx, pool, prefix); err != nil {
			t.Errorf("cleanup auth integration prefix %s: %v", prefix, err)
		}
	})
}

func deleteAuthPrefix(ctx context.Context, pool *pgxpool.Pool, prefix string) error {
	if _, err := pool.Exec(ctx, `DELETE FROM auth.api_token WHERE left(id::text,8)=$1`, prefix); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM auth.session WHERE left(id::text,8)=$1`, prefix); err != nil {
		return err
	}
	return nil
}

func assertDatabaseIssueTime(t *testing.T, kind string, createdAt, expiresAt, beforeIssue, afterIssue time.Time, ttl time.Duration) {
	t.Helper()
	if createdAt.Before(beforeIssue) || createdAt.After(afterIssue) {
		t.Fatalf("%s created_at was not sourced from PostgreSQL current time", kind)
	}
	if expiresAt.Sub(createdAt) != ttl.Truncate(time.Microsecond) {
		t.Fatalf("%s expiry did not use the requested database ttl", kind)
	}
}

func credentialDigest(plain string) string {
	digest := sha256.Sum256([]byte(plain))
	return fmt.Sprintf("%x", digest[:])
}

func runConcurrentErrors(first, second func() error) (error, error) {
	start := make(chan struct{})
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() {
		<-start
		firstResult <- first()
	}()
	go func() {
		<-start
		secondResult <- second()
	}()
	close(start)
	return <-firstResult, <-secondResult
}

func assertAllowedRaceAuthenticationResult(t *testing.T, kind string, err error) {
	t.Helper()
	if err != nil && errorCode(err) != application.ErrorCodeUnauthorized {
		t.Fatalf("unexpected %s authenticate-vs-revoke result: code=%s err=%v", kind, errorCode(err), err)
	}
}

func authIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func cleanupAuthRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM auth.api_token WHERE id::text LIKE 'd1%00000000000%' OR id::text LIKE 'd2%00000000000%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM auth.session WHERE id::text LIKE 'd1%00000000000%' OR id::text LIKE 'd2%00000000000%'`); err != nil {
		t.Fatal(err)
	}
}

func databaseNow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

func sessionIssue(session domain.Session) domain.SessionIssue {
	return domain.SessionIssue{
		ID: session.ID, TokenHash: session.TokenHash, CSRFHash: session.CSRFHash,
		UserLabel: session.UserLabel, Scopes: append([]capability.Capability(nil), session.Scopes...),
	}
}

func tokenIssue(token domain.APIToken) domain.APITokenIssue {
	return domain.APITokenIssue{
		ID: token.ID, TokenHash: token.TokenHash, Name: token.Name,
		Scopes: append([]capability.Capability(nil), token.Scopes...),
	}
}

func insertSession(ctx context.Context, pool *pgxpool.Pool, session domain.Session) error {
	scopes, err := json.Marshal(session.Scopes)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `INSERT INTO auth.session(
		id,token_hash,csrf_hash,user_label,scopes,created_at,last_seen_at,expires_at,revoked_at
	) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9)`,
		string(session.ID), session.TokenHash, session.CSRFHash, session.UserLabel, scopes,
		session.CreatedAt, session.LastSeenAt, session.ExpiresAt, session.RevokedAt)
	return err
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
