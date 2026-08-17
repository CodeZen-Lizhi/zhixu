package gitcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const statusAggregateTestWorkspaceID foundation.ID = "94000000-0000-4000-8000-000000000001"

type statusAggregateWorkspaceRepository struct {
	workspace            workspacedomain.Workspace
	err                  error
	allowBindingMismatch bool
}

func (repository statusAggregateWorkspaceRepository) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	if repository.err != nil {
		return workspacedomain.Workspace{}, repository.err
	}
	if !repository.allowBindingMismatch && repository.workspace.ID != id {
		return workspacedomain.Workspace{}, errors.New("workspace not found")
	}
	return repository.workspace, nil
}

func TestParseStatusAggregateCountsExactRecords(t *testing.T) {
	oid := strings.Repeat("a", 40)
	raw := statusAggregateFixture(
		[]byte("# branch.oid "+oid),
		[]byte("# branch.head feature/status"),
		[]byte("# branch.upstream origin/feature/status"),
		[]byte("# branch.ab +? -?"),
		[]byte(fmt.Sprintf("1 M. N... 100644 100644 100644 %s %s staged-secret.txt", oid, oid)),
		[]byte(fmt.Sprintf("1 .M N... 100644 100644 100644 %s %s unstaged-secret.txt", oid, oid)),
		[]byte(fmt.Sprintf("2 RM N... 100644 100644 100644 %s %s R100 renamed-secret.txt", oid, oid)),
		[]byte("original-secret.txt"),
		[]byte(fmt.Sprintf("u UU N... 100644 100644 100644 100644 %s %s %s conflict-secret.txt", oid, oid, oid)),
		[]byte("? untracked-secret.txt"),
		[]byte("! ignored-secret.txt"),
	)

	aggregate, err := parseStatusAggregate(raw, StatusObjectFormatSHA1)
	if err != nil {
		t.Fatal(err)
	}
	want := StatusAggregate{
		Branch:         "feature/status",
		Head:           oid,
		ObjectFormat:   StatusObjectFormatSHA1,
		StagedCount:    2,
		UnstagedCount:  2,
		UntrackedCount: 1,
		ConflictCount:  1,
	}
	if !reflect.DeepEqual(aggregate, want) {
		t.Fatalf("aggregate = %#v, want %#v", aggregate, want)
	}

	aggregate.WorkspaceID = "workspace-secret-canary"
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{
		"workspace-secret-canary",
		"staged-secret.txt",
		"unstaged-secret.txt",
		"renamed-secret.txt",
		"original-secret.txt",
		"conflict-secret.txt",
		"untracked-secret.txt",
		"ignored-secret.txt",
	} {
		if bytes.Contains(encoded, []byte(canary)) {
			t.Fatalf("aggregate JSON leaked %q: %s", canary, encoded)
		}
	}
}

func TestParseStatusAggregateAcceptsSHA256AndDiscardsNonUTF8Path(t *testing.T) {
	oid := strings.Repeat("b", 64)
	pathRecord := append([]byte("? "), 0xff, 0xfe)
	aggregate, err := parseStatusAggregate(statusAggregateFixture(
		[]byte("# branch.oid "+oid),
		[]byte("# branch.head main"),
		pathRecord,
	), StatusObjectFormatSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.ObjectFormat != StatusObjectFormatSHA256 || aggregate.Head != oid || aggregate.UntrackedCount != 1 || aggregate.Clean {
		t.Fatalf("aggregate = %#v", aggregate)
	}
}

func TestParseStatusObjectFormatIsStrict(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		want    string
		wantErr error
	}{
		{name: "sha1", raw: []byte("sha1\n"), want: StatusObjectFormatSHA1},
		{name: "sha256", raw: []byte("sha256\n"), want: StatusObjectFormatSHA256},
		{name: "unsupported", raw: []byte("sha512\n"), wantErr: errStatusAggregateUnsupported},
		{name: "invalid encoding", raw: []byte{'s', 'h', 'a', 0xff, '\n'}, wantErr: errStatusAggregateEncoding},
		{name: "multiple lines", raw: []byte("sha1\nsha256\n"), wantErr: errStatusAggregateInvalid},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format, err := parseStatusObjectFormat(test.raw)
			if format != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("format = %q, error = %#v", format, err)
			}
		})
	}
}

