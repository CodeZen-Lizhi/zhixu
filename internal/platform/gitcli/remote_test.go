package gitcli

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/remoteurl"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const remoteTestWorkspaceID foundation.ID = "b01b2c3d-4e5f-4678-9abc-def012345678"

type remoteWorkspaceRepository struct{ workspace workspacedomain.Workspace }

type staticRemotePolicy struct{ endpoint remoteurl.Endpoint }

func (policy staticRemotePolicy) ResolveHTTPSRemote(context.Context, string) (remoteurl.Endpoint, error) {
	return policy.endpoint, nil
}

func (r remoteWorkspaceRepository) GetWorkspaceByID(_ context.Context, id foundation.ID) (workspacedomain.Workspace, error) {
	if id != r.workspace.ID {
		return workspacedomain.Workspace{}, errors.New("workspace not found")
	}
	return r.workspace, nil
}

type remoteTestRepository struct {
	root   string
	bare   string
	client *RemoteClient
	access application.GitAccess
}

func TestParseRemoteChangesKeepsBoundedPreviewAndValidatesTail(t *testing.T) {
	raw := []byte(strings.Repeat("M\x00note.md\x00", domain.MaxChangedFiles))
	changes, err := parseRemoteChanges(raw)
	if err != nil || len(changes) != domain.MaxChangedFiles {
		t.Fatalf("changes=%d err=%v", len(changes), err)
	}

	raw = append(raw, []byte("M\x00overflow.md\x00")...)
	changes, err = parseRemoteChanges(raw)
	if err != nil || len(changes) != domain.MaxChangedFiles {
		t.Fatalf("changes=%d err=%v", len(changes), err)
	}

	raw = append(raw, []byte("X\x00unsafe.md\x00")...)
	if changes, err = parseRemoteChanges(raw); err == nil || changes != nil {
		t.Fatalf("invalid tail changes=%v err=%v", changes, err)
	}
}

func TestParseRemoteChangesRejectsMalformedStatusAndTermination(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("M\x00note.md"),
		[]byte("MALFORMED\x00note.md\x00"),
		[]byte("Rabc\x00old.md\x00new.md\x00"),
		[]byte("R+1\x00old.md\x00new.md\x00"),
		[]byte("R-0\x00old.md\x00new.md\x00"),
		[]byte("R101\x00old.md\x00new.md\x00"),
		[]byte("R100\x00old.md\x00new.md"),
	} {
		if changes, err := parseRemoteChanges(raw); err == nil || changes != nil {
			t.Fatalf("malformed record %q produced changes=%v err=%v", raw, changes, err)
		}
	}
}

func TestValidRemoteBranchRejectsInvalidGitRefComponents(t *testing.T) {
	for _, branch := range []string{"-topic", "feature//topic", "feature/.hidden", "feature/topic.lock", "feature/foo.lock/bar"} {
		if validRemoteBranch(branch) {
			t.Fatalf("invalid Git branch %q was accepted", branch)
		}
	}
	if !validRemoteBranch("feature/topic") {
		t.Fatal("valid Git branch was rejected")
	}
}

