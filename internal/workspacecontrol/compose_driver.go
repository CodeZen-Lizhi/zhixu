package workspacecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/candidateprobe"
)

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

const runtimeCleanupTimeout = 30 * time.Second

// CommandRunner is the test seam for fixed-argv process execution.
type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// ExecRunner executes argv directly and never invokes a shell.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("fixed command failed: %w", err)
	}
	return output, nil
}

// ComposeDriverOptions fixes every Docker scope before a control operation runs.
type ComposeDriverOptions struct {
	Executable   string
	Project      string
	BaseFile     string
	OverrideFile string
	EnvFile      string
	Runner       CommandRunner
	Validator    PathValidator
}

// ComposeDriver owns exact-grant rendering, validation, startup, inspection, and revocation.
type ComposeDriver struct {
	executable   string
	project      string
	baseFile     string
	overrideFile string
	envFile      string
	runner       CommandRunner
	validator    PathValidator
}

// NewComposeDriver constructs a driver whose project, files, profile, and services cannot vary per request.
func NewComposeDriver(options ComposeDriverOptions) (*ComposeDriver, error) {
	if options.Executable == "" || options.Project == "" || options.BaseFile == "" || options.OverrideFile == "" || options.EnvFile == "" {
		return nil, errors.New("compose driver scope is incomplete")
	}
	if !grantIdentityPattern.MatchString(options.Project) {
		return nil, errors.New("compose project is invalid")
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	return &ComposeDriver{
		executable: options.Executable, project: options.Project, baseFile: options.BaseFile,
		overrideFile: options.OverrideFile, envFile: options.EnvFile, runner: options.Runner, validator: options.Validator,
	}, nil
}

// ApplyGrant validates the host identity, validates the final Compose model, and starts only fixed runtime services.
func (driver *ComposeDriver) ApplyGrant(ctx context.Context, grant Grant) (returnErr error) {
	if err := driver.prepareGrantModel(ctx, grant); err != nil {
		return err
	}

	started := false
	defer func() {
		if returnErr != nil {
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimeCleanupTimeout)
			defer cancel()
			if started {
				_ = driver.RevokeGrant(cleanupContext)
			} else {
				_ = driver.removeGrantOverride()
			}
		}
	}()
	started = true
	if _, err := driver.compose(ctx, "up", "--detach", "--no-deps", "--force-recreate", "--wait", "app", "worker"); err != nil {
		return runtimeCommandFault(err, "WORKSPACE_RUNTIME_START_FAILED")
	}
	if _, err := driver.compose(ctx, "up", "--detach", "--no-deps", "--wait", "app-model-relay", "worker-model-relay"); err != nil {
		return runtimeCommandFault(err, "WORKSPACE_RUNTIME_START_FAILED")
	}
	current, err := driver.validator.Validate(grant.Root)
	if err != nil || current.Fingerprint.Digest() != grant.RootFingerprint {
		return &ValidationError{Code: "WORKSPACE_PATH_IDENTITY_CHANGED"}
	}
	if err := driver.InspectGrant(ctx, grant); err != nil {
		return err
	}
	return nil
}

// PrepareGrant validates the exact Compose model and runs Git checks inside a
// one-off unprivileged candidate container. The host control process never
// reads or mutates Workspace contents.
func (driver *ComposeDriver) PrepareGrant(ctx context.Context, operationID foundation.ID, grant Grant, initializeGit bool) (returnErr error) {
	if err := driver.prepareGrantModel(ctx, grant); err != nil {
		return err
	}
	if parsed, err := foundation.ParseID(string(operationID)); err != nil || parsed != operationID {
		return &ValidationError{Code: "WORKSPACE_OPERATION_ID_INVALID"}
	}
	defer func() {
		if returnErr != nil {
			_ = driver.removeGrantOverride()
		}
	}()
	if err := driver.runCandidateProbe(ctx, "app", "api", operationID, grant, initializeGit); err != nil {
		return candidateProbeFault("api", err)
	}
	if err := driver.runCandidateProbe(ctx, "worker", "worker", operationID, grant, false); err != nil {
		return candidateProbeFault("worker", err)
	}
	validated, err := driver.validator.Validate(grant.Root)
	if err != nil || validated.CanonicalPath != grant.Root ||
		validated.Fingerprint.Digest() != grant.RootFingerprint {
		return &ValidationError{Code: "WORKSPACE_PATH_IDENTITY_CHANGED"}
	}
	return nil
}

