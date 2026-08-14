package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	errorCodeQueryPlannerMissing       = "AGENT_QUERY_PLANNER_MISSING"
	errorCodeQueryPlanRequestInvalid   = "AGENT_QUERY_PLAN_REQUEST_INVALID"
	errorCodeQueryPlanResponseMismatch = "AGENT_QUERY_PLAN_RESPONSE_MISMATCH"
	queryPlanMaxOutputTokens           = 256
)

// QueryPlanRequest 冻结一次 PLAN 调用的模型运行身份、运行时版本和有界未信任输入。
type QueryPlanRequest struct {
	ModelRunRef foundation.ID
	ProfileRef  domain.ModelProfileRef
	PromptRef   domain.PromptRef
	SchemaRef   domain.SchemaRef
	Input       []byte
}

// QueryPlanRunResult 返回严格 Query Plan、精确使用量和实际运行时版本。
type QueryPlanRunResult struct {
	Plan          domain.RAGQueryPlanResult
	Usage         domain.TokenUsage
	RequestBytes  int64
	ResponseBytes int64
	Runtime       FrozenRuntimeRefs
}

// QueryPlanner 使用版本化 Catalog 和 ChatModel 执行单次严格 PLAN 调用。
type QueryPlanner struct {
	model   ChatModel
	catalog *RuntimeCatalog
}

// NewQueryPlanner 创建不执行 repair、retry、reduced 或 Tool Loop 的 Query Planner。
func NewQueryPlanner(model ChatModel, catalog *RuntimeCatalog) (*QueryPlanner, error) {
	if isNilChatModel(model) || catalog == nil {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeQueryPlannerMissing, false, errors.New("query planner model and catalog are required"))
	}
	return &QueryPlanner{model: model, catalog: catalog}, nil
}

// Plan 使用 phase=PLAN 调用 Provider 恰好一次，并原样接受严格 Decoder 通过的文档。
func (planner *QueryPlanner) Plan(ctx context.Context, request QueryPlanRequest) (QueryPlanRunResult, error) {
	if planner == nil || isNilChatModel(planner.model) || planner.catalog == nil {
		return QueryPlanRunResult{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeQueryPlannerMissing, false, errors.New("query planner is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateQueryPlanRequest(request); err != nil {
		return QueryPlanRunResult{}, err
	}
	boundInput := append([]byte(nil), request.Input...)
	sourceQuery := ""
	var err error
	if request.SchemaRef.Version == domain.OutputSchemaVersionV1 {
		boundInput, err = bindQueryPlanModelRunRef(request.Input, request.ModelRunRef)
	} else {
		sourceQuery, err = validateIdentitylessQueryPlanInput(request.Input)
	}
	if err != nil {
		return QueryPlanRunResult{}, err
	}
	snapshot, err := planner.catalog.Snapshot(request.PromptRef, request.SchemaRef, request.SchemaRef, request.ProfileRef)
	if err != nil {
		return QueryPlanRunResult{}, err
	}
	chatRequest := buildChatRequest(snapshot, snapshot.Schema, domain.ModelCallPlan, snapshot.Prompt.InitialInstruction, boundInput, "")
	if chatRequest.MaxOutputTokens > queryPlanMaxOutputTokens {
		chatRequest.MaxOutputTokens = queryPlanMaxOutputTokens
	}
	requestBytes, err := encodedChatRequestBytes(chatRequest)
	if err != nil {
		return QueryPlanRunResult{}, err
	}

	planCtx, cancel := context.WithTimeout(ctx, snapshot.Profile.Timeout)
	response, callErr := planner.model.Chat(planCtx, cloneChatRequest(chatRequest))
	contextErr := planCtx.Err()
	cancel()
	if callErr != nil {
		if contextErr != nil {
			return QueryPlanRunResult{}, operationContextError(contextErr)
		}
		return QueryPlanRunResult{}, callErr
	}
	if err := ValidateChatResponse(chatRequest, response); err != nil {
		return QueryPlanRunResult{}, err
	}
	decoded, err := snapshot.Schema.Decode(append([]byte(nil), response.Content...))
	if err != nil {
		return QueryPlanRunResult{}, err
	}
	if !bytes.Equal(decoded, response.Content) {
		return QueryPlanRunResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeDecoderContract, false, errors.New("query plan decoder transformed the accepted document"))
	}
	var plan domain.RAGQueryPlanResult
	if request.SchemaRef.Version == domain.OutputSchemaVersionV2 {
		providerPlan, decodeErr := domain.DecodeRAGQueryPlanProviderV2(decoded, domain.DefaultDecodeLimits())
		if decodeErr != nil {
			return QueryPlanRunResult{}, decodeErr
		}
		plan, err = providerPlan.Compose(request.ModelRunRef, sourceQuery)
	} else {
		plan, err = domain.DecodeRAGQueryPlan(decoded, domain.DefaultDecodeLimits())
		if err == nil && plan.ModelRunRef != request.ModelRunRef {
			return QueryPlanRunResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeQueryPlanResponseMismatch, false, errors.New("query plan model run reference differs from the frozen request"))
		}
	}
	if err != nil {
		return QueryPlanRunResult{}, err
	}
	return QueryPlanRunResult{
		Plan: plan, Usage: response.Usage, RequestBytes: requestBytes, ResponseBytes: int64(len(response.Content)),
		Runtime: FrozenRuntimeRefs{Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref, Schema: snapshot.Schema.Ref, Model: snapshot.Profile.Model},
	}, nil
}