func TestRemoteClientSynchronizesWithBareRemoteWithoutConfigMutation(t *testing.T) {
	repository := newRemoteTestRepository(t)
	ctx := context.Background()
	configBefore := runRemoteGit(t, repository.root, "config", "--local", "--list")

	if err := repository.client.TestConnection(ctx, repository.access); err != nil {
		t.Fatal(err)
	}
	if err := repository.client.Fetch(ctx, repository.access); err != nil {
		t.Fatal(err)
	}
	same, err := repository.client.Compare(ctx, repository.access)
	if err != nil {
		t.Fatal(err)
	}
	if same.Relation != domain.RelationSame || !same.Attached || !same.WorktreeClean {
		t.Fatalf("initial comparison = %#v", same)
	}
	if configAfter := runRemoteGit(t, repository.root, "config", "--local", "--list"); configAfter != configBefore {
		t.Fatalf("remote adapter changed local Git config:\nbefore=%q\nafter=%q", configBefore, configAfter)
	}

	remoteHead := commitToBareRemote(t, repository.bare, "remote.md", "remote ahead\n")
	if err := repository.client.Fetch(ctx, repository.access); err != nil {
		t.Fatal(err)
	}
	pull, err := repository.client.Compare(ctx, repository.access)
	if err != nil {
		t.Fatal(err)
	}
	if pull.Relation != domain.RelationRemoteAhead || pull.HeadOID == remoteHead || pull.Ahead != 0 || pull.Behind != 1 {
		t.Fatalf("pull comparison = %#v", pull)
	}
	mutation, err := repository.client.FastForward(ctx, application.FastForwardCommand{
		Access: repository.access, ExpectedHeadOID: pull.HeadOID, ExpectedRemoteOID: pull.RemoteOID,
	})
	if err != nil || !mutation.ResultKnown {
		t.Fatalf("FastForward() = %#v, %v", mutation, err)
	}
	verification, err := repository.client.Verify(ctx, application.VerifyCommand{
		Access: repository.access, ExpectedHeadOID: remoteHead, ExpectedRemoteOID: remoteHead,
	})
	if err != nil || !verification.WorktreeClean || verification.HeadOID != remoteHead || verification.RemoteOID != remoteHead {
		t.Fatalf("pull Verify() = %#v, %v", verification, err)
	}

	localHead := commitInRepository(t, repository.root, "local.md", "local ahead\n")
	if err := repository.client.Fetch(ctx, repository.access); err != nil {
		t.Fatal(err)
	}
	push, err := repository.client.Compare(ctx, repository.access)
	if err != nil {
		t.Fatal(err)
	}
	if push.Relation != domain.RelationLocalAhead || push.HeadOID != localHead || push.RemoteOID != remoteHead || push.Ahead != 1 || push.Behind != 0 {
		t.Fatalf("push comparison = %#v", push)
	}
	mutation, err = repository.client.Push(ctx, application.PushCommand{
		Access: repository.access, ExpectedHeadOID: push.HeadOID, ExpectedRemoteOID: push.RemoteOID,
	})
	if err != nil || !mutation.ResultKnown {
		t.Fatalf("Push() = %#v, %v", mutation, err)
	}
	verification, err = repository.client.Verify(ctx, application.VerifyCommand{
		Access: repository.access, ExpectedHeadOID: localHead, ExpectedRemoteOID: localHead,
	})
	if err != nil || !verification.WorktreeClean || verification.HeadOID != localHead || verification.RemoteOID != localHead {
		t.Fatalf("push Verify() = %#v, %v", verification, err)
	}

	privateRef := runRemoteGit(t, repository.root, "rev-parse", privateRemoteRef("main"))
	if privateRef != localHead {
		t.Fatalf("private fetched ref = %q, want %q", privateRef, localHead)
	}
}

