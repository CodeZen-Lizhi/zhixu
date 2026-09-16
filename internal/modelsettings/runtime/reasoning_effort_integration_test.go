//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	modelpostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// 此回环夹具是显式存储的兼容旧版端点；
// 公开草稿校验仍要求新的远程设置使用 HTTPS。
func TestReasoningEffortPersistedRevisionReachesRuntimeConnectionProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:11434")
	if err != nil {
		t.Fatalf("isolated managed relay fixture cannot bind: %v", err)
	}
	requests := make(chan map[string]json.RawMessage, 16)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Error("unexpected provider request route")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fixture-call","object":"chat.completion","created":1,"model":"gpt-6-astra","choices":[{"index":0,"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()

	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 4})
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{0x49}, 32))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := auditpostgres.NewGORMStore(fixture.Pool())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := modelpostgres.NewGORMRepository(fixture.Pool(), modelpostgres.WithGORMSecretSealer(sealer), modelpostgres.WithGORMAuditAppender(audit))
	if err != nil {
		t.Fatal(err)
	}
	settings := domain.CanonicalDisabledSettings()
	settings.Chat.Provider = domain.ChatProviderOpenAICompatible
	settings.Chat.BaseURL = managedOllamaBaseURL
	settings.Chat.Model, settings.Chat.ModelVersion = "gpt-6-astra", "gpt-6-astra"
	settings.Chat.ReasoningEffort = "medium"
	settings.Chat.ReasoningEffortByFunction = domain.ReasoningEffortOverrides{
		domain.ReasoningFileProfile: "low", domain.ReasoningMainNoteSynthesis: "high", domain.ReasoningNoteInterview: "",
	}
	secret, err := domain.ReplaceSecret("isolated-reasoning-key")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Value.Destroy()
	ctx := context.Background()
	saved, err := repository.SaveDesired(ctx, application.SaveCommand{Settings: settings,
		ChatSecret: secret, EmbeddingSecret: domain.ClearSecret(), CreatedBy: "reasoning-runtime-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	settings.Chat.ReasoningEffort = "high"
	settings.Chat.ReasoningEffortByFunction[domain.ReasoningFileProfile] = "max"
	_, err = repository.SaveDesired(ctx, application.SaveCommand{ExpectedRevision: saved.DesiredRevision, Settings: settings,
		ChatSecret: domain.KeepSecret(), EmbeddingSecret: domain.ClearSecret(), CreatedBy: "reasoning-runtime-fixture"})
	if err != nil {
		t.Fatal(err)
	}

	for _, revision := range []int64{1, 2} {
		resolved, loadErr := repository.LoadRevision(ctx, revision)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		models, buildErr := Build(config.Defaults(), resolved)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		wants := map[domain.ReasoningFunction]string{domain.ReasoningFileProfile: "low", domain.ReasoningMainNoteSynthesis: "high", domain.ReasoningKnowledgeQNA: "medium", domain.ReasoningNoteInterview: ""}
		if revision == 2 {
			wants[domain.ReasoningFileProfile] = "max"
			wants[domain.ReasoningKnowledgeQNA] = "high"
		}
		readWire := func(want string, budget string) {
			t.Helper()
			select {
			case body := <-requests:
				var actual string
				_ = json.Unmarshal(body["reasoning_effort"], &actual)
				if actual != want || (want == "" && body["reasoning_effort"] != nil) || body["temperature"] != nil || body["max_tokens"] != nil || string(body["max_completion_tokens"]) != budget {
					t.Fatal("persisted function setting did not control the actual request")
				}
			case <-time.After(time.Second):
				t.Fatal("configured request did not reach local provider")
			}
		}
		for _, function := range []domain.ReasoningFunction{domain.ReasoningFileProfile, domain.ReasoningMainNoteSynthesis, domain.ReasoningKnowledgeQNA, domain.ReasoningNoteInterview} {
			if err := models.ChatFor(function).Model().(platformmodels.ChatConnectionProber).ProbeConnection(ctx); err != nil {
				t.Fatal(err)
			}
			readWire(wants[function], "2048")
			if _, err := models.RuntimeChatFor(function).Model().Generate(ctx, []*schema.Message{{Role: schema.User, Content: "test"}}, einomodel.WithMaxTokens(256)); err != nil {
				t.Fatal(err)
			}
			readWire(wants[function], "256")
		}
		if err := models.Close(); err != nil {
			t.Fatal(err)
		}
		if err := NewConnectionTester(config.Defaults()).TestResolvedConnection(ctx, application.ConnectionTargetChat, resolved); err != nil {
			t.Fatal(err)
		}
		resolved.ChatAPIKey.Destroy()
		resolved.EmbeddingAPIKey.Destroy()
		expected := map[string]bool{}
		for _, want := range wants {
			expected[want] = true
		}
		for range len(expected) {
			select {
			case body := <-requests:
				var actual string
				_ = json.Unmarshal(body["reasoning_effort"], &actual)
				if !expected[actual] {
					t.Fatal("connection tester repeated or introduced an effort")
				}
				delete(expected, actual)
			case <-time.After(time.Second):
				t.Fatal("connection tester omitted an effective effort")
			}
		}
		if len(expected) != 0 || len(requests) != 0 {
			t.Fatal("connection tester did not probe exactly the unique efforts")
		}
	}
	snapshot, err := repository.Snapshot(ctx, 20*time.Second)
	if err != nil || snapshot.DesiredRevision != 2 || snapshot.ActiveRevision != 0 || snapshot.DesiredSettings.Settings.Chat.ReasoningEffort != "high" {
		t.Fatal("connection probes mutated desired or active revision")
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "isolated-reasoning-key") {
		t.Fatal("settings snapshot exposed the credential")
	}
}
