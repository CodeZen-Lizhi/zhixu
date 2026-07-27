package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	testWorkspaceID foundation.ID = "11111111-1111-4111-8111-111111111111"
	testExportID    foundation.ID = "22222222-2222-4222-8222-222222222222"
)

type workspaceReaderFake struct{ workspace workspacedomain.Workspace }

func (reader workspaceReaderFake) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return reader.workspace, nil
}

func TestStoreWritesReadsAndDeletesBoundExport(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	relative := ".knowledge/exports/" + string(testExportID) + ".md"
	payload := []byte("# export\n")
	digest := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(digest[:])

	path, hash, size, err := store.Write(context.Background(), testWorkspaceID, relative, payload)
	if err != nil || path != relative || hash != wantHash || size != int64(len(payload)) {
		t.Fatalf("Write() path=%q hash=%q size=%d err=%v", path, hash, size, err)
	}
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("export mode=%v err=%v", info.Mode(), err)
	}
	read, err := store.Read(context.Background(), testWorkspaceID, relative, hash, size)
	if err != nil || string(read) != string(payload) {
		t.Fatalf("Read()=%q err=%v", read, err)
	}
	if _, _, _, err := store.Write(context.Background(), testWorkspaceID, relative, payload); err != nil {
		t.Fatalf("idempotent Write() error=%v", err)
	}
	if _, _, _, err := store.Write(context.Background(), testWorkspaceID, relative, []byte("different")); err == nil {
		t.Fatal("Write() replaced an existing export with different content")
	}
	if err := store.DeletePrepared(context.Background(), testWorkspaceID, relative, hash, size); err != nil {
		t.Fatalf("DeletePrepared() error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
		t.Fatalf("export still exists after DeletePrepared(): %v", err)
	}
	if err := store.DeletePrepared(context.Background(), testWorkspaceID, relative, hash, size); err != nil {
		t.Fatalf("idempotent DeletePrepared() error=%v", err)
	}
}

func TestStoreRejectsTamperingUnsafePathsAndSymlinkDirectory(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	relative := ".knowledge/exports/" + string(testExportID) + ".json"
	payload := []byte("{\"ok\":true}\n")
	_, hash, size, err := store.Write(context.Background(), testWorkspaceID, relative, payload)
	if err != nil {
		t.Fatalf("Write() error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), []byte("{\"no\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), testWorkspaceID, relative, hash, size); err == nil {
		t.Fatal("Read() accepted tampered export")
	}
	for _, candidate := range []string{"/tmp/export.md", ".knowledge/exports/../escape.md", ".knowledge/exports/not-a-uuid.md", ".knowledge/exports/" + string(testExportID) + ".csv"} {
		if _, _, _, err := store.Write(context.Background(), testWorkspaceID, candidate, payload); err == nil {
			t.Fatalf("Write() accepted unsafe path %q", candidate)
		}
	}

	symlinkRoot := canonicalTempDir(t)
	outside := canonicalTempDir(t)
	if err := os.Mkdir(filepath.Join(symlinkRoot, ".knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(symlinkRoot, ".knowledge", "exports")); err != nil {
		t.Fatal(err)
	}
	symlinkStore := newTestStore(t, symlinkRoot)
	if _, _, _, err := symlinkStore.Write(context.Background(), testWorkspaceID, relative, payload); err == nil {
		t.Fatal("Write() accepted a symlink export directory")
	}
}

func TestStoreDeletePreparedPreservesMismatchedFile(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	prepared, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".md", []byte("# prepared\n"))
	if err != nil {
		t.Fatalf("Stage() error=%v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(prepared.StagingPath))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove staged file: %v", err)
	}
	const replacement = "# user replacement\n"
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	if err := store.DeletePrepared(context.Background(), testWorkspaceID, prepared.StagingPath, prepared.FileHash, prepared.FileSize); err == nil {
		t.Fatal("DeletePrepared() removed a file that no longer matched its durable binding")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != replacement {
		t.Fatalf("mismatched file was changed: content=%q err=%v", content, err)
	}
}

func TestStoreStagesAndPromotesRecoverably(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	payload := []byte("# staged export\n")

	prepared, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".md", payload)
	if err != nil {
		t.Fatalf("Stage() error=%v", err)
	}
	if prepared.StagingPath == "" || prepared.FinalPath != ".knowledge/exports/"+string(testExportID)+".md" || prepared.FileSize != int64(len(payload)) {
		t.Fatalf("Stage() returned invalid binding: %#v", prepared)
	}
	stagedInfo, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prepared.StagingPath)))
	if err != nil {
		t.Fatalf("Lstat(staging) error=%v", err)
	}
	if !stagedInfo.Mode().IsRegular() || stagedInfo.Mode().Perm() != 0o600 {
		t.Fatalf("staging mode=%v", stagedInfo.Mode())
	}
	staged, err := store.Read(context.Background(), testWorkspaceID, prepared.StagingPath, prepared.FileHash, prepared.FileSize)
	if err != nil || string(staged) != string(payload) {
		t.Fatalf("Read(staging)=%q err=%v", staged, err)
	}

	if err := store.Promote(context.Background(), testWorkspaceID, prepared); err != nil {
		t.Fatalf("Promote() error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prepared.StagingPath))); !os.IsNotExist(err) {
		t.Fatalf("staging remains after Promote(): %v", err)
	}
	final, err := store.Read(context.Background(), testWorkspaceID, prepared.FinalPath, prepared.FileHash, prepared.FileSize)
	if err != nil || string(final) != string(payload) {
		t.Fatalf("Read(final)=%q err=%v", final, err)
	}
	if err := store.Promote(context.Background(), testWorkspaceID, prepared); err != nil {
		t.Fatalf("recovered Promote() error=%v", err)
	}
}

