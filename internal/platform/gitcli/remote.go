package gitcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/remoteurl"
)

const (
	remoteStatusOutputLimit = 16 << 20
	remoteDiffOutputLimit   = 16 << 20
	remoteConfigOutputLimit = 1 << 20
	remoteRefPrefix         = "refs/zhixu/gitsync/"
)

// RemoteClient implements the Git-sync remote port with an ephemeral AskPass
// credential session. It never configures a repository remote or credential helper.
type RemoteClient struct {
	git        Client
	workspaces WorkspaceRepository
	policy     remoteurl.Policy
	allowLocal bool
}

// NewRemoteClient binds remote synchronization to server-owned Workspace roots.
func NewRemoteClient(git Client, workspaces WorkspaceRepository, policy remoteurl.Policy) (*RemoteClient, error) {
	if workspaces == nil {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, false, "workspace repository is unavailable", nil)
	}
	if policy == nil {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, false, "Git remote URL policy is unavailable", nil)
	}
	return newRemoteClient(git, workspaces, policy, false), nil
}

func newRemoteClientForTests(git Client, workspaces WorkspaceRepository) (*RemoteClient, error) {
	if workspaces == nil {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, false, "workspace repository is unavailable", nil)
	}
	return newRemoteClient(git, workspaces, nil, true), nil
}

func newRemoteClient(git Client, workspaces WorkspaceRepository, policy remoteurl.Policy, allowLocal bool) *RemoteClient {
	if strings.TrimSpace(git.executable) == "" {
		git = New("")
	}
	return &RemoteClient{git: git, workspaces: workspaces, policy: policy, allowLocal: allowLocal}
}

// TestConnection verifies that the configured branch is readable without persisting a remote.
func (c *RemoteClient) TestConnection(ctx context.Context, access application.GitAccess) error {
	if _, err := c.resolveRoot(ctx, access); err != nil {
		return err
	}
	result, err := c.runRemote(ctx, access, "ls-remote", "--heads", access.RemoteURL, remoteBranchRef(access.Branch))
	if err != nil {
		return classifyRemoteError(err, false)
	}
	if remoteOIDFromLSRemote(result.Stdout, remoteBranchRef(access.Branch)) == "" {
		return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "configured remote branch is absent", nil)
	}
	return nil
}

// Fetch updates one private, adapter-owned tracking ref and does not alter Git config.
func (c *RemoteClient) Fetch(ctx context.Context, access application.GitAccess) error {
	if _, err := c.resolveRoot(ctx, access); err != nil {
		return err
	}
	if _, err := c.runRemote(ctx, access, "fetch", "--no-tags", "--no-write-fetch-head", access.RemoteURL,
		"+"+remoteBranchRef(access.Branch)+":"+privateRemoteRef(access.Branch)); err != nil {
		return classifyRemoteError(err, false)
	}
	return nil
}

// Compare reads the attached branch, private fetched ref, worktree, ancestry and bounded path metadata.
func (c *RemoteClient) Compare(ctx context.Context, access application.GitAccess) (domain.Comparison, error) {
	root, err := c.resolveRoot(ctx, access)
	if err != nil {
		return domain.Comparison{}, err
	}
	state, err := c.readComparison(ctx, root, access.Branch)
	if err != nil {
		return domain.Comparison{}, err
	}
	if err := c.ensureComparisonStable(ctx, root, state); err != nil {
		return domain.Comparison{}, err
	}
	return state.comparison, nil
}

// FastForward advances only the configured branch with an old-value CAS. It never merges, resets or checks out.
func (c *RemoteClient) FastForward(ctx context.Context, command application.FastForwardCommand) (domain.MutationResult, error) {
	root, err := c.resolveRoot(ctx, command.Access)
	if err != nil {
		return domain.MutationResult{}, err
	}
	state, err := c.readComparison(ctx, root, command.Access.Branch)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if err := validateFastForward(command, state.comparison); err != nil {
		return domain.MutationResult{ResultKnown: true}, err
	}
	if err := c.ensureComparisonStable(ctx, root, state); err != nil {
		return domain.MutationResult{ResultKnown: true}, err
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{}, "update-ref", remoteBranchRef(command.Access.Branch), command.ExpectedRemoteOID, command.ExpectedHeadOID); err != nil {
		return domain.MutationResult{ResultKnown: true}, classifyFastForwardCASError(err)
	}
	// update-ref has committed the branch move. A subsequent worktree failure is intentionally unknown until Verify proves it.
	if _, err := c.git.runCommand(ctx, root, commandOptions{}, "read-tree", "-m", "-u", command.ExpectedHeadOID, command.ExpectedRemoteOID); err != nil {
		return domain.MutationResult{}, remoteError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, "Git worktree update could not be proven", nil)
	}
	return domain.MutationResult{ResultKnown: true}, nil
}

