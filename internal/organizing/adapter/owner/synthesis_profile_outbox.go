package owner

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/lib/pq"
)

type sourceReadyIDClaimer interface {
	workflowapp.ScopedSourceReadyOutbox
	ClaimSourceReadyIDScoped(context.Context, foundation.TransactionScope, foundation.ID) (workflowapp.SourceReadyOutboxFact, bool, error)
}

// ProfiledSynthesisOutbox 是已有所属模块事实的读取投影，不是第二条队列。先判断资格，再应用 LIMIT，避免不可用 Profile 阻塞其他已就绪来源，即使它们位于同一工作区。
type ProfiledSynthesisOutbox struct{ outbox sourceReadyIDClaimer }

func NewProfiledSynthesisOutbox(outbox sourceReadyIDClaimer) (*ProfiledSynthesisOutbox, error) {
	if nilDependency(outbox) {
		return nil, dependencyUnavailable("source-ready owner is unavailable")
	}
	return &ProfiledSynthesisOutbox{outbox: outbox}, nil
}

func (outbox *ProfiledSynthesisOutbox) ClaimSourceReadyScoped(ctx context.Context, scope foundation.TransactionScope) (workflowapp.SourceReadyOutboxFact, bool, error) {
	return outbox.ClaimSourceReadyExcludingScoped(ctx, scope, nil)
}

func (outbox *ProfiledSynthesisOutbox) PublishSourceReadyScoped(ctx context.Context, scope foundation.TransactionScope, fact workflowapp.SourceReadyOutboxFact) error {
	return outbox.outbox.PublishSourceReadyScoped(ctx, scope, fact)
}

func (outbox *ProfiledSynthesisOutbox) ClaimSourceReadyExcludingScoped(ctx context.Context, scope foundation.TransactionScope, excluded []foundation.ID) (workflowapp.SourceReadyOutboxFact, bool, error) {
	if outbox == nil || nilDependency(outbox.outbox) || ctx == nil || len(excluded) > 100 {
		return workflowapp.SourceReadyOutboxFact{}, false, invalid("profile-ready claim is invalid")
	}
	ids := make([]string, len(excluded))
	for i, id := range excluded {
		if !validID(id) {
			return workflowapp.SourceReadyOutboxFact{}, false, invalid("excluded workspace is invalid")
		}
		ids[i] = string(id)
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return workflowapp.SourceReadyOutboxFact{}, false, err
	}
	var row struct{ ID string }
	result := tx.WithContext(ctx).Raw(`SELECT event.id::text
		FROM workflow.outbox_event event
		JOIN core.source_version v ON v.id=CASE WHEN event.event_type='ingestion.source.ready'
		 THEN (event.payload->>'source_version_id')::uuid END AND v.workspace_id=event.workspace_id
		JOIN core.source s ON s.id=v.source_id AND s.workspace_id=v.workspace_id
		WHERE event.event_type=? AND event.published_at IS NULL
		AND NOT (event.workspace_id=ANY(?::uuid[]))
		AND (EXISTS (
		 SELECT 1 FROM learning.document_knowledge_profile_revision revision
		 WHERE revision.workspace_id=event.workspace_id AND revision.source_version_id=v.id
		 AND revision.parse_projection_id::text=event.payload->>'parse_projection_id'
		) OR `+synthesisDerivedSQL+` OR EXISTS (
		 SELECT 1 FROM organizing.synthesis_processing processing
		 WHERE processing.workspace_id=event.workspace_id AND processing.source_event_id=event.id
		 AND processing.fusion_request_id IS NULL AND processing.processor_version=?
		))
		ORDER BY event.occurred_at,event.id LIMIT 1 FOR UPDATE OF event SKIP LOCKED`,
		workflowapp.SourceReadyEventType, pq.Array(ids), domain.SynthesisProcessorVersion).Scan(&row)
	if result.Error != nil {
		return workflowapp.SourceReadyOutboxFact{}, false, synthesisSourceDBError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return workflowapp.SourceReadyOutboxFact{}, false, nil
	}
	id, err := foundation.ParseID(row.ID)
	if err != nil {
		return workflowapp.SourceReadyOutboxFact{}, false, inconsistent("profile-ready event identity is invalid")
	}
	return outbox.outbox.ClaimSourceReadyIDScoped(ctx, scope, id)
}
