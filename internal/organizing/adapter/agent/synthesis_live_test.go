package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// 与 cmd/worker 的 agentStructuredMaxOutputTokens 一致：提供方的思考可能占用与结构化内容相同的输出额度。下方另行限制调用次数和测试组截止时间。
const synthesisLiveTokens = 8192

// 这是提供方与内容实验，不验证 PostgreSQL、River、准入或发布流程。独立评审即使通过，也仍需人工检查。
func TestSynthesisLiveQuality(t *testing.T) {
	switch os.Getenv("ZHIXU_EINO_LIVE_SYNTHESIS_ENABLED") {
	case "", "false":
		t.Skip("live synthesis disabled; set ZHIXU_EINO_LIVE_SYNTHESIS_ENABLED=true")
	case "true":
	default:
		t.Fatal("ZHIXU_EINO_LIVE_SYNTHESIS_ENABLED must be true or false")
	}
	cfg := synthesisLiveConfig(t)
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatal("production model runtime configuration failed (details withheld)")
	}
	defer runtime.Close()
	contract, ok := runtime.Chat().Contract()
	if !ok {
		t.Fatal("production Chat capability is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	provider := &synthesisLiveChat{delegate: runtime.Chat().Model(), cancel: cancel}
	defer synthesisLiveLogCalls(t, provider)
	artifact := synthesisLiveArtifact{HumanReview: "REQUIRED: model SUPPORTED is not semantic-quality proof", CallTimeout: cfg.ChatTimeout.String(), MaxOutputTokens: synthesisLiveTokens}
	// O_EXCL 拒绝覆盖已有文件和符号链接；在任何付费调用前创建文件。
	if path := os.Getenv("ZHIXU_EINO_LIVE_SYNTHESIS_ARTIFACT"); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("cannot create exclusive 0600 review artifact")
		}
		defer func() {
			artifact.Calls = provider.count
			artifact.Responses = provider.responses
			artifact.CallDiagnostics = provider.diagnostics
			if err := json.NewEncoder(file).Encode(artifact); err != nil {
				t.Error("cannot write review artifact")
			}
			if err := file.Close(); err != nil {
				t.Error("cannot close review artifact")
			}
		}()
	}
	for _, name := range []string{"redis_rejects_mysql", "mixed_interview", "duplicate", "different_conditions", "opposing_unknown"} {
		passed := t.Run(name, func(t *testing.T) {
			input, generate, validate := synthesisLiveInput(t, name)
			catalog := agentapp.NewRuntimeCatalog()
			if err := RegisterSynthesisRuntimeCatalog(catalog); err != nil {
				t.Fatal(err)
			}
			profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "synthesis-live", Version: "v1"}, Model: contract.Model, Timeout: cfg.ChatTimeout, MaxOutputTokens: synthesisLiveTokens}
			if err := catalog.RegisterProfile(profile); err != nil {
				t.Fatal(err)
			}
			if err := catalog.Freeze(); err != nil {
				t.Fatal(err)
			}
			scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
			if err != nil {
				t.Fatal("Eino scheduler construction failed")
			}
			store := &synthesisMemoryStore{runs: make(map[foundation.ID]agentdomain.ModelRun), calls: make(map[foundation.ID][]agentdomain.ModelCall), steps: make(map[foundation.ID]app.SynthesisModelStepRecord)}
			budget := agentapp.DefaultRunBudget()
			budget.Timeout = 2 * time.Minute
			model, err := NewSynthesisModel(SynthesisModelDependencies{Model: provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: store, Store: store, ProfileRef: profile.Ref, IDs: &synthesisTestIDs{}, Clock: foundation.FixedClock{Value: synthesisFixtureTime}, Budget: budget})
			if err != nil {
				t.Fatal("synthesis model construction failed")
			}
			row := synthesisLiveCase{Name: name, Input: synthesisInput(input)}
			defer func() {
				if t.Failed() && row.ErrorStage == "" {
					row.ErrorStage = "quality_assertion"
					row.Error = &synthesisLiveFailureDiagnostic{Code: "LIVE_SYNTHESIS_QUALITY_ASSERTION_FAILED"}
				}
				artifact.Cases = append(artifact.Cases, row)
			}()
			result, err := model.GenerateSynthesisForExecution(ctx, generate, input)
			row.Generation = result
			if err != nil {
				row.ErrorStage = "generation"
				row.Error = synthesisLiveFailure(err)
				t.Fatalf("real generation failed: %s", row.Error)
			}
			receipt, err := model.ValidateSynthesisSemanticsForExecution(ctx, validate, input, result)
			row.Semantic = receipt
			if err != nil || !receipt.Accepted || receipt.ModelRunID == result.ModelRunID {
				row.ErrorStage = "semantic"
				row.Error = synthesisLiveFailure(err)
				t.Fatalf("independent semantic review failed: %s", row.Error)
			}
			if len(store.runs) != 2 {
				t.Fatal("expected distinct generation and semantic model journals")
			}
			refs := make([]domain.SynthesisSourceRef, len(input.Sources))
			for i, source := range input.Sources {
				refs[i] = source.Reference
			}
			if name == "mixed_interview" && len(result.Notes) != 1 {
				t.Fatal("expected one database main note")
			}
			if name != "mixed_interview" && len(result.Notes) > 1 {
				t.Fatal("scope expanded into additional notes")
			}
			for _, generated := range result.Notes {
				var current []domain.SynthesisItem
				title, noteID := generated.Title, synthesisTestID(900)
				if len(input.Notes) > 0 {
					if generated.NoteID != input.Notes[0].Note.ID {
						t.Fatal("generated outside frozen target")
					}
					current = input.Notes[0].Revision.Items
					title, noteID = input.Notes[0].Note.Title, input.Notes[0].Note.ID
				}
				applied, err := domain.ApplySynthesisDelta(generate.WorkspaceID, current, generated.Delta, refs)
				if err != nil {
					t.Fatal("source-bound delta could not be applied")
				}
				body, err := domain.RenderSynthesisMarkdown(generate.WorkspaceID, noteID, title, applied.Items)
				if err != nil {
					t.Fatal("generated body could not be rendered")
				}
				row.Bodies = append(row.Bodies, body)
				row.Items = append(row.Items, applied.Items...)
				if name == "redis_rejects_mysql" && (applied.Changed || applied.SourcesChanged || !reflect.DeepEqual(applied.Items, current)) {
					t.Fatal("out-of-scope source changed Redis baseline")
				}
				if name == "duplicate" && (applied.Changed || !applied.SourcesChanged) {
					t.Fatal("duplicate must add support without changing body semantics")
				}
			}
			if len(result.Notes) == 0 && len(input.Notes) == 1 {
				body, err := input.Notes[0].Revision.Content()
				if err != nil {
					t.Fatal("baseline rendering failed")
				}
				row.Bodies = append(row.Bodies, body)
				row.Items = append(row.Items, input.Notes[0].Revision.Items...)
			}
			if name != "redis_rejects_mysql" && len(result.Notes) == 0 {
				t.Fatal("expected meaningful source processing")
			}
			if name == "opposing_unknown" {
				conflict := false
				for _, item := range row.Items {
					if item.Conflict != nil && len(item.Conflict.Alternatives) >= 2 {
						seenOld, seenNew := false, false
						for _, alt := range item.Conflict.Alternatives {
							for _, ref := range alt.Sources {
								seenOld = seenOld || ref == refs[0]
								seenNew = seenNew || ref == refs[1]
							}
						}
						conflict = conflict || (seenOld && seenNew)
					}
				}
				if !conflict {
					t.Fatal("expected unresolved opposing alternatives with both sources")
				}
			}
			if name == "different_conditions" {
				conditioned := false
				for _, item := range row.Items {
					if item.Fact != nil && item.Fact.Applicability != "" {
						for _, ref := range item.Fact.Sources {
							conditioned = conditioned || ref == refs[1]
						}
					}
					if item.Conflict != nil {
						for _, alt := range item.Conflict.Alternatives {
							for _, ref := range alt.Sources {
								conditioned = conditioned || (ref == refs[1] && alt.Applicability != "")
							}
						}
					}
				}
				if !conditioned {
					t.Fatal("incoming applicable condition was dropped")
				}
			}
			t.Log("structure/source checks passed; body relevance, coverage and conditions still require human review")
		})
		if !passed {
			break
		} // 失败后不重试整个测试组，也不再进行付费调用。
	}
	t.Logf("real Chat calls: %d/12; human semantic review remains required", provider.count)
}