// Push publishes the configured branch with explicit non-force semantics. Verify must prove the remote postcondition.
func (c *RemoteClient) Push(ctx context.Context, command application.PushCommand) (domain.MutationResult, error) {
	root, err := c.resolveRoot(ctx, command.Access)
	if err != nil {
		return domain.MutationResult{}, err
	}
	state, err := c.readComparison(ctx, root, command.Access.Branch)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if err := validatePush(command, state.comparison); err != nil {
		return domain.MutationResult{ResultKnown: true}, err
	}
	if err := c.ensureComparisonStable(ctx, root, state); err != nil {
		return domain.MutationResult{ResultKnown: true}, err
	}
	// Both sides of the refspec are frozen run checkpoints. Do not use the
	// attached branch as the source: another writer could move it after the
	// comparison fence. The server's ordinary fast-forward rule is the final
	// concurrency fence; force and force-with-lease are intentionally forbidden.
	remoteRef := remoteBranchRef(command.Access.Branch)
	fence, err := newPushFenceSession(remoteRef, command.ExpectedRemoteOID)
	if err != nil {
		return domain.MutationResult{ResultKnown: true}, err
	}
	defer fence.Close()
	result, pushErr := c.runRemote(ctx, command.Access,
		"-c", "core.hooksPath="+fence.path, "push", "--porcelain", "--no-force",
		command.Access.RemoteURL, command.ExpectedHeadOID+":"+remoteRef)
	if pushErr != nil {
		classified := classifyPushError(pushErr, result.Stdout)
		if isKnownPushRejection(classified) {
			return domain.MutationResult{ResultKnown: true}, classified
		}
		// A disconnected or timed-out push can have reached the server; only Verify can settle it.
		return domain.MutationResult{}, classified
	}
	return domain.MutationResult{ResultKnown: true}, nil
}

// Verify refreshes the private ref and proves exact local/remote OIDs and clean worktree state.
func (c *RemoteClient) Verify(ctx context.Context, command application.VerifyCommand) (domain.Verification, error) {
	if err := c.Fetch(ctx, command.Access); err != nil {
		return domain.Verification{}, err
	}
	root, err := c.resolveRoot(ctx, command.Access)
	if err != nil {
		return domain.Verification{}, err
	}
	state, err := c.readComparison(ctx, root, command.Access.Branch)
	if err != nil {
		return domain.Verification{}, err
	}
	if !state.comparison.Attached || state.comparison.Branch != command.Access.Branch ||
		state.comparison.HeadOID != command.ExpectedHeadOID || state.comparison.RemoteOID != command.ExpectedRemoteOID {
		return domain.Verification{}, remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "Git refs drifted from the expected synchronization state", nil)
	}
	if err := c.ensureComparisonStable(ctx, root, state); err != nil {
		return domain.Verification{}, err
	}
	return domain.Verification{HeadOID: state.comparison.HeadOID, RemoteOID: state.comparison.RemoteOID, WorktreeClean: state.comparison.WorktreeClean}, nil
}

type remoteComparisonState struct {
	comparison domain.Comparison
	status     []byte
	remoteRef  string
}

func (c *RemoteClient) readComparison(ctx context.Context, root, expectedBranch string) (remoteComparisonState, error) {
	head, err := c.remoteOID(ctx, root, "HEAD")
	if err != nil {
		return remoteComparisonState{}, err
	}
	remote, err := c.remoteOID(ctx, root, privateRemoteRef(expectedBranch))
	if err != nil {
		return remoteComparisonState{}, err
	}
	branch, attached, err := c.remoteBranch(ctx, root)
	if err != nil {
		return remoteComparisonState{}, err
	}
	status, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: remoteStatusOutputLimit}, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return remoteComparisonState{}, classifyRemoteError(err, false)
	}
	ahead, behind, err := c.aheadBehind(ctx, root, head, remote)
	if err != nil {
		return remoteComparisonState{}, err
	}
	relation := remoteRelation(ahead, behind)
	changes, err := c.remoteChanges(ctx, root, head, remote)
	if err != nil {
		return remoteComparisonState{}, err
	}
	return remoteComparisonState{comparison: domain.Comparison{
		Attached: attached, Branch: branch, HeadOID: head, RemoteOID: remote, WorktreeClean: len(status.Stdout) == 0,
		Relation: relation, Ahead: ahead, Behind: behind, ChangedFiles: changes,
	}, status: status.Stdout, remoteRef: privateRemoteRef(expectedBranch)}, nil
}