func TestStoreStageIsCreateOnlyAndFinalConflictFailsClosed(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	first, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".json", []byte("{\"value\":1}\n"))
	if err != nil {
		t.Fatalf("first Stage() error=%v", err)
	}
	second, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".json", []byte("{\"value\":2}\n"))
	if err != nil {
		t.Fatalf("second Stage() error=%v", err)
	}
	if first.StagingPath == second.StagingPath || first.FileHash == second.FileHash {
		t.Fatalf("Stage() overwrote a staging result: first=%#v second=%#v", first, second)
	}
	if _, _, _, err := store.Write(context.Background(), testWorkspaceID, first.FinalPath, []byte("{\"other\":true}\n")); err != nil {
		t.Fatalf("Write(final) error=%v", err)
	}
	if err := store.Promote(context.Background(), testWorkspaceID, first); err == nil {
		t.Fatal("Promote() accepted a different existing final file")
	}
	final, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(first.FinalPath)))
	if err != nil || string(final) != "{\"other\":true}\n" {
		t.Fatalf("Promote() replaced the existing final file: content=%q err=%v", final, err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(first.StagingPath))); err != nil {
		t.Fatalf("failed Promote() removed staging: %v", err)
	}
}

func TestStorePromoteAcceptsMatchingExistingFinal(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	payload := []byte("# matching result\n")
	prepared, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".md", payload)
	if err != nil {
		t.Fatalf("Stage() error=%v", err)
	}
	if _, _, _, err := store.Write(context.Background(), testWorkspaceID, prepared.FinalPath, payload); err != nil {
		t.Fatalf("Write(final) error=%v", err)
	}
	if err := store.Promote(context.Background(), testWorkspaceID, prepared); err != nil {
		t.Fatalf("Promote() rejected a matching final file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prepared.StagingPath))); !os.IsNotExist(err) {
		t.Fatalf("matching Promote() left staging behind: %v", err)
	}
}

func TestStorePromoteRecoversAfterRenameBeforeCompletion(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	prepared, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".md", []byte("# crash window\n"))
	if err != nil {
		t.Fatalf("Stage() error=%v", err)
	}
	if err := os.Rename(filepath.Join(root, filepath.FromSlash(prepared.StagingPath)), filepath.Join(root, filepath.FromSlash(prepared.FinalPath))); err != nil {
		t.Fatalf("simulate rename error=%v", err)
	}
	if err := store.Promote(context.Background(), testWorkspaceID, prepared); err != nil {
		t.Fatalf("Promote() did not recover renamed final: %v", err)
	}
}

func TestStoreReadsDeletesAndListsOnlySafeStagingFiles(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	first, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".md", []byte("# first\n"))
	if err != nil {
		t.Fatalf("Stage() error=%v", err)
	}
	second, err := store.Stage(context.Background(), testWorkspaceID, testExportID, ".md", []byte("# second\n"))
	if err != nil {
		t.Fatalf("Stage() error=%v", err)
	}
	old := time.Now().Add(-time.Hour)
	for _, staged := range []string{first.StagingPath, second.StagingPath} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(staged)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".knowledge", "exports", ".staging", "ordinary.txt"), []byte("leave me alone"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListStaging(context.Background(), testWorkspaceID, time.Now().Add(-time.Minute), 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("bounded ListStaging()=%#v err=%v", candidates, err)
	}
	candidates, err = store.ListStaging(context.Background(), testWorkspaceID, time.Now().Add(-time.Minute), maxStagingCandidates)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("ListStaging()=%#v err=%v", candidates, err)
	}
	if err := store.DeleteOrphan(context.Background(), testWorkspaceID, first.StagingPath); err != nil {
		t.Fatalf("DeleteOrphan(staging) error=%v", err)
	}
	if err := store.DeleteOrphan(context.Background(), testWorkspaceID, first.StagingPath); err != nil {
		t.Fatalf("idempotent DeleteOrphan(staging) error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".knowledge", "exports", ".staging", "ordinary.txt")); err != nil {
		t.Fatalf("List/Delete touched ordinary staging neighbor: %v", err)
	}

	if err := os.Remove(filepath.Join(root, filepath.FromSlash(second.StagingPath))); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, filepath.FromSlash(second.StagingPath))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), testWorkspaceID, second.StagingPath, second.FileHash, second.FileSize); err == nil {
		t.Fatal("Read() accepted a staging symlink")
	}
	if err := store.DeleteOrphan(context.Background(), testWorkspaceID, second.StagingPath); err == nil {
		t.Fatal("DeleteOrphan() accepted a staging symlink")
	}
	if _, err := store.ListStaging(context.Background(), testWorkspaceID, time.Now(), maxStagingCandidates); err == nil {
		t.Fatal("ListStaging() accepted a controlled-name symlink")
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

func newTestStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := NewStore(workspaceReaderFake{workspace: workspacedomain.Workspace{ID: testWorkspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
