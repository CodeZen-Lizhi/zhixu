package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	documenthistory "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const historyStatusOutputLimit = 16 << 20

// HistoryClient exposes only bounded, read-only Git operations for Document History.
type HistoryClient struct {
	git        Client
	workspaces WorkspaceRepository
}

// NewHistoryClient binds history reads to server-owned Workspace roots.
func NewHistoryClient(git Client, workspaces WorkspaceRepository) (*HistoryClient, error) {
	if workspaces == nil {
		return nil, historyError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", errors.New("workspace repository is required"))
	}
	if strings.TrimSpace(git.executable) == "" {
		git = New("")
	}
	return &HistoryClient{git: git, workspaces: workspaces}, nil
}

// Inspect returns the attached current-branch baseline and dirty state for one canonical path.
func (c *HistoryClient) Inspect(ctx context.Context, workspaceID foundation.ID, targetPath string) (documenthistory.RepositoryState, error) {
	if unsafeHistoryPath(targetPath) {
		return documenthistory.RepositoryState{}, historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("canonical path is invalid"))
	}
	root, err := c.resolveRoot(ctx, workspaceID)
	if err != nil {
		return documenthistory.RepositoryState{}, err
	}
	return c.inspectResolved(ctx, workspaceID, targetPath, root)
}

