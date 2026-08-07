//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/hostcontroller"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestComposeRAGWorkspaceSwitchExternalFixture applies the same exact-root
// Coordinator and ComposeDriver path used by the native Host Controller.
func TestComposeRAGWorkspaceSwitchExternalFixture(t *testing.T) {
	if os.Getenv("ZHIXU_COMPOSE_RAG_WORKSPACE_SWITCH") != "1" {
		t.Skip("set ZHIXU_COMPOSE_RAG_WORKSPACE_SWITCH=1 for the external Compose fixture")
	}
	databaseURL := requireComposeRAGEnvironment(t, "ZHIXU_TEST_DATABASE_URL")
	project := requireComposeRAGEnvironment(t, "ZHIXU_TEST_COMPOSE_PROJECT")
	composeFile := requireComposeRAGEnvironment(t, "ZHIXU_TEST_COMPOSE_FILE")
	grantOverride := requireComposeRAGEnvironment(t, "ZHIXU_TEST_GRANT_OVERRIDE")
	environmentFile := requireComposeRAGEnvironment(t, "ZHIXU_TEST_COMPOSE_ENV_FILE")
	workspaceRoot := requireComposeRAGEnvironment(t, "ZHIXU_TEST_WORKSPACE_ROOT")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("open external Compose database")
	}
	defer pool.Close()
	repository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal("create Workspace control repository")
	}
	control, err := workspaceapplication.NewControlService(
		repository, repository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal("create Workspace control service")
	}
	ownerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal("create Workspace controller owner")
	}
	driver, err := hostcontroller.NewComposeDriver(hostcontroller.ComposeDriverOptions{
		Executable: "docker", Project: project, BaseFile: composeFile,
		OverrideFile: grantOverride, EnvFile: environmentFile,
		Validator: hostcontroller.PathValidator{},
	})
	if err != nil {
		t.Fatal("create Workspace Compose driver")
	}
	runtime := &composeRAGRecordingRuntime{driver: driver, failures: make(chan string, 1)}
	coordinator, err := hostcontroller.NewCoordinator(hostcontroller.CoordinatorOptions{
		Service: control, Runtime: runtime, Validator: hostcontroller.PathValidator{}, OwnerID: ownerID,
	})
	if err != nil {
		t.Fatal("create Workspace switch coordinator")
	}
	defer coordinator.Close()

	validated, err := (hostcontroller.PathValidator{}).Validate(workspaceRoot)
	if err != nil || validated.CanonicalPath != workspaceRoot {
		t.Fatal("validate exact external Workspace root")
	}
	state, err := coordinator.State(ctx)
	if err != nil {
		t.Fatal("read initial Workspace control state")
	}
	controllerID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal("create Workspace controller instance")
	}
	requestHash := fmt.Sprintf("%x", sha256.Sum256([]byte(project+"\x00"+workspaceRoot)))
	operation, err := coordinator.BeginSwitch(ctx, hostcontroller.SwitchCommand{
		ExpectedStateVersion: state.StateVersion,
		ControllerInstanceID: string(controllerID),
		IdempotencyKey:       "compose-rag-workspace-" + project,
		RequestHash:          requestHash,
		TargetKind:           "new",
		Name:                 "Compose RAG Smoke",
		RootPath:             workspaceRoot,
		RootFingerprint:      validated.Fingerprint.Digest(),
		BindingVersion:       validated.Fingerprint.BindingVersion,
		InitializeGit:        false,
	})
	if err != nil {
		t.Fatal("begin external Workspace switch")
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("external Workspace switch timed out")
		case code := <-runtime.failures:
			t.Fatalf("external Workspace runtime failed: %s", code)
		case <-ticker.C:
			operation, err = coordinator.Operation(ctx, operation.OperationID)
			if err != nil {
				t.Fatal("read external Workspace switch")
			}
			if operation.Result == "" {
				continue
			}
			if operation.Result != "succeeded" {
				t.Fatalf("external Workspace switch failed: %s", operation.ErrorCode)
			}
			state, err = coordinator.State(ctx)
			if err != nil || state.ActiveWorkspace == nil || state.ActiveWorkspace.RootPath != workspaceRoot ||
				state.Runtime.Status != hostcontroller.RuntimeReady {
				t.Fatal("external Workspace grant did not become ready")
			}
			t.Logf("active_workspace_id=%s", state.ActiveWorkspace.WorkspaceID)
			return
		}
	}
}

type composeRAGRecordingRuntime struct {
	driver   *hostcontroller.ComposeDriver
	failures chan string
}

func (runtime *composeRAGRecordingRuntime) PrepareGrant(
	ctx context.Context,
	operationID foundation.ID,
	grant hostcontroller.Grant,
	initializeGit bool,
) error {
	err := runtime.driver.PrepareGrant(ctx, operationID, grant, initializeGit)
	runtime.record(err)
	return err
}

func (runtime *composeRAGRecordingRuntime) ApplyGrant(ctx context.Context, grant hostcontroller.Grant) (*url.URL, error) {
	backend, err := runtime.driver.ApplyGrant(ctx, grant)
	runtime.record(err)
	return backend, err
}

func (runtime *composeRAGRecordingRuntime) RevokeGrant(ctx context.Context) error {
	err := runtime.driver.RevokeGrant(ctx)
	runtime.record(err)
	return err
}

func (runtime *composeRAGRecordingRuntime) CurrentBackend(ctx context.Context) (*url.URL, error) {
	return runtime.driver.CurrentBackend(ctx)
}

func (runtime *composeRAGRecordingRuntime) record(err error) {
	if err == nil {
		return
	}
	code := err.Error()
	if fault, ok := hostcontroller.AsFault(err); ok {
		code = fault.Code
	}
	select {
	case runtime.failures <- code:
	default:
	}
}

func requireComposeRAGEnvironment(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}
