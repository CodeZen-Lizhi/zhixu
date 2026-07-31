package candidateprobe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
)

func TestVerifyInitializesExactGitRootAndRemovesProbe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "知识 workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rootgrant.NewProcessGrant(foundation.ID("550e8400-e29b-41d4-a716-446655440000"), root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), grant, true, ExecGitRunner{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".git", "zhixu", "runtime-probes"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("candidate probe residue=%v", entries)
	}
	if err := Verify(context.Background(), grant, false, ExecGitRunner{}); err != nil {
		t.Fatalf("existing Git worktree rejected: %v", err)
	}
}

func TestVerifyRemovesZeroByteProbeLeftByCrash(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	probeDirectory := filepath.Join(root, ".git", "zhixu", "runtime-probes")
	if err := os.MkdirAll(probeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	staleProbe := filepath.Join(probeDirectory, "0123456789abcdef0123456789abcdef.probe")
	if err := os.WriteFile(staleProbe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	grant, err := rootgrant.NewProcessGrant(foundation.ID("550e8400-e29b-41d4-a716-446655440000"), root, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := Verify(context.Background(), grant, false, ExecGitRunner{}); err != nil {
		t.Fatalf("Verify() error=%v", err)
	}
	entries, err := os.ReadDir(probeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("candidate crash residue=%v", entries)
	}
}

func TestVerifyDoesNotDeleteUnsafeProbeResidue(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	probeDirectory := filepath.Join(root, ".git", "zhixu", "runtime-probes")
	if err := os.MkdirAll(probeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeProbe := filepath.Join(probeDirectory, "fedcba9876543210fedcba9876543210.probe")
	if err := os.WriteFile(unsafeProbe, []byte("not a protocol probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	grant, err := rootgrant.NewProcessGrant(foundation.ID("550e8400-e29b-41d4-a716-446655440000"), root, 3)
	if err != nil {
		t.Fatal(err)
	}

	err = Verify(context.Background(), grant, false, ExecGitRunner{})
	if FailureKindOf(err) != FailureProbe {
		t.Fatalf("Verify() error=%v kind=%q", err, FailureKindOf(err))
	}
	if content, readErr := os.ReadFile(unsafeProbe); readErr != nil || string(content) != "not a protocol probe" {
		t.Fatalf("unsafe residue changed: content=%q error=%v", content, readErr)
	}
}

func TestVerifyRejectsGitMetadataOutsideExactRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	gitDirectory := filepath.Join(base, "external-git")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gitDirectory, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	gitDirectory = filepath.Join(gitDirectory, "external-git")
	command := exec.Command("git", "init", "--quiet", "--separate-git-dir", gitDirectory, root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	grant, err := rootgrant.NewProcessGrant(foundation.ID("550e8400-e29b-41d4-a716-446655440000"), root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), grant, false, ExecGitRunner{}); err == nil {
		t.Fatal("Verify() accepted Git metadata outside the exact Workspace root")
	} else if got := FailureKindOf(err); got != FailureGitMetadataOutsideRoot {
		t.Fatalf("Verify() failure kind=%q", got)
	}
}

func TestVerifyClassifiesGitRequiredAndInitializationFailure(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rootgrant.NewProcessGrant(foundation.ID("550e8400-e29b-41d4-a716-446655440000"), root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), grant, false, ExecGitRunner{}); FailureKindOf(err) != FailureGitRequired {
		t.Fatalf("non-Git failure=%v kind=%q", err, FailureKindOf(err))
	}
	if err := Verify(context.Background(), grant, true, failingGitRunner{}); FailureKindOf(err) != FailureGitInitialize {
		t.Fatalf("Git init failure=%v kind=%q", err, FailureKindOf(err))
	}
}

func TestExitCodeMapsTypedFailure(t *testing.T) {
	err := candidateFailure(FailurePermission, errors.New("fixture permission failure"))
	if got := ExitCode(err); got != ExitPermission {
		t.Fatalf("ExitCode()=%d want=%d", got, ExitPermission)
	}
	if got := ExitCode(errors.New("unknown")); got != ExitProbe {
		t.Fatalf("unknown ExitCode()=%d want=%d", got, ExitProbe)
	}
}

type failingGitRunner struct{}

func (failingGitRunner) Run(context.Context, ...string) ([]byte, error) {
	return nil, errors.New("fixture Git failure")
}
