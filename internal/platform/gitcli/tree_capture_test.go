package gitcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestReadCommittedTreeChangesReadsFullFastForwardRangeWithoutWorktree(t *testing.T) {
	repository := newWritebackTestRepository(t, "sha1", "notes/target.md")
	writeCommittedTreeFile(t, repository.root, "archive/old.md", "old\n")
	writeCommittedTreeFile(t, repository.root, "remove.md", "remove\n")
	runWritebackGit(t, repository.root, "add", "--", "archive/old.md", "remove.md")
	runWritebackGit(t, repository.root, "commit", "-m", "add tracked paths")
	before := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))

	writeCommittedTreeFile(t, repository.root, "notes/target.md", "changed\n")
	writeCommittedTreeFile(t, repository.root, "alpha.md", "added\n")
	if err := os.Remove(filepath.Join(repository.root, "remove.md")); err != nil {
		t.Fatal(err)
	}
	runWritebackGit(t, repository.root, "mv", "archive/old.md", "archive/new.md")
	runWritebackGit(t, repository.root, "add", "--", "notes/target.md", "alpha.md", "remove.md")
	runWritebackGit(t, repository.root, "commit", "-m", "update committed tree")
	after := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))

	// A dirty worktree must not change the committed-range result.
	writeCommittedTreeFile(t, repository.root, "notes/target.md", "uncommitted drift\n")
	changes, err := repository.client.ReadCommittedTreeChanges(context.Background(), writebackTestWorkspaceID, before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspacedomain.CommittedTreeChange{
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "alpha.md"},
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "archive/new.md"},
		{Kind: workspacedomain.CommittedTreeChangeDelete, RelativePath: "archive/old.md"},
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "notes/target.md"},
		{Kind: workspacedomain.CommittedTreeChangeDelete, RelativePath: "remove.md"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
}

func TestReadCommittedTreeChangesRejectsNonFastForwardAndInvalidRange(t *testing.T) {
	repository := newWritebackTestRepository(t, "sha1", "notes/target.md")
	before := repository.head
	writeCommittedTreeFile(t, repository.root, repository.target, "child\n")
	runWritebackGit(t, repository.root, "add", "--", repository.target)
	runWritebackGit(t, repository.root, "commit", "-m", "child")
	after := strings.TrimSpace(runWritebackGit(t, repository.root, "rev-parse", "HEAD"))

	if _, err := repository.client.ReadCommittedTreeChanges(context.Background(), writebackTestWorkspaceID, after, before); err == nil {
		t.Fatal("non-fast-forward range was accepted")
	} else {
		requireGitErrorCode(t, err, "GIT_COMMITTED_TREE_NOT_FAST_FORWARD", foundation.ErrorVersionConflict)
	}
	if _, err := repository.client.ReadCommittedTreeChanges(context.Background(), writebackTestWorkspaceID, strings.ToUpper(before), after); err == nil {
		t.Fatal("non-canonical object id was accepted")
	}
}

func TestParseCommittedTreeChangesRejectsUnsafeAndOversizedData(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "unsafe path", raw: []byte("A\x00../escape.md\x00")},
		{name: "backslash path", raw: []byte("A\x00notes\\escape.md\x00")},
		{name: "incomplete record", raw: []byte("A\x00")},
		{name: "unknown status", raw: []byte("R100\x00before.md\x00after.md\x00")},
		{name: "output limit", raw: bytes.Repeat([]byte("x"), committedTreeDiffOutputLimit+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseCommittedTreeChanges(test.raw); err == nil {
				t.Fatal("invalid committed tree diff was accepted")
			}
		})
	}

	tooMany := make([]byte, 0, committedTreeChangeLimit*16)
	for index := 0; index <= committedTreeChangeLimit; index++ {
		tooMany = append(tooMany, "A\x00file-"...)
		tooMany = append(tooMany, strconv.Itoa(index)...)
		tooMany = append(tooMany, ".md\x00"...)
	}
	if _, err := parseCommittedTreeChanges(tooMany); err == nil {
		t.Fatal("too many committed tree changes were accepted")
	}
}

func TestParseCommittedTreeChangesSortsAndDeduplicatesCompatiblePaths(t *testing.T) {
	changes, err := parseCommittedTreeChanges([]byte("M\x00z.md\x00A\x00a.md\x00T\x00z.md\x00D\x00gone.md\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []workspacedomain.CommittedTreeChange{
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "a.md"},
		{Kind: workspacedomain.CommittedTreeChangeDelete, RelativePath: "gone.md"},
		{Kind: workspacedomain.CommittedTreeChangeUpsert, RelativePath: "z.md"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
	if _, err := parseCommittedTreeChanges([]byte("A\x00same.md\x00D\x00same.md\x00")); err == nil {
		t.Fatal("conflicting path operations were accepted")
	}
}

func writeCommittedTreeFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}
