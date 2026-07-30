package localfs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const secondExportID foundation.ID = "22222222-2222-4222-8222-222222222223"

func TestStageAttachmentsBuildsDeterministicPortableArchive(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.MkdirAll(filepath.Join(attachments, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"alpha.txt":         []byte("alpha\n"),
		"nested/binary.bin": {0x00, 0xff, 0x7f, 0x10},
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(attachments, filepath.FromSlash(name)), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := newTestStore(t, root)
	first, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	if err != nil {
		t.Fatalf("StageAttachments(first) error=%v", err)
	}
	second, err := store.StageAttachments(context.Background(), testWorkspaceID, secondExportID)
	if err != nil {
		t.Fatalf("StageAttachments(second) error=%v", err)
	}
	if first.ManifestHash != second.ManifestHash || first.PreparedFile.FileHash != second.PreparedFile.FileHash || first.PreparedFile.FileSize != second.PreparedFile.FileSize {
		t.Fatalf("deterministic bindings differ: first=%#v second=%#v", first, second)
	}
	if first.EntryCount != 2 || first.TotalUncompressedBytes != int64(len(files["alpha.txt"])+len(files["nested/binary.bin"])) {
		t.Fatalf("archive summary=%#v", first)
	}

	payload := readStorePayload(t, store, first.PreparedFile.StagingPath, first.PreparedFile.FileHash, first.PreparedFile.FileSize)
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("zip.NewReader() error=%v", err)
	}
	wantEntries := map[string][]byte{"attachments/alpha.txt": files["alpha.txt"], "attachments/nested/binary.bin": files["nested/binary.bin"]}
	var manifest attachmentManifest
	for _, entry := range archive.File {
		if entry.Method != zip.Store || !entry.Modified.Equal(deterministicZIPTime) || entry.Flags&0x800 == 0 || len(entry.Extra) != 0 || entry.Comment != "" {
			t.Fatalf("non-deterministic ZIP header for %q: method=%d modified=%s flags=%#x extra=%x comment=%q", entry.Name, entry.Method, entry.Modified, entry.Flags, entry.Extra, entry.Comment)
		}
		content := readZIPFile(t, entry)
		if entry.Name == "manifest.json" {
			if err := json.Unmarshal(content, &manifest); err != nil {
				t.Fatalf("manifest decode: %v", err)
			}
			continue
		}
		want, exists := wantEntries[entry.Name]
		if !exists || !bytes.Equal(content, want) {
			t.Fatalf("unexpected ZIP entry %q content=%v", entry.Name, content)
		}
		delete(wantEntries, entry.Name)
	}
	if len(wantEntries) != 0 || manifest.SchemaVersion != attachmentSchemaVersion || manifest.AttachmentRootContractVersion != attachmentRootVersion ||
		manifest.WorkspaceID != string(testWorkspaceID) || manifest.EntryCount != 2 || len(manifest.Entries) != 2 {
		t.Fatalf("manifest or entries are incomplete: manifest=%#v remaining=%v", manifest, wantEntries)
	}
}

func TestStageAttachmentsAllowsEmptyDirectoryAndRejectsMissingRoot(t *testing.T) {
	root := canonicalTempDir(t)
	store := newTestStore(t, root)
	if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
		t.Fatal("missing attachment root was accepted")
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeAttachmentRootNotFound || classified.Retryable {
			t.Fatalf("missing attachment root error=%v classified=%#v", err, classified)
		}
	}
	if err := os.Mkdir(filepath.Join(root, attachmentDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	archive, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	if err != nil {
		t.Fatalf("empty attachment root error=%v", err)
	}
	if archive.EntryCount != 0 || archive.TotalUncompressedBytes != 0 {
		t.Fatalf("empty archive summary=%#v", archive)
	}
}

func TestStageAttachmentsRejectsSymlinkHardlinkAndNormalizedCollision(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, root string) {
			outside := filepath.Join(root, "outside.txt")
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, attachmentDirectory, "link.txt")); err != nil {
				t.Fatal(err)
			}
		},
		"hardlink": func(t *testing.T, root string) {
			first := filepath.Join(root, attachmentDirectory, "first.txt")
			if err := os.WriteFile(first, []byte("same inode"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(first, filepath.Join(root, attachmentDirectory, "second.txt")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := canonicalTempDir(t)
			if err := os.Mkdir(filepath.Join(root, attachmentDirectory), 0o700); err != nil {
				t.Fatal(err)
			}
			setup(t, root)
			store := newTestStore(t, root)
			if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
				t.Fatalf("%s attachment tree was accepted", name)
			}
		})
	}
}

