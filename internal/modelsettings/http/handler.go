// Package http 提供模型设置的严格 HTTP wire、Session 二次校验与脱敏错误映射。
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	nethttp "net/http"
	"reflect"
	"strings"
	"time"

	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/go-chi/chi/v5"
)

const (
	maxRequestBodyBytes         = 64 * 1024
	maxSecretBytes              = 16 * 1024
	defaultOperationTimeout     = 35 * time.Second
	errorCodeInvalidJSON        = "INVALID_JSON"
	errorCodeUnsupportedMedia   = "UNSUPPORTED_MEDIA_TYPE"
	errorCodeSessionRequired    = "MODEL_SETTINGS_SESSION_REQUIRED"
	errorCodeTestTimeout        = "MODEL_SETTINGS_TEST_TIMEOUT"
	errorCodeInternal           = "INTERNAL_ERROR"
	localDevelopmentActor       = "local-development"
	connectionTargetChat        = "chat"
	connectionTargetEmbedding   = "embedding"
	connectionTestSuccessStatus = "ok"
)

// Options 配置认证模式与单次 HTTP 操作上限。
type Options struct {
	RequireSession bool
	Timeout        time.Duration
}

// Handler 将模型设置应用接口映射到严格、无缓存的 HTTP 契约。
type Handler struct {
	manager        modelsettingsapplication.SettingsManager
	requireSession bool
	timeout        time.Duration
}

// NewHandler 创建 fail-closed 的模型设置 HTTP Handler。
func NewHandler(manager modelsettingsapplication.SettingsManager, options Options) (*Handler, error) {
	if nilDependency(manager) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, modelsettingsdomain.ErrorCodeUnavailable, true, errors.New("model settings HTTP dependency is unavailable"))
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultOperationTimeout
	}
	if timeout < time.Millisecond || timeout > 5*time.Minute {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, modelsettingsdomain.ErrorCodeInvalid, false, errors.New("model settings HTTP timeout is invalid"))
	}
	return &Handler{manager: manager, requireSession: options.RequireSession, timeout: timeout}, nil
}

// Routes 在调用方的 `/api/v1` Router 下注册模型设置端点。
func (handler *Handler) Routes(router chi.Router) {
	router.Get("/settings/models", handler.get)
	router.Put("/settings/models", handler.update)
	router.Post("/settings/models/test", handler.testConnection)
}

type updateRequest struct {
	ExpectedRevision json.RawMessage `json:"expected_revision"`
	Chat             json.RawMessage `json:"chat"`
	Embedding        json.RawMessage `json:"embedding"`
}

type testRequest struct {
	Target    json.RawMessage `json:"target"`
	Chat      json.RawMessage `json:"chat,omitempty"`
	Embedding json.RawMessage `json:"embedding,omitempty"`
}

type chatDraftRequest struct {
	Provider       json.RawMessage `json:"provider"`
	APIStyle       json.RawMessage `json:"api_style"`
	BaseURL        json.RawMessage `json:"base_url"`
	Model          json.RawMessage `json:"model"`
	ModelVersion   json.RawMessage `json:"model_version"`
	AdapterVersion json.RawMessage `json:"adapter_version"`
	APIKey         json.RawMessage `json:"api_key"`
}

type embeddingDraftRequest struct {
	Provider       json.RawMessage `json:"provider"`
	BaseURL        json.RawMessage `json:"base_url"`
	Model          json.RawMessage `json:"model"`
	Dimensions     json.RawMessage `json:"dimensions"`
	Normalization  json.RawMessage `json:"normalization"`
	DistanceMetric json.RawMessage `json:"distance_metric"`
	APIKey         json.RawMessage `json:"api_key"`
}

type secretActionRequest struct {
	Action json.RawMessage `json:"action"`
	Value  json.RawMessage `json:"value,omitempty"`
}

