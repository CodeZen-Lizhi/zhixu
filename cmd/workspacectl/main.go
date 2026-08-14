package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspacecontrol"
)

const (
	defaultComposeProject = "zhixu"
	defaultDockerCommand  = "docker"
	defaultTimeout        = 30 * time.Minute
	databaseOpenTimeout   = 10 * time.Second
	databasePingTimeout   = 5 * time.Second
	resultSchema          = "workspace-control-result/v1"
)

var errUsage = errors.New("invalid workspacectl arguments")

type commandConfig struct {
	action              string
	composeFile         string
	environmentFile     string
	grantOverride       string
	composeProject      string
	dockerExecutable    string
	databaseURLFD       int
	controlInstanceID   foundation.ID
	timeout             time.Duration
	workspaceRoot       string
	workspaceName       string
	workspaceID         foundation.ID
	expectedFingerprint string
	idempotencyKey      string
	confirmation        string
	initializeGit       bool
}

type commandResult struct {
	Schema          string `json:"schema"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	Changed         bool   `json:"changed"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	CanonicalRoot   string `json:"canonical_root,omitempty"`
	RootFingerprint string `json:"root_fingerprint,omitempty"`
	BindingVersion  int64  `json:"binding_version,omitempty"`
	GrantGeneration int64  `json:"grant_generation,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
	OperationResult string `json:"operation_result,omitempty"`
}

type commandFailure struct {
	Schema      string `json:"schema"`
	ErrorCode   string `json:"error_code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	OperationID string `json:"operation_id,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	config, err := parseConfig(arguments)
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_ARGUMENTS_INVALID", Message: "workspacectl 参数无效",
		})
		return 2
	}
	if err := validateConfigFiles(config); err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_INPUT_UNSAFE", Message: "workspacectl 输入文件不安全",
		})
		return 2
	}
	databaseURL, err := readDatabaseURLDescriptor(config.databaseURLFD)
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_DATABASE_CREDENTIAL_INVALID", Message: "数据库凭据无效",
		})
		return 2
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	operationContext, cancelOperation := context.WithTimeout(signalContext, config.timeout)
	defer cancelOperation()

	openContext, cancelOpen := context.WithTimeout(operationContext, databaseOpenTimeout)
	database, err := platformpostgres.Open(openContext, databaseURL, 4, 1)
	cancelOpen()
	databaseURL = ""
	if err != nil {
		if operationContext.Err() != nil {
			writeFailure(stderr, failureFromError(operationContext.Err()))
			return 1
		}
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_DATABASE_UNAVAILABLE", Message: "Workspace 控制数据库不可用", Retryable: true,
		})
		return 1
	}
	defer database.Close()
	pingContext, cancelPing := context.WithTimeout(operationContext, databasePingTimeout)
	err = database.Ping(pingContext)
	cancelPing()
	if err != nil {
		if operationContext.Err() != nil {
			writeFailure(stderr, failureFromError(operationContext.Err()))
			return 1
		}
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_DATABASE_UNAVAILABLE", Message: "Workspace 控制数据库不可用", Retryable: true,
		})
		return 1
	}

	auditStore, err := auditpostgres.NewStore(database.DB())
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_REPOSITORY_UNAVAILABLE", Message: "Workspace 控制存储不可用", Retryable: true,
		})
		return 1
	}
	repository, err := workspacepostgres.NewRepository(database.DB(), workspacepostgres.WithAuditAppender(auditStore))
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_REPOSITORY_UNAVAILABLE", Message: "Workspace 控制存储不可用", Retryable: true,
		})
		return 1
	}
	control, err := workspaceapplication.NewControlService(
		repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_SERVICE_UNAVAILABLE", Message: "Workspace 控制服务不可用", Retryable: true,
		})
		return 1
	}
	if config.action == "rebind" {
		validated, validationErr := (workspacecontrol.PathValidator{}).Validate(config.workspaceRoot)
		if validationErr != nil || validated.CanonicalPath != config.workspaceRoot {
			if validationErr == nil {
				validationErr = &workspacecontrol.Fault{Code: "WORKSPACE_PATH_INVALID", Message: "Workspace 路径无效"}
			}
			writeFailure(stderr, failureFromError(validationErr))
			return 1
		}
		outcome, rebindErr := control.RebindWorkspace(operationContext, workspaceapplication.RebindWorkspaceCommand{
			ControllerInstanceID: config.controlInstanceID, WorkspaceID: config.workspaceID,
			CanonicalRoot: validated.CanonicalPath, OldRootFingerprint: config.expectedFingerprint,
			NewRootFingerprint: validated.Fingerprint.Digest(), IdempotencyKey: config.idempotencyKey,
		})
		if rebindErr != nil {
			writeFailure(stderr, failureFromError(rebindErr))
			return 1
		}
		if err := writeResult(stdout, rebindResult(outcome)); err != nil {
			writeFailure(stderr, &workspacecontrol.Fault{
				Code: "WORKSPACE_CONTROL_RESULT_UNAVAILABLE", Message: "Workspace 控制结果无法输出", Retryable: true,
			})
			return 1
		}
		return 0
	}
	driver, err := workspacecontrol.NewComposeDriver(workspacecontrol.ComposeDriverOptions{
		Executable: config.dockerExecutable, Project: config.composeProject, BaseFile: config.composeFile,
		OverrideFile: config.grantOverride, EnvFile: config.environmentFile, Validator: workspacecontrol.PathValidator{},
	})
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_RUNTIME_INVALID", Message: "Workspace 运行时配置无效",
		})
		return 2
	}
	leaseOwnerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_IDENTITY_UNAVAILABLE", Message: "Workspace 控制身份不可用", Retryable: true,
		})
		return 1
	}
	coordinator, err := workspacecontrol.NewCoordinator(workspacecontrol.CoordinatorOptions{
		Service: control, Runtime: driver, Validator: workspacecontrol.PathValidator{},
		ControlInstanceID: config.controlInstanceID, LeaseOwnerID: leaseOwnerID,
	})
	if err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_COORDINATOR_UNAVAILABLE", Message: "Workspace 协调器不可用", Retryable: true,
		})
		return 1
	}

	var result commandResult
	switch config.action {
	case "reconcile":
		if err := coordinator.Reconcile(operationContext); err != nil {
			writeFailure(stderr, failureFromError(err))
			return 1
		}
		snapshot, snapshotErr := control.Snapshot(operationContext, 0)
		if snapshotErr != nil {
			writeFailure(stderr, failureFromError(snapshotErr))
			return 1
		}
		result = reconcileResult(snapshot)
	case "switch":
		outcome, switchErr := coordinator.Switch(operationContext, workspacecontrol.SwitchCommand{
			Name: config.workspaceName, RootPath: config.workspaceRoot,
			IdempotencyKey: config.idempotencyKey, InitializeGit: config.initializeGit,
		})
		if switchErr != nil {
			writeFailure(stderr, failureFromError(switchErr))
			return 1
		}
		result = switchResult(outcome)
	default:
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_ACTION_INVALID", Message: "Workspace 控制操作无效",
		})
		return 2
	}
	if err := writeResult(stdout, result); err != nil {
		writeFailure(stderr, &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_RESULT_UNAVAILABLE", Message: "Workspace 控制结果无法输出", Retryable: true,
		})
		return 1
	}
	return 0
}