func synthesisLiveConfig(t *testing.T) config.Config {
	t.Helper()
	required := func(name string) string {
		value := os.Getenv(name)
		if value == "" || value != strings.TrimSpace(value) {
			t.Fatalf("%s must be set and canonical", name)
		}
		return value
	}
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = required("ZHIXU_EINO_LIVE_BASE_URL")
	cfg.ChatAPIKey = required("ZHIXU_EINO_LIVE_API_KEY")
	cfg.ChatModel = required("ZHIXU_EINO_LIVE_MODEL")
	cfg.ChatModelVersion = os.Getenv("ZHIXU_EINO_LIVE_MODEL_VERSION")
	if cfg.ChatModelVersion == "" {
		cfg.ChatModelVersion = cfg.ChatModel
	}
	cfg.ChatTimeout = 90 * time.Second
	if value, ok := os.LookupEnv("ZHIXU_EINO_LIVE_TIMEOUT"); ok {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > 90*time.Second {
			t.Fatal("ZHIXU_EINO_LIVE_TIMEOUT must be positive and <=90s")
		}
		cfg.ChatTimeout = timeout
	}
	return cfg
}

type synthesisLiveArtifact struct {
	CallTimeout     string
	MaxOutputTokens int
	HumanReview     string
	SourceReviews   []synthesisLiveSourceReviewCase
	Calls           int
	Cases           []synthesisLiveCase
	Responses       []string
	CallDiagnostics []synthesisLiveCallDiagnostic
}
type synthesisLiveCase struct {
	Name       string
	Input      synthesisProviderInput
	Generation app.SynthesisGenerationResult
	Semantic   app.SynthesisSemanticReceipt
	Items      []domain.SynthesisItem
	Bodies     []string
	ErrorStage string
	Error      *synthesisLiveFailureDiagnostic `json:",omitempty"`
}

type synthesisLiveCallDiagnostic struct {
	DurationMilliseconds int64
	Usage                agentdomain.TokenUsage
	Call                 int
	Error                *synthesisLiveFailureDiagnostic `json:",omitempty"`
}

