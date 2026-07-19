package models

import (
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

// NewConfiguredChatModel 按进程配置构造 API 与 Worker 共用的 Chat Adapter。
func NewConfiguredChatModel(cfg config.Config) (agentapplication.ChatModel, error) {
	switch cfg.ChatProvider {
	case config.ChatProviderDisabled:
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	case config.ChatProviderOpenAICompatible:
		return NewOpenAICompatibleChatModel(OpenAIChatOptions{
			BaseURL: cfg.ChatBaseURL, APIKey: cfg.ChatAPIKey, Model: cfg.ChatModel, ModelVersion: cfg.ChatModelVersion,
			AdapterVersion: cfg.ChatAdapterVersion, Timeout: cfg.ChatTimeout,
			MaxRequestBytes: cfg.ChatMaxRequestBytes, MaxResponseBytes: cfg.ChatMaxResponseBytes,
		})
	default:
		return nil, foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeChatConfigInvalid, false, errors.New("chat provider is unsupported"))
	}
}