func TestParseStatusAggregateRejectsUnsafeOrAmbiguousOutput(t *testing.T) {
	oid := strings.Repeat("a", 40)
	ordinary := []byte(fmt.Sprintf("1 M. N... 100644 100644 100644 %s %s tracked.txt", oid, oid))
	rename := []byte(fmt.Sprintf("2 R. N... 100644 100644 100644 %s %s R100 renamed.txt", oid, oid))
	valid := statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"))
	invalidBranch := append([]byte("# branch.head "), 0xff)
	oversized := bytes.Repeat([]byte{'x'}, statusAggregateOutputLimit+1)
	oversized[len(oversized)-1] = 0

	tests := []struct {
		name string
		raw  []byte
		want error
	}{
		{name: "missing NUL terminator", raw: valid[:len(valid)-1], want: errStatusAggregateInvalid},
		{name: "oversized", raw: oversized, want: errStatusAggregateInvalid},
		{name: "record before headers", raw: statusAggregateFixture([]byte("? secret.txt")), want: errStatusAggregateAmbiguous},
		{name: "missing head", raw: statusAggregateFixture([]byte("# branch.oid " + oid)), want: errStatusAggregateAmbiguous},
		{name: "duplicate oid", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.oid "+oid)), want: errStatusAggregateAmbiguous},
		{name: "header after record", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte("? secret.txt"), []byte("# branch.upstream origin/main")), want: errStatusAggregateAmbiguous},
		{name: "detached", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head (detached)")), want: errStatusAggregateDetached},
		{name: "unborn", raw: statusAggregateFixture([]byte("# branch.oid (initial)"), []byte("# branch.head main")), want: errStatusAggregateUnborn},
		{name: "invalid branch encoding", raw: statusAggregateFixture([]byte("# branch.oid "+oid), invalidBranch), want: errStatusAggregateEncoding},
		{name: "invalid head object id", raw: statusAggregateFixture([]byte("# branch.oid "+strings.Repeat("A", 40)), []byte("# branch.head main")), want: errStatusAggregateInvalid},
		{name: "upstream without ahead behind", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte("# branch.upstream origin/main")), want: errStatusAggregateAmbiguous},
		{name: "ahead behind without upstream", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte("# branch.ab +0 -0")), want: errStatusAggregateAmbiguous},
		{name: "unknown header", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte("# branch.future value")), want: errStatusAggregateUnsupported},
		{name: "unsupported record", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte("9 secret.txt")), want: errStatusAggregateUnsupported},
		{name: "submodule marker", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte(fmt.Sprintf("1 M. S... 100644 100644 100644 %s %s module", oid, oid))), want: errStatusAggregateSubmodule},
		{name: "gitlink mode", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte(fmt.Sprintf("1 A. N... 000000 160000 160000 %s %s module", oid, oid))), want: errStatusAggregateSubmodule},
		{name: "copy record", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), []byte(fmt.Sprintf("2 C. N... 100644 100644 100644 %s %s C100 copy.txt", oid, oid)), []byte("source.txt")), want: errStatusAggregateUnsupported},
		{name: "rename missing original", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), rename), want: errStatusAggregateAmbiguous},
		{name: "rename paths equal", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), rename, []byte("renamed.txt")), want: errStatusAggregateAmbiguous},
		{name: "ordinary missing path", raw: statusAggregateFixture([]byte("# branch.oid "+oid), []byte("# branch.head main"), ordinary[:len(ordinary)-len("tracked.txt")]), want: errStatusAggregateInvalid},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseStatusAggregate(test.raw, StatusObjectFormatSHA1)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %#v, want %v", err, test.want)
			}
		})
	}
}

