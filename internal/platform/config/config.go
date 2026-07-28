// Package config loads and validates process configuration.
package config

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authorigin "github.com/CodeZen-Lizhi/zhixu/internal/auth/origin"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/operability"

	"gopkg.in/yaml.v3"
)

const (
	defaultHTTPAddr                   = "127.0.0.1:8080"
	defaultVersion                    = "dev"
	defaultPingTimeout                = 2 * time.Second
	defaultGraphQueryTimeout          = 2 * time.Second
	defaultHealthInterval             = 15 * time.Second
	defaultShutdownTimeout            = 10 * time.Second
	defaultMaxConns                   = int32(10)
	defaultMinConns                   = int32(1)
	defaultWorkerQueue                = "workflow"
	defaultWorkerMaxWorkers           = 4
	defaultWorkerJobTimeout           = 15 * time.Minute
	defaultWorkerRescueStuckJobsAfter = 30 * time.Minute
	defaultWorkflowLeaseDuration      = 2 * time.Minute
	defaultWorkflowHeartbeatInterval  = 30 * time.Second
	defaultWorkerSoftStopTimeout      = 30 * time.Second
	defaultWorkerHardStopTimeout      = 60 * time.Second
	defaultWorkerHealthAddr           = "0.0.0.0:8081"
)

// AuthMode 控制 API 是否要求单用户身份认证。
type AuthMode string

const (
	// AuthModeDisabled 仅允许开发环境的 loopback 使用。
	AuthModeDisabled AuthMode = "disabled"
	// AuthModeRequired 要求配置 Bootstrap Token 和安全 Cookie 边界。
	AuthModeRequired AuthMode = "required"
)

const (
	defaultEmbeddingMaxBatchSize       = int32(128)
	defaultEmbeddingMaxInputBytes      = int32(64 * 1024)
	defaultEmbeddingMaxBatchInputBytes = int64(8 * 1024 * 1024)
	defaultEmbeddingTimeout            = 30 * time.Second
	defaultEmbeddingMaxResponseBytes   = int64(64 << 20)

	defaultRetrievalRRFK                     = int32(60)
	defaultRetrievalRRFLexicalCandidateLimit = int32(200)
	defaultRetrievalRRFVectorCandidateLimit  = int32(200)
	defaultRetrievalRRFFusedCandidateLimit   = int32(100)
	defaultRetrievalRRFRerankCandidateLimit  = int32(50)

	maxEmbeddingDimensions       = int32(16_000)
	maxEmbeddingTimeout          = 5 * time.Minute
	maxEmbeddingMaxResponseBytes = int64(128 << 20)

	defaultChatAdapterVersion   = "v1"
	defaultChatTimeout          = 30 * time.Second
	defaultChatMaxRequestBytes  = int64(4 << 20)
	defaultChatMaxResponseBytes = int64(4 << 20)
	maxChatTimeout              = 5 * time.Minute
	maxChatRequestBytes         = int64(16 << 20)
	maxChatResponseBytes        = int64(16 << 20)

	defaultWebFetchTimeout                = 30 * time.Second
	defaultWebFetchResponseHeaderTimeout  = 10 * time.Second
	defaultWebFetchTLSHandshakeTimeout    = 10 * time.Second
	defaultWebFetchMaxRedirects           = 5
	defaultWebFetchMaxURLBytes            = 8 * 1024
	defaultWebFetchMaxResponseHeaderBytes = int64(64 * 1024)
	defaultWebFetchMaxBodyBytes           = int64(2 * 1024 * 1024)
	defaultWebFetchMaxTextBytes           = 512 * 1024
	defaultWebFetchMaxResolvedIPs         = 16

	maxWebFetchTimeout               = 2 * time.Minute
	maxWebFetchResponseHeaderTimeout = 30 * time.Second
	maxWebFetchTLSHandshakeTimeout   = 30 * time.Second
	maxWebFetchRedirects             = 20
	maxWebFetchURLBytes              = 64 * 1024
	maxWebFetchResponseHeaderBytes   = int64(2 * 1024 * 1024)
	maxWebFetchBodyBytes             = int64(32 * 1024 * 1024)
	maxWebFetchResolvedIPs           = 64
	maxWebFetchAllowedContentTypes   = 2
)

const (
	defaultReindexDispatchPollInterval = time.Second
	defaultReindexDispatchBatchSize    = 10
	defaultReindexDispatchErrorBackoff = 5 * time.Second
	defaultReindexLeaseDuration        = 2 * time.Minute
	defaultReindexHeartbeatInterval    = 30 * time.Second

	maxReindexDispatchPollInterval = time.Minute
	maxReindexDispatchBatchSize    = 1_000
	maxReindexDispatchErrorBackoff = time.Minute
	maxReindexLeaseDuration        = 24 * time.Hour
	maxReindexHeartbeatInterval    = time.Minute
)

// TelemetryMode controls whether telemetry export is disabled or required for
// process readiness.
type TelemetryMode string

const (
	// TelemetryModeDisabled prevents exporter construction.
	TelemetryModeDisabled TelemetryMode = "disabled"
	// TelemetryModeOptional enables export without making exporter availability
	// a readiness requirement.
	TelemetryModeOptional TelemetryMode = "optional"
	// TelemetryModeRequired makes exporter availability a readiness requirement.
	TelemetryModeRequired TelemetryMode = "required"
)

// EmbeddingProvider controls whether and how production embeddings are
// generated.
type EmbeddingProvider string

const (
	// EmbeddingProviderDisabled keeps new indexes FTS-only.
	EmbeddingProviderDisabled EmbeddingProvider = "disabled"
	// EmbeddingProviderOpenAICompatible uses the OpenAI-compatible HTTP protocol.
	EmbeddingProviderOpenAICompatible EmbeddingProvider = "openai-compatible"
	// EmbeddingProviderOllama uses Ollama's native HTTP protocol.
	EmbeddingProviderOllama EmbeddingProvider = "ollama"
)

// ChatProvider 控制生产环境是否以及如何执行结构化 Chat 调用。
type ChatProvider string

const (
	// ChatProviderDisabled 明确关闭 Chat capability。
	ChatProviderDisabled ChatProvider = "disabled"
	// ChatProviderOpenAICompatible 使用 OpenAI-Compatible HTTP 协议。
	ChatProviderOpenAICompatible ChatProvider = "openai-compatible"
)

// ToolMode 控制 Tool runtime 或单项 Tool capability 是否进入生产组合。
type ToolMode string

const (
	// ToolModeDisabled 明确关闭对应 Tool runtime 或 capability。
	ToolModeDisabled ToolMode = "disabled"
	// ToolModeEnabled 允许 Composition Root 继续验证并组合对应能力。
	ToolModeEnabled ToolMode = "enabled"
)

