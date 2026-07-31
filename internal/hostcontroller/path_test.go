package hostcontroller

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspacePathContractFixtureMatchesReservedRules(t *testing.T) {
	t.Parallel()
	document, err := os.ReadFile(filepath.Join("..", "..", "deploy", "workspace_path_contract.json"))
	if err != nil {
		t.Fatalf("read shared path fixture: %v", err)
	}
	var cases []struct {
		Path  string `json:"path"`
		Valid bool   `json:"valid"`
	}
	if err := json.Unmarshal(document, &cases); err != nil {
		t.Fatalf("decode shared path fixture: %v", err)
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.Path, func(t *testing.T) {
			t.Parallel()
			err := validateMountTarget(testCase.Path)
			if (err == nil) != testCase.Valid {
				t.Fatalf("validateMountTarget(%q) error=%v, valid=%v", testCase.Path, err, testCase.Valid)
			}
		})
	}
}

func TestPathValidatorReturnsCanonicalPhysicalIdentity(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	parent := t.TempDir()
	alias := filepath.Join(parent, "workspace-link")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	validated, err := (PathValidator{}).Validate(alias)
	if err != nil {
		t.Fatalf("validate stable alias: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("canonicalize fixture: %v", err)
	}
	if validated.CanonicalPath != canonical {
		t.Fatalf("canonical path=%q, want %q", validated.CanonicalPath, canonical)
	}
	if validated.Fingerprint.PhysicalPath != canonical || validated.Fingerprint.Device == 0 || validated.Fingerprint.Inode == 0 || validated.Fingerprint.BindingVersion != 1 {
		t.Fatalf("unexpected fingerprint: %+v", validated.Fingerprint)
	}
}

func TestPathValidatorFailsClosed(t *testing.T) {
	t.Parallel()
	regularFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(regularFile, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	aliasParent := t.TempDir()
	reservedAlias := filepath.Join(aliasParent, "reserved")
	if err := os.Symlink("/tmp", reservedAlias); err != nil {
		t.Fatalf("create reserved alias: %v", err)
	}

	tests := []struct {
		name string
		path string
		code string
	}{
		{name: "empty", path: "", code: "WORKSPACE_PATH_INVALID"},
		{name: "relative", path: "workspace", code: "WORKSPACE_PATH_NOT_ABSOLUTE"},
		{name: "double slash", path: "//tmp/workspace", code: "WORKSPACE_PATH_NOT_ABSOLUTE"},
		{name: "control", path: "/tmp/a\nsecret", code: "WORKSPACE_PATH_INVALID"},
		{name: "missing", path: filepath.Join(t.TempDir(), "missing"), code: "WORKSPACE_PATH_NOT_FOUND"},
		{name: "file", path: regularFile, code: "WORKSPACE_PATH_NOT_DIRECTORY"},
		{name: "root", path: "/", code: "WORKSPACE_PATH_RESERVED"},
		{name: "legacy", path: "/workspace/project", code: "WORKSPACE_PATH_RESERVED"},
		{name: "reserved alias", path: reservedAlias, code: "WORKSPACE_PATH_RESERVED"},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := (PathValidator{}).Validate(testCase.path)
			var pathError *PathError
			if !errors.As(err, &pathError) || pathError.Code != testCase.code {
				t.Fatalf("Validate() error=%v, want code %s", err, testCase.code)
			}
			if testCase.path != "" && pathError.Error() == testCase.path {
				t.Fatal("path error leaked the host path")
			}
		})
	}
}

func TestValidateStateDirectoryRejectsSymlinkAndPermissiveMode(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	state := filepath.Join(parent, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	if err := ValidateStateDirectory(state); err != nil {
		t.Fatalf("secure state rejected: %v", err)
	}
	if err := os.Chmod(state, 0o755); err != nil {
		t.Fatalf("chmod state: %v", err)
	}
	if err := ValidateStateDirectory(state); err == nil {
		t.Fatal("permissive state directory was accepted")
	}
	alias := filepath.Join(parent, "state-link")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatalf("create state symlink: %v", err)
	}
	if err := ValidateStateDirectory(alias); err == nil {
		t.Fatal("state directory symlink was accepted")
	}
}
