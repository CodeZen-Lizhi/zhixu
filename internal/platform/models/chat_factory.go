package models

import (
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
)

// NewConfiguredChatModel 按进程配置和可选项目 telemetry 构造 API 与 Worker 共用的 Chat Adapter。
func NewConfiguredChatModel(cfg config.Config, telemetry ...ModelTelemetry) (agentapplication.ChatModel, error) {
	switch cfg.ChatProvider {
	case config.ChatProviderDisabled:
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeChatCapabilityUnavailable, false, errChatCapabilityOff)
	case config.ChatProviderOpenAICompatible, config.ChatProviderOllama:
		options := OpenAIChatOptions{
			BaseURL: cfg.ChatBaseURL, APIKey: cfg.ChatAPIKey, Model: cfg.ChatModel, ModelVersion: cfg.ChatModelVersion,
			AdapterVersion: cfg.ChatAdapterVersion, Timeout: cfg.ChatTimeout,
			MaxRequestBytes: cfg.ChatMaxRequestBytes, MaxResponseBytes: cfg.ChatMaxResponseBytes,
			APIStyle: ChatAPIStyle(cfg.ChatAPIStyle), Provider: string(cfg.ChatProvider),
		}
		model, err := NewEinoOpenAIChatModel(options, telemetry...)
		if err != nil {
			return nil, err
		}
		return model, nil
	default:
		return nil, foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeChatConfigInvalid, false, errors.New("chat provider is unsupported"))
	}
}