func (c *RemoteClient) ensureComparisonStable(ctx context.Context, root string, state remoteComparisonState) error {
	head, err := c.remoteOID(ctx, root, "HEAD")
	if err != nil {
		return err
	}
	remote, err := c.remoteOID(ctx, root, state.remoteRef)
	if err != nil {
		return err
	}
	branch, attached, err := c.remoteBranch(ctx, root)
	if err != nil {
		return err
	}
	status, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: remoteStatusOutputLimit}, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return classifyRemoteError(err, false)
	}
	if head != state.comparison.HeadOID || remote != state.comparison.RemoteOID || attached != state.comparison.Attached || branch != state.comparison.Branch || !bytes.Equal(status.Stdout, state.status) {
		return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "Git state changed during synchronization comparison", nil)
	}
	return nil
}

func (c *RemoteClient) remoteOID(ctx context.Context, root, ref string) (string, error) {
	value, err := c.git.output(ctx, root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "required Git ref is absent", nil)
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if !validRemoteOID(value) {
		return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "Git ref returned an invalid object id", nil)
	}
	return value, nil
}

func (c *RemoteClient) remoteBranch(ctx context.Context, root string) (string, bool, error) {
	branch, err := c.git.output(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if isExitCode(err, 1) {
			return "", false, nil
		}
		return "", false, classifyRemoteError(err, false)
	}
	branch = strings.TrimSpace(branch)
	if !validRemoteBranch(branch) {
		return "", false, remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "Git branch is invalid", nil)
	}
	return branch, true, nil
}

func (c *RemoteClient) aheadBehind(ctx context.Context, root, head, remote string) (int, int, error) {
	value, err := c.git.output(ctx, root, "rev-list", "--left-right", "--count", head+"..."+remote)
	if err != nil {
		return 0, 0, classifyRemoteError(err, false)
	}
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, 0, remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "Git ancestry result is invalid", nil)
	}
	ahead, aheadErr := strconv.Atoi(fields[0])
	behind, behindErr := strconv.Atoi(fields[1])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return 0, 0, remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "Git ancestry counts are invalid", nil)
	}
	return ahead, behind, nil
}

func (c *RemoteClient) remoteChanges(ctx context.Context, root, head, remote string) ([]domain.FileChange, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: remoteDiffOutputLimit}, "diff", "--name-status", "-z", "-M", "--no-ext-diff", "--no-textconv", head, remote)
	if err != nil {
		return nil, classifyRemoteError(err, false)
	}
	changes, err := parseRemoteChanges(result.Stdout)
	if err != nil {
		return nil, remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "Git changed-file metadata is invalid", nil)
	}
	return changes, nil
}

