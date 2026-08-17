package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisCandidateStreamInvalid 表示候选合成 Provider
	// 流违反了单次、有界、无工具的 Application Port 合同。
	ErrorCodeWorkspaceAnalysisCandidateStreamInvalid = "AGENT_WORKSPACE_ANALYSIS_CANDIDATE_STREAM_INVALID"
)

// WorkspaceAnalysisCandidateStreamChunk 是未持久化的候选 JSON 流片段。它
// 只能由已验证的 Provider frame 产生，不能携带 Provider 元数据或工具调用。
type WorkspaceAnalysisCandidateStreamChunk struct {
	Content string
}

// WorkspaceAnalysisCandidateStreamSink 接收候选 JSON 的原始有界片段。调用方
// 可在此完成受限增量解析；Candidate、Draft 与模型调用结算仍由持有 Workflow
// UoW 的上层负责。
type WorkspaceAnalysisCandidateStreamSink interface {
	Append(context.Context, WorkspaceAnalysisCandidateStreamChunk) error
}

// WorkspaceAnalysisCandidateStreamRuntime 只执行一次无工具的 Provider Stream。
// ChatRequest 和 ChatResponse 保持 Application 自有，Eino/Provider 类型不能越过
// 此边界；持久化授权、预算和 Model Call 记录由调用方在该调用外独占。
type WorkspaceAnalysisCandidateStreamRuntime interface {
	Stream(context.Context, ChatRequest, WorkspaceAnalysisCandidateStreamSink) (ChatResponse, error)
}

// ValidateWorkspaceAnalysisCandidateStreamRequest 校验候选合成的固定调用形状。
func ValidateWorkspaceAnalysisCandidateStreamRequest(request ChatRequest, sink WorkspaceAnalysisCandidateStreamSink) error {
	if err := ValidateChatRequest(request); err != nil {
		return err
	}
	if request.Phase != domain.ModelCallAnswer {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisCandidateStreamInvalid, false, errors.New("workspace analysis candidate stream must use the answer phase"))
	}
	if request.SchemaRef.ID != domain.WorkspaceAnalysisCandidateSchemaID || request.SchemaRef.Version != "1" {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisCandidateStreamInvalid, false, errors.New("workspace analysis candidate stream schema is invalid"))
	}
	if isNilWorkspaceAnalysisCandidateStreamSink(sink) {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisCandidateStreamInvalid, false, errors.New("workspace analysis candidate stream sink is unavailable"))
	}
	return nil
}

func isNilWorkspaceAnalysisCandidateStreamSink(sink WorkspaceAnalysisCandidateStreamSink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