func parseConfig(arguments []string) (commandConfig, error) {
	if len(arguments) == 0 {
		return commandConfig{}, errUsage
	}
	config := commandConfig{
		action: arguments[0], composeProject: defaultComposeProject,
		dockerExecutable: defaultDockerCommand, databaseURLFD: -1, timeout: defaultTimeout,
	}
	flags := flag.NewFlagSet("zhixu-workspacectl "+config.action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.composeFile, "compose-file", "", "base Compose file")
	flags.StringVar(&config.environmentFile, "env-file", "", "protected Compose environment file")
	flags.StringVar(&config.grantOverride, "grant-override", "", "generated exact-grant override")
	flags.StringVar(&config.composeProject, "compose-project", defaultComposeProject, "fixed Compose project")
	flags.StringVar(&config.dockerExecutable, "docker-executable", defaultDockerCommand, "Docker CLI executable")
	flags.IntVar(&config.databaseURLFD, "database-url-fd", -1, "inherited database URL file descriptor")
	var controlInstance string
	flags.StringVar(&controlInstance, "control-instance-id", "", "stable launcher control instance UUID")
	flags.DurationVar(&config.timeout, "timeout", defaultTimeout, "one-shot operation timeout")
	if config.action == "switch" {
		flags.StringVar(&config.workspaceRoot, "workspace-root", "", "host Workspace root")
		flags.StringVar(&config.workspaceName, "workspace-name", "", "Workspace display name")
		flags.StringVar(&config.idempotencyKey, "idempotency-key", "", "stable switch idempotency key")
		flags.BoolVar(&config.initializeGit, "initialize-git", false, "initialize Git when the root is not a repository")
	} else if config.action == "rebind" {
		var workspaceID string
		flags.StringVar(&config.workspaceRoot, "workspace-root", "", "host Workspace root")
		flags.StringVar(&workspaceID, "workspace-id", "", "selected Workspace UUID")
		flags.StringVar(&config.expectedFingerprint, "expected-root-fingerprint", "", "selected root fingerprint")
		flags.StringVar(&config.idempotencyKey, "idempotency-key", "", "stable rebind idempotency key")
		flags.StringVar(&config.confirmation, "confirm", "", "explicit rebind confirmation")
	} else if config.action != "reconcile" {
		return commandConfig{}, errUsage
	}
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return commandConfig{}, errUsage
	}
	if config.composeFile == "" || config.environmentFile == "" || config.grantOverride == "" ||
		config.composeProject == "" || config.dockerExecutable == "" || config.databaseURLFD < 3 ||
		config.timeout < time.Second || config.timeout > defaultTimeout {
		return commandConfig{}, errUsage
	}
	parsedControlInstance, err := foundation.ParseID(controlInstance)
	if err != nil || string(parsedControlInstance) != controlInstance {
		return commandConfig{}, errUsage
	}
	config.controlInstanceID = parsedControlInstance
	for _, path := range []string{config.composeFile, config.environmentFile, config.grantOverride} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return commandConfig{}, errUsage
		}
	}
	if config.action == "switch" && (config.workspaceRoot == "" || config.idempotencyKey == "") {
		return commandConfig{}, errUsage
	}
	if config.action == "rebind" {
		parsedWorkspaceID, parseErr := foundation.ParseID(flags.Lookup("workspace-id").Value.String())
		if parseErr != nil || string(parsedWorkspaceID) != flags.Lookup("workspace-id").Value.String() ||
			config.workspaceRoot == "" || config.idempotencyKey == "" || config.confirmation != "REBIND" ||
			len(config.expectedFingerprint) != 64 {
			return commandConfig{}, errUsage
		}
		config.workspaceID = parsedWorkspaceID
	}
	return config, nil
}