func TestParseStatusAggregateRejectsCounterOverflow(t *testing.T) {
	oid := strings.Repeat("a", 40)
	raw := make([]byte, 0, 500_000)
	appendRecord := func(value string) {
		raw = append(raw, value...)
		raw = append(raw, 0)
	}
	appendRecord("# branch.oid " + oid)
	appendRecord("# branch.head main")
	for range statusAggregateCountLimit + 1 {
		appendRecord("? x")
	}

	_, err := parseStatusAggregate(raw, StatusObjectFormatSHA1)
	if !errors.Is(err, errStatusAggregateCountOverflow) {
		t.Fatalf("error = %#v", err)
	}
}

func TestClassifyStatusAggregateParseUsesStableCodes(t *testing.T) {
	tests := []struct {
		cause error
		code  string
		kind  foundation.ErrorKind
	}{
		{cause: errStatusAggregateDetached, code: "GIT_REPOSITORY_DETACHED", kind: foundation.ErrorVersionConflict},
		{cause: errStatusAggregateUnborn, code: "GIT_REPOSITORY_UNBORN", kind: foundation.ErrorVersionConflict},
		{cause: errStatusAggregateSubmodule, code: "GIT_STATUS_SUBMODULE_UNSUPPORTED", kind: foundation.ErrorPermissionDenied},
		{cause: errStatusAggregateCountOverflow, code: "GIT_STATUS_COUNT_OVERFLOW", kind: foundation.ErrorConsistencyViolation},
		{cause: errStatusAggregateEncoding, code: "GIT_STATUS_ENCODING_INVALID", kind: foundation.ErrorConsistencyViolation},
		{cause: errStatusAggregateUnsupported, code: "GIT_STATUS_RECORD_UNSUPPORTED", kind: foundation.ErrorConsistencyViolation},
		{cause: errStatusAggregateAmbiguous, code: "GIT_STATUS_RESULT_AMBIGUOUS", kind: foundation.ErrorConsistencyViolation},
		{cause: errStatusAggregateInvalid, code: "GIT_STATUS_RESULT_INVALID", kind: foundation.ErrorConsistencyViolation},
	}

	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			requireStatusAggregateError(t, classifyStatusAggregateParse(test.cause), test.code, test.kind)
		})
	}
}

func TestStatusAggregateClientInspectsDirtyRepository(t *testing.T) {
	root := initRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rename-me.txt"), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt", "rename-me.txt")
	runGit(t, root, "commit", "-m", "add status fixtures")
	client := newStatusAggregateClientForTest(t, New(""), root)

	clean, err := client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !clean.Clean || clean.WorkspaceID != statusAggregateTestWorkspaceID {
		t.Fatalf("clean aggregate = %#v", clean)
	}

	runGit(t, root, "mv", "rename-me.txt", "renamed.txt")
	if err := os.WriteFile(filepath.Join(root, "renamed.txt"), []byte("one changed\ntwo\nthree\nfour\nfive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "renamed.txt")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged-new.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "staged-new.txt")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	aggregate, err := client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	wantHead := strings.TrimSpace(statusGitOutput(t, root, "rev-parse", "HEAD"))
	if aggregate.WorkspaceID != statusAggregateTestWorkspaceID || aggregate.Branch != "main" || aggregate.Head != wantHead ||
		aggregate.ObjectFormat != StatusObjectFormatSHA1 || aggregate.Clean || aggregate.StagedCount != 2 ||
		aggregate.UnstagedCount != 1 || aggregate.UntrackedCount != 1 || aggregate.ConflictCount != 0 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
}

func TestStatusAggregateClientCountsConflictOnly(t *testing.T) {
	root := initRepository(t)
	runGit(t, root, "switch", "-c", "conflict-side")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "side")
	runGit(t, root, "switch", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "main")
	command := exec.Command("git", "-C", root, "merge", "conflict-side")
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}

	aggregate, err := newStatusAggregateClientForTest(t, New(""), root).Inspect(context.Background(), statusAggregateTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Clean || aggregate.ConflictCount != 1 || aggregate.StagedCount != 0 || aggregate.UnstagedCount != 0 || aggregate.UntrackedCount != 0 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
}