func (c *HistoryClient) inspectResolved(ctx context.Context, workspaceID foundation.ID, targetPath, root string) (documenthistory.RepositoryState, error) {
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return documenthistory.RepositoryState{}, err
	}
	if unsafeHistoryPath(targetPath) {
		return documenthistory.RepositoryState{}, historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("canonical path is invalid"))
	}
	if err := c.ensureHistoryContentSafe(ctx, root, targetPath); err != nil {
		return documenthistory.RepositoryState{}, err
	}
	objectFormat, err := c.git.output(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return documenthistory.RepositoryState{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return documenthistory.RepositoryState{}, historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("unsupported git object format"))
	}
	head, err := c.git.output(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return documenthistory.RepositoryState{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	head = strings.ToLower(strings.TrimSpace(head))
	if !documenthistory.ValidObjectID(head) || len(head) != objectIDLength(objectFormat) {
		return documenthistory.RepositoryState{}, historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("git head is invalid"))
	}
	branch, err := c.git.output(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if isExitCode(err, 1) {
			return documenthistory.RepositoryState{}, historyError(foundation.ErrorVersionConflict, "DOCUMENT_HISTORY_DETACHED_HEAD", errors.New("document history requires an attached branch"))
		}
		return documenthistory.RepositoryState{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || len(branch) > 512 || strings.ContainsFunc(branch, unicode.IsControl) {
		return documenthistory.RepositoryState{}, historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("git branch is invalid"))
	}
	status, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: historyStatusOutputLimit}, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return documenthistory.RepositoryState{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	pathStatus, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: historyStatusOutputLimit}, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--", targetPath)
	if err != nil {
		return documenthistory.RepositoryState{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	return documenthistory.RepositoryState{
		WorkspaceID:  workspaceID,
		Branch:       branch,
		Head:         head,
		ObjectFormat: objectFormat,
		Dirty:        len(status.Stdout) != 0,
		PathDirty:    len(pathStatus.Stdout) != 0,
	}, nil
}

// List returns a bounded first-parent-independent path history on the current branch.
func (c *HistoryClient) List(ctx context.Context, workspaceID foundation.ID, targetPath string, offset, limit int) (documenthistory.CommitPage, error) {
	if unsafeHistoryPath(targetPath) || offset < 0 || offset > documenthistory.MaxHistoryOffset || limit < 1 || limit > documenthistory.MaxHistoryLimit || offset+limit > documenthistory.MaxHistoryOffset+documenthistory.MaxHistoryLimit {
		return documenthistory.CommitPage{}, historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("history range is invalid"))
	}
	root, err := c.resolveRoot(ctx, workspaceID)
	if err != nil {
		return documenthistory.CommitPage{}, err
	}
	state, err := c.inspectResolved(ctx, workspaceID, targetPath, root)
	if err != nil {
		return documenthistory.CommitPage{}, err
	}
	format := "%H%x00%P%x00%an%x00%ae%x00%aI%x00%s%x00%x00%x00"
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: historyStatusOutputLimit},
		"log", "--topo-order", "--no-show-signature", "--max-count="+strconv.Itoa(limit+1), "--skip="+strconv.Itoa(offset), "--format="+format, state.Head, "--", targetPath)
	if err != nil {
		return documenthistory.CommitPage{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	commits, err := parseHistoryLog(result.Stdout, state.ObjectFormat)
	if err != nil {
		return documenthistory.CommitPage{}, historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", err)
	}
	hasMore := len(commits) > limit
	if hasMore {
		commits = commits[:limit]
	}
	if err := c.ensureStableRootAndState(ctx, workspaceID, targetPath, root, state); err != nil {
		return documenthistory.CommitPage{}, err
	}
	return documenthistory.CommitPage{State: state, Commits: commits, HasMore: hasMore}, nil
}

// Compare returns complete bounded source texts and their stable unified patch.
func (c *HistoryClient) Compare(ctx context.Context, workspaceID foundation.ID, targetPath, left, right string) (documenthistory.Diff, error) {
	if unsafeHistoryPath(targetPath) || !documenthistory.ValidVersionRef(left) || !documenthistory.ValidVersionRef(right) || left == right {
		return documenthistory.Diff{}, historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("compare range is invalid"))
	}
	root, err := c.resolveRoot(ctx, workspaceID)
	if err != nil {
		return documenthistory.Diff{}, err
	}
	state, err := c.inspectResolved(ctx, workspaceID, targetPath, root)
	if err != nil {
		return documenthistory.Diff{}, err
	}
	leftContent, err := c.readVersion(ctx, root, state, targetPath, left)
	if err != nil {
		return documenthistory.Diff{}, err
	}
	rightContent, err := c.readVersion(ctx, root, state, targetPath, right)
	if err != nil {
		return documenthistory.Diff{}, err
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--full-index", "--no-renames", "--diff-algorithm=myers", "--no-indent-heuristic", "--inter-hunk-context=0", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3"}
	switch {
	case left == documenthistory.WorktreeRef:
		args = append(args, "-R", right)
	case right == documenthistory.WorktreeRef:
		args = append(args, left)
	default:
		args = append(args, left, right)
	}
	args = append(args, "--", targetPath)
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: documenthistory.MaxDiffBytes + 1}, args...)
	if err != nil {
		return documenthistory.Diff{}, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if len(result.Stdout) > documenthistory.MaxDiffBytes {
		return documenthistory.Diff{}, historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_OUTPUT_TOO_LARGE", errors.New("diff exceeds limit"))
	}
	finalLeft, err := c.readVersion(ctx, root, state, targetPath, left)
	if err != nil {
		return documenthistory.Diff{}, err
	}
	finalRight, err := c.readVersion(ctx, root, state, targetPath, right)
	if err != nil {
		return documenthistory.Diff{}, err
	}
	if err := c.ensureStableRootAndState(ctx, workspaceID, targetPath, root, state); err != nil {
		return documenthistory.Diff{}, err
	}
	if !bytes.Equal(finalLeft, leftContent) || !bytes.Equal(finalRight, rightContent) {
		return documenthistory.Diff{}, historyError(foundation.ErrorVersionConflict, "DOCUMENT_HISTORY_CURSOR_STALE", errors.New("compare baseline changed"))
	}
	patch := string(result.Stdout)
	return documenthistory.Diff{
		WorkspaceID:  workspaceID,
		Path:         targetPath,
		Head:         state.Head,
		Left:         left,
		Right:        right,
		LeftContent:  string(leftContent),
		RightContent: string(rightContent),
		Patch:        patch,
		DiffHash:     documenthistory.ComputeHash(result.Stdout),
	}, nil
}