type settingsResponse struct {
	DesiredRevision int64                   `json:"desired_revision"`
	ActiveRevision  int64                   `json:"active_revision"`
	DesiredSettings settingsSummaryResponse `json:"desired_settings"`
	ActiveSettings  settingsSummaryResponse `json:"active_settings"`
	Runtime         runtimeResponse         `json:"runtime"`
	Rollout         rolloutResponse         `json:"rollout"`
	RestartRequired bool                    `json:"restart_required"`
	Capabilities    capabilitiesResponse    `json:"capabilities"`
}

type settingsSummaryResponse struct {
	Chat      chatSettingsResponse      `json:"chat"`
	Embedding embeddingSettingsResponse `json:"embedding"`
}

type chatSettingsResponse struct {
	Provider         modelsettingsdomain.ChatProvider `json:"provider"`
	APIStyle         modelsettingsdomain.ChatAPIStyle `json:"api_style"`
	BaseURL          string                           `json:"base_url"`
	Model            string                           `json:"model"`
	ModelVersion     string                           `json:"model_version"`
	AdapterVersion   string                           `json:"adapter_version"`
	APIKeyConfigured bool                             `json:"api_key_configured"`
}

type embeddingSettingsResponse struct {
	Provider         modelsettingsdomain.EmbeddingProvider      `json:"provider"`
	BaseURL          string                                     `json:"base_url"`
	Model            string                                     `json:"model"`
	Dimensions       int32                                      `json:"dimensions"`
	Normalization    modelsettingsdomain.EmbeddingNormalization `json:"normalization"`
	DistanceMetric   modelsettingsdomain.DistanceMetric         `json:"distance_metric"`
	APIKeyConfigured bool                                       `json:"api_key_configured"`
}

type runtimeResponse struct {
	API    runtimeRoleResponse `json:"api"`
	Worker runtimeRoleResponse `json:"worker"`
}

type runtimeRoleResponse struct {
	AppliedRevision int64                            `json:"applied_revision"`
	Phase           modelsettingsdomain.RuntimePhase `json:"phase"`
	Fresh           bool                             `json:"fresh"`
}

type rolloutResponse struct {
	Phase          modelsettingsdomain.RolloutPhase `json:"phase"`
	TargetRevision *int64                           `json:"target_revision"`
	LastErrorCode  *string                          `json:"last_error_code"`
	Retryable      bool                             `json:"retryable"`
}

type capabilitiesResponse struct {
	Chat      modelsettingsdomain.Capability `json:"chat"`
	Embedding modelsettingsdomain.Capability `json:"embedding"`
}

type testResponse struct {
	Target       string                           `json:"target"`
	Status       string                           `json:"status"`
	Provider     string                           `json:"provider"`
	Model        string                           `json:"model"`
	APIStyle     modelsettingsdomain.ChatAPIStyle `json:"api_style,omitempty"`
	EndpointPath string                           `json:"endpoint_path"`
	LatencyMS    int64                            `json:"latency_ms"`
}

func (handler *Handler) get(writer nethttp.ResponseWriter, request *nethttp.Request) {
	noStore(writer)
	if _, ok := handler.authorize(writer, request); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	snapshot, err := handler.manager.Snapshot(ctx)
	if err != nil {
		writeManagerError(writer, err, nil)
		return
	}
	httpapi.WriteJSON(writer, nethttp.StatusOK, toSettingsResponse(snapshot))
}