func parseRemoteChanges(raw []byte) ([]domain.FileChange, error) {
	changes := make([]domain.FileChange, 0, min(len(raw), domain.MaxChangedFiles))
	for len(raw) > 0 {
		status, remaining, ok := popRemoteChangeField(raw)
		if !ok || len(status) == 0 {
			return nil, errors.New("truncated status record")
		}
		pathBytes, remaining, ok := popRemoteChangeField(remaining)
		if !ok {
			return nil, errors.New("truncated status record")
		}
		raw = remaining
		path := string(pathBytes)
		change := domain.FileChange{Path: path}
		switch string(status) {
		case "A":
			change.Kind = domain.FileAdded
		case "M", "T":
			change.Kind = domain.FileModified
		case "D":
			change.Kind = domain.FileDeleted
		default:
			if !validRemoteRenameStatus(status) {
				return nil, errors.New("unsupported status")
			}
			newPath, remaining, ok := popRemoteChangeField(raw)
			if !ok {
				return nil, errors.New("truncated rename record")
			}
			change.Kind, change.OldPath, change.Path = domain.FileRenamed, path, string(newPath)
			raw = remaining
		}
		if err := change.Validate(); err != nil {
			return nil, err
		}
		// Run metadata is a bounded preview. Continue parsing the tail so an
		// unsafe path or unsupported status cannot hide beyond the persisted limit.
		if len(changes) < domain.MaxChangedFiles {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func popRemoteChangeField(raw []byte) ([]byte, []byte, bool) {
	if len(raw) == 0 {
		return nil, nil, false
	}
	if index := bytes.IndexByte(raw, 0); index >= 0 {
		return raw[:index], raw[index+1:], true
	}
	return nil, raw, false
}

func validRemoteRenameStatus(status []byte) bool {
	if len(status) < 2 || status[0] != 'R' {
		return false
	}
	for _, digit := range status[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	score, err := strconv.Atoi(string(status[1:]))
	return err == nil && score >= 0 && score <= 100
}

func (c *RemoteClient) resolveRoot(ctx context.Context, access application.GitAccess) (string, error) {
	if c == nil || c.workspaces == nil || !validRemoteAccess(access) {
		return "", remoteError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, "Git remote access is invalid", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", classifyRemoteError(err, false)
	}
	workspace, err := c.workspaces.GetWorkspaceByID(ctx, access.WorkspaceID)
	if err != nil {
		return "", remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace lookup failed", nil)
	}
	if workspace.ID != access.WorkspaceID {
		return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "workspace binding is invalid", nil)
	}
	root := strings.TrimSpace(workspace.RootPath)
	if root == "" {
		return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, "workspace root is invalid", nil)
	}
	root, err = filepath.Abs(root)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return "", remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace root is unavailable", nil)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace root is unavailable", nil)
	}
	root = filepath.Clean(root)
	top, err := c.git.output(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, false, "workspace Git repository is unavailable", nil)
	}
	top, err = filepath.Abs(top)
	if err == nil {
		top, err = filepath.EvalSymlinks(top)
	}
	if err != nil || filepath.Clean(top) != root {
		return "", remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeUnavailable, false, "workspace root is not the Git repository root", nil)
	}
	return root, nil
}

func (c *RemoteClient) runRemote(ctx context.Context, access application.GitAccess, args ...string) (result commandResult, resultErr error) {
	root, err := c.resolveRoot(ctx, access)
	if err != nil {
		return commandResult{ExitCode: -1}, err
	}
	configGuard, err := c.lockLocalConfig(ctx, root)
	if err != nil {
		return commandResult{ExitCode: -1}, err
	}
	defer func() {
		if closeErr := configGuard.Close(); closeErr != nil && resultErr == nil {
			resultErr = closeErr
		}
	}()
	if err := c.validateLocalTransportConfig(ctx, root); err != nil {
		return commandResult{ExitCode: -1}, err
	}
	resolveConfig := ""
	if !c.allowLocal {
		endpoint, resolveErr := c.policy.ResolveHTTPSRemote(ctx, access.RemoteURL)
		if resolveErr != nil {
			return commandResult{ExitCode: -1}, resolveErr
		}
		resolveConfig, resolveErr = remoteResolveConfig(access.RemoteURL, endpoint)
		if resolveErr != nil {
			return commandResult{ExitCode: -1}, resolveErr
		}
	}
	session, err := newAskPassSession(access.Token)
	if err != nil {
		return commandResult{ExitCode: -1}, err
	}
	defer session.Close()
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := newBoundedBuffer(defaultCommandOutputLimit, cancel)
	stderr := newBoundedBuffer(defaultCommandOutputLimit, cancel)
	commandArgs := []string{
		"--no-pager", "--literal-pathspecs",
		"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.quotePath=true",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "color.ui=false", "-c", "credential.helper=",
		"-c", "http.followRedirects=false",
	}
	if resolveConfig != "" {
		commandArgs = append(commandArgs, "-c", "http.curloptResolve="+resolveConfig)
	}
	commandArgs = append(commandArgs, "-C", root)
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(runContext, c.git.executable, commandArgs...)
	configureRemoteCommandCancellation(command)
	command.Env = session.environment()
	command.Stdout, command.Stderr = stdout, stderr
	runErr := command.Run()
	result = commandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: commandExitCode(runErr)}
	if runErr == nil && !stdout.Exceeded() && !stderr.Exceeded() {
		return result, nil
	}
	if stdout.Exceeded() || stderr.Exceeded() {
		return result, errCommandOutputLimit
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	// Do not retain Git diagnostics: a hostile server must not turn them into a credential logging channel.
	return result, &remoteCommandError{exitCode: result.ExitCode, failure: classifyRemoteFailure(runErr, result.Stderr, result.Stdout)}
}

type remoteConfigGuard struct {
	path     string
	identity os.FileInfo
}

