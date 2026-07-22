// Package postgres implements batched canonical fact reads for Health detectors.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
)

const (
	detectorCursorSchema = "health-detector-cursor/v1"
	detectorLegacyCursor = "__legacy__"
)

// detectorCursor 是 detector keyset 的最后一项。
// v1 同时保存 object_type 与 id，避免不同类型复用同一 UUID 时相互跳过。
type detectorCursor struct {
	targetType string
	targetID   string
}

func decodeDetectorCursor(raw string) (detectorCursor, error) {
	if raw == "" {
		return detectorCursor{}, nil
	}
	if strings.HasPrefix(raw, detectorCursorSchema+"|") {
		parts := strings.Split(raw, "|")
		if len(parts) != 3 || !validDetectorTargetType(parts[1]) {
			return detectorCursor{}, errors.New("health detector cursor is invalid")
		}
		id, err := foundation.ParseID(parts[2])
		if err != nil {
			return detectorCursor{}, errors.New("health detector cursor is invalid")
		}
		return detectorCursor{targetType: parts[1], targetID: string(id)}, nil
	}
	// 兼容旧版只包含 target_id 的 checkpoint；新页面不会再签发此格式。
	id, err := foundation.ParseID(raw)
	if err != nil {
		return detectorCursor{}, errors.New("health detector cursor is invalid")
	}
	return detectorCursor{targetType: detectorLegacyCursor, targetID: string(id)}, nil
}

func encodeDetectorCursor(targetType, targetID string) (string, error) {
	if !validDetectorTargetType(targetType) {
		return "", errors.New("health detector cursor target type is invalid")
	}
	id, err := foundation.ParseID(targetID)
	if err != nil {
		return "", errors.New("health detector cursor target id is invalid")
	}
	return detectorCursorSchema + "|" + targetType + "|" + string(id), nil
}

func validDetectorTargetType(value string) bool {
	switch domain.ObjectType(value) {
	case domain.ObjectTypeTopic, domain.ObjectTypeClaim, domain.ObjectTypeRelation,
		domain.ObjectTypeConflict, domain.ObjectTypeSourceVersion, domain.ObjectTypeIndexVersion:
		return true
	default:
		return false
	}
}

// DB 是 detector 只读 adapter 所需的最小 PostgreSQL 查询边界。
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// FactReader 从 Knowledge、Ingestion 和 Retrieval canonical 表批量读取 detector facts。
type FactReader struct {
	db         DB
	membership healthapp.SmartCollectionMembershipPort
}

// NewFactReader 构造 detector reader；不会在 Health 模块创建知识写入路径。
func NewFactReader(db DB, memberships ...healthapp.SmartCollectionMembershipPort) (*FactReader, error) {
	if db == nil {
		return nil, errors.New("health detector database is nil")
	}
	reader := &FactReader{db: db}
	if len(memberships) > 0 {
		reader.membership = memberships[0]
	}
	return reader, nil
}

var _ healthapp.FactReader = (*FactReader)(nil)

// Find 每次只执行一个 detector 的有界批量查询，游标使用 object_type/id keyset。
func (reader *FactReader) Find(ctx context.Context, detectorID string, request healthapp.PageRequest) (healthapp.Page, error) {
	if reader == nil || reader.db == nil {
		return healthapp.Page{}, healthapp.ErrDetectorUnavailable
	}
	if request.Descriptor.ID != detectorID || request.Descriptor.Version != detector.DefaultDetectorVersion ||
		!request.Descriptor.Available || !request.Descriptor.SupportsScope(request.Scope.Type) ||
		!detector.DefaultSupportsScope(detectorID, request.Scope.Type) {
		return healthapp.Page{}, healthapp.ErrDetectorUnavailable
	}
	if request.BatchSize < 1 {
		request.BatchSize = domain.DefaultDetectorBatchSize
	}
	if request.BatchSize > domain.MaxDetectorBatchSize {
		request.BatchSize = domain.MaxDetectorBatchSize
	}
	var membership scanScopeMembership
	var binding healthapp.SmartCollectionBinding
	if request.Scope.Type == domain.ScanScopeTypeSmartCollection {
		var err error
		membership, binding, err = loadSmartCollectionScope(ctx, reader.membership, request.Scope.WorkspaceID, domain.ScanScope{
			Type: domain.ScanScopeTypeSmartCollection, Ref: request.Scope.Ref, Version: request.Scope.Version,
			SchemaVersion: "health-scope/smart-collection/v1", Hash: request.Scope.Hash, ReadModelRevision: request.Scope.ReadModelRevision,
			ExactCount: request.Scope.ExactCount,
		})
		if err != nil {
			return healthapp.Page{}, err
		}
	}
	query, args, err := detectorQuery(detectorID, request, membership)
	if err != nil {
		return healthapp.Page{}, err
	}
	rows, err := reader.db.Query(ctx, query, args...)
	if err != nil {
		return healthapp.Page{}, err
	}
	defer rows.Close()
	page := healthapp.Page{}
	var lastType, lastID string
	for rows.Next() {
		var workspaceID, targetType, targetID, summary, severity string
		var version int64
		if err := rows.Scan(&workspaceID, &targetType, &targetID, &version, &summary, &severity); err != nil {
			return healthapp.Page{}, err
		}
		if workspaceID != string(request.Scope.WorkspaceID) {
			return healthapp.Page{}, errors.New("health detector crossed workspace boundary")
		}
		page.Findings = append(page.Findings, healthapp.FindingFact{Target: domain.ObjectRef{Type: domain.ObjectType(targetType), ID: foundationID(targetID)}, TargetVersion: version, Severity: domain.Severity(severity), Summary: summary})
		lastType, lastID = targetType, targetID
	}
	if err := rows.Err(); err != nil {
		return healthapp.Page{}, err
	}
	if request.Scope.Type == domain.ScanScopeTypeSmartCollection {
		if err := revalidateSmartCollectionScope(ctx, reader.membership, binding); err != nil {
			return healthapp.Page{}, err
		}
	}
	page.Processed = int64(len(page.Findings))
	if len(page.Findings) < request.BatchSize {
		page.Complete = true
	} else {
		page.NextCursor, err = encodeDetectorCursor(lastType, lastID)
		if err != nil {
			return healthapp.Page{}, err
		}
	}
	return page, nil
}