// Config contains process settings, including connection secrets. Callers must
// use String or GoString rather than serializing the struct for diagnostics.
type Config struct {
	AppName                    string        `yaml:"app_name"`
	Version                    string        `yaml:"version"`
	Environment                string        `yaml:"environment"`
	HTTPAddr                   string        `yaml:"http_addr"`
	DatabaseURL                string        `yaml:"database_url"`
	DatabaseHost               string        `yaml:"database_host"`
	DatabasePort               string        `yaml:"database_port"`
	DatabaseName               string        `yaml:"database_name"`
	DatabaseUser               string        `yaml:"database_user"`
	DatabasePassword           string        `yaml:"database_password"`
	DatabaseMaxConns           int32         `yaml:"database_max_conns"`
	DatabaseMinConns           int32         `yaml:"database_min_conns"`
	DatabasePingTimeout        time.Duration `yaml:"database_ping_timeout"`
	GraphQueryTimeout          time.Duration `yaml:"graph_query_timeout"`
	HealthInterval             time.Duration `yaml:"health_interval"`
	ShutdownTimeout            time.Duration `yaml:"shutdown_timeout"`
	WebAssetsDir               string        `yaml:"web_assets_dir"`
	WorkerQueue                string        `yaml:"worker_queue"`
	WorkerMaxWorkers           int           `yaml:"worker_max_workers"`
	WorkerJobTimeout           time.Duration `yaml:"worker_job_timeout"`
	WorkerRescueStuckJobsAfter time.Duration `yaml:"worker_rescue_stuck_jobs_after"`
	WorkflowLeaseDuration      time.Duration `yaml:"workflow_lease"`
	WorkflowHeartbeatInterval  time.Duration `yaml:"workflow_heartbeat"`

	ReindexDispatchPollInterval time.Duration `yaml:"reindex_dispatch_poll_interval"`
	ReindexDispatchBatchSize    int           `yaml:"reindex_dispatch_batch_size"`
	ReindexDispatchErrorBackoff time.Duration `yaml:"reindex_dispatch_error_backoff"`
	ReindexLeaseDuration        time.Duration `yaml:"reindex_lease_duration"`
	ReindexHeartbeatInterval    time.Duration `yaml:"reindex_heartbeat_interval"`

	EmbeddingProvider           EmbeddingProvider             `yaml:"embedding_provider"`
	EmbeddingBaseURL            string                        `yaml:"embedding_base_url"`
	EmbeddingAPIKey             string                        `yaml:"embedding_api_key"`
	EmbeddingModel              string                        `yaml:"embedding_model"`
	EmbeddingDimensions         int32                         `yaml:"embedding_dimensions"`
	EmbeddingNormalization      domain.EmbeddingNormalization `yaml:"embedding_normalization"`
	EmbeddingDistanceMetric     domain.DistanceMetric         `yaml:"embedding_distance_metric"`
	EmbeddingMaxBatchSize       int32                         `yaml:"embedding_max_batch_size"`
	EmbeddingMaxInputBytes      int32                         `yaml:"embedding_max_input_bytes"`
	EmbeddingMaxBatchInputBytes int64                         `yaml:"embedding_max_batch_input_bytes"`
	EmbeddingTimeout            time.Duration                 `yaml:"embedding_timeout"`
	EmbeddingMaxResponseBytes   int64                         `yaml:"embedding_max_response_bytes"`

	ChatProvider         ChatProvider  `yaml:"chat_provider"`
	ChatBaseURL          string        `yaml:"chat_base_url"`
	ChatAPIKey           string        `yaml:"chat_api_key"`
	ChatModel            string        `yaml:"chat_model"`
	ChatModelVersion     string        `yaml:"chat_model_version"`
	ChatAdapterVersion   string        `yaml:"chat_adapter_version"`
	ChatTimeout          time.Duration `yaml:"chat_timeout"`
	ChatMaxRequestBytes  int64         `yaml:"chat_max_request_bytes"`
	ChatMaxResponseBytes int64         `yaml:"chat_max_response_bytes"`

	ToolRuntimeMode ToolMode `yaml:"tool_runtime_mode"`
	WebFetchMode    ToolMode `yaml:"web_fetch_mode"`

	WebFetchTimeout                time.Duration `yaml:"web_fetch_timeout"`
	WebFetchResponseHeaderTimeout  time.Duration `yaml:"web_fetch_response_header_timeout"`
	WebFetchTLSHandshakeTimeout    time.Duration `yaml:"web_fetch_tls_handshake_timeout"`
	WebFetchMaxRedirects           int           `yaml:"web_fetch_max_redirects"`
	WebFetchMaxURLBytes            int           `yaml:"web_fetch_max_url_bytes"`
	WebFetchMaxResponseHeaderBytes int64         `yaml:"web_fetch_max_response_header_bytes"`
	WebFetchMaxBodyBytes           int64         `yaml:"web_fetch_max_body_bytes"`
	WebFetchMaxTextBytes           int           `yaml:"web_fetch_max_text_bytes"`
	WebFetchMaxResolvedIPs         int           `yaml:"web_fetch_max_resolved_ips"`
	WebFetchAllowedContentTypes    []string      `yaml:"web_fetch_allowed_content_types"`

	RetrievalRRFK                     int32 `yaml:"retrieval_rrf_k"`
	RetrievalRRFLexicalCandidateLimit int32 `yaml:"retrieval_rrf_lexical_candidate_limit"`
	RetrievalRRFVectorCandidateLimit  int32 `yaml:"retrieval_rrf_vector_candidate_limit"`
	RetrievalRRFFusedCandidateLimit   int32 `yaml:"retrieval_rrf_fused_candidate_limit"`
	RetrievalRRFRerankCandidateLimit  int32 `yaml:"retrieval_rrf_rerank_candidate_limit"`

	WorkerSoftStopTimeout time.Duration `yaml:"worker_soft_stop_timeout"`
	WorkerHardStopTimeout time.Duration `yaml:"worker_hard_stop_timeout"`
	WorkerHealthAddr      string        `yaml:"worker_health_addr"`
	TelemetryMode         TelemetryMode `yaml:"telemetry_mode"`
	TelemetryEndpoint     string        `yaml:"telemetry_endpoint"`

	AuthMode           AuthMode      `yaml:"auth_mode"`
	AuthBootstrapToken string        `yaml:"auth_bootstrap_token"`
	AuthSessionTTL     time.Duration `yaml:"auth_session_ttl"`
	AuthAPITokenTTL    time.Duration `yaml:"auth_api_token_ttl"`
	AuthSecureCookie   bool          `yaml:"auth_secure_cookie"`
	AuthAllowedOrigins []string      `yaml:"auth_allowed_origins"`

	ReviewQuestionRefKey string `yaml:"review_question_ref_key"`
	// reviewQuestionRefKeyExplicit 区分缺省与显式空值，防止错误配置被静默随机化。
	reviewQuestionRefKeyExplicit bool
}

// Defaults returns safe non-sensitive defaults. It intentionally leaves the
// database URL empty so a process cannot silently connect to an unknown DB.
func Defaults() Config {
	return Config{
		AppName:                    "zhixu",
		Version:                    defaultVersion,
		Environment:                "development",
		HTTPAddr:                   defaultHTTPAddr,
		DatabaseMaxConns:           defaultMaxConns,
		DatabaseMinConns:           defaultMinConns,
		DatabasePingTimeout:        defaultPingTimeout,
		GraphQueryTimeout:          defaultGraphQueryTimeout,
		HealthInterval:             defaultHealthInterval,
		ShutdownTimeout:            defaultShutdownTimeout,
		WorkerQueue:                defaultWorkerQueue,
		WorkerMaxWorkers:           defaultWorkerMaxWorkers,
		WorkerJobTimeout:           defaultWorkerJobTimeout,
		WorkerRescueStuckJobsAfter: defaultWorkerRescueStuckJobsAfter,
		WorkflowLeaseDuration:      defaultWorkflowLeaseDuration,
		WorkflowHeartbeatInterval:  defaultWorkflowHeartbeatInterval,

		ReindexDispatchPollInterval: defaultReindexDispatchPollInterval,
		ReindexDispatchBatchSize:    defaultReindexDispatchBatchSize,
		ReindexDispatchErrorBackoff: defaultReindexDispatchErrorBackoff,
		ReindexLeaseDuration:        defaultReindexLeaseDuration,
		ReindexHeartbeatInterval:    defaultReindexHeartbeatInterval,

		EmbeddingProvider:           EmbeddingProviderDisabled,
		EmbeddingNormalization:      domain.NormalizationL2,
		EmbeddingDistanceMetric:     domain.DistanceCosine,
		EmbeddingMaxBatchSize:       defaultEmbeddingMaxBatchSize,
		EmbeddingMaxInputBytes:      defaultEmbeddingMaxInputBytes,
		EmbeddingMaxBatchInputBytes: defaultEmbeddingMaxBatchInputBytes,
		EmbeddingTimeout:            defaultEmbeddingTimeout,
		EmbeddingMaxResponseBytes:   defaultEmbeddingMaxResponseBytes,

		ChatProvider:         ChatProviderDisabled,
		ChatAdapterVersion:   defaultChatAdapterVersion,
		ChatTimeout:          defaultChatTimeout,
		ChatMaxRequestBytes:  defaultChatMaxRequestBytes,
		ChatMaxResponseBytes: defaultChatMaxResponseBytes,

		ToolRuntimeMode: ToolModeDisabled,
		WebFetchMode:    ToolModeDisabled,

		WebFetchTimeout:                defaultWebFetchTimeout,
		WebFetchResponseHeaderTimeout:  defaultWebFetchResponseHeaderTimeout,
		WebFetchTLSHandshakeTimeout:    defaultWebFetchTLSHandshakeTimeout,
		WebFetchMaxRedirects:           defaultWebFetchMaxRedirects,
		WebFetchMaxURLBytes:            defaultWebFetchMaxURLBytes,
		WebFetchMaxResponseHeaderBytes: defaultWebFetchMaxResponseHeaderBytes,
		WebFetchMaxBodyBytes:           defaultWebFetchMaxBodyBytes,
		WebFetchMaxTextBytes:           defaultWebFetchMaxTextBytes,
		WebFetchMaxResolvedIPs:         defaultWebFetchMaxResolvedIPs,
		WebFetchAllowedContentTypes:    []string{"text/plain", "text/html"},

		RetrievalRRFK:                     defaultRetrievalRRFK,
		RetrievalRRFLexicalCandidateLimit: defaultRetrievalRRFLexicalCandidateLimit,
		RetrievalRRFVectorCandidateLimit:  defaultRetrievalRRFVectorCandidateLimit,
		RetrievalRRFFusedCandidateLimit:   defaultRetrievalRRFFusedCandidateLimit,
		RetrievalRRFRerankCandidateLimit:  defaultRetrievalRRFRerankCandidateLimit,

		WorkerSoftStopTimeout: defaultWorkerSoftStopTimeout,
		WorkerHardStopTimeout: defaultWorkerHardStopTimeout,
		WorkerHealthAddr:      defaultWorkerHealthAddr,
		TelemetryMode:         TelemetryModeDisabled,
		AuthMode:              AuthModeDisabled,
		AuthSessionTTL:        authapplication.DefaultSessionTTL,
		AuthAPITokenTTL:       authapplication.DefaultAPITokenTTL,
		AuthAllowedOrigins:    []string{"http://127.0.0.1:8080"},
	}
}

// Load reads an optional YAML file and then applies environment overrides.
// Environment values are intentionally explicit and validated rather than
// silently falling back when malformed.
func Load(path string) (Config, error) {
	return loadWithLookup(path, os.LookupEnv, loadOptions{
		consumeAPISecrets: true,
		validateAuth:      true,
	})
}

// LoadWorker reads Worker configuration without consuming the API-only
// Bootstrap credential. Authentication is enforced at the API boundary; the
// Worker still validates every configuration group it actually consumes.
func LoadWorker(path string) (Config, error) {
	return loadNonAPIWithLookup(path, os.LookupEnv)
}

// LoadMigration reads migration configuration without consuming API-only
// authentication credentials or validating API listener authentication rules.
// Migrations only require database configuration and must be runnable before
// the API process receives its Bootstrap credential.
func LoadMigration(path string) (Config, error) {
	return loadNonAPIWithLookup(path, os.LookupEnv)
}

// LoadWithLookup is exposed for deterministic unit tests without mutating the
// process environment.
func LoadWithLookup(path string, lookup func(string) (string, bool)) (Config, error) {
	return loadWithLookup(path, lookup, loadOptions{
		consumeAPISecrets: true,
		validateAuth:      true,
	})
}

type loadOptions struct {
	consumeAPISecrets bool
	validateAuth      bool
}

func loadNonAPIWithLookup(path string, lookup func(string) (string, bool)) (Config, error) {
	return loadWithLookup(path, lookup, loadOptions{})
}

// loadWorkerWithLookup keeps the worker configuration test seam while
// sharing the non-API process behavior with migrations.
func loadWorkerWithLookup(path string, lookup func(string) (string, bool)) (Config, error) {
	return loadNonAPIWithLookup(path, lookup)
}

