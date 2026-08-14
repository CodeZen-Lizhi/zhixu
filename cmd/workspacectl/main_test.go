package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspacecontrol"
)

func TestParseConfigReconcile(t *testing.T) {
	config, err := parseConfig([]string{
		"reconcile", "--compose-file=/repo/compose.yml", "--env-file=/repo/.env",
		"--grant-override=/repo/.zhixu/workspace-grant.yml", "--database-url-fd=3",
		"--control-instance-id=550e8400-e29b-41d4-a716-446655440010",
	})
	if err != nil {
		t.Fatalf("parseConfig() error=%v", err)
	}
	if config.action != "reconcile" || config.composeProject != defaultComposeProject ||
		config.dockerExecutable != defaultDockerCommand || config.databaseURLFD != 3 || config.timeout != defaultTimeout ||
		config.controlInstanceID != foundation.ID("550e8400-e29b-41d4-a716-446655440010") {
		t.Fatalf("parseConfig()=%#v", config)
	}
}

func TestParseConfigSwitch(t *testing.T) {
	config, err := parseConfig([]string{
		"switch", "--compose-file=/repo/compose.yml", "--env-file=/repo/.env",
		"--grant-override=/repo/.zhixu/workspace-grant.yml", "--database-url-fd=4",
		"--control-instance-id=550e8400-e29b-41d4-a716-446655440011",
		"--workspace-root=/tmp/Knowledge Base", "--workspace-name=Knowledge", "--idempotency-key=switch-a",
		"--initialize-git", "--timeout=5m", "--compose-project=zhixu-test", "--docker-executable=docker-fixture",
	})
	if err != nil {
		t.Fatalf("parseConfig() error=%v", err)
	}
	if config.action != "switch" || config.workspaceRoot != "/tmp/Knowledge Base" ||
		config.workspaceName != "Knowledge" || config.idempotencyKey != "switch-a" || !config.initializeGit ||
		config.timeout != 5*time.Minute || config.composeProject != "zhixu-test" || config.dockerExecutable != "docker-fixture" {
		t.Fatalf("parseConfig()=%#v", config)
	}
}

