package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestOwnerForAuthenticatedPrincipalIsStableAcrossCredentials(t *testing.T) {
	t.Parallel()

	scopes := []capability.Capability{capability.ReadLocal}
	first, err := OwnerForAuthenticatedPrincipal(authdomain.Principal{
		Kind: authdomain.PrincipalSession, ID: testID(21), Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := OwnerForAuthenticatedPrincipal(authdomain.Principal{
		Kind: authdomain.PrincipalSession, ID: testID(22), Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := OwnerForAuthenticatedPrincipal(authdomain.Principal{
		Kind: authdomain.PrincipalAPIToken, ID: testID(23), Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != rotated || first != token || first != SingleUserOwner() {
		t.Fatalf("stable owner mismatch: first=%+v rotated=%+v token=%+v", first, rotated, token)
	}
}

func TestCandidateRequiresExplicitOwnerConfirmationBeforeEffective(t *testing.T) {
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	candidate := testCandidate(t, TypePreference, nil, now)
	if IsEffective(candidate, nil, now) {
		t.Fatal("candidate must never be effective")
	}
	confirmed, err := Confirm(candidate, candidate.Owner, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmed.Status != StatusActive || confirmed.ConfirmedAt == nil || confirmed.ConfirmedBy == nil {
		t.Fatalf("Confirm() = %#v", confirmed)
	}
	if !IsEffective(confirmed, nil, now.Add(2*time.Minute)) {
		t.Fatal("confirmed active memory should be effective")
	}
}

func TestMemoryLifecycleRejectsIllegalTransitionsAndOtherOwner(t *testing.T) {
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	candidate := testCandidate(t, TypePreference, nil, now)
	other := Principal{Kind: PrincipalUser, ID: testID(9)}
	if _, err := Confirm(candidate, other, now.Add(time.Minute)); !hasCode(err, ErrorCodeOwnerDenied) {
		t.Fatalf("Confirm(other) error = %v", err)
	}
	if _, err := Pause(candidate, candidate.Owner, now.Add(time.Minute)); !hasCode(err, ErrorCodeStateConflict) {
		t.Fatalf("Pause(candidate) error = %v", err)
	}
	active, err := Confirm(candidate, candidate.Owner, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	paused, err := Pause(active, active.Owner, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if IsEffective(paused, nil, now.Add(3*time.Minute)) {
		t.Fatal("paused memory must not be effective")
	}
	deleted, err := Delete(paused, paused.Owner, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := Resume(deleted, deleted.Owner, now.Add(4*time.Minute)); !hasCode(err, ErrorCodeStateConflict) {
		t.Fatalf("Resume(deleted) error = %v", err)
	}
}

func TestEpisodicMemoryRequiresExpiryAndTaskScopeFiltersEffectiveContext(t *testing.T) {
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	if _, err := NewCandidate(CandidateInput{
		ID: testID(1), WorkspaceID: testID(2), Owner: testPrincipal(), Type: TypeEpisodic,
		Content: json.RawMessage(`{"note":"temporary"}`), Source: Source{Type: SourceAgent, Ref: "agent:turn-1"}, CreatedAt: now,
	}); !hasCode(err, ErrorCodeInvalid) {
		t.Fatalf("NewCandidate(episodic without expiry) error = %v", err)
	}
	task := testID(7)
	candidate := testCandidate(t, TypeEpisodic, &task, now)
	active, err := Confirm(candidate, candidate.Owner, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if IsEffective(active, nil, now.Add(2*time.Minute)) || !IsEffective(active, &task, now.Add(2*time.Minute)) {
		t.Fatal("task-scoped active memory must require the matching task scope")
	}
	if IsEffective(active, &task, now.Add(2*time.Hour)) {
		t.Fatal("expired memory must not be effective even before maintenance transition")
	}
}

func TestMemoryCannotBeReactivatedAfterExpiryOrConfirmedByAnotherPrincipal(t *testing.T) {
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	candidate := testCandidate(t, TypeEpisodic, nil, now)
	active, err := Confirm(candidate, candidate.Owner, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	renewedExpiry := now.Add(3 * time.Hour)
	if _, err := Edit(active, active.Owner, EditInput{
		Content: json.RawMessage(`{"note":"renewed"}`), ExpiresAt: &renewedExpiry,
	}, now.Add(2*time.Hour)); !hasCode(err, ErrorCodeExpired) {
		t.Fatalf("Edit(expired) error = %v", err)
	}

	other := Principal{Kind: PrincipalUser, ID: testID(9)}
	invalidConfirmation := CloneMemory(active)
	invalidConfirmation.ConfirmedBy = &other
	if err := ValidateMemory(invalidConfirmation); !hasCode(err, ErrorCodeInvalid) {
		t.Fatalf("ValidateMemory(other confirmer) error = %v", err)
	}
}

func TestEditPreservesSourceProvenance(t *testing.T) {
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	candidate := testCandidate(t, TypePreference, nil, now)
	edited, err := Edit(candidate, candidate.Owner, EditInput{
		Content: json.RawMessage(`{"note":"edited"}`),
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	if edited.Source != candidate.Source {
		t.Fatalf("Edit() source = %+v, want immutable %+v", edited.Source, candidate.Source)
	}
}

func TestCanonicalContentRejectsDuplicateAndEmptyObjects(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"a":1,"a":2}`), json.RawMessage(`[]`)} {
		if _, err := CanonicalContent(raw); !hasCode(err, ErrorCodeInvalid) {
			t.Fatalf("CanonicalContent(%s) error = %v", raw, err)
		}
	}
}

func testCandidate(t *testing.T, typ Type, task *foundation.ID, now time.Time) Memory {
	t.Helper()
	expires := now.Add(time.Hour)
	if typ != TypeEpisodic {
		expires = time.Time{}
	}
	input := CandidateInput{
		ID: testID(1), WorkspaceID: testID(2), Owner: testPrincipal(), Type: typ,
		Content: json.RawMessage(`{"note":"remember this"}`), Source: Source{Type: SourceAgent, Ref: "agent:turn-1"},
		TaskScopeID: task, CreatedAt: now,
	}
	if typ == TypeEpisodic {
		input.ExpiresAt = &expires
	}
	memory, err := NewCandidate(input)
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}
	return memory
}

func testPrincipal() Principal {
	return Principal{Kind: PrincipalUser, ID: testID(3)}
}

func testID(last byte) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-%012d", last))
}

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
