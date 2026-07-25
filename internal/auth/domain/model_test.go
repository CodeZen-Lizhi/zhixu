package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalScopesSortsCopiesAndRejectsDuplicates(t *testing.T) {
	input := []capability.Capability{capability.WriteProposal, capability.ReadLocal}
	got, err := CanonicalScopes(input)
	if err != nil || len(got) != 2 || got[0] != capability.ReadLocal || got[1] != capability.WriteProposal {
		t.Fatalf("scopes=%#v err=%v", got, err)
	}
	got[0] = capability.EvaluationRun
	if input[0] != capability.WriteProposal {
		t.Fatal("canonical scopes aliased caller storage")
	}
	if _, err := CanonicalScopes([]capability.Capability{capability.ReadLocal, capability.ReadLocal}); err == nil {
		t.Fatal("duplicate scopes were accepted")
	}
}

func TestSessionAndTokenValidation(t *testing.T) {
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	session := Session{
		ID: "a0000000-0000-4000-8000-000000000001", TokenHash: strings.Repeat("a", 64),
		CSRFHash: strings.Repeat("b", 64), UserLabel: "owner", Scopes: []capability.Capability{capability.ReadLocal},
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := ValidateSession(session); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSessionIssue(SessionIssue{
		ID: session.ID, TokenHash: session.TokenHash, CSRFHash: session.CSRFHash,
		UserLabel: session.UserLabel, Scopes: session.Scopes,
	}); err != nil {
		t.Fatal(err)
	}
	session.Scopes = []capability.Capability{capability.WriteProposal, capability.ReadLocal}
	if err := ValidateSession(session); err == nil {
		t.Fatal("non-canonical session scopes were accepted")
	}
	token := APIToken{
		ID: foundation.ID("a0000000-0000-4000-8000-000000000002"), TokenHash: strings.Repeat("c", 64), Name: "automation",
		Scopes: []capability.Capability{capability.ReadLocal}, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := ValidateAPIToken(token); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAPITokenIssue(APITokenIssue{ID: token.ID, TokenHash: token.TokenHash, Name: token.Name, Scopes: token.Scopes}); err != nil {
		t.Fatal(err)
	}
}