// foundationID 避免在 SQL adapter 中泄漏 pgx 类型；UUID 规范由 domain observation 校验。
func foundationID(value string) foundation.ID { return foundation.ID(value) }

func detectorQuery(id string, request healthapp.PageRequest, memberships ...scanScopeMembership) (string, []any, error) {
	cursor, err := decodeDetectorCursor(request.Cursor)
	if err != nil {
		return "", nil, err
	}
	base, threshold, err := detectorBaseQuery(id)
	if err != nil {
		return "", nil, err
	}
	membership := scanScopeMembership{}
	if len(memberships) > 0 {
		membership = memberships[0]
	}
	cursorID := any(nil)
	if cursor.targetID != "" {
		cursorID = cursor.targetID
	}
	args := []any{string(request.Scope.WorkspaceID), string(request.Scope.Type), string(request.Scope.Ref), cursor.targetType, request.BatchSize, membership.TopicIDs, membership.ClaimIDs, cursorID}
	if threshold {
		value := request.LowConfidenceThreshold
		if value <= 0 || value >= 1 {
			return "", nil, errors.New("low confidence threshold is outside detector bounds")
		}
		args = append(args, value)
	}
	scopePredicate := scanScopePredicate(detectorFindingScopeTarget)
	query := fmt.Sprintf(`SELECT workspace_id,target_type,target_id,target_version,summary,severity
FROM (%s) AS findings(workspace_id,target_type,target_id,target_version,summary,severity)
WHERE workspace_id=$1
  AND %s
  AND ($4='' OR ($4='__legacy__' AND target_id>$8::uuid) OR
	   ($4<>'__legacy__' AND (target_type>$4 OR (target_type=$4 AND target_id>$8::uuid))))
ORDER BY target_type,target_id
LIMIT $5`, base, scopePredicate)
	return query, args, nil
}