func (handler *Handler) update(writer nethttp.ResponseWriter, request *nethttp.Request) {
	noStore(writer)
	principal, ok := handler.authorize(writer, request)
	if !ok {
		return
	}
	input, ok := decodeRequest[updateRequest](writer, request)
	if !ok {
		return
	}
	defer destroyRaw(input.ExpectedRevision)
	defer destroyRaw(input.Chat)
	defer destroyRaw(input.Embedding)
	expectedRevision, err := decodeInt64(input.ExpectedRevision)
	if err != nil || expectedRevision < 0 {
		writeInvalid(writer)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	current, err := handler.manager.Snapshot(ctx)
	if err != nil {
		writeManagerError(writer, err, nil)
		return
	}
	if current.DesiredRevision != expectedRevision {
		writeRevisionConflict(writer, current.DesiredRevision)
		return
	}
	settings, chatSecret, embeddingSecret, err := decodeSettings(input.Chat, input.Embedding, current.DesiredSettings.Settings)
	defer chatSecret.Value.Destroy()
	defer embeddingSecret.Value.Destroy()
	if err != nil {
		writeInvalid(writer)
		return
	}
	createdBy := localDevelopmentActor
	if handler.requireSession {
		createdBy = fmt.Sprintf("%s:%s", principal.Kind, principal.ID)
	}
	snapshot, err := handler.manager.Save(ctx, modelsettingsapplication.SaveCommand{
		ExpectedRevision: expectedRevision,
		Settings:         settings,
		ChatSecret:       chatSecret,
		EmbeddingSecret:  embeddingSecret,
		CreatedBy:        createdBy,
	})
	if err != nil {
		handler.writeConflictAwareError(writer, request, err)
		return
	}
	httpapi.WriteJSON(writer, nethttp.StatusOK, toSettingsResponse(snapshot))
}

func (handler *Handler) testConnection(writer nethttp.ResponseWriter, request *nethttp.Request) {
	noStore(writer)
	if _, ok := handler.authorize(writer, request); !ok {
		return
	}
	input, ok := decodeRequest[testRequest](writer, request)
	if !ok {
		return
	}
	defer destroyRaw(input.Target)
	defer destroyRaw(input.Chat)
	defer destroyRaw(input.Embedding)
	target, err := decodeString(input.Target)
	if err != nil {
		writeInvalid(writer)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	current, err := handler.manager.Snapshot(ctx)
	if err != nil {
		writeManagerError(writer, err, nil)
		return
	}
	command, err := decodeDraftCommand(target, input, current)
	defer command.ChatSecret.Value.Destroy()
	defer command.EmbeddingSecret.Value.Destroy()
	if err != nil {
		writeInvalid(writer)
		return
	}
	testTarget := modelsettingsapplication.ConnectionTarget(target)
	result, err := handler.manager.Test(ctx, modelsettingsapplication.TestCommand{Target: testTarget, Draft: command})
	if err != nil {
		if isConflict(err) {
			handler.writeConflictAwareError(writer, request, err)
		} else {
			writeTestError(writer, target, err)
		}
		return
	}
	if result.Target != testTarget || result.Provider == "" || result.Model == "" || result.LatencyMS < 0 || !validTestResultProtocol(result) {
		writeManagerError(writer, foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeCorrupt, false, errors.New("model settings test result is inconsistent")), nil)
		return
	}
	if target != connectionTargetChat && target != connectionTargetEmbedding {
		writeInvalid(writer)
		return
	}
	response := testResponse{Target: target, Provider: result.Provider, Model: result.Model, Status: connectionTestSuccessStatus,
		APIStyle: result.APIStyle, EndpointPath: result.EndpointPath, LatencyMS: result.LatencyMS}
	httpapi.WriteJSON(writer, nethttp.StatusOK, response)
}

func validTestResultProtocol(result modelsettingsapplication.TestResult) bool {
	switch result.Target {
	case modelsettingsapplication.ConnectionTargetChat:
		return (result.APIStyle == modelsettingsdomain.ChatAPIStyleChatCompletions && result.EndpointPath == "/v1/chat/completions") ||
			(result.APIStyle == modelsettingsdomain.ChatAPIStyleResponses && result.EndpointPath == "/v1/responses")
	case modelsettingsapplication.ConnectionTargetEmbedding:
		return result.APIStyle == "" && result.EndpointPath == "/v1/embeddings"
	default:
		return false
	}
}

func (handler *Handler) authorize(writer nethttp.ResponseWriter, request *nethttp.Request) (authdomain.Principal, bool) {
	if !handler.requireSession {
		return authdomain.Principal{}, true
	}
	principal, ok := authhttp.PrincipalFromContext(request.Context())
	if !ok || principal.Kind != authdomain.PrincipalSession {
		httpapi.WriteProblem(writer, nethttp.StatusForbidden, errorCodeSessionRequired, "模型设置只允许浏览器 Session 管理", false, nil)
		return authdomain.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) writeConflictAwareError(writer nethttp.ResponseWriter, request *nethttp.Request, err error) {
	if !isConflict(err) {
		writeManagerError(writer, err, nil)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	current, snapshotErr := handler.manager.Snapshot(ctx)
	if snapshotErr != nil {
		writeManagerError(writer, snapshotErr, nil)
		return
	}
	writeManagerError(writer, err, map[string]any{"current_revision": current.DesiredRevision})
}

func decodeDraftCommand(target string, input testRequest, current modelsettingsdomain.Snapshot) (modelsettingsapplication.DraftCommand, error) {
	command := modelsettingsapplication.DraftCommand{
		ExpectedRevision: current.DesiredRevision,
		Settings:         current.DesiredSettings.Settings,
		ChatSecret:       modelsettingsdomain.KeepSecret(),
		EmbeddingSecret:  modelsettingsdomain.KeepSecret(),
	}
	switch target {
	case connectionTargetChat:
		if len(input.Chat) == 0 || len(input.Embedding) != 0 {
			return modelsettingsapplication.DraftCommand{}, errors.New("chat test union is invalid")
		}
		chat, secret, err := decodeChat(input.Chat, command.Settings.Chat)
		if err != nil || chat.Provider != modelsettingsdomain.ChatProviderOpenAICompatible {
			secret.Value.Destroy()
			return modelsettingsapplication.DraftCommand{}, errors.New("chat test draft is invalid")
		}
		command.Settings.Chat = chat
		command.ChatSecret = secret
	case connectionTargetEmbedding:
		if len(input.Embedding) == 0 || len(input.Chat) != 0 {
			return modelsettingsapplication.DraftCommand{}, errors.New("embedding test union is invalid")
		}
		embedding, secret, err := decodeEmbedding(input.Embedding, command.Settings.Embedding)
		if err != nil || embedding.Provider == modelsettingsdomain.EmbeddingProviderDisabled {
			secret.Value.Destroy()
			return modelsettingsapplication.DraftCommand{}, errors.New("embedding test draft is invalid")
		}
		command.Settings.Embedding = embedding
		command.EmbeddingSecret = secret
	default:
		return modelsettingsapplication.DraftCommand{}, errors.New("connection test target is invalid")
	}
	return command, nil
}

func decodeSettings(chatRaw, embeddingRaw json.RawMessage, base modelsettingsdomain.Settings) (modelsettingsdomain.Settings, modelsettingsdomain.SecretAction, modelsettingsdomain.SecretAction, error) {
	chat, chatSecret, err := decodeChat(chatRaw, base.Chat)
	if err != nil {
		return modelsettingsdomain.Settings{}, modelsettingsdomain.SecretAction{}, modelsettingsdomain.SecretAction{}, err
	}
	embedding, embeddingSecret, err := decodeEmbedding(embeddingRaw, base.Embedding)
	if err != nil {
		chatSecret.Value.Destroy()
		return modelsettingsdomain.Settings{}, modelsettingsdomain.SecretAction{}, modelsettingsdomain.SecretAction{}, err
	}
	base.Chat = chat
	base.Embedding = embedding
	return base, chatSecret, embeddingSecret, nil
}

func decodeChat(raw json.RawMessage, base modelsettingsdomain.ChatSettings) (modelsettingsdomain.ChatSettings, modelsettingsdomain.SecretAction, error) {
	request, err := decodeRawObject[chatDraftRequest](raw)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	defer destroyChatRequest(&request)
	provider, err := decodeString(request.Provider)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	apiStyle, err := decodeString(request.APIStyle)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	baseURL, err := decodeString(request.BaseURL)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	model, err := decodeString(request.Model)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	modelVersion, err := decodeString(request.ModelVersion)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	adapterVersion, err := decodeString(request.AdapterVersion)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	secret, err := decodeSecretAction(request.APIKey)
	if err != nil {
		return modelsettingsdomain.ChatSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	base.Provider = modelsettingsdomain.ChatProvider(provider)
	base.APIStyle = modelsettingsdomain.ChatAPIStyle(apiStyle)
	base.BaseURL = baseURL
	base.Model = model
	base.ModelVersion = modelVersion
	base.AdapterVersion = adapterVersion
	return base, secret, nil
}

func decodeEmbedding(raw json.RawMessage, base modelsettingsdomain.EmbeddingSettings) (modelsettingsdomain.EmbeddingSettings, modelsettingsdomain.SecretAction, error) {
	request, err := decodeRawObject[embeddingDraftRequest](raw)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	defer destroyEmbeddingRequest(&request)
	provider, err := decodeString(request.Provider)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	baseURL, err := decodeString(request.BaseURL)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	model, err := decodeString(request.Model)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	dimensions, err := decodeInt64(request.Dimensions)
	if err != nil || dimensions < 0 || dimensions > 16000 {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, errors.New("embedding dimensions are invalid")
	}
	normalization, err := decodeString(request.Normalization)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	distanceMetric, err := decodeString(request.DistanceMetric)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	secret, err := decodeSecretAction(request.APIKey)
	if err != nil {
		return modelsettingsdomain.EmbeddingSettings{}, modelsettingsdomain.SecretAction{}, err
	}
	base.Provider = modelsettingsdomain.EmbeddingProvider(provider)
	base.BaseURL = baseURL
	base.Model = model
	base.Dimensions = int32(dimensions)
	base.Normalization = modelsettingsdomain.EmbeddingNormalization(normalization)
	base.DistanceMetric = modelsettingsdomain.DistanceMetric(distanceMetric)
	return base, secret, nil
}

func decodeSecretAction(raw json.RawMessage) (modelsettingsdomain.SecretAction, error) {
	request, err := decodeRawObject[secretActionRequest](raw)
	if err != nil {
		return modelsettingsdomain.SecretAction{}, err
	}
	defer destroyRaw(request.Action)
	defer destroyRaw(request.Value)
	action, err := decodeString(request.Action)
	if err != nil {
		return modelsettingsdomain.SecretAction{}, err
	}
	switch modelsettingsdomain.SecretActionKind(action) {
	case modelsettingsdomain.SecretActionKeep:
		if len(request.Value) != 0 {
			return modelsettingsdomain.SecretAction{}, errors.New("keep secret action contains a value")
		}
		return modelsettingsdomain.KeepSecret(), nil
	case modelsettingsdomain.SecretActionClear:
		if len(request.Value) != 0 {
			return modelsettingsdomain.SecretAction{}, errors.New("clear secret action contains a value")
		}
		return modelsettingsdomain.ClearSecret(), nil
	case modelsettingsdomain.SecretActionReplace:
		value, err := decodeString(request.Value)
		if err != nil || len(value) > maxSecretBytes {
			return modelsettingsdomain.SecretAction{}, errors.New("replace secret action value is invalid")
		}
		secret, err := modelsettingsdomain.NewSecret(value)
		value = ""
		if err != nil {
			return modelsettingsdomain.SecretAction{}, err
		}
		return modelsettingsdomain.SecretAction{Kind: modelsettingsdomain.SecretActionReplace, Value: secret}, nil
	default:
		return modelsettingsdomain.SecretAction{}, errors.New("secret action is invalid")
	}
}

func decodeRequest[T any](writer nethttp.ResponseWriter, request *nethttp.Request) (T, bool) {
	var zero T
	if !isJSONRequest(request) {
		httpapi.WriteProblem(writer, nethttp.StatusUnsupportedMediaType, errorCodeUnsupportedMedia, "请求必须使用 application/json", false, nil)
		return zero, false
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxRequestBodyBytes {
		destroyRaw(body)
		writeInvalidJSON(writer)
		return zero, false
	}
	defer destroyRaw(body)
	limits := strictJSONLimits()
	decoded, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		writeInvalidJSON(writer)
		return zero, false
	}
	return decoded, true
}

func decodeRawObject[T any](raw json.RawMessage) (T, error) {
	var zero T
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return zero, errors.New("required JSON object is missing")
	}
	return strictjson.DecodeObject[T](raw, strictJSONLimits(), nil)
}

func strictJSONLimits() strictjson.Limits {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxRequestBodyBytes
	limits.MaxDepth = 8
	limits.MaxArrayItems = 8
	limits.MaxObjectFields = 32
	return limits
}

func isJSONRequest(request *nethttp.Request) bool {
	values := request.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func decodeString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("required JSON string is missing")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("JSON field is not a string")
	}
	return value, nil
}

func decodeInt64(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, errors.New("required JSON integer is missing")
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errors.New("JSON field is not an integer")
	}
	return value, nil
}

func toSettingsResponse(snapshot modelsettingsdomain.Snapshot) settingsResponse {
	return settingsResponse{
		DesiredRevision: snapshot.DesiredRevision,
		ActiveRevision:  snapshot.ActiveRevision,
		DesiredSettings: toSettingsSummaryResponse(snapshot.DesiredSettings),
		ActiveSettings:  toSettingsSummaryResponse(snapshot.ActiveSettings),
		Runtime: runtimeResponse{
			API:    toRuntimeRoleResponse(snapshot.Runtime.API),
			Worker: toRuntimeRoleResponse(snapshot.Runtime.Worker),
		},
		Rollout:         toRolloutResponse(snapshot.Rollout),
		RestartRequired: snapshot.RestartRequired,
		Capabilities: capabilitiesResponse{
			Chat:      snapshot.ChatCapability,
			Embedding: snapshot.EmbeddingCapability,
		},
	}
}

func toSettingsSummaryResponse(summary modelsettingsdomain.SettingsSummary) settingsSummaryResponse {
	return settingsSummaryResponse{
		Chat: chatSettingsResponse{
			Provider: summary.Settings.Chat.Provider, APIStyle: summary.Settings.Chat.APIStyle, BaseURL: summary.Settings.Chat.BaseURL,
			Model: summary.Settings.Chat.Model, ModelVersion: summary.Settings.Chat.ModelVersion,
			AdapterVersion: summary.Settings.Chat.AdapterVersion, APIKeyConfigured: summary.Secrets.ChatConfigured,
		},
		Embedding: embeddingSettingsResponse{
			Provider: summary.Settings.Embedding.Provider, BaseURL: summary.Settings.Embedding.BaseURL,
			Model: summary.Settings.Embedding.Model, Dimensions: summary.Settings.Embedding.Dimensions,
			Normalization: summary.Settings.Embedding.Normalization, DistanceMetric: summary.Settings.Embedding.DistanceMetric,
			APIKeyConfigured: summary.Secrets.EmbeddingConfigured,
		},
	}
}

func toRuntimeRoleResponse(summary modelsettingsdomain.RuntimeSummary) runtimeRoleResponse {
	return runtimeRoleResponse{AppliedRevision: summary.AppliedRevision, Phase: summary.Phase, Fresh: summary.Fresh}
}

func toRolloutResponse(state modelsettingsdomain.RolloutState) rolloutResponse {
	response := rolloutResponse{Phase: state.Phase, Retryable: state.Phase == modelsettingsdomain.RolloutPhaseFailed}
	if state.Phase != modelsettingsdomain.RolloutPhaseIdle {
		target := state.TargetRevision
		response.TargetRevision = &target
	}
	if state.LastErrorCode != "" {
		code := state.LastErrorCode
		response.LastErrorCode = &code
	}
	return response
}

func isConflict(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorVersionConflict
}

func writeRevisionConflict(writer nethttp.ResponseWriter, revision int64) {
	writeManagerError(writer, foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeRevisionConflict, false, errors.New("desired revision changed")), map[string]any{"current_revision": revision})
}