// ReadBlob reads one exact blob from a verified ancestor of current HEAD.
func (c *HistoryClient) ReadBlob(ctx context.Context, workspaceID foundation.ID, commit, targetPath string) (documenthistory.Blob, error) {
	if unsafeHistoryPath(targetPath) || !documenthistory.ValidObjectID(commit) {
		return documenthistory.Blob{}, historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("blob request is invalid"))
	}
	root, err := c.resolveRoot(ctx, workspaceID)
	if err != nil {
		return documenthistory.Blob{}, err
	}
	state, err := c.inspectResolved(ctx, workspaceID, targetPath, root)
	if err != nil {
		return documenthistory.Blob{}, err
	}
	if err := c.ensureReadableCommit(ctx, root, state, commit); err != nil {
		return documenthistory.Blob{}, err
	}
	if commit != state.Head {
		if err := c.ensureCommitChangesPath(ctx, root, commit, targetPath); err != nil {
			return documenthistory.Blob{}, err
		}
	}
	content, err := c.readCommitBlob(ctx, root, commit, targetPath)
	if err != nil {
		return documenthistory.Blob{}, err
	}
	if err := c.ensureStableRootAndState(ctx, workspaceID, targetPath, root, state); err != nil {
		return documenthistory.Blob{}, err
	}
	return documenthistory.Blob{Commit: commit, Path: targetPath, Content: content, ContentHash: documenthistory.ComputeHash(content)}, nil
}

func (c *HistoryClient) readVersion(ctx context.Context, root string, state documenthistory.RepositoryState, targetPath, ref string) ([]byte, error) {
	if ref == documenthistory.WorktreeRef {
		content, err := readSafeWorkspaceFile(root, targetPath)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return nil, historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("document worktree content is not valid UTF-8 text"))
		}
		return content, nil
	}
	if err := c.ensureReadableCommit(ctx, root, state, ref); err != nil {
		return nil, err
	}
	if ref != state.Head {
		if err := c.ensureCommitChangesPath(ctx, root, ref, targetPath); err != nil {
			return nil, err
		}
	}
	return c.readCommitBlob(ctx, root, ref, targetPath)
}

func (c *HistoryClient) readCommitBlob(ctx context.Context, root, commit, targetPath string) ([]byte, error) {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: documenthistory.MaxBlobBytes + 1}, "cat-file", "blob", commit+":"+targetPath)
	if err != nil {
		if isMissingObject(err) {
			return nil, historyError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", errors.New("document path is absent at commit"))
		}
		return nil, classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if len(result.Stdout) > documenthistory.MaxBlobBytes {
		return nil, historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_OUTPUT_TOO_LARGE", errors.New("blob exceeds limit"))
	}
	if !utf8.Valid(result.Stdout) || bytes.IndexByte(result.Stdout, 0) >= 0 {
		return nil, historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("document blob is not valid UTF-8 text"))
	}
	return result.Stdout, nil
}

func (c *HistoryClient) ensureCommitChangesPath(ctx context.Context, root, commit, targetPath string) error {
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: 256}, "log", "-1", "--format=%H", commit, "--", targetPath)
	if err != nil {
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if strings.ToLower(strings.TrimSpace(string(result.Stdout))) != commit {
		return historyError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", errors.New("commit is outside canonical path history"))
	}
	return nil
}

func (c *HistoryClient) ensureReadableCommit(ctx context.Context, root string, state documenthistory.RepositoryState, commit string) error {
	if !documenthistory.ValidObjectID(commit) || len(commit) != objectIDLength(state.ObjectFormat) {
		return historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("commit id is invalid for repository"))
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "cat-file", "-e", commit+"^{commit}"); err != nil {
		if isMissingObject(err) {
			return historyError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", errors.New("commit does not exist"))
		}
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if _, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "merge-base", "--is-ancestor", commit, state.Head); err != nil {
		if isExitCode(err, 1) {
			return historyError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", errors.New("commit is outside current branch history"))
		}
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	return nil
}

