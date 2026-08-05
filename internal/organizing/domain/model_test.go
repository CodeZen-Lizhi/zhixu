package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestMaterialRefDiscriminatedUnion(t *testing.T) {
	evidence := []EvidenceRef{{IndexVersionID: testID(91), ChunkID: testID(92), SourceVersionID: testID(1), SourceSpanID: testID(2), ContentHash: testHash("a"), ExcerptHash: testHash("b")}}
	cases := []MaterialRef{
		{Kind: MaterialSourceVersion, SourceVersionID: testID(1), ContentHash: testHash("c"), Evidence: evidence},
		{Kind: MaterialDocumentRevision, DocumentID: testID(3), ArticleRevisionID: testID(4), Version: 2, ContentHash: testHash("d")},
		{Kind: MaterialClaim, ClaimID: testID(5), Version: 3, ContentHash: testHash("e"), Evidence: evidence},
		{Kind: MaterialSmartCollection, CollectionID: testID(6), Version: 4, QueryHash: testHash("f"), ReadModelRevision: testHash("0")},
	}
	for _, current := range cases {
		if err := current.Validate(); err != nil {
			t.Fatalf("%s: %v", current.Kind, err)
		}
	}
	forged := cases[0]
	forged.ClaimID = testID(9)
	assertDomainCode(t, forged.Validate(), ErrorCodeMaterialInvalid)
	missingEvidence := cases[2]
	missingEvidence.Evidence = nil
	assertDomainCode(t, missingEvidence.Validate(), ErrorCodeMaterialInvalid)
}

func TestEvidenceRefAllowsTypedIdentitiesToShareUUIDValue(t *testing.T) {
	t.Parallel()
	identity := testID(90)
	evidence := EvidenceRef{
		IndexVersionID: identity, ChunkID: identity, SourceVersionID: identity, SourceSpanID: identity,
		ContentHash: testHash("a"), ExcerptHash: testHash("b"),
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("EvidenceRef.Validate() error = %v", err)
	}
}

