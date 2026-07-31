package hostcontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/candidateprobe"
)

type recordedCommand struct {
	executable string
	arguments  []string
}

type fakeCommandRunner struct {
	mu       sync.Mutex
	commands []recordedCommand
	grant    Grant
	model    []byte
	failOn   string
	failCode int
}

func (runner *fakeCommandRunner) Run(_ context.Context, executable string, arguments ...string) ([]byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	copied := append([]string(nil), arguments...)
	runner.commands = append(runner.commands, recordedCommand{executable: executable, arguments: copied})
	joined := strings.Join(arguments, " ")
	if runner.failOn != "" && strings.Contains(joined, runner.failOn) {
		if runner.failCode != 0 {
			return nil, fakeProcessError(runner.failCode)
		}
		return nil, errors.New("fixture failure")
	}
	switch {
	case strings.HasSuffix(joined, "config --format json"):
		return runner.model, nil
	case strings.HasSuffix(joined, "ps --quiet app"):
		return []byte("aaaaaaaaaaaa\n"), nil
	case strings.HasSuffix(joined, "ps --quiet worker"):
		return []byte("bbbbbbbbbbbb\n"), nil
	case len(arguments) >= 2 && arguments[0] == "inspect":
		return []byte(fmt.Sprintf(`[{"Type":"bind","Source":%q,"Destination":%q,"RW":true},{"Type":"volume","Source":"secret","Destination":"/run/secret","RW":false}]`, runner.grant.Root, runner.grant.Root)), nil
	case strings.Contains(joined, " port --index 1 app 8080"):
		return []byte("127.0.0.1:49152\n"), nil
	default:
		return nil, nil
	}
}

type fakeProcessError int

func (failure fakeProcessError) Error() string { return "fixture process failed" }

func (failure fakeProcessError) ExitCode() int { return int(failure) }

func TestComposeDriverAppliesOnlyFixedArgvWithoutHostPath(t *testing.T) {
	t.Parallel()
	root := canonicalTestDirectory(t)
	grant := validatedTestGrant(t, "workspace-driver-1", root, 3)
	stateDirectory := t.TempDir()
	runner := &fakeCommandRunner{grant: grant, model: composeFixture(grant, true)}
	driver, err := NewComposeDriver(ComposeDriverOptions{
		Executable: "docker-fixture", Project: "deploy", BaseFile: "/repo/deploy/compose.yml",
		OverrideFile: filepath.Join(stateDirectory, "grant.yml"), EnvFile: "/repo/.env", Runner: runner,
	})
	if err != nil {
		t.Fatalf("NewComposeDriver() error: %v", err)
	}
	backend, err := driver.ApplyGrant(context.Background(), grant)
	if err != nil {
		t.Fatalf("ApplyGrant() error: %v", err)
	}
	if backend.String() != "http://127.0.0.1:49152" {
		t.Fatalf("backend=%s", backend)
	}
	if _, err := os.Stat(filepath.Join(stateDirectory, "grant.yml")); err != nil {
		t.Fatalf("grant override missing: %v", err)
	}

	runner.mu.Lock()
	commands := append([]recordedCommand(nil), runner.commands...)
	runner.mu.Unlock()
	if len(commands) < 10 {
		t.Fatalf("recorded %d commands, want complete lifecycle", len(commands))
	}
	for _, command := range commands {
		if command.executable != "docker-fixture" {
			t.Fatalf("unexpected executable: %s", command.executable)
		}
		for _, argument := range command.arguments {
			if strings.Contains(argument, root) {
				t.Fatalf("host path leaked to argv: %q", argument)
			}
		}
	}
	joined := make([]string, 0, len(commands))
	for _, command := range commands {
		joined = append(joined, strings.Join(command.arguments, " "))
	}
	all := strings.Join(joined, "\n")
	for _, required := range []string{
		"--profile workspace-runtime --project-name deploy",
		"up --detach --no-deps --force-recreate --wait app worker",
		"run --rm --no-deps -T firewall",
		"inspect --format {{json .Mounts}} aaaaaaaaaaaa",
	} {
		if !strings.Contains(all, required) {
			t.Fatalf("missing fixed command %q\n%s", required, all)
		}
	}
}