func TestRecordAttachmentPathRejectsCaseAndUnicodeCollisions(t *testing.T) {
	for name, paths := range map[string][]string{
		"case files":          {"A.txt", "a.txt"},
		"unicode files":       {"caf\u00e9.txt", "cafe\u0301.txt"},
		"case directories":    {"A", "a"},
		"unicode directories": {"caf\u00e9", "cafe\u0301"},
	} {
		t.Run(name, func(t *testing.T) {
			seen := map[string]string{}
			if err := recordAttachmentPath(seen, paths[0]); err != nil {
				t.Fatalf("first path rejected: %v", err)
			}
			if err := recordAttachmentPath(seen, paths[1]); err == nil {
				t.Fatalf("collision %q/%q was accepted", paths[0], paths[1])
			}
		})
	}
}

func TestStageAttachmentsSortsByNFCPathBytes(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"\u0100.txt", "\u212b.txt"} {
		if err := os.WriteFile(filepath.Join(attachments, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := newTestStore(t, root)
	result, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	if err != nil {
		t.Fatal(err)
	}
	payload := readStorePayload(t, store, result.PreparedFile.StagingPath, result.PreparedFile.FileHash, result.PreparedFile.FileSize)
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"manifest.json", "attachments/\u212b.txt", "attachments/\u0100.txt"}
	if len(archive.File) != len(want) {
		t.Fatalf("ZIP entries=%d want=%d", len(archive.File), len(want))
	}
	for index, name := range want {
		if archive.File[index].Name != name {
			t.Fatalf("ZIP entry[%d]=%q want=%q", index, archive.File[index].Name, name)
		}
	}
}

func TestStageAttachmentsRejectsFIFOAndSocket(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string) func()
	}{
		{
			name: "fifo",
			setup: func(t *testing.T, name string) func() {
				if err := syscall.Mkfifo(name, 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
		},
		{
			name: "socket",
			setup: func(t *testing.T, name string) func() {
				listener, err := net.Listen("unix", name)
				if err != nil {
					t.Fatal(err)
				}
				return func() { _ = listener.Close() }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := shortCanonicalTempDir(t)
			attachments := filepath.Join(root, attachmentDirectory)
			if err := os.Mkdir(attachments, 0o700); err != nil {
				t.Fatal(err)
			}
			cleanup := test.setup(t, filepath.Join(attachments, test.name))
			defer cleanup()
			store := newTestStore(t, root)
			if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
				t.Fatalf("%s attachment was accepted", test.name)
			}
			assertNoAttachmentStaging(t, root)
		})
	}
}

