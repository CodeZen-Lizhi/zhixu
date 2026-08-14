package eino

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// AnswerStreamRuntime 只执行 Agent 结束后的 tool-free Eino Stream。它不会
// 发布 Agent 中间消息；draft sink 失败后继续消费 Provider 流并返回最终正文。
type AnswerStreamRuntime struct {
	model    einomodel.ToolCallingChatModel
	observer runtimeMetricObserver
}

// NewAnswerStreamRuntime 创建正式的最终答案流适配器。
func NewAnswerStreamRuntime(model einomodel.ToolCallingChatModel) (*AnswerStreamRuntime, error) {
	return NewAnswerStreamRuntimeWithMetrics(model, nil)
}

// NewAnswerStreamRuntimeWithMetrics 创建带发布观察指标的最终答案流适配器。
func NewAnswerStreamRuntimeWithMetrics(model einomodel.ToolCallingChatModel, metrics observability.Metrics) (*AnswerStreamRuntime, error) {
	if nilEinoChatModel(model) {
		return nil, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino answer stream model is unavailable"))
	}
	return &AnswerStreamRuntime{model: model, observer: newRuntimeMetricObserver(metrics)}, nil
}

// Stream 唯一消费并关闭 Eino StreamReader；只有正文 frame 会进入项目 sink。
func (runtime *AnswerStreamRuntime) Stream(
	ctx context.Context,
	request agentapplication.AnswerStreamRequest,
	sink agentapplication.AnswerStreamSink,
) (result agentapplication.AnswerStreamResult, runErr error) {
	if runtime == nil || nilEinoChatModel(runtime.model) {
		return agentapplication.AnswerStreamResult{}, einoRuntimeError(foundation.ErrorDependencyUnavailable, agentRuntimeUnavailableCode, errors.New("eino answer stream runtime is unavailable"))
	}
	if err := agentapplication.ValidateAnswerStreamRequest(request); err != nil {
		return agentapplication.AnswerStreamResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := runtime.observer.start()
	var draftError error
	defer func() {
		runtime.observer.observeAnswerCompletion(ctx, startedAt, draftError, runErr)
	}()
	recordingModel, err := newRecordingRuntimeModel(runtime.model, runtimeCallBinding{
		model: request.Model, profile: request.Profile, prompt: request.Prompt, schema: request.Schema,
		maxInputTokens: request.MaxInputTokens, maxOutputTokens: request.MaxOutputTokens,
		phase: agentdomain.ModelCallAnswer, budget: request.Budget, recorder: request.Recorder,
	})
	if err != nil {
		return agentapplication.AnswerStreamResult{}, err
	}
	reader, err := recordingModel.Stream(ctx, projectAgentMessages(request.Messages), einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		return agentapplication.AnswerStreamResult{}, err
	}
	defer reader.Close()

	var content strings.Builder
	usage := agentdomain.TokenUsage{}
	draftDegraded := false
	sinkEnabled := !nilAnswerStreamSink(sink)
	firstTokenObserved := false
	for {
		chunk, receiveErr := reader.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			return agentapplication.AnswerStreamResult{}, mapEinoRuntimeError(receiveErr)
		}
		if chunk == nil || len(chunk.ToolCalls) != 0 || !utf8.ValidString(chunk.Content) || content.Len() > agentapplication.MaxAgentResultBytes-len(chunk.Content) {
			return agentapplication.AnswerStreamResult{}, einoRuntimeError(foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeAnswerStreamInvalid, errors.New("eino answer stream emitted an invalid bounded chunk"))
		}
		if chunk.Content != "" {
			if !firstTokenObserved {
				firstTokenObserved = true
				runtime.observer.observeAnswerFirstToken(ctx, startedAt)
			}
			content.WriteString(chunk.Content)
			if sinkEnabled {
				if appendErr := appendBoundedDraftContent(ctx, sink, chunk.Content); appendErr != nil {
					draftDegraded = true
					draftError = appendErr
					sinkEnabled = false
				}
			}
		}
		if current, known := runtimeMessageUsage(chunk); known {
			usage = current
		}
	}
	result = agentapplication.AnswerStreamResult{Content: content.String(), Usage: usage, DraftDegraded: draftDegraded}
	if strings.TrimSpace(result.Content) == "" || len(result.Content) > agentapplication.MaxAgentResultBytes ||
		!utf8.ValidString(result.Content) || result.Usage.Validate() != nil {
		return agentapplication.AnswerStreamResult{}, einoRuntimeError(foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeAnswerStreamInvalid, errors.New("eino answer stream returned an invalid result"))
	}
	return result, nil
}

func appendBoundedDraftContent(ctx context.Context, sink agentapplication.AnswerStreamSink, content string) error {
	for len(content) > 0 {
		chunkBytes := min(len(content), agentapplication.MaxAnswerStreamChunkBytes)
		for chunkBytes > 0 && !utf8.ValidString(content[:chunkBytes]) {
			chunkBytes--
		}
		if chunkBytes == 0 {
			return einoRuntimeError(foundation.ErrorConsistencyViolation, agentapplication.ErrorCodeAnswerStreamInvalid, errors.New("eino answer stream could not split a valid utf-8 chunk"))
		}
		if err := sink.Append(ctx, agentapplication.AnswerStreamChunk{Content: content[:chunkBytes]}); err != nil {
			return err
		}
		content = content[chunkBytes:]
	}
	return nil
}

func nilAnswerStreamSink(sink agentapplication.AnswerStreamSink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

var _ agentapplication.AnswerStreamRuntime = (*AnswerStreamRuntime)(nil)
