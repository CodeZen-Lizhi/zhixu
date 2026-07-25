package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
occurred_at,created_at`

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
	var aggregateID any
	if event.AggregateID != nil {
		aggregateID = string(*event.AggregateID)
	}
	row := tx.QueryRow(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,schema_version,
summary,payload,correlation,occurred_at,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12::jsonb,$13,$14)
RETURNING `+timelineEventColumns,
		string(event.ID), string(event.WorkspaceID), string(event.EventType), string(event.AggregateType), aggregateID,
		event.SourceEventRef, nullableText(event.SourceRef), event.EventVersion, event.SchemaVersion, event.Summary,
		string(event.Payload), string(correlation), event.OccurredAt.UTC(), event.CreatedAt.UTC())
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

// ListImpactObjects 以一条 UNION 查询读取 Relation、Conflict 与 Health 下游，不执行写入。
func (repository *Repository) ListImpactObjects(ctx context.Context, event domain.KnowledgeEvent) ([]domain.ImpactObject, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return nil, timelineUnavailable(errors.New("impact repository is unavailable"))
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	if event.AggregateID == nil {
		return []domain.ImpactObject{}, nil
	}
	rows, err := repository.db.Query(ctx, `WITH impact AS (
	SELECT 'RELATION'::text AS object_type,r.id,r.workspace_id,r.version,
	       CASE WHEN r.relation_type='CONFLICTS_WITH' THEN 'RESOLVE_CONFLICT' ELSE 'REVIEW' END AS action,
	       CASE WHEN r.relation_type='CONFLICTS_WITH' THEN '关联冲突需要重新调查' ELSE '关联关系依赖已变化对象' END AS reason,
	       false AS requires_proposal
FROM core.relation r
WHERE r.workspace_id=$1 AND (
  ($2 IN ('TOPIC','CLAIM') AND ((r.source_node_type=$2 AND r.source_node_id=$3) OR (r.target_node_type=$2 AND r.target_node_id=$3)))
  OR ($2='RELATION' AND r.id=$3)
)
UNION ALL
	SELECT 'CONFLICT',c.id,c.workspace_id,c.version,'RESOLVE_CONFLICT','冲突结论或成员可能受影响',false
FROM core.conflict c
WHERE c.workspace_id=$1 AND (($2='CONFLICT' AND c.id=$3) OR ($2='TOPIC' AND c.topic_id=$3))
UNION ALL
SELECT 'HEALTH_ISSUE',h.id,h.workspace_id,h.version,'REFRESH_HEALTH','健康问题证据需要按新版本复核',false
FROM ops.health_issue h
WHERE h.workspace_id=$1 AND h.target_type=$2 AND h.target_id=$3
)
SELECT object_type,id::text,workspace_id::text,version,action,reason,requires_proposal
FROM impact ORDER BY object_type,id LIMIT $4`, string(event.WorkspaceID), string(event.AggregateType), string(*event.AggregateID), domain.MaxImpactObjects+1)
	if err != nil {
		return nil, timelineStorage(err)
	}
	defer rows.Close()
	objects := make([]domain.ImpactObject, 0)
	for rows.Next() {
		var object domain.ImpactObject
		var objectType, id, workspaceID, action string
		if err := rows.Scan(&objectType, &id, &workspaceID, &object.Version, &action, &object.Reason, &object.RequiresProposal); err != nil {
			return nil, timelineStorage(err)
		}
		object.Type, object.ID, object.WorkspaceID, object.Action = domain.ImpactObjectType(objectType), foundation.ID(id), foundation.ID(workspaceID), domain.ImpactAction(action)
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, timelineStorage(err)
	}
	if len(objects) > domain.MaxImpactObjects {
		return nil, timelineCorrupt(errors.New("impact object result exceeded limit"))
	}
	return dedupeImpactObjects(objects)
}

// GetImpactReport 按 source event 返回幂等报告。
func (repository *Repository) GetImpactReport(ctx context.Context, workspaceID, sourceEventID foundation.ID) (domain.ImpactReport, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || !timelineValidID(workspaceID) || !timelineValidID(sourceEventID) {
		return domain.ImpactReport{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New("impact report source identity is invalid"))
	}
	return getImpactReport(ctx, repository.db, workspaceID, sourceEventID)
}