func TestRemoteClientSupportsLinkedWorktreeConfigLock(t *testing.T) {
	repository := newRemoteTestRepository(t)
	linkedRoot := filepath.Join(t.TempDir(), "linked-worktree")
	runRemoteGit(t, repository.root, "worktree", "add", "--detach", linkedRoot, "HEAD")
	linkedRoot, err := filepath.EvalSymlinks(linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	commonDirectory := runRemoteGit(t, linkedRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	configWriteResultPath := filepath.Join(t.TempDir(), "config-write-result")
	executable := writeExecutable(t, `#!/bin/sh
case "$*" in
*ls-remote*)
  test -f "$ZHIXU_TEST_COMMON_DIR/config.lock" || exit 98
  git -C "$ZHIXU_TEST_LINKED_ROOT" config --local 'http.https://example.invalid/.proxy' 'http://proxy.invalid' >/dev/null 2>&1
  printf '%s' "$?" > "$ZHIXU_TEST_CONFIG_WRITE_RESULT"
  exec git "$@"
  ;;
*) exec git "$@" ;;
esac
`)
	t.Setenv("ZHIXU_TEST_COMMON_DIR", commonDirectory)
	t.Setenv("ZHIXU_TEST_LINKED_ROOT", linkedRoot)
	t.Setenv("ZHIXU_TEST_CONFIG_WRITE_RESULT", configWriteResultPath)
	client, err := newRemoteClientForTests(New(executable), remoteWorkspaceRepository{workspace: workspacedomain.Workspace{
		ID: remoteTestWorkspaceID, RootPath: linkedRoot,
	}})
	if err != nil {
		t.Fatal(err)
	}

	if err := client.TestConnection(context.Background(), repository.access); err != nil {
		t.Fatal(err)
	}
	if err := client.Fetch(context.Background(), repository.access); err != nil {
		t.Fatal(err)
	}
	configWriteResult, err := os.ReadFile(configWriteResultPath)
	if err != nil || string(configWriteResult) == "0" {
		t.Fatalf("linked-worktree config write was not blocked: result=%q err=%v", configWriteResult, err)
	}
	if _, err := os.Stat(filepath.Join(commonDirectory, "config.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared Git config lock remains after linked-worktree command: %v", err)
	}
}

func TestRemoteClientComparisonReportsDirtyDetachedAndDiverged(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		repository := newRemoteTestRepository(t)
		if err := repository.client.Fetch(context.Background(), repository.access); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository.root, "README.md"), []byte("dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		comparison, err := repository.client.Compare(context.Background(), repository.access)
		if err != nil {
			t.Fatal(err)
		}
		if comparison.WorktreeClean || comparison.Relation != domain.RelationSame {
			t.Fatalf("dirty comparison = %#v", comparison)
		}
	})
	t.Run("detached", func(t *testing.T) {
		repository := newRemoteTestRepository(t)
		if err := repository.client.Fetch(context.Background(), repository.access); err != nil {
			t.Fatal(err)
		}
		head := runRemoteGit(t, repository.root, "rev-parse", "HEAD")
		runRemoteGit(t, repository.root, "checkout", "--detach", head)
		comparison, err := repository.client.Compare(context.Background(), repository.access)
		if err != nil {
			t.Fatal(err)
		}
		if comparison.Attached || comparison.Branch != "" || comparison.Relation != domain.RelationSame {
			t.Fatalf("detached comparison = %#v", comparison)
		}
	})
	t.Run("diverged", func(t *testing.T) {
		repository := newRemoteTestRepository(t)
		commitInRepository(t, repository.root, "local.md", "local\n")
		commitToBareRemote(t, repository.bare, "remote.md", "remote\n")
		if err := repository.client.Fetch(context.Background(), repository.access); err != nil {
			t.Fatal(err)
		}
		comparison, err := repository.client.Compare(context.Background(), repository.access)
		if err != nil {
			t.Fatal(err)
		}
		if comparison.Relation != domain.RelationDiverged || comparison.Ahead != 1 || comparison.Behind != 1 {
			t.Fatalf("diverged comparison = %#v", comparison)
		}
	})
}