// 不保留错误或 ConnectionDiagnostic 本身：二者都可能携带底层原因或提供方控制的字段，禁止写入此产物或日志。
type synthesisLiveFailureDiagnostic struct {
	Code               string
	Kind               foundation.ErrorKind
	Retryable          bool
	Stage              models.ConnectionStage            `json:",omitempty"`
	ProviderHTTPStatus int                               `json:",omitempty"`
	ValidationReason   models.ConnectionValidationReason `json:",omitempty"`
}

func synthesisLiveFailure(err error) *synthesisLiveFailureDiagnostic {
	if err == nil {
		return nil
	}
	result := &synthesisLiveFailureDiagnostic{Code: "LIVE_UNCLASSIFIED_ERROR"}
	var classified *foundation.Error
	if errors.As(err, &classified) && classified != nil {
		result.Code, result.Kind, result.Retryable = classified.Code, classified.Kind, classified.Retryable
	}
	var diagnostic *models.ConnectionDiagnostic
	if errors.As(err, &diagnostic) && diagnostic != nil {
		switch diagnostic.Stage {
		case models.ConnectionStageRequest, models.ConnectionStageDNS, models.ConnectionStageConnect,
			models.ConnectionStageTLS, models.ConnectionStageProviderResponse, models.ConnectionStageResponseRead,
			models.ConnectionStageResponseValidation, models.ConnectionStageCancelled, models.ConnectionStageTimeout:
			result.Stage = diagnostic.Stage
		}
		if diagnostic.ProviderHTTPStatus >= 100 && diagnostic.ProviderHTTPStatus <= 599 {
			result.ProviderHTTPStatus = diagnostic.ProviderHTTPStatus
		}
		if diagnostic.ValidationReason.IsKnown() {
			result.ValidationReason = diagnostic.ValidationReason
		}
	}
	return result
}

func (diagnostic *synthesisLiveFailureDiagnostic) String() string {
	if diagnostic == nil {
		return "no classified error; inspect the failed assertion"
	}
	return fmt.Sprintf("code=%s kind=%s retryable=%t stage=%s provider_status=%d validation_reason=%s",
		diagnostic.Code, diagnostic.Kind, diagnostic.Retryable, diagnostic.Stage, diagnostic.ProviderHTTPStatus, diagnostic.ValidationReason)
}

func synthesisLiveLogCalls(t *testing.T, chat *synthesisLiveChat) {
	t.Helper()
	for _, call := range chat.diagnostics {
		if call.Error != nil {
			t.Logf("Chat call %d failed: %s", call.Call, call.Error)
		}
	}
}

// 所有场景共用一个限制器，覆盖 INITIAL、REPAIR 和 REDUCED 调用。提供方只接收现有生产请求，不使用伪造响应。
type synthesisLiveChat struct {
	delegate    agentapp.ChatModel
	cancel      context.CancelFunc
	mu          sync.Mutex
	count       int
	maxCalls    int // 零值保留原先五个场景最多 12 次调用的限制。
	responses   []string
	diagnostics []synthesisLiveCallDiagnostic
}

func (chat *synthesisLiveChat) Chat(ctx context.Context, request agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	chat.mu.Lock()
	limit := chat.maxCalls
	if limit == 0 {
		limit = 12
	}
	if ctx.Err() != nil || chat.count >= limit || request.MaxOutputTokens <= 0 || request.MaxOutputTokens > synthesisLiveTokens {
		chat.mu.Unlock()
		chat.cancel()
		return agentapp.ChatResponse{}, errors.New("live synthesis hard budget exhausted")
	}
	chat.count++
	call := chat.count
	chat.mu.Unlock()
	started := time.Now()
	response, err := chat.delegate.Chat(ctx, request)
	elapsed := time.Since(started).Milliseconds()
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(response.Content) > 0 {
		chat.responses = append(chat.responses, string(response.Content))
	}
	if response.Usage.OutputTokens > synthesisLiveTokens {
		chat.cancel()
		response, err = agentapp.ChatResponse{}, errors.New("live synthesis provider exceeded output budget")
	}
	chat.diagnostics = append(chat.diagnostics, synthesisLiveCallDiagnostic{Call: call, DurationMilliseconds: elapsed, Usage: response.Usage, Error: synthesisLiveFailure(err)})
	return response, err
}

