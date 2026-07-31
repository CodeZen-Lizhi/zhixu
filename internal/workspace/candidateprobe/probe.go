// Package candidateprobe verifies an exact Workspace mount inside an
// unprivileged one-off API or Worker container before activation.
package candidateprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
)

var runtimeProbeNamePattern = regexp.MustCompile(`^[0-9a-f]{32}\.probe$`)

const (
	EnvOperationID     = "ZHIXU_WORKSPACE_OPERATION_ID"
	EnvRootFingerprint = "ZHIXU_WORKSPACE_ROOT_FINGERPRINT"
	EnvBindingVersion  = "ZHIXU_WORKSPACE_BINDING_VERSION"
)

// FailureKind is the non-sensitive reason a candidate runtime could not
// prove the exact Workspace grant.
type FailureKind string

const (
	FailureConfiguration          FailureKind = "configuration"
	FailurePathIdentity           FailureKind = "path_identity"
	FailurePathUnavailable        FailureKind = "path_unavailable"
	FailurePermission             FailureKind = "permission"
	FailureGitRequired            FailureKind = "git_required"
	FailureGitInvalid             FailureKind = "git_invalid"
	FailureGitMetadataOutsideRoot FailureKind = "git_metadata_outside_root"
	FailureGitInitialize          FailureKind = "git_initialize"
	FailureProbe                  FailureKind = "probe"
)

// Candidate process exit codes form a private protocol between the bundled
// probe and the native Host Controller.
const (
	ExitConfiguration = 20 + iota
	ExitPathIdentity
	ExitPathUnavailable
	ExitPermission
	ExitGitRequired
	ExitGitInvalid
	ExitGitMetadataOutsideRoot
	ExitGitInitialize
	ExitProbe
	ExitDatabaseUnavailable
	ExitRuntimeRegistration
)

// Failure preserves a typed candidate failure without exposing the host path
// or raw command output across the process boundary.
type Failure struct {
	Kind  FailureKind
	cause error
}

func (failure *Failure) Error() string {
	if failure == nil || failure.Kind == "" {
		return "candidate verification failed"
	}
	return "candidate verification failed: " + string(failure.Kind)
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// FailureKindOf returns the stable candidate failure classification.
func FailureKindOf(err error) FailureKind {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return ""
}

// ExitCode maps a typed verification failure to the candidate process
// protocol. Unknown failures remain a generic probe failure.
func ExitCode(err error) int {
	switch FailureKindOf(err) {
	case FailureConfiguration:
		return ExitConfiguration
	case FailurePathIdentity:
		return ExitPathIdentity
	case FailurePathUnavailable:
		return ExitPathUnavailable
	case FailurePermission:
		return ExitPermission
	case FailureGitRequired:
		return ExitGitRequired
	case FailureGitInvalid:
		return ExitGitInvalid
	case FailureGitMetadataOutsideRoot:
		return ExitGitMetadataOutsideRoot
	case FailureGitInitialize:
		return ExitGitInitialize
	default:
		return ExitProbe
	}
}

// GitRunner is the fixed-argv Git boundary used by the candidate probe.
type GitRunner interface {
	Run(context.Context, ...string) ([]byte, error)
}

// ExecGitRunner invokes Git without a shell.
type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("candidate Git command failed")
	}
	return output, nil
}

