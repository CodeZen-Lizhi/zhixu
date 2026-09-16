package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSourcePresenceDistinguishesAbsenceFromUnsafePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "present.md"), []byte("note"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("absent.md", filepath.Join(root, "broken.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		missing, fails bool
	}{
		{"present.md", false, false}, {"absent.md", true, false}, {"absent/child.md", true, false},
		{"broken.md", false, true}, {"escape/child.md", false, true}, {"present.md/child.md", false, true},
		{"../outside.md", false, true}, {"/outside.md", false, true}, {".knowledge/absent.md", false, true},
		{"capture://input", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing, err := (Scanner{}).MissingLocalSource(t.Context(), root, tc.name)
			if missing != tc.missing || (err != nil) != tc.fails {
				t.Fatalf("missing=%v err=%v", missing, err)
			}
		})
	}
	if missing, err := (Scanner{}).MissingLocalSource(t.Context(), filepath.Join(root, "missing-root"), "note.md"); missing || err == nil {
		t.Fatalf("missing root treated as absence: %v %v", missing, err)
	}
}

func TestSourceDiscoveryPagesFollowTraversalOrderAndExcludeManagedPaths(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"a/b.md", "a.txt", "z.md", ".git/private.md", ".knowledge/generated.md", "tmp/excluded.md"} {
		target := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("note"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{}
	var paths []string
	after := ""
	for n := 0; n < 20; n++ {
		page, err := scanner.ScanPage(t.Context(), root, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, page.Paths...)
		if page.Done {
			break
		}
		if page.After == after {
			t.Fatal("discovery cursor stalled")
		}
		after = page.After
	}
	if len(paths) != 3 || paths[0] != "a/b.md" || paths[1] != "a.txt" || paths[2] != "z.md" {
		t.Fatalf("paths=%v", paths)
	}
	first, err := scanner.ObserveSource(t.Context(), root, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("updated"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := scanner.ObserveSource(t.Context(), root, "a.txt")
	if err != nil || first.ContentHash == second.ContentHash {
		t.Fatalf("file edit not observed: %+v %v", second, err)
	}
	if _, err := (Scanner{Options: ScanOptions{MaxBytes: 2}}).ObserveSource(t.Context(), root, "a.txt"); err == nil {
		t.Fatal("oversize file accepted")
	}
	if _, err := scanner.ObserveSource(t.Context(), root, ".knowledge/generated.md"); err == nil {
		t.Fatal("managed content observed")
	}
}

func TestDiscoverySessionRetainsAuthorizedRootAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("authorized"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := (Scanner{}).BindSourceDiscovery(t.Context(), root, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("ungranted"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := session.ObserveSource(t.Context(), root, "note.md")
	if err != nil || observed.ByteSize != int64(len("authorized")) {
		t.Fatalf("read replaced root: %+v %v", observed, err)
	}
	captured, err := session.Capture(t.Context(), root, observed)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root+"-old", captured.ManagedLocation))
	if err != nil || string(data) != "authorized" {
		t.Fatalf("captured wrong physical root: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".knowledge")); !os.IsNotExist(err) {
		t.Fatalf("wrote to replacement root: %v", err)
	}
	if err := session.Revalidate(t.Context()); err == nil {
		t.Fatal("replaced root allowed registration")
	}
}

func TestSourceDiscoveryUnreadableDirectoryAtPageBoundary(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "blocked")
	if err := os.Mkdir(directory, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
	if _, err := os.ReadDir(directory); err == nil {
		t.Skip("test requires directory read permissions to be enforced")
	}
	if err := os.WriteFile(filepath.Join(root, "next.md"), []byte("note"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := (Scanner{}).ScanPage(t.Context(), root, "", 1)
	if err != nil || page.Failed != 1 || len(page.Failures) != 1 || page.Failures[0].Path != "blocked" || page.Failures[0].Code != "DIRECTORY_READ_FAILED" || len(page.ReadDirectories) != 0 || page.After != "blocked" || page.Done || len(page.Paths) != 0 {
		t.Fatalf("directory failure was recovered at page boundary: %+v %v", page, err)
	}
	page, err = (Scanner{}).ScanPage(t.Context(), root, "blocked", 1)
	if err != nil || page.Failed != 1 || len(page.Failures) != 1 || len(page.ReadDirectories) != 0 || page.After != "next.md" || !page.Done || len(page.Paths) != 1 || page.Paths[0] != "next.md" {
		t.Fatalf("cursor hid observed directory read failure: %+v %v", page, err)
	}
	page, err = (Scanner{}).ScanPage(t.Context(), root, "", 2)
	if err != nil || page.Failed != 1 || len(page.ReadDirectories) != 0 || len(page.Paths) != 1 || page.Paths[0] != "next.md" || !page.Done {
		t.Fatalf("second directory callback consumed a page entry: %+v %v", page, err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	page, err = (Scanner{}).ScanPage(t.Context(), root, "", 1)
	if err != nil || len(page.Failures) != 0 || len(page.ReadDirectories) != 1 || page.ReadDirectories[0] != "blocked" {
		t.Fatalf("successful directory read absent: %+v %v", page, err)
	}
}