func (c *HistoryClient) resolveRoot(ctx context.Context, workspaceID foundation.ID) (string, error) {
	if !documenthistory.ValidID(workspaceID) {
		return "", historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("workspace id is invalid"))
	}
	if ctx == nil {
		return "", historyError(foundation.ErrorInvalidInput, "DOCUMENT_HISTORY_INVALID", errors.New("context is nil"))
	}
	workspace, err := c.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if workspace.ID != workspaceID || workspace.Status != workspacedomain.WorkspaceStatusActive || workspace.Availability != workspacedomain.WorkspaceAvailabilityAvailable {
		return "", historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", errors.New("workspace root is not available"))
	}
	root, err := filepath.Abs(strings.TrimSpace(workspace.RootPath))
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return "", historyError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", historyError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", errors.Join(err, errors.New("workspace root is not a directory")))
	}
	return filepath.Clean(root), nil
}

func (c *HistoryClient) ensureRepositoryTopLevel(ctx context.Context, root string) error {
	topLevel, err := c.git.output(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		if isMissingRepository(err) {
			return historyError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", errors.New("git repository is missing"))
		}
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	canonical, err := filepath.Abs(topLevel)
	if err == nil {
		canonical, err = filepath.EvalSymlinks(canonical)
	}
	if err != nil {
		return historyError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if filepath.Clean(canonical) != filepath.Clean(root) {
		return historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", errors.New("workspace root is not repository top-level"))
	}
	graftsPath, err := c.git.output(ctx, root, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if !filepath.IsAbs(graftsPath) {
		graftsPath = filepath.Join(root, graftsPath)
	}
	if _, err := os.Lstat(graftsPath); err == nil {
		return historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_GIT_UNSAFE", errors.New("git object overrides are not allowed"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return historyError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	return nil
}

func (c *HistoryClient) ensureHistoryContentSafe(ctx context.Context, root, targetPath string) error {
	unsafeFilter, err := c.git.hasTrackedContentFilter(ctx, root)
	if err != nil {
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	if unsafeFilter {
		return historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_GIT_UNSAFE", errors.New("tracked content filters are not allowed"))
	}
	attributes := []string{"filter", "ident", "text", "eol", "working-tree-encoding", "diff"}
	args := append([]string{"check-attr", "-z"}, attributes...)
	args = append(args, "--", targetPath)
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: 4096}, args...)
	if err != nil {
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	parts := splitNUL(result.Stdout)
	if len(parts) != len(attributes)*3 {
		return historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("git attribute result is invalid"))
	}
	for index, attribute := range attributes {
		offset := index * 3
		if string(parts[offset]) != targetPath || string(parts[offset+1]) != attribute {
			return historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("git attribute binding is invalid"))
		}
		value := string(parts[offset+2])
		if value == "unspecified" || value == "unset" && attribute != "diff" {
			continue
		}
		return historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_GIT_UNSAFE", errors.New("content-transforming git attributes are not allowed"))
	}
	flags, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: historyStatusOutputLimit}, "ls-files", "-v", "-z")
	if err != nil {
		return classifyHistoryRead("DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	for _, entry := range splitNUL(flags.Stdout) {
		if len(entry) < 3 || entry[1] != ' ' {
			return historyError(foundation.ErrorConsistencyViolation, "DOCUMENT_HISTORY_RESULT_INVALID", errors.New("git index flag result is invalid"))
		}
		if entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z' {
			return historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_GIT_UNSAFE", errors.New("hidden git index flags are not allowed"))
		}
	}
	return nil
}

func (c *HistoryClient) ensureStableRootAndState(ctx context.Context, workspaceID foundation.ID, targetPath, root string, expected documenthistory.RepositoryState) error {
	currentRoot, err := c.resolveRoot(ctx, workspaceID)
	if err != nil {
		return err
	}
	if currentRoot != root {
		return historyError(foundation.ErrorVersionConflict, "DOCUMENT_HISTORY_CURSOR_STALE", errors.New("workspace root changed during history read"))
	}
	current, err := c.inspectResolved(ctx, workspaceID, targetPath, root)
	if err != nil {
		return err
	}
	if current != expected {
		return historyError(foundation.ErrorVersionConflict, "DOCUMENT_HISTORY_CURSOR_STALE", errors.New("git baseline changed during history read"))
	}
	return nil
}

