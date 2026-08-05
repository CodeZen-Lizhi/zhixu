package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	testWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	testArtifactID  foundation.ID = "20000000-0000-4000-8000-000000000002"
	testRevisionID  foundation.ID = "30000000-0000-4000-8000-000000000003"
	testExportID    foundation.ID = "40000000-0000-4000-8000-000000000004"
)

type workspaceReaderFake struct {
	workspaces map[foundation.ID]workspacedomain.Workspace
}

func (reader workspaceReaderFake) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	return reader.workspaces[id], nil
}

func TestExporterWritesCanonicalMarkdownWithVerifiedBinding(t *testing.T) {
	root := canonicalTempDir(t)
	exporter := newTestExporter(t, map[foundation.ID]string{testWorkspaceID: root})
	snapshot := testSnapshot(t, testWorkspaceID, testArtifactID, testRevisionID, testExportID)

	managed, err := exporter.Export(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	wantPath := ".knowledge/exports/artifacts/" + string(testArtifactID) + "/" + string(testExportID) + ".md"
	if managed.OutputPath != wantPath {
		t.Fatalf("OutputPath = %q, want %q", managed.OutputPath, wantPath)
	}
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(wantPath)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if managed.OutputHash != hex.EncodeToString(digest[:]) || managed.OutputSize != int64(len(payload)) {
		t.Fatalf("managed binding = %+v, payload bytes = %d", managed, len(payload))
	}
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(wantPath)))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != privateExportFileMode {
		t.Fatalf("export file mode = %v, err = %v", info.Mode(), err)
	}
	content := string(payload)
	for _, want := range []string{
		"# Go Concurrency",
		"- Artifact ID: `" + string(testArtifactID) + "`",
		"- Revision SHA-256: `" + snapshot.State.Revision.ContentHash + "`",
		"## Channels",
		"Channels synchronize work.",
		"Source Version: `50000000-0000-4000-8000-000000000005`",
		"### Source Documents",
		"Document: `b0000000-0000-4000-8000-000000000011`",
		"Article Revision: `c0000000-0000-4000-8000-000000000012`",
		"## Limits",
		"- `NO_SOURCE`: No approved source covers limits",
		"- Formal Knowledge: `false`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("export content missing %q:\n%s", want, content)
		}
	}
}

func TestExporterAtomicallyReplacesExistingManagedFile(t *testing.T) {
	root := canonicalTempDir(t)
	exporter := newTestExporter(t, map[foundation.ID]string{testWorkspaceID: root})
	snapshot := testSnapshot(t, testWorkspaceID, testArtifactID, testRevisionID, testExportID)
	first, err := exporter.Export(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("initial Export() error = %v", err)
	}
	filePath := filepath.Join(root, filepath.FromSlash(first.OutputPath))
	if err := os.WriteFile(filePath, []byte("stale"), privateExportFileMode); err != nil {
		t.Fatal(err)
	}
	second, err := exporter.Export(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("replacement Export() error = %v", err)
	}
	payload, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "stale" || second.OutputHash != first.OutputHash || second.OutputSize != first.OutputSize {
		t.Fatalf("replacement was not bound to the snapshot: first=%+v second=%+v payload=%q", first, second, payload)
	}
}

func TestExporterRendersStableBytesForOneFrozenRevision(t *testing.T) {
	first := testSnapshot(t, testWorkspaceID, testArtifactID, testRevisionID, testExportID)
	second := first
	second.State.Artifact.UpdatedAt = first.State.Artifact.UpdatedAt.Add(time.Hour)
	if string(renderMarkdown(first)) != string(renderMarkdown(second)) {
		t.Fatal("frozen revision rendered different bytes after only artifact updated_at changed")
	}
}

func TestExporterRejectsInvalidSnapshotAndSymlinkPaths(t *testing.T) {
	root := canonicalTempDir(t)
	exporter := newTestExporter(t, map[foundation.ID]string{testWorkspaceID: root})
	snapshot := testSnapshot(t, testWorkspaceID, testArtifactID, testRevisionID, testExportID)

	invalidID := snapshot
	invalidID.ExportID = "../escape"
	if _, err := exporter.Export(context.Background(), invalidID); err == nil {
		t.Fatal("Export() accepted malformed export ID")
	}
	incomplete := snapshot
	incomplete.State.Artifact.SourceCoverage = []domain.Coverage{}
	if _, err := exporter.Export(context.Background(), incomplete); err == nil {
		t.Fatal("Export() accepted an incomplete revision snapshot")
	}

	if err := os.MkdirAll(filepath.Join(root, ".knowledge", "exports"), privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, ".knowledge", "exports"), privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	outside := canonicalTempDir(t)
	if err := os.Symlink(outside, filepath.Join(root, ".knowledge", "exports", "artifacts")); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), snapshot); err == nil {
		t.Fatal("Export() accepted an artifact export symlink directory")
	}
}

