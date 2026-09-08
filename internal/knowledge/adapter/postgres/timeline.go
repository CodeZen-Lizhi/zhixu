package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const timelineEventColumns = `id::text,workspace_id::text,event_type,aggregate_type,aggregate_id::text,
source_event_ref,COALESCE(source_ref,''),event_version,schema_version,summary,payload::text,correlation::text,
operator_type,operator_id::text,owner_binding::text,occurred_at,created_at`

const impactReportSelect = `SELECT report.id::text,report.workspace_id::text,report.source_event_id::text,report.status,
report.objects::text,report.summary::text,report.generated_at,report.schema_version,report.analysis_version,
report.supersedes_report_id::text,successor.id::text,report.source_event_version,report.fingerprint,
report.error_code,report.stale_reason,report.version,report.created_at,event.source_event_ref,event.event_version
FROM ops.impact_report report JOIN ops.knowledge_event event
  ON event.id=report.source_event_id AND event.workspace_id=report.workspace_id
LEFT JOIN ops.impact_report successor
  ON successor.workspace_id=report.workspace_id AND successor.supersedes_report_id=report.id`

func scanTimelineEvent(row interface{ Scan(...any) error }) (domain.KnowledgeEvent, error) {
	var event domain.KnowledgeEvent
	var id, workspaceID, eventType, aggregateType string
	var aggregateID, operatorType, operatorID, ownerBinding *string
	var payload, correlation string
	if err := row.Scan(&id, &workspaceID, &eventType, &aggregateType, &aggregateID, &event.SourceEventRef, &event.SourceRef,
		&event.EventVersion, &event.SchemaVersion, &event.Summary, &payload, &correlation,
		&operatorType, &operatorID, &ownerBinding, &event.OccurredAt, &event.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		if errors.Is(err, sql.ErrNoRows) {
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