func loadWithLookup(path string, lookup func(string) (string, bool), options loadOptions) (Config, error) {
	cfg := Defaults()
	if path != "" {
		if err := applyYAMLFile(path, &cfg); err != nil {
			return cfg, err
		}
	}
	if !options.consumeAPISecrets {
		cfg.AuthBootstrapToken = ""
		cfg.ReviewQuestionRefKey = ""
		cfg.reviewQuestionRefKeyExplicit = false
	}
	if err := applyEnv(&cfg, lookup, options.consumeAPISecrets); err != nil {
		return cfg, err
	}
	if options.consumeAPISecrets {
		if err := materializeReviewQuestionRefKey(&cfg); err != nil {
			return cfg, err
		}
	}
	if !options.consumeAPISecrets {
		cfg.AuthBootstrapToken = ""
		cfg.ReviewQuestionRefKey = ""
		cfg.reviewQuestionRefKeyExplicit = false
	}
	return cfg, cfg.validate(options.validateAuth)
}

// fileConfig keeps YAML duration values as strings so their parsing is
// explicit and consistent across yaml.v3 versions. Pointer fields preserve
// defaults when a YAML key is omitted.
type fileConfig struct {
	AppName                    *string `yaml:"app_name"`
	Version                    *string `yaml:"version"`
	Environment                *string `yaml:"environment"`
	HTTPAddr                   *string `yaml:"http_addr"`
	DatabaseURL                *string `yaml:"database_url"`
	DatabaseHost               *string `yaml:"database_host"`
	DatabasePort               *string `yaml:"database_port"`
	DatabaseName               *string `yaml:"database_name"`
	DatabaseUser               *string `yaml:"database_user"`
	DatabasePassword           *string `yaml:"database_password"`
	DatabaseMaxConns           *int32  `yaml:"database_max_conns"`
	DatabaseMinConns           *int32  `yaml:"database_min_conns"`
	DatabasePingTimeout        *string `yaml:"database_ping_timeout"`
	GraphQueryTimeout          *string `yaml:"graph_query_timeout"`
	HealthInterval             *string `yaml:"health_interval"`
	ShutdownTimeout            *string `yaml:"shutdown_timeout"`
	WebAssetsDir               *string `yaml:"web_assets_dir"`
	WorkerQueue                *string `yaml:"worker_queue"`
	WorkerMaxWorkers           *int    `yaml:"worker_max_workers"`
	WorkerJobTimeout           *string `yaml:"worker_job_timeout"`
	WorkerRescueStuckJobsAfter *string `yaml:"worker_rescue_stuck_jobs_after"`
	WorkflowLeaseDuration      *string `yaml:"workflow_lease"`
	WorkflowHeartbeatInterval  *string `yaml:"workflow_heartbeat"`

	ReindexDispatchPollInterval *string `yaml:"reindex_dispatch_poll_interval"`
	ReindexDispatchBatchSize    *int    `yaml:"reindex_dispatch_batch_size"`
	ReindexDispatchErrorBackoff *string `yaml:"reindex_dispatch_error_backoff"`
	ReindexLeaseDuration        *string `yaml:"reindex_lease_duration"`
	ReindexHeartbeatInterval    *string `yaml:"reindex_heartbeat_interval"`

	EmbeddingProvider           *EmbeddingProvider             `yaml:"embedding_provider"`
	EmbeddingBaseURL            *string                        `yaml:"embedding_base_url"`
	EmbeddingAPIKey             *string                        `yaml:"embedding_api_key"`
	EmbeddingModel              *string                        `yaml:"embedding_model"`
	EmbeddingDimensions         *int32                         `yaml:"embedding_dimensions"`
	EmbeddingNormalization      *domain.EmbeddingNormalization `yaml:"embedding_normalization"`
	EmbeddingDistanceMetric     *domain.DistanceMetric         `yaml:"embedding_distance_metric"`
	EmbeddingMaxBatchSize       *int32                         `yaml:"embedding_max_batch_size"`
	EmbeddingMaxInputBytes      *int32                         `yaml:"embedding_max_input_bytes"`
	EmbeddingMaxBatchInputBytes *int64                         `yaml:"embedding_max_batch_input_bytes"`
	EmbeddingTimeout            *string                        `yaml:"embedding_timeout"`
	EmbeddingMaxResponseBytes   *int64                         `yaml:"embedding_max_response_bytes"`

	ChatProvider         *ChatProvider `yaml:"chat_provider"`
	ChatBaseURL          *string       `yaml:"chat_base_url"`
	ChatAPIKey           *string       `yaml:"chat_api_key"`
	ChatModel            *string       `yaml:"chat_model"`
	ChatModelVersion     *string       `yaml:"chat_model_version"`
	ChatAdapterVersion   *string       `yaml:"chat_adapter_version"`
	ChatTimeout          *string       `yaml:"chat_timeout"`
	ChatMaxRequestBytes  *int64        `yaml:"chat_max_request_bytes"`
	ChatMaxResponseBytes *int64        `yaml:"chat_max_response_bytes"`

	ToolRuntimeMode *ToolMode `yaml:"tool_runtime_mode"`
	WebFetchMode    *ToolMode `yaml:"web_fetch_mode"`

	WebFetchTimeout                *string   `yaml:"web_fetch_timeout"`
	WebFetchResponseHeaderTimeout  *string   `yaml:"web_fetch_response_header_timeout"`
	WebFetchTLSHandshakeTimeout    *string   `yaml:"web_fetch_tls_handshake_timeout"`
	WebFetchMaxRedirects           *int      `yaml:"web_fetch_max_redirects"`
	WebFetchMaxURLBytes            *int      `yaml:"web_fetch_max_url_bytes"`
	WebFetchMaxResponseHeaderBytes *int64    `yaml:"web_fetch_max_response_header_bytes"`
	WebFetchMaxBodyBytes           *int64    `yaml:"web_fetch_max_body_bytes"`
	WebFetchMaxTextBytes           *int      `yaml:"web_fetch_max_text_bytes"`
	WebFetchMaxResolvedIPs         *int      `yaml:"web_fetch_max_resolved_ips"`
	WebFetchAllowedContentTypes    *[]string `yaml:"web_fetch_allowed_content_types"`

	RetrievalRRFK                     *int32 `yaml:"retrieval_rrf_k"`
	RetrievalRRFLexicalCandidateLimit *int32 `yaml:"retrieval_rrf_lexical_candidate_limit"`
	RetrievalRRFVectorCandidateLimit  *int32 `yaml:"retrieval_rrf_vector_candidate_limit"`
	RetrievalRRFFusedCandidateLimit   *int32 `yaml:"retrieval_rrf_fused_candidate_limit"`
	RetrievalRRFRerankCandidateLimit  *int32 `yaml:"retrieval_rrf_rerank_candidate_limit"`

	WorkerSoftStopTimeout *string        `yaml:"worker_soft_stop_timeout"`
	WorkerHardStopTimeout *string        `yaml:"worker_hard_stop_timeout"`
	WorkerHealthAddr      *string        `yaml:"worker_health_addr"`
	TelemetryMode         *TelemetryMode `yaml:"telemetry_mode"`
	TelemetryEndpoint     *string        `yaml:"telemetry_endpoint"`

	AuthMode           *AuthMode `yaml:"auth_mode"`
	AuthBootstrapToken *string   `yaml:"auth_bootstrap_token"`
	AuthSessionTTL     *string   `yaml:"auth_session_ttl"`
	AuthAPITokenTTL    *string   `yaml:"auth_api_token_ttl"`
	AuthSecureCookie   *bool     `yaml:"auth_secure_cookie"`
	AuthAllowedOrigins *[]string `yaml:"auth_allowed_origins"`

	ReviewQuestionRefKey *string `yaml:"review_question_ref_key"`
}

func applyYAMLFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	var raw fileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}
	if raw.AppName != nil {
		cfg.AppName = *raw.AppName
	}
	if raw.Version != nil {
		cfg.Version = *raw.Version
	}
	if raw.Environment != nil {
		cfg.Environment = *raw.Environment
	}
	if raw.HTTPAddr != nil {
		cfg.HTTPAddr = *raw.HTTPAddr
	}
	if raw.DatabaseURL != nil {
		cfg.DatabaseURL = *raw.DatabaseURL
	}
	if raw.DatabaseHost != nil {
		cfg.DatabaseHost = *raw.DatabaseHost
	}
	if raw.DatabasePort != nil {
		cfg.DatabasePort = *raw.DatabasePort
	}
	if raw.DatabaseName != nil {
		cfg.DatabaseName = *raw.DatabaseName
	}
	if raw.DatabaseUser != nil {
		cfg.DatabaseUser = *raw.DatabaseUser
	}
	if raw.DatabasePassword != nil {
		cfg.DatabasePassword = *raw.DatabasePassword
	}
	if raw.DatabaseMaxConns != nil {
		cfg.DatabaseMaxConns = *raw.DatabaseMaxConns
	}
	if raw.DatabaseMinConns != nil {
		cfg.DatabaseMinConns = *raw.DatabaseMinConns
	}
	if raw.WebAssetsDir != nil {
		cfg.WebAssetsDir = *raw.WebAssetsDir
	}
	if raw.WorkerQueue != nil {
		cfg.WorkerQueue = *raw.WorkerQueue
	}
	if raw.WorkerMaxWorkers != nil {
		cfg.WorkerMaxWorkers = *raw.WorkerMaxWorkers
	}
	if raw.ReindexDispatchBatchSize != nil {
		cfg.ReindexDispatchBatchSize = *raw.ReindexDispatchBatchSize
	}
	if raw.EmbeddingProvider != nil {
		cfg.EmbeddingProvider = *raw.EmbeddingProvider
	}
	if raw.EmbeddingBaseURL != nil {
		cfg.EmbeddingBaseURL = *raw.EmbeddingBaseURL
	}
	if raw.EmbeddingAPIKey != nil {
		cfg.EmbeddingAPIKey = *raw.EmbeddingAPIKey
	}
	if raw.EmbeddingModel != nil {
		cfg.EmbeddingModel = *raw.EmbeddingModel
	}
	if raw.EmbeddingDimensions != nil {
		cfg.EmbeddingDimensions = *raw.EmbeddingDimensions
	}
	if raw.EmbeddingNormalization != nil {
		cfg.EmbeddingNormalization = *raw.EmbeddingNormalization
	}
	if raw.EmbeddingDistanceMetric != nil {
		cfg.EmbeddingDistanceMetric = *raw.EmbeddingDistanceMetric
	}
	if raw.EmbeddingMaxBatchSize != nil {
		cfg.EmbeddingMaxBatchSize = *raw.EmbeddingMaxBatchSize
	}
	if raw.EmbeddingMaxInputBytes != nil {
		cfg.EmbeddingMaxInputBytes = *raw.EmbeddingMaxInputBytes
	}
	if raw.EmbeddingMaxBatchInputBytes != nil {
		cfg.EmbeddingMaxBatchInputBytes = *raw.EmbeddingMaxBatchInputBytes
	}
	if raw.EmbeddingMaxResponseBytes != nil {
		cfg.EmbeddingMaxResponseBytes = *raw.EmbeddingMaxResponseBytes
	}
	if raw.ChatProvider != nil {
		cfg.ChatProvider = *raw.ChatProvider
	}
	if raw.ChatBaseURL != nil {
		cfg.ChatBaseURL = *raw.ChatBaseURL
	}
	if raw.ChatAPIKey != nil {
		cfg.ChatAPIKey = *raw.ChatAPIKey
	}
	if raw.ChatModel != nil {
		cfg.ChatModel = *raw.ChatModel
	}
	if raw.ChatModelVersion != nil {
		cfg.ChatModelVersion = *raw.ChatModelVersion
	}
	if raw.ChatAdapterVersion != nil {
		cfg.ChatAdapterVersion = *raw.ChatAdapterVersion
	}
	if raw.ChatMaxRequestBytes != nil {
		cfg.ChatMaxRequestBytes = *raw.ChatMaxRequestBytes
	}
	if raw.ChatMaxResponseBytes != nil {
		cfg.ChatMaxResponseBytes = *raw.ChatMaxResponseBytes
	}
	if raw.ToolRuntimeMode != nil {
		cfg.ToolRuntimeMode = *raw.ToolRuntimeMode
	}
	if raw.WebFetchMode != nil {
		cfg.WebFetchMode = *raw.WebFetchMode
	}
	if raw.WebFetchMaxRedirects != nil {
		cfg.WebFetchMaxRedirects = *raw.WebFetchMaxRedirects
	}
	if raw.WebFetchMaxURLBytes != nil {
		cfg.WebFetchMaxURLBytes = *raw.WebFetchMaxURLBytes
	}
	if raw.WebFetchMaxResponseHeaderBytes != nil {
		cfg.WebFetchMaxResponseHeaderBytes = *raw.WebFetchMaxResponseHeaderBytes
	}
	if raw.WebFetchMaxBodyBytes != nil {
		cfg.WebFetchMaxBodyBytes = *raw.WebFetchMaxBodyBytes
	}
	if raw.WebFetchMaxTextBytes != nil {
		cfg.WebFetchMaxTextBytes = *raw.WebFetchMaxTextBytes
	}
	if raw.WebFetchMaxResolvedIPs != nil {
		cfg.WebFetchMaxResolvedIPs = *raw.WebFetchMaxResolvedIPs
	}
	if raw.WebFetchAllowedContentTypes != nil {
		cfg.WebFetchAllowedContentTypes = append([]string(nil), (*raw.WebFetchAllowedContentTypes)...)
	}
	if raw.RetrievalRRFK != nil {
		cfg.RetrievalRRFK = *raw.RetrievalRRFK
	}
	if raw.RetrievalRRFLexicalCandidateLimit != nil {
		cfg.RetrievalRRFLexicalCandidateLimit = *raw.RetrievalRRFLexicalCandidateLimit
	}
	if raw.RetrievalRRFVectorCandidateLimit != nil {
		cfg.RetrievalRRFVectorCandidateLimit = *raw.RetrievalRRFVectorCandidateLimit
	}
	if raw.RetrievalRRFFusedCandidateLimit != nil {
		cfg.RetrievalRRFFusedCandidateLimit = *raw.RetrievalRRFFusedCandidateLimit
	}
	if raw.RetrievalRRFRerankCandidateLimit != nil {
		cfg.RetrievalRRFRerankCandidateLimit = *raw.RetrievalRRFRerankCandidateLimit
	}
	if raw.WorkerHealthAddr != nil {
		cfg.WorkerHealthAddr = *raw.WorkerHealthAddr
	}
	if raw.TelemetryMode != nil {
		cfg.TelemetryMode = *raw.TelemetryMode
	}
	if raw.TelemetryEndpoint != nil {
		cfg.TelemetryEndpoint = *raw.TelemetryEndpoint
	}
	if raw.AuthMode != nil {
		cfg.AuthMode = *raw.AuthMode
	}
	if raw.AuthBootstrapToken != nil {
		cfg.AuthBootstrapToken = *raw.AuthBootstrapToken
	}
	if raw.AuthSecureCookie != nil {
		cfg.AuthSecureCookie = *raw.AuthSecureCookie
	}
	if raw.AuthAllowedOrigins != nil {
		cfg.AuthAllowedOrigins = append([]string(nil), (*raw.AuthAllowedOrigins)...)
	}
	if raw.ReviewQuestionRefKey != nil {
		cfg.ReviewQuestionRefKey = *raw.ReviewQuestionRefKey
		cfg.reviewQuestionRefKeyExplicit = true
	}
	for name, value := range map[string]*string{
		"database_ping_timeout":             raw.DatabasePingTimeout,
		"graph_query_timeout":               raw.GraphQueryTimeout,
		"health_interval":                   raw.HealthInterval,
		"shutdown_timeout":                  raw.ShutdownTimeout,
		"worker_job_timeout":                raw.WorkerJobTimeout,
		"worker_rescue_stuck_jobs_after":    raw.WorkerRescueStuckJobsAfter,
		"workflow_lease":                    raw.WorkflowLeaseDuration,
		"workflow_heartbeat":                raw.WorkflowHeartbeatInterval,
		"reindex_dispatch_poll_interval":    raw.ReindexDispatchPollInterval,
		"reindex_dispatch_error_backoff":    raw.ReindexDispatchErrorBackoff,
		"reindex_lease_duration":            raw.ReindexLeaseDuration,
		"reindex_heartbeat_interval":        raw.ReindexHeartbeatInterval,
		"embedding_timeout":                 raw.EmbeddingTimeout,
		"chat_timeout":                      raw.ChatTimeout,
		"web_fetch_timeout":                 raw.WebFetchTimeout,
		"web_fetch_response_header_timeout": raw.WebFetchResponseHeaderTimeout,
		"web_fetch_tls_handshake_timeout":   raw.WebFetchTLSHandshakeTimeout,
		"worker_soft_stop_timeout":          raw.WorkerSoftStopTimeout,
		"worker_hard_stop_timeout":          raw.WorkerHardStopTimeout,
		"auth_session_ttl":                  raw.AuthSessionTTL,
		"auth_api_token_ttl":                raw.AuthAPITokenTTL,
	} {
		if value == nil {
			continue
		}
		parsed, err := time.ParseDuration(strings.TrimSpace(*value))
		if err != nil {
			if name == "graph_query_timeout" || strings.HasPrefix(name, "web_fetch_") {
				return fmt.Errorf("parse %s: invalid duration", name)
			}
			return fmt.Errorf("parse %s: %w", name, err)
		}
		switch name {
		case "database_ping_timeout":
			cfg.DatabasePingTimeout = parsed
		case "graph_query_timeout":
			cfg.GraphQueryTimeout = parsed
		case "health_interval":
			cfg.HealthInterval = parsed
		case "shutdown_timeout":
			cfg.ShutdownTimeout = parsed
		case "worker_job_timeout":
			cfg.WorkerJobTimeout = parsed
		case "worker_rescue_stuck_jobs_after":
			cfg.WorkerRescueStuckJobsAfter = parsed
		case "workflow_lease":
			cfg.WorkflowLeaseDuration = parsed
		case "workflow_heartbeat":
			cfg.WorkflowHeartbeatInterval = parsed
		case "reindex_dispatch_poll_interval":
			cfg.ReindexDispatchPollInterval = parsed
		case "reindex_dispatch_error_backoff":
			cfg.ReindexDispatchErrorBackoff = parsed
		case "reindex_lease_duration":
			cfg.ReindexLeaseDuration = parsed
		case "reindex_heartbeat_interval":
			cfg.ReindexHeartbeatInterval = parsed
		case "embedding_timeout":
			cfg.EmbeddingTimeout = parsed
		case "chat_timeout":
			cfg.ChatTimeout = parsed
		case "web_fetch_timeout":
			cfg.WebFetchTimeout = parsed
		case "web_fetch_response_header_timeout":
			cfg.WebFetchResponseHeaderTimeout = parsed
		case "web_fetch_tls_handshake_timeout":
			cfg.WebFetchTLSHandshakeTimeout = parsed
		case "worker_soft_stop_timeout":
			cfg.WorkerSoftStopTimeout = parsed
		case "worker_hard_stop_timeout":
			cfg.WorkerHardStopTimeout = parsed
		case "auth_session_ttl":
			cfg.AuthSessionTTL = parsed
		case "auth_api_token_ttl":
			cfg.AuthAPITokenTTL = parsed
		}
	}
	return nil
}

// Validate checks process settings that must be valid before starting a
// server. An empty DatabaseURL is allowed so API readiness can report a real
// degraded state; Worker startup separately calls ValidateDatabase.
func (c Config) Validate() error {
	return c.validate(true)
}