func TestExporterRejectsSymlinkTargetAndKeepsOutsideUntouched(t *testing.T) {
	root := canonicalTempDir(t)
	exporter := newTestExporter(t, map[foundation.ID]string{testWorkspaceID: root})
	snapshot := testSnapshot(t, testWorkspaceID, testArtifactID, testRevisionID, testExportID)
	managed, err := exporter.Export(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(managed.OutputPath))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(canonicalTempDir(t), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), privateExportFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), snapshot); err == nil {
		t.Fatal("Export() accepted a symlink target")
	}
	payload, err := os.ReadFile(outside)
	if err != nil || string(payload) != "outside" {
		t.Fatalf("outside target changed: payload=%q err=%v", payload, err)
	}
}

func TestExporterBindsOutputToRequestedWorkspace(t *testing.T) {
	root := canonicalTempDir(t)
	otherRoot := canonicalTempDir(t)
	exporter := newTestExporter(t, map[foundation.ID]string{testWorkspaceID: root, "60000000-0000-4000-8000-000000000006": otherRoot})
	snapshot := testSnapshot(t, testWorkspaceID, testArtifactID, testRevisionID, testExportID)
	managed, err := exporter.Export(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(managed.OutputPath))); err != nil {
		t.Fatalf("bound workspace is missing its export: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(otherRoot, filepath.FromSlash(managed.OutputPath))); !os.IsNotExist(err) {
		t.Fatalf("other workspace received an export: %v", err)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestExporter(t *testing.T, roots map[foundation.ID]string) *Exporter {
	t.Helper()
	workspaces := make(map[foundation.ID]workspacedomain.Workspace, len(roots))
	for id, root := range roots {
		workspaces[id] = workspacedomain.Workspace{ID: id, RootPath: root}
	}
	exporter, err := NewExporter(workspaceReaderFake{workspaces: workspaces})
	if err != nil {
		t.Fatal(err)
	}
	return exporter
}

func testSnapshot(t *testing.T, workspaceID, artifactID, revisionID, exportID foundation.ID) artifactapp.ExportSnapshot {
	t.Helper()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID: artifactID, InitialRevisionID: revisionID, WorkspaceID: workspaceID,
		Type: "study-guide", Title: "Go Concurrency", ScopeDefinition: "Approved Go concurrency material.", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = domain.SubmitOutline(artifact, revision, "70000000-0000-4000-8000-000000000007", []domain.OutlineSection{
		{Key: "channels", Title: "Channels"}, {Key: "limits", Title: "Limits"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = domain.ApproveOutline(artifact, revision, "80000000-0000-4000-8000-000000000008", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	covered := domain.Section{Key: "channels", Title: "Channels", Content: "Channels synchronize work.", Citations: []domain.Citation{{
		SourceVersionID: "50000000-0000-4000-8000-000000000005", SourceSpanID: "60000000-0000-4000-8000-000000000006",
		VerifiedContentHash: strings.Repeat("a", 64), Excerpt: "Verified source span.", Verified: true,
	}}, DocumentSources: []domain.DocumentSource{{
		DocumentID: "b0000000-0000-4000-8000-000000000011", ArticleRevisionID: "c0000000-0000-4000-8000-000000000012",
		RevisionNo: 3, VerifiedContentHash: strings.Repeat("b", 64), Verified: true,
	}}, Coverage: domain.Coverage{SectionKey: "channels", Status: domain.CoverageCovered, Gaps: []domain.Gap{}}}
	artifact, revision, err = domain.RecordSection(artifact, revision, "90000000-0000-4000-8000-000000000009", covered, domain.CreatorHuman, nil, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	gap := domain.Section{Key: "limits", Title: "Limits", Content: "", Citations: []domain.Citation{}, Coverage: domain.Coverage{
		SectionKey: "limits", Status: domain.CoverageGap, Gaps: []domain.Gap{{Code: "NO_SOURCE", Description: "No approved source covers limits"}},
	}}
	artifact, revision, err = domain.RecordSection(artifact, revision, "a0000000-0000-4000-8000-000000000010", gap, domain.CreatorHuman, nil, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = domain.ApproveDraft(artifact, revision, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = domain.MarkExported(artifact, revision, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return artifactapp.ExportSnapshot{ExportID: exportID, State: artifactapp.State{Artifact: artifact, Revision: revision}}
}
