package application

import (
	"context"
	"errors"
	"reflect"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const errorCodeWorkspaceAnalysisTimelineUnavailable = "CONVERSATION_WORKSPACE_ANALYSIS_TIMELINE_UNAVAILABLE"

// WorkspaceAnalysisTimelineQuery 绑定一个 Workspace 内 Answer 的权威时间线。
type WorkspaceAnalysisTimelineQuery struct {
	WorkspaceID foundation.ID
	AnswerID    foundation.ID
}

// WorkspaceAnalysisTimelineReader 是加载脱敏、权威 Workspace Analysis 时间线的窄端口。
type WorkspaceAnalysisTimelineReader interface {
	// GetWorkspaceAnalysisTimeline 按 Workspace 与 Answer 身份读取快照；跨 Workspace 和非分析 Answer 均返回 NotFound。
	GetWorkspaceAnalysisTimeline(context.Context, WorkspaceAnalysisTimelineQuery) (conversationdomain.WorkspaceAnalysisTimeline, error)
}

// GetWorkspaceAnalysisTimeline 返回经过身份和领域不变量复核的权威时间线快照。
func (service *Service) GetWorkspaceAnalysisTimeline(ctx context.Context, query WorkspaceAnalysisTimelineQuery) (conversationdomain.WorkspaceAnalysisTimeline, error) {
	if service == nil || isNilWorkspaceAnalysisTimelineReader(service.timelines) {
		return conversationdomain.WorkspaceAnalysisTimeline{}, workspaceAnalysisTimelineUnavailable()
	}
	if err := validateScopedIDs(query.WorkspaceID, query.AnswerID); err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, err
	}
	timeline, err := service.timelines.GetWorkspaceAnalysisTimeline(ctx, query)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimeline{}, err
	}
	if err := timeline.Validate(); err != nil || timeline.WorkspaceID != query.WorkspaceID || timeline.AnswerID != query.AnswerID {
		return conversationdomain.WorkspaceAnalysisTimeline{}, resultInconsistent(err)
	}
	return timeline, nil
}

func workspaceAnalysisTimelineUnavailable() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeWorkspaceAnalysisTimelineUnavailable, false, errors.New("workspace analysis timeline reader is unavailable"))
}

func isNilWorkspaceAnalysisTimelineReader(reader WorkspaceAnalysisTimelineReader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