func unsafeHistoryPath(value string) bool {
	return unsafeGitPath(value) || !documenthistory.ValidPath(value)
}

func parseHistoryLog(output []byte, objectFormat string) ([]documenthistory.Commit, error) {
	records := bytes.Split(output, []byte{0, 0, 0})
	commits := make([]documenthistory.Commit, 0, len(records))
	for _, record := range records {
		record = bytes.Trim(record, "\r\n")
		if len(record) == 0 {
			continue
		}
		fields := bytes.Split(record, []byte{0})
		if len(fields) != 6 {
			return nil, fmt.Errorf("git log record has %d fields", len(fields))
		}
		id := strings.ToLower(string(fields[0]))
		if !documenthistory.ValidObjectID(id) || len(id) != objectIDLength(objectFormat) {
			return nil, errors.New("git log commit id is invalid")
		}
		parents := strings.Fields(strings.ToLower(string(fields[1])))
		if len(parents) > documenthistory.MaxCommitParents {
			return nil, errors.New("git log parent list is too large")
		}
		for _, parent := range parents {
			if !documenthistory.ValidObjectID(parent) || len(parent) != objectIDLength(objectFormat) {
				return nil, errors.New("git log parent id is invalid")
			}
		}
		committedAt, err := time.Parse(time.RFC3339, string(fields[4]))
		if err != nil {
			return nil, fmt.Errorf("parse git commit time: %w", err)
		}
		authorName, authorEmail := string(fields[2]), string(fields[3])
		subject := strings.TrimSpace(string(fields[5]))
		if !validGitDisplayField(authorName, 512, false) || !validGitDisplayField(authorEmail, 512, false) || !validGitDisplayField(subject, 4096, false) || subject == "" {
			return nil, errors.New("git log subject is invalid")
		}
		commits = append(commits, documenthistory.Commit{ID: id, ParentIDs: parents, AuthorName: authorName, AuthorEmail: authorEmail, CommittedAt: committedAt, Subject: subject})
	}
	return commits, nil
}

func validGitDisplayField(value string, max int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || len(value) > max || !allowEmpty && value == "" {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func objectIDLength(format string) int {
	if format == "sha256" {
		return 64
	}
	return 40
}

func isMissingObject(err error) bool {
	var commandErr *commandError
	if !errors.As(err, &commandErr) {
		return false
	}
	message := commandErr.Stderr()
	return strings.Contains(message, "Not a valid object name") || strings.Contains(message, "bad object") || strings.Contains(message, "does not exist in") || strings.Contains(message, "path '") && strings.Contains(message, "exists on disk, but not in")
}

func classifyHistoryRead(code string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, "DOCUMENT_HISTORY_GIT_TIMEOUT", true, err)
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, "DOCUMENT_HISTORY_GIT_CANCELLED", false, err)
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) && commandErr.OutputLimitExceeded() {
		return historyError(foundation.ErrorPermissionDenied, "DOCUMENT_HISTORY_OUTPUT_TOO_LARGE", err)
	}
	var executableErr *exec.Error
	if errors.As(err, &executableErr) || errors.Is(err, os.ErrNotExist) {
		return historyError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_GIT_UNAVAILABLE", err)
	}
	return historyError(foundation.ErrorDependencyUnavailable, code, err)
}

func historyError(kind foundation.ErrorKind, code string, cause error) error {
	return foundation.NewError(kind, code, false, cause)
}

var _ interface {
	Inspect(context.Context, foundation.ID, string) (documenthistory.RepositoryState, error)
	List(context.Context, foundation.ID, string, int, int) (documenthistory.CommitPage, error)
	Compare(context.Context, foundation.ID, string, string, string) (documenthistory.Diff, error)
	ReadBlob(context.Context, foundation.ID, string, string) (documenthistory.Blob, error)
} = (*HistoryClient)(nil)
