package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var (
	_ knowledgeapp.TimelineReader         = (*GORMRepository)(nil)
	_ knowledgeapp.EventProjector         = (*GORMRepository)(nil)
	_ knowledgeapp.ImpactRepository       = (*GORMRepository)(nil)
	_ knowledgeapp.ScopedImpactRepository = (*GORMRepository)(nil)
)

// AppendEvent appends an immutable Timeline event. The source binding is
// protected by the same transaction-scoped advisory lock as the legacy path.
func (repository *GORMRepository) AppendEvent(ctx context.Context, event domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error) {
	event.OccurredAt = domain.CanonicalTimelineTime(event.OccurredAt)
	event.CreatedAt = domain.CanonicalTimelineTime(event.CreatedAt)
	if err := repository.ready(ctx); err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	var persisted domain.KnowledgeEvent
	var replayed bool
	err := repository.within(ctx, foundation.TransactionOptions{}, domain.ErrorCodeTimelineUnavailable, domain.ErrorCodeTimelineUnavailable, classifyGORMKnowledge,
		func(callbackCtx context.Context, _ foundation.TransactionScope, transaction *gorm.DB) error {
			var appendErr error
			persisted, replayed, appendErr = gormAppendTimelineEvent(callbackCtx, transaction, event)
			return appendErr
		})
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	return persisted, replayed, nil
}