func writeInvalid(writer nethttp.ResponseWriter) {
	writeManagerError(writer, foundation.NewError(foundation.ErrorInvalidInput, modelsettingsdomain.ErrorCodeInvalid, false, errors.New("model settings request is invalid")), nil)
}

func writeInvalidJSON(writer nethttp.ResponseWriter) {
	httpapi.WriteProblem(writer, nethttp.StatusBadRequest, errorCodeInvalidJSON, "请求 JSON 无效", false, nil)
}

func writeManagerError(writer nethttp.ResponseWriter, err error, details map[string]any) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(writer, nethttp.StatusServiceUnavailable, modelsettingsdomain.ErrorCodeUnavailable, "模型设置服务暂不可用", errors.Is(err, context.DeadlineExceeded), nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, nethttp.StatusInternalServerError, errorCodeInternal, "模型设置请求失败", false, nil)
		return
	}
	status := nethttp.StatusInternalServerError
	message := "模型设置请求失败"
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		status, message = nethttp.StatusBadRequest, "模型设置请求无效"
	case foundation.ErrorVersionConflict:
		status, message = nethttp.StatusConflict, "模型设置已发生变化"
	case foundation.ErrorPermissionDenied:
		status, message = nethttp.StatusForbidden, "无权管理模型设置"
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		status, message = nethttp.StatusServiceUnavailable, "模型设置服务暂不可用"
	case foundation.ErrorNotFound:
		if classified.Code == modelsettingsdomain.ErrorCodeActiveRevisionUnavailable {
			status, message = nethttp.StatusServiceUnavailable, "模型设置服务暂不可用"
		}
	case foundation.ErrorConsistencyViolation:
		if classified.Code == modelsettingsdomain.ErrorCodeCorrupt || classified.Code == modelsettingsdomain.ErrorCodeUnavailable {
			status, message = nethttp.StatusServiceUnavailable, "模型设置服务暂不可用"
		}
	}
	if status != nethttp.StatusConflict {
		details = nil
	}
	httpapi.WriteProblem(writer, status, classified.Code, message, classified.Retryable, details)
}