func (driver *ComposeDriver) runCandidateProbe(
	ctx context.Context,
	service string,
	role string,
	operationID foundation.ID,
	grant Grant,
	initializeGit bool,
) error {
	arguments := []string{
		"run", "--rm", "--no-deps", "-T",
		"-e", candidateprobe.EnvOperationID + "=" + string(operationID),
		"-e", candidateprobe.EnvRootFingerprint + "=" + grant.RootFingerprint,
		"-e", candidateprobe.EnvBindingVersion + "=" + strconv.FormatInt(grant.BindingVersion, 10),
		"--entrypoint", "/app/zhixu-workspace-probe", service,
		"--role=" + role,
	}
	if initializeGit {
		arguments = append(arguments, "--initialize-git")
	}
	_, err := driver.compose(ctx, arguments...)
	return err
}

func candidateProbeFault(role string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch processExitCode(err) {
	case candidateprobe.ExitPathIdentity:
		return &Fault{Code: "WORKSPACE_PATH_IDENTITY_CHANGED", Message: "所选宿主机目录在验证期间发生了变化"}
	case candidateprobe.ExitPathUnavailable:
		return &Fault{Code: "WORKSPACE_RUNTIME_ROOT_UNAVAILABLE", Message: "运行容器无法访问所选宿主机目录"}
	case candidateprobe.ExitPermission:
		return &Fault{Code: "WORKSPACE_RUNTIME_ROOT_PERMISSION_DENIED", Message: "运行容器无权访问所选目录，请检查 Docker 文件共享和目录权限"}
	case candidateprobe.ExitGitRequired:
		return &Fault{Code: "WORKSPACE_GIT_REQUIRED", Message: "所选目录不是 Git 仓库；如需创建，请勾选初始化 Git"}
	case candidateprobe.ExitGitInvalid:
		return &Fault{Code: "WORKSPACE_GIT_INVALID", Message: "所选目录的 Git 仓库无法安全验证"}
	case candidateprobe.ExitGitMetadataOutsideRoot:
		return &Fault{Code: "WORKSPACE_GIT_METADATA_OUTSIDE_ROOT", Message: "Git 元数据位于所选目录之外，无法按精确目录授权"}
	case candidateprobe.ExitGitInitialize:
		return &Fault{Code: "WORKSPACE_GIT_INIT_FAILED", Message: "无法在所选目录中初始化 Git 仓库"}
	case candidateprobe.ExitProbe:
		return &Fault{Code: "WORKSPACE_RUNTIME_ROOT_PROBE_FAILED", Message: "运行容器无法完成所选目录的读写验证"}
	case candidateprobe.ExitDatabaseUnavailable:
		return &Fault{Code: "WORKSPACE_CANDIDATE_DATABASE_UNAVAILABLE", Message: "Workspace 候选运行时暂时无法连接数据库", Retryable: true}
	case candidateprobe.ExitRuntimeRegistration:
		return &Fault{Code: "WORKSPACE_CANDIDATE_REGISTRATION_FAILED", Message: "Workspace 候选运行时无法登记准备状态", Retryable: true}
	case candidateprobe.ExitConfiguration:
		return &Fault{Code: "WORKSPACE_CANDIDATE_CONFIGURATION_INVALID", Message: "Workspace 候选运行时配置无效"}
	}
	if role == "worker" {
		return &Fault{Code: "WORKSPACE_WORKER_CANDIDATE_FAILED", Message: "Worker 无法验证所选宿主机目录"}
	}
	return &Fault{Code: "WORKSPACE_API_CANDIDATE_FAILED", Message: "API 无法验证所选宿主机目录"}
}

func processExitCode(err error) int {
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode()
	}
	return -1
}

func (driver *ComposeDriver) prepareGrantModel(ctx context.Context, grant Grant) error {
	validated, err := driver.validator.Validate(grant.Root)
	if err != nil {
		return err
	}
	if validated.CanonicalPath != grant.Root {
		return &ValidationError{Code: "WORKSPACE_GRANT_ROOT_NOT_CANONICAL"}
	}
	if validated.Fingerprint.Digest() != grant.RootFingerprint {
		return &ValidationError{Code: "WORKSPACE_GRANT_FINGERPRINT_MISMATCH"}
	}
	if err := grant.validate(); err != nil {
		return err
	}
	if err := WriteGrantOverride(driver.overrideFile, grant); err != nil {
		return err
	}

	model, err := driver.compose(ctx, "config", "--format", "json")
	if err != nil {
		_ = driver.removeGrantOverride()
		return runtimeCommandFault(err, "COMPOSE_MODEL_UNAVAILABLE")
	}
	if err := ValidateGrantedComposeModel(model, grant); err != nil {
		_ = driver.removeGrantOverride()
		return err
	}
	return nil
}