func TestRemoteCredentialSessionKeepsTokenOutOfArgumentsConfigAndErrors(t *testing.T) {
	root := newRemoteTestRepository(t).root
	argsPath := filepath.Join(t.TempDir(), "args")
	envPath := filepath.Join(t.TempDir(), "env")
	helperPath := filepath.Join(t.TempDir(), "helper-path")
	configWriteResultPath := filepath.Join(t.TempDir(), "config-write-result")
	executable := writeExecutable(t, `#!/bin/sh
case "$*" in
*--show-toplevel*) printf '%s\n' "$ZHIXU_TEST_ROOT" ;;
*ls-remote*)
  test -f "$ZHIXU_TEST_ROOT/.git/config.lock" || exit 98
  git -C "$ZHIXU_TEST_ROOT" config --local 'http.https://example.invalid/.proxy' 'http://proxy.invalid' >/dev/null 2>&1
  printf '%s' "$?" > "$ZHIXU_TEST_CONFIG_WRITE_RESULT"
  printf '%040d\trefs/heads/main\n' 0
  printf '%s\n' "$@" > "$ZHIXU_TEST_ARGS"
  env | sort > "$ZHIXU_TEST_ENV"
  printf '%s' "$GIT_ASKPASS" > "$ZHIXU_TEST_HELPER"
  ;;
*) exec git "$@" ;;
esac
`)
	t.Setenv("ZHIXU_TEST_ROOT", root)
	t.Setenv("ZHIXU_TEST_ARGS", argsPath)
	t.Setenv("ZHIXU_TEST_ENV", envPath)
	t.Setenv("ZHIXU_TEST_HELPER", helperPath)
	t.Setenv("ZHIXU_TEST_CONFIG_WRITE_RESULT", configWriteResultPath)
	t.Setenv("HTTP_PROXY", "http://proxy.invalid")
	t.Setenv("https_proxy", "http://proxy.invalid")
	t.Setenv("ALL_PROXY", "http://proxy.invalid")
	t.Setenv("no_proxy", "example.invalid")
	remoteURL := "https://example.invalid/repository.git"
	client, err := NewRemoteClient(
		New(executable),
		remoteWorkspaceRepository{workspace: workspacedomain.Workspace{ID: remoteTestWorkspaceID, RootPath: root}},
		staticRemotePolicy{endpoint: remoteurl.Endpoint{
			URL: remoteURL, Hostname: "example.invalid", Port: 443,
			Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	access := remoteTestAccess(t, remoteURL)
	if err := client.TestConnection(context.Background(), access); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(arguments), "remote-test-secret") || strings.Contains(string(arguments), "user@") {
		t.Fatalf("credential leaked into argv: %q", arguments)
	}
	config := runRemoteGit(t, root, "config", "--local", "--list")
	if strings.Contains(config, "remote-test-secret") {
		t.Fatalf("credential leaked into Git config: %q", config)
	}
	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(environment), "GIT_ASKPASS=") || !strings.Contains(string(environment), "GIT_CONFIG_GLOBAL="+os.DevNull) {
		t.Fatalf("credential session environment is incomplete: %q", environment)
	}
	if !strings.Contains(string(arguments), "http.followRedirects=false") {
		t.Fatalf("remote command permits HTTP redirects: %q", arguments)
	}
	if !strings.Contains(string(arguments), "http.curloptResolve=example.invalid:443:8.8.8.8") {
		t.Fatalf("remote command does not pin the validated DNS answer: %q", arguments)
	}
	for _, entry := range strings.Split(string(environment), "\n") {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(name) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			t.Fatalf("proxy environment escaped the credential session boundary: %q", entry)
		}
	}
	path, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AskPass helper remains after command: %v", err)
	}
	configWriteResult, err := os.ReadFile(configWriteResultPath)
	if err != nil || string(configWriteResult) == "0" {
		t.Fatalf("concurrent git config write was not blocked: result=%q err=%v", configWriteResult, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "config.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git config lock remains after command: %v", err)
	}

	access.RemoteURL = "https://user:remote-test-secret@example.invalid/repository.git"
	err = client.TestConnection(context.Background(), access)
	requireRemoteErrorCode(t, err, domain.ErrorCodeInvalid, foundation.ErrorInvalidInput)
	if strings.Contains(err.Error(), "remote-test-secret") {
		t.Fatalf("credential leaked into error: %v", err)
	}
}