// validateIdentitylessQueryPlanInput rejects server-owned identity before the
// v2 provider call; the trusted ModelRun reference is injected only on Compose.
func validateIdentitylessQueryPlanInput(input []byte) (string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(input, &document); err != nil || document == nil {
		return "", applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("query plan input must be a JSON object"))
	}
	if _, exists := document["model_run_ref"]; exists {
		return "", applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("query plan input reserves model_run_ref for the server"))
	}
	var question string
	if raw, exists := document["question"]; !exists || json.Unmarshal(raw, &question) != nil || domain.ValidateRAGQueryPlanSourceQuery(question) != nil {
		return "", applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("query plan input requires a bounded question"))
	}
	return question, nil
}

// bindQueryPlanModelRunRef 把服务端分配的 Model Run 身份加入仅存在内存的 PLAN 输入，供 Provider 精确回显绑定。
func bindQueryPlanModelRunRef(input []byte, modelRunRef foundation.ID) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(input, &document); err != nil || document == nil {
		return nil, applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("query plan input must be a JSON object"))
	}
	if _, exists := document["model_run_ref"]; exists {
		return nil, applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("query plan input reserves model_run_ref for the server"))
	}
	encodedRef, err := json.Marshal(modelRunRef)
	if err != nil {
		return nil, applicationError(foundation.ErrorNonRetryableFailure, errorCodeQueryPlanRequestInvalid, false, err)
	}
	document["model_run_ref"] = encodedRef
	bound, err := json.Marshal(document)
	if err != nil || len(bound) > MaxStructuredInputBytes {
		return nil, applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("bound query plan input exceeds the structured input budget"))
	}
	return bound, nil
}

func validateQueryPlanRequest(request QueryPlanRequest) error {
	if !canonicalApplicationID(request.ModelRunRef) || request.ProfileRef.Validate() != nil || request.PromptRef.Validate() != nil ||
		request.SchemaRef.Validate() != nil || request.SchemaRef.ID != domain.RAGQueryPlanSchemaID ||
		(request.SchemaRef.Version != domain.OutputSchemaVersionV1 && request.SchemaRef.Version != domain.OutputSchemaVersionV2) || len(request.Input) == 0 ||
		len(request.Input) > MaxStructuredInputBytes || !utf8.Valid(request.Input) || bytes.IndexByte(request.Input, 0) >= 0 {
		return applicationError(foundation.ErrorInvalidInput, errorCodeQueryPlanRequestInvalid, false, errors.New("query plan references or bounded input are invalid"))
	}
	return nil
}