func TestParseConfigRebindRequiresExactConfirmationAndIdentity(t *testing.T) {
	arguments := []string{
		"rebind", "--compose-file=/repo/compose.yml", "--env-file=/repo/.env",
		"--grant-override=/repo/.zhixu/workspace-grant.yml", "--database-url-fd=4",
		"--control-instance-id=550e8400-e29b-41d4-a716-446655440011",
		"--workspace-root=/tmp/Knowledge Base", "--workspace-id=550e8400-e29b-41d4-a716-446655440012",
		"--expected-root-fingerprint=" + strings.Repeat("a", 64), "--idempotency-key=rebind-a", "--confirm=REBIND",
	}
	config, err := parseConfig(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if config.action != "rebind" || config.workspaceID != "550e8400-e29b-41d4-a716-446655440012" ||
		config.expectedFingerprint != strings.Repeat("a", 64) || config.confirmation != "REBIND" {
		t.Fatalf("parseConfig()=%#v", config)
	}
	for _, replacement := range []string{"", "rebind", "DELETE"} {
		candidate := append([]string(nil), arguments...)
		candidate[len(candidate)-1] = "--confirm=" + replacement
		if _, err := parseConfig(candidate); err == nil {
			t.Fatalf("parseConfig accepted confirmation %q", replacement)
		}
	}
}

func TestParseConfigRejectsIncompleteOrUnsafeArguments(t *testing.T) {
	validCommon := []string{
		"--compose-file=/repo/compose.yml", "--env-file=/repo/.env",
		"--grant-override=/repo/.zhixu/workspace-grant.yml", "--database-url-fd=3",
		"--control-instance-id=550e8400-e29b-41d4-a716-446655440012",
	}
	tests := [][]string{
		nil,
		{"serve"},
		append([]string{"reconcile"}, validCommon[:4]...),
		append([]string{"reconcile", "--control-instance-id=not-a-uuid"}, validCommon[:4]...),
		append([]string{"reconcile", "--compose-file=relative.yml"}, validCommon[1:]...),
		append([]string{"switch"}, validCommon...),
		append(append([]string{"switch"}, validCommon...), "--workspace-root=/tmp/workspace"),
		append(append([]string{"switch"}, validCommon...), "--workspace-root=/tmp/workspace", "--idempotency-key=key", "extra"),
	}
	for _, arguments := range tests {
		if _, err := parseConfig(arguments); err == nil {
			t.Fatalf("parseConfig(%q) succeeded", arguments)
		}
	}
}

func TestValidateConfigFilesRequiresProtectedStateAndEnvironment(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	composeFile := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environmentFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(environmentFile, []byte("VALUE=fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := commandConfig{
		composeFile: composeFile, environmentFile: environmentFile,
		grantOverride: filepath.Join(stateDirectory, "workspace-grant.yml"),
	}
	if err := validateConfigFiles(config); err != nil {
		t.Fatalf("validateConfigFiles() error=%v", err)
	}
	if err := os.Chmod(environmentFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateConfigFiles(config); err == nil {
		t.Fatal("validateConfigFiles() accepted a permissive environment file")
	}
}

func TestReadDatabaseURL(t *testing.T) {
	for _, value := range []string{
		"postgres://user:secret@127.0.0.1/zhixu",
		"postgresql://user:secret@127.0.0.1/zhixu\n",
	} {
		got, err := readDatabaseURL(strings.NewReader(value))
		if err != nil || !strings.HasPrefix(got, "postgres") {
			t.Fatalf("readDatabaseURL(%q)=%q error=%v", value, got, err)
		}
	}
	for _, value := range []string{"", "http://127.0.0.1/db", " postgres://db", "postgres://db\nextra"} {
		if _, err := readDatabaseURL(strings.NewReader(value)); err == nil {
			t.Fatalf("readDatabaseURL(%q) succeeded", value)
		}
	}
}

func TestCommandResultsExposeSelectionBindingWithoutBackendURL(t *testing.T) {
	workspace := workspacedomain.Workspace{
		ID: foundation.ID("550e8400-e29b-41d4-a716-446655440001"), RootPath: "/tmp/knowledge",
		RootFingerprint: strings.Repeat("a", 64), BindingVersion: 1,
	}
	operation := workspacedomain.SwitchOperation{
		ID:              foundation.ID("550e8400-e29b-41d4-a716-446655440002"),
		GrantGeneration: 7, Result: workspacedomain.SwitchResultSucceeded,
	}
	result := switchResult(workspacecontrol.SwitchOutcome{
		Operation: operation, Workspace: workspace, GrantGeneration: operation.GrantGeneration, Changed: true,
	})
	var output bytes.Buffer
	if err := writeResult(&output, result); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["canonical_root"] != workspace.RootPath || decoded["workspace_id"] != string(workspace.ID) ||
		decoded["operation_result"] != string(workspacedomain.SwitchResultSucceeded) || decoded["grant_generation"] != float64(7) {
		t.Fatalf("result=%v", decoded)
	}
	for _, forbidden := range []string{"backend_url", "port", "listen_address"} {
		if _, found := decoded[forbidden]; found {
			t.Fatalf("result contains %q: %v", forbidden, decoded)
		}
	}
}

func TestUnchangedSwitchResultReportsCurrentGrantGeneration(t *testing.T) {
	result := switchResult(workspacecontrol.SwitchOutcome{
		Workspace: workspacedomain.Workspace{
			ID: foundation.ID("550e8400-e29b-41d4-a716-446655440004"), RootPath: "/tmp/knowledge",
		},
		GrantGeneration: 9,
	})
	if result.Changed || result.Status != "reconciled" || result.GrantGeneration != 9 || result.OperationID != "" {
		t.Fatalf("switchResult()=%#v", result)
	}
}

func TestRebindResultReportsPersistedBindingGeneration(t *testing.T) {
	result := rebindResult(workspacedomain.WorkspaceBindingMigrationResult{
		Workspace: workspacedomain.Workspace{
			ID: "550e8400-e29b-41d4-a716-446655440014", RootPath: "/tmp/knowledge",
			RootFingerprint: strings.Repeat("b", 64), BindingVersion: 2,
		},
		Changed: true,
	})
	if result.Action != "rebind" || result.Status != "rebound" || !result.Changed || result.BindingVersion != 2 || result.GrantGeneration != 0 {
		t.Fatalf("rebindResult()=%#v", result)
	}
}

func TestFailureOutputDoesNotExposeUnderlyingError(t *testing.T) {
	fault := failureFromError(errors.New("postgres://user:secret@127.0.0.1/private"))
	var output bytes.Buffer
	writeFailure(&output, fault)
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "/private") {
		t.Fatalf("failure leaked dependency detail: %s", output.String())
	}
}

func TestFailureFromErrorPreservesCancelledOperationID(t *testing.T) {
	operationID := "550e8400-e29b-41d4-a716-446655440003"
	fault := failureFromError(errors.Join(
		&workspacecontrol.Fault{Code: "WORKSPACE_RECOVERY_INCOMPLETE", OperationID: operationID},
		context.Canceled,
	))
	if fault.Code != "WORKSPACE_CONTROL_CANCELLED" || fault.OperationID != operationID || fault.Retryable {
		t.Fatalf("failureFromError()=%#v", fault)
	}
}
