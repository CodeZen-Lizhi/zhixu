package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

const timelineEventColumns = `id::text,workspace_id::text,event_type,aggregate_type,aggregate_id::text,
source_event_ref,COALESCE(source_ref,''),event_version,schema_version,summary,payload::text,correlation::text,
operator_type,operator_id::text,owner_binding::text,occurred_at,created_at`

var (
	_ knowledgeapp.TimelineReader   = (*Repository)(nil)
	_ knowledgeapp.EventProjector   = (*Repository)(nil)
	_ knowledgeapp.ImpactRepository = (*Repository)(nil)
)

// AppendEvent 幂等追加不可变 Knowledge Event；相同 source_event_ref 只有精确内容才能重放。
func (repository *Repository) AppendEvent(ctx context.Context, event domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return domain.KnowledgeEvent{}, false, timelineUnavailable(errors.New("timeline repository is unavailable"))
	}
	event.OccurredAt = domain.CanonicalTimelineTime(event.OccurredAt)
	event.CreatedAt = domain.CanonicalTimelineTime(event.CreatedAt)
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.KnowledgeEvent{}, false, timelineStorage(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	persisted, replayed, err := appendTimelineEventTx(ctx, tx, event)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.KnowledgeEvent{}, false, timelineStorage(err)
	}
	return persisted, replayed, nil
}

func appendTimelineEventTx(ctx context.Context, tx pgx.Tx, event domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error) {
	if ctx == nil || tx == nil {
		return domain.KnowledgeEvent{}, false, timelineUnavailable(errors.New("timeline transaction is unavailable"))
	}
	event.OccurredAt = domain.CanonicalTimelineTime(event.OccurredAt)
	event.CreatedAt = domain.CanonicalTimelineTime(event.CreatedAt)
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`, string(event.WorkspaceID), event.SourceEventRef); err != nil {
		return domain.KnowledgeEvent{}, false, timelineStorage(err)
	}
	persisted, found, err := getTimelineEventBySource(ctx, tx, event.WorkspaceID, event.SourceEventRef)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	if found {
		if !sameTimelineEvent(persisted, event) {
			return domain.KnowledgeEvent{}, false, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeImpactConflict, false, errors.New("timeline source event is bound to different content"))
		}
		return persisted, true, nil
	}
	correlation, err := json.Marshal(event.Correlation)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	var operatorType, operatorID, ownerBinding any
	if event.Operator != nil {
		operatorType = string(event.Operator.Type)
		if event.Operator.ID != nil {
			operatorID = string(*event.Operator.ID)
		}
	}
	if event.OwnerBinding != nil {
		encoded, marshalErr := json.Marshal(event.OwnerBinding)
		if marshalErr != nil {
			return domain.KnowledgeEvent{}, false, marshalErr
		}
		ownerBinding = string(encoded)
	}
	var aggregateID any
	if event.AggregateID != nil {
		aggregateID = string(*event.AggregateID)
	}
	row := tx.QueryRow(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,schema_version,
summary,payload,correlation,operator_type,operator_id,owner_binding,occurred_at,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12::jsonb,$13,$14,$15::jsonb,$16,$17)
RETURNING `+timelineEventColumns,
		string(event.ID), string(event.WorkspaceID), string(event.EventType), string(event.AggregateType), aggregateID,
		event.SourceEventRef, nullableText(event.SourceRef), event.EventVersion, event.SchemaVersion, event.Summary,
		string(event.Payload), string(correlation), operatorType, operatorID, ownerBinding,
		event.OccurredAt.UTC(), event.CreatedAt.UTC())
	persisted, err = scanTimelineEvent(row)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	return persisted, false, nil
}