func (c Config) validate(validateAuth bool) error {
	if strings.TrimSpace(c.AppName) == "" {
		return errors.New("app_name must not be empty")
	}
	if strings.TrimSpace(c.Version) == "" {
		return errors.New("version must not be empty")
	}
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return errors.New("http_addr must not be empty")
	}
	if c.DatabaseMaxConns < 0 || c.DatabaseMinConns < 0 {
		return errors.New("database pool sizes must not be negative")
	}
	if c.DatabaseMaxConns > 0 && c.DatabaseMinConns > c.DatabaseMaxConns {
		return errors.New("database_min_conns must not exceed database_max_conns")
	}
	if c.DatabasePingTimeout <= 0 {
		return errors.New("database_ping_timeout must be positive")
	}
	if c.GraphQueryTimeout <= 0 {
		return errors.New("graph_query_timeout must be positive")
	}
	if c.HealthInterval <= 0 {
		return errors.New("health_interval must be positive")
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("shutdown_timeout must be positive")
	}
	if err := operability.ValidateRiverOptions(c.WorkerQueue, c.WorkerMaxWorkers, c.WorkerJobTimeout, c.WorkerRescueStuckJobsAfter, c.WorkerSoftStopTimeout); err != nil {
		return err
	}
	if c.WorkflowLeaseDuration <= 0 {
		return errors.New("workflow_lease must be positive")
	}
	if c.WorkflowHeartbeatInterval <= 0 {
		return errors.New("workflow_heartbeat must be positive")
	}
	if c.WorkflowHeartbeatInterval > (c.WorkflowLeaseDuration-time.Nanosecond)/3 {
		return errors.New("workflow_heartbeat must be less than one third of workflow_lease")
	}
	if c.ReindexDispatchPollInterval <= 0 || c.ReindexDispatchPollInterval > maxReindexDispatchPollInterval {
		return errors.New("reindex_dispatch_poll_interval must be positive and at most 1m")
	}
	if c.ReindexDispatchBatchSize <= 0 || c.ReindexDispatchBatchSize > maxReindexDispatchBatchSize {
		return errors.New("reindex_dispatch_batch_size must be between 1 and 1000")
	}
	if c.ReindexDispatchErrorBackoff <= 0 || c.ReindexDispatchErrorBackoff > maxReindexDispatchErrorBackoff {
		return errors.New("reindex_dispatch_error_backoff must be positive and at most 1m")
	}
	if c.ReindexLeaseDuration <= 0 || c.ReindexLeaseDuration > maxReindexLeaseDuration {
		return errors.New("reindex_lease_duration must be positive and at most 24h")
	}
	if c.ReindexHeartbeatInterval <= 0 || c.ReindexHeartbeatInterval > maxReindexHeartbeatInterval {
		return errors.New("reindex_heartbeat_interval must be positive and at most 1m")
	}
	if c.ReindexHeartbeatInterval >= c.ReindexLeaseDuration {
		return errors.New("reindex_heartbeat_interval must be less than reindex_lease_duration")
	}
	if c.WorkerHardStopTimeout <= 0 {
		return errors.New("worker_hard_stop_timeout must be positive")
	}
	if c.WorkerSoftStopTimeout >= c.WorkerHardStopTimeout {
		return errors.New("worker_soft_stop_timeout must be less than worker_hard_stop_timeout")
	}
	if err := validateListenAddress(c.WorkerHealthAddr); err != nil {
		return err
	}
	if err := c.validateTelemetry(); err != nil {
		return err
	}
	if validateAuth {
		if err := c.validateAuth(); err != nil {
			return err
		}
		if err := c.validateReviewQuestionRefKey(); err != nil {
			return err
		}
	}
	if err := c.validateEmbedding(); err != nil {
		return err
	}
	if err := c.validateChat(); err != nil {
		return err
	}
	if err := c.validateTools(); err != nil {
		return err
	}
	if err := domain.ValidateRRFConfig(c.RetrievalRRFConfig()); err != nil {
		return errors.New("retrieval RRF configuration is invalid")
	}
	return nil
}

func (c Config) validateReviewQuestionRefKey() error {
	key := strings.TrimSpace(c.ReviewQuestionRefKey)
	if key == "" {
		if c.AuthMode == AuthModeDisabled && isDevelopmentEnvironment(c.Environment) && isLoopbackAddress(c.HTTPAddr) {
			return nil
		}
		bootstrap := strings.TrimSpace(c.AuthBootstrapToken)
		if c.AuthMode == AuthModeRequired && len(bootstrap) >= 32 && bootstrap == c.AuthBootstrapToken {
			return nil
		}
	}
	return validateReviewQuestionRefKeyValue(c.ReviewQuestionRefKey)
}

func materializeReviewQuestionRefKey(cfg *Config) error {
	if cfg.reviewQuestionRefKeyExplicit || cfg.ReviewQuestionRefKey != "" {
		return validateReviewQuestionRefKeyValue(cfg.ReviewQuestionRefKey)
	}
	if cfg.AuthMode == AuthModeRequired && cfg.AuthBootstrapToken != "" {
		digest := sha256.Sum256([]byte("zhixu/review-question-ref/v1\x00" + cfg.AuthBootstrapToken))
		cfg.ReviewQuestionRefKey = hex.EncodeToString(digest[:])
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return errors.New("generate review_question_ref_key: secure randomness unavailable")
	}
	cfg.ReviewQuestionRefKey = base64.RawURLEncoding.EncodeToString(key)
	return nil
}

func validateReviewQuestionRefKeyValue(key string) error {
	if len(key) < 32 || key != strings.TrimSpace(key) {
		return errors.New("review_question_ref_key must contain at least 32 canonical bytes")
	}
	return nil
}

func (c Config) validateAuth() error {
	development := isDevelopmentEnvironment(c.Environment)
	switch c.AuthMode {
	case AuthModeDisabled:
		if c.AuthBootstrapToken != "" {
			return errors.New("auth_bootstrap_token must be empty when auth_mode is disabled")
		}
		if !development || !isLoopbackAddress(c.HTTPAddr) {
			return errors.New("auth_mode disabled is only allowed for development loopback HTTP")
		}
	case AuthModeRequired:
		bootstrap := strings.TrimSpace(c.AuthBootstrapToken)
		if len(bootstrap) < 32 || bootstrap != c.AuthBootstrapToken {
			return errors.New("auth_bootstrap_token must contain at least 32 canonical characters")
		}
		if len(c.AuthAllowedOrigins) == 0 {
			return errors.New("auth_allowed_origins must not be empty when auth_mode is required")
		}
		for _, origin := range c.AuthAllowedOrigins {
			if !validAuthOrigin(origin) {
				return errors.New("auth_allowed_origins contains an invalid exact origin")
			}
		}
		if !c.AuthSecureCookie {
			if !development {
				return errors.New("auth_secure_cookie may be false only for development loopback origins")
			}
			if !isLoopbackAddress(c.HTTPAddr) {
				return errors.New("insecure auth cookies require a loopback HTTP listener")
			}
			for _, origin := range c.AuthAllowedOrigins {
				if !isLoopbackHTTPOrigin(origin) {
					return errors.New("insecure auth origins must use loopback HTTP")
				}
			}
		} else if !development {
			for _, origin := range c.AuthAllowedOrigins {
				if !strings.HasPrefix(origin, "https://") {
					return errors.New("non-loopback auth origins must use https")
				}
			}
		}
	default:
		return errors.New("auth_mode must be disabled or required")
	}
	if c.AuthSessionTTL <= 0 || c.AuthSessionTTL > authapplication.MaxSessionTTL {
		return errors.New("auth_session_ttl is outside the supported range")
	}
	if c.AuthAPITokenTTL <= 0 || c.AuthAPITokenTTL > authapplication.MaxAPITokenTTL {
		return errors.New("auth_api_token_ttl is outside the supported range")
	}
	return nil
}

func isDevelopmentEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "development", "dev", "test", "testing":
		return true
	default:
		return false
	}
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validAuthOrigin(value string) bool {
	return authorigin.IsCanonical(value)
}

func isLoopbackHTTPOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseAuthOrigins(raw string) ([]string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return nil, errors.New("parse ZHIXU_AUTH_ALLOWED_ORIGINS: invalid origin list")
	}
	values := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validAuthOrigin(value) {
			return nil, errors.New("parse ZHIXU_AUTH_ALLOWED_ORIGINS: invalid exact origin")
		}
		if _, exists := seen[value]; exists {
			return nil, errors.New("parse ZHIXU_AUTH_ALLOWED_ORIGINS: duplicate origin")
		}
		seen[value] = struct{}{}
	}
	return values, nil
}

func (c Config) validateTools() error {
	if c.ToolRuntimeMode != ToolModeDisabled && c.ToolRuntimeMode != ToolModeEnabled {
		return errors.New("tool_runtime_mode must be disabled or enabled")
	}
	if c.WebFetchMode != ToolModeDisabled && c.WebFetchMode != ToolModeEnabled {
		return errors.New("web_fetch_mode must be disabled or enabled")
	}
	if c.WebFetchMode == ToolModeEnabled && c.ToolRuntimeMode != ToolModeEnabled {
		return errors.New("web_fetch_mode cannot be enabled when tool_runtime_mode is disabled")
	}
	if c.WebFetchTimeout <= 0 || c.WebFetchTimeout > maxWebFetchTimeout {
		return errors.New("web_fetch_timeout must be positive and at most 2m")
	}
	if c.WebFetchResponseHeaderTimeout <= 0 || c.WebFetchResponseHeaderTimeout > maxWebFetchResponseHeaderTimeout {
		return errors.New("web_fetch_response_header_timeout must be positive and at most 30s")
	}
	if c.WebFetchTLSHandshakeTimeout <= 0 || c.WebFetchTLSHandshakeTimeout > maxWebFetchTLSHandshakeTimeout {
		return errors.New("web_fetch_tls_handshake_timeout must be positive and at most 30s")
	}
	if c.WebFetchMaxRedirects < 0 || c.WebFetchMaxRedirects > maxWebFetchRedirects {
		return errors.New("web_fetch_max_redirects must be between 0 and 20")
	}
	if c.WebFetchMaxURLBytes <= 0 || c.WebFetchMaxURLBytes > maxWebFetchURLBytes {
		return errors.New("web_fetch_max_url_bytes must be between 1 and 65536")
	}
	if c.WebFetchMaxResponseHeaderBytes <= 0 || c.WebFetchMaxResponseHeaderBytes > maxWebFetchResponseHeaderBytes {
		return errors.New("web_fetch_max_response_header_bytes must be between 1 and 2097152")
	}
	if c.WebFetchMaxBodyBytes <= 0 || c.WebFetchMaxBodyBytes > maxWebFetchBodyBytes {
		return errors.New("web_fetch_max_body_bytes must be between 1 and 33554432")
	}
	if c.WebFetchMaxTextBytes <= 0 || int64(c.WebFetchMaxTextBytes) > c.WebFetchMaxBodyBytes {
		return errors.New("web_fetch_max_text_bytes must be positive and at most web_fetch_max_body_bytes")
	}
	if c.WebFetchMaxResolvedIPs <= 0 || c.WebFetchMaxResolvedIPs > maxWebFetchResolvedIPs {
		return errors.New("web_fetch_max_resolved_ips must be between 1 and 64")
	}
	if err := validateWebFetchContentTypes(c.WebFetchAllowedContentTypes); err != nil {
		return err
	}
	return nil
}