func validateConfigFiles(config commandConfig) error {
	if err := workspacecontrol.ValidateStateDirectory(filepath.Dir(config.grantOverride)); err != nil {
		return err
	}
	if err := validateRegularFile(config.composeFile); err != nil {
		return err
	}
	if err := validateProtectedFile(config.environmentFile); err != nil {
		return err
	}
	return validateOptionalProtectedFile(config.grantOverride)
}

func validateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("input is not a regular file")
	}
	return nil
}

func validateProtectedFile(path string) error {
	if err := validateRegularFile(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return errors.New("protected input permissions are invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("protected input owner is invalid")
	}
	return nil
}

func validateOptionalProtectedFile(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return validateProtectedFile(path)
}

func readDatabaseURLDescriptor(fileDescriptor int) (string, error) {
	file := os.NewFile(uintptr(fileDescriptor), "workspace-control-database")
	if file == nil {
		return "", errors.New("database descriptor is invalid")
	}
	defer file.Close()
	return readDatabaseURL(file)
}

func readDatabaseURL(reader io.Reader) (string, error) {
	if reader == nil {
		return "", errors.New("database credential reader is unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 4098))
	if err != nil || len(raw) > 4097 {
		return "", errors.New("database credential could not be read")
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if value == "" || len(value) > 4096 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("database credential is invalid")
	}
	if !strings.HasPrefix(value, "postgres://") && !strings.HasPrefix(value, "postgresql://") {
		return "", errors.New("database credential scheme is invalid")
	}
	return value, nil
}

func reconcileResult(snapshot workspacedomain.ControlSnapshot) commandResult {
	result := commandResult{
		Schema: resultSchema, Action: "reconcile", Status: "idle", Changed: false,
		GrantGeneration: snapshot.State.GrantGeneration,
	}
	if snapshot.Active == nil {
		return result
	}
	result.Status = "reconciled"
	result.WorkspaceID = string(snapshot.Active.ID)
	result.CanonicalRoot = snapshot.Active.RootPath
	result.RootFingerprint = snapshot.Active.RootFingerprint
	result.BindingVersion = snapshot.Active.BindingVersion
	return result
}

func switchResult(outcome workspacecontrol.SwitchOutcome) commandResult {
	result := commandResult{
		Schema: resultSchema, Action: "switch", Status: "reconciled", Changed: outcome.Changed,
		WorkspaceID: string(outcome.Workspace.ID), CanonicalRoot: outcome.Workspace.RootPath,
		RootFingerprint: outcome.Workspace.RootFingerprint, BindingVersion: outcome.Workspace.BindingVersion,
		GrantGeneration: outcome.GrantGeneration,
	}
	if !outcome.Changed {
		return result
	}
	result.Status = "switched"
	result.OperationID = string(outcome.Operation.ID)
	result.OperationResult = string(outcome.Operation.Result)
	return result
}

func rebindResult(outcome workspacedomain.WorkspaceBindingMigrationResult) commandResult {
	status := "reconciled"
	if outcome.Changed {
		status = "rebound"
	}
	return commandResult{
		Schema: resultSchema, Action: "rebind", Status: status, Changed: outcome.Changed,
		WorkspaceID: string(outcome.Workspace.ID), CanonicalRoot: outcome.Workspace.RootPath,
		RootFingerprint: outcome.Workspace.RootFingerprint, BindingVersion: outcome.Workspace.BindingVersion,
	}
}

func failureFromError(err error) *workspacecontrol.Fault {
	operationID := ""
	if fault, ok := workspacecontrol.AsFault(err); ok {
		operationID = fault.OperationID
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &workspacecontrol.Fault{
			Code: "WORKSPACE_CONTROL_CANCELLED", Message: "Workspace 控制操作已取消",
			Retryable: false, OperationID: operationID,
		}
	}
	if fault, ok := workspacecontrol.AsFault(err); ok {
		return fault
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return &workspacecontrol.Fault{
			Code: classified.Code, Message: "Workspace 控制操作失败", Retryable: classified.Retryable,
		}
	}
	return &workspacecontrol.Fault{
		Code: "WORKSPACE_CONTROL_UNAVAILABLE", Message: "Workspace 控制操作失败", Retryable: true,
	}
}

func writeResult(writer io.Writer, result commandResult) error {
	if writer == nil {
		return errors.New("result writer is unavailable")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func writeFailure(writer io.Writer, fault *workspacecontrol.Fault) {
	if writer == nil {
		return
	}
	if fault == nil {
		fault = &workspacecontrol.Fault{Code: "WORKSPACE_CONTROL_UNAVAILABLE", Message: "Workspace 控制操作失败", Retryable: true}
	}
	message := fault.Message
	if message == "" {
		message = "Workspace 控制操作失败"
	}
	_ = json.NewEncoder(writer).Encode(commandFailure{
		Schema: resultSchema, ErrorCode: fault.Code, Message: message,
		Retryable: fault.Retryable, OperationID: fault.OperationID,
	})
}