func detectorBaseQuery(id string) (string, bool, error) {
	switch id {
	case detector.DetectorOrphan:
		return `SELECT t.workspace_id,'TOPIC' AS target_type,t.id AS target_id,t.version,
 'active topic has no confirmed BELONGS_TO membership' AS summary,'MEDIUM' AS severity
 FROM core.topic t
 WHERE t.status='ACTIVE' AND NOT EXISTS (
   SELECT 1 FROM core.relation r WHERE r.workspace_id=t.workspace_id AND r.status='CONFIRMED'
     AND r.relation_type='BELONGS_TO' AND r.target_node_type='TOPIC' AND r.target_node_id=t.id)
 UNION ALL
 SELECT c.workspace_id,'CLAIM',c.id,c.version,
 'formal claim has no confirmed BELONGS_TO membership','MEDIUM'
 FROM core.claim c
 WHERE c.status IN ('CONFIRMED','DISPUTED') AND NOT EXISTS (
   SELECT 1 FROM core.relation r WHERE r.workspace_id=c.workspace_id AND r.status='CONFIRMED'
     AND r.relation_type='BELONGS_TO' AND r.source_node_type='CLAIM' AND r.source_node_id=c.id)`, false, nil
	case detector.DetectorDuplicate:
		return `SELECT workspace_id,'RELATION',id,version,
 'confirmed DUPLICATES relation is a duplicate fact','HIGH'
 FROM core.relation WHERE status='CONFIRMED' AND relation_type='DUPLICATES'`, false, nil
	case detector.DetectorConflict:
		return `SELECT workspace_id,'CONFLICT',id,version,
 summary,
 CASE severity WHEN 'CRITICAL' THEN 'CRITICAL' WHEN 'HIGH' THEN 'HIGH' WHEN 'MEDIUM' THEN 'MEDIUM' ELSE 'LOW' END
 FROM core.conflict WHERE status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE')`, false, nil
	case detector.DetectorStale:
		return `SELECT workspace_id,'RELATION',id,version,
 'relation is marked STALE and requires review','MEDIUM'
 FROM core.relation WHERE status='STALE'`, false, nil
	case detector.DetectorMissingSource:
		return `SELECT c.workspace_id,'CLAIM',c.id,c.version,
 'confirmed claim has no supporting source','HIGH'
 FROM core.claim c WHERE c.status='CONFIRMED' AND NOT EXISTS (
   SELECT 1 FROM core.claim_source cs WHERE cs.workspace_id=c.workspace_id AND cs.claim_id=c.id AND cs.support_type='SUPPORTS')
 UNION ALL
 SELECT r.workspace_id,'RELATION',r.id,r.version,
 'confirmed relation has no evidence','HIGH'
 FROM core.relation r WHERE r.status='CONFIRMED' AND NOT EXISTS (
   SELECT 1 FROM core.relation_evidence re WHERE re.workspace_id=r.workspace_id AND re.relation_id=r.id)`, false, nil
	case detector.DetectorLowConfidence:
		return `SELECT workspace_id,'CLAIM',id,version,
 'claim confidence is below the configured detector threshold','LOW'
	 FROM core.claim WHERE confidence_score IS NOT NULL AND confidence_score < $9
 UNION ALL
 SELECT workspace_id,'RELATION',id,version,
 'relation confidence is below the configured detector threshold','LOW'
	 FROM core.relation WHERE confidence_score IS NOT NULL AND confidence_score < $9`, true, nil
	case detector.DetectorBrokenReference:
		return `SELECT c.workspace_id,'CLAIM',c.id,c.version,
 'claim source provenance cannot be verified','CRITICAL'
 FROM core.claim c
 WHERE EXISTS (
   SELECT 1 FROM core.claim_source cs
   LEFT JOIN core.source_version sv ON sv.id=cs.source_version_id
   LEFT JOIN ingestion.source_span ss ON ss.id=cs.source_span_id
   LEFT JOIN core.source s ON s.id=sv.source_id
   WHERE cs.claim_id=c.id AND cs.workspace_id=c.workspace_id
     AND (sv.id IS NULL OR ss.id IS NULL OR s.id IS NULL OR sv.source_id IS NULL
       OR s.workspace_id<>c.workspace_id OR ss.workspace_id<>c.workspace_id)
 )
 UNION ALL
 SELECT r.workspace_id,'RELATION',r.id,r.version,
 'relation evidence provenance cannot be verified','CRITICAL'
 FROM core.relation r
 WHERE EXISTS (
   SELECT 1 FROM core.relation_evidence re
   LEFT JOIN core.source_version sv ON sv.id=re.source_version_id
   LEFT JOIN ingestion.source_span ss ON ss.id=re.source_span_id
   LEFT JOIN core.source s ON s.id=sv.source_id
   WHERE re.relation_id=r.id AND re.workspace_id=r.workspace_id
     AND (sv.id IS NULL OR ss.id IS NULL OR s.id IS NULL OR sv.source_id IS NULL
       OR s.workspace_id<>r.workspace_id OR ss.workspace_id<>r.workspace_id)
 )`, false, nil
	case detector.DetectorIndexError:
		return `SELECT iv.workspace_id,'INDEX_VERSION',iv.id,iv.version,
 CASE WHEN iv.status='failed' THEN 'index version is failed'
      ELSE 'index projection contains failed lexical or vector work' END,'CRITICAL'
 FROM retrieval.index_version iv
 LEFT JOIN retrieval.chunk_projection cp ON cp.index_version_id=iv.id AND cp.workspace_id=iv.workspace_id
 WHERE iv.status='failed' OR cp.lexical_status='failed' OR cp.vector_status IN ('failed','skipped_oversized')
 GROUP BY iv.workspace_id,iv.id,iv.version,iv.status`, false, nil
	case detector.DetectorSupersededUsage:
		return `SELECT c.workspace_id,'CLAIM',c.id,c.version,
 'claim still consumes a superseded source version','MEDIUM'
 FROM core.claim c
 WHERE EXISTS (
   SELECT 1
   FROM core.claim_source cs
   JOIN core.source_version sv ON sv.id=cs.source_version_id
   JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=c.workspace_id
   WHERE cs.claim_id=c.id AND cs.workspace_id=c.workspace_id
     AND sv.captured_at < (SELECT max(latest.captured_at) FROM core.source_version latest WHERE latest.source_id=sv.source_id)
 )
 UNION ALL
 SELECT r.workspace_id,'RELATION',r.id,r.version,
 'relation still consumes a superseded source version','MEDIUM'
 FROM core.relation r
 WHERE EXISTS (
   SELECT 1
   FROM core.relation_evidence re
   JOIN core.source_version sv ON sv.id=re.source_version_id
   JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=r.workspace_id
   WHERE re.relation_id=r.id AND re.workspace_id=r.workspace_id
     AND sv.captured_at < (SELECT max(latest.captured_at) FROM core.source_version latest WHERE latest.source_id=sv.source_id)
 )`, false, nil
	default:
		return "", false, fmt.Errorf("unknown health detector %q", id)
	}
}
