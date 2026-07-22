package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
)

// ReadDB 是 Health read adapter 所需的只读 PostgreSQL 查询边界。
type ReadDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ReadRepository 提供 Issue 列表、详情和 Summary 的 Workspace-scoped 读取。
type ReadRepository struct{ db ReadDB }

var _ healthapp.IssueReadPort = (*ReadRepository)(nil)

// ListIssues 让既有写 repository 复用同一只读 seam，避免 HTTP composition 产生第二套 adapter。
func (repository *IssueRepository) ListIssues(ctx context.Context, request healthapp.IssueListQuery) (healthapp.IssueListResult, error) {
	if repository == nil {
		return healthapp.IssueListResult{}, errors.New("health issue repository is nil")
	}
	return (&ReadRepository{db: repository.db}).ListIssues(ctx, request)
}

// GetIssueDetail 让既有 IssueRepository 复用 Workspace-scoped 详情读取。
func (repository *IssueRepository) GetIssueDetail(ctx context.Context, workspaceID, issueID foundation.ID) (healthapp.IssueDetail, error) {
	if repository == nil {
		return healthapp.IssueDetail{}, errors.New("health issue repository is nil")
	}
	return (&ReadRepository{db: repository.db}).GetIssueDetail(ctx, workspaceID, issueID)
}

// GetHealthSummary 让既有 IssueRepository 复用 Health Summary 读取。
func (repository *IssueRepository) GetHealthSummary(ctx context.Context, workspaceID foundation.ID) (healthapp.HealthSummary, error) {
	if repository == nil {
		return healthapp.HealthSummary{}, errors.New("health issue repository is nil")
	}
	return (&ReadRepository{db: repository.db}).GetHealthSummary(ctx, workspaceID)
}

// NewReadRepository 构造只读 Health repository。
func NewReadRepository(db ReadDB) (*ReadRepository, error) {
	if db == nil {
		return nil, errors.New("health read database is nil")
	}
	return &ReadRepository{db: db}, nil
}

// NewIssueReadRepository 是 NewReadRepository 的语义化别名，便于 HTTP composition 注入。
func NewIssueReadRepository(db ReadDB) (*ReadRepository, error) { return NewReadRepository(db) }

// ListIssues 使用 (updated_at DESC,id DESC) keyset 查询，SQL 始终绑定 Workspace。
func (repository *ReadRepository) ListIssues(ctx context.Context, request healthapp.IssueListQuery) (healthapp.IssueListResult, error) {
	if repository == nil || repository.db == nil || !validID(request.WorkspaceID) || request.Limit < 1 || request.Limit > healthapp.MaxIssueListLimit {
		return healthapp.IssueListResult{}, errors.New("health issue list request is invalid")
	}
	args := []any{string(request.WorkspaceID), stringSlice(request.Statuses), stringSlice(request.Severities), stringSlice(request.Types)}
	query := `SELECT id::text,workspace_id::text,type,target_type,target_id::text,detector_id,detector_version,severity,evidence_summary,status,
 first_detected_at,last_detected_at,last_verified_at,version,updated_at
 FROM ops.health_issue
 WHERE workspace_id=$1
	   AND (COALESCE(cardinality($2::text[]),0) = 0 OR status=ANY($2::text[]))
	   AND (COALESCE(cardinality($3::text[]),0) = 0 OR severity=ANY($3::text[]))
	   AND (COALESCE(cardinality($4::text[]),0) = 0 OR type=ANY($4::text[]))`
	if request.After != nil {
		query += ` AND (updated_at,id) < ($5,$6)`
		args = append(args, request.After.UpdatedAt.UTC(), string(request.After.ID), request.Limit+1)
		query += ` ORDER BY updated_at DESC,id DESC LIMIT $7`
	} else {
		args = append(args, request.Limit+1)
		query += ` ORDER BY updated_at DESC,id DESC LIMIT $5`
	}
	rows, err := repository.db.Query(ctx, query, args...)
	if err != nil {
		return healthapp.IssueListResult{}, err
	}
	defer rows.Close()
	result := healthapp.IssueListResult{}
	for rows.Next() {
		var item healthapp.IssueListItem
		var id, workspaceID, issueType, targetType, targetID, detectorID, detectorVersion, severity, status string
		if err := rows.Scan(&id, &workspaceID, &issueType, &targetType, &targetID, &detectorID, &detectorVersion, &severity, &item.EvidenceSummary, &status, &item.FirstDetectedAt, &item.LastDetectedAt, &item.LastVerifiedAt, &item.Version, &item.UpdatedAt); err != nil {
			return healthapp.IssueListResult{}, err
		}
		if workspaceID != string(request.WorkspaceID) {
			return healthapp.IssueListResult{}, errors.New("health issue list crossed workspace boundary")
		}
		item.ID, item.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
		item.Type, item.Target = domain.IssueType(issueType), domain.ObjectRef{Type: domain.ObjectType(targetType), ID: foundation.ID(targetID)}
		item.DetectorID, item.DetectorVersion = detectorID, detectorVersion
		item.Severity, item.Status = domain.Severity(severity), domain.IssueStatus(status)
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return healthapp.IssueListResult{}, err
	}
	if len(result.Items) > request.Limit {
		result.HasMore = true
		last := result.Items[request.Limit-1]
		result.Next = &healthapp.IssueListPosition{UpdatedAt: last.UpdatedAt, ID: last.ID}
		result.Items = result.Items[:request.Limit]
	}
	return result, nil
}