// Verify proves that the injected process root is the mounted canonical Git
// worktree and that the runtime user can create and remove a zero-byte probe
// only inside the repository-owned metadata namespace.
func Verify(ctx context.Context, grant rootgrant.ProcessGrant, initializeGit bool, git GitRunner) error {
	if ctx == nil || git == nil {
		return candidateFailure(FailureConfiguration, errors.New("candidate probe dependencies are invalid"))
	}
	root := grant.CanonicalRoot()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolvedRoot) != root {
		return candidateFailure(FailurePathIdentity, err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return candidateFailure(pathFailureKind(err), err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return candidateFailure(FailurePathUnavailable, errors.New("candidate Workspace root is unavailable"))
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return candidateFailure(pathFailureKind(err), err)
	}
	defer rootHandle.Close()
	handleInfo, err := rootHandle.Stat(".")
	if err != nil || !os.SameFile(rootInfo, handleInfo) {
		return candidateFailure(FailurePathIdentity, err)
	}

	if initializeGit {
		if _, err := git.Run(ctx, "-C", root, "init", "--quiet"); err != nil {
			return candidateFailure(FailureGitInitialize, err)
		}
	}
	topLevel, err := git.Run(ctx, "-C", root, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(topLevel)) != root {
		return candidateFailure(FailureGitRequired, err)
	}
	bare, err := git.Run(ctx, "-C", root, "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(string(bare)) != "false" {
		return candidateFailure(FailureGitInvalid, err)
	}
	gitDirectoryOutput, err := git.Run(ctx, "-C", root, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return candidateFailure(FailureGitInvalid, err)
	}
	gitDirectory := strings.TrimSpace(string(gitDirectoryOutput))
	resolvedGitDirectory, err := filepath.EvalSymlinks(gitDirectory)
	if err != nil || !pathWithin(root, resolvedGitDirectory) {
		return candidateFailure(FailureGitMetadataOutsideRoot, err)
	}
	gitRelative, err := filepath.Rel(root, resolvedGitDirectory)
	if err != nil || gitRelative == ".." || strings.HasPrefix(gitRelative, ".."+string(filepath.Separator)) {
		return candidateFailure(FailureGitMetadataOutsideRoot, err)
	}
	gitRoot, err := rootHandle.OpenRoot(gitRelative)
	if err != nil {
		return candidateFailure(FailureProbe, err)
	}
	defer gitRoot.Close()
	probeDirectory := filepath.Join("zhixu", "runtime-probes")
	if err := gitRoot.MkdirAll(probeDirectory, 0o700); err != nil {
		return candidateFailure(pathFailureKind(err), err)
	}
	probeDirectoryInfo, err := gitRoot.Lstat(probeDirectory)
	if err != nil || !probeDirectoryInfo.IsDir() || probeDirectoryInfo.Mode()&os.ModeSymlink != 0 {
		return candidateFailure(FailureProbe, err)
	}
	probeRoot, err := gitRoot.OpenRoot(probeDirectory)
	if err != nil {
		return candidateFailure(FailureProbe, err)
	}
	defer probeRoot.Close()
	openedProbeInfo, err := probeRoot.Stat(".")
	if err != nil || !os.SameFile(probeDirectoryInfo, openedProbeInfo) {
		return candidateFailure(FailureProbe, err)
	}
	if err := removeStaleRuntimeProbes(probeRoot); err != nil {
		return candidateFailure(FailureProbe, err)
	}
	probeName, err := randomProbeName()
	if err != nil {
		return candidateFailure(FailureProbe, err)
	}
	probe, err := probeRoot.OpenFile(probeName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return candidateFailure(pathFailureKind(err), err)
	}
	probePresent := true
	defer func() {
		if probePresent {
			_ = probeRoot.Remove(probeName)
		}
	}()
	if err := probe.Close(); err != nil {
		return candidateFailure(FailureProbe, err)
	}
	probeInfo, err := probeRoot.Lstat(probeName)
	if err != nil || !probeInfo.Mode().IsRegular() || probeInfo.Size() != 0 {
		return candidateFailure(FailureProbe, err)
	}
	if err := probeRoot.Remove(probeName); err != nil {
		return candidateFailure(FailureProbe, err)
	}
	probePresent = false
	if _, err := git.Run(ctx, "-C", root, "status", "--porcelain=v1", "--untracked-files=no"); err != nil {
		return candidateFailure(FailureGitInvalid, err)
	}
	currentInfo, err := os.Lstat(root)
	if err != nil || !os.SameFile(rootInfo, currentInfo) {
		return candidateFailure(FailurePathIdentity, err)
	}
	return nil
}

func removeStaleRuntimeProbes(root *os.Root) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !runtimeProbeNamePattern.MatchString(entry.Name()) {
			continue
		}
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != 0 {
			return errors.New("candidate runtime probe residue is unsafe")
		}
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func candidateFailure(kind FailureKind, cause error) error {
	if cause == nil {
		cause = errors.New("candidate verification failed")
	}
	return &Failure{Kind: kind, cause: cause}
}

func pathFailureKind(err error) FailureKind {
	if errors.Is(err, fs.ErrPermission) {
		return FailurePermission
	}
	return FailurePathUnavailable
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func randomProbeName() (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate candidate probe identity: %w", err)
	}
	return hex.EncodeToString(raw) + ".probe", nil
}