func TestRemoteClientRejectsUnsafeLocalTransportConfigBeforeNetworkCommand(t *testing.T) {
	repository := newRemoteTestRepository(t)
	marker := filepath.Join(t.TempDir(), "network-command-invoked")
	executable := writeExecutable(t, `#!/bin/sh
case "$*" in
*ls-remote*) : > "$ZHIXU_TEST_NETWORK_MARKER"; exit 99 ;;
*) exec git "$@" ;;
esac
`)
	t.Setenv("ZHIXU_TEST_NETWORK_MARKER", marker)
	runRemoteGit(t, repository.root, "config", "--local", "url.file:///tmp/unsafe-remote.insteadOf", repository.access.RemoteURL)
	client, err := newRemoteClientForTests(New(executable), remoteWorkspaceRepository{workspace: workspacedomain.Workspace{
		ID: remoteTestWorkspaceID, RootPath: repository.root,
	}})
	if err != nil {
		t.Fatal(err)
	}

	err = client.TestConnection(context.Background(), repository.access)
	requireRemoteErrorCode(t, err, domain.ErrorCodeURLInvalid, foundation.ErrorPermissionDenied)
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsafe local config reached the network command: %v", statErr)
	}
}

func TestUnsafeRemoteLocalConfigClassification(t *testing.T) {
	unsafe := []string{
		"include.path",
		"includeif.gitdir:/workspace/.path",
		"extensions.worktreeconfig",
		"core.askpass",
		"core.gitproxy",
		"http.https://example.invalid/.proxy",
		"credential.helper",
		"protocol.ext.allow",
		"url.file:///tmp/unsafe.insteadof",
	}
	for _, key := range unsafe {
		t.Run(key, func(t *testing.T) {
			if !unsafeRemoteLocalConfig([]byte(key + "\x00")) {
				t.Fatalf("local transport key %q was accepted", key)
			}
		})
	}
	if unsafeRemoteLocalConfig([]byte("core.repositoryformatversion\x00user.name\x00remote.origin.url\x00push.default\x00")) {
		t.Fatal("ordinary repository metadata or an explicitly bypassed named remote was rejected")
	}
}

func TestAskPassSessionUsesPrivateTemporaryHelperAndCleansIt(t *testing.T) {
	token, err := domain.NewToken("remote-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAskPassSession(token)
	if err != nil {
		t.Fatal(err)
	}
	path := session.path
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("AskPass helper mode = %o, want 700", info.Mode().Perm())
	}
	helper, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(helper), "remote-test-secret") {
		t.Fatalf("AskPass helper contains the token: %q", helper)
	}
	session.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AskPass helper remains after Close: %v", err)
	}
}

func TestPushFenceSessionUsesPrivateTemporaryHookAndCleansIt(t *testing.T) {
	session, err := newPushFenceSession("refs/heads/main", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	path := session.path
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("push fence directory mode=%v", info.Mode().Perm())
	}
	hook, err := os.Stat(filepath.Join(path, "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	if hook.Mode().Perm() != 0o700 {
		t.Fatalf("push fence hook mode=%v", hook.Mode().Perm())
	}
	session.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("push fence remains after Close: %v", err)
	}
}

func TestRemoteFailureMappingAndUnknownMutationBoundary(t *testing.T) {
	tests := []struct {
		name      string
		failure   remoteFailure
		mutation  bool
		code      string
		kind      foundation.ErrorKind
		retryable bool
	}{
		{"authentication", remoteFailureAuth, false, domain.ErrorCodeAuthenticationFailed, foundation.ErrorPermissionDenied, false},
		{"offline", remoteFailureOffline, false, domain.ErrorCodeOffline, foundation.ErrorRetryableFailure, true},
		{"non-fast-forward", remoteFailureNonFF, true, domain.ErrorCodeNonFastForward, foundation.ErrorVersionConflict, false},
		{"unknown mutation", remoteFailureGeneric, true, domain.ErrorCodeResultUnknown, foundation.ErrorManualRecoveryRequired, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyRemoteError(&remoteCommandError{failure: test.failure}, test.mutation)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != test.code || classified.Kind != test.kind || classified.Retryable != test.retryable {
				t.Fatalf("classification = %#v", err)
			}
		})
	}
}

