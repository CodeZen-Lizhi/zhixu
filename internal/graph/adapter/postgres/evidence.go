package postgres

import (
	"errors"
	"net/url"
	"time"

	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const relationEvidenceWindowSQL = `
WITH owner AS (
    SELECT (SELECT COUNT(*)::int FROM core.relation_evidence counted WHERE counted.workspace_id=r.workspace_id AND counted.relation_id=r.id) AS expected_count
    FROM core.relation r WHERE r.workspace_id=(@p1) AND r.id=(@p2) AND r.status IN ('CONFIRMED','STALE')
)
SELECT owner.expected_count,e.id::text,e.workspace_id::text,e.relation_id::text,e.source_version_id::text,e.source_span_id::text,
       e.reason,e.applicability,e.applicability_schema_version,e.applicability_hash,e.confirmation_method,e.confirmed_by,e.created_at
FROM owner
LEFT JOIN LATERAL (
    SELECT evidence.* FROM core.relation_evidence evidence
    WHERE evidence.workspace_id=(@p1) AND evidence.relation_id=(@p2)
    ORDER BY evidence.created_at,evidence.id LIMIT (@p3)
) e ON true
ORDER BY e.created_at,e.id
`

func scanRelationEvidenceItem(row rowScanner) (int, *graphdomain.RelationEvidenceItem, error) {
	var expectedCount int
	var id, workspaceID, relationID, sourceVersionID, sourceSpanID, reason, schemaVersion, applicabilityHash *string
	var applicabilityRaw []byte
	var confirmationMethod, confirmedBy *string
	var createdAt *time.Time
	if err := row.Scan(&expectedCount, &id, &workspaceID, &relationID, &sourceVersionID, &sourceSpanID, &reason,
		&applicabilityRaw, &schemaVersion, &applicabilityHash, &confirmationMethod, &confirmedBy, &createdAt); err != nil {
		return 0, nil, err
	}
	if id == nil {
		return expectedCount, nil, nil
	}
	if workspaceID == nil || relationID == nil || sourceVersionID == nil || sourceSpanID == nil || reason == nil || schemaVersion == nil || applicabilityHash == nil || createdAt == nil {
		return 0, nil, errors.New("relation evidence projection contains partial nulls")
	}
	applicability, err := knowledge.ParseApplicability(applicabilityRaw)
	if err != nil || applicability.SchemaVersion != *schemaVersion || applicability.Hash != *applicabilityHash {
		return 0, nil, errors.New("relation evidence applicability projection is inconsistent")
	}
	item := graphdomain.RelationEvidenceItem{ID: idValue(*id), WorkspaceID: idValue(*workspaceID), RelationID: idValue(*relationID), Reason: *reason, CreatedAt: *createdAt}
	item.Provenance = knowledge.ProvenanceRef{WorkspaceID: item.WorkspaceID, SourceVersionID: idValue(*sourceVersionID), SourceSpanID: idValue(*sourceSpanID)}
	item.Applicability = applicability
	if confirmationMethod != nil || confirmedBy != nil {
		if confirmationMethod == nil || confirmedBy == nil {
			return 0, nil, errors.New("relation evidence confirmation projection is inconsistent")
		}
		item.Confirmation = &knowledge.Confirmation{Method: knowledge.ConfirmationMethod(*confirmationMethod), Reference: *confirmedBy}
	}
	workspacePath := url.PathEscape(string(item.WorkspaceID))
	versionPath := url.PathEscape(string(item.Provenance.SourceVersionID))
	spanPath := url.PathEscape(string(item.Provenance.SourceSpanID))
	item.SourceHref = "/api/v1/workspaces/" + workspacePath + "/source-versions/" + versionPath
	item.SpanHref = item.SourceHref + "/spans/" + spanPath
	return expectedCount, &item, nil
}