func validateWebFetchContentTypes(values []string) error {
	if len(values) == 0 || len(values) > maxWebFetchAllowedContentTypes {
		return errors.New("web_fetch_allowed_content_types must contain one or two supported types")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != strings.TrimSpace(value) || (value != "text/plain" && value != "text/html") {
			return errors.New("web_fetch_allowed_content_types supports only text/plain and text/html")
		}
		if _, duplicate := seen[value]; duplicate {
			return errors.New("web_fetch_allowed_content_types must not contain duplicates")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateListenAddress(address string) error {
	_, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return errors.New("worker_health_addr must be a valid host:port address")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return errors.New("worker_health_addr port must be between 1 and 65535")
	}
	return nil
}

func (c Config) validateTelemetry() error {
	switch c.TelemetryMode {
	case TelemetryModeDisabled:
		if strings.TrimSpace(c.TelemetryEndpoint) != "" {
			return errors.New("telemetry_endpoint must be empty when telemetry_mode is disabled")
		}
		return nil
	case TelemetryModeOptional, TelemetryModeRequired:
		if strings.TrimSpace(c.TelemetryEndpoint) == "" {
			return errors.New("telemetry_endpoint is required when telemetry_mode is optional or required")
		}
		endpoint, err := url.Parse(c.TelemetryEndpoint)
		if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
			return errors.New("telemetry_endpoint must be a valid absolute URL")
		}
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return errors.New("telemetry_endpoint scheme must be http or https")
		}
		return nil
	default:
		return errors.New("telemetry_mode must be disabled, optional, or required")
	}
}

func (c Config) validateEmbedding() error {
	if c.EmbeddingMaxBatchSize <= 0 || c.EmbeddingMaxBatchSize > domain.MaxEmbeddingBatchSize {
		return errors.New("embedding_max_batch_size must be between 1 and 1000")
	}
	if c.EmbeddingMaxInputBytes <= 0 || c.EmbeddingMaxInputBytes > domain.MaxEmbeddingInputBytes {
		return errors.New("embedding_max_input_bytes must be between 1 and 10485760")
	}
	if c.EmbeddingMaxBatchInputBytes < int64(c.EmbeddingMaxInputBytes) || c.EmbeddingMaxBatchInputBytes > domain.MaxEmbeddingBatchInputBytes {
		return errors.New("embedding_max_batch_input_bytes must be at least embedding_max_input_bytes and at most 67108864")
	}
	if c.EmbeddingTimeout <= 0 || c.EmbeddingTimeout > maxEmbeddingTimeout {
		return errors.New("embedding_timeout must be positive and at most 5m")
	}
	if c.EmbeddingMaxResponseBytes <= 0 || c.EmbeddingMaxResponseBytes > maxEmbeddingMaxResponseBytes {
		return errors.New("embedding_max_response_bytes must be between 1 and 134217728")
	}
	if c.EmbeddingNormalization != domain.NormalizationNone && c.EmbeddingNormalization != domain.NormalizationL2 {
		return errors.New("embedding_normalization must be none or l2")
	}
	switch c.EmbeddingDistanceMetric {
	case domain.DistanceCosine, domain.DistanceInnerProduct, domain.DistanceEuclidean:
	default:
		return errors.New("embedding_distance_metric must be cosine, inner_product, or euclidean")
	}

	switch c.EmbeddingProvider {
	case EmbeddingProviderDisabled:
		if c.EmbeddingBaseURL != "" || c.EmbeddingAPIKey != "" || c.EmbeddingModel != "" || c.EmbeddingDimensions != 0 {
			return errors.New("embedding provider settings must be empty when embedding_provider is disabled")
		}
		return nil
	case EmbeddingProviderOpenAICompatible, EmbeddingProviderOllama:
		if c.EmbeddingModel == "" || c.EmbeddingModel != strings.TrimSpace(c.EmbeddingModel) {
			return errors.New("embedding_model must be non-empty and canonical when embedding is enabled")
		}
		if c.EmbeddingDimensions <= 0 || c.EmbeddingDimensions > maxEmbeddingDimensions {
			return errors.New("embedding_dimensions must be between 1 and 16000 when embedding is enabled")
		}
		if err := validateEmbeddingBaseURL(c.EmbeddingBaseURL, c.EmbeddingProvider == EmbeddingProviderOllama); err != nil {
			return err
		}
		if c.EmbeddingProvider == EmbeddingProviderOpenAICompatible {
			if strings.TrimSpace(c.EmbeddingAPIKey) == "" || c.EmbeddingAPIKey != strings.TrimSpace(c.EmbeddingAPIKey) {
				return errors.New("embedding_api_key must be non-empty and canonical for openai-compatible")
			}
			return nil
		}
		if c.EmbeddingAPIKey != "" {
			return errors.New("embedding_api_key must be empty for ollama")
		}
		return nil
	default:
		return errors.New("embedding_provider must be disabled, openai-compatible, or ollama")
	}
}

