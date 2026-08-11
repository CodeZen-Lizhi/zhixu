// Package config loads and validates process configuration.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authorigin "github.com/CodeZen-Lizhi/zhixu/internal/auth/origin"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/operability"
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

// ChatAPIStyle selects the explicit OpenAI-compatible Chat endpoint contract.
type ChatAPIStyle string

const (
	ChatAPIStyleChatCompletions ChatAPIStyle = "chat_completions"
	ChatAPIStyleResponses       ChatAPIStyle = "responses"
)

// ModelSettingsMode 控制模型身份来自静态配置还是受管数据库 revision。
type ModelSettingsMode string

const (
	// ModelSettingsModeStatic 保持现有 Env/YAML 模型配置行为。
	ModelSettingsModeStatic ModelSettingsMode = "static"
	// ModelSettingsModeManaged 从数据库加载 active 或固定 rollout target。
	ModelSettingsModeManaged ModelSettingsMode = "managed"
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
	AppName                    string        `yaml:"app_name" validate:"notblank"`
	Version                    string        `yaml:"version" validate:"notblank"`
	Environment                string        `yaml:"environment"`
	HTTPAddr                   string        `yaml:"http_addr" validate:"notblank"`
	DatabaseURL                string        `yaml:"database_url"`
	DatabaseHost               string        `yaml:"database_host"`
	DatabasePort               string        `yaml:"database_port"`
	DatabaseName               string        `yaml:"database_name"`
	DatabaseUser               string        `yaml:"database_user"`
	DatabasePassword           string        `yaml:"database_password"`
	DatabaseMaxConns           int32         `yaml:"database_max_conns" validate:"gte=0"`
	DatabaseMinConns           int32         `yaml:"database_min_conns" validate:"gte=0"`
	DatabasePingTimeout        time.Duration `yaml:"database_ping_timeout" validate:"gt=0"`
	GraphQueryTimeout          time.Duration `yaml:"graph_query_timeout" validate:"gt=0"`
	HealthInterval             time.Duration `yaml:"health_interval" validate:"gt=0"`
	ShutdownTimeout            time.Duration `yaml:"shutdown_timeout" validate:"gt=0"`
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
	ChatAPIStyle         ChatAPIStyle  `yaml:"chat_api_style"`
	ChatBaseURL          string        `yaml:"chat_base_url"`
	ChatAPIKey           string        `yaml:"chat_api_key"`
	ChatModel            string        `yaml:"chat_model"`
	ChatModelVersion     string        `yaml:"chat_model_version"`
	ChatAdapterVersion   string        `yaml:"chat_adapter_version"`
	ChatTimeout          time.Duration `yaml:"chat_timeout"`
	ChatMaxRequestBytes  int64         `yaml:"chat_max_request_bytes"`
	ChatMaxResponseBytes int64         `yaml:"chat_max_response_bytes"`

	// ModelSettingsMode 显式选择静态或数据库受管的模型设置来源。
	ModelSettingsMode ModelSettingsMode `yaml:"model_settings_mode"`
	// ModelSettingsKeyFile 是只读 AES-256-GCM 主密钥文件路径。
	ModelSettingsKeyFile string `yaml:"model_settings_key_file"`
	// GitSyncKeyFile 是 Git Remote Token 的只读 AES-256-GCM 主密钥文件路径。
	GitSyncKeyFile string `yaml:"git_sync_key_file"`
	// ModelSettingsRolloutID 固定候选进程只能加载指定 rollout target。
	ModelSettingsRolloutID string `yaml:"model_settings_rollout_id"`
	// ModelSettingsPrepared 让候选进程在 commit 前只登记 prepared，不接收工作。
	ModelSettingsPrepared bool `yaml:"model_settings_prepared"`

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
		ChatAPIStyle:         ChatAPIStyleChatCompletions,
		ChatAdapterVersion:   defaultChatAdapterVersion,
		ChatTimeout:          defaultChatTimeout,
		ChatMaxRequestBytes:  defaultChatMaxRequestBytes,
		ChatMaxResponseBytes: defaultChatMaxResponseBytes,
		ModelSettingsMode:    ModelSettingsModeStatic,

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

// LoadWorker reads Worker configuration through the non-API profile. It does
// not query or retain API-only Bootstrap/Review secrets and skips their
// validation, while still validating every other shared configuration group.
func LoadWorker(path string) (Config, error) {
	return loadNonAPIWithLookup(path, os.LookupEnv)
}

// LoadMigration uses the same non-API profile as Worker and ModelCtl. It does
// not query or retain API-only Bootstrap/Review secrets, but it deliberately
// keeps validation for every other shared configuration group rather than
// reducing migration startup to database-only validation.
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
	loader, err := newConfigLoader(lookup, options)
	if err != nil {
		return Config{}, err
	}
	return loader.load(path)
}

