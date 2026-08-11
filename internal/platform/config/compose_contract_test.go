package config

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	composeContractBootstrap       = "compose-contract-bootstrap-token-32-characters"
	composeContractDevQuestionRef  = "zhixu_dev_review_question_ref_key_change_me"
	composeContractQuestionRef     = "compose-contract-question-ref-key-32-bytes"
	composeContractUTF8QuestionRef = "密密密密密密密密密密密"
)

func TestLoopbackFirewallRuntimeImageIncludesRequiredNetworkTools(t *testing.T) {
	repositoryRoot := composeContractRepositoryRoot(t)
	dockerfile, err := os.ReadFile(filepath.Join(repositoryRoot, "deploy", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	installStep, _, found := strings.Cut(string(dockerfile), "&& addgroup")
	if !found {
		t.Fatal("runtime image package installation step is missing")
	}
	for _, packageName := range []string{"iptables", "iproute2", "socat"} {
		if !strings.Contains(installStep, " "+packageName) {
			t.Fatalf("loopback firewall runtime dependency %q is missing", packageName)
		}
	}
}

func TestComposeAuthEnvironmentAndMakefileContract(t *testing.T) {
	repositoryRoot := composeContractRepositoryRoot(t)
	requireDockerCompose(t, repositoryRoot)

	defaultModel := resolvedComposeModel(t, repositoryRoot, nil)
	defaultApp := composeServiceEnvironment(t, defaultModel, "app")
	if defaultApp["ZHIXU_AUTH_MODE"] != string(AuthModeDisabled) || defaultApp["ZHIXU_AUTH_BOOTSTRAP_TOKEN"] != "" {
		t.Fatalf("default app auth mode=%v bootstrap_configured=%t", defaultApp["ZHIXU_AUTH_MODE"], defaultApp["ZHIXU_AUTH_BOOTSTRAP_TOKEN"] != "")
	}
	if defaultApp["ZHIXU_REVIEW_QUESTION_REF_KEY"] != composeContractDevQuestionRef {
		t.Fatal("default Compose model lost the development-only Review question-reference key")
	}
	if defaultApp["ZHIXU_AUTH_ALLOWED_ORIGINS"] != "http://127.0.0.1:8080" {
		t.Fatalf("default allowed origin=%v", defaultApp["ZHIXU_AUTH_ALLOWED_ORIGINS"])
	}
	assertNonAPIProcessesHaveNoAPISecrets(t, defaultModel)
	if _, err := LoadWithLookup("", composeEnvironmentLookup(t, defaultApp)); err != nil {
		t.Fatalf("resolved default API configuration is invalid: %v", err)
	}
	defaultWorker := composeServiceEnvironment(t, defaultModel, "worker")
	if _, err := loadWorkerWithLookup("", composeEnvironmentLookup(t, defaultWorker)); err != nil {
		t.Fatalf("resolved Worker configuration is invalid without the API Bootstrap credential: %v", err)
	}

	customPortModel := resolvedComposeModel(t, repositoryRoot, map[string]string{
		"ZHIXU_HTTP_PORT": "18080",
	})
	customPortApp := composeServiceEnvironment(t, customPortModel, "app")
	if customPortApp["ZHIXU_AUTH_ALLOWED_ORIGINS"] != "http://127.0.0.1:18080" {
		t.Fatalf("custom-port default allowed origin=%v", customPortApp["ZHIXU_AUTH_ALLOWED_ORIGINS"])
	}

	explicitOriginModel := resolvedComposeModel(t, repositoryRoot, map[string]string{
		"ZHIXU_HTTP_PORT":            "18080",
		"ZHIXU_AUTH_ALLOWED_ORIGINS": "http://localhost:18080",
	})
	explicitOriginApp := composeServiceEnvironment(t, explicitOriginModel, "app")
	if explicitOriginApp["ZHIXU_AUTH_ALLOWED_ORIGINS"] != "http://localhost:18080" {
		t.Fatalf("explicit allowed origin lost precedence: %v", explicitOriginApp["ZHIXU_AUTH_ALLOWED_ORIGINS"])
	}

	requiredModel := resolvedComposeModel(t, repositoryRoot, map[string]string{
		"ZHIXU_HTTP_PORT":               "18080",
		"ZHIXU_AUTH_MODE":               string(AuthModeRequired),
		"ZHIXU_AUTH_BOOTSTRAP_TOKEN":    composeContractBootstrap,
		"ZHIXU_REVIEW_QUESTION_REF_KEY": composeContractQuestionRef,
		"ZHIXU_AUTH_ALLOWED_ORIGINS":    "http://127.0.0.1:18080",
		"ZHIXU_AUTH_SECURE_COOKIE":      "false",
	})
	requiredApp := composeServiceEnvironment(t, requiredModel, "app")
	requiredConfig, err := LoadWithLookup("", composeEnvironmentLookup(t, requiredApp))
	if err != nil {
		t.Fatalf("resolved required API configuration is invalid: %v", err)
	}
	if requiredConfig.ReviewQuestionRefKey != composeContractQuestionRef {
		t.Fatal("Compose did not preserve the explicit API Review question-reference key")
	}
	if strings.Contains(requiredConfig.String(), composeContractQuestionRef) {
		t.Fatal("Config.String echoed the Review question-reference key")
	}
	if requiredApp["ZHIXU_HTTP_ADDR"] != "127.0.0.1:8081" {
		t.Fatalf("Compose must keep the API process on loopback: %v", requiredApp["ZHIXU_HTTP_ADDR"])
	}
	assertNonAPIProcessesHaveNoAPISecrets(t, requiredModel)

	runComposeAuthMakeCheck(t, repositoryRoot, nil, true, "")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_AUTH_MODE": string(AuthModeRequired),
	}, false, "required mode needs")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_AUTH_MODE":            string(AuthModeRequired),
		"ZHIXU_AUTH_BOOTSTRAP_TOKEN": composeContractBootstrap,
	}, true, "")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_REVIEW_QUESTION_REF_KEY": composeContractQuestionRef,
	}, true, "")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_REVIEW_QUESTION_REF_KEY": "",
	}, false, "at least 32 canonical bytes")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_REVIEW_QUESTION_REF_KEY": composeContractUTF8QuestionRef,
	}, true, "")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_REVIEW_QUESTION_REF_KEY": "short",
	}, false, "at least 32 canonical bytes")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_REVIEW_QUESTION_REF_KEY": " " + composeContractQuestionRef,
	}, false, "at least 32 canonical bytes")
	runComposeAuthMakeCheck(t, repositoryRoot, map[string]string{
		"ZHIXU_AUTH_MODE":            string(AuthModeDisabled),
		"ZHIXU_AUTH_BOOTSTRAP_TOKEN": composeContractBootstrap,
	}, false, "must be empty")
}

