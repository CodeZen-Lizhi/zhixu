package live

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/chatgraph"
	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
)

func TestOpenAICompatibleChatSmoke(t *testing.T) {
	config, enabled, err := LoadConfigFromEnv()
	if err != nil {
		var missing *MissingRequiredConfigError
		if errors.As(err, &missing) {
			t.Skipf("live smoke enabled but credentials are incomplete: %v", err)
		}
		t.Fatalf("load live smoke config: %v", err)
	}
	if !enabled {
		t.Skipf("live smoke disabled; set %s=true to enable it", EnvEnabled)
	}

	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	chatModel, err := NewChatModel(ctx, config)
	if err != nil {
		t.Fatalf("create live chat model: %v", err)
	}
	runner, err := chatgraph.New(ctx, chatModel)
	if err != nil {
		t.Fatalf("create live chat graph: %v", err)
	}

	response, err := runner.Run(ctx, contract.ChatRequest{
		Messages: []contract.Message{
			{Role: contract.RoleSystem, Content: "Return a concise plain-text response."},
			{Role: contract.RoleUser, Content: "Reply with: eino-live-ready"},
		},
	})
	if err != nil {
		t.Fatalf("run live chat graph: %v", err)
	}
	if strings.TrimSpace(response.Message.Content) == "" {
		t.Fatal("live provider returned an empty response")
	}
}