// GetIssueDetail 返回当前 Issue 与完整 observation/evidence/decision 历史。
func (repository *ReadRepository) GetIssueDetail(ctx context.Context, workspaceID, issueID foundation.ID) (healthapp.IssueDetail, error) {
	if repository == nil || repository.db == nil || !validID(workspaceID) || !validID(issueID) {
		return healthapp.IssueDetail{}, errors.New("health issue detail request is invalid")
	}
	issue, found, err := repository.loadCurrentIssue(ctx, workspaceID, issueID)
	if err != nil {
		return healthapp.IssueDetail{}, err
	}
	if !found {
		return healthapp.IssueDetail{}, foundation.NewError(foundation.ErrorNotFound, "HEALTH_NOT_FOUND", false, pgx.ErrNoRows)
	}
	observations, err := repository.loadObservations(ctx, workspaceID, issueID)
	if err != nil {
		return healthapp.IssueDetail{}, err
	}
	decisions, err := repository.loadDecisions(ctx, workspaceID, issueID)
	if err != nil {
		return healthapp.IssueDetail{}, err
	}
	detail := healthapp.IssueDetail{Issue: issue, Observations: observations, Decisions: decisions}
	if len(observations) > 0 {
		detail.LatestObservation = &observations[0]
		// 当前 Issue 聚合的 evidence/object versions 与最新 observation 对齐。
		detail.Issue.Evidence = append([]domain.IssueEvidence(nil), observations[0].Evidence...)
		detail.Issue.ObjectVersions = append([]domain.ObjectVersion(nil), observations[0].TargetVersions...)
	}
	return detail, nil
}