func TestStatusAggregateClientFailsClosedForRepositoryStates(t *testing.T) {
	t.Run("detached", func(t *testing.T) {
		root := initRepository(t)
		runGit(t, root, "checkout", "--detach")
		_, err := newStatusAggregateClientForTest(t, New(""), root).Inspect(context.Background(), statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_REPOSITORY_DETACHED", foundation.ErrorVersionConflict)
	})

	t.Run("unborn", func(t *testing.T) {
		root := t.TempDir()
		runGit(t, root, "init", "--initial-branch=main")
		_, err := newStatusAggregateClientForTest(t, New(""), root).Inspect(context.Background(), statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_REPOSITORY_UNBORN", foundation.ErrorVersionConflict)
	})

}

func TestStatusAggregateClientRejectsWorkspaceAndRepositoryMismatch(t *testing.T) {
	root := initRepository(t)

	t.Run("workspace binding", func(t *testing.T) {
		client, err := NewStatusAggregateClient(New(""), statusAggregateWorkspaceRepository{
			workspace: workspacedomain.Workspace{
				ID: "94000000-0000-4000-8000-000000000099", RootPath: root,
				Status: workspacedomain.WorkspaceStatusActive, Availability: workspacedomain.WorkspaceAvailabilityAvailable,
			},
			allowBindingMismatch: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_STATUS_WORKSPACE_BINDING_INVALID", foundation.ErrorConsistencyViolation)
	})

	t.Run("nested repository root", func(t *testing.T) {
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := newStatusAggregateClientForTest(t, New(""), nested).Inspect(context.Background(), statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_REPOSITORY_ROOT_MISMATCH", foundation.ErrorPermissionDenied)
	})

	t.Run("inactive workspace", func(t *testing.T) {
		client, err := NewStatusAggregateClient(New(""), statusAggregateWorkspaceRepository{workspace: workspacedomain.Workspace{
			ID: statusAggregateTestWorkspaceID, RootPath: root,
			Status: workspacedomain.WorkspaceStatusInactive, Availability: workspacedomain.WorkspaceAvailabilityAvailable,
		}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_STATUS_WORKSPACE_UNAVAILABLE", foundation.ErrorPermissionDenied)
	})
}

func TestNewStatusAggregateClientRequiresWorkspaceRepository(t *testing.T) {
	var typedNil *statusAggregateWorkspaceRepository
	for _, test := range []struct {
		name       string
		repository WorkspaceRepository
	}{
		{name: "nil"},
		{name: "typed nil", repository: typedNil},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewStatusAggregateClient(New(""), test.repository)
			if client != nil {
				t.Fatalf("client = %#v", client)
			}
			requireStatusAggregateError(t, err, "GIT_STATUS_WORKSPACE_REPOSITORY_UNAVAILABLE", foundation.ErrorDependencyUnavailable)
		})
	}
}

func TestStatusAggregateClientInspectRejectsTypedNilWorkspaceRepository(t *testing.T) {
	var typedNil *statusAggregateWorkspaceRepository
	client := &StatusAggregateClient{git: New(""), workspaces: typedNil}

	_, err := client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
	requireStatusAggregateError(t, err, "GIT_STATUS_INSPECTOR_UNAVAILABLE", foundation.ErrorDependencyUnavailable)
}

func TestStatusAggregateClientUsesFixedIsolatedCommandProfile(t *testing.T) {
	root := canonicalStatusRoot(t, t.TempDir())
	logPath := filepath.Join(t.TempDir(), "commands.log")
	attackerHome := t.TempDir()
	head := strings.Repeat("c", 40)
	t.Setenv("HOME", attackerHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_DIR", "/attacker/git-dir")
	t.Setenv("GIT_WORK_TREE", "/attacker/work-tree")
	t.Setenv("STATUS_TEST_ROOT", root)
	t.Setenv("STATUS_TEST_HEAD", head)
	t.Setenv("STATUS_TEST_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
set -eu
[ "$HOME" = "$XDG_CONFIG_HOME" ] || exit 20
[ -d "$HOME" ] || exit 21
[ -z "$(ls -A "$HOME")" ] || exit 22
{
  printf 'CALL\n'
  for arg in "$@"; do
    printf 'ARG=%s\n' "$arg"
  done
  printf 'ENV=%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$HOME" "$XDG_CONFIG_HOME" "${GIT_CONFIG_NOSYSTEM-unset}" "${GIT_CONFIG_GLOBAL-unset}" \
    "${GIT_ATTR_NOSYSTEM-unset}" "${GIT_OPTIONAL_LOCKS-unset}" "${GIT_DIR-unset}" \
    "${GIT_WORK_TREE-unset}" "${LC_ALL-unset}" "${LANG-unset}"
} >> "$STATUS_TEST_LOG"
case "$*" in
  *"rev-parse --show-toplevel") printf '%s\n' "$STATUS_TEST_ROOT" ;;
  *"rev-parse --show-object-format") printf 'sha1\n' ;;
  *"status --porcelain=v2 --branch --no-ahead-behind -z --untracked-files=normal --ignore-submodules=all")
    printf '# branch.oid %s\000# branch.head main\000' "$STATUS_TEST_HEAD"
    ;;
  *) exit 23 ;;
esac
`)
	client := newStatusAggregateClientForTest(t, New(executable), root)

	aggregate, err := client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !aggregate.Clean || aggregate.Head != head || aggregate.WorkspaceID != statusAggregateTestWorkspaceID {
		t.Fatalf("aggregate = %#v", aggregate)
	}

	commandLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls, environments := parseStatusAggregateCommandLog(t, commandLog)
	base := []string{
		"--no-pager",
		"--literal-pathspecs",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.quotePath=true",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "diff.compactionHeuristic=false",
		"-c", "diff.suppressBlankEmpty=false",
		"-c", "log.showSignature=false",
		"-c", "color.ui=false",
		"-C", root,
	}
	wantCalls := [][]string{
		append(append([]string{}, base...), "rev-parse", "--show-toplevel"),
		append(append([]string{}, base...), "rev-parse", "--show-object-format"),
		append(append([]string{}, base...), "status", "--porcelain=v2", "--branch", "--no-ahead-behind", "-z", "--untracked-files=normal", "--ignore-submodules=all"),
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if len(environments) != 3 {
		t.Fatalf("environments = %#v", environments)
	}
	isolatedHome := environments[0][0]
	for _, environment := range environments {
		want := []string{isolatedHome, isolatedHome, "1", os.DevNull, "1", "0", "unset", "unset", "C", "C"}
		if !reflect.DeepEqual(environment, want) {
			t.Fatalf("environment = %#v, want %#v", environment, want)
		}
	}
	if isolatedHome == attackerHome {
		t.Fatal("status command inherited HOME")
	}
	if _, err := os.Stat(isolatedHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated config directory was not removed: %v", err)
	}
}

func TestStatusAggregateClientClassifiesTimeoutAndOutputOverflow(t *testing.T) {
	root := canonicalStatusRoot(t, t.TempDir())
	head := strings.Repeat("d", 40)
	t.Setenv("STATUS_TEST_ROOT", root)
	t.Setenv("STATUS_TEST_HEAD", head)

	t.Run("timeout", func(t *testing.T) {
		executable := writeExecutable(t, `#!/bin/sh
case "$*" in
  *"rev-parse --show-toplevel") printf '%s\n' "$STATUS_TEST_ROOT" ;;
  *"rev-parse --show-object-format") printf 'sha1\n' ;;
  *"status --porcelain=v2 --branch --no-ahead-behind -z --untracked-files=normal --ignore-submodules=all") exec sleep 10 ;;
  *) exit 31 ;;
