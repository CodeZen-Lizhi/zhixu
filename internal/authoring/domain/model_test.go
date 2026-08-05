package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	testWorkspaceID foundation.ID = "69000000-0000-4000-8000-000000000001"
	testDraftID     foundation.ID = "69000000-0000-4000-8000-000000000002"
	testDocumentID  foundation.ID = "69000000-0000-4000-8000-000000000003"
	testRevisionID  foundation.ID = "69000000-0000-4000-8000-000000000004"
)

func TestBlankWorkingDraftAndAutosaveDoNotCreateRevision(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC)
	draft, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != "" || draft.TargetPath != "" || draft.Body != "" || draft.DocumentID != "" || draft.Version != 1 {
		t.Fatalf("blank draft = %#v", draft)
	}
	updated, err := ApplyUpdate(draft, 1, " Java AI ", "notes/../draft", "# Java AI\n", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.DocumentID != "" || updated.Title != " Java AI " || updated.TargetPath != "notes/../draft" {
		t.Fatalf("updated draft = %#v", updated)
	}
	if _, err := ApplyUpdate(updated, 1, "stale", "a.md", "body", now.Add(2*time.Second)); !isKind(err, foundation.ErrorVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
}

func TestCanonicalizeTargetPath(t *testing.T) {
	canonical, err := CanonicalizeTargetPath(" notes//java/./rag.md ")
	if err != nil || canonical != "notes/java/rag.md" {
		t.Fatalf("canonical = %q, %v", canonical, err)
	}
	for _, value := range []string{
		"", "/tmp/a.md", "../a.md", "notes/../a.md", "notes\\a.md", ".git/a.md",
		".KNOWLEDGE/a.md", "notes/a.markdown", "notes/a.txt", "notes/line\nbreak.md",
	} {
		if _, err := CanonicalizeTargetPath(value); !isCode(err, ErrorCodeTargetPathInvalid) {
			t.Fatalf("unsafe target %q error = %v", value, err)
		}
	}
}

func TestPrepareFreezeCreatesDocumentThenAppendsParentedRevision(t *testing.T) {
	now := time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC)
	blank, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := ApplyUpdate(blank, 1, " Java AI ", "notes//java-ai.md", "# Java AI\n\nRAG", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	frozenDraft, document, first, err := PrepareFreeze(edited, 2, testDocumentID, testRevisionID, nil, nil, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if frozenDraft.DocumentID != testDocumentID || frozenDraft.Version != 3 || frozenDraft.Title != "Java AI" || frozenDraft.TargetPath != "notes/java-ai.md" {
		t.Fatalf("frozen draft = %#v", frozenDraft)
	}
	if document.Version != 1 || document.Lifecycle != DocumentDraft || document.CanonicalPath != frozenDraft.TargetPath {
		t.Fatalf("document = %#v", document)
	}
	if first.RevisionNo != 1 || first.ParentRevisionID != "" || first.ContentHash != ComputeContentHash(first.Content) {
		t.Fatalf("first revision = %#v", first)
	}

	secondDraft, err := ApplyUpdate(frozenDraft, 3, "Java AI v2", "notes/java-ai-v2.md", first.Content+"\n\nAgent", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	secondID := foundation.ID("69000000-0000-4000-8000-000000000005")
	nextDraft, nextDocument, second, err := PrepareFreeze(secondDraft, 4, testDocumentID, secondID, &document, &first, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if nextDraft.Version != 5 || nextDocument.Version != 2 || second.RevisionNo != 2 || second.ParentRevisionID != first.ID {
		t.Fatalf("next freeze = draft %#v document %#v revision %#v", nextDraft, nextDocument, second)
	}
	if first.Content != "# Java AI\n\nRAG" || first.RevisionNo != 1 {
		t.Fatalf("first revision was mutated: %#v", first)
	}
}

func TestFreezeRejectsIncompleteFieldsAndBounds(t *testing.T) {
	now := time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC)
	draft, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := PrepareFreeze(draft, 1, testDocumentID, testRevisionID, nil, nil, now.Add(time.Second)); !isKind(err, foundation.ErrorInvalidInput) {
		t.Fatalf("empty freeze error = %v", err)
	}
	if _, err := ApplyUpdate(draft, 1, strings.Repeat("a", MaxTitleBytes+1), "a.md", "body", now.Add(time.Second)); !isCode(err, ErrorCodeDraftInvalid) {
		t.Fatalf("oversized title error = %v", err)
	}
}

func TestAuthoringTimesUsePostgreSQLPrecision(t *testing.T) {
	now := time.Date(2026, 8, 3, 3, 30, 0, 123456789, time.UTC)
	draft, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Truncate(time.Microsecond)
	if draft.CreatedAt != want || draft.UpdatedAt != want {
		t.Fatalf("blank draft time = %s/%s, want %s", draft.CreatedAt, draft.UpdatedAt, want)
	}
	updated, err := ApplyUpdate(draft, 1, "Java AI", "java-ai.md", "body", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpdatedAt != now.Add(time.Second).Truncate(time.Microsecond) {
		t.Fatalf("updated time = %s", updated.UpdatedAt)
	}
}

func TestFreezeRejectsInactiveDocumentAndInvalidPersistedEnums(t *testing.T) {
	now := time.Date(2026, 8, 3, 3, 45, 0, 0, time.UTC)
	draft, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	draft, err = ApplyUpdate(draft, 1, "Java AI", "java-ai.md", "body", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	draft, document, revision, err := PrepareFreeze(
		draft, 2, testDocumentID, testRevisionID, nil, nil, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, err = ApplyUpdate(draft, 3, "Java AI", "java-ai.md", "body v2", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, lifecycle := range []DocumentLifecycle{DocumentArchived, DocumentDeleted} {
		inactive := document
		inactive.Lifecycle = lifecycle
		_, _, _, err := PrepareFreeze(
			draft, 4, testDocumentID, foundation.ID("69000000-0000-4000-8000-000000000006"),
			&inactive, &revision, now.Add(4*time.Second),
		)
		if !isKind(err, foundation.ErrorVersionConflict) {
			t.Fatalf("%s freeze error = %v", lifecycle, err)
		}
	}

	invalidPublished := document
	invalidPublished.Lifecycle = DocumentPublished
	if err := invalidPublished.Validate(); !isCode(err, ErrorCodeFreezeInvalid) {
		t.Fatalf("published document without current revision error = %v", err)
	}
	invalidDraft := document
	invalidDraft.CurrentPublishedRevisionID = revision.ID
	if err := invalidDraft.Validate(); !isCode(err, ErrorCodeFreezeInvalid) {
		t.Fatalf("draft document with current revision error = %v", err)
	}
	invalidRevision := revision
	invalidRevision.OptimizationMode = "UNKNOWN"
	if err := invalidRevision.Validate(); !isCode(err, ErrorCodeFreezeInvalid) {
		t.Fatalf("unknown optimization mode error = %v", err)
	}
	invalidRevision = revision
	invalidRevision.CreatedByType = "HUMAN"
	if err := invalidRevision.Validate(); !isCode(err, ErrorCodeFreezeInvalid) {
		t.Fatalf("unknown creator type error = %v", err)
	}
	invalidRevision = revision
	invalidRevision.RevisionNo = MaxRevisionNo + 1
	invalidRevision.ParentRevisionID = foundation.ID("69000000-0000-4000-8000-000000000007")
	if err := invalidRevision.Validate(); !isCode(err, ErrorCodeFreezeInvalid) {
		t.Fatalf("overflowing revision number error = %v", err)
	}
}

func TestCommandHashesAreCanonicalAndBindRequest(t *testing.T) {
	draft, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	createA, err := ComputeCreateRequestHash(testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	createB, _ := ComputeCreateRequestHash(testWorkspaceID)
	if createA != createB || len(createA) != 64 {
		t.Fatalf("create hashes = %q %q", createA, createB)
	}
	updateA, err := ComputeUpdateRequestHash(draft, 1, "title", "a.md", "body")
	if err != nil {
		t.Fatal(err)
	}
	updateB, _ := ComputeUpdateRequestHash(draft, 1, "title", "a.md", "body changed")
	if updateA == updateB {
		t.Fatal("different update bodies produced the same request hash")
	}
	freezeA, _ := ComputeFreezeRequestHash(testWorkspaceID, testDraftID, 1)
	freezeB, _ := ComputeFreezeRequestHash(testWorkspaceID, testDraftID, 2)
	if freezeA == freezeB {
		t.Fatal("different expected versions produced the same freeze hash")
	}
}

func TestFreezeCanonicalizesMarkdownAndUsesChangeControlAbsenceToken(t *testing.T) {
	now := time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC)
	draft, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	draft, err = ApplyUpdate(draft, 1, "Java AI", "notes/java-ai.md", "# Java AI\r\n\rBody\r", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	frozen, _, revision, err := PrepareFreeze(draft, 2, testDocumentID, testRevisionID, nil, nil, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Body != "# Java AI\n\nBody\n" || revision.Content != frozen.Body ||
		revision.ContentHash != ComputeContentHash(frozen.Body) {
		t.Fatalf("frozen=%q revision=%#v", frozen.Body, revision)
	}
	authoringToken, err := ComputeAbsenceToken(testWorkspaceID, "notes/java-ai.md")
	if err != nil {
		t.Fatal(err)
	}
	changeControlToken, err := changecontroldomain.ComputeAbsenceToken(testWorkspaceID, "notes/java-ai.md")
	if err != nil || authoringToken != changeControlToken {
		t.Fatalf("tokens authoring=%q change-control=%q err=%v", authoringToken, changeControlToken, err)
	}
}

func TestPublicationBindingClosedIsTerminalAndWellShaped(t *testing.T) {
	now := time.Date(2026, 8, 3, 5, 30, 0, 0, time.UTC)
	token, err := ComputeAbsenceToken(testWorkspaceID, "notes/java-ai.md")
	if err != nil {
		t.Fatal(err)
	}
	binding := PublicationBinding{
		ID:            foundation.ID("69000000-0000-4000-8000-000000000010"),
		ReservationID: foundation.ID("69000000-0000-4000-8000-000000000011"),
		WorkspaceID:   testWorkspaceID, DocumentID: testDocumentID, ArticleRevisionID: testRevisionID,
		ProposalID:         foundation.ID("69000000-0000-4000-8000-000000000012"),
		ProposalRevisionID: foundation.ID("69000000-0000-4000-8000-000000000013"),
		TargetPath:         "notes/java-ai.md", ContentHash: ComputeContentHash("# Java AI\n"),
		TargetMode: ProposalTargetCreateOnly, AbsenceToken: token, Status: PublicationClosed,
		ErrorCode: "AUTHORING_PUBLICATION_PROPOSAL_REJECTED", Version: 2, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	binding.ErrorCode = ""
	if err := binding.Validate(); !isCode(err, ErrorCodePublicationInvalid) {
		t.Fatalf("closed publication without reason error=%v", err)
	}
}

func isKind(err error, kind foundation.ErrorKind) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind
}

func isCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