// Validate checks process settings that must be valid before starting a
// server. An empty DatabaseURL is allowed so API readiness can report a real
// degraded state; Worker startup separately calls ValidateDatabase.
func (c Config) Validate() error {
	return c.validate(true)
}

// ValidateModels 只校验共享模型配置与受管设置来源，供非 API Composition Root 复用。
func (c Config) ValidateModels() error {
	if err := c.validateEmbedding(); err != nil {
		return err
	}
	if err := c.validateChat(); err != nil {
		return err
	}
	return c.validateModelSettings()
}

func (c Config) validate(validateAuth bool) error {
	configValidation, err := newConfigValidator()
	if err != nil {
		return err
	}
	return c.validateWith(configValidation, validateAuth)
}

func (c Config) validateWith(configValidation *configValidator, validateAuth bool) error {
	if err := configValidation.validate(c); err != nil {
		return err
	}
	if c.DatabaseMaxConns > 0 && c.DatabaseMinConns > c.DatabaseMaxConns {
		return errors.New("database_min_conns must not exceed database_max_conns")
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
	if err := c.validateModelSettings(); err != nil {
		return err
	}
	if err := c.validateGitSyncKeyFile(); err != nil {
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

func (c Config) validateModelSettings() error {
	switch c.ModelSettingsMode {
	case ModelSettingsModeStatic:
		if c.ModelSettingsKeyFile != "" || c.ModelSettingsRolloutID != "" || c.ModelSettingsPrepared {
			return errors.New("managed model settings fields must be empty in static mode")
		}
		return nil
	case ModelSettingsModeManaged:
		if c.ModelSettingsKeyFile == "" || c.ModelSettingsKeyFile != strings.TrimSpace(c.ModelSettingsKeyFile) || !filepath.IsAbs(c.ModelSettingsKeyFile) || filepath.Clean(c.ModelSettingsKeyFile) != c.ModelSettingsKeyFile {
			return errors.New("model_settings_key_file must be a canonical absolute path in managed mode")
		}
		if c.ModelSettingsRolloutID != strings.TrimSpace(c.ModelSettingsRolloutID) || len(c.ModelSettingsRolloutID) > 128 || strings.ContainsAny(c.ModelSettingsRolloutID, "\r\n\t") {
			return errors.New("model_settings_rollout_id is invalid")
		}
		if c.ModelSettingsPrepared && c.ModelSettingsRolloutID == "" {
			return errors.New("model_settings_prepared requires model_settings_rollout_id")
		}
		return nil
	default:
		return errors.New("model_settings_mode must be static or managed")
	}
}

func (c Config) validateGitSyncKeyFile() error {
	if c.GitSyncKeyFile == "" {
		return nil
	}
	if c.GitSyncKeyFile != strings.TrimSpace(c.GitSyncKeyFile) || strings.ContainsAny(c.GitSyncKeyFile, "\x00\r\n\t") ||
		!filepath.IsAbs(c.GitSyncKeyFile) || filepath.Clean(c.GitSyncKeyFile) != c.GitSyncKeyFile {
		return errors.New("git_sync_key_file must be a canonical absolute path when configured")
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
	if c.ChatAPIStyle != ChatAPIStyleChatCompletions && c.ChatAPIStyle != ChatAPIStyleResponses {
		return errors.New("chat_api_style must be chat_completions or responses")
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
		parsed, _ := url.Parse(c.ChatBaseURL)
		cleanedPath := strings.TrimRight(parsed.Path, "/")
		if (c.ChatAPIStyle == ChatAPIStyleResponses && strings.HasSuffix(cleanedPath, "/v1/chat/completions")) ||
			(c.ChatAPIStyle == ChatAPIStyleChatCompletions && strings.HasSuffix(cleanedPath, "/v1/responses")) {
			return errors.New("chat_base_url targets a different chat_api_style")
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
		"Config{AppName:%q Version:%q Environment:%q HTTPAddr:%q DatabaseConfigured:%t DatabaseMaxConns:%d DatabaseMinConns:%d DatabasePingTimeout:%s GraphQueryTimeout:%s HealthInterval:%s ShutdownTimeout:%s WebAssetsDir:%q WorkerQueue:%q WorkerMaxWorkers:%d WorkerJobTimeout:%s WorkerRescueStuckJobsAfter:%s WorkflowLeaseDuration:%s WorkflowHeartbeatInterval:%s ReindexDispatchPollInterval:%s ReindexDispatchBatchSize:%d ReindexDispatchErrorBackoff:%s ReindexLeaseDuration:%s ReindexHeartbeatInterval:%s ModelSettingsMode:%q ModelSettingsKeyConfigured:%t GitSyncKeyConfigured:%t ModelSettingsRolloutConfigured:%t ModelSettingsPrepared:%t EmbeddingProvider:%q EmbeddingConfigured:%t EmbeddingModel:%q EmbeddingDimensions:%d EmbeddingNormalization:%q EmbeddingDistanceMetric:%q EmbeddingMaxBatchSize:%d EmbeddingMaxInputBytes:%d EmbeddingMaxBatchInputBytes:%d EmbeddingTimeout:%s EmbeddingMaxResponseBytes:%d ChatProvider:%q ChatConfigured:%t ChatAPIStyle:%q ChatModel:%q ChatModelVersion:%q ChatAdapterVersion:%q ChatTimeout:%s ChatMaxRequestBytes:%d ChatMaxResponseBytes:%d ToolRuntimeMode:%q WebFetchMode:%q WebFetchTimeout:%s WebFetchResponseHeaderTimeout:%s WebFetchTLSHandshakeTimeout:%s WebFetchMaxRedirects:%d WebFetchMaxURLBytes:%d WebFetchMaxResponseHeaderBytes:%d WebFetchMaxBodyBytes:%d WebFetchMaxTextBytes:%d WebFetchMaxResolvedIPs:%d WebFetchAllowedContentTypeCount:%d RetrievalRRFK:%d RetrievalRRFLexicalCandidateLimit:%d RetrievalRRFVectorCandidateLimit:%d RetrievalRRFFusedCandidateLimit:%d RetrievalRRFRerankCandidateLimit:%d WorkerSoftStopTimeout:%s WorkerHardStopTimeout:%s WorkerHealthAddr:%q TelemetryMode:%q TelemetryConfigured:%t AuthMode:%q AuthConfigured:%t AuthSessionTTL:%s AuthAPITokenTTL:%s AuthSecureCookie:%t AuthAllowedOriginCount:%d ReviewQuestionRefConfigured:%t}",
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
		c.ModelSettingsMode,
		strings.TrimSpace(c.ModelSettingsKeyFile) != "",
		strings.TrimSpace(c.GitSyncKeyFile) != "",
		strings.TrimSpace(c.ModelSettingsRolloutID) != "",
		c.ModelSettingsPrepared,
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
		c.ChatAPIStyle,
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