esac
`)
		client := newStatusAggregateClientForTest(t, New(executable), root)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := client.Inspect(ctx, statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_STATUS_TIMEOUT", foundation.ErrorRetryableFailure)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("stdout overflow", func(t *testing.T) {
		executable := writeExecutable(t, `#!/bin/sh
case "$*" in
  *"rev-parse --show-toplevel") printf '%s\n' "$STATUS_TEST_ROOT" ;;
  *"rev-parse --show-object-format") printf 'sha1\n' ;;
  *"status --porcelain=v2 --branch --no-ahead-behind -z --untracked-files=normal --ignore-submodules=all") dd if=/dev/zero bs=1048577 count=1 2>/dev/null ;;
  *) exit 32 ;;
esac
`)
		client := newStatusAggregateClientForTest(t, New(executable), root)
		_, err := client.Inspect(context.Background(), statusAggregateTestWorkspaceID)
		requireStatusAggregateError(t, err, "GIT_STATUS_OUTPUT_TOO_LARGE", foundation.ErrorPermissionDenied)
		if strings.Contains(err.Error(), root) {
			t.Fatalf("error leaked repository path: %v", err)
		}
	})
}

func TestRunCommandRejectsUnsafeIsolatedConfigDirectory(t *testing.T) {
	configDir := t.TempDir()
	if err := os.Chmod(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := New(writeExecutable(t, "#!/bin/sh\nexit 0\n")).runCommand(
		context.Background(), t.TempDir(), commandOptions{ReadOnly: true, IsolatedConfigDir: configDir}, "status",
	)
	if !errors.Is(err, errCommandConfigHomeUnsafe) {
		t.Fatalf("error = %#v", err)
	}
}

func statusAggregateFixture(records ...[]byte) []byte {
	size := len(records)
	for _, record := range records {
		size += len(record)
	}
	raw := make([]byte, 0, size)
	for _, record := range records {
		raw = append(raw, record...)
		raw = append(raw, 0)
	}
	return raw
}

func newStatusAggregateClientForTest(t *testing.T, git Client, root string) *StatusAggregateClient {
	t.Helper()
	canonical := canonicalStatusRoot(t, root)
	client, err := NewStatusAggregateClient(git, statusAggregateWorkspaceRepository{workspace: workspacedomain.Workspace{
		ID: statusAggregateTestWorkspaceID, RootPath: canonical,
		Status: workspacedomain.WorkspaceStatusActive, Availability: workspacedomain.WorkspaceAvailabilityAvailable,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func canonicalStatusRoot(t *testing.T, root string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func statusGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func parseStatusAggregateCommandLog(t *testing.T, raw []byte) ([][]string, [][]string) {
	t.Helper()
	sections := strings.Split(string(raw), "CALL\n")
	if len(sections) < 2 || sections[0] != "" {
		t.Fatalf("command log = %q", raw)
	}
	calls := make([][]string, 0, len(sections)-1)
	environments := make([][]string, 0, len(sections)-1)
	for _, section := range sections[1:] {
		lines := strings.Split(strings.TrimSuffix(section, "\n"), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[len(lines)-1], "ENV=") {
			t.Fatalf("command log section = %q", section)
		}
		args := make([]string, 0, len(lines)-1)
		for _, line := range lines[:len(lines)-1] {
			if !strings.HasPrefix(line, "ARG=") {
				t.Fatalf("command log line = %q", line)
			}
			args = append(args, strings.TrimPrefix(line, "ARG="))
		}
		calls = append(calls, args)
		environment := strings.Split(strings.TrimPrefix(lines[len(lines)-1], "ENV="), "\t")
		if len(environment) != 10 {
			t.Fatalf("environment = %#v", environment)
		}
		environments = append(environments, environment)
	}
	return calls, environments
}

func requireStatusAggregateError(t *testing.T, err error, code string, kind foundation.ErrorKind) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code || classified.Kind != kind {
		t.Fatalf("error = %#v, want code=%s kind=%s", err, code, kind)
	}
}