func TestComposeUpMakefileRunsAuthGuardBeforeStartingServices(t *testing.T) {
	repositoryRoot := composeContractRepositoryRoot(t)
	fakeCompose := filepath.Join(t.TempDir(), "docker-compose-contract")
	fakeSource := `#!/usr/bin/env python3
import json
import os
import sys

with open(os.environ["COMPOSE_CONTRACT_LOG"], "a", encoding="utf-8") as log:
    log.write(" ".join(sys.argv[1:]) + "\n")

if "config" in sys.argv and "--format" in sys.argv:
    app_environment = {
        "ZHIXU_AUTH_MODE": os.environ.get("ZHIXU_AUTH_MODE", "disabled"),
        "ZHIXU_AUTH_BOOTSTRAP_TOKEN": os.environ.get("ZHIXU_AUTH_BOOTSTRAP_TOKEN", ""),
        "ZHIXU_REVIEW_QUESTION_REF_KEY": os.environ.get("ZHIXU_REVIEW_QUESTION_REF_KEY", ""),
        "ZHIXU_AUTH_SECURE_COOKIE": os.environ.get("ZHIXU_AUTH_SECURE_COOKIE", "false"),
        "ZHIXU_HTTP_ADDR": os.environ.get("COMPOSE_CONTRACT_HTTP_ADDR", "127.0.0.1:8081"),
    }
    non_api_environments = {"migrate": {}, "worker": {}}
    non_api_secret_target = os.environ.get("COMPOSE_CONTRACT_API_SECRET_TARGET", "")
    if non_api_secret_target in non_api_environments:
        non_api_environments[non_api_secret_target]["ZHIXU_REVIEW_QUESTION_REF_KEY"] = os.environ["ZHIXU_REVIEW_QUESTION_REF_KEY"]
    app = {
        "environment": app_environment,
        "network_mode": os.environ.get("COMPOSE_CONTRACT_APP_NETWORK_MODE", "container:zhixu-app-netns"),
    }
    if os.environ.get("COMPOSE_CONTRACT_APP_PORT") == "1":
        app["ports"] = [{"host_ip": "127.0.0.1", "protocol": "tcp"}]
    json.dump({
        "services": {
            "app": app,
            "app-model-relay": {
                "network_mode": os.environ.get("COMPOSE_CONTRACT_APP_RELAY_NETWORK_MODE", "container:zhixu-app-netns"),
            },
            "migrate": {"environment": non_api_environments["migrate"]},
            "worker": {
                "environment": non_api_environments["worker"],
                "network_mode": os.environ.get("COMPOSE_CONTRACT_WORKER_NETWORK_MODE", "container:zhixu-worker-netns"),
            },
            "worker-model-relay": {
                "network_mode": os.environ.get("COMPOSE_CONTRACT_WORKER_RELAY_NETWORK_MODE", "container:zhixu-worker-netns"),
            },
        }
    }, sys.stdout)
`
	if err := os.WriteFile(fakeCompose, []byte(fakeSource), 0o700); err != nil {
		t.Fatal(err)
	}

	for name, testCase := range map[string]struct {
		overrides   map[string]string
		wantSuccess bool
		wantUp      bool
	}{
		"disabled empty": {wantSuccess: true, wantUp: true},
		"required missing": {overrides: map[string]string{
			"ZHIXU_AUTH_MODE": string(AuthModeRequired),
		}},
		"disabled explicit": {overrides: map[string]string{
			"ZHIXU_AUTH_MODE":            string(AuthModeDisabled),
			"ZHIXU_AUTH_BOOTSTRAP_TOKEN": composeContractBootstrap,
		}},
		"required valid": {overrides: map[string]string{
			"ZHIXU_AUTH_MODE":               string(AuthModeRequired),
			"ZHIXU_AUTH_BOOTSTRAP_TOKEN":    composeContractBootstrap,
			"ZHIXU_REVIEW_QUESTION_REF_KEY": composeContractQuestionRef,
		}, wantSuccess: true, wantUp: true},
		"empty Review question-reference key": {overrides: map[string]string{
			"ZHIXU_REVIEW_QUESTION_REF_KEY": "",
		}},
		"short Review question-reference key": {overrides: map[string]string{
			"ZHIXU_REVIEW_QUESTION_REF_KEY": "short",
		}},
		"non-canonical Review question-reference key": {overrides: map[string]string{
			"ZHIXU_REVIEW_QUESTION_REF_KEY": composeContractQuestionRef + " ",
		}},
		"Migrate Review question-reference key leak": {overrides: map[string]string{
			"COMPOSE_CONTRACT_API_SECRET_TARGET": "migrate",
		}},
		"Worker Review question-reference key leak": {overrides: map[string]string{
			"COMPOSE_CONTRACT_API_SECRET_TARGET": "worker",
		}},
		"disabled independent app port": {overrides: map[string]string{
			"COMPOSE_CONTRACT_APP_PORT": "1",
		}},
		"insecure required independent app port": {overrides: map[string]string{
			"ZHIXU_AUTH_MODE":            string(AuthModeRequired),
			"ZHIXU_AUTH_BOOTSTRAP_TOKEN": composeContractBootstrap,
			"COMPOSE_CONTRACT_APP_PORT":  "1",
		}},
		"secure required independent app port": {overrides: map[string]string{
			"ZHIXU_AUTH_MODE":            string(AuthModeRequired),
			"ZHIXU_AUTH_BOOTSTRAP_TOKEN": composeContractBootstrap,
			"ZHIXU_AUTH_SECURE_COOKIE":   "true",
			"COMPOSE_CONTRACT_APP_PORT":  "1",
		}, wantSuccess: true, wantUp: true},
		"disabled app host networking": {overrides: map[string]string{
			"COMPOSE_CONTRACT_APP_NETWORK_MODE": "host",
		}},
		"disabled worker host networking": {overrides: map[string]string{
			"COMPOSE_CONTRACT_WORKER_NETWORK_MODE": "host",
		}},
		"disabled detached app namespace": {overrides: map[string]string{
			"COMPOSE_CONTRACT_APP_NETWORK_MODE": "bridge",
		}},
		"disabled app relay host networking": {overrides: map[string]string{
			"COMPOSE_CONTRACT_APP_RELAY_NETWORK_MODE": "host",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "compose.log")
			overrides := make(map[string]string, len(testCase.overrides)+3)
			for key, value := range testCase.overrides {
				overrides[key] = value
			}
			if _, exists := overrides["ZHIXU_REVIEW_QUESTION_REF_KEY"]; !exists {
				overrides["ZHIXU_REVIEW_QUESTION_REF_KEY"] = composeContractDevQuestionRef
			}
			overrides["DOCKER_COMPOSE"] = fakeCompose
			overrides["COMPOSE_CONTRACT_LOG"] = logPath

			command := exec.Command("make", "compose-up")
			command.Dir = repositoryRoot
			command.Env = composeContractEnvironment(overrides)
			output, err := command.CombinedOutput()
			if testCase.wantSuccess && err != nil {
				t.Fatalf("compose-up unexpectedly failed: %v (%s)", err, strings.TrimSpace(string(output)))
			}
			if !testCase.wantSuccess && err == nil {
				t.Fatal("compose-up unexpectedly accepted an unsafe auth configuration")
			}
			logOutput, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			started := strings.Contains(string(logOutput), " up -d --build --wait")
			if started != testCase.wantUp {
				t.Fatalf("compose-up start invocation=%t want=%t log=%q", started, testCase.wantUp, string(logOutput))
			}
			for _, secret := range []string{composeContractBootstrap, composeContractDevQuestionRef, composeContractQuestionRef, composeContractUTF8QuestionRef} {
				if strings.Contains(string(output), secret) {
					t.Fatal("compose-up echoed an API-only secret")
				}
			}
		})
	}
}

func composeContractRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve compose contract test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}

func requireDockerCompose(t *testing.T, repositoryRoot string) {
	t.Helper()
	command := exec.Command("docker", "compose", "version")
	command.Dir = repositoryRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("docker compose is unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
}

func resolvedComposeModel(t *testing.T, repositoryRoot string, overrides map[string]string) map[string]any {
	t.Helper()
	command := exec.Command(
		"docker", "compose",
		"--profile", "workspace-runtime",
		"-f", "deploy/compose.yml",
		"--env-file", ".env.example",
		"config", "--format", "json",
	)
	command.Dir = repositoryRoot
	command.Env = composeContractEnvironment(overrides)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("resolve Compose model: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	var model map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &model); err != nil {
		t.Fatalf("decode Compose model: %v", err)
	}
	return model
}

func composeServiceEnvironment(t *testing.T, model map[string]any, serviceName string) map[string]any {
	t.Helper()
	services, ok := model["services"].(map[string]any)
	if !ok {
		t.Fatal("Compose services are missing")
	}
	service, ok := services[serviceName].(map[string]any)
	if !ok {
		t.Fatalf("Compose service %q is missing", serviceName)
	}
	environment, ok := service["environment"].(map[string]any)
	if !ok {
		t.Fatalf("Compose service %q environment is missing", serviceName)
	}
	return environment
}

func composeEnvironmentLookup(t *testing.T, environment map[string]any) func(string) (string, bool) {
	t.Helper()
	values := make(map[string]string, len(environment))
	for key, value := range environment {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("Compose environment %q is not a string", key)
		}
		values[key] = text
	}
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func assertNonAPIProcessesHaveNoAPISecrets(t *testing.T, model map[string]any) {
	t.Helper()
	for _, serviceName := range []string{"migrate", "worker"} {
		environment := composeServiceEnvironment(t, model, serviceName)
		for _, secretName := range []string{"ZHIXU_AUTH_BOOTSTRAP_TOKEN", "ZHIXU_REVIEW_QUESTION_REF_KEY"} {
			if _, exists := environment[secretName]; exists {
				t.Fatalf("Compose injected API-only secret %s into %s", secretName, serviceName)
			}
		}
	}
}

func runComposeAuthMakeCheck(t *testing.T, repositoryRoot string, overrides map[string]string, wantSuccess bool, wantMessage string) {
	t.Helper()
	command := exec.Command("make", "compose-auth-check")
	command.Dir = repositoryRoot
	command.Env = composeContractEnvironment(overrides)
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("compose-auth-check unexpectedly failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if !wantSuccess && err == nil {
		t.Fatal("compose-auth-check unexpectedly accepted an unsafe configuration")
	}
	if wantMessage != "" && !strings.Contains(string(output), wantMessage) {
		t.Fatalf("compose-auth-check output=%q want substring %q", string(output), wantMessage)
	}
	for _, secret := range []string{composeContractBootstrap, composeContractDevQuestionRef, composeContractQuestionRef, composeContractUTF8QuestionRef} {
		if strings.Contains(string(output), secret) {
			t.Fatal("compose-auth-check echoed an API-only secret")
		}
	}
}

func composeContractEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ZHIXU_") || key == "DOCKER_COMPOSE" || key == "COMPOSE_CONTRACT_LOG" || key == "COMPOSE_CONTRACT_API_SECRET_TARGET" || key == "COMPOSE_CONTRACT_HOST_IP" || key == "COMPOSE_CONTRACT_HTTP_ADDR" || key == "COMPOSE_CONTRACT_NETWORK_MODE" || key == "COMPOSE_CONTRACT_PROXY_NETWORK_MODE" || key == "COMPOSE_CONTRACT_PROXY_USER" || key == "COMPOSE_CONTRACT_PROXY_DEPENDS_ON" || key == "COMPOSE_CONTRACT_PROXY_DEPENDENCY_CONDITION" || key == "COMPOSE_CONTRACT_FIREWALL_NETWORK_MODE" || key == "COMPOSE_CONTRACT_FIREWALL_ENTRYPOINT" || key == "COMPOSE_CONTRACT_FIREWALL_USER" || key == "COMPOSE_CONTRACT_FIREWALL_CAPABILITY" || key == "COMPOSE_CONTRACT_FIREWALL_DEPENDS_ON" || key == "COMPOSE_CONTRACT_FIREWALL_DEPENDENCY_CONDITION" {
			continue
		}
		environment = append(environment, entry)
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}
