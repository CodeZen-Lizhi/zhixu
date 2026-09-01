// Package audit 将 Knowledge Impact 分析的稳定摘要写入通用 Audit 边界。
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
)

const (
	impactAnalyzedAction     = "IMPACT_ANALYZED"
	impactReportResourceType = "IMPACT_REPORT"
)

// ImpactActorResolver 从请求上下文提取可安全持久化的审计行为主体。
type ImpactActorResolver func(context.Context) (auditdomain.ActorType, string)

// ImpactRecorder 将只读 Impact 报告摘要转换为 append-only Audit 事件。
type ImpactRecorder struct {
	recorder     *auditapplication.Recorder
	resolveActor ImpactActorResolver
}

var _ knowledgeapplication.ImpactAuditPort = (*ImpactRecorder)(nil)
var _ knowledgeapplication.ScopedImpactAuditPort = (*ImpactRecorder)(nil)

// NewImpactRecorder 构造 Impact 到 Audit 的窄适配器。
func NewImpactRecorder(recorder *auditapplication.Recorder, resolveActor ImpactActorResolver) (*ImpactRecorder, error) {
	if recorder == nil {
		return nil, errors.New("impact audit recorder is unavailable")
	}
	if resolveActor == nil {
		return nil, errors.New("impact audit actor resolver is unavailable")
	}
	return &ImpactRecorder{recorder: recorder, resolveActor: resolveActor}, nil
}

// RecordImpactAnalysis 以请求幂等键派生的 Audit ID 固定重放绑定，不把本次 HTTP 的 replay 状态写入事实。
func (recorder *ImpactRecorder) RecordImpactAnalysis(ctx context.Context, record knowledgeapplication.ImpactAuditRecord) error {
	if recorder == nil || recorder.recorder == nil || recorder.resolveActor == nil {
		return errors.New("impact audit recorder is unavailable")
	}
	if ctx == nil {
		return errors.New("impact audit context is nil")
	}
	event, err := recorder.impactEvent(ctx, record)
	if err != nil {
		return err
	}
	_, _, err = recorder.recorder.Record(ctx, event)
	return err
}

// RecordImpactAnalysisTx 在调用方事务中追加 Impact Audit；不会提交或回滚事务。
func (recorder *ImpactRecorder) RecordImpactAnalysisTx(ctx context.Context, transaction any, record knowledgeapplication.ImpactAuditRecord) error {
	if recorder == nil || recorder.recorder == nil || recorder.resolveActor == nil {
		return errors.New("impact audit recorder is unavailable")
	}
	if ctx == nil {
		return errors.New("impact audit context is nil")
	}
	event, err := recorder.impactEvent(ctx, record)
	if err != nil {
		return err
	}
	_, _, err = recorder.recorder.RecordTx(ctx, transaction, event)
	return err
}

// RecordImpactAnalysisScoped 在调用方 opaque transaction 中追加 Impact Audit；不会提交或回滚事务。
func (recorder *ImpactRecorder) RecordImpactAnalysisScoped(ctx context.Context, scope foundation.TransactionScope, record knowledgeapplication.ImpactAuditRecord) error {
	if recorder == nil || recorder.recorder == nil || recorder.resolveActor == nil {
		return errors.New("impact audit recorder is unavailable")
	}
	if ctx == nil || scope == nil {
		return errors.New("impact audit scoped transaction is unavailable")
	}
	event, err := recorder.impactEvent(ctx, record)
	if err != nil {
		return err
	}
	_, _, err = recorder.recorder.RecordScoped(ctx, scope, event)
	return err
}

func (recorder *ImpactRecorder) impactEvent(ctx context.Context, record knowledgeapplication.ImpactAuditRecord) (auditdomain.Event, error) {
	actorType, actorRef := recorder.resolveActor(ctx)
	correlation, err := json.Marshal(struct {
		ReportID      string `json:"report_id"`
		TimelineEvent string `json:"timeline_event_id"`
	}{ReportID: string(record.ReportID), TimelineEvent: string(record.SourceEventID)})
	if err != nil {
		return auditdomain.Event{}, err
	}
	metadata, err := json.Marshal(struct {
		ObjectCount int `json:"object_count"`
	}{ObjectCount: record.ObjectCount})
	if err != nil {
		return auditdomain.Event{}, err
	}
	workspaceID := record.WorkspaceID
	return auditdomain.Event{
		ID:             record.AuditID,
		WorkspaceID:    &workspaceID,
		ActorType:      actorType,
		ActorRef:       actorRef,
		Action:         impactAnalyzedAction,
		ResourceType:   impactReportResourceType,
		ResourceRef:    string(record.ReportID),
		Outcome:        auditdomain.OutcomeSucceeded,
		IdempotencyKey: record.IdempotencyKey,
		Correlation:    correlation,
		Metadata:       metadata,
		SchemaVersion:  auditdomain.SchemaVersion,
		OccurredAt:     record.OccurredAt.UTC().Truncate(time.Microsecond),
	}, nil
}