func (repository *ReadRepository) loadCurrentIssue(ctx context.Context, workspaceID, issueID foundation.ID) (domain.Issue, bool, error) {
	var issue domain.Issue
	var id, ws, issueType, targetType, targetID, detectorID, identityHash, fingerprint, detectorVersion, severity, status string
	var statusReason *string
	var deferredUntil, resolvedAt *time.Time
	var repairID, repairOptionCode, repairBindingFingerprint *string
	var repairBindingObjectVersions []byte
	var repairProposalCreatedAt *time.Time
	err := repository.db.QueryRow(ctx, `SELECT id::text,workspace_id::text,type,target_type,target_id::text,detector_id,identity_hash,fingerprint,detector_version,severity,evidence_summary,status,status_reason,deferred_until,
repair_proposal_id::text,repair_option_code,repair_binding_fingerprint,repair_binding_object_versions,repair_proposal_created_at,
version,first_detected_at,last_detected_at,last_verified_at,resolved_at,created_at,updated_at
FROM ops.health_issue WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(issueID)).Scan(&id, &ws, &issueType, &targetType, &targetID, &detectorID, &identityHash, &fingerprint, &detectorVersion, &severity, &issue.EvidenceSummary, &status, &statusReason, &deferredUntil, &repairID, &repairOptionCode, &repairBindingFingerprint, &repairBindingObjectVersions, &repairProposalCreatedAt, &issue.Version, &issue.FirstDetectedAt, &issue.LastDetectedAt, &issue.LastVerifiedAt, &resolvedAt, &issue.CreatedAt, &issue.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Issue{}, false, nil
	}
	if err != nil {
		return domain.Issue{}, false, err
	}
	if ws != string(workspaceID) {
		return domain.Issue{}, false, errors.New("health issue detail crossed workspace boundary")
	}
	issue.ID, issue.WorkspaceID = foundation.ID(id), foundation.ID(ws)
	issue.Type, issue.Target = domain.IssueType(issueType), domain.ObjectRef{Type: domain.ObjectType(targetType), ID: foundation.ID(targetID)}
	issue.DetectorID, issue.IdentityHash, issue.Fingerprint, issue.DetectorVersion = detectorID, identityHash, fingerprint, detectorVersion
	issue.Severity, issue.Status = domain.Severity(severity), domain.IssueStatus(status)
	if statusReason != nil {
		issue.StatusReason = *statusReason
	}
	issue.DeferredUntil, issue.ResolvedAt = deferredUntil, resolvedAt
	if repairID != nil || repairOptionCode != nil || repairBindingFingerprint != nil || len(repairBindingObjectVersions) > 0 || repairProposalCreatedAt != nil {
		if repairID == nil || repairOptionCode == nil || repairBindingFingerprint == nil || len(repairBindingObjectVersions) == 0 || repairProposalCreatedAt == nil {
			return domain.Issue{}, false, errors.New("health issue proposal binding is incomplete")
		}
		var objectVersions []domain.ObjectVersion
		if err := json.Unmarshal(repairBindingObjectVersions, &objectVersions); err != nil {
			return domain.Issue{}, false, err
		}
		issue.Proposal = &domain.ProposalBinding{ProposalID: foundation.ID(*repairID), RepairOptionCode: *repairOptionCode, Fingerprint: *repairBindingFingerprint, ObjectVersions: objectVersions, CreatedAt: repairProposalCreatedAt.UTC()}
	}
	issue.RepairOptions = healthapp.RepairOptionsForIssue(issue.Type)
	return issue, true, nil
}

func (repository *ReadRepository) loadObservations(ctx context.Context, workspaceID, issueID foundation.ID) ([]healthapp.IssueObservationRecord, error) {
	rows, err := repository.db.Query(ctx, `SELECT id::text,issue_version,scan_id::text,detector_version,fingerprint,evidence_fingerprint,target_versions,severity,observed_at
FROM ops.health_issue_observation WHERE workspace_id=$1 AND issue_id=$2 ORDER BY observed_at DESC,id DESC`, string(workspaceID), string(issueID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]healthapp.IssueObservationRecord, 0)
	for rows.Next() {
		var item healthapp.IssueObservationRecord
		var id, scanID, severity string
		var raw []byte
		if err := rows.Scan(&id, &item.IssueVersion, &scanID, &item.DetectorVersion, &item.Fingerprint, &item.EvidenceFingerprint, &raw, &severity, &item.ObservedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.TargetVersions); err != nil {
			return nil, err
		}
		item.ID, item.ScanID, item.Severity = foundation.ID(id), foundation.ID(scanID), domain.Severity(severity)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return result, nil
	}
	observationIDs := make([]string, 0, len(result))
	byObservationID := make(map[foundation.ID]int, len(result))
	for index := range result {
		observationIDs = append(observationIDs, string(result[index].ID))
		byObservationID[result[index].ID] = index
	}
	evidenceRows, err := repository.db.Query(ctx, `SELECT observation_id::text,ref_type,ref_id::text,hash,summary
FROM ops.health_issue_evidence
WHERE workspace_id=$1 AND observation_id::text=ANY($2::text[])
ORDER BY observation_id,evidence_no,id`, string(workspaceID), observationIDs)
	if err != nil {
		return nil, err
	}
	defer evidenceRows.Close()
	for evidenceRows.Next() {
		var observationID, refType, refID, hash, summary string
		if err := evidenceRows.Scan(&observationID, &refType, &refID, &hash, &summary); err != nil {
			return nil, err
		}
		index, ok := byObservationID[foundation.ID(observationID)]
		if !ok {
			return nil, errors.New("health observation evidence crossed issue boundary")
		}
		result[index].Evidence = append(result[index].Evidence, domain.IssueEvidence{Ref: domain.ObjectRef{Type: domain.ObjectType(refType), ID: foundation.ID(refID)}, Hash: hash, Summary: summary})
	}
	return result, evidenceRows.Err()
}

func (repository *ReadRepository) loadDecisions(ctx context.Context, workspaceID, issueID foundation.ID) ([]healthapp.IssueDecisionRecord, error) {
	rows, err := repository.db.Query(ctx, `SELECT id::text,issue_version,proposal_id::text,idempotency_key,action,reason,defer_until,created_at
FROM ops.health_issue_decision WHERE workspace_id=$1 AND issue_id=$2 ORDER BY created_at DESC,id DESC`, string(workspaceID), string(issueID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []healthapp.IssueDecisionRecord
	for rows.Next() {
		var item healthapp.IssueDecisionRecord
		var id, action string
		var proposalID, reason *string
		if err := rows.Scan(&id, &item.IssueVersion, &proposalID, &item.IdempotencyKey, &action, &reason, &item.DeferredUntil, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.ID, item.Action = foundation.ID(id), domain.IssueDecisionAction(action)
		if proposalID != nil {
			value := foundation.ID(*proposalID)
			item.ProposalID = &value
		}
		if reason != nil {
			item.Reason = *reason
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetHealthSummary 返回 open Issue 聚合和最新 Scan coverage；当前未落地能力显式 unavailable。
func (repository *ReadRepository) GetHealthSummary(ctx context.Context, workspaceID foundation.ID) (healthapp.HealthSummary, error) {
	if repository == nil || repository.db == nil || !validID(workspaceID) {
		return healthapp.HealthSummary{}, errors.New("health summary request is invalid")
	}
	summary := healthapp.HealthSummary{WorkspaceID: workspaceID, OpenBySeverity: map[domain.Severity]int64{}, OpenByType: map[domain.IssueType]int64{}}
	for _, severity := range []domain.Severity{domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow} {
		summary.OpenBySeverity[severity] = 0
	}
	for _, issueType := range []domain.IssueType{domain.IssueTypeOrphan, domain.IssueTypeDuplicate, domain.IssueTypeConflict, domain.IssueTypeStale, domain.IssueTypeMissingSource, domain.IssueTypeLowConfidence, domain.IssueTypeBrokenReference, domain.IssueTypeIndexError, domain.IssueTypeSupersededUsage, domain.IssueTypeReviewInvalidated} {
		summary.OpenByType[issueType] = 0
	}
	rows, err := repository.db.Query(ctx, `SELECT severity,type,count(*) FROM ops.health_issue WHERE workspace_id=$1 AND status IN ('OPEN','ACKNOWLEDGED','DEFERRED','PROPOSAL_CREATED','REOPENED') GROUP BY severity,type`, string(workspaceID))
	if err != nil {
		return healthapp.HealthSummary{}, err
	}
	for rows.Next() {
		var severity, issueType string
		var count int64
		if err := rows.Scan(&severity, &issueType, &count); err != nil {
			rows.Close()
			return healthapp.HealthSummary{}, err
		}
		summary.OpenCount += count
		summary.OpenBySeverity[domain.Severity(severity)] += count
		summary.OpenByType[domain.IssueType(issueType)] += count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return healthapp.HealthSummary{}, err
	}
	rows.Close()
	trend, err := repository.loadHealthTrend(ctx, workspaceID)
	if err != nil {
		return healthapp.HealthSummary{}, err
	}
	summary.Trend = trend
	last, err := repository.loadLatestScan(ctx, workspaceID)
	if err != nil {
		return healthapp.HealthSummary{}, err
	}
	summary.LastScan = last
	summary.Unavailable = []healthapp.CapabilityAvailability{{Code: "REVIEW_INVALIDATED", Reason: "review capability is not implemented"}, {Code: "DIRECTORY_SCOPE", Reason: "directory scope is not implemented"}}
	return summary, nil
}

func (repository *ReadRepository) loadHealthTrend(ctx context.Context, workspaceID foundation.ID) ([]healthapp.HealthTrendPoint, error) {
	rows, err := repository.db.Query(ctx, `WITH utc_days AS (
	SELECT ((CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - ($2::integer - 1) + day_offset)::date AS day
	FROM generate_series(0,$2::integer - 1) AS offsets(day_offset)
), terminal_scans AS (
	SELECT (COALESCE(completed_at,updated_at) AT TIME ZONE 'UTC')::date AS day,
	       (SUM(created_count) + SUM(reopened_count))::bigint AS detected_count,
	       SUM(resolved_count)::bigint AS resolved_count
	FROM ops.health_scan
	WHERE workspace_id=$1
	  AND status IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED')
	  AND COALESCE(completed_at,updated_at) >= (((CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - ($2::integer - 1))::timestamp AT TIME ZONE 'UTC')
	  AND COALESCE(completed_at,updated_at) <= CURRENT_TIMESTAMP
	GROUP BY (COALESCE(completed_at,updated_at) AT TIME ZONE 'UTC')::date
)
SELECT to_char(utc_days.day,'YYYY-MM-DD'),COALESCE(terminal_scans.detected_count,0),COALESCE(terminal_scans.resolved_count,0)
FROM utc_days
LEFT JOIN terminal_scans ON terminal_scans.day=utc_days.day
ORDER BY utc_days.day`, string(workspaceID), healthapp.HealthTrendDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]healthapp.HealthTrendPoint, 0, healthapp.HealthTrendDays)
	for rows.Next() {
		var point healthapp.HealthTrendPoint
		if err := rows.Scan(&point.Date, &point.DetectedCount, &point.ResolvedCount); err != nil {
			return nil, err
		}
		result = append(result, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != healthapp.HealthTrendDays {
		return nil, errors.New("health trend query returned an invalid number of UTC days")
	}
	return result, nil
}

func (repository *ReadRepository) loadLatestScan(ctx context.Context, workspaceID foundation.ID) (*healthapp.ScanSummary, error) {
	var scan domain.Scan
	err := scanHealthScan(repository.db.QueryRow(ctx, scanSelectSQL+` WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, string(workspaceID)), &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	coverage, err := loadCoverage(ctx, repository.db, scan.ID, workspaceID, false)
	if err != nil {
		return nil, err
	}
	scan.Coverage = coverage
	return &healthapp.ScanSummary{ID: scan.ID, Status: scan.Status, Scope: scan.Scope, CompletedAt: scan.CompletedAt, Counters: scan.Counters, Coverage: coverage, UpdatedAt: scan.UpdatedAt}, nil
}

func stringSlice[T interface{ ~string }](values []T) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
