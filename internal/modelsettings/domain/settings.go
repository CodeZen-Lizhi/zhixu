package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ManagedOllamaBaseURL is the only endpoint accepted for the managed local runtime.
const ManagedOllamaBaseURL = "http://127.0.0.1:11434"

// ChatProvider identifies the configured structured-chat protocol.
type ChatProvider string

const (
	ChatProviderDisabled         ChatProvider = "disabled"
	ChatProviderOpenAICompatible ChatProvider = "openai-compatible"
	ChatProviderOllama           ChatProvider = "ollama"
)

// ChatAPIStyle selects the explicit OpenAI-compatible Chat wire protocol.
type ChatAPIStyle string

const (
	ChatAPIStyleChatCompletions ChatAPIStyle = "chat_completions"
	ChatAPIStyleResponses       ChatAPIStyle = "responses"
)

// EmbeddingProvider identifies the configured embedding protocol.
type EmbeddingProvider string

const (
	EmbeddingProviderDisabled         EmbeddingProvider = "disabled"
	EmbeddingProviderOpenAICompatible EmbeddingProvider = "openai-compatible"
	EmbeddingProviderOllama           EmbeddingProvider = "ollama"
)

// EmbeddingNormalization controls persisted vector normalization.
type EmbeddingNormalization string

const (
	NormalizationNone EmbeddingNormalization = "none"
	NormalizationL2   EmbeddingNormalization = "l2"
)

// DistanceMetric controls persisted vector distance semantics.
type DistanceMetric string

const (
	DistanceCosine       DistanceMetric = "cosine"
	DistanceInnerProduct DistanceMetric = "inner_product"
	DistanceEuclidean    DistanceMetric = "euclidean"
)