func (c *RemoteClient) lockLocalConfig(ctx context.Context, root string) (*remoteConfigGuard, error) {
	commonDirectory, err := c.git.output(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace Git config location is unavailable", nil)
	}
	commonDirectory, err = filepath.Abs(commonDirectory)
	if err == nil {
		commonDirectory, err = filepath.EvalSymlinks(commonDirectory)
	}
	if err != nil || !validRemoteCommonDirectory(root, commonDirectory) {
		return nil, remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeUnavailable, false, "workspace Git config location is unsafe", nil)
	}
	configPath := filepath.Join(commonDirectory, "config")
	configInfo, err := os.Lstat(configPath)
	if err != nil || !configInfo.Mode().IsRegular() || configInfo.Mode()&os.ModeSymlink != 0 {
		return nil, remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeUnavailable, false, "workspace Git config file is unsafe", nil)
	}
	lockPath := configPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, remoteError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, "workspace Git config is being changed", nil)
		}
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace Git config lock is unavailable", nil)
	}
	identity, statErr := lockFile.Stat()
	closeErr := lockFile.Close()
	if statErr != nil || closeErr != nil {
		_ = os.Remove(lockPath)
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace Git config lock could not be secured", nil)
	}
	return &remoteConfigGuard{path: lockPath, identity: identity}, nil
}

func validRemoteCommonDirectory(root, commonDirectory string) bool {
	commonInfo, err := os.Stat(commonDirectory)
	if err != nil || !commonInfo.IsDir() {
		return false
	}
	if pathWithinRemoteRoot(root, commonDirectory) {
		return true
	}

	markerPath := filepath.Join(root, ".git")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Size() > 4096 {
		return false
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	value := strings.TrimSpace(string(marker))
	const prefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return false
	}
	gitDirectory := strings.TrimSpace(value[len(prefix):])
	if gitDirectory == "" {
		return false
	}
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(root, gitDirectory)
	}
	gitDirectory, err = filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		return false
	}
	gitInfo, err := os.Stat(gitDirectory)
	if err != nil || !gitInfo.IsDir() {
		return false
	}

	commonMarkerPath := filepath.Join(gitDirectory, "commondir")
	commonMarkerInfo, err := os.Lstat(commonMarkerPath)
	if err != nil || !commonMarkerInfo.Mode().IsRegular() || commonMarkerInfo.Size() > 4096 {
		return false
	}
	commonMarker, err := os.ReadFile(commonMarkerPath)
	if err != nil {
		return false
	}
	reportedCommon := strings.TrimSpace(string(commonMarker))
	if reportedCommon == "" {
		return false
	}
	if !filepath.IsAbs(reportedCommon) {
		reportedCommon = filepath.Join(gitDirectory, reportedCommon)
	}
	reportedCommon, err = filepath.EvalSymlinks(reportedCommon)
	if err != nil || filepath.Clean(reportedCommon) != filepath.Clean(commonDirectory) {
		return false
	}
	worktreesDirectory := filepath.Join(commonDirectory, "worktrees")
	return filepath.Clean(gitDirectory) != filepath.Clean(worktreesDirectory) && pathWithinRemoteRoot(worktreesDirectory, gitDirectory)
}

func (guard *remoteConfigGuard) Close() error {
	if guard == nil || guard.path == "" || guard.identity == nil {
		return remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace Git config lock is invalid", nil)
	}
	current, err := os.Lstat(guard.path)
	if err != nil || !os.SameFile(current, guard.identity) {
		return remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeUnavailable, false, "workspace Git config lock identity changed", nil)
	}
	if err := os.Remove(guard.path); err != nil {
		return remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "workspace Git config lock could not be released", nil)
	}
	guard.path = ""
	guard.identity = nil
	return nil
}

func pathWithinRemoteRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// validateLocalTransportConfig rejects repository-owned settings that could
// rewrite a validated URL, replace the credential session, or override the
// command's HTTPS transport policy. Ordinary repository metadata and named
// remotes remain valid because every network command receives an explicit URL.
func (c *RemoteClient) validateLocalTransportConfig(ctx context.Context, root string) error {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: remoteConfigOutputLimit},
		"config", "--local", "--no-includes", "--null", "--name-only", "--list")
	if err != nil {
		return remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeURLInvalid, false, "workspace Git transport configuration could not be validated", nil)
	}
	if unsafeRemoteLocalConfig(result.Stdout) {
		return remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeURLInvalid, false, "workspace Git transport configuration is unsafe", nil)
	}
	return nil
}