func TestStageAttachmentsRejectsSourceChangesAndRemovesStaging(t *testing.T) {
	for _, test := range []struct {
		name string
		hook func(*Store, string)
	}{
		{
			name: "content replacement after scan",
			hook: func(store *Store, attachments string) {
				store.attachmentHook.afterScan = func() {
					if err := os.WriteFile(filepath.Join(attachments, "source.bin"), []byte("changed"), 0o600); err != nil {
						panic(err)
					}
				}
			},
		},
		{
			name: "new file after archive",
			hook: func(store *Store, attachments string) {
				store.attachmentHook.afterArchive = func() {
					if err := os.WriteFile(filepath.Join(attachments, "added.bin"), []byte("added"), 0o600); err != nil {
						panic(err)
					}
				}
			},
		},
		{
			name: "new empty directory after archive",
			hook: func(store *Store, attachments string) {
				store.attachmentHook.afterArchive = func() {
					if err := os.Mkdir(filepath.Join(attachments, "added-empty"), 0o700); err != nil {
						panic(err)
					}
				}
			},
		},
		{
			name: "content replacement at final verification barrier",
			hook: func(store *Store, attachments string) {
				store.attachmentHook.beforeFinalVerification = func() {
					if err := os.WriteFile(filepath.Join(attachments, "source.bin"), []byte("changed"), 0o600); err != nil {
						panic(err)
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			attachments := filepath.Join(root, attachmentDirectory)
			if err := os.Mkdir(attachments, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(attachments, "source.bin"), []byte("initial"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newTestStore(t, root)
			test.hook(store, attachments)
			if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
				t.Fatal("attachment source mutation was accepted")
			}
			assertNoAttachmentStaging(t, root)
		})
	}
}

func TestStageAttachmentsRejectsFIFOReplacementBetweenLstatAndOpen(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(attachments, "source.bin")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.secureHook.afterFileLstat = func(relative string) {
		if relative != "source.bin" {
			return
		}
		store.secureHook.afterFileLstat = nil
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(source, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
		result <- err
	}()
	select {
	case err := <-result:
		assertAttachmentResultInconsistent(t, err)
	case <-time.After(time.Second):
		unblock, _ := os.OpenFile(source, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if unblock != nil {
			_ = unblock.Close()
		}
		t.Fatal("attachment FIFO replacement blocked the Worker")
	}
	assertNoAttachmentStaging(t, root)
}

func TestStageAttachmentsRejectsAttachmentRootReplacementAfterEmptyArchive(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := canonicalTempDir(t)
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.attachmentHook.afterArchive = func() {
		if err := os.Rename(attachments, attachments+"-original"); err != nil {
			panic(err)
		}
		if err := os.Symlink(outside, attachments); err != nil {
			panic(err)
		}
	}
	if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
		t.Fatal("empty attachment archive accepted a replaced attachment root")
	}
	payload, err := os.ReadFile(sentinel)
	if err != nil || string(payload) != "outside" {
		t.Fatalf("attachment root replacement touched outside sentinel: payload=%q err=%v", payload, err)
	}
	assertNoAttachmentStaging(t, root+"-original")
}

func TestStageAttachmentsRejectsNestedAncestorReplacement(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	nested := filepath.Join(attachments, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "source.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(outside, "source.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.attachmentHook.afterScan = func() {
		if err := os.Rename(nested, nested+"-original"); err != nil {
			panic(err)
		}
		if err := os.Symlink(outside, nested); err != nil {
			panic(err)
		}
	}
	if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
		t.Fatal("attachment archive accepted a replaced nested ancestor")
	}
	assertNoAttachmentStaging(t, root)
	payload, err := os.ReadFile(filepath.Join(outside, "source.txt"))
	if err != nil || string(payload) != "outside" {
		t.Fatalf("nested replacement touched outside source: payload=%q err=%v", payload, err)
	}
}

func TestStageAttachmentsRejectsAncestorReplacementThatPreservesFileAndParent(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	ancestor := filepath.Join(attachments, "ancestor")
	parent := filepath.Join(ancestor, "parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "source.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.attachmentHook.afterArchive = func() {
		original := filepath.Join(root, "ancestor-original")
		if err := os.Rename(ancestor, original); err != nil {
			panic(err)
		}
		if err := os.Mkdir(ancestor, 0o700); err != nil {
			panic(err)
		}
		if err := os.Rename(filepath.Join(original, "parent"), parent); err != nil {
			panic(err)
		}
	}
	if _, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID); err == nil {
		t.Fatal("attachment archive accepted a replaced ancestor with preserved file and parent identities")
	}
	assertNoAttachmentStaging(t, root)
}

func TestStageAttachmentsRejectsNestedReplacementBetweenLstatAndOpen(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	nested := filepath.Join(attachments, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "source.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.secureHook.afterChildLstat = func(name string) {
		if name != "nested" {
			return
		}
		store.secureHook.afterChildLstat = nil
		if err := os.Rename(nested, nested+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "source.txt"), []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	assertAttachmentResultInconsistent(t, err)
	payload, readErr := os.ReadFile(filepath.Join(nested, "source.txt"))
	if readErr != nil || string(payload) != "replacement" {
		t.Fatalf("nested replacement was touched: payload=%q err=%v", payload, readErr)
	}
	assertNoAttachmentStaging(t, root)
}

func TestStageAttachmentsRechecksRootDuringFinalVerification(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.attachmentHook.beforeFinalVerification = func() {
		if err := os.Rename(attachments, attachments+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(attachments, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	assertAttachmentResultInconsistent(t, err)
	assertNoAttachmentStaging(t, root)
}

func TestStageAttachmentsRechecksStagingHashBeforePrepare(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "source.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	store.attachmentHook.afterArchive = func() {
		staging := filepath.Join(root, filepath.FromSlash(stagingDirectory))
		entries, err := os.ReadDir(staging)
		if err != nil || len(entries) != 1 {
			t.Fatalf("staging entries=%v err=%v", entries, err)
		}
		archivePath := filepath.Join(staging, entries[0].Name())
		info, err := os.Stat(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(archivePath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, bytes.Repeat([]byte{'x'}, int(info.Size())), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	assertAttachmentResultInconsistent(t, err)
	assertNoAttachmentStaging(t, root)
}

func TestStageAttachmentsPinsStagingAcrossOrdinaryManagedReplacement(t *testing.T) {
	root := canonicalTempDir(t)
	attachments := filepath.Join(root, attachmentDirectory)
	if err := os.Mkdir(attachments, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "source.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, root)
	var sentinel string
	store.attachmentHook.afterArchive = func() {
		sentinel = replaceExportsWithOrdinaryDirectory(t, root, "attachment-sentinel.zip.stage")
	}
	_, err := store.StageAttachments(context.Background(), testWorkspaceID, testExportID)
	assertUnsafeManagedError(t, err)
	payload, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(payload) != "replacement sentinel" {
		t.Fatalf("attachment archive touched replacement sentinel: payload=%q err=%v", payload, readErr)
	}
	replacementEntries, readErr := os.ReadDir(filepath.Join(root, ".knowledge", "exports", ".staging"))
	if readErr != nil || len(replacementEntries) != 1 || replacementEntries[0].Name() != filepath.Base(sentinel) {
		t.Fatalf("attachment archive wrote replacement staging: entries=%v err=%v", replacementEntries, readErr)
	}
	payload, readErr = os.ReadFile(filepath.Join(attachments, "source.txt"))
	if readErr != nil || string(payload) != "source" {
		t.Fatalf("attachment archive touched source after managed replacement: payload=%q err=%v", payload, readErr)
	}
}

func TestAttachmentLimitsRejectWithoutTruncation(t *testing.T) {
	for name, test := range map[string]struct {
		entries int
		total   int64
		file    int64
	}{
		"entry count": {entries: maxAttachmentEntries, total: 0, file: 0},
		"single file": {entries: 0, total: 0, file: maxAttachmentFileBytes + 1},
		"total bytes": {entries: 1, total: maxAttachmentTotalBytes - 1, file: 2},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateAttachmentLimits(test.entries, test.total, test.file)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeAttachmentLimitExceeded || classified.Retryable {
				t.Fatalf("attachment limit was accepted: %#v", test)
			}
		})
	}
}

func TestBoundedArchiveWriterRejectsBeforeExceedingResultLimit(t *testing.T) {
	var output bytes.Buffer
	writer := &boundedArchiveWriter{writer: &output, remaining: 4}
	if written, err := writer.Write([]byte("1234")); err != nil || written != 4 {
		t.Fatalf("bounded write=%d err=%v", written, err)
	}
	written, err := writer.Write([]byte("5"))
	classifiedErr := archiveWriteFailure(err)
	var classified *foundation.Error
	if written != 0 || !errors.As(classifiedErr, &classified) || classified.Code != domain.ErrorCodeAttachmentLimitExceeded || classified.Retryable {
		t.Fatalf("overflow write=%d err=%v classified=%#v output=%q", written, err, classified, output.Bytes())
	}
	if output.String() != "1234" {
		t.Fatalf("overflow wrote partial bytes: %q", output.String())
	}
}

func assertNoAttachmentStaging(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(stagingDirectory)))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed attachment archive left staging files: %v", entries)
	}
}

func assertAttachmentResultInconsistent(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != domain.ErrorCodeResultInvalid || classified.Retryable {
		t.Fatalf("attachment consistency error=%v classified=%#v", err, classified)
	}
}

func shortCanonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "zx-exp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func readZIPFile(t *testing.T, file *zip.File) []byte {
	t.Helper()
	reader, err := file.Open()
	if err != nil {
		t.Fatalf("open ZIP entry %q: %v", file.Name, err)
	}
	defer reader.Close()
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read ZIP entry %q: %v", file.Name, err)
	}
	return payload
}
