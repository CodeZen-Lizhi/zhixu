package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsWorkerRuntime(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if cfg.WorkerQueue != "workflow" || cfg.WorkerMaxWorkers != 4 {
		t.Fatalf("unexpected worker defaults: %s", cfg)
	}
	if cfg.WorkerJobTimeout != 15*time.Minute || cfg.WorkerRescueStuckJobsAfter != 30*time.Minute {
		t.Fatalf("unexpected River timeout defaults: %s", cfg)
	}
	if cfg.WorkflowLeaseDuration != 2*time.Minute || cfg.WorkflowHeartbeatInterval != 30*time.Second {
		t.Fatalf("unexpected workflow lease defaults: %s", cfg)
	}
	if cfg.ReindexDispatchPollInterval != time.Second || cfg.ReindexDispatchBatchSize != 10 || cfg.ReindexDispatchErrorBackoff != 5*time.Second {
		t.Fatalf("unexpected reindex dispatcher defaults: %s", cfg)
	}
	if cfg.ReindexLeaseDuration != 2*time.Minute || cfg.ReindexHeartbeatInterval != 30*time.Second {
		t.Fatalf("unexpected reindex lease defaults: %s", cfg)
	}
	if cfg.WorkerSoftStopTimeout != 30*time.Second || cfg.WorkerHardStopTimeout != time.Minute {
		t.Fatalf("unexpected shutdown defaults: %s", cfg)
	}
	if cfg.WorkerHealthAddr != "0.0.0.0:8081" {
		t.Fatalf("unexpected worker health address: %q", cfg.WorkerHealthAddr)
	}
	if cfg.TelemetryMode != TelemetryModeDisabled || cfg.TelemetryEndpoint != "" {
		t.Fatalf("telemetry must default to explicitly disabled: %s", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
}

func TestLoadWithLookupYAMLThenEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http_addr: 127.0.0.1:9090\ndatabase_url: yaml-value\nhealth_interval: 1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) (string, bool) {
		if key == "ZHIXU_HTTP_ADDR" {
			return "127.0.0.1:9191", true
		}
		if key == "ZHIXU_DATABASE_URL" {
			return "env-value", true
		}
		return "", false
	}
	cfg, err := LoadWithLookup(path, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9191" || cfg.DatabaseURL != "env-value" {
		t.Fatalf("environment should override YAML: %+v", cfg)
	}
	if cfg.HealthInterval != time.Minute {
		t.Fatalf("YAML duration not loaded: %s", cfg.HealthInterval)
	}
}

func TestLoadWithLookupRejectsInvalidYAMLDuration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("health_interval: not-a-duration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithLookup(path, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected invalid YAML duration error")
	}
}

func TestLoadWithLookupRejectsUnknownYAMLField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("unexpected: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithLookup(path, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected unknown YAML field error")
	}
}

func TestLoadWithLookupRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()
	_, err := LoadWithLookup("", func(key string) (string, bool) {
		if key == "ZHIXU_HEALTH_INTERVAL" {
			return "not-a-duration", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestLoadWithLookupRejectsInvalidReindexEnvironment(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"ZHIXU_REINDEX_DISPATCH_BATCH_SIZE":    "not-a-number",
		"ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL": "not-a-duration",
	}
	for key, value := range tests {
		key, value := key, value
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, err := LoadWithLookup("", func(candidate string) (string, bool) {
				if candidate == key {
					return value, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected error containing %q, got %v", key, err)
			}
		})
	}
}

func TestLoadWithLookupWorkerEnvironmentOverrides(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"ZHIXU_WORKER_QUEUE":              "critical-workflow",
		"ZHIXU_WORKER_MAX_WORKERS":        "7",
		"ZHIXU_WORKER_JOB_TIMEOUT":        "20m",
		"ZHIXU_WORKER_RESCUE_STUCK_AFTER": "45m",
		"ZHIXU_WORKFLOW_LEASE":            "3m",
		"ZHIXU_WORKFLOW_HEARTBEAT":        "45s",

		"ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL": "2s",
		"ZHIXU_REINDEX_DISPATCH_BATCH_SIZE":    "25",
		"ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF": "7s",
		"ZHIXU_REINDEX_LEASE_DURATION":         "4m",
		"ZHIXU_REINDEX_HEARTBEAT_INTERVAL":     "50s",

		"ZHIXU_WORKER_SOFT_STOP_TIMEOUT": "40s",
		"ZHIXU_WORKER_HARD_STOP_TIMEOUT": "90s",
		"ZHIXU_WORKER_HEALTH_ADDR":       "127.0.0.1:9091",
		"ZHIXU_TELEMETRY_MODE":           "required",
		"OTEL_EXPORTER_OTLP_ENDPOINT":    "https://otel.example.test:4318",
	}
	cfg, err := LoadWithLookup("", func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerQueue != "critical-workflow" || cfg.WorkerMaxWorkers != 7 {
		t.Fatalf("worker environment override failed: %s", cfg)
	}
	if cfg.WorkerJobTimeout != 20*time.Minute || cfg.WorkerRescueStuckJobsAfter != 45*time.Minute {
		t.Fatalf("River timeout environment override failed: %s", cfg)
	}
	if cfg.WorkflowLeaseDuration != 3*time.Minute || cfg.WorkflowHeartbeatInterval != 45*time.Second {
		t.Fatalf("workflow environment override failed: %s", cfg)
	}
	if cfg.ReindexDispatchPollInterval != 2*time.Second || cfg.ReindexDispatchBatchSize != 25 || cfg.ReindexDispatchErrorBackoff != 7*time.Second {
		t.Fatalf("reindex dispatcher environment override failed: %s", cfg)
	}
	if cfg.ReindexLeaseDuration != 4*time.Minute || cfg.ReindexHeartbeatInterval != 50*time.Second {
		t.Fatalf("reindex lease environment override failed: %s", cfg)
	}
	if cfg.WorkerSoftStopTimeout != 40*time.Second || cfg.WorkerHardStopTimeout != 90*time.Second {
		t.Fatalf("shutdown environment override failed: %s", cfg)
	}
	if cfg.WorkerHealthAddr != "127.0.0.1:9091" {
		t.Fatalf("health environment override failed: %s", cfg)
	}
	if cfg.TelemetryMode != TelemetryModeRequired || cfg.TelemetryEndpoint != "https://otel.example.test:4318" {
		t.Fatalf("telemetry environment override failed: %s", cfg)
	}
}

func TestLoadWithLookupWorkerYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`worker_queue: batch
worker_max_workers: 2
worker_job_timeout: 10m
worker_rescue_stuck_jobs_after: 25m
workflow_lease: 90s
workflow_heartbeat: 20s
reindex_dispatch_poll_interval: 3s
reindex_dispatch_batch_size: 40
reindex_dispatch_error_backoff: 9s
reindex_lease_duration: 5m
reindex_heartbeat_interval: 55s
worker_soft_stop_timeout: 15s
worker_hard_stop_timeout: 45s
worker_health_addr: ":8181"
telemetry_mode: optional
telemetry_endpoint: http://collector:4318
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerQueue != "batch" || cfg.WorkerMaxWorkers != 2 || cfg.WorkerHealthAddr != ":8181" {
		t.Fatalf("worker YAML not loaded: %s", cfg)
	}
	if cfg.ReindexDispatchPollInterval != 3*time.Second || cfg.ReindexDispatchBatchSize != 40 || cfg.ReindexDispatchErrorBackoff != 9*time.Second {
		t.Fatalf("reindex dispatcher YAML not loaded: %s", cfg)
	}
	if cfg.ReindexLeaseDuration != 5*time.Minute || cfg.ReindexHeartbeatInterval != 55*time.Second {
		t.Fatalf("reindex lease YAML not loaded: %s", cfg)
	}
	if cfg.TelemetryMode != TelemetryModeOptional || cfg.TelemetryEndpoint != "http://collector:4318" {
		t.Fatalf("telemetry YAML not loaded: %s", cfg)
	}
}

func TestValidateWorkerBoundaryCombinations(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.WorkerJobTimeout = time.Second
	cfg.WorkerRescueStuckJobsAfter = time.Second + time.Nanosecond
	cfg.WorkflowLeaseDuration = 4 * time.Nanosecond
	cfg.WorkflowHeartbeatInterval = time.Nanosecond
	cfg.ReindexDispatchPollInterval = time.Minute
	cfg.ReindexDispatchBatchSize = 1_000
	cfg.ReindexDispatchErrorBackoff = time.Minute
	cfg.ReindexLeaseDuration = time.Minute + time.Nanosecond
	cfg.ReindexHeartbeatInterval = time.Minute
	cfg.WorkerSoftStopTimeout = time.Second
	cfg.WorkerHardStopTimeout = time.Second + time.Nanosecond
	if err := cfg.Validate(); err != nil {
		t.Fatalf("strictly ordered boundary values must be valid: %v", err)
	}
}

func TestValidateRejectsInvalidWorkerConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "empty queue", change: func(c *Config) { c.WorkerQueue = " " }, want: "worker_queue"},
		{name: "zero workers", change: func(c *Config) { c.WorkerMaxWorkers = 0 }, want: "worker_max_workers"},
		{name: "negative workers", change: func(c *Config) { c.WorkerMaxWorkers = -1 }, want: "worker_max_workers"},
		{name: "zero job timeout", change: func(c *Config) { c.WorkerJobTimeout = 0 }, want: "worker_job_timeout"},
		{name: "zero rescue timeout", change: func(c *Config) { c.WorkerRescueStuckJobsAfter = 0 }, want: "worker_rescue_stuck_jobs_after"},
		{name: "job equals rescue", change: func(c *Config) { c.WorkerJobTimeout = c.WorkerRescueStuckJobsAfter }, want: "worker_job_timeout"},
		{name: "job exceeds rescue", change: func(c *Config) { c.WorkerJobTimeout = c.WorkerRescueStuckJobsAfter + time.Second }, want: "worker_job_timeout"},
		{name: "zero lease", change: func(c *Config) { c.WorkflowLeaseDuration = 0 }, want: "workflow_lease"},
		{name: "zero heartbeat", change: func(c *Config) { c.WorkflowHeartbeatInterval = 0 }, want: "workflow_heartbeat"},
		{name: "heartbeat equals lease third", change: func(c *Config) { c.WorkflowHeartbeatInterval = c.WorkflowLeaseDuration / 3 }, want: "workflow_heartbeat"},
		{name: "heartbeat exceeds lease third", change: func(c *Config) { c.WorkflowHeartbeatInterval = c.WorkflowLeaseDuration / 2 }, want: "workflow_heartbeat"},
		{name: "zero reindex poll interval", change: func(c *Config) { c.ReindexDispatchPollInterval = 0 }, want: "reindex_dispatch_poll_interval"},
		{name: "reindex poll interval exceeds maximum", change: func(c *Config) { c.ReindexDispatchPollInterval = time.Minute + time.Nanosecond }, want: "reindex_dispatch_poll_interval"},
		{name: "zero reindex batch size", change: func(c *Config) { c.ReindexDispatchBatchSize = 0 }, want: "reindex_dispatch_batch_size"},
		{name: "reindex batch size exceeds maximum", change: func(c *Config) { c.ReindexDispatchBatchSize = 1_001 }, want: "reindex_dispatch_batch_size"},
		{name: "zero reindex error backoff", change: func(c *Config) { c.ReindexDispatchErrorBackoff = 0 }, want: "reindex_dispatch_error_backoff"},
		{name: "reindex error backoff exceeds maximum", change: func(c *Config) { c.ReindexDispatchErrorBackoff = time.Minute + time.Nanosecond }, want: "reindex_dispatch_error_backoff"},
		{name: "zero reindex lease", change: func(c *Config) { c.ReindexLeaseDuration = 0 }, want: "reindex_lease_duration"},
		{name: "reindex lease exceeds maximum", change: func(c *Config) { c.ReindexLeaseDuration = 24*time.Hour + time.Nanosecond }, want: "reindex_lease_duration"},
		{name: "zero reindex heartbeat", change: func(c *Config) { c.ReindexHeartbeatInterval = 0 }, want: "reindex_heartbeat_interval"},
		{name: "reindex heartbeat exceeds maximum", change: func(c *Config) { c.ReindexHeartbeatInterval = time.Minute + time.Nanosecond }, want: "reindex_heartbeat_interval"},
		{name: "reindex heartbeat equals lease", change: func(c *Config) { c.ReindexHeartbeatInterval = c.ReindexLeaseDuration }, want: "reindex_heartbeat_interval"},
		{name: "reindex heartbeat exceeds lease", change: func(c *Config) { c.ReindexHeartbeatInterval = c.ReindexLeaseDuration + time.Nanosecond }, want: "reindex_heartbeat_interval"},
		{name: "zero soft stop", change: func(c *Config) { c.WorkerSoftStopTimeout = 0 }, want: "worker_soft_stop_timeout"},
		{name: "zero hard stop", change: func(c *Config) { c.WorkerHardStopTimeout = 0 }, want: "worker_hard_stop_timeout"},
		{name: "soft equals hard", change: func(c *Config) { c.WorkerSoftStopTimeout = c.WorkerHardStopTimeout }, want: "worker_soft_stop_timeout"},
		{name: "soft exceeds hard", change: func(c *Config) { c.WorkerSoftStopTimeout = c.WorkerHardStopTimeout + time.Second }, want: "worker_soft_stop_timeout"},
		{name: "health missing port", change: func(c *Config) { c.WorkerHealthAddr = "localhost" }, want: "worker_health_addr"},
		{name: "health zero port", change: func(c *Config) { c.WorkerHealthAddr = "localhost:0" }, want: "worker_health_addr"},
		{name: "health port overflow", change: func(c *Config) { c.WorkerHealthAddr = "localhost:65536" }, want: "worker_health_addr"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			test.change(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateTelemetryModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mode     TelemetryMode
		endpoint string
		wantErr  bool
	}{
		{name: "disabled", mode: TelemetryModeDisabled},
		{name: "optional", mode: TelemetryModeOptional, endpoint: "http://otel:4318"},
		{name: "required", mode: TelemetryModeRequired, endpoint: "https://otel.example.test"},
		{name: "disabled endpoint", mode: TelemetryModeDisabled, endpoint: "http://otel:4318", wantErr: true},
		{name: "optional missing endpoint", mode: TelemetryModeOptional, wantErr: true},
		{name: "required missing endpoint", mode: TelemetryModeRequired, wantErr: true},
		{name: "unknown mode", mode: "best-effort", endpoint: "http://otel:4318", wantErr: true},
		{name: "relative endpoint", mode: TelemetryModeOptional, endpoint: "otel:4318", wantErr: true},
		{name: "unsupported scheme", mode: TelemetryModeRequired, endpoint: "file:///tmp/otel", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			cfg.TelemetryMode = test.mode
			cfg.TelemetryEndpoint = test.endpoint
			err := cfg.Validate()
			if test.wantErr && err == nil {
				t.Fatal("expected telemetry validation error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected telemetry validation error: %v", err)
			}
		})
	}
}

func TestLoadWithLookupDisabledTelemetryDoesNotConsumeEndpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("telemetry_mode: optional\ntelemetry_endpoint: https://yaml-secret@otel.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		switch key {
		case "ZHIXU_TELEMETRY_MODE":
			return "disabled", true
		case "OTEL_EXPORTER_OTLP_ENDPOINT":
			return "https://user:secret@otel.example.test", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelemetryEndpoint != "" {
		t.Fatal("disabled telemetry must not consume the exporter endpoint")
	}
}

func TestConfigFormattingAndErrorsDoNotExposeSecrets(t *testing.T) {
	t.Parallel()
	const (
		password       = "database-password-secret"
		databaseURL    = "postgres://app:database-url-secret@db/zhixu"
		telemetryToken = "telemetry-token-secret"
	)
	cfg := Defaults()
	cfg.DatabasePassword = password
	cfg.DatabaseURL = databaseURL
	cfg.TelemetryMode = TelemetryModeRequired
	cfg.TelemetryEndpoint = "https://collector:" + telemetryToken + "@otel.example.test"
	cfg.ReindexDispatchPollInterval = 2 * time.Second
	cfg.ReindexDispatchBatchSize = 25
	cfg.ReindexDispatchErrorBackoff = 7 * time.Second
	cfg.ReindexLeaseDuration = 4 * time.Minute
	cfg.ReindexHeartbeatInterval = 50 * time.Second
	for _, formatted := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		for _, secret := range []string{password, databaseURL, telemetryToken} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("formatted config exposed secret %q: %s", secret, formatted)
			}
		}
		for _, value := range []string{
			"ReindexDispatchPollInterval:2s",
			"ReindexDispatchBatchSize:25",
			"ReindexDispatchErrorBackoff:7s",
			"ReindexLeaseDuration:4m0s",
			"ReindexHeartbeatInterval:50s",
		} {
			if !strings.Contains(formatted, value) {
				t.Fatalf("formatted config omitted %q: %s", value, formatted)
			}
		}
	}

	cfg.DatabaseURL = "postgres://app:invalid-url-secret@%zz"
	if err := cfg.ValidateDatabase(); err == nil || strings.Contains(err.Error(), "invalid-url-secret") {
		t.Fatalf("database validation error must be stable and secret-safe: %v", err)
	}
	cfg.DatabaseURL = ""
	cfg.TelemetryEndpoint = "https://collector:invalid-endpoint-secret@%zz"
	if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "invalid-endpoint-secret") {
		t.Fatalf("telemetry validation error must be stable and secret-safe: %v", err)
	}
}

func TestValidateDatabaseRequiresExplicitURL(t *testing.T) {
	t.Parallel()
	if err := Defaults().ValidateDatabase(); err == nil {
		t.Fatal("expected missing database URL error")
	}
}

func TestDatabaseConnectionStringEscapesPassword(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.DatabaseHost = "db"
	cfg.DatabaseName = "zhixu"
	cfg.DatabaseUser = "app"
	cfg.DatabasePassword = "p@ss/word#1"
	got, err := cfg.DatabaseConnectionString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://app:p%40ss%2Fword%231@db:5432/zhixu?sslmode=disable" {
		t.Fatalf("unexpected connection string: %s", got)
	}
}

func TestDatabaseConnectionStringRejectsMalformedURL(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.DatabaseURL = "postgres://%zz"
	if _, err := cfg.DatabaseConnectionString(); err == nil {
		t.Fatal("expected malformed database URL error")
	}
}