func synthesisLiveInput(t *testing.T, name string) (app.SynthesisGenerationInput, workflowapp.ExecutionContext, workflowapp.ExecutionContext) {
	t.Helper()
	input, generate, validate := synthesisFixtureInput(t)
	incoming := "MySQL InnoDB uses a clustered primary-key index."
	switch name {
	case "mixed_interview":
		incoming = "Synthetic interview notes. Database module: Redis cache entries expire after five minutes. MySQL InnoDB uses a clustered primary-key index. Oracle provides read consistency. Unrelated career module: I led the ORBIT hiring campaign and won a sailing competition."
	case "duplicate":
		incoming = "For stable Redis cache values, entries expire after five minutes."
	case "different_conditions":
		incoming = "For rapidly changing Redis cache values, entries expire after one minute."
	case "opposing_unknown":
		incoming = "Redis cache entries must expire after one minute, not five minutes. The source does not state applicability conditions."
	}
	source := synthesisFixtureSource(200, incoming)
	input.Sources = []app.SynthesisSourceExcerpt{source}
	input.SourceEvent.Source = source.Reference.Source
	if name == "mixed_interview" {
		prepared := app.SynthesisGoalPreparedInput{Progress: app.SynthesisGoalSelectionProgress{Request: app.SynthesisGoalRequest{ID: synthesisTestID(500), WorkspaceID: generate.WorkspaceID, Goal: "整理数据库专项面试知识，只提取 Redis、MySQL、Oracle 模块，保留面试语境，不纳入个人经历。", Status: app.SynthesisGoalCatalogReady, CatalogBatches: 1}, CatalogBatches: 1, PreparedBatches: 1, Selections: 1, Succeeded: 1}, Sources: []app.SynthesisGoalPreparedSource{{Excerpt: source, Points: []app.GoalSourcePointBinding{{SelectionID: synthesisTestID(510), ModelRunID: synthesisTestID(520), Locator: app.KnowledgePointLocator{ProfileRevisionID: synthesisTestID(530), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}, Reason: "数据库模块"}}}}}
		var err error
		input.Goal, err = app.BuildSynthesisGoalBinding(prepared)
		if err != nil {
			t.Fatal("synthetic goal fixture invalid")
		}
	} else {
		baseline, _, _ := synthesisFixtureWithNote(t)
		note := baseline.Notes[0]
		old := synthesisFixtureSource(100, "For stable Redis cache values, entries expire after five minutes.")
		note.Note.TopicKey, note.Note.Title, note.Note.Aliases = "redis cache", "Redis cache", []string{}
		note.Revision.Title = note.Note.Title
		item := domain.SynthesisItem{ID: synthesisTestID(31), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "Entries expire after five minutes.", Applicability: "Stable Redis cache values", Sources: []domain.SynthesisSourceRef{old.Reference}}}
		note.Revision.Items = []domain.SynthesisItem{item}
		note.Revision.Delta = domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &item}}}
		body, err := domain.RenderSynthesisMarkdown(note.Note.WorkspaceID, note.Note.ID, note.Note.Title, note.Revision.Items)
		if err != nil {
			t.Fatal("synthetic baseline rendering failed")
		}
		note.Revision.ContentHash = synthesisHash([]byte(body))
		note.Revision.Hash, err = domain.ComputeSynthesisRevisionHash(note.Revision)
		if err != nil {
			t.Fatal("synthetic baseline hash failed")
		}
		note.Anchor = &app.SynthesisAnchorBinding{AnchorID: synthesisTestID(89), ScopeVersion: 1, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"Interview"}, Description: "Redis cache expiration only. MySQL and Oracle are outside this scope. Do not expand scope."}, AllowedSources: []domain.SynthesisSourceRef{source.Reference}}
		input.Notes = []app.SynthesisGenerationNote{note}
		input.Sources = []app.SynthesisSourceExcerpt{old, source}
		// 验证仅面向已批准目标的融合路径；普通 source-ready 事件仍可合法创建其他主题的笔记。
		input.SourceEvent.Fusion = &domain.SynthesisFusionTrigger{
			RequestID: synthesisTestID(600), AnchorID: note.Anchor.AnchorID, NoteID: note.Note.ID,
			ProposalID: synthesisTestID(601), ScopeVersion: note.Anchor.ScopeVersion,
			AllowedSources: append([]domain.SynthesisSourceRef(nil), note.Anchor.AllowedSources...),
		}
	}
	input.GenerationPromptVersion, input.SemanticPromptVersion = app.LatestSynthesisPromptVersions(app.SynthesisOriginalPromptVersion(input), input.SourceEvent.Fusion != nil)
	if err := input.Validate(); err != nil {
		t.Fatalf("synthetic input invalid: %s", synthesisTestCode(err))
	}

	return input, generate, validate
}

