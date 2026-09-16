//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	owner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestHistoricalSourceReadsManagedBytesAfterOriginalRemoval(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 108)
	ctx := t.Context()
	record := f.generation(t, 181000, nil, "Redis expiration controls cache lifetime.")
	record.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(record, "historical source")}
	result, err := f.service.ApplyGeneration(ctx, record.Input, record.Generation)
	if err != nil {
		t.Fatal(err)
	}
	note, err := f.service.GetNote(ctx, f.workspace, result.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	ref := record.Input.Sources[0].Reference
	original := record.Input.Sources[0].Text
	var path string
	if err := f.db.Raw(`SELECT original_location FROM core.source WHERE id=?`, string(ref.Source.SourceID)).Scan(&path).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	scanner := filesystem.Scanner{}
	captured, err := scanner.Capture(ctx, f.root, workspacedomain.ScannedFile{RelativePath: path, ContentHash: ref.Source.ContentHash, ByteSize: int64(len(original))})
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := retrievalworkspace.NewReader(workspaces, scanner)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := owner.NewGORMSynthesisSourceReader(f.platform, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := changecontrollocalfs.NewReader(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := authoringchange.NewProposalCreator(f.changes, targets)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := authoringapp.NewService(authoringapp.Dependencies{Repository: f.authoring, Proposals: creator, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewSynthesisService(app.SynthesisDependencies{Store: f.store, Sources: sources, Publications: publisher, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	read := func() app.SynthesisSourceView {
		t.Helper()
		view, err := service.OpenSource(ctx, f.workspace, note.Note.ID, note.CurrentRevision.ID, ref)
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	current := read()
	if current.Text != original || current.SnapshotText != "" || current.Availability != domain.MaterialAvailable {
		t.Fatalf("current %+v", current)
	}
	// 存在的来源排在已删除来源之前；推进游标必须到达后续来源，不能始终重复检查第一页。
	const presentPath = "still-present.md"
	if err := os.WriteFile(filepath.Join(f.root, presentPath), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = workspaces.RegisterSourceVersion(ctx, workspacedomain.SourceRegistration{
		Source:   workspacedomain.Source{ID: organizingIntegrationID(100), WorkspaceID: f.workspace, Type: "markdown", LogicalName: presentPath, OriginalLocation: presentPath, CreatedAt: f.now},
		Artifact: workspacedomain.ContentArtifact{ID: organizingIntegrationID(181090), WorkspaceID: f.workspace, ContentHash: ref.Source.ContentHash, ByteSize: int64(len(original)), ManagedLocation: captured.ManagedLocation, CreatedAt: f.now},
		Version:  workspacedomain.SourceVersion{ID: organizingIntegrationID(181091), ContentHash: ref.Source.ContentHash, ByteSize: int64(len(original)), MediaType: "text/markdown", OriginalContentLocation: presentPath, SecurityStatus: "pending", CapturedAt: f.now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, path)); err != nil {
		t.Fatal(err)
	}
	firstPage, err := workspaces.ReconcileLocalSourcePresence(ctx, f.workspace, "", 1, scanner)
	if err != nil || firstPage.Checked != 1 || firstPage.Removed != 0 {
		t.Fatalf("present first page: %+v %v", firstPage, err)
	}
	presence, err := workspaces.ReconcileLocalSourcePresence(ctx, f.workspace, firstPage.After, 1, scanner)
	if err != nil || presence.Removed != 1 {
		t.Fatalf("detect deleted local source: %+v %v", presence, err)
	}
	replay, err := workspaces.ReconcileLocalSourcePresence(ctx, f.workspace, "", 100, scanner)
	if err != nil || replay.Removed != 0 {
		t.Fatalf("repeated absence check: %+v %v", replay, err)
	}
	if _, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 100); err != nil {
		t.Fatal(err)
	}
	historical := read()
	if historical.Text != "" || historical.SnapshotText != original || historical.Availability != domain.MaterialUnavailable || historical.Reference != ref {
		t.Fatalf("historical %+v", historical)
	}
	if _, err := sources.ReadSynthesisSource(ctx, ref.Source); err == nil {
		t.Fatal("historical evidence admitted as current generation input")
	}
	if again := read(); !reflect.DeepEqual(historical, again) {
		t.Fatal("historical reread changed")
	}
	saved, err := service.GetSynthesisRevision(ctx, f.workspace, note.Note.ID, note.CurrentRevision.ID)
	if err != nil || !reflect.DeepEqual(saved, *note.CurrentRevision) {
		t.Fatal("historical note changed", err)
	}

	newContent := original + "\nNew knowledge belongs to a later version."
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(newContent), 0600); err != nil {
		t.Fatal(err)
	}
	newHash := organizingIntegrationHash(newContent)
	newCapture, err := scanner.Capture(ctx, f.root, workspacedomain.ScannedFile{RelativePath: path, ContentHash: newHash, ByteSize: int64(len(newContent))})
	if err != nil {
		t.Fatal(err)
	}
	newRegistration := workspacedomain.SourceRegistration{
		Source:   workspacedomain.Source{ID: ref.Source.SourceID, WorkspaceID: f.workspace, Type: "markdown", LogicalName: path, OriginalLocation: path, CreatedAt: f.now},
		Artifact: workspacedomain.ContentArtifact{ID: organizingIntegrationID(181060), WorkspaceID: f.workspace, ContentHash: newHash, ByteSize: int64(len(newContent)), ManagedLocation: newCapture.ManagedLocation, CreatedAt: time.Now().UTC()},
		Version:  workspacedomain.SourceVersion{ID: organizingIntegrationID(181061), ContentHash: newHash, ByteSize: int64(len(newContent)), MediaType: "text/markdown", OriginalContentLocation: path, SecurityStatus: "pending", CapturedAt: time.Now().UTC()},
	}
	_, err = workspaces.RegisterSourceVersion(ctx, newRegistration)
	if err != nil {
		t.Fatal(err)
	}
	stale := read()
	if stale.Availability != domain.MaterialStale || stale.Text != "" || stale.SnapshotText != original {
		t.Fatalf("new version substituted historical bytes %+v", stale)
	}
	// 相同内容登记须等待缺失检查持有的来源锁；检查提交后，登记操作清除墓碑。
	if err := os.Remove(filepath.Join(f.root, path)); err != nil {
		t.Fatal(err)
	}
	probeEntered, releaseProbe := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseProbe:
		default:
			close(releaseProbe)
		}
	}()
	presenceDone := make(chan error, 1)
	go func() {
		_, checkErr := workspaces.ReconcileLocalSourcePresence(ctx, f.workspace, "", 100, historicalPresenceFunc(func(probeCtx context.Context, rootPath, relative string) (bool, error) {
			missing, err := scanner.MissingLocalSource(probeCtx, rootPath, relative)
			if err != nil || !missing {
				return missing, err
			}
			close(probeEntered)
			select {
			case <-releaseProbe:
				return true, nil
			case <-probeCtx.Done():
				return false, probeCtx.Err()
			}
		}))
		presenceDone <- checkErr
	}()
	select {
	case <-probeEntered:
	case err := <-presenceDone:
		t.Fatalf("absence probe did not run: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("absence probe timed out")
	}
	replayCtx, cancelReplay := context.WithTimeout(ctx, 200*time.Millisecond)
	_, replayErr := workspaces.RegisterSourceVersion(replayCtx, newRegistration)
	expired := errors.Is(replayCtx.Err(), context.DeadlineExceeded)
	cancelReplay()
	if replayErr == nil || !expired {
		t.Fatalf("registration crossed absence lock: %v", replayErr)
	}
	close(releaseProbe)
	if err := <-presenceDone; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(newContent), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.RegisterSourceVersion(ctx, newRegistration); err != nil {
		t.Fatal(err)
	}
	var removed bool
	if err := f.db.Raw(`SELECT removed_at IS NOT NULL FROM core.source WHERE id=?`, string(ref.Source.SourceID)).Scan(&removed).Error; err != nil || removed {
		t.Fatalf("same-content restore retained tombstone: %v %v", removed, err)
	}
	impacts, err := service.ReadSynthesisSourceImpacts(ctx, app.SynthesisSourceImpactQuery{WorkspaceID: f.workspace, NoteID: note.Note.ID, RevisionID: note.CurrentRevision.ID})
	if err != nil || len(impacts.Items) != 1 || impacts.Items[0].CurrentlyUnavailable {
		t.Fatalf("restored source lost persistent reminder: %+v %v", impacts, err)
	}
	if err := os.WriteFile(filepath.Join(f.root, captured.ManagedLocation), []byte("tampered artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	unavailable := read()
	if unavailable.Text != "" || unavailable.SnapshotText != "" {
		t.Fatal("tampered snapshot was served")
	}
	if err := os.WriteFile(filepath.Join(f.root, captured.ManagedLocation), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
 SELECT ?,workspace_id,source_version_id,'validating','quarantined',parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,'historical-quarantine',2,clock_timestamp(),clock_timestamp() FROM ingestion.attempt WHERE id=?`, string(organizingIntegrationID(181050)), string(record.Input.SourceEvent.IngestionAttemptID)).Error; err != nil {
		t.Fatal(err)
	}
	quarantined := read()
	if quarantined.Text != "" || quarantined.SnapshotText != "" {
		t.Fatal("quarantined content was served")
	}
}

type historicalPresenceFunc func(context.Context, string, string) (bool, error)

func (f historicalPresenceFunc) MissingLocalSource(ctx context.Context, root, relative string) (bool, error) {
	return f(ctx, root, relative)
}