func validateEmbeddingBaseURL(raw string, allowLoopbackHTTP bool) error {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return errors.New("embedding_base_url is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return errors.New("embedding_base_url is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !allowLoopbackHTTP || !isLoopbackHost(parsed.Hostname()) {
		return errors.New("embedding_base_url must use https, except loopback ollama may use http")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (c Config) validateChat() error {
	if c.ChatTimeout <= 0 || c.ChatTimeout > maxChatTimeout {
		return errors.New("chat_timeout must be positive and at most 5m")
	}
	if c.ChatMaxRequestBytes <= 0 || c.ChatMaxRequestBytes > maxChatRequestBytes {
		return errors.New("chat_max_request_bytes must be between 1 and 16777216")
	}
	if c.ChatMaxResponseBytes <= 0 || c.ChatMaxResponseBytes > maxChatResponseBytes {
		return errors.New("chat_max_response_bytes must be between 1 and 16777216")
	}
	if !canonicalChatSetting(c.ChatAdapterVersion, 64) {
		return errors.New("chat_adapter_version must be non-empty and canonical")
	}
	switch c.ChatProvider {
	case ChatProviderDisabled:
		if c.ChatBaseURL != "" || c.ChatAPIKey != "" || c.ChatModel != "" || c.ChatModelVersion != "" {
			return errors.New("chat provider settings must be empty when chat_provider is disabled")
		}
		return nil
	case ChatProviderOpenAICompatible:
		if !canonicalChatSetting(c.ChatModel, 128) {
			return errors.New("chat_model must be non-empty and canonical when chat is enabled")
		}
		if !canonicalChatSetting(c.ChatModelVersion, 64) {
			return errors.New("chat_model_version must be non-empty and canonical when chat is enabled")
		}
		if c.ChatAPIKey != "" && !canonicalChatSecret(c.ChatAPIKey) {
			return errors.New("chat_api_key must be canonical when configured")
		}
		if err := validateChatBaseURL(c.ChatBaseURL); err != nil {
			return err
		}
		return nil
	default:
		return errors.New("chat_provider must be disabled or openai-compatible")
	}
}

func validateChatBaseURL(raw string) error {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return errors.New("chat_base_url is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return errors.New("chat_base_url is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return errors.New("chat_base_url must use https, except loopback compatible endpoints may use http")
	}
	return nil
}

func canonicalChatSetting(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func canonicalChatSecret(value string) bool {
	if len(value) > 8192 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// RetrievalRRFConfig returns the strict versioned fusion configuration.
func (c Config) RetrievalRRFConfig() domain.RRFConfig {
	return domain.RRFConfig{
		SchemaVersion:         domain.RRFFusionSchemaVersionV1,
		Method:                domain.FusionMethodRRF,
		K:                     c.RetrievalRRFK,
		LexicalCandidateLimit: c.RetrievalRRFLexicalCandidateLimit,
		VectorCandidateLimit:  c.RetrievalRRFVectorCandidateLimit,
		FusedCandidateLimit:   c.RetrievalRRFFusedCandidateLimit,
		RerankCandidateLimit:  c.RetrievalRRFRerankCandidateLimit,
	}
}

// ValidateDatabase returns the configuration error that prevents a real DB
// readiness check. A URL is parsed here so malformed configuration is a
// startup/configuration failure rather than a retryable dependency outage.
func (c Config) ValidateDatabase() error {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		if _, err := url.Parse(c.DatabaseURL); err != nil {
			return errors.New("database_url is invalid")
		}
		return nil
	}
	if strings.TrimSpace(c.DatabaseHost) == "" || strings.TrimSpace(c.DatabaseName) == "" || strings.TrimSpace(c.DatabaseUser) == "" || c.DatabasePassword == "" {
		return errors.New("database_url or database_host/database_name/database_user/database_password is not configured")
	}
	return nil
}

// DatabaseConnectionString returns the explicit URL or safely builds one from
// discrete connection settings so reserved characters in passwords are escaped.
func (c Config) DatabaseConnectionString() (string, error) {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		if err := c.ValidateDatabase(); err != nil {
			return "", err
		}
		return c.DatabaseURL, nil
	}
	if err := c.ValidateDatabase(); err != nil {
		return "", err
	}
	port := strings.TrimSpace(c.DatabasePort)
	if port == "" {
		port = "5432"
	}
	databaseURL := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(strings.TrimSpace(c.DatabaseHost), port),
		Path:   "/" + strings.TrimSpace(c.DatabaseName),
	}
	databaseURL.User = url.UserPassword(strings.TrimSpace(c.DatabaseUser), c.DatabasePassword)
	query := databaseURL.Query()
	query.Set("sslmode", "disable")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
}

// String returns a non-sensitive summary suitable for diagnostics. Connection
// credentials and exporter endpoints are intentionally omitted.
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{AppName:%q Version:%q Environment:%q HTTPAddr:%q DatabaseConfigured:%t DatabaseMaxConns:%d DatabaseMinConns:%d DatabasePingTimeout:%s GraphQueryTimeout:%s HealthInterval:%s ShutdownTimeout:%s WebAssetsDir:%q WorkerQueue:%q WorkerMaxWorkers:%d WorkerJobTimeout:%s WorkerRescueStuckJobsAfter:%s WorkflowLeaseDuration:%s WorkflowHeartbeatInterval:%s ReindexDispatchPollInterval:%s ReindexDispatchBatchSize:%d ReindexDispatchErrorBackoff:%s ReindexLeaseDuration:%s ReindexHeartbeatInterval:%s EmbeddingProvider:%q EmbeddingConfigured:%t EmbeddingModel:%q EmbeddingDimensions:%d EmbeddingNormalization:%q EmbeddingDistanceMetric:%q EmbeddingMaxBatchSize:%d EmbeddingMaxInputBytes:%d EmbeddingMaxBatchInputBytes:%d EmbeddingTimeout:%s EmbeddingMaxResponseBytes:%d ChatProvider:%q ChatConfigured:%t ChatModel:%q ChatModelVersion:%q ChatAdapterVersion:%q ChatTimeout:%s ChatMaxRequestBytes:%d ChatMaxResponseBytes:%d ToolRuntimeMode:%q WebFetchMode:%q WebFetchTimeout:%s WebFetchResponseHeaderTimeout:%s WebFetchTLSHandshakeTimeout:%s WebFetchMaxRedirects:%d WebFetchMaxURLBytes:%d WebFetchMaxResponseHeaderBytes:%d WebFetchMaxBodyBytes:%d WebFetchMaxTextBytes:%d WebFetchMaxResolvedIPs:%d WebFetchAllowedContentTypeCount:%d RetrievalRRFK:%d RetrievalRRFLexicalCandidateLimit:%d RetrievalRRFVectorCandidateLimit:%d RetrievalRRFFusedCandidateLimit:%d RetrievalRRFRerankCandidateLimit:%d WorkerSoftStopTimeout:%s WorkerHardStopTimeout:%s WorkerHealthAddr:%q TelemetryMode:%q TelemetryConfigured:%t AuthMode:%q AuthConfigured:%t AuthSessionTTL:%s AuthAPITokenTTL:%s AuthSecureCookie:%t AuthAllowedOriginCount:%d ReviewQuestionRefConfigured:%t}",
		c.AppName,
		c.Version,
		c.Environment,
		c.HTTPAddr,
		strings.TrimSpace(c.DatabaseURL) != "" || strings.TrimSpace(c.DatabaseHost) != "",
		c.DatabaseMaxConns,
		c.DatabaseMinConns,
		c.DatabasePingTimeout,
		c.GraphQueryTimeout,
		c.HealthInterval,
		c.ShutdownTimeout,
		c.WebAssetsDir,
		c.WorkerQueue,
		c.WorkerMaxWorkers,
		c.WorkerJobTimeout,
		c.WorkerRescueStuckJobsAfter,
		c.WorkflowLeaseDuration,
		c.WorkflowHeartbeatInterval,
		c.ReindexDispatchPollInterval,
		c.ReindexDispatchBatchSize,
		c.ReindexDispatchErrorBackoff,
		c.ReindexLeaseDuration,
		c.ReindexHeartbeatInterval,
		c.EmbeddingProvider,
		c.EmbeddingProvider != EmbeddingProviderDisabled,
		c.EmbeddingModel,
		c.EmbeddingDimensions,
		c.EmbeddingNormalization,
		c.EmbeddingDistanceMetric,
		c.EmbeddingMaxBatchSize,
		c.EmbeddingMaxInputBytes,
		c.EmbeddingMaxBatchInputBytes,
		c.EmbeddingTimeout,
		c.EmbeddingMaxResponseBytes,
		c.ChatProvider,
		c.ChatProvider != ChatProviderDisabled,
		c.ChatModel,
		c.ChatModelVersion,
		c.ChatAdapterVersion,
		c.ChatTimeout,
		c.ChatMaxRequestBytes,
		c.ChatMaxResponseBytes,
		c.ToolRuntimeMode,
		c.WebFetchMode,
		c.WebFetchTimeout,
		c.WebFetchResponseHeaderTimeout,
		c.WebFetchTLSHandshakeTimeout,
		c.WebFetchMaxRedirects,
		c.WebFetchMaxURLBytes,
		c.WebFetchMaxResponseHeaderBytes,
		c.WebFetchMaxBodyBytes,
		c.WebFetchMaxTextBytes,
		c.WebFetchMaxResolvedIPs,
		len(c.WebFetchAllowedContentTypes),
		c.RetrievalRRFK,
		c.RetrievalRRFLexicalCandidateLimit,
		c.RetrievalRRFVectorCandidateLimit,
		c.RetrievalRRFFusedCandidateLimit,
		c.RetrievalRRFRerankCandidateLimit,
		c.WorkerSoftStopTimeout,
		c.WorkerHardStopTimeout,
		c.WorkerHealthAddr,
		c.TelemetryMode,
		strings.TrimSpace(c.TelemetryEndpoint) != "",
		c.AuthMode,
		strings.TrimSpace(c.AuthBootstrapToken) != "",
		c.AuthSessionTTL,
		c.AuthAPITokenTTL,
		c.AuthSecureCookie,
		len(c.AuthAllowedOrigins),
		strings.TrimSpace(c.ReviewQuestionRefKey) != "",
	)
}

// GoString applies the same secret-safe representation to %#v formatting.
func (c Config) GoString() string {
	return c.String()
}

