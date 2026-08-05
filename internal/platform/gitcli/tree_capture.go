package gitcli

import (
	"context"
	"errors"
	"sort"
	"strings"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	committedTreeDiffOutputLimit = 16 << 20
	committedTreeChangeLimit     = 10_000
)

// ReadCommittedTreeChanges 读取同一对象格式、严格快进提交范围内的完整 tree 差异。
// 它只读取 Git object database，不观察或改动工作树。
func (c *WritebackClient) ReadCommittedTreeChanges(ctx context.Context, workspaceID foundation.ID, beforeCommit, afterCommit string) ([]workspacedomain.CommittedTreeChange, error) {
	if c == nil || !validWritebackWorkspaceID(workspaceID) || !canonicalGitObjectID(beforeCommit) || !canonicalGitObjectID(afterCommit) || len(beforeCommit) != len(afterCommit) {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_COMMITTED_TREE_INPUT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	root, err := c.resolveWritebackRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := c.ensureRepositoryTopLevel(ctx, root); err != nil {
		return nil, err
	}
	format, err := c.readObjectFormat(ctx, root)
	if err != nil {
		return nil, err
	}
	if (format == changecontrol.GitObjectFormatSHA1 && len(beforeCommit) != 40) ||
		(format == changecontrol.GitObjectFormatSHA256 && len(beforeCommit) != 64) {
		return nil, gitWritebackError(foundation.ErrorInvalidInput, "GIT_COMMITTED_TREE_OBJECT_FORMAT_INVALID", false, changecontrol.ErrGitInvalidInput)
	}
	if _, err := c.readCommitObject(ctx, root, beforeCommit); err != nil {
		return nil, err
	}
	if _, err := c.readCommitObject(ctx, root, afterCommit); err != nil {
		return nil, err
	}
	if err := c.ensureCommittedAncestor(ctx, root, beforeCommit, afterCommit); err != nil {
		return nil, err
	}
	result, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true, MaxOutputBytes: committedTreeDiffOutputLimit},
		"diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", beforeCommit, afterCommit, "--")
	if err != nil {
		return nil, classifyGitReadError("GIT_COMMITTED_TREE_DIFF_FAILED", err)
	}
	changes, err := parseCommittedTreeChanges(result.Stdout)
	if err != nil {
		return nil, gitWritebackError(foundation.ErrorConsistencyViolation, "GIT_COMMITTED_TREE_DIFF_INVALID", false, errors.Join(changecontrol.ErrGitConsistencyViolation, err))
	}
	return changes, nil
}

func (c *WritebackClient) ensureCommittedAncestor(ctx context.Context, root, beforeCommit, afterCommit string) error {
	_, err := c.git.runCommand(ctx, root, commandOptions{ReadOnly: true}, "merge-base", "--is-ancestor", beforeCommit, afterCommit)
	if err == nil {
		return nil
	}
	if isExitCode(err, 1) {
		return gitWritebackError(foundation.ErrorVersionConflict, "GIT_COMMITTED_TREE_NOT_FAST_FORWARD", false, changecontrol.ErrGitVersionConflict)
	}
	return classifyGitReadError("GIT_COMMITTED_TREE_ANCESTRY_FAILED", err)
}

func parseCommittedTreeChanges(raw []byte) ([]workspacedomain.CommittedTreeChange, error) {
	if len(raw) > committedTreeDiffOutputLimit {
		return nil, errors.New("committed tree diff output exceeds the limit")
	}
	parts := splitNUL(raw)
	if len(parts)%2 != 0 {
		return nil, errors.New("committed tree diff has an incomplete record")
	}
	byPath := make(map[string]workspacedomain.CommittedTreeChangeKind, len(parts)/2)
	for index := 0; index < len(parts); index += 2 {
		if index/2 >= committedTreeChangeLimit {
			return nil, errors.New("committed tree change count exceeds the limit")
		}
		kind, valid := committedTreeChangeKind(string(parts[index]))
		path := string(parts[index+1])
		if !valid || !safeCommittedTreePath(path) {
			return nil, errors.New("committed tree diff has an unsafe record")
		}
		if existing, found := byPath[path]; found {
			if existing != kind {
				return nil, errors.New("committed tree diff has conflicting path changes")
			}
			continue
		}
		byPath[path] = kind
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]workspacedomain.CommittedTreeChange, 0, len(paths))
	for _, path := range paths {
		changes = append(changes, workspacedomain.CommittedTreeChange{Kind: byPath[path], RelativePath: path})
	}
	return changes, nil
}

func committedTreeChangeKind(status string) (workspacedomain.CommittedTreeChangeKind, bool) {
	switch status {
	case "A", "M", "T":
		return workspacedomain.CommittedTreeChangeUpsert, true
	case "D":
		return workspacedomain.CommittedTreeChangeDelete, true
	default:
		return "", false
	}
}

func safeCommittedTreePath(path string) bool {
	return !unsafeGitPath(path) && !strings.Contains(path, "\\")
}

var _ workspacedomain.CommittedTreeReader = (*WritebackClient)(nil)