func unsafeRemoteLocalConfig(raw []byte) bool {
	for _, entry := range bytes.Split(raw, []byte{0}) {
		key := strings.ToLower(string(entry))
		if key == "" {
			continue
		}
		if key == "include.path" ||
			(strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path")) ||
			key == "extensions.worktreeconfig" ||
			key == "core.askpass" || key == "core.gitproxy" ||
			strings.HasPrefix(key, "http.") ||
			strings.HasPrefix(key, "credential.") ||
			strings.HasPrefix(key, "protocol.") ||
			strings.HasPrefix(key, "url.") {
			return true
		}
	}
	return false
}

type askPassSession struct {
	path  string
	token []byte
}

type pushFenceSession struct {
	path string
}

func newPushFenceSession(remoteRef, expectedOID string) (*pushFenceSession, error) {
	if !strings.HasPrefix(remoteRef, "refs/heads/") || !validRemoteOID(expectedOID) {
		return nil, remoteError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, "Git push fence is invalid", nil)
	}
	directory, err := os.MkdirTemp("", "zhixu-git-push-fence-")
	if err != nil {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot create Git push fence", nil)
	}
	cleanup := func() {
		_ = os.RemoveAll(directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot secure Git push fence", nil)
	}
	for name, value := range map[string]string{"expected-ref": remoteRef + "\n", "expected-oid": expectedOID + "\n"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			cleanup()
			return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot write Git push fence", nil)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			cleanup()
			return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot secure Git push fence", nil)
		}
	}
	hookPath := filepath.Join(directory, "pre-push")
	hook := `#!/bin/sh
hook_dir=${0%/*}
IFS= read -r expected_ref < "$hook_dir/expected-ref" || exit 1
IFS= read -r expected_oid < "$hook_dir/expected-oid" || exit 1
matched=0
while IFS=' ' read -r local_ref local_oid remote_ref remote_oid
do
  if test "$remote_ref" = "$expected_ref"
  then
    matched=1
    if test "$remote_oid" != "$expected_oid"
    then
      printf '%s\n' ZHIXU_GIT_EXPECTED_REMOTE_REF_DRIFT >&2
      exit 1
    fi
  fi
done
if test "$matched" != 1
then
  printf '%s\n' ZHIXU_GIT_EXPECTED_REMOTE_REF_DRIFT >&2
  exit 1
fi
`
	if err := os.WriteFile(hookPath, []byte(hook), 0o700); err != nil {
		cleanup()
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot write Git push fence", nil)
	}
	if err := os.Chmod(hookPath, 0o700); err != nil {
		cleanup()
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot secure Git push fence", nil)
	}
	return &pushFenceSession{path: directory}, nil
}

func (session *pushFenceSession) Close() {
	if session == nil || session.path == "" {
		return
	}
	_ = os.RemoveAll(session.path)
	session.path = ""
}

func newAskPassSession(token domain.Token) (*askPassSession, error) {
	if !token.Configured() {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeAuthenticationFailed, false, "Git remote token is unavailable", nil)
	}
	helper, err := os.CreateTemp("", "zhixu-git-askpass-")
	if err != nil {
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot create Git credential helper", nil)
	}
	path := helper.Name()
	if _, err := io.WriteString(helper, "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' x-access-token ;;\n*Password*) printf '%s\\n' \"$ZHIXU_GIT_ASKPASS_TOKEN\" ;;\n*) exit 1 ;;\nesac\n"); err != nil {
		_ = helper.Close()
		_ = os.Remove(path)
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot write Git credential helper", nil)
	}
	if err := helper.Chmod(0o700); err != nil {
		_ = helper.Close()
		_ = os.Remove(path)
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot secure Git credential helper", nil)
	}
	if err := helper.Close(); err != nil {
		_ = os.Remove(path)
		return nil, remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "cannot close Git credential helper", nil)
	}
	return &askPassSession{path: path, token: token.Bytes()}, nil
}

func (s *askPassSession) environment() []string {
	base := commandEnvironment(false, "")
	environment := make([]string, 0, len(base)+5)
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found && isProxyEnvironment(name) {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment,
		"GIT_ASKPASS="+s.path,
		"GIT_ASKPASS_REQUIRE=force",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"ZHIXU_GIT_ASKPASS_TOKEN="+string(s.token),
	)
	return environment
}

func isProxyEnvironment(name string) bool {
	switch strings.ToLower(name) {
	case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
		return true
	default:
		return false
	}
}

