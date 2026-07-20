package postgres

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// RelationEvidenceWindow 返回 Relation 自有证据的稳定有界窗口，不读取来源正文。
func (repository *Repository) RelationEvidenceWindow(ctx context.Context, workspaceID, relationID foundation.ID) (graphapp.RelationEvidenceResultWindow, error) {
	if repository == nil || repository.db == nil {
		return graphapp.RelationEvidenceResultWindow{}, unavailable(errors.New("graph repository is unavailable"))
	}
	rows, err := repository.db.Query(ctx, relationEvidenceWindowSQL, string(workspaceID), string(relationID), graphapp.MaxResultWindowItems+1)
	if err != nil {
		return graphapp.RelationEvidenceResultWindow{}, classify(err)
	}
	defer rows.Close()
	items := make([]graphdomain.RelationEvidenceItem, 0, graphapp.MaxResultWindowItems)
	expectedCount := -1
	for rows.Next() {
		count, item, scanErr := scanRelationEvidenceItem(rows)
		if scanErr != nil {
			return graphapp.RelationEvidenceResultWindow{}, inconsistent(scanErr)
		}
		if expectedCount >= 0 && expectedCount != count {
			return graphapp.RelationEvidenceResultWindow{}, inconsistent(errors.New("relation evidence owner count changed within one result"))
		}
		expectedCount = count
		if item != nil {
			items = append(items, *item)
		}
	}
	if err := rows.Err(); err != nil {
		return graphapp.RelationEvidenceResultWindow{}, classify(err)
	}
	if expectedCount < 0 {
		return graphapp.RelationEvidenceResultWindow{}, notFound(graphdomain.ErrorCodeRelationNotFound, errors.New("graph relation does not exist"))
	}
	truncated := len(items) > graphapp.MaxResultWindowItems
	if truncated {
		items = items[:graphapp.MaxResultWindowItems]
	}
	if (expectedCount <= graphapp.MaxResultWindowItems && len(items) != expectedCount) || (expectedCount > graphapp.MaxResultWindowItems && !truncated) {
		return graphapp.RelationEvidenceResultWindow{}, inconsistent(errors.New("relation evidence count is inconsistent"))
	}
	window := graphapp.RelationEvidenceResultWindow{Items: items, Truncated: truncated}
	if truncated {
		window.Reason = "RESULT_WINDOW_LIMIT"
	}
	return window, nil
}

const relationEvidenceWindowSQL = `
WITH owner AS (
    SELECT (SELECT COUNT(*)::int FROM core.relation_evidence counted WHERE counted.workspace_id=r.workspace_id AND counted.relation_id=r.id) AS expected_count
    FROM core.relation r WHERE r.workspace_id=$1 AND r.id=$2 AND r.status IN ('CONFIRMED','STALE')
)
SELECT owner.expected_count,e.id::text,e.workspace_id::text,e.relation_id::text,e.source_version_id::text,e.source_span_id::text,
       e.reason,e.applicability,e.applicability_schema_version,e.applicability_hash,e.confirmation_method,e.confirmed_by,e.created_at
FROM owner
LEFT JOIN LATERAL (
    SELECT evidence.* FROM core.relation_evidence evidence
    WHERE evidence.workspace_id=$1 AND evidence.relation_id=$2
    ORDER BY evidence.created_at,evidence.id LIMIT $3
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