func applyEnv(cfg *Config, lookup func(string) (string, bool), consumeAPISecrets bool) error {
	if value, ok := lookup("ZHIXU_AUTH_MODE"); ok {
		cfg.AuthMode = AuthMode(value)
	}
	if value, ok := lookup("ZHIXU_TOOL_RUNTIME_MODE"); ok {
		cfg.ToolRuntimeMode = ToolMode(value)
	}
	if value, ok := lookup("ZHIXU_WEB_FETCH_MODE"); ok {
		cfg.WebFetchMode = ToolMode(value)
	}
	if value, ok := lookup("ZHIXU_CHAT_PROVIDER"); ok {
		cfg.ChatProvider = ChatProvider(value)
		if cfg.ChatProvider == ChatProviderDisabled {
			cfg.ChatBaseURL = ""
			cfg.ChatAPIKey = ""
			cfg.ChatModel = ""
			cfg.ChatModelVersion = ""
		}
	}
	if value, ok := lookup("ZHIXU_EMBEDDING_PROVIDER"); ok {
		cfg.EmbeddingProvider = EmbeddingProvider(value)
		if cfg.EmbeddingProvider == EmbeddingProviderDisabled {
			cfg.EmbeddingBaseURL = ""
			cfg.EmbeddingAPIKey = ""
			cfg.EmbeddingModel = ""
			cfg.EmbeddingDimensions = 0
		}
	}
	values := map[string]*string{
		"ZHIXU_APP_NAME":           &cfg.AppName,
		"ZHIXU_VERSION":            &cfg.Version,
		"ZHIXU_ENVIRONMENT":        &cfg.Environment,
		"ZHIXU_HTTP_ADDR":          &cfg.HTTPAddr,
		"ZHIXU_DATABASE_URL":       &cfg.DatabaseURL,
		"ZHIXU_DATABASE_HOST":      &cfg.DatabaseHost,
		"ZHIXU_DATABASE_PORT":      &cfg.DatabasePort,
		"ZHIXU_DATABASE_NAME":      &cfg.DatabaseName,
		"ZHIXU_DATABASE_USER":      &cfg.DatabaseUser,
		"ZHIXU_DATABASE_PASSWORD":  &cfg.DatabasePassword,
		"ZHIXU_WEB_ASSETS_DIR":     &cfg.WebAssetsDir,
		"ZHIXU_WORKER_QUEUE":       &cfg.WorkerQueue,
		"ZHIXU_WORKER_HEALTH_ADDR": &cfg.WorkerHealthAddr,
	}
	for key, target := range values {
		if value, ok := lookup(key); ok {
			*target = value
		}
	}
	if cfg.EmbeddingProvider != EmbeddingProviderDisabled {
		for key, target := range map[string]*string{
			"ZHIXU_EMBEDDING_BASE_URL": &cfg.EmbeddingBaseURL,
			"ZHIXU_EMBEDDING_API_KEY":  &cfg.EmbeddingAPIKey,
			"ZHIXU_EMBEDDING_MODEL":    &cfg.EmbeddingModel,
		} {
			if value, ok := lookup(key); ok {
				*target = value
			}
		}
	}
	if cfg.ChatProvider != ChatProviderDisabled {
		for key, target := range map[string]*string{
			"ZHIXU_CHAT_BASE_URL":        &cfg.ChatBaseURL,
			"ZHIXU_CHAT_API_KEY":         &cfg.ChatAPIKey,
			"ZHIXU_CHAT_MODEL":           &cfg.ChatModel,
			"ZHIXU_CHAT_MODEL_VERSION":   &cfg.ChatModelVersion,
			"ZHIXU_CHAT_ADAPTER_VERSION": &cfg.ChatAdapterVersion,
		} {
			if value, ok := lookup(key); ok {
				*target = value
			}
		}
	}
	if consumeAPISecrets {
		if value, ok := lookup("ZHIXU_AUTH_BOOTSTRAP_TOKEN"); ok {
			cfg.AuthBootstrapToken = value
		}
		if value, ok := lookup("ZHIXU_REVIEW_QUESTION_REF_KEY"); ok {
			cfg.ReviewQuestionRefKey = value
			cfg.reviewQuestionRefKeyExplicit = true
		}
	}
	if value, ok := lookup("ZHIXU_AUTH_ALLOWED_ORIGINS"); ok {
		origins, err := parseAuthOrigins(value)
		if err != nil {
			return err
		}
		cfg.AuthAllowedOrigins = origins
	}
	if value, ok := lookup("ZHIXU_AUTH_SECURE_COOKIE"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse ZHIXU_AUTH_SECURE_COOKIE: invalid boolean")
		}
		cfg.AuthSecureCookie = parsed
	}
	if value, ok := lookup("ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES"); ok {
		contentTypes, err := parseWebFetchContentTypes(value)
		if err != nil {
			return err
		}
		cfg.WebFetchAllowedContentTypes = contentTypes
	}
	if value, ok := lookup("ZHIXU_EMBEDDING_NORMALIZATION"); ok {
		cfg.EmbeddingNormalization = domain.EmbeddingNormalization(value)
	}
	if value, ok := lookup("ZHIXU_EMBEDDING_DISTANCE_METRIC"); ok {
		cfg.EmbeddingDistanceMetric = domain.DistanceMetric(value)
	}

	if value, ok := lookup("ZHIXU_DATABASE_MAX_CONNS"); ok {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_DATABASE_MAX_CONNS", err)
		}
		cfg.DatabaseMaxConns = int32(parsed)
	}
	if value, ok := lookup("ZHIXU_DATABASE_MIN_CONNS"); ok {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_DATABASE_MIN_CONNS", err)
		}
		cfg.DatabaseMinConns = int32(parsed)
	}
	if value, ok := lookup("ZHIXU_WORKER_MAX_WORKERS"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_WORKER_MAX_WORKERS", err)
		}
		cfg.WorkerMaxWorkers = parsed
	}
	if value, ok := lookup("ZHIXU_REINDEX_DISPATCH_BATCH_SIZE"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_REINDEX_DISPATCH_BATCH_SIZE", err)
		}
		cfg.ReindexDispatchBatchSize = parsed
	}
	for key, target := range map[string]*int{
		"ZHIXU_WEB_FETCH_MAX_REDIRECTS":    &cfg.WebFetchMaxRedirects,
		"ZHIXU_WEB_FETCH_MAX_URL_BYTES":    &cfg.WebFetchMaxURLBytes,
		"ZHIXU_WEB_FETCH_MAX_TEXT_BYTES":   &cfg.WebFetchMaxTextBytes,
		"ZHIXU_WEB_FETCH_MAX_RESOLVED_IPS": &cfg.WebFetchMaxResolvedIPs,
	} {
		if value, ok := lookup(key); ok {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("parse %s: invalid integer", key)
			}
			*target = parsed
		}
	}
	if cfg.EmbeddingProvider != EmbeddingProviderDisabled {
		if value, ok := lookup("ZHIXU_EMBEDDING_DIMENSIONS"); ok {
			parsed, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return errors.New("parse ZHIXU_EMBEDDING_DIMENSIONS: invalid integer")
			}
			cfg.EmbeddingDimensions = int32(parsed)
		}
	}
	for key, target := range map[string]*int32{
		"ZHIXU_EMBEDDING_MAX_BATCH_SIZE":              &cfg.EmbeddingMaxBatchSize,
		"ZHIXU_EMBEDDING_MAX_INPUT_BYTES":             &cfg.EmbeddingMaxInputBytes,
		"ZHIXU_RETRIEVAL_RRF_K":                       &cfg.RetrievalRRFK,
		"ZHIXU_RETRIEVAL_RRF_LEXICAL_CANDIDATE_LIMIT": &cfg.RetrievalRRFLexicalCandidateLimit,
		"ZHIXU_RETRIEVAL_RRF_VECTOR_CANDIDATE_LIMIT":  &cfg.RetrievalRRFVectorCandidateLimit,
		"ZHIXU_RETRIEVAL_RRF_FUSED_CANDIDATE_LIMIT":   &cfg.RetrievalRRFFusedCandidateLimit,
		"ZHIXU_RETRIEVAL_RRF_RERANK_CANDIDATE_LIMIT":  &cfg.RetrievalRRFRerankCandidateLimit,
	} {
		if value, ok := lookup(key); ok {
			parsed, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return fmt.Errorf("parse %s: invalid integer", key)
			}
			*target = int32(parsed)
		}
	}
	if value, ok := lookup("ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES"); ok {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("parse ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES: invalid integer")
		}
		cfg.EmbeddingMaxResponseBytes = parsed
	}
	if value, ok := lookup("ZHIXU_EMBEDDING_MAX_BATCH_INPUT_BYTES"); ok {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("parse ZHIXU_EMBEDDING_MAX_BATCH_INPUT_BYTES: invalid integer")
		}
		cfg.EmbeddingMaxBatchInputBytes = parsed
	}
	for key, target := range map[string]*int64{
		"ZHIXU_CHAT_MAX_REQUEST_BYTES":              &cfg.ChatMaxRequestBytes,
		"ZHIXU_CHAT_MAX_RESPONSE_BYTES":             &cfg.ChatMaxResponseBytes,
		"ZHIXU_WEB_FETCH_MAX_RESPONSE_HEADER_BYTES": &cfg.WebFetchMaxResponseHeaderBytes,
		"ZHIXU_WEB_FETCH_MAX_BODY_BYTES":            &cfg.WebFetchMaxBodyBytes,
	} {
		if value, ok := lookup(key); ok {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fmt.Errorf("parse %s: invalid integer", key)
			}
			*target = parsed
		}
	}
	for key, target := range map[string]*time.Duration{
		"ZHIXU_DATABASE_PING_TIMEOUT":     &cfg.DatabasePingTimeout,
		"ZHIXU_GRAPH_QUERY_TIMEOUT":       &cfg.GraphQueryTimeout,
		"ZHIXU_HEALTH_INTERVAL":           &cfg.HealthInterval,
		"ZHIXU_SHUTDOWN_TIMEOUT":          &cfg.ShutdownTimeout,
		"ZHIXU_WORKER_JOB_TIMEOUT":        &cfg.WorkerJobTimeout,
		"ZHIXU_WORKER_RESCUE_STUCK_AFTER": &cfg.WorkerRescueStuckJobsAfter,
		"ZHIXU_WORKFLOW_LEASE":            &cfg.WorkflowLeaseDuration,
		"ZHIXU_WORKFLOW_HEARTBEAT":        &cfg.WorkflowHeartbeatInterval,

		"ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL":    &cfg.ReindexDispatchPollInterval,
		"ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF":    &cfg.ReindexDispatchErrorBackoff,
		"ZHIXU_REINDEX_LEASE_DURATION":            &cfg.ReindexLeaseDuration,
		"ZHIXU_REINDEX_HEARTBEAT_INTERVAL":        &cfg.ReindexHeartbeatInterval,
		"ZHIXU_EMBEDDING_TIMEOUT":                 &cfg.EmbeddingTimeout,
		"ZHIXU_CHAT_TIMEOUT":                      &cfg.ChatTimeout,
		"ZHIXU_WEB_FETCH_TIMEOUT":                 &cfg.WebFetchTimeout,
		"ZHIXU_WEB_FETCH_RESPONSE_HEADER_TIMEOUT": &cfg.WebFetchResponseHeaderTimeout,
		"ZHIXU_WEB_FETCH_TLS_HANDSHAKE_TIMEOUT":   &cfg.WebFetchTLSHandshakeTimeout,

		"ZHIXU_WORKER_SOFT_STOP_TIMEOUT": &cfg.WorkerSoftStopTimeout,
		"ZHIXU_WORKER_HARD_STOP_TIMEOUT": &cfg.WorkerHardStopTimeout,
		"ZHIXU_AUTH_SESSION_TTL":         &cfg.AuthSessionTTL,
		"ZHIXU_AUTH_API_TOKEN_TTL":       &cfg.AuthAPITokenTTL,
	} {
		if value, ok := lookup(key); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				if key == "ZHIXU_GRAPH_QUERY_TIMEOUT" || key == "ZHIXU_EMBEDDING_TIMEOUT" || key == "ZHIXU_CHAT_TIMEOUT" || strings.HasPrefix(key, "ZHIXU_WEB_FETCH_") {
					return fmt.Errorf("parse %s: invalid duration", key)
				}
				return fmt.Errorf("parse %s: %w", key, err)
			}
			*target = parsed
		}
	}
	if value, ok := lookup("ZHIXU_TELEMETRY_MODE"); ok {
		cfg.TelemetryMode = TelemetryMode(value)
		if cfg.TelemetryMode == TelemetryModeDisabled {
			cfg.TelemetryEndpoint = ""
		}
	}
	if cfg.TelemetryMode != TelemetryModeDisabled {
		if value, ok := lookup("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
			cfg.TelemetryEndpoint = value
		}
	}
	return nil
}

func parseWebFetchContentTypes(raw string) ([]string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return nil, errors.New("parse ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES: invalid content type list")
	}
	values := strings.Split(raw, ",")
	if err := validateWebFetchContentTypes(values); err != nil {
		return nil, errors.New("parse ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES: invalid content type list")
	}
	return append([]string(nil), values...), nil
}