func writeTestError(writer nethttp.ResponseWriter, target string, err error) {
	var classified *foundation.Error
	classifiedFound := errors.As(err, &classified)
	details, diagnosticMessage := testDiagnosticDetails(target, err)
	if errors.Is(err, context.DeadlineExceeded) || classifiedFound && strings.HasSuffix(classified.Code, "_TIMEOUT") {
		code, retryable := errorCodeTestTimeout, true
		if classifiedFound {
			code, retryable = classified.Code, classified.Retryable
		}
		httpapi.WriteProblem(writer, nethttp.StatusGatewayTimeout, code, "模型连接测试超时", retryable, details)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(writer, nethttp.StatusServiceUnavailable, modelsettingsdomain.ErrorCodeUnavailable, "模型连接测试未完成", false, details)
		return
	}
	if !classifiedFound {
		httpapi.WriteProblem(writer, nethttp.StatusInternalServerError, errorCodeInternal, "模型连接测试失败", false, nil)
		return
	}
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		httpapi.WriteProblem(writer, nethttp.StatusBadRequest, classified.Code, "模型连接测试请求无效", false, nil)
	case foundation.ErrorVersionConflict:
		httpapi.WriteProblem(writer, nethttp.StatusConflict, classified.Code, "模型设置已发生变化", classified.Retryable, nil)
	case foundation.ErrorDependencyUnavailable:
		httpapi.WriteProblem(writer, nethttp.StatusServiceUnavailable, classified.Code, "模型连接测试服务暂不可用", classified.Retryable, nil)
	case foundation.ErrorRetryableFailure, foundation.ErrorNonRetryableFailure, foundation.ErrorConsistencyViolation:
		if classified.Code == modelsettingsdomain.ErrorCodeUnavailable || classified.Code == modelsettingsdomain.ErrorCodeCorrupt {
			httpapi.WriteProblem(writer, nethttp.StatusServiceUnavailable, classified.Code, "模型连接测试服务暂不可用", classified.Retryable, nil)
			return
		}
		message := "模型 Provider 拒绝请求或返回无效响应"
		if diagnosticMessage != "" {
			message = diagnosticMessage
		}
		httpapi.WriteProblem(writer, nethttp.StatusBadGateway, classified.Code, message, classified.Retryable, details)
	default:
		httpapi.WriteProblem(writer, nethttp.StatusInternalServerError, errorCodeInternal, "模型连接测试失败", false, nil)
	}
}

