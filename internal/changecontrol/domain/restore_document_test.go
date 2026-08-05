package domain

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateRestoreDocumentAndRequestHashBindFrozenFacts(t *testing.T) {
	workspaceID := foundation.ID("50000000-0000-4000-8000-000000000001")
	documentID := foundation.ID("50000000-0000-4000-8000-000000000002")
	currentHash := ComputeContentHash([]byte("current\n"))
	targetHash := ComputeContentHash([]byte("target\n"))
	restore := RestoreDocument{
		WorkspaceID: workspaceID, DocumentID: documentID,
		TargetCommit:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedHead:            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpectedDocumentVersion: 7, PreviewHash: ComputeContentHash([]byte("preview")),
		CurrentContentHash: currentHash, TargetContentHash: targetHash,
		SchemaVersion: RestoreDocumentSchemaVersion,
	}
	canonical, err := ValidateRestoreDocument(restore)
	if err != nil || canonical != restore {
		t.Fatalf("ValidateRestoreDocument()=(%+v,%v)", canonical, err)
	}
	first, err := ComputeRestoreDocumentRequestHash(workspaceID, restore, "notes/java-ai.md", "target\n", "evidence", ProposalRiskLevelHigh, "risk", "rollback")
	if err != nil || !ValidHash(first) {
		t.Fatalf("ComputeRestoreDocumentRequestHash()=(%q,%v)", first, err)
	}
	changed := restore
	changed.ExpectedDocumentVersion++
	second, err := ComputeRestoreDocumentRequestHash(workspaceID, changed, "notes/java-ai.md", "target\n", "evidence", ProposalRiskLevelHigh, "risk", "rollback")
	if err != nil || second == first {
		t.Fatalf("changed request hash=(%q,%v), first=%q", second, err, first)
	}

	invalid := []RestoreDocument{restore, restore, restore, restore}
	invalid[0].ExpectedHead = invalid[0].TargetCommit
	invalid[1].TargetContentHash = invalid[1].CurrentContentHash
	invalid[2].DocumentID = invalid[2].WorkspaceID
	invalid[3].SchemaVersion = "document-restore/v2"
	for index, value := range invalid {
		if _, err := ValidateRestoreDocument(value); err == nil {
			t.Fatalf("invalid restore %d was accepted", index)
		}
	}
}

func TestValidateRestoreProposalRevisionBindsExactTargetBytes(t *testing.T) {
	workspaceID := foundation.ID("50000000-0000-4000-8000-000000000001")
	restore := RestoreDocument{
		WorkspaceID: workspaceID, DocumentID: "50000000-0000-4000-8000-000000000002",
		TargetCommit:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedHead:            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpectedDocumentVersion: 7, PreviewHash: ComputeContentHash([]byte("preview")),
		CurrentContentHash: ComputeContentHash([]byte("current\n")), TargetContentHash: ComputeContentHash([]byte("target\n")),
		SchemaVersion: RestoreDocumentSchemaVersion,
	}
	changeHash, err := ComputeChangeHashForTarget(workspaceID, "notes/java-ai.md", TargetModeReplace, restore.CurrentContentHash, "target\n")
	if err != nil {
		t.Fatal(err)
	}
	revision := Revision{
		ID: "50000000-0000-4000-8000-000000000003", ProposalID: "50000000-0000-4000-8000-000000000004",
		RevisionNo: 1, TargetPath: "notes/java-ai.md", TargetMode: TargetModeReplace,
		BaseHash: restore.CurrentContentHash, Content: "target\n", EvidenceSummary: "evidence",
		Risk: "risk", RollbackPlan: "rollback", ChangeHash: changeHash, RestoreDocument: &restore,
	}
	if err := ValidateProposalRevisionForType(ProposalTypeRestoreDocument, revision); err != nil {
		t.Fatal(err)
	}
	revision.Content = "drift\n"
	if err := ValidateProposalRevisionForType(ProposalTypeRestoreDocument, revision); err == nil {
		t.Fatal("restore revision accepted content outside frozen target hash")
	}
}