func TestRemotePushMarksLostResponseAsUnknownForRestartReconciliation(t *testing.T) {
	repository := newRemoteTestRepository(t)
	argsPath := filepath.Join(t.TempDir(), "push-args")
	executable := writeExecutable(t, `#!/bin/sh
case "$*" in
*"push --porcelain --no-force"*) printf '%s\n' "$@" > "$ZHIXU_TEST_PUSH_ARGS"; printf 'response lost\n' >&2; exit 1 ;;
*) exec git "$@" ;;
esac
`)
	t.Setenv("ZHIXU_TEST_PUSH_ARGS", argsPath)
	client, err := newRemoteClientForTests(New(executable), remoteWorkspaceRepository{workspace: workspacedomain.Workspace{ID: remoteTestWorkspaceID, RootPath: repository.root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Fetch(context.Background(), repository.access); err != nil {
		t.Fatal(err)
	}
	localHead := commitInRepository(t, repository.root, "local.md", "local\n")
	comparison, err := client.Compare(context.Background(), repository.access)
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := client.Push(context.Background(), application.PushCommand{
		Access: repository.access, ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
	})
	arguments, readErr := os.ReadFile(argsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if mutation.ResultKnown {
		t.Fatalf("Push() marked an interrupted response as known: %#v; Git args: %q", mutation, arguments)
	}
	requireRemoteErrorCode(t, err, domain.ErrorCodeResultUnknown, foundation.ErrorManualRecoveryRequired)
	if comparison.HeadOID != localHead {
		t.Fatalf("Push comparison HEAD = %s, want %s", comparison.HeadOID, localHead)
	}
	if !strings.Contains(string(arguments), localHead+":refs/heads/main") {
		t.Fatalf("Push() did not freeze the local source OID: %q", arguments)
	}
	if strings.Contains(string(arguments), "force-with-lease") {
		t.Fatalf("Push() used force semantics: %q", arguments)
	}
	if !strings.Contains(string(arguments), "--no-force") {
		t.Fatalf("Push() did not explicitly disable force: %q", arguments)
	}
	if !strings.Contains(string(arguments), "http.followRedirects=false") {
		t.Fatalf("Push() permits HTTP redirects: %q", arguments)
	}
}

func TestRemotePushRejectsAnyRemoteMovementWithKnownResult(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, repository remoteTestRepository, expectedRemote string) string
	}{
		{
			name: "advance",
			mutate: func(t *testing.T, repository remoteTestRepository, _ string) string {
				return commitToBareRemote(t, repository.bare, "remote-advance.md", "advance\n")
			},
		},
		{
			name: "rollback",
			mutate: func(t *testing.T, repository remoteTestRepository, expectedRemote string) string {
				parent := runRemoteGit(t, repository.bare, "rev-parse", expectedRemote+"^")
				runRemoteGit(t, repository.bare, "update-ref", "refs/heads/main", parent, expectedRemote)
				return parent
			},
		},
		{
			name: "diverge",
			mutate: func(t *testing.T, repository remoteTestRepository, _ string) string {
				return commitToBareRemote(t, repository.bare, "remote-diverge.md", "diverge\n")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newRemoteTestRepository(t)
			expectedRemote := commitToBareRemote(t, repository.bare, "expected.md", "expected\n")
			if err := repository.client.Fetch(context.Background(), repository.access); err != nil {
				t.Fatal(err)
			}
			comparison, err := repository.client.Compare(context.Background(), repository.access)
			if err != nil {
				t.Fatal(err)
			}
			mutation, err := repository.client.FastForward(context.Background(), application.FastForwardCommand{
				Access: repository.access, ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
			})
			if err != nil || !mutation.ResultKnown {
				t.Fatalf("FastForward() = %#v, %v", mutation, err)
			}
			localHead := commitInRepository(t, repository.root, "local-"+test.name+".md", "local\n")
			actualRemote := test.mutate(t, repository, expectedRemote)

			mutation, err = repository.client.Push(context.Background(), application.PushCommand{
				Access: repository.access, ExpectedHeadOID: localHead, ExpectedRemoteOID: expectedRemote,
			})
			if !mutation.ResultKnown {
				t.Fatalf("Push() outcome should be known after a server rejection: %#v", mutation)
			}
			requireRemoteErrorCode(t, err, domain.ErrorCodeRefDrift, foundation.ErrorVersionConflict)
			if got := runRemoteGit(t, repository.bare, "rev-parse", "refs/heads/main"); got != actualRemote {
				t.Fatalf("remote branch changed despite non-fast-forward rejection: got %s, want %s", got, actualRemote)
			}
			if got := runRemoteGit(t, repository.root, "rev-parse", "HEAD"); got != localHead {
				t.Fatalf("local branch changed despite rejected push: got %s, want %s", got, localHead)
			}
		})
	}
}

func TestFastForwardCASMapsRefDrift(t *testing.T) {
	err := classifyFastForwardCASError(&commandError{stderr: "fatal: cannot lock ref 'refs/heads/main': is at abc"})
	requireRemoteErrorCode(t, err, domain.ErrorCodeRefDrift, foundation.ErrorVersionConflict)
}

func newRemoteTestRepository(t *testing.T) remoteTestRepository {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	seed := filepath.Join(t.TempDir(), "seed")
	runRemoteGit(t, "", "init", "--bare", bare)
	runRemoteGit(t, "", "init", "--initial-branch=main", seed)
	configureRemoteIdentity(t, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRemoteGit(t, seed, "add", "README.md")
	runRemoteGit(t, seed, "commit", "-m", "initial")
	runRemoteGit(t, seed, "push", bare, "refs/heads/main:refs/heads/main")

	root := filepath.Join(t.TempDir(), "workspace")
	runRemoteGit(t, "", "clone", "--branch=main", bare, root)
	runRemoteGit(t, root, "remote", "remove", "origin")
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newRemoteClientForTests(New(""), remoteWorkspaceRepository{workspace: workspacedomain.Workspace{ID: remoteTestWorkspaceID, RootPath: canonical}})
	if err != nil {
		t.Fatal(err)
	}
	return remoteTestRepository{root: canonical, bare: bare, client: client, access: remoteTestAccess(t, bare)}
}

func remoteTestAccess(t *testing.T, remoteURL string) application.GitAccess {
	t.Helper()
	token, err := domain.NewToken("remote-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return application.GitAccess{WorkspaceID: remoteTestWorkspaceID, ConfigRevision: 1, RemoteURL: remoteURL, Branch: "main", Token: token}
}

func commitToBareRemote(t *testing.T, bare, name, content string) string {
	t.Helper()
	writer := filepath.Join(t.TempDir(), "writer")
	runRemoteGit(t, "", "clone", "--branch=main", bare, writer)
	configureRemoteIdentity(t, writer)
	return commitAndPushRemote(t, writer, name, content, bare)
}

func commitAndPushRemote(t *testing.T, root, name, content, bare string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runRemoteGit(t, root, "add", "--", name)
	runRemoteGit(t, root, "commit", "-m", "remote")
	head := runRemoteGit(t, root, "rev-parse", "HEAD")
	runRemoteGit(t, root, "push", bare, "refs/heads/main:refs/heads/main")
	return head
}

func commitInRepository(t *testing.T, root, name, content string) string {
	t.Helper()
	configureRemoteIdentity(t, root)
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runRemoteGit(t, root, "add", "--", name)
	runRemoteGit(t, root, "commit", "-m", "local")
	return runRemoteGit(t, root, "rev-parse", "HEAD")
}

func configureRemoteIdentity(t *testing.T, root string) {
	t.Helper()
	runRemoteGit(t, root, "config", "user.name", "Remote Test")
	runRemoteGit(t, root, "config", "user.email", "remote-test@example.invalid")
}

func runRemoteGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	if root != "" {
		command.Dir = root
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func requireRemoteErrorCode(t *testing.T, err error, code string, kind foundation.ErrorKind) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code || classified.Kind != kind {
		t.Fatalf("error = %#v, want %s/%s", err, kind, code)
	}
}