func gormAppendTimelineEvent(ctx context.Context, database *gorm.DB, event domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error) {
	if ctx == nil || !validKnowledgeGORMDatabase(database) {
		return domain.KnowledgeEvent{}, false, timelineUnavailable(errors.New("timeline transaction is unavailable"))
	}
	event.OccurredAt = domain.CanonicalTimelineTime(event.OccurredAt)
	event.CreatedAt = domain.CanonicalTimelineTime(event.CreatedAt)
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	if _, err := gormKnowledgeExec(ctx, database, `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?,0))`, string(event.WorkspaceID), event.SourceEventRef); err != nil {
		return domain.KnowledgeEvent{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	existing, found, err := gormGetTimelineEventBySource(ctx, database, event.WorkspaceID, event.SourceEventRef, true)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	if found {
		if !sameTimelineEvent(existing, event) {
			return domain.KnowledgeEvent{}, false, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeImpactConflict, false, errors.New("timeline source event is bound to different content"))
		}
		return existing, true, nil
	}
	correlation, err := json.Marshal(event.Correlation)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	var aggregateID, operatorType, operatorID, ownerBinding any
	if event.AggregateID != nil {
		aggregateID = string(*event.AggregateID)
	}
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
		ownerBinding = knowledgeJSONB(encoded)
	}
	row, err := gormKnowledgeRawRow(ctx, database, `INSERT INTO ops.knowledge_event(
		id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,schema_version,
		summary,payload,correlation,operator_type,operator_id,owner_binding,occurred_at,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?::jsonb,?::jsonb,?,?,?::jsonb,?,?)
		RETURNING `+timelineEventColumns,
		string(event.ID), string(event.WorkspaceID), string(event.EventType), string(event.AggregateType), aggregateID,
		event.SourceEventRef, nullableText(event.SourceRef), event.EventVersion, event.SchemaVersion, event.Summary,
		knowledgeJSONB(event.Payload), knowledgeJSONB(correlation), operatorType, operatorID, ownerBinding, event.OccurredAt.UTC(), event.CreatedAt.UTC())
	if err != nil {
		return domain.KnowledgeEvent{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	persisted, err := gormScanTimelineEvent(row)
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	return persisted, false, nil
}

func (repository *GORMRepository) ListEvents(ctx context.Context, query domain.TimelineQuery) (domain.TimelinePage, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.TimelinePage{}, err
	}
	if err := domain.ValidateTimelineQuery(query); err != nil {
		return domain.TimelinePage{}, err
	}
	arguments := []any{string(query.WorkspaceID)}
	clauses := []string{"workspace_id=?"}
	add := func(value any, expression string) {
		arguments = append(arguments, value)
		clauses = append(clauses, expression)
	}
	if len(query.Filter.EventTypes) > 0 {
		types := make([]string, len(query.Filter.EventTypes))
		for i, value := range query.Filter.EventTypes {
			types[i] = string(value)
		}
		// This intentionally remains one parameter: GORM must not expand array values.
		add(gormTextArray(types), "event_type=ANY(?::text[])")
	}
	if query.Filter.AggregateType != "" {
		add(string(query.Filter.AggregateType), "aggregate_type=?")
	}
	if query.Filter.AggregateID != nil {
		add(string(*query.Filter.AggregateID), "aggregate_id=?")
	}
	if query.Filter.SourceEventRef != "" {
		add(query.Filter.SourceEventRef, "source_event_ref=?")
	}
	if query.Filter.OccurredAfter != nil {
		add(query.Filter.OccurredAfter.UTC(), "occurred_at >= ?")
	}
	if query.Filter.OccurredBefore != nil {
		add(query.Filter.OccurredBefore.UTC(), "occurred_at <= ?")
	}
	if query.After != nil {
		arguments = append(arguments, query.After.OccurredAt.UTC(), string(query.After.ID))
		clauses = append(clauses, "(occurred_at,id) < (?,?)")
	}
	arguments = append(arguments, query.Limit+1)
	rows, err := gormKnowledgeRawRows(ctx, repository.database, `SELECT `+timelineEventColumns+` FROM ops.knowledge_event WHERE `+strings.Join(clauses, " AND ")+` ORDER BY occurred_at DESC,id DESC LIMIT ?`, arguments...)
	if err != nil {
		return domain.TimelinePage{}, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	defer rows.Close()
	page := domain.TimelinePage{}
	for rows.Next() {
		event, scanErr := gormScanTimelineEvent(rows)
		if scanErr != nil {
			return domain.TimelinePage{}, scanErr
		}
		if event.WorkspaceID != query.WorkspaceID {
			return domain.TimelinePage{}, timelineCorrupt(errors.New("timeline query crossed workspace boundary"))
		}
		page.Items = append(page.Items, event)
	}
	if err := rows.Err(); err != nil {
		return domain.TimelinePage{}, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	if len(page.Items) > query.Limit {
		page.HasMore = true
		last := page.Items[query.Limit-1]
		page.Next = &domain.TimelinePosition{OccurredAt: last.OccurredAt, ID: last.ID}
		page.Items = page.Items[:query.Limit]
	}
	return page, nil
}

func (repository *GORMRepository) GetEvent(ctx context.Context, workspaceID, eventID foundation.ID) (domain.KnowledgeEvent, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.KnowledgeEvent{}, err
	}
	if !timelineValidID(workspaceID) || !timelineValidID(eventID) {
		return domain.KnowledgeEvent{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeTimelineInvalid, false, errors.New("timeline event identity is invalid"))
	}
	row, err := gormKnowledgeRawRow(ctx, repository.database, `SELECT `+timelineEventColumns+` FROM ops.knowledge_event WHERE workspace_id=? AND id=?`, string(workspaceID), string(eventID))
	if err != nil {
		return domain.KnowledgeEvent{}, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	event, err := gormScanTimelineEvent(row)
	if gormKnowledgeNoRows(err) {
		return domain.KnowledgeEvent{}, notFound(domain.ErrorCodeTimelineNotFound, err)
	}
	if err != nil {
		return domain.KnowledgeEvent{}, err
	}
	return event, nil
}

// ListImpactObjects uses one Workspace-scoped CTE rather than per-object
// lookups; every repeated predicate remains individually parameterized.
func (repository *GORMRepository) ListImpactObjects(ctx context.Context, event domain.KnowledgeEvent) ([]domain.ImpactObject, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	if event.AggregateID == nil {
		return []domain.ImpactObject{}, nil
	}
	workspaceID, aggregateType, aggregateID := string(event.WorkspaceID), string(event.AggregateType), string(*event.AggregateID)
	// Every repeated predicate remains independently bound because GORM's Raw
	// SQL uses positional question-mark placeholders.
	arguments := []any{
		aggregateID, aggregateType,
		workspaceID, aggregateType, aggregateID,
		workspaceID, aggregateType, aggregateID,
		aggregateID, aggregateType,
		workspaceID, workspaceID,
		workspaceID, aggregateType, aggregateType, aggregateID, aggregateType, aggregateID, aggregateType, aggregateID,
		workspaceID, aggregateType, aggregateID, aggregateType, aggregateID,
		workspaceID, aggregateType, aggregateID,
		workspaceID, workspaceID, workspaceID,
		workspaceID,
		workspaceID, workspaceID, workspaceID,
		domain.MaxImpactObjects + 1,
	}
	rows, err := gormKnowledgeRawRows(ctx, repository.database, gormImpactObjectsSQL, arguments...)
	if err != nil {
		return nil, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	defer rows.Close()
	objects := make([]domain.ImpactObject, 0)
	for rows.Next() {
		var object domain.ImpactObject
		var objectType, id, workspaceID, action string
		var ownerBinding *string
		if err := rows.Scan(&objectType, &id, &workspaceID, &object.Version, &action, &object.Reason, &object.RequiresProposal, &ownerBinding); err != nil {
			return nil, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
		}
		object.Type, object.ID, object.WorkspaceID, object.Action = domain.ImpactObjectType(objectType), foundation.ID(id), foundation.ID(workspaceID), domain.ImpactAction(action)
		if err := decodeImpactOwnerBinding(&object, ownerBinding); err != nil {
			return nil, timelineCorrupt(err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	if len(objects) > domain.MaxImpactObjects {
		return nil, timelineCorrupt(errors.New("impact object result exceeded limit"))
	}
	return dedupeImpactObjects(objects)
}

const gormImpactObjectsSQL = `WITH changed_claim AS MATERIALIZED (
    SELECT ?::uuid AS claim_id WHERE ?='CLAIM'
    UNION
    SELECT relation.source_node_id FROM core.relation relation
     WHERE relation.workspace_id=? AND ?='RELATION' AND relation.id=? AND relation.source_node_type='CLAIM'
    UNION
    SELECT relation.target_node_id FROM core.relation relation
     WHERE relation.workspace_id=? AND ?='RELATION' AND relation.id=? AND relation.target_node_type='CLAIM'
), changed_relation AS MATERIALIZED (
    SELECT ?::uuid AS relation_id WHERE ?='RELATION'
), changed_tuple AS MATERIALIZED (
    SELECT source.source_version_id,source.source_span_id FROM core.claim_source source
      JOIN changed_claim changed ON changed.claim_id=source.claim_id WHERE source.workspace_id=?
    UNION
    SELECT evidence.source_version_id,evidence.source_span_id FROM core.relation_evidence evidence
      JOIN changed_relation changed ON changed.relation_id=evidence.relation_id WHERE evidence.workspace_id=?
), impact AS (
    SELECT 'RELATION'::text object_type,relation.id,relation.workspace_id,relation.version,
      CASE WHEN relation.relation_type='CONFLICTS_WITH' THEN 'RESOLVE_CONFLICT' ELSE 'REVIEW' END action,
      CASE WHEN relation.relation_type='CONFLICTS_WITH' THEN '关联冲突需要重新调查' ELSE '关联关系依赖已变化对象' END reason,
      false requires_proposal,NULL::jsonb owner_binding
    FROM core.relation relation WHERE relation.workspace_id=? AND ((? IN ('TOPIC','CLAIM') AND ((relation.source_node_type=? AND relation.source_node_id=?) OR (relation.target_node_type=? AND relation.target_node_id=?))) OR (?='RELATION' AND relation.id=?))
    UNION ALL
    SELECT 'CONFLICT',conflict.id,conflict.workspace_id,conflict.version,'RESOLVE_CONFLICT','冲突结论或成员可能受影响',false,NULL::jsonb
    FROM core.conflict conflict WHERE conflict.workspace_id=? AND ((?='CONFLICT' AND conflict.id=?) OR (?='TOPIC' AND conflict.topic_id=?))
    UNION ALL
    SELECT 'HEALTH_ISSUE',issue.id,issue.workspace_id,issue.version,'REFRESH_HEALTH','健康问题证据需要按新版本复核',false,NULL::jsonb
    FROM ops.health_issue issue WHERE issue.workspace_id=? AND issue.target_type=? AND issue.target_id=?
    UNION ALL
    SELECT 'ARTIFACT',artifact.id,artifact.workspace_id,artifact.version,'REGENERATE_ARTIFACT','产物引用的正式来源已发生变化',true,
      jsonb_build_object('artifact_id',artifact.id,'artifact_version',artifact.version,'revision_id',revision.id,'revision_no',revision.revision_no,'content_hash',revision.content_hash)
    FROM changed_tuple changed JOIN learning.artifact_revision_citation_selector selector ON selector.workspace_id=? AND selector.source_version_id=changed.source_version_id AND selector.source_span_id=changed.source_span_id
    JOIN learning.artifact artifact ON artifact.workspace_id=? AND artifact.id=selector.artifact_id AND artifact.current_revision_id=selector.revision_id AND artifact.domain_schema_version='artifact/v1'
    JOIN learning.artifact_revision revision ON revision.workspace_id=? AND revision.id=artifact.current_revision_id AND revision.artifact_id=artifact.id AND revision.domain_schema_version='artifact-revision/v1'
    UNION ALL
    SELECT 'REVIEW_CARD',card.id,card.workspace_id,card.version,'REVALIDATE_REVIEW_CARD','复习卡片的主张或证据绑定已发生变化',true,
      jsonb_build_object('card_id',card.id,'card_version',card.version,'status',card.status,'fingerprint',card.fingerprint,'claim_id',card.claim_id,'evidence_binding_fingerprint',learning.review_card_evidence_binding_fingerprint(card.evidence))
    FROM changed_claim changed JOIN learning.review_card card ON card.workspace_id=? AND card.claim_id=changed.claim_id
    UNION ALL
    SELECT 'REVIEW_CARD',card.id,card.workspace_id,card.version,'REVALIDATE_REVIEW_CARD','复习卡片的主张或证据绑定已发生变化',true,
      jsonb_build_object('card_id',card.id,'card_version',card.version,'status',card.status,'fingerprint',card.fingerprint,'claim_id',card.claim_id,'evidence_binding_fingerprint',learning.review_card_evidence_binding_fingerprint(card.evidence))
    FROM changed_tuple changed JOIN learning.review_card_evidence_selector version_selector ON version_selector.workspace_id=? AND version_selector.selector_kind='SOURCE_VERSION' AND version_selector.selector_id=changed.source_version_id
    JOIN learning.review_card_evidence_selector span_selector ON span_selector.workspace_id=? AND span_selector.card_id=version_selector.card_id AND span_selector.claim_id=version_selector.claim_id AND span_selector.selector_kind='SOURCE_SPAN' AND span_selector.selector_id=changed.source_span_id
    JOIN learning.review_card card ON card.workspace_id=? AND card.id=version_selector.card_id AND card.claim_id=version_selector.claim_id
    WHERE EXISTS (SELECT 1 FROM (SELECT element.item FROM jsonb_array_elements(CASE WHEN jsonb_typeof(card.evidence)='array' THEN card.evidence ELSE '[]'::jsonb END) element(item) LIMIT 128) evidence WHERE jsonb_typeof(evidence.item)='object' AND evidence.item->>'schema_version'='review-evidence/v1' AND learning.try_review_evidence_uuid(evidence.item->>'source_version_id')=changed.source_version_id AND learning.try_review_evidence_uuid(evidence.item->>'source_span_id')=changed.source_span_id)
)
SELECT DISTINCT object_type,id::text,workspace_id::text,version,action,reason,requires_proposal,owner_binding::text FROM impact ORDER BY object_type,id,version,owner_binding::text LIMIT ?`

func gormGetTimelineEventBySource(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, sourceEventRef string, lock bool) (domain.KnowledgeEvent, bool, error) {
	statement := `SELECT ` + timelineEventColumns + ` FROM ops.knowledge_event WHERE workspace_id=? AND source_event_ref=?`
	if lock {
		statement += ` FOR UPDATE`
	}
	row, err := gormKnowledgeRawRow(ctx, database, statement, string(workspaceID), sourceEventRef)
	if err != nil {
		return domain.KnowledgeEvent{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeTimelineUnavailable)
	}
	event, err := gormScanTimelineEvent(row)
	if gormKnowledgeNoRows(err) {
		return domain.KnowledgeEvent{}, false, nil
	}
	if err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	return event, true, nil
}

func (repository *GORMRepository) GetImpactReport(ctx context.Context, workspaceID, sourceEventID foundation.ID, analysisVersion domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ImpactReport{}, false, err
	}
	if !timelineValidID(workspaceID) || !timelineValidID(sourceEventID) || !validImpactAnalysisVersion(analysisVersion) {
		return domain.ImpactReport{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact report source identity is invalid"))
	}
	return gormGetImpactReport(ctx, repository.database, workspaceID, sourceEventID, analysisVersion)
}

func gormGetImpactReport(ctx context.Context, database *gorm.DB, workspaceID, sourceEventID foundation.ID, analysisVersion domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error) {
	row, err := gormKnowledgeRawRow(ctx, database, impactReportSelect+` WHERE report.workspace_id=? AND report.source_event_id=? AND report.analysis_version=?`, string(workspaceID), string(sourceEventID), string(analysisVersion))
	if err != nil {
		return domain.ImpactReport{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	report, err := gormScanImpactReport(row)
	if gormKnowledgeNoRows(err) {
		return domain.ImpactReport{}, false, nil
	}
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	return report, true, nil
}

func (repository *GORMRepository) ImpactAnalysisReady(ctx context.Context, workspaceID foundation.ID) (bool, error) {
	if err := repository.ready(ctx); err != nil {
		return false, err
	}
	if !timelineValidID(workspaceID) {
		return false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact analysis readiness identity is invalid"))
	}
	return gormImpactAnalysisReady(ctx, repository.database, workspaceID)
}

func gormImpactAnalysisReady(ctx context.Context, database *gorm.DB, workspaceID foundation.ID) (bool, error) {
	row, err := gormKnowledgeRawRow(ctx, database, `SELECT EXISTS(SELECT 1 FROM core.schema_meta AS schema JOIN learning.artifact_citation_selector_backfill AS marker ON marker.workspace_id=? WHERE schema.key='timeline_impact' AND schema.value='m7-v2' AND marker.contract_version='artifact-citation-selector-backfill/v1' AND marker.status='COMPLETED')`, string(workspaceID))
	if err != nil {
		return false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	var ready bool
	if err := row.Scan(&ready); err != nil {
		return false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	return ready, nil
}

func (repository *GORMRepository) GetImpactReportByID(ctx context.Context, workspaceID, reportID foundation.ID) (domain.ImpactReport, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ImpactReport{}, err
	}
	if !timelineValidID(workspaceID) || !timelineValidID(reportID) {
		return domain.ImpactReport{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact report identity is invalid"))
	}
	row, err := gormKnowledgeRawRow(ctx, repository.database, impactReportSelect+` WHERE report.workspace_id=? AND report.id=?`, string(workspaceID), string(reportID))
	if err != nil {
		return domain.ImpactReport{}, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	report, err := gormScanImpactReport(row)
	if gormKnowledgeNoRows(err) {
		return domain.ImpactReport{}, notFound(domain.ErrorCodeImpactNotFound, err)
	}
	return report, err
}

func (repository *GORMRepository) SaveImpactReport(ctx context.Context, report domain.ImpactReport) (domain.ImpactReport, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ImpactReport{}, false, err
	}
	var persisted domain.ImpactReport
	var replayed bool
	err := repository.within(ctx, foundation.TransactionOptions{}, domain.ErrorCodeImpactUnavailable, domain.ErrorCodeImpactUnavailable, classifyGORMKnowledge, func(callbackCtx context.Context, _ foundation.TransactionScope, transaction *gorm.DB) error {
		var saveErr error
		persisted, replayed, saveErr = gormSaveImpactReport(callbackCtx, transaction, report)
		return saveErr
	})
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	return persisted, replayed, nil
}

func (repository *GORMRepository) SaveImpactReportWithAudit(ctx context.Context, report domain.ImpactReport, idempotencyKey string, _ knowledgeapp.ImpactAuditPort) (domain.ImpactReport, bool, error) {
	return domain.ImpactReport{}, false, impactUnavailable(errors.New("legacy impact audit port is unavailable for GORM persistence"))
}

// SaveImpactReportWithScopedAudit persists the report and audit record inside
// one opaque scope. The scoped port cannot be replaced with the legacy any Tx API.
func (repository *GORMRepository) SaveImpactReportWithScopedAudit(ctx context.Context, report domain.ImpactReport, idempotencyKey string, audit knowledgeapp.ScopedImpactAuditPort) (domain.ImpactReport, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ImpactReport{}, false, err
	}
	if isNilKnowledgeGORMDependency(audit) {
		return domain.ImpactReport{}, false, impactUnavailable(errors.New("scoped impact audit is unavailable"))
	}
	report.GeneratedAt = domain.CanonicalTimelineTime(report.GeneratedAt)
	report.CreatedAt = domain.CanonicalTimelineTime(report.CreatedAt)
	if err := validateImpactReportIntegrity(report); err != nil {
		return domain.ImpactReport{}, false, err
	}
	var persisted domain.ImpactReport
	var replayed bool
	err := repository.within(ctx, foundation.TransactionOptions{}, domain.ErrorCodeImpactUnavailable, domain.ErrorCodeImpactUnavailable, classifyGORMKnowledge,
		func(callbackCtx context.Context, scope foundation.TransactionScope, transaction *gorm.DB) error {
			// Legacy service replay audits an already durable report without reapplying
			// the mutable v2 readiness gate. First writes still go through gormSaveImpactReport.
			existing, found, loadErr := gormGetImpactReport(callbackCtx, transaction, report.WorkspaceID, report.SourceEventID, report.EffectiveAnalysisVersion())
			if loadErr != nil {
				return loadErr
			}
			if found {
				if !sameImpactReport(existing, report) {
					return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeImpactConflict, false, errors.New("impact source event is bound to a different report"))
				}
				persisted, replayed = existing, true
			} else {
				var saveErr error
				persisted, replayed, saveErr = gormSaveImpactReport(callbackCtx, transaction, report)
				if saveErr != nil {
					return saveErr
				}
			}
			record, recordErr := knowledgeapp.BuildImpactAuditRecord(persisted, idempotencyKey, replayed)
			if recordErr != nil {
				return recordErr
			}
			return audit.RecordImpactAnalysisScoped(callbackCtx, scope, record)
		})
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	return persisted, replayed, nil
}

func gormSaveImpactReport(ctx context.Context, database *gorm.DB, report domain.ImpactReport) (domain.ImpactReport, bool, error) {
	report.GeneratedAt, report.CreatedAt = domain.CanonicalTimelineTime(report.GeneratedAt), domain.CanonicalTimelineTime(report.CreatedAt)
	if err := validateImpactReportIntegrity(report); err != nil {
		return domain.ImpactReport{}, false, err
	}
	analysisVersion := report.EffectiveAnalysisVersion()
	if analysisVersion == domain.ImpactAnalysisVersionV2 {
		ready, err := gormImpactAnalysisReady(ctx, database, report.WorkspaceID)
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
	var supersedes any
	if report.SupersedesReportID != nil {
		supersedes = string(*report.SupersedesReportID)
	}
	row, err := gormKnowledgeRawRow(ctx, database, `WITH report_object AS MATERIALIZED (SELECT value FROM jsonb_array_elements(?::jsonb) AS item(value))
	INSERT INTO ops.impact_report(id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,source_event_version,fingerprint,error_code,stale_reason,version,created_at,analysis_version,supersedes_report_id)
	SELECT ?,?,?,?,?,?::jsonb,?::jsonb,?,?,?,?,?,?,?,?,?
	FROM ops.knowledge_event event WHERE event.workspace_id=? AND event.id=? AND event.event_version::bigint=? AND event.source_event_ref=?
	AND (?='impact-analysis/v1' OR (?::uuid IS NULL AND NOT EXISTS (SELECT 1 FROM ops.impact_report predecessor WHERE predecessor.workspace_id=? AND predecessor.source_event_id=? AND predecessor.analysis_version='impact-analysis/v1')) OR EXISTS (SELECT 1 FROM ops.impact_report predecessor WHERE predecessor.workspace_id=? AND predecessor.source_event_id=? AND predecessor.analysis_version='impact-analysis/v1' AND predecessor.id=?::uuid))
	AND NOT EXISTS (SELECT 1 FROM report_object object WHERE object.value->>'type'='ARTIFACT' AND NOT EXISTS (SELECT 1 FROM learning.artifact artifact JOIN learning.artifact_revision revision ON revision.workspace_id=artifact.workspace_id AND revision.artifact_id=artifact.id AND revision.id=artifact.current_revision_id AND revision.domain_schema_version='artifact-revision/v1' WHERE artifact.workspace_id=? AND artifact.domain_schema_version='artifact/v1' AND artifact.id=(object.value->>'id')::uuid AND artifact.version=(object.value->>'version')::bigint AND artifact.id=(object.value->'artifact_binding'->>'artifact_id')::uuid AND artifact.version=(object.value->'artifact_binding'->>'artifact_version')::bigint AND revision.id=(object.value->'artifact_binding'->>'revision_id')::uuid AND revision.revision_no=(object.value->'artifact_binding'->>'revision_no')::bigint AND revision.content_hash=object.value->'artifact_binding'->>'content_hash'))
	AND NOT EXISTS (SELECT 1 FROM report_object object WHERE object.value->>'type'='REVIEW_CARD' AND NOT EXISTS (SELECT 1 FROM learning.review_card card WHERE card.workspace_id=? AND card.id=(object.value->>'id')::uuid AND card.version=(object.value->>'version')::bigint AND card.id=(object.value->'review_card_binding'->>'card_id')::uuid AND card.version=(object.value->'review_card_binding'->>'card_version')::bigint AND card.status=object.value->'review_card_binding'->>'status' AND card.fingerprint=object.value->'review_card_binding'->>'fingerprint' AND card.claim_id=(object.value->'review_card_binding'->>'claim_id')::uuid AND learning.review_card_evidence_binding_fingerprint(card.evidence)=object.value->'review_card_binding'->>'evidence_binding_fingerprint'))
	ON CONFLICT(workspace_id,source_event_id,analysis_version) DO NOTHING
	RETURNING id::text,workspace_id::text,source_event_id::text,status,objects::text,summary::text,generated_at,schema_version,analysis_version,supersedes_report_id::text,NULL::text,source_event_version,fingerprint,error_code,stale_reason,version,created_at`,
		knowledgeJSONB(objects), string(report.ID), string(report.WorkspaceID), string(report.SourceEventID), string(report.Status), knowledgeJSONB(objects), knowledgeJSONB(summary), report.GeneratedAt.UTC(), report.SchemaVersion(), report.SourceVersion, report.Fingerprint, nullableText(report.ErrorCode), nullableText(report.StaleReason), report.Version, report.CreatedAt.UTC(), string(analysisVersion), supersedes,
		string(report.WorkspaceID), string(report.SourceEventID), report.SourceVersion, report.SourceEventRef, string(analysisVersion), supersedes, string(report.WorkspaceID), string(report.SourceEventID), string(report.WorkspaceID), string(report.SourceEventID), supersedes, string(report.WorkspaceID), string(report.WorkspaceID))
	if err != nil {
		return domain.ImpactReport{}, false, classifyGORMKnowledge(ctx, err, domain.ErrorCodeImpactUnavailable)
	}
	persisted, err := gormScanImpactReportRow(row, report.SourceEventRef)
	if gormKnowledgeNoRows(err) {
		existing, found, loadErr := gormGetImpactReport(ctx, database, report.WorkspaceID, report.SourceEventID, analysisVersion)
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

func gormScanTimelineEvent(row interface{ Scan(...any) error }) (domain.KnowledgeEvent, error) {
	event, err := scanTimelineEvent(row)
	if gormKnowledgeNoRows(err) {
		return domain.KnowledgeEvent{}, sql.ErrNoRows
	}
	return event, err
}

func gormScanImpactReport(row interface{ Scan(...any) error }) (domain.ImpactReport, error) {
	report, err := scanImpactReport(row)
	if gormKnowledgeNoRows(err) {
		return domain.ImpactReport{}, sql.ErrNoRows
	}
	return report, err
}

func gormScanImpactReportRow(row interface{ Scan(...any) error }, sourceEventRef string) (domain.ImpactReport, error) {
	report, err := scanImpactReportRow(row, sourceEventRef)
	if gormKnowledgeNoRows(err) {
		return domain.ImpactReport{}, sql.ErrNoRows
	}
	return report, err
}

// gormTextArray is deliberately a driver.Valuer rather than a raw slice so a
// Postgres array stays one bound value under GORM.
type gormTextArray []string

func (value gormTextArray) Value() (driver.Value, error) {
	return pq.Array([]string(value)).Value()
}