// ListEvents 按 (occurred_at DESC,id DESC) 返回 Workspace-scoped Timeline 页面。
func (repository *Repository) ListEvents(ctx context.Context, query domain.TimelineQuery) (domain.TimelinePage, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return domain.TimelinePage{}, timelineUnavailable(errors.New("timeline repository is unavailable"))
	}
	if err := domain.ValidateTimelineQuery(query); err != nil {
		return domain.TimelinePage{}, err
	}
	args := []any{string(query.WorkspaceID)}
	clauses := []string{"workspace_id=$1"}
	add := func(value any, expression string) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expression, len(args)))
	}
	if len(query.Filter.EventTypes) > 0 {
		types := make([]string, len(query.Filter.EventTypes))
		for index, eventType := range query.Filter.EventTypes {
			types[index] = string(eventType)
		}
		add(types, "event_type=ANY($%d::text[])")
	}
	if query.Filter.AggregateType != "" {
		add(string(query.Filter.AggregateType), "aggregate_type=$%d")
	}
	if query.Filter.AggregateID != nil {
		add(string(*query.Filter.AggregateID), "aggregate_id=$%d")
	}
	if query.Filter.SourceEventRef != "" {
		add(query.Filter.SourceEventRef, "source_event_ref=$%d")
	}
	if query.Filter.OccurredAfter != nil {
		add(query.Filter.OccurredAfter.UTC(), "occurred_at >= $%d")
	}
	if query.Filter.OccurredBefore != nil {
		add(query.Filter.OccurredBefore.UTC(), "occurred_at <= $%d")
	}
	if query.After != nil {
		args = append(args, query.After.OccurredAt.UTC(), string(query.After.ID))
		clauses = append(clauses, fmt.Sprintf("(occurred_at,id) < ($%d,$%d)", len(args)-1, len(args)))
	}
	args = append(args, query.Limit+1)
	sql := `SELECT ` + timelineEventColumns + ` FROM ops.knowledge_event WHERE ` + strings.Join(clauses, " AND ") + fmt.Sprintf(" ORDER BY occurred_at DESC,id DESC LIMIT $%d", len(args))
	rows, err := repository.db.Query(ctx, sql, args...)
	if err != nil {
		return domain.TimelinePage{}, timelineStorage(err)
	}
	defer rows.Close()
	page := domain.TimelinePage{}
	for rows.Next() {
		event, scanErr := scanTimelineEvent(rows)
		if scanErr != nil {
			return domain.TimelinePage{}, scanErr
		}
		if event.WorkspaceID != query.WorkspaceID {
			return domain.TimelinePage{}, timelineCorrupt(errors.New("timeline query crossed workspace boundary"))
		}
		page.Items = append(page.Items, event)
	}
	if err := rows.Err(); err != nil {
		return domain.TimelinePage{}, timelineStorage(err)
	}
	if len(page.Items) > query.Limit {
		page.HasMore = true
		last := page.Items[query.Limit-1]
		page.Next = &domain.TimelinePosition{OccurredAt: last.OccurredAt, ID: last.ID}
		page.Items = page.Items[:query.Limit]
	}
	return page, nil
}

