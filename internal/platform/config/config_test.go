package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
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
	if cfg.GraphQueryTimeout != 2*time.Second {
		t.Fatalf("unexpected Graph query timeout default: %s", cfg)
	}
	if cfg.TelemetryMode != TelemetryModeDisabled || cfg.TelemetryEndpoint != "" {
		t.Fatalf("telemetry must default to explicitly disabled: %s", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
}

func TestDefaultsEmbeddingAndRetrievalRRF(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if cfg.EmbeddingProvider != EmbeddingProviderDisabled || cfg.EmbeddingBaseURL != "" || cfg.EmbeddingAPIKey != "" || cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 {
		t.Fatalf("embedding must default to explicitly disabled: %s", cfg)
	}
	if cfg.EmbeddingNormalization != domain.NormalizationL2 || cfg.EmbeddingDistanceMetric != domain.DistanceCosine ||
		cfg.EmbeddingMaxBatchSize != 128 || cfg.EmbeddingMaxInputBytes != 64*1024 ||
		cfg.EmbeddingMaxBatchInputBytes != 8*1024*1024 ||
		cfg.EmbeddingTimeout != 30*time.Second || cfg.EmbeddingMaxResponseBytes != 64<<20 {
		t.Fatalf("unexpected embedding safety defaults: %s", cfg)
	}
	wantRRF := domain.RRFConfig{
		SchemaVersion: 1, Method: domain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 200, VectorCandidateLimit: 200,
		FusedCandidateLimit: 100, RerankCandidateLimit: 50,
	}
	if got := cfg.RetrievalRRFConfig(); got != wantRRF {
		t.Fatalf("unexpected retrieval RRF defaults: %+v", got)
	}
}

func TestLoadWithLookupYAMLThenEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http_addr: 127.0.0.1:9090\ndatabase_url: yaml-value\nhealth_interval: 1m\ngraph_query_timeout: 3s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) (string, bool) {
		if key == "ZHIXU_HTTP_ADDR" {
			return "127.0.0.1:9191", true
		}
		if key == "ZHIXU_DATABASE_URL" {
			return "env-value", true
		}
		if key == "ZHIXU_GRAPH_QUERY_TIMEOUT" {
			return "4s", true
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
	if cfg.GraphQueryTimeout != 4*time.Second {
		t.Fatalf("Graph query timeout environment override not loaded: %s", cfg.GraphQueryTimeout)
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

func TestLoadWithLookupGraphQueryTimeoutYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("graph_query_timeout: 3s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GraphQueryTimeout != 3*time.Second {
		t.Fatalf("Graph query timeout YAML not loaded: %s", cfg.GraphQueryTimeout)
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

func TestLoadWithLookupRejectsInvalidGraphQueryTimeout(t *testing.T) {
	t.Parallel()
	_, err := LoadWithLookup("", func(key string) (string, bool) {
		if key == "ZHIXU_GRAPH_QUERY_TIMEOUT" {
			return "not-a-duration", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "ZHIXU_GRAPH_QUERY_TIMEOUT") {
		t.Fatalf("expected Graph query timeout parse error, got %v", err)
	}
}

func TestValidateRejectsNonPositiveGraphQueryTimeout(t *testing.T) {
	t.Parallel()
	for _, value := range []time.Duration{0, -time.Nanosecond} {
		cfg := Defaults()
		cfg.GraphQueryTimeout = value
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "graph_query_timeout") {
			t.Fatalf("GraphQueryTimeout=%s error=%v", value, err)
		}
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

func TestLoadWithLookupEmbeddingYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`embedding_provider: openai-compatible
embedding_base_url: https://embedding.example.test/v1
embedding_api_key: yaml-key
embedding_model: text-embedding-v3
embedding_dimensions: 1536
embedding_normalization: none
embedding_distance_metric: inner_product
embedding_max_batch_size: 64
embedding_max_input_bytes: 32768
embedding_max_batch_input_bytes: 4194304
embedding_timeout: 45s
embedding_max_response_bytes: 33554432
retrieval_rrf_k: 70
retrieval_rrf_lexical_candidate_limit: 250
retrieval_rrf_vector_candidate_limit: 240
retrieval_rrf_fused_candidate_limit: 120
retrieval_rrf_rerank_candidate_limit: 60
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingProvider != EmbeddingProviderOpenAICompatible || cfg.EmbeddingBaseURL != "https://embedding.example.test/v1" ||
		cfg.EmbeddingAPIKey != "yaml-key" || cfg.EmbeddingModel != "text-embedding-v3" || cfg.EmbeddingDimensions != 1536 ||
		cfg.EmbeddingNormalization != domain.NormalizationNone || cfg.EmbeddingDistanceMetric != domain.DistanceInnerProduct ||
		cfg.EmbeddingMaxBatchSize != 64 || cfg.EmbeddingMaxInputBytes != 32768 || cfg.EmbeddingMaxBatchInputBytes != 4194304 || cfg.EmbeddingTimeout != 45*time.Second ||
		cfg.EmbeddingMaxResponseBytes != 33554432 {
		t.Fatalf("embedding YAML not loaded: %s", cfg)
	}
	if cfg.RetrievalRRFK != 70 || cfg.RetrievalRRFLexicalCandidateLimit != 250 || cfg.RetrievalRRFVectorCandidateLimit != 240 ||
		cfg.RetrievalRRFFusedCandidateLimit != 120 || cfg.RetrievalRRFRerankCandidateLimit != 60 {
		t.Fatalf("retrieval RRF YAML not loaded: %s", cfg)
	}
}

func TestLoadWithLookupEmbeddingEnvironment(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"ZHIXU_EMBEDDING_PROVIDER":                    "ollama",
		"ZHIXU_EMBEDDING_BASE_URL":                    "http://127.0.0.1:11434/models",
		"ZHIXU_EMBEDDING_MODEL":                       "nomic-embed-text",
		"ZHIXU_EMBEDDING_DIMENSIONS":                  "768",
		"ZHIXU_EMBEDDING_NORMALIZATION":               "l2",
		"ZHIXU_EMBEDDING_DISTANCE_METRIC":             "cosine",
		"ZHIXU_EMBEDDING_MAX_BATCH_SIZE":              "32",
		"ZHIXU_EMBEDDING_MAX_INPUT_BYTES":             "16384",
		"ZHIXU_EMBEDDING_MAX_BATCH_INPUT_BYTES":       "2097152",
		"ZHIXU_EMBEDDING_TIMEOUT":                     "20s",
		"ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES":          "16777216",
		"ZHIXU_RETRIEVAL_RRF_K":                       "55",
		"ZHIXU_RETRIEVAL_RRF_LEXICAL_CANDIDATE_LIMIT": "180",
		"ZHIXU_RETRIEVAL_RRF_VECTOR_CANDIDATE_LIMIT":  "170",
		"ZHIXU_RETRIEVAL_RRF_FUSED_CANDIDATE_LIMIT":   "90",
		"ZHIXU_RETRIEVAL_RRF_RERANK_CANDIDATE_LIMIT":  "40",
	}
	cfg, err := LoadWithLookup("", func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingProvider != EmbeddingProviderOllama || cfg.EmbeddingBaseURL != "http://127.0.0.1:11434/models" ||
		cfg.EmbeddingAPIKey != "" || cfg.EmbeddingModel != "nomic-embed-text" || cfg.EmbeddingDimensions != 768 ||
		cfg.EmbeddingMaxBatchSize != 32 || cfg.EmbeddingMaxInputBytes != 16384 || cfg.EmbeddingMaxBatchInputBytes != 2097152 || cfg.EmbeddingTimeout != 20*time.Second ||
		cfg.EmbeddingMaxResponseBytes != 16777216 || cfg.RetrievalRRFK != 55 || cfg.RetrievalRRFRerankCandidateLimit != 40 {
		t.Fatalf("embedding environment not loaded: %s", cfg)
	}
}

func TestLoadWithLookupDisabledEmbeddingDoesNotConsumeSecrets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("embedding_provider: openai-compatible\nembedding_base_url: https://yaml-secret.example.test\nembedding_api_key: yaml-secret\nembedding_model: yaml-model\nembedding_dimensions: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		switch key {
		case "ZHIXU_EMBEDDING_PROVIDER":
			return "disabled", true
		case "ZHIXU_EMBEDDING_BASE_URL":
			return "https://env-secret.example.test", true
		case "ZHIXU_EMBEDDING_API_KEY":
			return "env-secret", true
		case "ZHIXU_EMBEDDING_DIMENSIONS":
			return "not-a-number", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingBaseURL != "" || cfg.EmbeddingAPIKey != "" || cfg.EmbeddingModel != "" || cfg.EmbeddingDimensions != 0 {
		t.Fatalf("disabled embedding must not consume provider settings: %s", cfg)
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

func TestValidateEmbeddingProviderGroups(t *testing.T) {
	t.Parallel()
	enabled := func(provider EmbeddingProvider) Config {
		cfg := Defaults()
		cfg.EmbeddingProvider = provider
		cfg.EmbeddingBaseURL = "https://embedding.example.test/base"
		cfg.EmbeddingModel = "embed-v1"
		cfg.EmbeddingDimensions = 3
		if provider == EmbeddingProviderOpenAICompatible {
			cfg.EmbeddingAPIKey = "test-key"
		}
		return cfg
	}
	valid := []Config{
		Defaults(),
		enabled(EmbeddingProviderOpenAICompatible),
		func() Config {
			cfg := enabled(EmbeddingProviderOllama)
			cfg.EmbeddingBaseURL = "http://localhost:11434"
			return cfg
		}(),
	}
	for _, cfg := range valid {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid embedding group rejected: %s: %v", cfg, err)
		}
	}

	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "unknown provider", change: func(c *Config) { c.EmbeddingProvider = "other" }, want: "embedding_provider"},
		{name: "disabled endpoint", change: func(c *Config) { c.EmbeddingBaseURL = "https://secret.example.test" }, want: "must be empty"},
		{name: "openai missing key", change: func(c *Config) { *c = enabled(EmbeddingProviderOpenAICompatible); c.EmbeddingAPIKey = "" }, want: "embedding_api_key"},
		{name: "openai key whitespace", change: func(c *Config) { *c = enabled(EmbeddingProviderOpenAICompatible); c.EmbeddingAPIKey = " key " }, want: "embedding_api_key"},
		{name: "openai loopback http", change: func(c *Config) {
			*c = enabled(EmbeddingProviderOpenAICompatible)
			c.EmbeddingBaseURL = "http://localhost:8080"
		}, want: "https"},
		{name: "ollama remote http", change: func(c *Config) {
			*c = enabled(EmbeddingProviderOllama)
			c.EmbeddingBaseURL = "http://models.example.test"
		}, want: "https"},
		{name: "ollama api key", change: func(c *Config) { *c = enabled(EmbeddingProviderOllama); c.EmbeddingAPIKey = "not-allowed" }, want: "embedding_api_key"},
		{name: "endpoint userinfo", change: func(c *Config) {
			*c = enabled(EmbeddingProviderOpenAICompatible)
			c.EmbeddingBaseURL = "https://user:secret@models.example.test"
		}, want: "embedding_base_url"},
		{name: "endpoint query", change: func(c *Config) {
			*c = enabled(EmbeddingProviderOpenAICompatible)
			c.EmbeddingBaseURL = "https://models.example.test?secret=query"
		}, want: "embedding_base_url"},
		{name: "endpoint fragment", change: func(c *Config) {
			*c = enabled(EmbeddingProviderOpenAICompatible)
			c.EmbeddingBaseURL = "https://models.example.test#secret"
		}, want: "embedding_base_url"},
		{name: "empty model", change: func(c *Config) { *c = enabled(EmbeddingProviderOllama); c.EmbeddingModel = "" }, want: "embedding_model"},
		{name: "model whitespace", change: func(c *Config) { *c = enabled(EmbeddingProviderOllama); c.EmbeddingModel = " model " }, want: "embedding_model"},
		{name: "zero dimensions", change: func(c *Config) { *c = enabled(EmbeddingProviderOllama); c.EmbeddingDimensions = 0 }, want: "embedding_dimensions"},
		{name: "dimensions too large", change: func(c *Config) { *c = enabled(EmbeddingProviderOllama); c.EmbeddingDimensions = 16_001 }, want: "embedding_dimensions"},
		{name: "bad normalization", change: func(c *Config) { c.EmbeddingNormalization = "unit" }, want: "embedding_normalization"},
		{name: "bad distance", change: func(c *Config) { c.EmbeddingDistanceMetric = "manhattan" }, want: "embedding_distance_metric"},
		{name: "zero batch", change: func(c *Config) { c.EmbeddingMaxBatchSize = 0 }, want: "embedding_max_batch_size"},
		{name: "batch too large", change: func(c *Config) { c.EmbeddingMaxBatchSize = domain.MaxEmbeddingBatchSize + 1 }, want: "embedding_max_batch_size"},
		{name: "zero input bytes", change: func(c *Config) { c.EmbeddingMaxInputBytes = 0 }, want: "embedding_max_input_bytes"},
		{name: "input too large", change: func(c *Config) { c.EmbeddingMaxInputBytes = domain.MaxEmbeddingInputBytes + 1 }, want: "embedding_max_input_bytes"},
		{name: "batch input below single input", change: func(c *Config) { c.EmbeddingMaxBatchInputBytes = int64(c.EmbeddingMaxInputBytes) - 1 }, want: "embedding_max_batch_input_bytes"},
		{name: "batch input too large", change: func(c *Config) { c.EmbeddingMaxBatchInputBytes = domain.MaxEmbeddingBatchInputBytes + 1 }, want: "embedding_max_batch_input_bytes"},
		{name: "zero timeout", change: func(c *Config) { c.EmbeddingTimeout = 0 }, want: "embedding_timeout"},
		{name: "timeout too large", change: func(c *Config) { c.EmbeddingTimeout = 5*time.Minute + time.Nanosecond }, want: "embedding_timeout"},
		{name: "zero response", change: func(c *Config) { c.EmbeddingMaxResponseBytes = 0 }, want: "embedding_max_response_bytes"},
		{name: "response too large", change: func(c *Config) { c.EmbeddingMaxResponseBytes = (128 << 20) + 1 }, want: "embedding_max_response_bytes"},
		{name: "rrf zero k", change: func(c *Config) { c.RetrievalRRFK = 0 }, want: "retrieval RRF"},
		{name: "rrf k too large", change: func(c *Config) { c.RetrievalRRFK = domain.MaxRRFK + 1 }, want: "retrieval RRF"},
		{name: "rrf candidate too large", change: func(c *Config) { c.RetrievalRRFVectorCandidateLimit = domain.MaxFusionCandidateLimit + 1 }, want: "retrieval RRF"},
		{name: "rrf rerank exceeds fused", change: func(c *Config) { c.RetrievalRRFRerankCandidateLimit = c.RetrievalRRFFusedCandidateLimit + 1 }, want: "retrieval RRF"},
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

func TestLoadWithLookupRejectsInvalidEmbeddingEnvironmentWithoutEchoingValue(t *testing.T) {
	t.Parallel()
	const secret = "numeric-secret-canary"
	tests := []string{
		"ZHIXU_EMBEDDING_DIMENSIONS",
		"ZHIXU_EMBEDDING_MAX_BATCH_SIZE",
		"ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES",
		"ZHIXU_EMBEDDING_TIMEOUT",
		"ZHIXU_RETRIEVAL_RRF_K",
	}
	for _, key := range tests {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, err := LoadWithLookup("", func(candidate string) (string, bool) {
				switch candidate {
				case "ZHIXU_EMBEDDING_PROVIDER":
					return "ollama", true
				case "ZHIXU_EMBEDDING_BASE_URL":
					return "http://localhost:11434", true
				case "ZHIXU_EMBEDDING_MODEL":
					return "embed-v1", true
				case "ZHIXU_EMBEDDING_DIMENSIONS":
					if key != candidate {
						return "3", true
					}
				}
				if candidate == key {
					return secret, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), secret) {
				t.Fatalf("expected secret-safe parse error for %s, got %v", key, err)
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
		embeddingKey   = "embedding-api-key-secret"
		embeddingURL   = "https://embedding-endpoint-secret.example.test/private"
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
	cfg.GraphQueryTimeout = 3 * time.Second
	cfg.EmbeddingProvider = EmbeddingProviderOpenAICompatible
	cfg.EmbeddingBaseURL = embeddingURL
	cfg.EmbeddingAPIKey = embeddingKey
	cfg.EmbeddingModel = "embed-v1"
	cfg.EmbeddingDimensions = 3
	for _, formatted := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		for _, secret := range []string{password, databaseURL, telemetryToken, embeddingKey, embeddingURL} {
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
			"GraphQueryTimeout:3s",
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
	cfg.TelemetryMode = TelemetryModeDisabled
	cfg.TelemetryEndpoint = ""
	cfg.EmbeddingBaseURL = "https://embedding-url-secret@%zz"
	if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "embedding-url-secret") || strings.Contains(err.Error(), embeddingKey) {
		t.Fatalf("embedding validation error must be stable and secret-safe: %v", err)
	}
}

func TestValidateDatabaseRequiresExplicitURL(t *testing.T) {
	t.Parallel()
	if err := Defaults().ValidateDatabase(); err == nil {
		t.Fatal("expected missing database URL error")
	}
}

func TestAuthDefaultsAreExplicitlyDisabledOnlyForDevelopmentLoopback(t *testing.T) {
	cfg := Defaults()
	if cfg.AuthMode != AuthModeDisabled || cfg.AuthSessionTTL <= 0 || cfg.AuthAPITokenTTL <= 0 {
		t.Fatalf("auth defaults=%+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development loopback defaults should validate: %v", err)
	}
	for _, mutate := range []func(*Config){
		func(value *Config) { value.Environment = "production" },
		func(value *Config) { value.HTTPAddr = "0.0.0.0:8080" },
	} {
		invalid := cfg
		mutate(&invalid)
		if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "auth_mode disabled") {
			t.Fatalf("expected disabled auth rejection, cfg=%+v err=%v", invalid, err)
		}
	}
}

func TestRequiredAuthLoadsSecretsAndOriginsWithoutEchoingBootstrap(t *testing.T) {
	bootstrap := "a-development-bootstrap-token-with-more-than-32-bytes"
	questionRefKey := "a-shared-review-question-reference-key-with-more-than-32-bytes"
	values := map[string]string{
		"ZHIXU_AUTH_MODE":               string(AuthModeRequired),
		"ZHIXU_AUTH_BOOTSTRAP_TOKEN":    bootstrap,
		"ZHIXU_REVIEW_QUESTION_REF_KEY": questionRefKey,
		"ZHIXU_AUTH_ALLOWED_ORIGINS":    "https://127.0.0.1:8080,https://example.test",
		"ZHIXU_AUTH_SECURE_COOKIE":      "true",
		"ZHIXU_AUTH_SESSION_TTL":        "6h",
		"ZHIXU_AUTH_API_TOKEN_TTL":      "48h",
		"ZHIXU_ENVIRONMENT":             "production",
		"ZHIXU_HTTP_ADDR":               "0.0.0.0:8080",
	}
	cfg, err := LoadWithLookup("", func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthMode != AuthModeRequired || cfg.AuthBootstrapToken != bootstrap || cfg.ReviewQuestionRefKey != questionRefKey || len(cfg.AuthAllowedOrigins) != 2 || !cfg.AuthSecureCookie || cfg.AuthSessionTTL != 6*time.Hour || cfg.AuthAPITokenTTL != 48*time.Hour {
		t.Fatalf("loaded auth config=%+v", cfg)
	}
	for _, secret := range []string{bootstrap, questionRefKey} {
		if strings.Contains(cfg.String(), secret) {
			t.Fatalf("config String leaked an API-only secret: %s", cfg.String())
		}
	}
}

func TestAPIConfigMaterializesStableReviewQuestionRefKey(t *testing.T) {
	loadRequired := func(bootstrap string) Config {
		t.Helper()
		cfg, err := LoadWithLookup("", func(key string) (string, bool) {
			switch key {
			case "ZHIXU_AUTH_MODE":
				return string(AuthModeRequired), true
			case "ZHIXU_AUTH_BOOTSTRAP_TOKEN":
				return bootstrap, true
			default:
				return "", false
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	bootstrap := "a-stable-bootstrap-token-with-more-than-32-bytes"
	first := loadRequired(bootstrap)
	second := loadRequired(bootstrap)
	rotated := loadRequired(bootstrap + "-rotated")
	if len(first.ReviewQuestionRefKey) < 32 || first.ReviewQuestionRefKey != second.ReviewQuestionRefKey {
		t.Fatal("required API loads must derive one stable 32+ byte Review question-reference key")
	}
	if first.ReviewQuestionRefKey == bootstrap || first.ReviewQuestionRefKey == rotated.ReviewQuestionRefKey {
		t.Fatal("Review question-reference key derivation must be domain-separated and Bootstrap-bound")
	}
	if strings.Contains(first.String(), first.ReviewQuestionRefKey) {
		t.Fatal("Config.String leaked the derived Review question-reference key")
	}
}

func TestReviewQuestionRefKeyValidationUsesCanonicalUTF8Bytes(t *testing.T) {
	validUTF8 := "密密密密密密密密密密密"
	cfg, err := LoadWithLookup("", func(key string) (string, bool) {
		if key == "ZHIXU_REVIEW_QUESTION_REF_KEY" {
			return validUTF8, true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("32+ UTF-8 byte key was rejected: %v", err)
	}
	if cfg.ReviewQuestionRefKey != validUTF8 {
		t.Fatal("explicit Review question-reference key was not preserved")
	}

	for name, key := range map[string]string{
		"empty":               "",
		"short":               strings.Repeat("k", 31),
		"leading whitespace":  " " + strings.Repeat("k", 32),
		"trailing whitespace": strings.Repeat("k", 32) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadWithLookup("", func(candidate string) (string, bool) {
				if candidate == "ZHIXU_REVIEW_QUESTION_REF_KEY" {
					return key, true
				}
				return "", false
			})
			leaked := err != nil && key != "" && strings.Contains(err.Error(), key)
			if err == nil || !strings.Contains(err.Error(), "review_question_ref_key") || leaked {
				t.Fatalf("invalid Review question-reference key produced an unsafe error: %v", err)
			}
		})
	}
}

func TestDisabledAuthRejectsAnyExplicitBootstrapValue(t *testing.T) {
	for _, bootstrap := range []string{
		"a-disabled-bootstrap-token-with-more-than-32-bytes",
		"   ",
	} {
		_, err := LoadWithLookup("", func(key string) (string, bool) {
			switch key {
			case "ZHIXU_AUTH_MODE":
				return string(AuthModeDisabled), true
			case "ZHIXU_AUTH_BOOTSTRAP_TOKEN":
				return bootstrap, true
			default:
				return "", false
			}
		})
		if err == nil || !strings.Contains(err.Error(), "auth_bootstrap_token must be empty") {
			t.Fatalf("disabled auth accepted an explicit Bootstrap value: %v", err)
		}
	}
}

func TestNonAPIConfigDoesNotConsumeAPISecrets(t *testing.T) {
	secretLookups := map[string]bool{
		"ZHIXU_AUTH_BOOTSTRAP_TOKEN":    false,
		"ZHIXU_REVIEW_QUESTION_REF_KEY": false,
	}
	cfg, err := loadNonAPIWithLookup("", func(key string) (string, bool) {
		switch key {
		case "ZHIXU_ENVIRONMENT":
			return "production", true
		case "ZHIXU_AUTH_MODE":
			return string(AuthModeRequired), true
		case "ZHIXU_AUTH_BOOTSTRAP_TOKEN", "ZHIXU_REVIEW_QUESTION_REF_KEY":
			secretLookups[key] = true
			return "worker-must-never-consume-this-api-secret", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("non-API configuration should load without API-only auth validation: %v", err)
	}
	if secretLookups["ZHIXU_AUTH_BOOTSTRAP_TOKEN"] || secretLookups["ZHIXU_REVIEW_QUESTION_REF_KEY"] || cfg.AuthBootstrapToken != "" || cfg.ReviewQuestionRefKey != "" {
		t.Fatalf("non-API configuration consumed an API-only secret: lookups=%v bootstrap_configured=%t question_ref_configured=%t", secretLookups, cfg.AuthBootstrapToken != "", cfg.ReviewQuestionRefKey != "")
	}
	if cfg.Environment != "production" || cfg.AuthMode != AuthModeRequired {
		t.Fatalf("worker lost non-secret runtime configuration: %+v", cfg)
	}
}

func TestRequiredAuthRejectsUnsafeProductionConfiguration(t *testing.T) {
	base := Defaults()
	base.AuthMode = AuthModeRequired
	base.AuthBootstrapToken = "a-development-bootstrap-token-with-more-than-32-bytes"
	base.AuthAllowedOrigins = []string{"http://example.test"}
	for name, mutate := range map[string]func(*Config){
		"insecure cookie": func(value *Config) { value.Environment = "production"; value.AuthSecureCookie = false },
		"http origin":     func(value *Config) { value.Environment = "production"; value.AuthSecureCookie = true },
		"short bootstrap": func(value *Config) { value.AuthBootstrapToken = "short" },
		"bad origin":      func(value *Config) { value.AuthAllowedOrigins = []string{"https://example.test/path"} },
		"default https port": func(value *Config) {
			value.AuthAllowedOrigins = []string{"https://example.test:443"}
		},
		"default http port": func(value *Config) {
			value.AuthAllowedOrigins = []string{"http://example.test:80"}
		},
		"uppercase origin host": func(value *Config) {
			value.AuthAllowedOrigins = []string{"https://EXAMPLE.test"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := base
			mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("unsafe auth configuration was accepted")
			}
		})
	}
}

func TestRequiredAuthRejectsNonCanonicalOriginFromEnvironment(t *testing.T) {
	for _, origin := range []string{
		"http://example.test:80",
		"https://example.test:443",
		"http://127.000.000.001",
		"http://[0:0:0:0:0:0:0:1]:8080",
		"https://b\u00fccher.example",
	} {
		t.Run(origin, func(t *testing.T) {
			_, err := LoadWithLookup("", func(key string) (string, bool) {
				switch key {
				case "ZHIXU_AUTH_MODE":
					return string(AuthModeRequired), true
				case "ZHIXU_AUTH_BOOTSTRAP_TOKEN":
					return "a-development-bootstrap-token-with-more-than-32-bytes", true
				case "ZHIXU_AUTH_ALLOWED_ORIGINS":
					return origin, true
				case "ZHIXU_AUTH_SECURE_COOKIE":
					return "true", true
				case "ZHIXU_ENVIRONMENT":
					return "development", true
				default:
					return "", false
				}
			})
			if err == nil || !strings.Contains(err.Error(), "invalid exact origin") {
				t.Fatalf("LoadWithLookup accepted non-canonical origin %q: %v", origin, err)
			}
		})
	}
}

func TestRequiredAuthRestrictsInsecureDevelopmentConfigurationToLoopbackOrigins(t *testing.T) {
	base := Defaults()
	base.AuthMode = AuthModeRequired
	base.AuthBootstrapToken = "a-development-bootstrap-token-with-more-than-32-bytes"
	base.AuthSecureCookie = false
	base.AuthAllowedOrigins = []string{"http://127.0.0.1:8080"}

	tests := map[string]func(*Config){
		"non-loopback origin":   func(value *Config) { value.AuthAllowedOrigins = []string{"http://example.test"} },
		"non-loopback listener": func(value *Config) { value.HTTPAddr = "0.0.0.0:8080" },
		"https loopback origin": func(value *Config) { value.AuthAllowedOrigins = []string{"https://127.0.0.1:8080"} },
		"production label":      func(value *Config) { value.Environment = "production" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			invalid := base
			mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("insecure non-loopback auth configuration was accepted")
			}
		})
	}
	for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		valid := base
		valid.HTTPAddr = address
		if err := valid.Validate(); err != nil {
			t.Fatalf("development loopback-origin auth should support listener %s: %v", address, err)
		}
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