func TestComposeDriverRevokesAfterPostStartFailure(t *testing.T) {
	t.Parallel()
	root := canonicalTestDirectory(t)
	grant := validatedTestGrant(t, "workspace-driver-2", root, 4)
	runner := &fakeCommandRunner{grant: grant, model: composeFixture(grant, true), failOn: "run --rm --no-deps -T firewall"}
	driver, err := NewComposeDriver(ComposeDriverOptions{
		Executable: "docker", Project: "deploy", BaseFile: "/repo/compose.yml",
		OverrideFile: filepath.Join(t.TempDir(), "grant.yml"), EnvFile: "/repo/.env", Runner: runner,
	})
	if err != nil {
		t.Fatalf("NewComposeDriver() error: %v", err)
	}
	if _, err := driver.ApplyGrant(context.Background(), grant); err == nil {
		t.Fatal("ApplyGrant() succeeded after firewall failure")
	} else if fault, ok := AsFault(err); !ok || fault.Code != "WORKSPACE_RUNTIME_FIREWALL_FAILED" {
		t.Fatalf("ApplyGrant() error=%v before firewall fixture", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	all := ""
	for _, command := range runner.commands {
		all += strings.Join(command.arguments, " ") + "\n"
	}
	if !strings.Contains(all, "stop proxy app-model-relay worker-model-relay app worker") ||
		!strings.Contains(all, "rm --force --stop proxy firewall app-model-relay worker-model-relay app worker") {
		t.Fatalf("failed grant was not revoked:\n%s", all)
	}
	if _, err := os.Lstat(driver.overrideFile); !os.IsNotExist(err) {
		t.Fatalf("failed grant override remains: %v", err)
	}
}

func TestComposeDriverRevokesPartialInitialStart(t *testing.T) {
	t.Parallel()
	root := canonicalTestDirectory(t)
	grant := validatedTestGrant(t, "workspace-driver-partial", root, 5)
	runner := &fakeCommandRunner{
		grant: grant, model: composeFixture(grant, true),
		failOn: "up --detach --no-deps --force-recreate --wait app worker",
	}
	driver, err := NewComposeDriver(ComposeDriverOptions{
		Executable: "docker", Project: "deploy", BaseFile: "/repo/compose.yml",
		OverrideFile: filepath.Join(t.TempDir(), "grant.yml"), EnvFile: "/repo/.env", Runner: runner,
	})
	if err != nil {
		t.Fatalf("NewComposeDriver() error: %v", err)
	}
	if _, err := driver.ApplyGrant(context.Background(), grant); err == nil {
		t.Fatal("ApplyGrant() succeeded after partial startup failure")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	all := ""
	for _, command := range runner.commands {
		all += strings.Join(command.arguments, " ") + "\n"
	}
	if !strings.Contains(all, "rm --force --stop proxy firewall app-model-relay worker-model-relay app worker") {
		t.Fatalf("partial startup was not revoked:\n%s", all)
	}
}

func TestComposeDriverPreparesBothRealCandidateRolesWithFixedArgv(t *testing.T) {
	t.Parallel()
	root := canonicalTestDirectory(t)
	grant := validatedTestGrant(t, "workspace-driver-candidate", root, 6)
	operationID := foundation.ID("550e8400-e29b-41d4-a716-446655440000")
	stateDirectory := t.TempDir()
	runner := &fakeCommandRunner{grant: grant, model: composeFixture(grant, true)}
	driver, err := NewComposeDriver(ComposeDriverOptions{
		Executable: "docker", Project: "deploy", BaseFile: "/repo/compose.yml",
		OverrideFile: filepath.Join(stateDirectory, "grant.yml"), EnvFile: "/repo/.env", Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.PrepareGrant(context.Background(), operationID, grant, true); err != nil {
		t.Fatalf("PrepareGrant() error: %v", err)
	}

	runner.mu.Lock()
	commands := append([]recordedCommand(nil), runner.commands...)
	runner.mu.Unlock()
	all := make([]string, 0, len(commands))
	for _, command := range commands {
		joined := strings.Join(command.arguments, " ")
		all = append(all, joined)
		if strings.Contains(joined, root) {
			t.Fatalf("host path leaked to candidate argv: %s", joined)
		}
		if strings.Contains(joined, "--entrypoint git") {
			t.Fatalf("Controller invoked Git directly: %s", joined)
		}
	}
	joined := strings.Join(all, "\n")
	common := "-e " + candidateprobe.EnvOperationID + "=" + string(operationID) +
		" -e " + candidateprobe.EnvRootFingerprint + "=" + grant.RootFingerprint +
		" -e " + candidateprobe.EnvBindingVersion + "=" + fmt.Sprintf("%d", grant.BindingVersion) +
		" --entrypoint /app/zhixu-workspace-probe"
	if !strings.Contains(joined, common+" app --role=api --initialize-git") {
		t.Fatalf("API candidate command missing:\n%s", joined)
	}
	if !strings.Contains(joined, common+" worker --role=worker") {
		t.Fatalf("Worker candidate command missing:\n%s", joined)
	}
	if strings.Contains(joined, "worker --role=worker --initialize-git") {
		t.Fatalf("Worker candidate received Git initialization:\n%s", joined)
	}
}

func TestComposeDriverRejectsWhenWorkerCandidateFailsAndRemovesGrant(t *testing.T) {
	t.Parallel()
	root := canonicalTestDirectory(t)
	grant := validatedTestGrant(t, "workspace-driver-candidate-failure", root, 7)
	stateDirectory := t.TempDir()
	runner := &fakeCommandRunner{
		grant: grant, model: composeFixture(grant, true),
		failOn: "worker --role=worker",
	}
	driver, err := NewComposeDriver(ComposeDriverOptions{
		Executable: "docker", Project: "deploy", BaseFile: "/repo/compose.yml",
		OverrideFile: filepath.Join(stateDirectory, "grant.yml"), EnvFile: "/repo/.env", Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = driver.PrepareGrant(context.Background(), foundation.ID("550e8400-e29b-41d4-a716-446655440001"), grant, false)
	fault, ok := AsFault(err)
	if !ok || fault.Code != "WORKSPACE_WORKER_CANDIDATE_FAILED" {
		t.Fatalf("PrepareGrant() error=%v", err)
	}
	if _, err := os.Lstat(driver.overrideFile); !os.IsNotExist(err) {
		t.Fatalf("failed candidate grant override remains: %v", err)
	}
}

func TestComposeDriverMapsCandidateExitCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		exitCode  int
		faultCode string
		retryable bool
	}{
		{name: "permission", exitCode: candidateprobe.ExitPermission, faultCode: "WORKSPACE_RUNTIME_ROOT_PERMISSION_DENIED"},
		{name: "Git required", exitCode: candidateprobe.ExitGitRequired, faultCode: "WORKSPACE_GIT_REQUIRED"},
		{name: "external Git metadata", exitCode: candidateprobe.ExitGitMetadataOutsideRoot, faultCode: "WORKSPACE_GIT_METADATA_OUTSIDE_ROOT"},
		{name: "database", exitCode: candidateprobe.ExitDatabaseUnavailable, faultCode: "WORKSPACE_CANDIDATE_DATABASE_UNAVAILABLE", retryable: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := canonicalTestDirectory(t)
			grant := validatedTestGrant(t, fmt.Sprintf("workspace-driver-candidate-map-%d", index), root, int64(index+10))
			runner := &fakeCommandRunner{
				grant: grant, model: composeFixture(grant, true),
				failOn: "app --role=api", failCode: test.exitCode,
			}
			driver, err := NewComposeDriver(ComposeDriverOptions{
				Executable: "docker", Project: "deploy", BaseFile: "/repo/compose.yml",
				OverrideFile: filepath.Join(t.TempDir(), "grant.yml"), EnvFile: "/repo/.env", Runner: runner,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = driver.PrepareGrant(context.Background(), foundation.ID("550e8400-e29b-41d4-a716-446655440001"), grant, false)
			fault, ok := AsFault(err)
			if !ok || fault.Code != test.faultCode || fault.Retryable != test.retryable {
				t.Fatalf("PrepareGrant() error=%v fault=%+v", err, fault)
			}
		})
	}
}

func TestComposeDriverRejectsPublicBackend(t *testing.T) {
	t.Parallel()
	runner := &fakeCommandRunner{}
	driver, err := NewComposeDriver(ComposeDriverOptions{
		Executable: "docker", Project: "deploy", BaseFile: "/repo/compose.yml",
		OverrideFile: "/state/grant.yml", EnvFile: "/repo/.env", Runner: runner,
	})
	if err != nil {
		t.Fatalf("NewComposeDriver() error: %v", err)
	}
	runner.failOn = "unreachable"
	runner.model = nil
	// Override the port response by recording a purpose-specific runner.
	driver.runner = commandRunnerFunc(func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
		if strings.Contains(strings.Join(arguments, " "), " port --index 1 app 8080") {
			return []byte("0.0.0.0:8080\n"), nil
		}
		return nil, nil
	})
	if _, err := driver.CurrentBackend(context.Background()); err == nil {
		t.Fatal("CurrentBackend() accepted a public listener")
	}
}

type commandRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (function commandRunnerFunc) Run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	return function(ctx, executable, arguments...)
}

func canonicalTestDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	return root
}

func validatedTestGrant(t *testing.T, workspaceID, root string, generation int64) Grant {
	t.Helper()
	validated, err := (PathValidator{}).Validate(root)
	if err != nil {
		t.Fatalf("validate grant root: %v", err)
	}
	return Grant{
		WorkspaceID: workspaceID, Root: validated.CanonicalPath,
		RootFingerprint: validated.Fingerprint.Digest(), BindingVersion: validated.Fingerprint.BindingVersion,
		Generation: generation,
	}
}