// GetEvent 返回 Workspace-scoped Timeline 事件；跨 Workspace 与不存在统一 NotFound。
func (repository *Repository) GetEvent(ctx context.Context, workspaceID, eventID foundation.ID) (domain.KnowledgeEvent, error) {
	if repository == nil || repository.db == nil || ctx == nil || !timelineValidID(workspaceID) || !timelineValidID(eventID) {
		return domain.KnowledgeEvent{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeTimelineInvalid, false, errors.New("timeline event identity is invalid"))
	}
	event, err := scanTimelineEvent(repository.db.QueryRow(ctx, `SELECT `+timelineEventColumns+` FROM ops.knowledge_event WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(eventID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.KnowledgeEvent{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeTimelineNotFound, false, err)
	}
	if err != nil {
		return domain.KnowledgeEvent{}, err
	}
	return event, nil
}

// ListImpactObjects 以一条 Workspace-scoped 查询读取正式 provenance 关联的下游对象。
func (repository *Repository) ListImpactObjects(ctx context.Context, event domain.KnowledgeEvent) ([]domain.ImpactObject, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return nil, impactUnavailable(errors.New("impact repository is unavailable"))
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	if event.AggregateID == nil {
		return []domain.ImpactObject{}, nil
	}
	rows, err := repository.db.Query(ctx, `WITH changed_claim AS MATERIALIZED (
    SELECT $3::uuid AS claim_id
    WHERE $2='CLAIM'

    UNION

    SELECT relation.source_node_id
    FROM core.relation AS relation
    WHERE relation.workspace_id=$1
      AND $2='RELATION'
      AND relation.id=$3
      AND relation.source_node_type='CLAIM'

    UNION

    SELECT relation.target_node_id
    FROM core.relation AS relation
    WHERE relation.workspace_id=$1
      AND $2='RELATION'
      AND relation.id=$3
      AND relation.target_node_type='CLAIM'
), changed_relation AS MATERIALIZED (
    SELECT $3::uuid AS relation_id
    WHERE $2='RELATION'
), changed_tuple AS MATERIALIZED (
    SELECT source.source_version_id,source.source_span_id
    FROM core.claim_source AS source
    JOIN changed_claim AS changed ON changed.claim_id=source.claim_id
    WHERE source.workspace_id=$1

    UNION

    SELECT evidence.source_version_id,evidence.source_span_id
    FROM core.relation_evidence AS evidence
    JOIN changed_relation AS changed ON changed.relation_id=evidence.relation_id
    WHERE evidence.workspace_id=$1
), impact AS (
    SELECT 'RELATION'::text AS object_type,relation.id,relation.workspace_id,relation.version,
           CASE WHEN relation.relation_type='CONFLICTS_WITH' THEN 'RESOLVE_CONFLICT' ELSE 'REVIEW' END AS action,
           CASE WHEN relation.relation_type='CONFLICTS_WITH' THEN '关联冲突需要重新调查' ELSE '关联关系依赖已变化对象' END AS reason,
           false AS requires_proposal,NULL::jsonb AS owner_binding
    FROM core.relation AS relation
    WHERE relation.workspace_id=$1 AND (
      ($2 IN ('TOPIC','CLAIM') AND (
        (relation.source_node_type=$2 AND relation.source_node_id=$3)
        OR (relation.target_node_type=$2 AND relation.target_node_id=$3)
      ))
      OR ($2='RELATION' AND relation.id=$3)
    )

    UNION ALL

    SELECT 'CONFLICT',conflict.id,conflict.workspace_id,conflict.version,
           'RESOLVE_CONFLICT','冲突结论或成员可能受影响',false,NULL::jsonb
    FROM core.conflict AS conflict
    WHERE conflict.workspace_id=$1
      AND (($2='CONFLICT' AND conflict.id=$3) OR ($2='TOPIC' AND conflict.topic_id=$3))

    UNION ALL

    SELECT 'HEALTH_ISSUE',issue.id,issue.workspace_id,issue.version,
           'REFRESH_HEALTH','健康问题证据需要按新版本复核',false,NULL::jsonb
    FROM ops.health_issue AS issue
    WHERE issue.workspace_id=$1 AND issue.target_type=$2 AND issue.target_id=$3

    UNION ALL

    SELECT 'ARTIFACT',artifact.id,artifact.workspace_id,artifact.version,
           'REGENERATE_ARTIFACT','产物引用的正式来源已发生变化',true,
           jsonb_build_object(
             'artifact_id',artifact.id,
             'artifact_version',artifact.version,
             'revision_id',revision.id,
             'revision_no',revision.revision_no,
             'content_hash',revision.content_hash
           )
    FROM changed_tuple AS changed
    JOIN learning.artifact_revision_citation_selector AS selector
      ON selector.workspace_id=$1
     AND selector.source_version_id=changed.source_version_id
     AND selector.source_span_id=changed.source_span_id
    JOIN learning.artifact AS artifact
      ON artifact.workspace_id=$1
     AND artifact.id=selector.artifact_id
     AND artifact.current_revision_id=selector.revision_id
     AND artifact.domain_schema_version='artifact/v1'
    JOIN learning.artifact_revision AS revision
      ON revision.workspace_id=$1
     AND revision.id=artifact.current_revision_id
     AND revision.artifact_id=artifact.id
     AND revision.domain_schema_version='artifact-revision/v1'

    UNION ALL

    SELECT 'REVIEW_CARD',card.id,card.workspace_id,card.version,
           'REVALIDATE_REVIEW_CARD','复习卡片的主张或证据绑定已发生变化',true,
           jsonb_build_object(
             'card_id',card.id,
             'card_version',card.version,
             'status',card.status,
             'fingerprint',card.fingerprint,
             'claim_id',card.claim_id,
             'evidence_binding_fingerprint',learning.review_card_evidence_binding_fingerprint(card.evidence)
           )
    FROM changed_claim AS changed
    JOIN learning.review_card AS card
      ON card.workspace_id=$1 AND card.claim_id=changed.claim_id

    UNION ALL

    SELECT 'REVIEW_CARD',card.id,card.workspace_id,card.version,
           'REVALIDATE_REVIEW_CARD','复习卡片的主张或证据绑定已发生变化',true,
           jsonb_build_object(
             'card_id',card.id,
             'card_version',card.version,
             'status',card.status,
             'fingerprint',card.fingerprint,
             'claim_id',card.claim_id,
             'evidence_binding_fingerprint',learning.review_card_evidence_binding_fingerprint(card.evidence)
           )
    FROM changed_tuple AS changed
    JOIN learning.review_card_evidence_selector AS version_selector
      ON version_selector.workspace_id=$1
     AND version_selector.selector_kind='SOURCE_VERSION'
     AND version_selector.selector_id=changed.source_version_id
    JOIN learning.review_card_evidence_selector AS span_selector
      ON span_selector.workspace_id=$1
     AND span_selector.card_id=version_selector.card_id
     AND span_selector.claim_id=version_selector.claim_id
     AND span_selector.selector_kind='SOURCE_SPAN'
     AND span_selector.selector_id=changed.source_span_id
    JOIN learning.review_card AS card
      ON card.workspace_id=$1
     AND card.id=version_selector.card_id
     AND card.claim_id=version_selector.claim_id
    WHERE EXISTS (
        SELECT 1
        FROM (
            SELECT element.item
            FROM jsonb_array_elements(
                CASE WHEN jsonb_typeof(card.evidence)='array' THEN card.evidence ELSE '[]'::jsonb END
            ) AS element(item)
            LIMIT 128
        ) AS evidence
        WHERE jsonb_typeof(evidence.item)='object'
          AND evidence.item->>'schema_version'='review-evidence/v1'
          AND learning.try_review_evidence_uuid(evidence.item->>'source_version_id')=changed.source_version_id
          AND learning.try_review_evidence_uuid(evidence.item->>'source_span_id')=changed.source_span_id
    )
)
SELECT DISTINCT object_type,id::text,workspace_id::text,version,action,reason,
       requires_proposal,owner_binding::text
FROM impact
ORDER BY object_type,id,version,owner_binding::text
LIMIT $4`, string(event.WorkspaceID), string(event.AggregateType), string(*event.AggregateID), domain.MaxImpactObjects+1)
	if err != nil {
		return nil, impactStorage(err)
	}
	defer rows.Close()
	objects := make([]domain.ImpactObject, 0)
	for rows.Next() {
		var object domain.ImpactObject
		var objectType, id, workspaceID, action string
		var ownerBinding *string
		if err := rows.Scan(&objectType, &id, &workspaceID, &object.Version, &action, &object.Reason, &object.RequiresProposal, &ownerBinding); err != nil {
			return nil, impactStorage(err)
		}
		object.Type, object.ID, object.WorkspaceID, object.Action = domain.ImpactObjectType(objectType), foundation.ID(id), foundation.ID(workspaceID), domain.ImpactAction(action)
		if err := decodeImpactOwnerBinding(&object, ownerBinding); err != nil {
			return nil, timelineCorrupt(err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, impactStorage(err)
	}
	if len(objects) > domain.MaxImpactObjects {
		return nil, timelineCorrupt(errors.New("impact object result exceeded limit"))
	}
	return dedupeImpactObjects(objects)
}

// GetImpactReport 按 source event 和分析策略返回不可变报告。
func (repository *Repository) GetImpactReport(ctx context.Context, workspaceID, sourceEventID foundation.ID, analysisVersion domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || !timelineValidID(workspaceID) || !timelineValidID(sourceEventID) || !validImpactAnalysisVersion(analysisVersion) {
		return domain.ImpactReport{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact report source identity is invalid"))
	}
	return getImpactReport(ctx, repository.db, workspaceID, sourceEventID, analysisVersion)
}

func getImpactReport(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID, sourceEventID foundation.ID, analysisVersion domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error) {
	report, err := scanImpactReport(queryer.QueryRow(ctx, impactReportSelect+`
WHERE report.workspace_id=$1 AND report.source_event_id=$2 AND report.analysis_version=$3`,
		string(workspaceID), string(sourceEventID), string(analysisVersion)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ImpactReport{}, false, nil
	}
	return report, err == nil, err
}

// ImpactAnalysisReady 只在迁移契约和 Workspace selector marker 均完成时启用 v2 创建。
func (repository *Repository) ImpactAnalysisReady(ctx context.Context, workspaceID foundation.ID) (bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || !timelineValidID(workspaceID) {
		return false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact analysis readiness identity is invalid"))
	}
	return impactAnalysisReady(ctx, repository.db, workspaceID)
}

func impactAnalysisReady(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID) (bool, error) {
	var ready bool
	err := queryer.QueryRow(ctx, `SELECT EXISTS(
    SELECT 1
    FROM core.schema_meta AS schema
    JOIN learning.artifact_citation_selector_backfill AS marker
      ON marker.workspace_id=$1
    WHERE schema.key='timeline_impact'
      AND schema.value='m7-v2'
      AND marker.contract_version='artifact-citation-selector-backfill/v1'
      AND marker.status='COMPLETED'
)`, string(workspaceID)).Scan(&ready)
	if err != nil {
		return false, impactStorage(err)
	}
	return ready, nil
}

// GetImpactReportByID 返回 Workspace-scoped 报告。
func (repository *Repository) GetImpactReportByID(ctx context.Context, workspaceID, reportID foundation.ID) (domain.ImpactReport, error) {
	if repository == nil || repository.db == nil || ctx == nil || !timelineValidID(workspaceID) || !timelineValidID(reportID) {
		return domain.ImpactReport{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact report identity is invalid"))
	}
	report, err := scanImpactReport(repository.db.QueryRow(ctx, impactReportSelect+` WHERE report.workspace_id=$1 AND report.id=$2`, string(workspaceID), string(reportID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ImpactReport{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeImpactNotFound, false, err)
	}
	return report, err
}

// SaveImpactReport 幂等保存同一 source event 的唯一报告；不会触碰 Knowledge 主表。
func (repository *Repository) SaveImpactReport(ctx context.Context, report domain.ImpactReport) (domain.ImpactReport, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return domain.ImpactReport{}, false, impactUnavailable(errors.New("impact repository is unavailable"))
	}
	return saveImpactReport(ctx, repository.db, report)
}

// SaveImpactReportWithAudit 在同一 PostgreSQL 事务中保存报告、Timeline Outbox 和请求 Audit。
func (repository *Repository) SaveImpactReportWithAudit(ctx context.Context, report domain.ImpactReport, idempotencyKey string, audit knowledgeapp.ImpactAuditPort) (domain.ImpactReport, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || isNilImpactAudit(audit) {
		return domain.ImpactReport{}, false, impactUnavailable(errors.New("impact atomic persistence is unavailable"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.ImpactReport{}, false, impactStorage(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	persisted, replayed, err := saveImpactReport(ctx, tx, report)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	record, err := knowledgeapp.BuildImpactAuditRecord(persisted, idempotencyKey, replayed)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	if err := audit.RecordImpactAnalysisTx(ctx, tx, record); err != nil {
		return domain.ImpactReport{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ImpactReport{}, false, impactStorage(err)
	}
	return persisted, replayed, nil
}

func saveImpactReport(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, report domain.ImpactReport) (domain.ImpactReport, bool, error) {
	report.GeneratedAt = domain.CanonicalTimelineTime(report.GeneratedAt)
	report.CreatedAt = domain.CanonicalTimelineTime(report.CreatedAt)
	if err := validateImpactReportIntegrity(report); err != nil {
		return domain.ImpactReport{}, false, err
	}
	analysisVersion := report.EffectiveAnalysisVersion()
	if analysisVersion == domain.ImpactAnalysisVersionV2 {
		ready, err := impactAnalysisReady(ctx, queryer, report.WorkspaceID)
		if err != nil {
			return domain.ImpactReport{}, false, err
		}
		if !ready {
			return domain.ImpactReport{}, false, impactUnavailable(errors.New("impact analysis v2 selector projection is not ready"))
		}
	}
	objects, err := json.Marshal(report.Objects)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	summary, err := json.Marshal(report.Summary)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	var supersedesReportID any
	if report.SupersedesReportID != nil {
		supersedesReportID = string(*report.SupersedesReportID)
	}
	row := queryer.QueryRow(ctx, `WITH report_object AS MATERIALIZED (
    SELECT value
    FROM jsonb_array_elements($5::jsonb) AS item(value)
)
INSERT INTO ops.impact_report(
id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,source_event_version,
fingerprint,error_code,stale_reason,version,created_at,analysis_version,supersedes_report_id)
SELECT $1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14,$16,$17
FROM ops.knowledge_event event
WHERE event.workspace_id=$2
  AND event.id=$3
  AND event.event_version::bigint=$9
  AND event.source_event_ref=$15
  AND (
      $16='impact-analysis/v1'
      OR ($17::uuid IS NULL AND NOT EXISTS (
          SELECT 1
          FROM ops.impact_report AS predecessor
          WHERE predecessor.workspace_id=$2
            AND predecessor.source_event_id=$3
            AND predecessor.analysis_version='impact-analysis/v1'
      ))
      OR EXISTS (
          SELECT 1
          FROM ops.impact_report AS predecessor
          WHERE predecessor.workspace_id=$2
            AND predecessor.source_event_id=$3
            AND predecessor.analysis_version='impact-analysis/v1'
            AND predecessor.id=$17::uuid
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM report_object AS object
      WHERE object.value->>'type'='ARTIFACT'
        AND NOT EXISTS (
            SELECT 1
            FROM learning.artifact AS artifact
            JOIN learning.artifact_revision AS revision
              ON revision.workspace_id=artifact.workspace_id
             AND revision.artifact_id=artifact.id
             AND revision.id=artifact.current_revision_id
             AND revision.domain_schema_version='artifact-revision/v1'
            WHERE artifact.workspace_id=$2
              AND artifact.domain_schema_version='artifact/v1'
              AND artifact.id=(object.value->>'id')::uuid
              AND artifact.version=(object.value->>'version')::bigint
              AND artifact.id=(object.value->'artifact_binding'->>'artifact_id')::uuid
              AND artifact.version=(object.value->'artifact_binding'->>'artifact_version')::bigint
              AND revision.id=(object.value->'artifact_binding'->>'revision_id')::uuid
              AND revision.revision_no=(object.value->'artifact_binding'->>'revision_no')::bigint
              AND revision.content_hash=object.value->'artifact_binding'->>'content_hash'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM report_object AS object
      WHERE object.value->>'type'='REVIEW_CARD'
        AND NOT EXISTS (
            SELECT 1
            FROM learning.review_card AS card
            WHERE card.workspace_id=$2
              AND card.id=(object.value->>'id')::uuid
              AND card.version=(object.value->>'version')::bigint
              AND card.id=(object.value->'review_card_binding'->>'card_id')::uuid
              AND card.version=(object.value->'review_card_binding'->>'card_version')::bigint
              AND card.status=object.value->'review_card_binding'->>'status'
              AND card.fingerprint=object.value->'review_card_binding'->>'fingerprint'
              AND card.claim_id=(object.value->'review_card_binding'->>'claim_id')::uuid
              AND learning.review_card_evidence_binding_fingerprint(card.evidence)=
                  object.value->'review_card_binding'->>'evidence_binding_fingerprint'
        )
  )
ON CONFLICT(workspace_id,source_event_id,analysis_version) DO NOTHING
RETURNING id::text,workspace_id::text,source_event_id::text,status,objects::text,summary::text,generated_at,
schema_version,analysis_version,supersedes_report_id::text,NULL::text,source_event_version,
fingerprint,error_code,stale_reason,version,created_at`,
		string(report.ID), string(report.WorkspaceID), string(report.SourceEventID), string(report.Status), string(objects), string(summary), report.GeneratedAt.UTC(),
		report.SchemaVersion(), report.SourceVersion, report.Fingerprint, nullableText(report.ErrorCode), nullableText(report.StaleReason), report.Version, report.CreatedAt.UTC(),
		report.SourceEventRef, string(analysisVersion), supersedesReportID)
	persisted, err := scanImpactReportRow(row, report.SourceEventRef)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, loadErr := getImpactReport(ctx, queryer, report.WorkspaceID, report.SourceEventID, analysisVersion)
		if loadErr != nil {
			return domain.ImpactReport{}, false, loadErr
		}
		if !found {
			return domain.ImpactReport{}, false, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeImpactConflict, false, errors.New("impact report source event or owner binding is stale"))
		}
		if !sameImpactReport(existing, report) {
			return domain.ImpactReport{}, false, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeImpactConflict, false, errors.New("impact source event is bound to a different report"))
		}
		return existing, true, nil
	}
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	return persisted, false, nil
}

func isNilImpactAudit(audit knowledgeapp.ImpactAuditPort) bool {
	if audit == nil {
		return true
	}
	value := reflect.ValueOf(audit)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

const impactReportSelect = `SELECT report.id::text,report.workspace_id::text,report.source_event_id::text,report.status,
report.objects::text,report.summary::text,report.generated_at,report.schema_version,report.analysis_version,
report.supersedes_report_id::text,successor.id::text,report.source_event_version,report.fingerprint,
report.error_code,report.stale_reason,report.version,report.created_at,event.source_event_ref,event.event_version
FROM ops.impact_report report JOIN ops.knowledge_event event
  ON event.id=report.source_event_id AND event.workspace_id=report.workspace_id
LEFT JOIN ops.impact_report successor
  ON successor.workspace_id=report.workspace_id AND successor.supersedes_report_id=report.id`

func getTimelineEventBySource(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, sourceEventRef string) (domain.KnowledgeEvent, bool, error) {
	event, err := scanTimelineEvent(queryer.QueryRow(ctx, `SELECT `+timelineEventColumns+` FROM ops.knowledge_event WHERE workspace_id=$1 AND source_event_ref=$2 FOR UPDATE`, string(workspaceID), sourceEventRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.KnowledgeEvent{}, false, nil
	}
	return event, err == nil, err
}

func scanTimelineEvent(row interface{ Scan(...any) error }) (domain.KnowledgeEvent, error) {
	var event domain.KnowledgeEvent
	var id, workspaceID, eventType, aggregateType string
	var aggregateID, operatorType, operatorID, ownerBinding *string
	var payload, correlation string
	if err := row.Scan(&id, &workspaceID, &eventType, &aggregateType, &aggregateID, &event.SourceEventRef, &event.SourceRef,
		&event.EventVersion, &event.SchemaVersion, &event.Summary, &payload, &correlation,
		&operatorType, &operatorID, &ownerBinding, &event.OccurredAt, &event.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.KnowledgeEvent{}, err
		}
		return domain.KnowledgeEvent{}, timelineStorage(err)
	}
	event.ID, event.WorkspaceID, event.EventType, event.AggregateType = foundation.ID(id), foundation.ID(workspaceID), domain.EventType(eventType), domain.TimelineAggregateType(aggregateType)
	if aggregateID != nil {
		value := foundation.ID(*aggregateID)
		event.AggregateID = &value
	}
	event.Payload = json.RawMessage(payload)
	if err := decodeTimelineCorrelation(correlation, &event.Correlation); err != nil {
		return domain.KnowledgeEvent{}, timelineCorrupt(err)
	}
	operator, binding, err := decodeTimelineEventExtensions(operatorType, operatorID, ownerBinding)
	if err != nil {
		return domain.KnowledgeEvent{}, timelineCorrupt(err)
	}
	event.Operator, event.OwnerBinding = operator, binding
	event.OccurredAt = domain.CanonicalTimelineTime(event.OccurredAt)
	event.CreatedAt = domain.CanonicalTimelineTime(event.CreatedAt)
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, timelineCorrupt(err)
	}
	return event, nil
}

func scanImpactReport(row interface{ Scan(...any) error }) (domain.ImpactReport, error) {
	var sourceEventRef string
	var sourceEventVersion int64
	report, err := scanImpactReportData(row, &sourceEventRef, &sourceEventVersion)
	if err != nil {
		return domain.ImpactReport{}, err
	}
	report.SourceEventRef = sourceEventRef
	if report.SourceVersion != sourceEventVersion {
		return domain.ImpactReport{}, timelineCorrupt(errors.New("impact report source event version is inconsistent"))
	}
	if err := validateImpactReportIntegrity(report); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	return report, nil
}

func scanImpactReportRow(row interface{ Scan(...any) error }, sourceEventRef string) (domain.ImpactReport, error) {
	report, err := scanImpactReportData(row, nil, nil)
	if err != nil {
		return domain.ImpactReport{}, err
	}
	report.SourceEventRef = sourceEventRef
	if err := validateImpactReportIntegrity(report); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	return report, nil
}

func scanImpactReportData(row interface{ Scan(...any) error }, sourceEventRef *string, sourceEventVersion *int64) (domain.ImpactReport, error) {
	var report domain.ImpactReport
	var id, workspaceID, sourceEventID, status, objects, summary, schemaVersion, analysisVersion string
	var supersedesReportID, supersededByReportID, errorCode, staleReason *string
	destinations := []any{
		&id, &workspaceID, &sourceEventID, &status, &objects, &summary, &report.GeneratedAt,
		&schemaVersion, &analysisVersion, &supersedesReportID, &supersededByReportID, &report.SourceVersion,
		&report.Fingerprint, &errorCode, &staleReason, &report.Version, &report.CreatedAt,
	}
	if sourceEventRef != nil {
		destinations = append(destinations, sourceEventRef)
	}
	if sourceEventVersion != nil {
		destinations = append(destinations, sourceEventVersion)
	}
	if err := row.Scan(destinations...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ImpactReport{}, err
		}
		return domain.ImpactReport{}, impactStorage(err)
	}
	report.AnalysisVersion = domain.ImpactAnalysisVersion(analysisVersion)
	if !validImpactAnalysisVersion(report.AnalysisVersion) || schemaVersion != report.SchemaVersion() {
		return domain.ImpactReport{}, timelineCorrupt(errors.New("impact report schema is unsupported"))
	}
	report.ID, report.WorkspaceID, report.SourceEventID, report.Status = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(sourceEventID), domain.ImpactReportStatus(status)
	if supersedesReportID != nil {
		value := foundation.ID(*supersedesReportID)
		report.SupersedesReportID = &value
	}
	if supersededByReportID != nil {
		value := foundation.ID(*supersededByReportID)
		report.SupersededByReportID = &value
	}
	if errorCode != nil {
		report.ErrorCode = *errorCode
	}
	if staleReason != nil {
		report.StaleReason = *staleReason
	}
	if err := decodeTimelineJSON(objects, &report.Objects, "impact report objects"); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	if err := decodeTimelineJSON(summary, &report.Summary, "impact report summary"); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	report.GeneratedAt = domain.CanonicalTimelineTime(report.GeneratedAt)
	report.CreatedAt = domain.CanonicalTimelineTime(report.CreatedAt)
	return report, nil
}

func sameTimelineEvent(left, right domain.KnowledgeEvent) bool {
	leftCorrelation, _ := json.Marshal(left.Correlation)
	rightCorrelation, _ := json.Marshal(right.Correlation)
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.EventType == right.EventType && left.AggregateType == right.AggregateType && sameOptionalID(left.AggregateID, right.AggregateID) && left.SourceEventRef == right.SourceEventRef && left.SourceRef == right.SourceRef && left.EventVersion == right.EventVersion && left.SchemaVersion == right.SchemaVersion && left.Summary == right.Summary && string(left.Payload) == string(right.Payload) && string(leftCorrelation) == string(rightCorrelation) && reflect.DeepEqual(left.Operator, right.Operator) && reflect.DeepEqual(left.OwnerBinding, right.OwnerBinding) && left.OccurredAt.Equal(right.OccurredAt) && left.CreatedAt.Equal(right.CreatedAt)
}

func decodeTimelineEventExtensions(operatorType, operatorID, ownerBinding *string) (*domain.EventOperator, *domain.EventOwnerBinding, error) {
	var operator *domain.EventOperator
	if operatorType == nil {
		if operatorID != nil {
			return nil, nil, errors.New("timeline operator id is present without a type")
		}
	} else {
		value := domain.EventOperator{Type: domain.EventOperatorType(*operatorType)}
		if operatorID != nil {
			parsed, err := foundation.ParseID(*operatorID)
			if err != nil || parsed != foundation.ID(*operatorID) {
				return nil, nil, errors.New("timeline operator id is invalid")
			}
			value.ID = &parsed
		}
		operator = &value
	}

	var binding *domain.EventOwnerBinding
	if ownerBinding != nil {
		value := domain.EventOwnerBinding{}
		if err := decodeTimelineJSON(*ownerBinding, &value, "timeline owner binding"); err != nil {
			return nil, nil, err
		}
		binding = &value
	}
	return operator, binding, nil
}

func decodeTimelineJSON(raw string, target any, field string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%s contains trailing JSON", field)
		}
		return fmt.Errorf("decode %s trailing data: %w", field, err)
	}
	return nil
}

func decodeImpactOwnerBinding(object *domain.ImpactObject, raw *string) error {
	if object == nil {
		return errors.New("impact object is nil")
	}
	switch object.Type {
	case domain.ImpactObjectArtifact:
		if raw == nil {
			return errors.New("artifact impact owner binding is missing")
		}
		binding := domain.ArtifactImpactBinding{}
		if err := decodeTimelineJSON(*raw, &binding, "artifact impact owner binding"); err != nil {
			return err
		}
		object.ArtifactBinding = &binding
	case domain.ImpactObjectReviewCard:
		if raw == nil {
			return errors.New("review card impact owner binding is missing")
		}
		binding := domain.ReviewCardImpactBinding{}
		if err := decodeTimelineJSON(*raw, &binding, "review card impact owner binding"); err != nil {
			return err
		}
		object.ReviewCardBinding = &binding
	default:
		if raw != nil {
			return errors.New("legacy impact object contains an owner binding")
		}
	}
	return nil
}

func sameImpactReport(left, right domain.ImpactReport) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SourceEventID == right.SourceEventID && left.SourceEventRef == right.SourceEventRef && left.SourceVersion == right.SourceVersion && left.EffectiveAnalysisVersion() == right.EffectiveAnalysisVersion() && sameOptionalID(left.SupersedesReportID, right.SupersedesReportID) && left.Status == right.Status && left.Fingerprint == right.Fingerprint && left.ErrorCode == right.ErrorCode && left.StaleReason == right.StaleReason && left.Version == right.Version && reflect.DeepEqual(left.Objects, right.Objects) && reflect.DeepEqual(left.Summary, right.Summary)
}

func validateImpactReportIntegrity(report domain.ImpactReport) error {
	if err := domain.ValidateImpactReport(report); err != nil {
		return err
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(
		report.EffectiveAnalysisVersion(), report.SourceEventID, report.SourceVersion, report.Objects,
	)
	if err != nil {
		return err
	}
	if fingerprint != report.Fingerprint {
		return errors.New("impact report fingerprint does not match its source and objects")
	}
	return nil
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func dedupeImpactObjects(objects []domain.ImpactObject) ([]domain.ImpactObject, error) {
	sort.Slice(objects, func(left, right int) bool {
		leftKey := string(objects[left].Type) + ":" + string(objects[left].ID)
		rightKey := string(objects[right].Type) + ":" + string(objects[right].ID)
		return leftKey < rightKey
	})
	result := make([]domain.ImpactObject, 0, len(objects))
	for _, object := range objects {
		if err := domain.ValidateImpactObject(object); err != nil {
			return nil, timelineCorrupt(err)
		}
		if len(result) > 0 && result[len(result)-1].Type == object.Type && result[len(result)-1].ID == object.ID {
			if !reflect.DeepEqual(result[len(result)-1], object) {
				return nil, timelineCorrupt(errors.New("impact query returned conflicting duplicate objects"))
			}
			continue
		}
		result = append(result, object)
	}
	return result, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func timelineValidID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func timelineStorage(err error) error {
	return classify(err, domain.ErrorCodeTimelineUnavailable)
}

func impactStorage(err error) error {
	return classify(err, domain.ErrorCodeImpactUnavailable)
}

func timelineUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, err)
}

func impactUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeImpactUnavailable, true, err)
}

func timelineCorrupt(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeTimelineInconsistent, false, err)
}

func validImpactAnalysisVersion(version domain.ImpactAnalysisVersion) bool {
	return version == domain.ImpactAnalysisVersionV1 || version == domain.ImpactAnalysisVersionV2
}
