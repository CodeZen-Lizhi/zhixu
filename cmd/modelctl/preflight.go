package main

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
)

type modelPreflighter struct {
	tester *modelruntime.ConnectionTester
}

func (preflighter modelPreflighter) Preflight(ctx context.Context, role modeldomain.RuntimeRole, resolved modeldomain.ResolvedSettings) error {
	if ctx == nil || !modeldomain.ValidRuntimeRole(role) || resolved.Revision < 0 || preflighter.tester == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, modeldomain.ErrorCodeInvalid, false, errors.New("model preflight input is invalid"))
	}
	if resolved.Settings.Chat.Provider != modeldomain.ChatProviderDisabled {
		if err := preflighter.tester.TestChat(ctx, resolved.Settings.Chat, resolved.ChatAPIKey); err != nil {
			return err
		}
	}
	if resolved.Settings.Embedding.Provider != modeldomain.EmbeddingProviderDisabled {
		if err := preflighter.tester.TestEmbedding(ctx, resolved.Settings.Embedding, resolved.EmbeddingAPIKey); err != nil {
			return err
		}
	}
	return nil
}
