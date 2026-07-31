package hostcontroller

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWriteGrantOverrideUsesExactStructuredBind(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	destination := filepath.Join(directory, "grant.yml")
	root := filepath.Join(directory, "selected root:with-special")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create grant root: %v", err)
	}
	grant := validatedTestGrant(t, "workspace-1", root, 9)
	root = grant.Root
	if err := WriteGrantOverride(destination, grant); err != nil {
		t.Fatalf("WriteGrantOverride() error: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("stat override: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("override mode=%o, want 600", info.Mode().Perm())
	}
	document, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	var decoded grantOverride
	if err := yaml.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode override: %v", err)
	}
	for _, role := range []string{"app", "worker"} {
		service := decoded.Services[role]
		if service.Environment[grantWorkspaceIDEnv] != grant.WorkspaceID || service.Environment[grantRootEnv] != root || service.Environment[grantGenerationEnv] != "9" {
			t.Fatalf("%s environment mismatch: %+v", role, service.Environment)
		}
		if len(service.Volumes) != 1 || service.Volumes[0].Source != root || service.Volumes[0].Target != root || service.Volumes[0].Bind.CreateHostPath {
			t.Fatalf("%s mount mismatch: %+v", role, service.Volumes)
		}
	}
	if strings.Contains(string(document), "${") || strings.Contains(string(document), "ZHIXU_WORKSPACE_ROOT") {
		t.Fatal("override contains shell interpolation or legacy grant input")
	}
}

func TestComposeModelValidationSeparatesBaseAndGrant(t *testing.T) {
	t.Parallel()
	grant := Grant{
		WorkspaceID: "workspace-1", Root: "/tmp/zhixu-model-contract",
		RootFingerprint: strings.Repeat("a", 64), BindingVersion: 1, Generation: 3,
	}
	base := composeFixture(grant, false)
	if err := ValidateBaseComposeModel(base); err != nil {
		t.Fatalf("base rejected: %v", err)
	}
	if err := ValidateGrantedComposeModel(base, grant); validationCode(err) != "WORKSPACE_GRANT_ENV_MISMATCH" {
		t.Fatalf("ungranted model error=%v", err)
	}

	granted := composeFixture(grant, true)
	if err := ValidateGrantedComposeModel(granted, grant); err != nil {
		t.Fatalf("grant rejected: %v", err)
	}
	if err := ValidateBaseComposeModel(granted); validationCode(err) != "BASE_WORKSPACE_GRANT_PRESENT" {
		t.Fatalf("granted base error=%v", err)
	}

	var model map[string]any
	if err := json.Unmarshal(granted, &model); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	services := model["services"].(map[string]any)
	worker := services["worker"].(map[string]any)
	worker["volumes"] = append(worker["volumes"].([]any), map[string]any{
		"type": "bind", "source": "/tmp/extra", "target": "/tmp/extra", "bind": map[string]any{"create_host_path": false},
	})
	mutated, _ := json.Marshal(model)
	if err := ValidateGrantedComposeModel(mutated, grant); validationCode(err) != "WORKSPACE_BIND_COUNT_INVALID" {
		t.Fatalf("extra bind error=%v", err)
	}
}

func TestGrantedComposeRejectsCapabilitiesOnSidecars(t *testing.T) {
	t.Parallel()
	grant := Grant{
		WorkspaceID: "workspace-1", Root: "/tmp/zhixu-model-contract",
		RootFingerprint: strings.Repeat("a", 64), BindingVersion: 1, Generation: 3,
	}
	tests := []struct {
		name string
		code string
		edit func(map[string]any)
	}{
		{
			name: "grant environment", code: "NON_RUNTIME_WORKSPACE_GRANT_FORBIDDEN",
			edit: func(proxy map[string]any) {
				proxy["environment"].(map[string]any)[grantWorkspaceIDEnv] = grant.WorkspaceID
			},
		},
		{
			name: "Docker socket descendant", code: "DOCKER_SOCKET_MOUNT_FORBIDDEN",
			edit: func(proxy map[string]any) {
				proxy["volumes"] = []any{map[string]any{"type": "volume", "source": "/var/run/docker.sock/child", "target": "/run/socket"}}
			},
		},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var model map[string]any
			if err := json.Unmarshal(composeFixture(grant, true), &model); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			proxy := model["services"].(map[string]any)["proxy"].(map[string]any)
			testCase.edit(proxy)
			document, _ := json.Marshal(model)
			if err := ValidateGrantedComposeModel(document, grant); validationCode(err) != testCase.code {
				t.Fatalf("ValidateGrantedComposeModel() error=%v, want %s", err, testCase.code)
			}
		})
	}
}

func composeFixture(grant Grant, includeGrant bool) []byte {
	services := map[string]any{}
	for _, role := range []string{"app", "worker"} {
		environment := map[string]string{"ZHIXU_ENVIRONMENT": "development"}
		volumes := []any{map[string]any{"type": "volume", "source": "secret", "target": "/run/secret", "read_only": true}}
		if includeGrant {
			environment[grantWorkspaceIDEnv] = grant.WorkspaceID
			environment[grantRootEnv] = grant.Root
			environment[grantGenerationEnv] = fmt.Sprintf("%d", grant.Generation)
			volumes = append(volumes, map[string]any{
				"type": "bind", "source": grant.Root, "target": grant.Root, "read_only": false,
				"bind": map[string]any{"create_host_path": false},
			})
		}
		services[role] = map[string]any{"environment": environment, "volumes": volumes, "user": "10001:10001"}
	}
	services["proxy"] = map[string]any{"environment": map[string]string{}, "volumes": []any{}}
	document, _ := json.Marshal(map[string]any{"services": services})
	return document
}