// RevokeGrant removes every runtime container that could retain the Workspace bind.
func (driver *ComposeDriver) RevokeGrant(ctx context.Context) error {
	grantPresent, err := driver.grantOverridePresent()
	if err != nil {
		return err
	}
	run := driver.composeBase
	if grantPresent {
		run = driver.compose
	}
	_, stopErr := run(ctx, "stop", "app-model-relay", "worker-model-relay", "app", "worker")
	_, removeErr := run(ctx, "rm", "--force", "--stop", "app-model-relay", "worker-model-relay", "app", "worker")
	if stopErr != nil || removeErr != nil {
		if errors.Is(stopErr, context.Canceled) || errors.Is(stopErr, context.DeadlineExceeded) ||
			errors.Is(removeErr, context.Canceled) || errors.Is(removeErr, context.DeadlineExceeded) {
			return errors.Join(stopErr, removeErr)
		}
		return runtimeFault("WORKSPACE_RUNTIME_REVOKE_FAILED")
	}
	return driver.removeGrantOverride()
}

func (driver *ComposeDriver) grantOverridePresent() (bool, error) {
	info, err := os.Lstat(driver.overrideFile)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, runtimeFault("WORKSPACE_GRANT_REVOKE_FAILED")
	}
	return true, nil
}

func (driver *ComposeDriver) removeGrantOverride() error {
	info, err := os.Lstat(driver.overrideFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return runtimeFault("WORKSPACE_GRANT_REVOKE_FAILED")
	}
	if err := os.Remove(driver.overrideFile); err != nil {
		return runtimeFault("WORKSPACE_GRANT_REVOKE_FAILED")
	}
	return nil
}

// InspectGrant proves that both live containers retain exactly the expected bind identity.
func (driver *ComposeDriver) InspectGrant(ctx context.Context, grant Grant) error {
	for _, service := range []string{"app", "worker"} {
		output, err := driver.compose(ctx, "ps", "--quiet", service)
		if err != nil {
			return runtimeCommandFault(err, "WORKSPACE_MOUNT_INSPECTION_FAILED")
		}
		containerID := strings.TrimSpace(string(output))
		if !containerIDPattern.MatchString(containerID) {
			return runtimeFault("WORKSPACE_MOUNT_INSPECTION_FAILED")
		}
		mountOutput, err := driver.runner.Run(ctx, driver.executable, "inspect", "--format", "{{json .Mounts}}", containerID)
		if err != nil {
			return runtimeCommandFault(err, "WORKSPACE_MOUNT_INSPECTION_FAILED")
		}
		if err := validateInspectedMounts(mountOutput, grant); err != nil {
			return err
		}
	}
	return nil
}

func (driver *ComposeDriver) compose(ctx context.Context, arguments ...string) ([]byte, error) {
	fixed := []string{
		"compose", "--profile", "workspace-runtime", "--project-name", driver.project,
		"-f", driver.baseFile, "-f", driver.overrideFile, "--env-file", driver.envFile,
	}
	return driver.runner.Run(ctx, driver.executable, append(fixed, arguments...)...)
}

func (driver *ComposeDriver) composeBase(ctx context.Context, arguments ...string) ([]byte, error) {
	fixed := []string{
		"compose", "--profile", "workspace-runtime", "--project-name", driver.project,
		"-f", driver.baseFile, "--env-file", driver.envFile,
	}
	return driver.runner.Run(ctx, driver.executable, append(fixed, arguments...)...)
}

type inspectedMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

func validateInspectedMounts(document []byte, grant Grant) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	var mounts []inspectedMount
	if err := decoder.Decode(&mounts); err != nil {
		return runtimeFault("WORKSPACE_MOUNT_INSPECTION_FAILED")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runtimeFault("WORKSPACE_MOUNT_INSPECTION_FAILED")
	}
	workspaceBinds := 0
	for _, mount := range mounts {
		if containsDockerSocket(mount.Source) || containsDockerSocket(mount.Destination) {
			return &ValidationError{Code: "DOCKER_SOCKET_MOUNT_FORBIDDEN"}
		}
		if mount.Type != "bind" {
			continue
		}
		workspaceBinds++
		if mount.Source != grant.Root || mount.Destination != grant.Root || !mount.RW {
			return &ValidationError{Code: "WORKSPACE_BIND_IDENTITY_MISMATCH"}
		}
	}
	if workspaceBinds != 1 {
		return &ValidationError{Code: "WORKSPACE_BIND_COUNT_INVALID"}
	}
	return nil
}

func runtimeFault(code string) error {
	return &Fault{Code: code, Message: "Workspace 运行时操作失败", Retryable: true}
}

func runtimeCommandFault(err error, code string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return runtimeFault(code)
}