// 离线检查只验证测试数据和付费调用限制，不证明内容质量。
func TestSynthesisLivePreflight(t *testing.T) {
	for _, name := range []string{"redis_rejects_mysql", "mixed_interview", "duplicate", "different_conditions", "opposing_unknown"} {
		t.Run(name, func(t *testing.T) {
			input, _, _ := synthesisLiveInput(t, name)
			if name == "mixed_interview" {
				if input.Goal == nil || input.SourceEvent.Fusion != nil {
					t.Fatal("main-note creation must use the goal path")
				}
			} else if input.SourceEvent.Fusion == nil || input.SourceEvent.Fusion.NoteID != input.Notes[0].Note.ID {
				t.Fatal("main-note update must bind the approved fusion target")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	steps := make([]agentapp.DeterministicChatStep, 12)
	for i := range steps {
		steps[i].Response = agentapp.ChatResponse{Content: []byte(`{"ok":true}`), Usage: agentdomain.TokenUsage{OutputTokens: 1}}
	}
	delegate := agentapp.NewDeterministicChatModel(steps...)
	chat := &synthesisLiveChat{delegate: delegate, cancel: cancel}
	request := agentapp.ChatRequest{MaxOutputTokens: synthesisLiveTokens}
	for range 12 {
		if _, err := chat.Chat(ctx, request); err != nil {
			t.Fatal("guard rejected a budgeted call")
		}
	}
	if _, err := chat.Chat(ctx, request); err == nil || delegate.CallCount() != 12 || ctx.Err() == nil {
		t.Fatal("hard call limit did not stop before provider")
	}
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	chat = &synthesisLiveChat{delegate: delegate, cancel: cancel2}
	request.MaxOutputTokens = synthesisLiveTokens + 1
	if _, err := chat.Chat(ctx2, request); err == nil || delegate.CallCount() != 12 || ctx2.Err() == nil {
		t.Fatal("oversized output budget reached provider")
	}
}

// 不启动 HTTP 服务，也不读取真实配置；让错误通过真实测试边界携带探针值，以同时检查产物编码和日志格式化。
func TestSynthesisLiveSafeDiagnostics(t *testing.T) {
	const canary = "sk-live-diagnostic-canary https://private.example/canary Authorization prompt-provider-raw-canary"
	providerDiagnostic := func(stage models.ConnectionStage, status int, reason models.ConnectionValidationReason) *models.ConnectionDiagnostic {
		return &models.ConnectionDiagnostic{
			Stage: stage, ProviderHTTPStatus: status, ValidationReason: reason,
			ProviderErrorCode: canary, ProviderErrorType: canary, ProviderMessage: canary,
			ProviderRequestID: canary, TransportError: canary,
		}
	}
	classified := func(kind foundation.ErrorKind, code string, retryable bool, diagnostic *models.ConnectionDiagnostic) error {
		return fmt.Errorf("%s: %w", canary, foundation.NewError(kind, code, retryable, errors.Join(diagnostic, errors.New(canary))))
	}
	for _, test := range []struct {
		name string
		err  error
		want *synthesisLiveFailureDiagnostic
	}{
		{
			name: "provider unavailable",
			err: classified(foundation.ErrorRetryableFailure, models.ErrorCodeChatProviderUnavailable, true,
				providerDiagnostic(models.ConnectionStageProviderResponse, http.StatusBadGateway, "")),
			want: &synthesisLiveFailureDiagnostic{Code: models.ErrorCodeChatProviderUnavailable, Kind: foundation.ErrorRetryableFailure,
				Retryable: true, Stage: models.ConnectionStageProviderResponse, ProviderHTTPStatus: http.StatusBadGateway},
		},
		{
			name: "output truncated",
			err: classified(foundation.ErrorConsistencyViolation, models.ErrorCodeChatResponseInvalid, false,
				providerDiagnostic(models.ConnectionStageResponseValidation, 0, models.ConnectionValidationFinishReasonLength)),
			want: &synthesisLiveFailureDiagnostic{Code: models.ErrorCodeChatResponseInvalid, Kind: foundation.ErrorConsistencyViolation,
				Stage: models.ConnectionStageResponseValidation, ValidationReason: models.ConnectionValidationFinishReasonLength},
		},
		{
			name: "unknown diagnostic values withheld",
			err: classified(foundation.ErrorNonRetryableFailure, models.ErrorCodeChatRequestFailed, false,
				providerDiagnostic(models.ConnectionStage(canary), 999, models.ConnectionValidationReason(canary))),
			want: &synthesisLiveFailureDiagnostic{Code: models.ErrorCodeChatRequestFailed, Kind: foundation.ErrorNonRetryableFailure},
		},
		{name: "unclassified raw error withheld", err: errors.New(canary), want: &synthesisLiveFailureDiagnostic{Code: "LIVE_UNCLASSIFIED_ERROR"}},
		{name: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			delegate := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Err: test.err})
			chat := &synthesisLiveChat{delegate: delegate, cancel: cancel}
			_, err := chat.Chat(ctx, agentapp.ChatRequest{MaxOutputTokens: synthesisLiveTokens})
			if err != test.err || delegate.CallCount() != 1 || chat.count != 1 || ctx.Err() != nil {
				t.Fatal("diagnostics changed the returned error, call count or cancellation")
			}
			if len(chat.diagnostics) != 1 || chat.diagnostics[0].Call != 1 || !reflect.DeepEqual(chat.diagnostics[0].Error, test.want) {
				t.Fatal("stable call classification was lost or unsafe diagnostic fields were accepted")
			}
			artifact := synthesisLiveArtifact{Calls: chat.count, Responses: chat.responses, CallDiagnostics: chat.diagnostics,
				SourceReviews: []synthesisLiveSourceReviewCase{{ErrorStage: "review", Error: synthesisLiveFailure(err)}}}
			encoded, marshalErr := json.Marshal(artifact)
			if marshalErr != nil {
				t.Fatal("cannot encode diagnostic artifact")
			}
			logLine := fmt.Sprintf("Chat call %d failed: %s", chat.diagnostics[0].Call, chat.diagnostics[0].Error)
			for _, forbidden := range []string{"sk-live-diagnostic-canary", "https://private.example/canary", "Authorization", "prompt-provider-raw-canary",
				"ProviderErrorCode", "ProviderErrorType", "ProviderMessage", "ProviderRequestID", "TransportError", "Cause"} {
				if strings.Contains(string(encoded), forbidden) || strings.Contains(logLine, forbidden) {
					t.Fatal("artifact or log exposed a forbidden error field")
				}
			}
		})
	}
}

// 本地模拟 HTTP 提供方检查生产适配器实际发送的预算参数；其输出仅证明接线正确，绝不用于真实模型场景。
func TestSynthesisLiveTransportBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			MaxTokens      int `json:"max_tokens"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || wire.MaxTokens != synthesisLiveTokens || wire.ResponseFormat.Type != "json_schema" {
			t.Error("production transport lost structured output or token cap")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"synthesis-local","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"notes\":[]}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = server.URL + "/v1"
	cfg.ChatAPIKey = "synthetic-local-key"
	cfg.ChatModel = "synthesis-local"
	cfg.ChatModelVersion = cfg.ChatModel
	cfg.ChatTimeout = time.Second
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	harness := newSynthesisHarness(t, synthesisFactOutput)
	input, generation, _ := synthesisFixtureInput(t)
	if _, err := harness.model.GenerateSynthesisForExecution(t.Context(), generation, input); err != nil {
		t.Fatal(err)
	}
	request := harness.provider.Calls()[0]
	contract, _ := runtime.Chat().Contract()
	request.Model = contract.Model
	request.MaxOutputTokens = synthesisLiveTokens
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	chat := &synthesisLiveChat{delegate: runtime.Chat().Model(), cancel: cancel}
	if _, err := chat.Chat(ctx, request); err != nil {
		t.Fatalf("local production transport check failed: %v", err)
	}
	if chat.count != 1 {
		t.Fatal("unexpected transport call count")
	}
}

const sourceReviewLiveTimeout = 5 * time.Minute

// 单独显式启用；这些输出仅来自所配置的真实模型。
func TestSynthesisLiveSourceReviewQuality(t *testing.T) {
	switch os.Getenv("ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED") {
	case "", "false":
		t.Skip("live current-text review disabled; set ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED=true")
	case "true":
	default:
		t.Fatal("ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED must be true or false")
	}
	cfg := synthesisLiveConfig(t)
	path := os.Getenv("ZHIXU_EINO_LIVE_SOURCE_REVIEW_ARTIFACT")
	if path == "" || path != strings.TrimSpace(path) {
		t.Fatal("ZHIXU_EINO_LIVE_SOURCE_REVIEW_ARTIFACT must be set and canonical")
	}
	// 必须在构建运行时或进行付费调用前创建产物文件。
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("cannot create exclusive 0600 source-review artifact")
	}
	artifact := synthesisLiveArtifact{HumanReview: "REQUIRED: synthetic owner REVIEWED is not business completion; inspect current full text, original evidence and raw checks", CallTimeout: cfg.ChatTimeout.String(), MaxOutputTokens: synthesisLiveTokens}
	ctx, cancel := context.WithTimeout(t.Context(), sourceReviewLiveTimeout)
	defer cancel()
	provider := &synthesisLiveChat{cancel: cancel, maxCalls: 4}
	defer synthesisLiveLogCalls(t, provider)
	defer func() {
		artifact.Calls, artifact.Responses = provider.count, provider.responses
		artifact.CallDiagnostics = provider.diagnostics
		if err := json.NewEncoder(file).Encode(artifact); err != nil {
			t.Error("cannot write source-review artifact")
		}
		if err := file.Close(); err != nil {
			t.Error("cannot close source-review artifact")
		}
	}()
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatal("production model runtime configuration failed (details withheld)")
	}
	defer runtime.Close()
	contract, ok := runtime.Chat().Contract()
	if !ok {
		t.Fatal("production Chat capability is unavailable")
	}
	provider.delegate = runtime.Chat().Model()
	for _, supported := range []bool{true, false} {
		name := "current_full_text_supported"
		if !supported {
			name = "old_local_claim_overridden_elsewhere"
		}
		if !t.Run(name, func(t *testing.T) {
			input := synthesisLiveSourceReviewInput(supported)
			row := synthesisLiveSourceReviewCase{Name: name, Input: input, ExpectedSupported: supported}
			defer func() { artifact.SourceReviews = append(artifact.SourceReviews, row) }()
			model, store, execution := synthesisLiveSourceReviewModel(t, ctx, provider, contract.Model, cfg.ChatTimeout, input)
			result, err := model.Review(ctx, execution, store.row.ID)
			row.Result = result
			if err != nil {
				row.ErrorStage = "review"
				row.Error = synthesisLiveFailure(err)
				t.Fatalf("real current-text review failed: %s", row.Error)
			}
			row.Bound, row.Accepted, err = app.BindSourceReviewOutput(result.Output, input.Snapshot)
			if err != nil || row.Accepted != supported {
				row.ErrorStage = "semantic_expectation"
				row.Error = synthesisLiveFailure(err)
				t.Fatalf("current full-text support expectation failed: %s; inspect artifact", row.Error)
			}
			for _, check := range row.Bound.Checks {
				if !supported && check.Verdict == "SUPPORTED" {
					row.ErrorStage = "old_claim_false_support"
					t.Fatal("old claim was incorrectly accepted as current support")
				}
				if supported && !slices.Contains(check.Targets, "N001P001") {
					row.ErrorStage = "wrong_supported_paragraph"
					t.Fatal("support must bind the actual current claim paragraph")
				}
			}
			if len(store.journal.calls[result.ModelRunID]) == 0 {
				t.Fatal("missing production RecordingChatModel journal")
			}
		}) {
			break // 首次失败后不做外层重试，也不继续后续场景。
		}
	}
	t.Logf("real Chat calls: %d/4; human review required; no PG, permission or publication acceptance", provider.count)
}

type synthesisLiveSourceReviewCase struct {
	Name              string
	Input             app.SynthesisSourceReviewInput
	ExpectedSupported bool
	Result            app.SynthesisManuscriptSourceReview
	Bound             app.SynthesisSourceReviewOutput
	Accepted          bool
	ErrorStage        string
	Error             *synthesisLiveFailureDiagnostic `json:",omitempty"`
}

func synthesisLiveSourceReviewInput(supported bool) app.SynthesisSourceReviewInput {
	old := "For stable Redis cache values, entries expire after five minutes."
	claim := "For rapidly changing Redis cache values, entries expire after one minute."
	contextParagraph := "This manuscript concerns rapidly changing Redis cache values only. The former five-minute rule for stable values is outside its current scope."
	evidence := claim + " " + contextParagraph
	if !supported {
		claim = old
		contextParagraph = "Correction applying to this entire manuscript, including the preceding paragraph: the five-minute rule is withdrawn and retained above only as a historical quotation. For all current cache values, expiration must be one minute; five minutes must not be used."
		evidence = old
	}
	full := claim + "\n\n" + contextParagraph
	source := synthesisFixtureSource(200, evidence)
	second := synthesisFixtureSource(300, evidence)
	parts := []app.SynthesisSourceReviewParagraph{
		{Label: "N001P001", Ordinal: 1, StartByte: 0, EndByte: len(claim), Hash: synthesisHash([]byte(claim))},
		{Label: "N001P002", Ordinal: 2, StartByte: len(claim) + 2, EndByte: len(full), Hash: synthesisHash([]byte(contextParagraph))},
	}
	return app.SynthesisSourceReviewInput{Sources: []app.SynthesisSourceExcerpt{source, second}, Snapshot: app.SynthesisSourceReviewSnapshot{
		Targets: []app.SynthesisSourceReviewTarget{{Label: "N001", NoteID: synthesisTestID(30), BaseRevisionID: synthesisTestID(32), TargetKind: "LOCAL_FILE", FullContent: full, FullContentHash: synthesisHash([]byte(full)), FileExists: true, FileHash: synthesisHash([]byte(full)), Paragraphs: parts,
			Anchor: &app.SynthesisAnchorBinding{AnchorID: synthesisTestID(89), ScopeVersion: 1, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"Operators"}, Description: "Redis cache expiration"}}}},
		Obligations: []app.SynthesisSourceReviewObligation{{Label: "O001", NoteLabel: "N001", SourceLabel: "S001", Source: source.Reference, HistoricalStatement: old, HistoricalApplicability: "Stable Redis cache values"}, {Label: "O002", NoteLabel: "N001", SourceLabel: "S002", Source: second.Reference, HistoricalStatement: old, HistoricalApplicability: "Stable Redis cache values"}},
	}}
}

// 仅模拟面向模型的所属模块方法；不应用证据，不声称完成 ModelRun 终态事务，REVIEWED 也不代表补源完成。
type synthesisLiveSourceReviewStore struct {
	row     app.SynthesisManuscriptSourceReview
	input   app.SynthesisSourceReviewInput
	journal *synthesisMemoryStore
}

func (s *synthesisLiveSourceReviewStore) GetSourceReview(context.Context, foundation.ID, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return s.row, nil
}
func (s *synthesisLiveSourceReviewStore) ReadSourceReviewInput(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisSourceReviewInput, error) {
	return s.input, nil
}
func (s *synthesisLiveSourceReviewStore) ClaimSourceReview(_ context.Context, e workflowapp.ExecutionContext, _ foundation.ID, model foundation.ID, hash string) (app.SynthesisManuscriptSourceReview, error) {
	s.row.Status, s.row.ModelRunID, s.row.RequestHash = "RUNNING", model, hash
	s.row.NodeRunID, s.row.NodeAttemptID = e.NodeRunID, e.NodeAttemptID
	return s.row, nil
}
func (s *synthesisLiveSourceReviewStore) CompleteSourceReview(_ context.Context, _ workflowapp.ExecutionContext, _ foundation.ID, output []byte) (app.SynthesisManuscriptSourceReview, error) {
	_, accepted, err := app.BindSourceReviewOutput(output, s.input.Snapshot)
	if err != nil {
		return s.row, err
	}
	s.row.Output = append(json.RawMessage(nil), output...)
	s.row.Status = "REJECTED"
	if accepted {
		s.row.Status = "REVIEWED"
	}
	return s.row, nil
}
func (s *synthesisLiveSourceReviewStore) FailSourceReview(context.Context, foundation.ID, foundation.ID, foundation.ID, error) error {
	s.row.Status = "FAILED"
	return nil
}
func (*synthesisLiveSourceReviewStore) PrepareSourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return app.SynthesisManuscriptSourceReview{}, errors.New("live fixture cannot prepare real source review")
}
func (*synthesisLiveSourceReviewStore) ApplySourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return app.SynthesisManuscriptSourceReview{}, errors.New("live fixture cannot apply evidence")
}

func synthesisLiveSourceReviewModel(t *testing.T, ctx context.Context, provider agentapp.ChatModel, modelRef agentdomain.ModelRef, timeout time.Duration, input app.SynthesisSourceReviewInput) (*SourceReviewModel, *synthesisLiveSourceReviewStore, workflowapp.ExecutionContext) {
	t.Helper()
	_, execution, _ := synthesisFixtureInput(t)
	execution.NodeKind = app.SynthesisSourceReviewModel
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterSourceReviewRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "source-review-live", Version: "v1"}, Model: modelRef, Timeout: timeout, MaxOutputTokens: synthesisLiveTokens}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal("Eino scheduler construction failed")
	}
	store := &synthesisLiveSourceReviewStore{input: input, journal: &synthesisMemoryStore{runs: make(map[foundation.ID]agentdomain.ModelRun), calls: make(map[foundation.ID][]agentdomain.ModelCall)}}
	store.row = app.SynthesisManuscriptSourceReview{ID: synthesisTestID(700), WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, Status: "PREPARED", Snapshot: &store.input.Snapshot}
	budget := agentapp.DefaultRunBudget()
	budget.Timeout = 2 * time.Minute
	model, err := NewSourceReviewModel(SourceReviewModelDependencies{Model: provider, ModelRuns: store.journal, Store: store, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: &synthesisTestIDs{}, Clock: foundation.FixedClock{Value: synthesisFixtureTime}, Budget: budget})
	if err != nil {
		t.Fatal("source-review model construction failed")
	}
	return model, store, execution
}

// 固定 Chat 输出仅用于离线接线验证，不能作为真实模型质量证据。
func TestSynthesisLiveSourceReviewPlumbing(t *testing.T) {
	for _, supported := range []bool{true, false} {
		input := synthesisLiveSourceReviewInput(supported)
		verdict, reason, targets := "SUPPORTED", "CURRENT_TEXT_SUPPORTED", []string{"N001P001"}
		if !supported {
			verdict, reason, targets = "UNSUPPORTED", "CURRENT_TEXT_CONTRADICTS", []string{}
		}
		raw, err := json.Marshal(app.SynthesisSourceReviewOutput{Checks: []app.SynthesisSourceReviewCheck{{Obligation: "O001", Source: "S001", Targets: targets, Verdict: verdict, ReasonCode: reason}, {Obligation: "O002", Source: "S002", Targets: targets, Verdict: verdict, ReasonCode: reason}}})
		if err != nil {
			t.Fatal(err)
		}
		delegate := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: synthesisFixtureModelRef, Content: raw, Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}})
		ctx, cancel := context.WithTimeout(t.Context(), sourceReviewLiveTimeout)
		chat := &synthesisLiveChat{delegate: delegate, cancel: cancel, maxCalls: 4}
		model, store, execution := synthesisLiveSourceReviewModel(t, ctx, chat, synthesisFixtureModelRef, time.Second, input)
		result, err := model.Review(ctx, execution, store.row.ID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		bound, accepted, err := app.BindSourceReviewOutput(result.Output, input.Snapshot)
		if err != nil || accepted != supported || len(bound.Checks) != 2 {
			t.Fatal("fixed result was not bound correctly")
		}
		for i, check := range bound.Checks {
			if check.Obligation != input.Snapshot.Obligations[i].Label || check.Source != input.Snapshot.Obligations[i].SourceLabel || check.Verdict != verdict || !reflect.DeepEqual(check.Targets, targets) {
				t.Fatal("obligation/source/paragraph binding mismatch")
			}
		}
		calls := delegate.Calls()
		if len(calls) != 1 || len(store.journal.calls[result.ModelRunID]) != 1 {
			t.Fatal("expected exactly one recorded production call, including refusal")
		}
		payload, err := app.BuildSourceReviewPayload(input)
		if err != nil {
			t.Fatal(err)
		}
		fullJSON, err := json.Marshal(input.Snapshot.Targets[0].FullContent)
		if err != nil {
			t.Fatal(err)
		}
		oldJSON, err := json.Marshal(input.Snapshot.Obligations[0].HistoricalStatement)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range calls[0].Messages {
			found = found || (strings.Contains(message.Content, string(payload)) &&
				strings.Contains(message.Content, `"full_content":`+string(fullJSON)) &&
				strings.Contains(message.Content, `"untrusted_historical_statement":`+string(oldJSON)))
		}
		if !found || string(fullJSON) == string(oldJSON) {
			t.Fatal("complete current text (including non-selected paragraph) or distinct history missing from actual Chat")
		}
		if calls[0].MaxOutputTokens != synthesisLiveTokens || calls[0].SchemaRef.ID != app.SynthesisSourceReviewSchema {
			t.Fatal("production source-review schema/token budget lost")
		}
	}
}

func TestSynthesisLiveSourceReviewBudget(t *testing.T) {
	for _, mode := range []string{"calls", "request_tokens", "reported_tokens", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), sourceReviewLiveTimeout)
			defer cancel()
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Minute {
				t.Fatal("missing five-minute deadline")
			}
			usage := int64(1)
			if mode == "reported_tokens" {
				usage = synthesisLiveTokens + 1
			}
			steps := make([]agentapp.DeterministicChatStep, 4)
			for i := range steps {
				steps[i].Response = agentapp.ChatResponse{Content: []byte(`{}`), Usage: agentdomain.TokenUsage{OutputTokens: usage}}
			}
			delegate := agentapp.NewDeterministicChatModel(steps...)
			chat := &synthesisLiveChat{delegate: delegate, cancel: cancel, maxCalls: 4}
			request := agentapp.ChatRequest{MaxOutputTokens: synthesisLiveTokens}
			wantCalls := 0
			switch mode {
			case "calls":
				for range 4 {
					if _, err := chat.Chat(ctx, request); err != nil {
						t.Fatal(err)
					}
				}
				wantCalls = 4
			case "request_tokens":
				request.MaxOutputTokens++
			case "reported_tokens":
				wantCalls = 1
			case "deadline":
				expired, done := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer done()
				ctx = expired
			}
			if _, err := chat.Chat(ctx, request); err == nil || delegate.CallCount() != wantCalls || ctx.Err() == nil {
				t.Fatal("source-review hard budget did not stop calls")
			}
		})
	}
}