// ChatSettings freezes every non-secret chat setting that affects runtime behavior.
type ChatSettings struct {
	Provider         ChatProvider
	APIStyle         ChatAPIStyle
	BaseURL          string
	Model            string
	ModelVersion     string
	AdapterVersion   string
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

// EmbeddingSettings freezes every non-secret embedding setting that affects an index contract.
type EmbeddingSettings struct {
	Provider           EmbeddingProvider
	BaseURL            string
	Model              string
	Dimensions         int32
	Normalization      EmbeddingNormalization
	DistanceMetric     DistanceMetric
	MaxBatchSize       int32
	MaxInputBytes      int32
	MaxBatchInputBytes int64
	Timeout            time.Duration
	MaxResponseBytes   int64
}

// Settings is one immutable model configuration revision without credentials.
type Settings struct {
	Chat      ChatSettings
	Embedding EmbeddingSettings
}

// ManagedOllamaRequirement is the canonical, non-secret local model demand.
type ManagedOllamaRequirement struct {
	Required bool
	Models   []string
	Hash     string
}

// RequiresManagedOllama derives local runtime demand from explicit providers.
// The exact relay check is retained only for append-only legacy Chat revisions.
func RequiresManagedOllama(settings Settings) ManagedOllamaRequirement {
	models := make([]string, 0, 2)
	if settings.Chat.Provider == ChatProviderOllama ||
		settings.Chat.Provider == ChatProviderOpenAICompatible && settings.Chat.BaseURL == ManagedOllamaBaseURL {
		models = append(models, settings.Chat.Model)
	}
	if settings.Embedding.Provider == EmbeddingProviderOllama {
		models = append(models, settings.Embedding.Model)
	}
	slicesSortUnique(&models)
	if len(models) == 0 {
		return ManagedOllamaRequirement{}
	}
	digest := sha256.Sum256([]byte("managed-ollama-requirement/v1\n" + strings.Join(models, "\n")))
	return ManagedOllamaRequirement{Required: true, Models: models, Hash: hex.EncodeToString(digest[:])}
}

func slicesSortUnique(values *[]string) {
	slices.Sort(*values)
	output := (*values)[:0]
	for _, value := range *values {
		if len(output) == 0 || output[len(output)-1] != value {
			output = append(output, value)
		}
	}
	*values = output
}

// SecretConfiguration describes credential presence without exposing a value.
type SecretConfiguration struct {
	ChatConfigured      bool
	EmbeddingConfigured bool
}

// SettingsSummary is safe to return from read APIs.
type SettingsSummary struct {
	Settings Settings
	Secrets  SecretConfiguration
}

// ResolvedSettings is an in-process configuration with short-lived credentials.
type ResolvedSettings struct {
	Revision        int64
	Settings        Settings
	ChatAPIKey      Secret
	EmbeddingAPIKey Secret
}

// CanonicalDisabledSettings returns the revision-zero settings projection.
func CanonicalDisabledSettings() Settings {
	return Settings{
		Chat: ChatSettings{
			Provider: ChatProviderDisabled, APIStyle: ChatAPIStyleChatCompletions, AdapterVersion: "v1", Timeout: 30 * time.Second,
			MaxRequestBytes: 4 << 20, MaxResponseBytes: 4 << 20,
		},
		Embedding: EmbeddingSettings{
			Provider: EmbeddingProviderDisabled, Normalization: NormalizationL2,
			DistanceMetric: DistanceCosine, MaxBatchSize: 128, MaxInputBytes: 64 * 1024,
			MaxBatchInputBytes: 8 * 1024 * 1024, Timeout: 30 * time.Second,
			MaxResponseBytes: 64 << 20,
		},
	}
}

// ValidateStructural enforces storage-safe invariants; the configured factory remains the full validator.
func (settings Settings) ValidateStructural() error {
	if err := settings.Chat.validate(); err != nil {
		return err
	}
	if err := settings.Embedding.validate(); err != nil {
		return err
	}
	return nil
}

// ValidateWritableSettings rejects legacy endpoint inference for newly submitted drafts.
// Historical revisions are intentionally validated through ValidateStructural instead.
func ValidateWritableSettings(settings Settings) error {
	switch settings.Chat.Provider {
	case ChatProviderOpenAICompatible:
		if !usesHTTPS(settings.Chat.BaseURL) {
			return invalid("new openai-compatible chat settings must use https")
		}
	case ChatProviderOllama:
		if settings.Chat.BaseURL != ManagedOllamaBaseURL {
			return invalid("ollama chat must use the managed relay")
		}
	}
	switch settings.Embedding.Provider {
	case EmbeddingProviderOpenAICompatible:
		if !usesHTTPS(settings.Embedding.BaseURL) {
			return invalid("new openai-compatible embedding settings must use https")
		}
	case EmbeddingProviderOllama:
		if settings.Embedding.BaseURL != ManagedOllamaBaseURL {
			return invalid("ollama embedding must use the managed relay")
		}
	}
	return nil
}

func usesHTTPS(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https"
}

// CanonicalizeSettings normalizes endpoint identity before validation, encryption, or persistence.
func CanonicalizeSettings(settings Settings) (Settings, error) {
	if settings.Chat.Provider != ChatProviderDisabled {
		normalized, err := NormalizeBaseURL(settings.Chat.BaseURL)
		if err != nil {
			return Settings{}, err
		}
		settings.Chat.BaseURL = normalized
	}
	if settings.Embedding.Provider != EmbeddingProviderDisabled {
		normalized, err := NormalizeBaseURL(settings.Embedding.BaseURL)
		if err != nil {
			return Settings{}, err
		}
		settings.Embedding.BaseURL = normalized
	}
	if err := settings.ValidateStructural(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func (settings ChatSettings) validate() error {
	if !canonicalStoredDuration(settings.Timeout) || settings.Timeout > 5*time.Minute || settings.MaxRequestBytes <= 0 ||
		settings.MaxRequestBytes > 16<<20 || settings.MaxResponseBytes <= 0 || settings.MaxResponseBytes > 16<<20 ||
		!canonicalText(settings.AdapterVersion, 64) {
		return invalid("chat limits or adapter version are invalid")
	}
	if settings.APIStyle != ChatAPIStyleChatCompletions && settings.APIStyle != ChatAPIStyleResponses {
		return invalid("chat API style is invalid")
	}
	switch settings.Provider {
	case ChatProviderDisabled:
		if settings.BaseURL != "" || settings.Model != "" || settings.ModelVersion != "" {
			return invalid("disabled chat settings must not contain a model target")
		}
	case ChatProviderOpenAICompatible, ChatProviderOllama:
		if !canonicalText(settings.Model, 128) || !canonicalText(settings.ModelVersion, 64) {
			return invalid("chat model identity is invalid")
		}
		if settings.Provider == ChatProviderOllama && settings.APIStyle != ChatAPIStyleChatCompletions {
			return invalid("ollama chat must use chat completions")
		}
		if normalized, err := NormalizeBaseURL(settings.BaseURL); err != nil || normalized != settings.BaseURL {
			if err == nil {
				return invalid("chat model base URL is not canonical")
			}
			return err
		}
	default:
		return invalid("chat provider is invalid")
	}
	return nil
}

func (settings EmbeddingSettings) validate() error {
	if settings.MaxBatchSize <= 0 || settings.MaxBatchSize > 1000 || settings.MaxInputBytes <= 0 ||
		settings.MaxInputBytes > 10<<20 || settings.MaxBatchInputBytes < int64(settings.MaxInputBytes) ||
		settings.MaxBatchInputBytes > 64<<20 || !canonicalStoredDuration(settings.Timeout) || settings.Timeout > 5*time.Minute ||
		settings.MaxResponseBytes <= 0 || settings.MaxResponseBytes > 128<<20 {
		return invalid("embedding limits are invalid")
	}
	if settings.Normalization != NormalizationNone && settings.Normalization != NormalizationL2 {
		return invalid("embedding normalization is invalid")
	}
	switch settings.DistanceMetric {
	case DistanceCosine, DistanceInnerProduct, DistanceEuclidean:
	default:
		return invalid("embedding distance metric is invalid")
	}
	switch settings.Provider {
	case EmbeddingProviderDisabled:
		if settings.BaseURL != "" || settings.Model != "" || settings.Dimensions != 0 {
			return invalid("disabled embedding settings must not contain a model target")
		}
	case EmbeddingProviderOpenAICompatible, EmbeddingProviderOllama:
		if !canonicalText(settings.Model, 128) || settings.Dimensions <= 0 || settings.Dimensions > 16000 {
			return invalid("embedding model identity is invalid")
		}
		if normalized, err := NormalizeBaseURL(settings.BaseURL); err != nil || normalized != settings.BaseURL {
			if err == nil {
				return invalid("embedding model base URL is not canonical")
			}
			return err
		}
	default:
		return invalid("embedding provider is invalid")
	}
	return nil
}

func canonicalStoredDuration(value time.Duration) bool {
	return value >= time.Microsecond && value%time.Microsecond == 0
}

// NormalizeBaseURL returns the stable endpoint identity used by secret AAD and keep checks.
func NormalizeBaseURL(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 || !utf8.ValidString(raw) {
		return "", invalid("model base URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", invalid("model base URL is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalid("model base URL scheme is invalid")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.TrimSpace(hostname) != hostname {
		return "", invalid("model base URL host is invalid")
	}
	port := parsed.Port()
	if port != "" {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 {
			return "", invalid("model base URL port is invalid")
		}
		if scheme == "https" && value == 443 || scheme == "http" && value == 80 {
			port = ""
		}
	}
	if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Scheme = scheme
	if parsed.Path == "/" {
		parsed.Path = ""
	} else {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}
	return parsed.String(), nil
}

func canonicalText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalid, false, errors.New(message))
}

func (settings Settings) String() string {
	return fmt.Sprintf("Settings{chat_provider:%q chat_model:%q embedding_provider:%q embedding_model:%q embedding_dimensions:%d}",
		settings.Chat.Provider, settings.Chat.Model, settings.Embedding.Provider, settings.Embedding.Model, settings.Embedding.Dimensions)
}

func (settings Settings) GoString() string { return settings.String() }