func testDiagnosticDetails(target string, err error) (map[string]any, string) {
	if target != connectionTargetChat && target != connectionTargetEmbedding {
		return nil, ""
	}
	var diagnostic *platformmodels.ConnectionDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic == nil {
		return nil, ""
	}
	var message string
	switch diagnostic.Stage {
	case platformmodels.ConnectionStageProviderResponse:
		message = "模型 Provider 返回错误"
	case platformmodels.ConnectionStageResponseRead:
		message = "模型 Provider 响应读取失败"
	case platformmodels.ConnectionStageResponseValidation:
		message = "模型 Provider 响应校验失败"
	case platformmodels.ConnectionStageRequest, platformmodels.ConnectionStageDNS, platformmodels.ConnectionStageConnect,
		platformmodels.ConnectionStageTLS, platformmodels.ConnectionStageCancelled, platformmodels.ConnectionStageTimeout:
		message = "模型 Provider 连接失败"
	default:
		return nil, ""
	}
	details := map[string]any{"target": target, "stage": diagnostic.Stage}
	if diagnostic.Stage == platformmodels.ConnectionStageProviderResponse || diagnostic.Stage == platformmodels.ConnectionStageResponseRead {
		if diagnostic.ProviderHTTPStatus >= 100 && diagnostic.ProviderHTTPStatus <= 599 {
			details["provider_http_status"] = diagnostic.ProviderHTTPStatus
		}
		if diagnostic.ProviderErrorCode != "" {
			details["provider_error_code"] = diagnostic.ProviderErrorCode
		}
		if diagnostic.ProviderErrorType != "" {
			details["provider_error_type"] = diagnostic.ProviderErrorType
		}
		if diagnostic.ProviderMessage != "" {
			details["provider_message"] = diagnostic.ProviderMessage
		}
		if diagnostic.ProviderRequestID != "" {
			details["provider_request_id"] = diagnostic.ProviderRequestID
		}
	}
	if diagnostic.TransportError != "" && diagnostic.Stage != platformmodels.ConnectionStageProviderResponse && diagnostic.Stage != platformmodels.ConnectionStageResponseValidation {
		details["transport_error"] = diagnostic.TransportError
	}
	if diagnostic.ValidationReason.IsKnown() && diagnostic.Stage == platformmodels.ConnectionStageResponseValidation {
		details["validation_reason"] = diagnostic.ValidationReason
	}
	return details, message
}

func noStore(writer nethttp.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
}

func destroyRaw(raw []byte) {
	clear(raw)
}

func destroyChatRequest(request *chatDraftRequest) {
	if request == nil {
		return
	}
	destroyRaw(request.Provider)
	destroyRaw(request.APIStyle)
	destroyRaw(request.BaseURL)
	destroyRaw(request.Model)
	destroyRaw(request.ModelVersion)
	destroyRaw(request.AdapterVersion)
	destroyRaw(request.APIKey)
}

func destroyEmbeddingRequest(request *embeddingDraftRequest) {
	if request == nil {
		return
	}
	destroyRaw(request.Provider)
	destroyRaw(request.BaseURL)
	destroyRaw(request.Model)
	destroyRaw(request.Dimensions)
	destroyRaw(request.Normalization)
	destroyRaw(request.DistanceMetric)
	destroyRaw(request.APIKey)
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