func TestDraftCASAndSnapshotHash(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	draft, err := NewDraft(testID(10), testID(11), "  Java AI Agent  ", now)
	if err != nil {
		t.Fatal(err)
	}
	material := DraftMaterial{ID: testID(12), Ref: MaterialRef{Kind: MaterialDocumentRevision, DocumentID: testID(13), ArticleRevisionID: testID(14), Version: 1, ContentHash: testHash("1")}, Title: "Java AI", Reasons: []SuggestionReasonCode{ReasonUserAdded}, Origin: MaterialOriginUser, Availability: MaterialAvailable, Score: 1, Selected: true}
	next, err := ReplaceMaterials(draft, 1, []DraftMaterial{material}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.Version != 2 || next.Materials[0].DraftID != draft.ID {
		t.Fatalf("unexpected next draft: %#v", next)
	}
	if _, err := ReplaceMaterials(next, 1, []DraftMaterial{material}, now.Add(2*time.Second)); err == nil {
		t.Fatal("stale CAS succeeded")
	}
	next, err = UpdateDraft(next, next.Version, next.Intent, testID(17), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	templateHash := testHash("2")
	confirmed, snapshot, err := Confirm(next, 3, testID(15), testID(16), testID(17), templateHash, []MaterialRef{next.Materials[0].Ref}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != DraftConfirmed || snapshot.Hash == "" {
		t.Fatalf("unexpected confirmation: %#v %#v", confirmed, snapshot)
	}
	copy := snapshot
	copy.Hash = ""
	recomputed, err := ComputeSnapshotHash(copy)
	if err != nil || recomputed != snapshot.Hash {
		t.Fatalf("hash mismatch: %s %v", recomputed, err)
	}
	copy.Materials[0].Version++
	changed, err := ComputeSnapshotHash(copy)
	if err != nil || changed == snapshot.Hash {
		t.Fatal("version change did not change snapshot hash")
	}
}

func TestDraftTemplateSelectionCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 30, 0, 0, time.UTC)
	draft, err := NewDraft(testID(18), testID(19), "initial intent", now)
	if err != nil {
		t.Fatalf("NewDraft() error = %v", err)
	}
	if draft.TemplateRevisionID != "" {
		t.Fatalf("NewDraft() TemplateRevisionID = %q, want null identity", draft.TemplateRevisionID)
	}

	revisionID := testID(20)
	updated, err := UpdateDraft(draft, draft.Version, "  normalized intent  ", revisionID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	if updated.Intent != "normalized intent" || updated.TemplateRevisionID != revisionID || updated.Version != draft.Version+1 {
		t.Fatalf("UpdateDraft() = %+v", updated)
	}
	if draft.Intent != "initial intent" || draft.TemplateRevisionID != "" {
		t.Fatalf("UpdateDraft() mutated current draft: %+v", draft)
	}
	if _, err := UpdateDraft(updated, updated.Version, "next", "", now.Add(2*time.Second)); err == nil {
		t.Fatal("UpdateDraft() accepted an empty TemplateRevisionID")
	}
}

func TestConfirmRequiresSelectedTemplateRevision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 45, 0, 0, time.UTC)
	draft, err := NewDraft(testID(28), testID(29), "topic", now)
	if err != nil {
		t.Fatalf("NewDraft() error = %v", err)
	}
	material := DraftMaterial{
		ID: testID(30), Ref: MaterialRef{Kind: MaterialDocumentRevision, DocumentID: testID(31), ArticleRevisionID: testID(32), Version: 1, ContentHash: testHash("9")},
		Title: "Material", Reasons: []SuggestionReasonCode{ReasonUserAdded}, Origin: MaterialOriginUser, Availability: MaterialAvailable, Score: 1, Selected: true,
	}
	draft, err = ReplaceMaterials(draft, draft.Version, []DraftMaterial{material}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ReplaceMaterials() error = %v", err)
	}
	confirm := func(current Draft, revisionID foundation.ID) error {
		_, _, confirmErr := Confirm(current, current.Version, testID(33), testID(34), revisionID, testHash("8"), []MaterialRef{current.Materials[0].Ref}, now.Add(3*time.Second))
		return confirmErr
	}
	assertDomainCode(t, confirm(draft, testID(35)), ErrorCodeVersionConflict)

	draft, err = UpdateDraft(draft, draft.Version, draft.Intent, testID(36), now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	assertDomainCode(t, confirm(draft, testID(35)), ErrorCodeVersionConflict)
}

func TestSnapshotHashCanonicalizesEvidenceOrder(t *testing.T) {
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	evidenceA := EvidenceRef{IndexVersionID: testID(90), ChunkID: testID(91), SourceVersionID: testID(21), SourceSpanID: testID(22), ContentHash: testHash("a"), ExcerptHash: testHash("b")}
	evidenceB := EvidenceRef{IndexVersionID: testID(90), ChunkID: testID(92), SourceVersionID: testID(21), SourceSpanID: testID(23), ContentHash: testHash("c"), ExcerptHash: testHash("d")}
	base := Snapshot{WorkspaceID: testID(24), DraftID: testID(25), DraftVersion: 1, TemplateID: testID(26), TemplateRevisionID: testID(27), TemplateHash: testHash("e"), Intent: "topic", CreatedAt: now,
		Materials: []MaterialRef{{Kind: MaterialSourceVersion, SourceVersionID: testID(21), ContentHash: testHash("f"), Evidence: []EvidenceRef{evidenceA, evidenceB}}}}
	left, err := ComputeSnapshotHash(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Materials[0].Evidence = []EvidenceRef{evidenceB, evidenceA}
	right, err := ComputeSnapshotHash(base)
	if err != nil || left != right {
		t.Fatalf("evidence order changed hash: %s %s %v", left, right, err)
	}
}

func TestConfirmRejectsUnselectedStaleAndUnavailableMaterials(t *testing.T) {
	now := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	draft, err := NewDraft(testID(40), testID(41), "Java AI", now)
	if err != nil {
		t.Fatal(err)
	}
	selected := DraftMaterial{ID: testID(42), Ref: MaterialRef{Kind: MaterialSourceVersion, SourceVersionID: testID(43), ContentHash: testHash("1")}, Title: "Selected", Reasons: []SuggestionReasonCode{ReasonHybridMatch}, Origin: MaterialOriginSuggested, Availability: MaterialAvailable, Score: 0.9, Selected: true}
	unselected := DraftMaterial{ID: testID(44), Ref: MaterialRef{Kind: MaterialDocumentRevision, DocumentID: testID(45), ArticleRevisionID: testID(46), Version: 1, ContentHash: testHash("2")}, Title: "Unselected", Reasons: []SuggestionReasonCode{ReasonUserAdded}, Origin: MaterialOriginUser, Availability: MaterialAvailable, Score: 1}
	draft, err = ReplaceMaterials(draft, 1, []DraftMaterial{selected, unselected}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	draft, err = UpdateDraft(draft, draft.Version, draft.Intent, testID(49), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	confirm := func(materials []MaterialRef) error {
		_, _, confirmErr := Confirm(draft, 3, testID(47), testID(48), testID(49), testHash("3"), materials, now.Add(3*time.Second))
		return confirmErr
	}
	assertDomainCode(t, confirm([]MaterialRef{draft.Materials[0].Ref, draft.Materials[1].Ref}), ErrorCodeSnapshotInvalid)
	stale := draft.Materials[0].Ref
	stale.ContentHash = testHash("4")
	assertDomainCode(t, confirm([]MaterialRef{stale}), ErrorCodeSnapshotInvalid)
	unavailable := draft
	unavailable.Materials = append([]DraftMaterial(nil), draft.Materials...)
	unavailable.Materials[0].Availability = MaterialStale
	if _, _, confirmErr := Confirm(unavailable, 3, testID(50), testID(48), testID(49), testHash("3"), []MaterialRef{unavailable.Materials[0].Ref}, now.Add(3*time.Second)); confirmErr == nil {
		t.Fatal("stale selected material was confirmed")
	}
	if err := confirm([]MaterialRef{draft.Materials[0].Ref}); err != nil {
		t.Fatalf("selected exact material was rejected: %v", err)
	}
}

func TestConfirmAcceptsOnlyMembersOfSelectedCollection(t *testing.T) {
	now := time.Date(2026, 8, 3, 9, 45, 0, 0, time.UTC)
	draft, err := NewDraft(testID(60), testID(61), "Java AI", now)
	if err != nil {
		t.Fatal(err)
	}
	collection := MaterialRef{Kind: MaterialSmartCollection, CollectionID: testID(62), Version: 3, QueryHash: testHash("5"), ReadModelRevision: testHash("6")}
	draft, err = ReplaceMaterials(draft, 1, []DraftMaterial{{ID: testID(63), Ref: collection, Title: "Java AI Collection", Reasons: []SuggestionReasonCode{ReasonUserAdded}, Origin: MaterialOriginUser, Availability: MaterialAvailable, Score: 1, Selected: true}}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	draft, err = UpdateDraft(draft, draft.Version, draft.Intent, testID(67), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	member := MaterialRef{Kind: MaterialSourceVersion, SourceVersionID: testID(64), OriginCollectionID: collection.CollectionID, ContentHash: testHash("7")}
	if _, _, err := Confirm(draft, 3, testID(65), testID(66), testID(67), testHash("8"), []MaterialRef{collection, member}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("selected collection expansion was rejected: %v", err)
	}
	foreign := member
	foreign.OriginCollectionID = testID(68)
	if _, _, err := Confirm(draft, 3, testID(69), testID(66), testID(67), testHash("8"), []MaterialRef{collection, foreign}, now.Add(3*time.Second)); err == nil {
		t.Fatal("foreign collection expansion was accepted")
	}
	if _, _, err := Confirm(draft, 3, testID(70), testID(66), testID(67), testHash("8"), []MaterialRef{member}, now.Add(3*time.Second)); err == nil {
		t.Fatal("snapshot omitted its selected collection marker")
	}
}

func testID(value int) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-" + leftPad(value))
}
func leftPad(value int) string {
	const digits = "000000000000"
	text := fmtInt(value)
	return digits[:len(digits)-len(text)] + text
}
func fmtInt(value int) string {
	if value == 0 {
		return "0"
	}
	var data [20]byte
	i := len(data)
	for value > 0 {
		i--
		data[i] = byte('0' + value%10)
		value /= 10
	}
	return string(data[i:])
}
func testHash(char string) string {
	result := ""
	for len(result) < 64 {
		result += char
	}
	return result[:64]
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}
