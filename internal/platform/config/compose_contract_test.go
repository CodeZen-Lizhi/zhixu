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
	assertServicesHaveNoAPISecrets(t, defaultModel, "worker")
	defaultBootstrapModel := resolvedBootstrapComposeModel(t, repositoryRoot, nil)
	assertServicesHaveNoAPISecrets(t, defaultBootstrapModel, "worker", "migrate", "modelctl")
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
	assertServicesHaveNoAPISecrets(t, requiredModel, "worker")

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

func TestComposeWorkspaceAnalysisFeatureGateContract(t *testing.T) {
	repositoryRoot := composeContractRepositoryRoot(t)
	requireDockerCompose(t, repositoryRoot)

	defaults := resolvedComposeModel(t, repositoryRoot, nil)
	defaultApp := composeServiceEnvironment(t, defaults, "app")
	defaultWorker := composeServiceEnvironment(t, defaults, "worker")
	if defaultApp["ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED"] != "false" ||
		defaultWorker["ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED"] != "false" ||
		defaultApp["ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION"] != "1" ||
		defaultWorker["ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION"] != "1" {
		t.Fatalf("default Workspace Analysis Compose gates drifted: app=%v worker=%v", defaultApp, defaultWorker)
	}

	enabled := resolvedComposeModel(t, repositoryRoot, map[string]string{
		"ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED":     "true",
		"ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED":  "true",
		"ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION": "9",
	})
	appEnvironment := composeServiceEnvironment(t, enabled, "app")
	workerEnvironment := composeServiceEnvironment(t, enabled, "worker")
	appConfig, err := LoadWithLookup("", composeEnvironmentLookup(t, appEnvironment))
	if err != nil {
		t.Fatalf("load enabled Workspace Analysis API configuration: %v", err)
	}
	workerConfig, err := loadWorkerWithLookup("", composeEnvironmentLookup(t, workerEnvironment))
	if err != nil {
		t.Fatalf("load enabled Workspace Analysis Worker configuration: %v", err)
	}
	if !appConfig.WorkspaceAnalysisAPIEnabled || appConfig.WorkspaceAnalysisConfigRevision != 9 ||
		!workerConfig.WorkspaceAnalysisWorkerEnabled || workerConfig.WorkspaceAnalysisConfigRevision != 9 {
		t.Fatalf("enabled Workspace Analysis Compose gates were not preserved: app=%s worker=%s", appConfig.String(), workerConfig.String())
	}
}

func TestComposeMakefileDelegatesMutationsToLauncher(t *testing.T) {
	repositoryRoot := composeContractRepositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(contents)

	for target, launcherCommand := range map[string]string{
		"compose-up":   "./zhixu up",
		"compose-down": "./zhixu down",
	} {
		recipe := makeTargetRecipe(t, makefile, target)
		if !strings.Contains(recipe, launcherCommand) {
			t.Fatalf("%s does not delegate to %q", target, launcherCommand)
		}
		if strings.Contains(recipe, "$(DOCKER_COMPOSE)") {
			t.Fatalf("%s still mutates Docker Compose directly", target)
		}
	}
	bootstrapRecipe := makeTargetRecipe(t, makefile, "compose-bootstrap-check")
	for _, composeFile := range []string{"deploy/compose.yml", "deploy/compose.bootstrap.yml"} {
		if !strings.Contains(bootstrapRecipe, composeFile) {
			t.Fatalf("compose-bootstrap-check does not render %s", composeFile)
		}
	}
}

func makeTargetRecipe(t *testing.T, makefile string, target string) string {
	t.Helper()
	marker := "\n" + target + ":"
	start := strings.Index(makefile, marker)
	if start < 0 {
		t.Fatalf("Make target %s is missing", target)
	}
	start += len(marker)
	end := strings.Index(makefile[start:], "\n\n")
	if end < 0 {
		return makefile[start:]
	}
	return makefile[start : start+end]
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
	return resolvedComposeFiles(t, repositoryRoot, overrides, "deploy/compose.yml")
}

func resolvedBootstrapComposeModel(t *testing.T, repositoryRoot string, overrides map[string]string) map[string]any {
	t.Helper()
	return resolvedComposeFiles(t, repositoryRoot, overrides, "deploy/compose.yml", "deploy/compose.bootstrap.yml")
}

func resolvedComposeFiles(t *testing.T, repositoryRoot string, overrides map[string]string, files ...string) map[string]any {
	t.Helper()
	arguments := []string{"compose", "--profile", "workspace-runtime", "--profile", "modelctl"}
	for _, file := range files {
		arguments = append(arguments, "-f", file)
	}
	arguments = append(arguments, "--env-file", ".env.example", "config", "--format", "json")
	command := exec.Command("docker", arguments...)
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

func assertServicesHaveNoAPISecrets(t *testing.T, model map[string]any, serviceNames ...string) {
	t.Helper()
	for _, serviceName := range serviceNames {
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
		if strings.HasPrefix(key, "ZHIXU_") || key == "DOCKER_COMPOSE" {
			continue
		}
		environment = append(environment, entry)
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}