func remoteResolveConfig(raw string, endpoint remoteurl.Endpoint) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || endpoint.URL != raw || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() != endpoint.Hostname ||
		endpoint.Port == 0 || len(endpoint.Addresses) == 0 || len(endpoint.Addresses) > 16 {
		return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeURLInvalid, false, "Git remote endpoint policy returned an invalid binding", nil)
	}
	expectedPort := uint16(443)
	if parsed.Port() != "" {
		port, parseErr := strconv.ParseUint(parsed.Port(), 10, 16)
		if parseErr != nil {
			return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeURLInvalid, false, "Git remote endpoint policy returned an invalid port", nil)
		}
		expectedPort = uint16(port)
	}
	if endpoint.Port != expectedPort {
		return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeURLInvalid, false, "Git remote endpoint policy returned a mismatched port", nil)
	}
	addresses := make([]string, 0, len(endpoint.Addresses))
	seen := make(map[netip.Addr]struct{}, len(endpoint.Addresses))
	for _, address := range endpoint.Addresses {
		address = address.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
			address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
			return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeURLInvalid, false, "Git remote endpoint policy returned a non-public address", nil)
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		value := address.String()
		if address.Is6() {
			value = "[" + value + "]"
		}
		addresses = append(addresses, value)
	}
	if len(addresses) == 0 {
		return "", remoteError(foundation.ErrorConsistencyViolation, domain.ErrorCodeURLInvalid, false, "Git remote endpoint policy returned no addresses", nil)
	}
	return endpoint.Hostname + ":" + strconv.Itoa(int(endpoint.Port)) + ":" + strings.Join(addresses, ","), nil
}

func (s *askPassSession) Close() {
	if s == nil {
		return
	}
	for index := range s.token {
		s.token[index] = 0
	}
	_ = os.Remove(s.path)
	s.path, s.token = "", nil
}

type remoteFailure string

const (
	remoteFailureGeneric  remoteFailure = "generic"
	remoteFailureAuth     remoteFailure = "authentication"
	remoteFailureOffline  remoteFailure = "offline"
	remoteFailureNonFF    remoteFailure = "non-fast-forward"
	remoteFailureRefDrift remoteFailure = "ref-drift"
	remoteFailureMissing  remoteFailure = "missing"
)

type remoteCommandError struct {
	exitCode int
	failure  remoteFailure
}

func (e *remoteCommandError) Error() string { return "Git remote command failed" }

func classifyRemoteFailure(runErr error, stderr, stdout []byte) remoteFailure {
	var executableError *exec.Error
	if errors.As(runErr, &executableError) || errors.Is(runErr, os.ErrNotExist) {
		return remoteFailureMissing
	}
	message := strings.ToLower(string(stderr) + "\n" + string(stdout))
	switch {
	case strings.Contains(message, "zhixu_git_expected_remote_ref_drift"):
		return remoteFailureRefDrift
	case strings.Contains(message, "authentication failed"), strings.Contains(message, "authentication required"),
		strings.Contains(message, "http basic: access denied"), strings.Contains(message, "could not read username"),
		strings.Contains(message, "could not read password"), strings.Contains(message, "terminal prompts disabled"),
		strings.Contains(message, "http 401"), strings.Contains(message, "http 403"):
		return remoteFailureAuth
	case strings.Contains(message, "[rejected]"), strings.Contains(message, "non-fast-forward"), strings.Contains(message, "fetch first"),
		strings.Contains(message, "stale info"), strings.Contains(message, "remote ref updated since checkout"):
		return remoteFailureNonFF
	case strings.Contains(message, "could not resolve host"), strings.Contains(message, "failed to connect"),
		strings.Contains(message, "connection refused"), strings.Contains(message, "connection timed out"),
		strings.Contains(message, "network is unreachable"), strings.Contains(message, "couldn't connect"):
		return remoteFailureOffline
	default:
		return remoteFailureGeneric
	}
}

func classifyPushError(err error, stdout []byte) error {
	output := strings.ToLower(string(stdout))
	if strings.Contains(output, "[rejected]") || strings.Contains(output, "stale info") {
		return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeNonFastForward, false, "Git remote rejected a non-fast-forward update", nil)
	}
	return classifyRemoteError(err, true)
}

func isKnownPushRejection(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && (classified.Code == domain.ErrorCodeNonFastForward || classified.Code == domain.ErrorCodeRefDrift)
}

