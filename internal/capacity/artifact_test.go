package capacity

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactWritesAtomicallyWithRestrictedPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "capacity")
	path := filepath.Join(directory, "summary.json")
	if err := WriteAtomicArtifact(path, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "first\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomicArtifact(path, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "second\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "second\n" {
		t.Fatalf("artifact payload=%q", payload)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("artifact modes file=%#o directory=%#o", fileInfo.Mode().Perm(), directoryInfo.Mode().Perm())
	}
}

func TestPrepareArtifactDirectoryRejectsBroadOrLinkedTargets(t *testing.T) {
	if err := PrepareArtifactDirectory("."); err == nil {
		t.Fatal("working directory was accepted as an artifact directory")
	}
	root := string(filepath.Separator)
	if err := PrepareArtifactDirectory(root); err == nil {
		t.Fatal("filesystem root was accepted as an artifact directory")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "artifact-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := PrepareArtifactDirectory(link); err == nil {
		t.Fatal("symbolic-link artifact directory was accepted")
	}
}

func TestPrepareArtifactDirectoryDoesNotChangeExistingDirectoryPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareArtifactDirectory(directory); err == nil {
		t.Fatal("existing non-private artifact directory was accepted")
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing directory mode was changed to %#o", info.Mode().Perm())
	}
}
