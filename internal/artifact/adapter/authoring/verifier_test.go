package authoring

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestVerifierRebuildsExactHistoricalDocumentSource(t *testing.T) {
	workspaceID := verifierID(1)
	documentID := verifierID(2)
	revisionID := verifierID(3)
	snapshot := verifierSnapshot(workspaceID, documentID, revisionID)
	reader := &revisionReaderFake{snapshots: []authoringapp.ArticleRevisionSnapshot{snapshot}}
	verifier, err := NewVerifier(reader)
	if err != nil {
		t.Fatal(err)
	}

	verified, err := verifier.VerifyDocumentSources(context.Background(), workspaceID, []artifactapp.DocumentSourceInput{{
		DocumentID: documentID, ArticleRevisionID: revisionID, RevisionNo: int64(snapshot.Revision.RevisionNo), ContentHash: snapshot.Revision.ContentHash,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || !verified[0].Verified || verified[0].DocumentID != documentID ||
		verified[0].ArticleRevisionID != revisionID || verified[0].VerifiedContentHash != snapshot.Revision.ContentHash {
		t.Fatalf("verified=%+v", verified)
	}
	if reader.query.WorkspaceID != workspaceID || len(reader.query.Items) != 1 || reader.query.Items[0].RevisionID != revisionID {
		t.Fatalf("query=%+v", reader.query)
	}
}

func TestVerifierRejectsNonCanonicalAndDriftingDocumentSources(t *testing.T) {
	workspaceID := verifierID(10)
	documentID := verifierID(11)
	revisionID := verifierID(12)
	snapshot := verifierSnapshot(workspaceID, documentID, revisionID)
	reader := &revisionReaderFake{snapshots: []authoringapp.ArticleRevisionSnapshot{snapshot}}
	verifier, err := NewVerifier(reader)
	if err != nil {
		t.Fatal(err)
	}
	input := artifactapp.DocumentSourceInput{DocumentID: documentID, ArticleRevisionID: revisionID, RevisionNo: 2, ContentHash: snapshot.Revision.ContentHash}

	uppercase := input
	uppercase.ContentHash = strings.ToUpper(uppercase.ContentHash)
	if _, err := verifier.VerifyDocumentSources(context.Background(), workspaceID, []artifactapp.DocumentSourceInput{uppercase}); err == nil || reader.calls != 0 {
		t.Fatalf("uppercase hash was accepted: err=%v calls=%d", err, reader.calls)
	}

	drifted := snapshot
	drifted.Revision.Content = "different immutable content"
	drifted.Revision.ContentHash = authoringdomain.ComputeContentHash(drifted.Revision.Content)
	reader.snapshots = []authoringapp.ArticleRevisionSnapshot{drifted}
	if _, err := verifier.VerifyDocumentSources(context.Background(), workspaceID, []artifactapp.DocumentSourceInput{input}); err == nil {
		t.Fatal("drifting document source was accepted")
	}
}

type revisionReaderFake struct {
	query     authoringapp.ArticleRevisionBatchQuery
	snapshots []authoringapp.ArticleRevisionSnapshot
	calls     int
}

func (reader *revisionReaderFake) GetArticleRevisions(_ context.Context, query authoringapp.ArticleRevisionBatchQuery) ([]authoringapp.ArticleRevisionSnapshot, error) {
	reader.calls++
	reader.query = query
	return append([]authoringapp.ArticleRevisionSnapshot(nil), reader.snapshots...), nil
}

func verifierSnapshot(workspaceID, documentID, revisionID foundation.ID) authoringapp.ArticleRevisionSnapshot {
	now := time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)
	content := "# Historical Java AI\n\nVerified document body."
	return authoringapp.ArticleRevisionSnapshot{
		Document: authoringdomain.Document{
			ID: documentID, WorkspaceID: workspaceID, CanonicalPath: "notes/java-ai.md", Title: "Historical Java AI",
			Lifecycle: authoringdomain.DocumentDraft, Version: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		},
		Revision: authoringdomain.ArticleRevision{
			ID: revisionID, WorkspaceID: workspaceID, DocumentID: documentID, ParentRevisionID: verifierID(99),
			RevisionNo: 2, Content: content, ContentHash: authoringdomain.ComputeContentHash(content), Status: authoringdomain.RevisionDraft,
			OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: now,
		},
	}
}

func verifierID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-%012d", value))
}