func classifyFastForwardCASError(err error) error {
	var commandError *commandError
	if errors.As(err, &commandError) {
		message := strings.ToLower(commandError.Stderr())
		if strings.Contains(message, "cannot lock ref") || strings.Contains(message, "is at") {
			return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "Git branch changed before the fast-forward CAS", nil)
		}
	}
	return classifyRemoteError(err, false)
}

func remoteRelation(ahead, behind int) domain.Relation {
	switch {
	case ahead == 0 && behind == 0:
		return domain.RelationSame
	case ahead == 0:
		return domain.RelationRemoteAhead
	case behind == 0:
		return domain.RelationLocalAhead
	default:
		return domain.RelationDiverged
	}
}

func remoteBranchRef(branch string) string  { return "refs/heads/" + branch }
func privateRemoteRef(branch string) string { return remoteRefPrefix + branch }

func validateFastForward(command application.FastForwardCommand, comparison domain.Comparison) error {
	if !validRemoteOID(command.ExpectedHeadOID) || !validRemoteOID(command.ExpectedRemoteOID) ||
		comparison.HeadOID != command.ExpectedHeadOID || comparison.RemoteOID != command.ExpectedRemoteOID ||
		!comparison.Attached || comparison.Branch != command.Access.Branch || !comparison.WorktreeClean || comparison.Relation != domain.RelationRemoteAhead {
		return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "Git fast-forward preconditions drifted", nil)
	}
	return nil
}

func validatePush(command application.PushCommand, comparison domain.Comparison) error {
	if !validRemoteOID(command.ExpectedHeadOID) || !validRemoteOID(command.ExpectedRemoteOID) ||
		comparison.HeadOID != command.ExpectedHeadOID || comparison.RemoteOID != command.ExpectedRemoteOID ||
		!comparison.Attached || comparison.Branch != command.Access.Branch || !comparison.WorktreeClean || comparison.Relation != domain.RelationLocalAhead {
		return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "Git push preconditions drifted", nil)
	}
	return nil
}

func validRemoteAccess(access application.GitAccess) bool {
	if !validWritebackWorkspaceID(access.WorkspaceID) || access.ConfigRevision < 1 || !access.Token.Configured() || !validRemoteBranch(access.Branch) {
		return false
	}
	parsed, err := url.Parse(access.RemoteURL)
	return err == nil && access.RemoteURL == strings.TrimSpace(access.RemoteURL) && access.RemoteURL != "" && parsed.User == nil && !strings.ContainsRune(access.RemoteURL, '\x00')
}

func validRemoteBranch(branch string) bool {
	if branch == "" || len(branch) > 255 || strings.TrimSpace(branch) != branch || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\") || strings.ContainsFunc(branch, unicode.IsControl) {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validRemoteOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func remoteOIDFromLSRemote(output []byte, ref string) string {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref && validRemoteOID(strings.ToLower(fields[0])) {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

func classifyRemoteError(err error, mutation bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return remoteError(foundation.ErrorNonRetryableFailure, domain.ErrorCodeUnavailable, false, "Git remote operation was cancelled", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return remoteError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, "Git remote operation timed out", context.DeadlineExceeded)
	}
	var sessionError *foundation.Error
	if errors.As(err, &sessionError) {
		return err
	}
	var commandError *remoteCommandError
	if errors.As(err, &commandError) {
		switch commandError.failure {
		case remoteFailureAuth:
			return remoteError(foundation.ErrorPermissionDenied, domain.ErrorCodeAuthenticationFailed, false, "Git remote authentication failed", nil)
		case remoteFailureOffline:
			return remoteError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, "Git remote is unreachable", nil)
		case remoteFailureNonFF:
			return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeNonFastForward, false, "Git remote rejected a non-fast-forward update", nil)
		case remoteFailureRefDrift:
			return remoteError(foundation.ErrorVersionConflict, domain.ErrorCodeRefDrift, false, "Git remote changed after the sync checkpoint", nil)
		case remoteFailureMissing:
			return remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, false, "Git executable is unavailable", nil)
		case remoteFailureGeneric:
			if mutation {
				return remoteError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, "Git remote mutation response is not provable", nil)
			}
		}
		return remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "Git remote command failed", nil)
	}
	if errors.Is(err, errCommandOutputLimit) {
		return remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, false, "Git remote response exceeds the limit", nil)
	}
	return remoteError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, "Git remote operation failed", nil)
}

func remoteError(kind foundation.ErrorKind, code string, retryable bool, message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(kind, code, retryable, cause)
}

var _ application.RemoteRepository = (*RemoteClient)(nil)
