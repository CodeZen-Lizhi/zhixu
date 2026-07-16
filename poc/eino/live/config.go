package live

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

const (
	EnvEnabled = "ZHIXU_EINO_LIVE_ENABLED"
	EnvAPIKey  = "ZHIXU_EINO_LIVE_API_KEY"
	EnvModel   = "ZHIXU_EINO_LIVE_MODEL"
	EnvBaseURL = "ZHIXU_EINO_LIVE_BASE_URL"
	EnvTimeout = "ZHIXU_EINO_LIVE_TIMEOUT"

	defaultTimeout = 30 * time.Second
)

// Config contains the explicitly enabled OpenAI-compatible smoke configuration.
type Config struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

// MissingRequiredConfigError identifies missing live credentials without exposing values.
type MissingRequiredConfigError struct {
	EnvironmentVariables []string
}

func (e *MissingRequiredConfigError) Error() string {
	return fmt.Sprintf("missing required live environment variables: %s", strings.Join(e.EnvironmentVariables, ", "))
}

// LoadConfigFromEnv reads live smoke configuration without performing network calls.
func LoadConfigFromEnv() (Config, bool, error) {
	return loadConfig(os.LookupEnv)
}

// Validate rejects incomplete, unsafe, or unbounded live provider configuration.
func (c Config) Validate() error {
	missing := make([]string, 0, 2)
	if strings.TrimSpace(c.APIKey) == "" {
		missing = append(missing, EnvAPIKey)
	}
	if strings.TrimSpace(c.Model) == "" {
		missing = append(missing, EnvModel)
	}
	if len(missing) > 0 {
		return &MissingRequiredConfigError{EnvironmentVariables: missing}
	}
	if c.Timeout <= 0 {
		return errors.New("live timeout must be greater than zero")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil
	}

	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return errors.New("live base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("live base URL must use http or https")
	}
	if parsed.Host == "" {
		return errors.New("live base URL must include a host")
	}
	if parsed.User != nil {
		return errors.New("live base URL must not include credentials")
	}
	if parsed.Fragment != "" {
		return errors.New("live base URL must not include a fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return errors.New("live base URL may use http only for a loopback host")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// NewChatModel constructs the official Eino OpenAI-compatible model adapter.
func NewChatModel(ctx context.Context, config Config) (model.BaseChatModel, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate live chat model config: %w", err)
	}

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
		Timeout: config.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create live chat model: %w", err)
	}
	return chatModel, nil
}

type lookupEnv func(string) (string, bool)

func loadConfig(lookup lookupEnv) (Config, bool, error) {
	enabledValue, exists := lookup(EnvEnabled)
	if !exists || strings.TrimSpace(enabledValue) == "" {
		return Config{}, false, nil
	}

	enabled, err := strconv.ParseBool(strings.TrimSpace(enabledValue))
	if err != nil {
		return Config{}, false, fmt.Errorf("parse %s: expected a boolean", EnvEnabled)
	}
	if !enabled {
		return Config{}, false, nil
	}

	timeout := defaultTimeout
	if value, ok := lookup(EnvTimeout); ok && strings.TrimSpace(value) != "" {
		timeout, err = time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return Config{}, true, fmt.Errorf("parse %s: %w", EnvTimeout, err)
		}
	}

	config := Config{
		APIKey:  lookupValue(lookup, EnvAPIKey),
		Model:   lookupValue(lookup, EnvModel),
		BaseURL: lookupValue(lookup, EnvBaseURL),
		Timeout: timeout,
	}
	if err := config.Validate(); err != nil {
		return Config{}, true, err
	}
	return config, true, nil
}

func lookupValue(lookup lookupEnv, name string) string {
	value, _ := lookup(name)
	return strings.TrimSpace(value)
}