func getImpactReport(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID, sourceEventID foundation.ID) (domain.ImpactReport, bool, error) {
	report, err := scanImpactReport(queryer.QueryRow(ctx, impactReportSelect+` WHERE report.workspace_id=$1 AND report.source_event_id=$2`, string(workspaceID), string(sourceEventID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ImpactReport{}, false, nil
	}
	return report, err == nil, err
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
		return domain.ImpactReport{}, false, timelineUnavailable(errors.New("impact repository is unavailable"))
	}
	return saveImpactReport(ctx, repository.db, report)
}

// SaveImpactReportWithAudit 在同一 PostgreSQL 事务中保存报告、Timeline Outbox 和请求 Audit。
func (repository *Repository) SaveImpactReportWithAudit(ctx context.Context, report domain.ImpactReport, idempotencyKey string, audit knowledgeapp.ImpactAuditPort) (domain.ImpactReport, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || isNilImpactAudit(audit) {
		return domain.ImpactReport{}, false, timelineUnavailable(errors.New("impact atomic persistence is unavailable"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.ImpactReport{}, false, timelineStorage(err)
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
		return domain.ImpactReport{}, false, timelineStorage(err)
	}
	return persisted, replayed, nil
}

func saveImpactReport(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, report domain.ImpactReport) (domain.ImpactReport, bool, error) {
	report.GeneratedAt = domain.CanonicalTimelineTime(report.GeneratedAt)
	report.CreatedAt = domain.CanonicalTimelineTime(report.CreatedAt)
	if err := domain.ValidateImpactReport(report); err != nil {
		return domain.ImpactReport{}, false, err
	}
	objects, err := json.Marshal(report.Objects)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	summary, err := json.Marshal(report.Summary)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	row := queryer.QueryRow(ctx, `INSERT INTO ops.impact_report(
id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,source_event_version,
fingerprint,error_code,stale_reason,version,created_at)
SELECT $1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14
FROM ops.knowledge_event event
WHERE event.workspace_id=$2 AND event.id=$3 AND event.event_version::bigint=$9 AND event.source_event_ref=$15
ON CONFLICT(workspace_id,source_event_id) DO NOTHING
RETURNING id::text,workspace_id::text,source_event_id::text,status,objects::text,summary::text,generated_at,
schema_version,source_event_version,fingerprint,error_code,stale_reason,version,created_at`,
		string(report.ID), string(report.WorkspaceID), string(report.SourceEventID), string(report.Status), string(objects), string(summary), report.GeneratedAt.UTC(),
		domain.ImpactReportSchemaVersion, report.SourceVersion, report.Fingerprint, nullableText(report.ErrorCode), nullableText(report.StaleReason), report.Version, report.CreatedAt.UTC(), report.SourceEventRef)
	persisted, err := scanImpactReportRow(row, report.SourceEventRef)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, loadErr := getImpactReport(ctx, queryer, report.WorkspaceID, report.SourceEventID)
		if loadErr != nil {
			return domain.ImpactReport{}, false, loadErr
		}
		if !found {
			return domain.ImpactReport{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeImpactConflict, false, errors.New("impact report source event binding is invalid"))
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
report.objects::text,report.summary::text,report.generated_at,report.schema_version,report.source_event_version,
report.fingerprint,report.error_code,report.stale_reason,report.version,report.created_at,event.source_event_ref,
event.event_version
FROM ops.impact_report report JOIN ops.knowledge_event event
  ON event.id=report.source_event_id AND event.workspace_id=report.workspace_id`

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
	var aggregateID *string
	var payload, correlation string
	if err := row.Scan(&id, &workspaceID, &eventType, &aggregateType, &aggregateID, &event.SourceEventRef, &event.SourceRef,
		&event.EventVersion, &event.SchemaVersion, &event.Summary, &payload, &correlation, &event.OccurredAt, &event.CreatedAt); err != nil {
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
	if err := json.Unmarshal([]byte(correlation), &event.Correlation); err != nil {
		return domain.KnowledgeEvent{}, timelineCorrupt(err)
	}
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
	if err := domain.ValidateImpactReport(report); err != nil {
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
	if err := domain.ValidateImpactReport(report); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	return report, nil
}

func scanImpactReportData(row interface{ Scan(...any) error }, sourceEventRef *string, sourceEventVersion *int64) (domain.ImpactReport, error) {
	var report domain.ImpactReport
	var id, workspaceID, sourceEventID, status, objects, summary, schemaVersion string
	var errorCode, staleReason *string
	destinations := []any{&id, &workspaceID, &sourceEventID, &status, &objects, &summary, &report.GeneratedAt, &schemaVersion, &report.SourceVersion, &report.Fingerprint, &errorCode, &staleReason, &report.Version, &report.CreatedAt}
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
		return domain.ImpactReport{}, timelineStorage(err)
	}
	if schemaVersion != domain.ImpactReportSchemaVersion {
		return domain.ImpactReport{}, timelineCorrupt(errors.New("impact report schema is unsupported"))
	}
	report.ID, report.WorkspaceID, report.SourceEventID, report.Status = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(sourceEventID), domain.ImpactReportStatus(status)
	if errorCode != nil {
		report.ErrorCode = *errorCode
	}
	if staleReason != nil {
		report.StaleReason = *staleReason
	}
	if err := json.Unmarshal([]byte(objects), &report.Objects); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	if err := json.Unmarshal([]byte(summary), &report.Summary); err != nil {
		return domain.ImpactReport{}, timelineCorrupt(err)
	}
	report.GeneratedAt = domain.CanonicalTimelineTime(report.GeneratedAt)
	report.CreatedAt = domain.CanonicalTimelineTime(report.CreatedAt)
	return report, nil
}

func sameTimelineEvent(left, right domain.KnowledgeEvent) bool {
	leftCorrelation, _ := json.Marshal(left.Correlation)
	rightCorrelation, _ := json.Marshal(right.Correlation)
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.EventType == right.EventType && left.AggregateType == right.AggregateType && sameOptionalID(left.AggregateID, right.AggregateID) && left.SourceEventRef == right.SourceEventRef && left.SourceRef == right.SourceRef && left.EventVersion == right.EventVersion && left.SchemaVersion == right.SchemaVersion && left.Summary == right.Summary && string(left.Payload) == string(right.Payload) && string(leftCorrelation) == string(rightCorrelation) && left.OccurredAt.Equal(right.OccurredAt) && left.CreatedAt.Equal(right.CreatedAt)
}

func sameImpactReport(left, right domain.ImpactReport) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SourceEventID == right.SourceEventID && left.SourceEventRef == right.SourceEventRef && left.SourceVersion == right.SourceVersion && left.Status == right.Status && left.Fingerprint == right.Fingerprint && left.ErrorCode == right.ErrorCode && left.StaleReason == right.StaleReason && left.Version == right.Version && reflect.DeepEqual(left.Objects, right.Objects) && reflect.DeepEqual(left.Summary, right.Summary)
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
			if result[len(result)-1] != object {
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

func timelineUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, err)
}

func timelineCorrupt(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeTimelineInconsistent, false, err)
}
