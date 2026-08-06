package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const issueFingerprintSchemaVersion = "health-issue-fingerprint/v1"

// IssueDB 是 Issue repository 所需的最小 PostgreSQL 事务边界。
type IssueDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// IssueRepository 持久化 Issue、Observation、Evidence 和 CAS Decision。
type IssueRepository struct {
	db         IssueDB
	generator  foundation.IDGenerator
	membership healthapp.SmartCollectionMembershipPort
}

var _ healthapp.DetectorPageStore = (*IssueRepository)(nil)

// NewIssueRepository 构造 Health Issue repository；可注入 ID generator 以便确定性测试。
func NewIssueRepository(db IssueDB, generators ...foundation.IDGenerator) (*IssueRepository, error) {
	if db == nil {
		return nil, errors.New("health issue database is nil")
	}
	var generator foundation.IDGenerator = foundation.NewUUIDGenerator(nil)
	if len(generators) > 0 && generators[0] != nil {
		generator = generators[0]
	}
	return &IssueRepository{db: db, generator: generator}, nil
}

// NewSmartCollectionIssueRepository 构造会以 Collection durable membership 约束 missing-set 的 repository。
func NewSmartCollectionIssueRepository(db IssueDB, membership healthapp.SmartCollectionMembershipPort, generators ...foundation.IDGenerator) (*IssueRepository, error) {
	if membership == nil {
		return nil, errors.New("health smart-collection membership is nil")
	}
	repository, err := NewIssueRepository(db, generators...)
	if err != nil {
		return nil, err
	}
	repository.membership = membership
	return repository, nil
}

// UpsertObservation 将单个 detector observation 幂等合并到稳定 Issue identity。
func (repository *IssueRepository) UpsertObservation(ctx context.Context, workspaceID, scanID, issueID foundation.ID, observation domain.IssueObservation, repairOptions []domain.RepairOption, observedAt time.Time) (domain.Issue, domain.ObservationOutcome, error) {
	if err := domain.ValidateIssueObservation(workspaceID, observation); err != nil {
		return domain.Issue{}, "", err
	}
	if observedAt.IsZero() || !validRepositoryID(workspaceID) || !validRepositoryID(scanID) {
		return domain.Issue{}, "", errors.New("health observation identity or time is invalid")
	}
	identity, err := domain.ComputeIssueIdentityHash(workspaceID, observation)
	if err != nil {
		return domain.Issue{}, "", err
	}
	if len(repairOptions) == 0 {
		repairOptions = healthapp.RepairOptionsForIssue(observation.Type)
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Issue{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, found, err := repository.loadIssueForUpdate(ctx, tx, workspaceID, identity, repairOptions)
	if err != nil {
		return domain.Issue{}, "", err
	}
	var result healthapp.ReconcileResult
	var outcome domain.ObservationOutcome
	if !found {
		if !validRepositoryID(issueID) {
			return domain.Issue{}, "", errors.New("new health observation requires issue id")
		}
		result, err = healthapp.ReconcileObservation(workspaceID, issueID, nil, observation, repairOptions, observedAt)
		if err != nil {
			return domain.Issue{}, "", err
		}
		outcome = result.Outcome
		if err := repository.insertIssue(ctx, tx, result.Issue); err != nil {
			return domain.Issue{}, "", err
		}
	} else {
		result, err = healthapp.ReconcileObservation(workspaceID, current.ID, &current, observation, repairOptions, observedAt)
		if err != nil {
			return domain.Issue{}, "", err
		}
		outcome = result.Outcome
		if outcome == domain.ObservationOutcomeUnchanged {
			if err := repository.updateLastVerifiedAt(ctx, tx, result.Issue); err != nil {
				return domain.Issue{}, "", err
			}
		} else if err := repository.updateIssue(ctx, tx, result.Issue); err != nil {
			return domain.Issue{}, "", err
		}
	}
	if err := repository.insertObservation(ctx, tx, result.Issue, scanID, observation, observedAt); err != nil {
		return domain.Issue{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Issue{}, "", err
	}
	return result.Issue, outcome, nil
}

// ReconcileDetectorPage 在一个短事务内 upsert 一页 observations 并持久化 seen identities。
func (repository *IssueRepository) ReconcileDetectorPage(ctx context.Context, request healthapp.DetectorPageReconcileRequest) (healthapp.DetectorPageReconcileResult, error) {
	if repository == nil || repository.db == nil || repository.generator == nil || !validRepositoryID(request.WorkspaceID) || !validRepositoryID(request.ScanID) || request.DetectorID == "" || request.ObservedAt.IsZero() {
		return healthapp.DetectorPageReconcileResult{}, errors.New("health detector page request is invalid")
	}
	for _, observation := range request.Observations {
		if observation.DetectorID != request.DetectorID {
			return healthapp.DetectorPageReconcileResult{}, errors.New("health detector page contains a foreign detector observation")
		}
		if err := domain.ValidateIssueObservation(request.WorkspaceID, observation); err != nil {
			return healthapp.DetectorPageReconcileResult{}, err
		}
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var scanWorkspace string
	var detectorStatus string
	if err := tx.QueryRow(ctx, `SELECT scan.workspace_id::text,coverage.status
FROM ops.health_scan scan JOIN ops.health_scan_detector coverage
  ON coverage.scan_id=scan.id AND coverage.workspace_id=scan.workspace_id
WHERE scan.id=$1 AND scan.workspace_id=$2 AND coverage.detector_id=$3
  AND scan.status IN ('PENDING','RUNNING') AND coverage.status IN ('PENDING','RUNNING')
FOR UPDATE OF scan,coverage`, string(request.ScanID), string(request.WorkspaceID), request.DetectorID).Scan(&scanWorkspace, &detectorStatus); err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	identities, observationsByIdentity, err := detectorPageIdentities(request.WorkspaceID, request.Observations)
	if err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.health_scan_seen_identity(scan_id,workspace_id,detector_id,identity_hash,created_at)
	SELECT $1,$2,$3,identity_hash,$5
	FROM unnest($4::text[]) AS identity(identity_hash)
	ON CONFLICT DO NOTHING`, string(request.ScanID), string(request.WorkspaceID), request.DetectorID, identities, request.ObservedAt.UTC()); err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	issues, err := repository.loadIssuesForUpdate(ctx, tx, request.WorkspaceID, identities)
	if err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	result, finalIssues, history, evidence, err := repository.reconcileDetectorPage(request, observationsByIdentity, issues)
	if err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	if err := upsertDetectorPageIssues(ctx, tx, finalIssues); err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	if err := insertDetectorPageHistory(ctx, tx, history, evidence); err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return healthapp.DetectorPageReconcileResult{}, err
	}
	return result, nil
}

type detectorPageObservation struct {
	identity    string
	observation domain.IssueObservation
}

type detectorPageIssueRow struct {
	ID                          string          `json:"id"`
	WorkspaceID                 string          `json:"workspace_id"`
	Type                        string          `json:"type"`
	TargetType                  string          `json:"target_type"`
	TargetID                    string          `json:"target_id"`
	DetectorID                  string          `json:"detector_id"`
	IdentityHash                string          `json:"identity_hash"`
	FingerprintSchemaVersion    string          `json:"fingerprint_schema_version"`
	Fingerprint                 string          `json:"fingerprint"`
	DetectorVersion             string          `json:"detector_version"`
	Severity                    string          `json:"severity"`
	EvidenceSummary             string          `json:"evidence_summary"`
	Status                      string          `json:"status"`
	StatusReason                *string         `json:"status_reason"`
	DeferredUntil               *time.Time      `json:"deferred_until"`
	RepairProposalID            *string         `json:"repair_proposal_id"`
	RepairOptionCode            *string         `json:"repair_option_code"`
	RepairBindingFingerprint    *string         `json:"repair_binding_fingerprint"`
	RepairBindingObjectVersions json.RawMessage `json:"repair_binding_object_versions"`
	RepairProposalCreatedAt     *time.Time      `json:"repair_proposal_created_at"`
	ExpectedVersion             int64           `json:"expected_version"`
	Version                     int64           `json:"version"`
	FirstDetectedAt             time.Time       `json:"first_detected_at"`
	LastDetectedAt              time.Time       `json:"last_detected_at"`
	LastVerifiedAt              time.Time       `json:"last_verified_at"`
	Unchanged                   bool            `json:"unchanged"`
	ResolvedAt                  *time.Time      `json:"resolved_at"`
	CreatedAt                   time.Time       `json:"created_at"`
	UpdatedAt                   time.Time       `json:"updated_at"`
}

type detectorPageHistoryRow struct {
	ID                       string          `json:"id"`
	WorkspaceID              string          `json:"workspace_id"`
	IssueID                  string          `json:"issue_id"`
	IssueVersion             int64           `json:"issue_version"`
	ScanID                   string          `json:"scan_id"`
	DetectorVersion          string          `json:"detector_version"`
	FingerprintSchemaVersion string          `json:"fingerprint_schema_version"`
	Fingerprint              string          `json:"fingerprint"`
	EvidenceFingerprint      string          `json:"evidence_fingerprint"`
	TargetVersions           json.RawMessage `json:"target_versions"`
	Severity                 string          `json:"severity"`
	ObservedAt               time.Time       `json:"observed_at"`
}

type detectorPageEvidenceRow struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	ObservationID string    `json:"observation_id"`
	EvidenceNo    int       `json:"evidence_no"`
	RefType       string    `json:"ref_type"`
	RefID         string    `json:"ref_id"`
	Summary       string    `json:"summary"`
	Hash          string    `json:"hash"`
	CreatedAt     time.Time `json:"created_at"`
}

type detectorPageStoredEvidence struct {
	RefType string `json:"ref_type"`
	RefID   string `json:"ref_id"`
	Hash    string `json:"hash"`
	Summary string `json:"summary"`
}

func detectorPageIdentities(workspaceID foundation.ID, observations []domain.IssueObservation) ([]string, []detectorPageObservation, error) {
	identities := make([]string, 0, len(observations))
	seen := make(map[string]struct{}, len(observations))
	prepared := make([]detectorPageObservation, 0, len(observations))
	for _, observation := range observations {
		identity, err := domain.ComputeIssueIdentityHash(workspaceID, observation)
		if err != nil {
			return nil, nil, err
		}
		prepared = append(prepared, detectorPageObservation{identity: identity, observation: observation})
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	return identities, prepared, nil
}

func (repository *IssueRepository) loadIssuesForUpdate(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, identities []string) (map[string]domain.Issue, error) {
	rows, err := tx.Query(ctx, `WITH locked AS MATERIALIZED (
	SELECT i.id,i.workspace_id,i.type,i.target_type,i.target_id,i.detector_id,i.identity_hash,i.fingerprint,
	       i.detector_version,i.severity,i.evidence_summary,i.status,i.status_reason,i.deferred_until,
	       i.repair_proposal_id,i.repair_option_code,i.repair_binding_fingerprint,i.repair_binding_object_versions,
	       i.repair_proposal_created_at,i.version,i.first_detected_at,i.last_detected_at,i.last_verified_at,
	       i.resolved_at,i.created_at,i.updated_at
	FROM ops.health_issue i
	WHERE i.workspace_id=$1 AND i.identity_hash=ANY($2::text[])
	ORDER BY i.identity_hash
	FOR UPDATE
), latest AS (
	SELECT observation.issue_id,observation.id,observation.target_versions
	FROM ops.health_issue_observation observation
	JOIN locked issue ON issue.id=observation.issue_id AND issue.workspace_id=observation.workspace_id AND issue.fingerprint=observation.fingerprint
), latest_evidence AS (
	SELECT evidence.observation_id,
	       jsonb_agg(jsonb_build_object('ref_type',evidence.ref_type,'ref_id',evidence.ref_id::text,
	                                      'hash',evidence.hash,'summary',evidence.summary)
	                 ORDER BY evidence.evidence_no) AS items
	FROM ops.health_issue_evidence evidence
	JOIN latest ON latest.id=evidence.observation_id
	GROUP BY evidence.observation_id
)
	SELECT issue.id::text,issue.workspace_id::text,issue.type,issue.target_type,issue.target_id::text,issue.detector_id,
	       issue.identity_hash,issue.fingerprint,issue.detector_version,issue.severity,issue.evidence_summary,
	       issue.status,issue.status_reason,issue.deferred_until,issue.repair_proposal_id::text,issue.repair_option_code,
	       issue.repair_binding_fingerprint,issue.repair_binding_object_versions,issue.repair_proposal_created_at,
	       issue.version,issue.first_detected_at,issue.last_detected_at,issue.last_verified_at,issue.resolved_at,
	       issue.created_at,issue.updated_at,latest.target_versions,COALESCE(latest_evidence.items,'[]'::jsonb)
	FROM locked issue
	LEFT JOIN latest ON latest.issue_id=issue.id
	LEFT JOIN latest_evidence ON latest_evidence.observation_id=latest.id
	ORDER BY issue.identity_hash`, string(workspaceID), identities)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues := make(map[string]domain.Issue, len(identities))
	for rows.Next() {
		var issue domain.Issue
		var issueType, targetType, targetID, severity, status string
		var statusReason, repairID, repairOptionCode, repairFingerprint *string
		var repairCreatedAt *time.Time
		var repairVersions, targetVersions, rawEvidence []byte
		if err := rows.Scan(
			&issue.ID, &issue.WorkspaceID, &issueType, &targetType, &targetID, &issue.DetectorID,
			&issue.IdentityHash, &issue.Fingerprint, &issue.DetectorVersion, &severity, &issue.EvidenceSummary,
			&status, &statusReason, &issue.DeferredUntil, &repairID, &repairOptionCode, &repairFingerprint,
			&repairVersions, &repairCreatedAt, &issue.Version, &issue.FirstDetectedAt,
			&issue.LastDetectedAt, &issue.LastVerifiedAt, &issue.ResolvedAt, &issue.CreatedAt, &issue.UpdatedAt,
			&targetVersions, &rawEvidence,
		); err != nil {
			return nil, err
		}
		issue.Type = domain.IssueType(issueType)
		issue.Target = domain.ObjectRef{Type: domain.ObjectType(targetType), ID: foundation.ID(targetID)}
		issue.Severity = domain.Severity(severity)
		issue.Status = domain.IssueStatus(status)
		issue.StatusReason = valueString(statusReason)
		issue.RepairOptions = healthapp.RepairOptionsForIssue(issue.Type)
		if err := json.Unmarshal(targetVersions, &issue.ObjectVersions); err != nil {
			return nil, err
		}
		var storedEvidence []detectorPageStoredEvidence
		if err := json.Unmarshal(rawEvidence, &storedEvidence); err != nil {
			return nil, err
		}
		issue.Evidence = make([]domain.IssueEvidence, 0, len(storedEvidence))
		for _, item := range storedEvidence {
			issue.Evidence = append(issue.Evidence, domain.IssueEvidence{Ref: domain.ObjectRef{Type: domain.ObjectType(item.RefType), ID: foundation.ID(item.RefID)}, Hash: item.Hash, Summary: item.Summary})
		}
		if repairID != nil || repairOptionCode != nil || repairFingerprint != nil || len(repairVersions) > 0 || repairCreatedAt != nil {
			if repairID == nil || repairOptionCode == nil || repairFingerprint == nil || len(repairVersions) == 0 || repairCreatedAt == nil {
				return nil, errors.New("health issue proposal binding is incomplete")
			}
			issue.Proposal = &domain.ProposalBinding{CreatedAt: repairCreatedAt.UTC()}
			if err := json.Unmarshal(repairVersions, &issue.Proposal.ObjectVersions); err != nil {
				return nil, err
			}
			issue.Proposal.ProposalID = foundation.ID(*repairID)
			issue.Proposal.RepairOptionCode = *repairOptionCode
			issue.Proposal.Fingerprint = *repairFingerprint
		}
		issues[issue.IdentityHash] = issue
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

func (repository *IssueRepository) reconcileDetectorPage(request healthapp.DetectorPageReconcileRequest, observations []detectorPageObservation, existing map[string]domain.Issue) (healthapp.DetectorPageReconcileResult, []detectorPageIssueRow, []detectorPageHistoryRow, []detectorPageEvidenceRow, error) {
	result := healthapp.DetectorPageReconcileResult{}
	current := make(map[string]domain.Issue, len(existing)+len(observations))
	for identity, issue := range existing {
		current[identity] = issue
	}
	history := make([]detectorPageHistoryRow, 0, len(observations))
	evidenceRows := make([]detectorPageEvidenceRow, 0)
	historyByIdentity := make(map[string][]int, len(observations))
	unchangedByIdentity := make(map[string]bool, len(observations))
	for _, prepared := range observations {
		issue, found := current[prepared.identity]
		issueID := issue.ID
		var err error
		if !found {
			issueID, err = repository.generator.New()
			if err != nil {
				return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, err
			}
		}
		reconciled, err := healthapp.ReconcileObservation(request.WorkspaceID, issueID, issuePointer(issue, found), prepared.observation, healthapp.RepairOptionsForIssue(prepared.observation.Type), request.ObservedAt)
		if err != nil {
			return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, err
		}
		if reconciled.Created {
			result.Created++
		} else if reconciled.Outcome == domain.ObservationOutcomeReopened {
			result.Reopened++
		} else {
			result.Unchanged++
		}
		current[prepared.identity] = reconciled.Issue
		if reconciled.Outcome == domain.ObservationOutcomeUnchanged {
			unchangedByIdentity[prepared.identity] = true
			continue
		}
		unchangedByIdentity[prepared.identity] = false
		observationID, err := repository.generator.New()
		if err != nil {
			return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, err
		}
		targetVersions, err := json.Marshal(prepared.observation.ObjectVersions)
		if err != nil {
			return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, err
		}
		fingerprint, err := domain.ComputeIssueFingerprint(request.WorkspaceID, prepared.observation)
		if err != nil {
			return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, err
		}
		history = append(history, detectorPageHistoryRow{
			ID: string(observationID), WorkspaceID: string(request.WorkspaceID), IssueID: string(reconciled.Issue.ID),
			IssueVersion: reconciled.Issue.Version, ScanID: string(request.ScanID), DetectorVersion: prepared.observation.DetectorVersion,
			FingerprintSchemaVersion: issueFingerprintSchemaVersion, Fingerprint: fingerprint,
			EvidenceFingerprint: fingerprintEvidence(prepared.observation.Evidence), TargetVersions: targetVersions,
			Severity: string(reconciled.Issue.Severity), ObservedAt: request.ObservedAt.UTC(),
		})
		historyByIdentity[prepared.identity] = append(historyByIdentity[prepared.identity], len(history)-1)
		for index, item := range prepared.observation.Evidence {
			evidenceID, idErr := repository.generator.New()
			if idErr != nil {
				return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, idErr
			}
			evidenceRows = append(evidenceRows, detectorPageEvidenceRow{
				ID: string(evidenceID), WorkspaceID: string(request.WorkspaceID), ObservationID: string(observationID),
				EvidenceNo: index + 1, RefType: string(item.Ref.Type), RefID: string(item.Ref.ID), Summary: item.Summary,
				Hash: item.Hash, CreatedAt: request.ObservedAt.UTC(),
			})
		}
	}
	identities := make([]string, 0, len(current))
	for identity := range current {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	issues := make([]detectorPageIssueRow, 0, len(identities))
	for _, identity := range identities {
		issue := current[identity]
		expectedVersion := int64(0)
		if stored, found := existing[identity]; found {
			expectedVersion = stored.Version
		}
		row, err := detectorPageIssueInput(issue, expectedVersion)
		if err != nil {
			return healthapp.DetectorPageReconcileResult{}, nil, nil, nil, err
		}
		// Only an unchanged observation against the persisted version can use the verify-only update.
		// A duplicate observation after an in-page reopen must retain the full CAS update from the original version.
		row.Unchanged = unchangedByIdentity[identity] && (expectedVersion == 0 || issue.Version == expectedVersion)
		issues = append(issues, row)
		for _, historyIndex := range historyByIdentity[identity] {
			history[historyIndex].IssueVersion = issue.Version
		}
	}
	observationIDRemap := make(map[string]string, len(history))
	for _, indexes := range historyByIdentity {
		ids := make([]string, 0, len(indexes))
		for _, index := range indexes {
			ids = append(ids, history[index].ID)
		}
		sort.Strings(ids)
		for position, index := range indexes {
			oldID := history[index].ID
			history[index].ID = ids[position]
			observationIDRemap[oldID] = ids[position]
		}
	}
	for index := range evidenceRows {
		evidenceRows[index].ObservationID = observationIDRemap[evidenceRows[index].ObservationID]
	}
	return result, issues, history, evidenceRows, nil
}

func detectorPageIssueInput(issue domain.Issue, expectedVersion int64) (detectorPageIssueRow, error) {
	row := detectorPageIssueRow{
		ID: string(issue.ID), WorkspaceID: string(issue.WorkspaceID), Type: string(issue.Type), TargetType: string(issue.Target.Type),
		TargetID: string(issue.Target.ID), DetectorID: issue.DetectorID, IdentityHash: issue.IdentityHash,
		FingerprintSchemaVersion: issueFingerprintSchemaVersion, Fingerprint: issue.Fingerprint, DetectorVersion: issue.DetectorVersion,
		Severity: string(issue.Severity), EvidenceSummary: issue.EvidenceSummary, Status: string(issue.Status),
		StatusReason: nullableString(issue.StatusReason), DeferredUntil: issue.DeferredUntil, ExpectedVersion: expectedVersion, Version: issue.Version,
		FirstDetectedAt: issue.FirstDetectedAt.UTC(), LastDetectedAt: issue.LastDetectedAt.UTC(), LastVerifiedAt: issue.LastVerifiedAt.UTC(),
		ResolvedAt: issue.ResolvedAt, CreatedAt: issue.CreatedAt.UTC(), UpdatedAt: issue.UpdatedAt.UTC(),
	}
	if issue.Proposal != nil {
		proposalID, repairOption, fingerprint := string(issue.Proposal.ProposalID), issue.Proposal.RepairOptionCode, issue.Proposal.Fingerprint
		row.RepairProposalID, row.RepairOptionCode, row.RepairBindingFingerprint = &proposalID, &repairOption, &fingerprint
		versions, err := json.Marshal(issue.Proposal.ObjectVersions)
		if err != nil {
			return detectorPageIssueRow{}, err
		}
		row.RepairBindingObjectVersions = versions
		createdAt := issue.Proposal.CreatedAt.UTC()
		row.RepairProposalCreatedAt = &createdAt
	}
	return row, nil
}

func upsertDetectorPageIssues(ctx context.Context, tx pgx.Tx, issues []detectorPageIssueRow) error {
	payload, err := json.Marshal(issues)
	if err != nil {
		return err
	}
	var insertedCount, updatedCount, verifiedCount int
	err = tx.QueryRow(ctx, `WITH input AS (
	SELECT * FROM jsonb_to_recordset($1::jsonb) AS issue(
		id uuid,workspace_id uuid,type text,target_type text,target_id uuid,detector_id text,identity_hash text,
		fingerprint_schema_version text,fingerprint text,detector_version text,severity text,evidence_summary text,
		status text,status_reason text,deferred_until timestamptz,repair_proposal_id uuid,repair_option_code text,
		repair_binding_fingerprint text,repair_binding_object_versions jsonb,repair_proposal_created_at timestamptz,
		expected_version bigint,unchanged boolean,version bigint,first_detected_at timestamptz,last_detected_at timestamptz,last_verified_at timestamptz,
		resolved_at timestamptz,created_at timestamptz,updated_at timestamptz)
), inserted AS (
	INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,fingerprint,
		detector_version,severity,evidence_summary,status,status_reason,deferred_until,repair_proposal_id,
		repair_option_code,repair_binding_fingerprint,repair_binding_object_versions,repair_proposal_created_at,
		version,first_detected_at,last_detected_at,last_verified_at,resolved_at,created_at,updated_at)
	SELECT id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,fingerprint,
	       detector_version,severity,evidence_summary,status,status_reason,deferred_until,repair_proposal_id,
	       repair_option_code,repair_binding_fingerprint,repair_binding_object_versions,repair_proposal_created_at,
	       version,first_detected_at,last_detected_at,last_verified_at,resolved_at,created_at,updated_at
	FROM input WHERE expected_version=0
	ON CONFLICT DO NOTHING
	RETURNING identity_hash
), updated AS (
	UPDATE ops.health_issue issue SET
		fingerprint_schema_version=input.fingerprint_schema_version,fingerprint=input.fingerprint,
		detector_version=input.detector_version,severity=input.severity,evidence_summary=input.evidence_summary,
		status=input.status,status_reason=input.status_reason,deferred_until=input.deferred_until,
		repair_proposal_id=input.repair_proposal_id,repair_option_code=input.repair_option_code,
		repair_binding_fingerprint=input.repair_binding_fingerprint,
		repair_binding_object_versions=input.repair_binding_object_versions,
		repair_proposal_created_at=input.repair_proposal_created_at,version=input.version,
		last_detected_at=input.last_detected_at,last_verified_at=input.last_verified_at,
		resolved_at=input.resolved_at,updated_at=input.updated_at
	FROM input
	WHERE input.expected_version>0 AND NOT input.unchanged AND issue.id=input.id AND issue.workspace_id=input.workspace_id
	  AND issue.identity_hash=input.identity_hash AND issue.version=input.expected_version
	RETURNING issue.identity_hash
)
,
verified AS (
	UPDATE ops.health_issue issue SET last_verified_at=input.last_verified_at
	FROM input
	WHERE input.expected_version>0 AND input.unchanged AND issue.id=input.id AND issue.workspace_id=input.workspace_id
	  AND issue.identity_hash=input.identity_hash AND issue.version=input.expected_version
	RETURNING issue.identity_hash
)
	SELECT (SELECT count(*) FROM inserted),(SELECT count(*) FROM updated),(SELECT count(*) FROM verified)`, payload).Scan(&insertedCount, &updatedCount, &verifiedCount)
	if err != nil {
		return detectorPageWriteError(err)
	}
	expectedInserted, expectedUpdated, expectedVerified := 0, 0, 0
	for _, issue := range issues {
		if issue.ExpectedVersion == 0 {
			expectedInserted++
		} else if issue.Unchanged {
			expectedVerified++
		} else {
			expectedUpdated++
		}
	}
	if insertedCount != expectedInserted || updatedCount != expectedUpdated || verifiedCount != expectedVerified {
		return detectorPageWriteError(errors.New("health detector page issue CAS did not reconcile every identity"))
	}
	return nil
}

func insertDetectorPageHistory(ctx context.Context, tx pgx.Tx, history []detectorPageHistoryRow, evidence []detectorPageEvidenceRow) error {
	historyPayload, err := json.Marshal(history)
	if err != nil {
		return err
	}
	evidencePayload, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	var insertedObservations, insertedEvidence, expectedEvidence int
	err = tx.QueryRow(ctx, `WITH observation_input AS (
	SELECT * FROM jsonb_to_recordset($1::jsonb) AS observation(
		id uuid,workspace_id uuid,issue_id uuid,issue_version bigint,scan_id uuid,detector_version text,
		fingerprint_schema_version text,fingerprint text,evidence_fingerprint text,target_versions jsonb,
		severity text,observed_at timestamptz)
), inserted_observations AS (
	INSERT INTO ops.health_issue_observation(
		id,workspace_id,issue_id,issue_version,scan_id,detector_version,fingerprint_schema_version,
		fingerprint,evidence_fingerprint,target_versions,severity,observed_at)
	SELECT id,workspace_id,issue_id,issue_version,scan_id,detector_version,fingerprint_schema_version,
	       fingerprint,evidence_fingerprint,target_versions,severity,observed_at
	FROM observation_input
	ON CONFLICT(issue_id,fingerprint) DO NOTHING
	RETURNING id
), evidence_input AS (
	SELECT * FROM jsonb_to_recordset($2::jsonb) AS evidence(
		id uuid,workspace_id uuid,observation_id uuid,evidence_no integer,ref_type text,ref_id uuid,
		summary text,hash text,created_at timestamptz)
), expected_evidence AS (
	SELECT count(*) AS count FROM evidence_input evidence
	JOIN inserted_observations observation ON observation.id=evidence.observation_id
), inserted_evidence AS (
	INSERT INTO ops.health_issue_evidence(
		id,workspace_id,observation_id,evidence_no,ref_type,ref_id,summary,hash,created_at)
	SELECT evidence.id,evidence.workspace_id,evidence.observation_id,evidence.evidence_no,evidence.ref_type,
	       evidence.ref_id,evidence.summary,evidence.hash,evidence.created_at
	FROM evidence_input evidence
	JOIN inserted_observations observation ON observation.id=evidence.observation_id
	ON CONFLICT(observation_id,evidence_no) DO NOTHING
	RETURNING id
)
	SELECT (SELECT count(*) FROM inserted_observations),
	       (SELECT count(*) FROM inserted_evidence),
	       (SELECT count FROM expected_evidence)`, historyPayload, evidencePayload).Scan(&insertedObservations, &insertedEvidence, &expectedEvidence)
	if err != nil {
		return err
	}
	if insertedObservations > len(history) || insertedEvidence != expectedEvidence {
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeIssueTransitionInvalid, false, errors.New("health detector page history insert count is inconsistent"))
	}
	return nil
}

func detectorPageWriteError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code != "23505" && postgresError.Code != "40001" && postgresError.Code != "40P01" {
		return err
	}
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIssueTransitionInvalid, true, err)
}

// ResolveMissingForCompleteScan 在 Scan 终态前对已完整 coverage 的 detector 解决未见 active Issue并回填计数。
func (repository *IssueRepository) ResolveMissingForCompleteScan(ctx context.Context, workspaceID, scanID foundation.ID, detectorID string) (int64, error) {
	if repository == nil || repository.db == nil || !validRepositoryID(workspaceID) || !validRepositoryID(scanID) || detectorID == "" {
		return 0, errors.New("health missing-set resolution request is invalid")
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	scan, err := loadScanByID(ctx, tx, workspaceID, scanID, true)
	if err != nil {
		return 0, err
	}
	if scan.Status != domain.ScanStatusRunning || !domain.CoverageComplete(scan.Coverage) {
		return 0, nil
	}
	coverage, found := coverageItem(scan.Coverage, detectorID)
	if !found || coverage.Status != domain.DetectorCoverageStatusSucceeded {
		return 0, nil
	}
	if scan.Scope.Type != domain.ScanScopeTypeWorkspace && scan.Scope.Type != domain.ScanScopeTypeTopic && scan.Scope.Type != domain.ScanScopeTypeSmartCollection {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeScanInvalid, false, errors.New("health missing-set scope is invalid"))
	}
	if (scan.Scope.Type == domain.ScanScopeTypeTopic || scan.Scope.Type == domain.ScanScopeTypeSmartCollection) && !detector.DefaultSupportsScope(detectorID, scan.Scope.Type) {
		return 0, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, healthapp.ErrDetectorUnavailable)
	}
	membership := scanScopeMembership{}
	var binding healthapp.SmartCollectionBinding
	if scan.Scope.Type == domain.ScanScopeTypeSmartCollection {
		membership, binding, err = loadSmartCollectionScope(ctx, repository.membership, workspaceID, scan.Scope)
		if err != nil {
			return 0, err
		}
	}
	now, err := issueDatabaseTime(ctx, tx)
	if err != nil {
		return 0, err
	}
	scopePredicate := scanScopePredicate(issueScopeTarget)
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_issue issue
SET status='RESOLVED',status_reason=NULL,deferred_until=NULL,repair_proposal_id=NULL,
    repair_option_code=NULL,repair_binding_fingerprint=NULL,repair_binding_object_versions=NULL,
    repair_proposal_created_at=NULL,resolved_at=GREATEST($6,issue.updated_at),
    last_verified_at=GREATEST($6,issue.updated_at),updated_at=GREATEST($6,issue.updated_at),version=version+1
WHERE issue.workspace_id=$1 AND issue.detector_id=$2
  AND issue.status IN ('OPEN','ACKNOWLEDGED','REOPENED')
  AND NOT EXISTS (
	    SELECT 1 FROM ops.health_scan_seen_identity seen
	    WHERE seen.scan_id=$3 AND seen.workspace_id=issue.workspace_id
	      AND seen.detector_id=issue.detector_id AND seen.identity_hash=issue.identity_hash)
	  AND `+scopePredicate, string(workspaceID), detectorID, string(scanID), string(scan.Scope.Type), string(scan.Scope.Ref), now, membership.TopicIDs, membership.ClaimIDs)
	if err != nil {
		return 0, err
	}
	resolved := commandTag.RowsAffected()
	if resolved > 0 {
		detectorTag, updateErr := tx.Exec(ctx, `UPDATE ops.health_scan_detector
SET resolved_count=resolved_count+$4
WHERE scan_id=$1 AND workspace_id=$2 AND detector_id=$3 AND status='SUCCEEDED'`, string(scanID), string(workspaceID), detectorID, resolved)
		if updateErr != nil {
			return 0, updateErr
		}
		if detectorTag.RowsAffected() != 1 {
			return 0, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanInvalid, false, errors.New("health detector resolved count finalization did not match"))
		}
		scanTag, updateErr := tx.Exec(ctx, `UPDATE ops.health_scan
SET resolved_count=resolved_count+$3,version=version+1,updated_at=GREATEST($4,updated_at)
WHERE id=$1 AND workspace_id=$2 AND status='RUNNING'`, string(scanID), string(workspaceID), resolved, now)
		if updateErr != nil {
			return 0, updateErr
		}
		if scanTag.RowsAffected() != 1 {
			return 0, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanInvalid, false, errors.New("health scan resolved count finalization did not match"))
		}
	}
	if scan.Scope.Type == domain.ScanScopeTypeSmartCollection {
		if err := revalidateSmartCollectionScope(ctx, repository.membership, binding); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return resolved, nil
}

func issuePointer(issue domain.Issue, found bool) *domain.Issue {
	if !found {
		return nil
	}
	return &issue
}

// GetIssue 读取带最新 Evidence/target versions 的 Issue 聚合。
func (repository *IssueRepository) GetIssue(ctx context.Context, workspaceID, issueID foundation.ID, repairOptions []domain.RepairOption) (domain.Issue, error) {
	if !validRepositoryID(workspaceID) || !validRepositoryID(issueID) {
		return domain.Issue{}, errors.New("health issue identity is invalid")
	}
	issue, found, err := repository.loadIssue(ctx, repository.db, workspaceID, issueID, repairOptions, false)
	if err != nil {
		return domain.Issue{}, err
	}
	if !found {
		return domain.Issue{}, pgx.ErrNoRows
	}
	return issue, nil
}

// ApplyDecision 以 expected version + idempotency key 原子更新 Issue 状态。
func (repository *IssueRepository) ApplyDecision(ctx context.Context, workspaceID, issueID foundation.ID, decision domain.IssueDecision, at time.Time, repairOptions []domain.RepairOption) (domain.Issue, error) {
	if !validRepositoryID(workspaceID) || !validRepositoryID(issueID) || at.IsZero() {
		return domain.Issue{}, errors.New("health decision identity or time is invalid")
	}
	requestHash, err := decisionHash(decision)
	if err != nil {
		return domain.Issue{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Issue{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt, found, receiptErr := repository.loadDecisionReceipt(ctx, tx, workspaceID, decision.IdempotencyKey); receiptErr != nil {
		return domain.Issue{}, receiptErr
	} else if found {
		return repository.replayDecision(ctx, tx, workspaceID, issueID, requestHash, repairOptions, receipt)
	}
	issue, found, err := repository.loadIssueForUpdateByID(ctx, tx, workspaceID, issueID, repairOptions)
	if err != nil || !found {
		if err != nil {
			return domain.Issue{}, err
		}
		return domain.Issue{}, pgx.ErrNoRows
	}
	if receipt, found, receiptErr := repository.loadDecisionReceipt(ctx, tx, workspaceID, decision.IdempotencyKey); receiptErr != nil {
		return domain.Issue{}, receiptErr
	} else if found {
		return repository.replayDecision(ctx, tx, workspaceID, issueID, requestHash, repairOptions, receipt)
	}
	updated, err := domain.ApplyIssueDecision(issue, decision, at)
	if err != nil {
		return domain.Issue{}, err
	}
	decisionID, err := repository.generator.New()
	if err != nil {
		return domain.Issue{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.health_issue_decision(
 id,workspace_id,issue_id,issue_version,proposal_id,repair_option_code,idempotency_key,request_hash,action,reason,defer_until,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, string(decisionID), string(workspaceID), string(issueID), issue.Version,
		nullableProposalID(decision.ProposalID), nullableString(decision.RepairOptionCode), decision.IdempotencyKey, requestHash, string(decision.Action), nullableString(decision.Reason), decision.DeferredUntil, at.UTC()); err != nil {
		return domain.Issue{}, err
	}
	if err := repository.updateIssue(ctx, tx, updated); err != nil {
		return domain.Issue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Issue{}, err
	}
	return updated, nil
}

type decisionReceipt struct {
	IssueID     foundation.ID
	RequestHash string
}

func (repository *IssueRepository) loadDecisionReceipt(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, idempotencyKey string) (decisionReceipt, bool, error) {
	var issueID, requestHash string
	err := queryer.QueryRow(ctx, `SELECT issue_id::text,request_hash FROM ops.health_issue_decision WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), idempotencyKey).Scan(&issueID, &requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return decisionReceipt{}, false, nil
	}
	if err != nil {
		return decisionReceipt{}, false, err
	}
	return decisionReceipt{IssueID: foundation.ID(issueID), RequestHash: requestHash}, true, nil
}

func (repository *IssueRepository) replayDecision(ctx context.Context, tx pgx.Tx, workspaceID, issueID foundation.ID, requestHash string, repairOptions []domain.RepairOption, receipt decisionReceipt) (domain.Issue, error) {
	if receipt.IssueID != issueID || receipt.RequestHash != requestHash {
		return domain.Issue{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIssueDecisionInvalid, false, errors.New("health decision idempotency key is bound to a different request"))
	}
	issue, found, err := repository.loadIssueForUpdateByID(ctx, tx, workspaceID, receipt.IssueID, repairOptions)
	if err != nil {
		return domain.Issue{}, err
	}
	if !found {
		return domain.Issue{}, pgx.ErrNoRows
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Issue{}, err
	}
	return issue, nil
}

func (repository *IssueRepository) loadIssueForUpdate(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, identity string, repairOptions []domain.RepairOption) (domain.Issue, bool, error) {
	if identity == "" {
		return domain.Issue{}, false, errors.New("health issue identity is required")
	}
	return repository.loadIssue(ctx, tx, workspaceID, foundation.ID(""), repairOptions, true, identity)
}

func (repository *IssueRepository) loadIssueForUpdateByID(ctx context.Context, tx pgx.Tx, workspaceID, issueID foundation.ID, repairOptions []domain.RepairOption) (domain.Issue, bool, error) {
	return repository.loadIssue(ctx, tx, workspaceID, issueID, repairOptions, true)
}

func (repository *IssueRepository) loadIssue(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, workspaceID, issueID foundation.ID, repairOptions []domain.RepairOption, forUpdate bool, identities ...string) (domain.Issue, bool, error) {
	where := "i.id=$2"
	second := string(issueID)
	if len(identities) > 0 && identities[0] != "" {
		where = "i.identity_hash=$2"
		second = identities[0]
	}
	var issue domain.Issue
	var targetType, targetID, detectorID, identityHash, fingerprint, detectorVersion, severity, issueType, status string
	var statusReason *string
	var deferredUntil, resolvedAt *time.Time
	var repairID, repairOptionCode, repairBindingFingerprint *string
	var repairBindingObjectVersions []byte
	var repairProposalCreatedAt *time.Time
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE"
	}
	err := queryer.QueryRow(ctx, `SELECT i.id::text,i.workspace_id::text,i.type,i.target_type,i.target_id::text,i.detector_id,
 i.identity_hash,i.fingerprint,i.detector_version,i.severity,i.evidence_summary,i.status,i.status_reason,i.deferred_until,
 i.repair_proposal_id,i.repair_option_code,i.repair_binding_fingerprint,i.repair_binding_object_versions,
 i.repair_proposal_created_at,i.version,i.first_detected_at,i.last_detected_at,i.last_verified_at,i.resolved_at,i.created_at,i.updated_at
 FROM ops.health_issue i WHERE i.workspace_id=$1 AND `+where+lockClause, string(workspaceID), second).Scan(
		&issue.ID, &issue.WorkspaceID, &issueType, &targetType, &targetID, &detectorID, &identityHash, &fingerprint, &detectorVersion, &severity, &issue.EvidenceSummary, &status, &statusReason, &deferredUntil, &repairID, &repairOptionCode, &repairBindingFingerprint, &repairBindingObjectVersions, &repairProposalCreatedAt, &issue.Version, &issue.FirstDetectedAt, &issue.LastDetectedAt, &issue.LastVerifiedAt, &resolvedAt, &issue.CreatedAt, &issue.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Issue{}, false, nil
	}
	if err != nil {
		return domain.Issue{}, false, err
	}
	issue.WorkspaceID = foundation.ID(issue.WorkspaceID)
	issue.Type = domain.IssueType(issueType)
	issue.Target = domain.ObjectRef{Type: domain.ObjectType(targetType), ID: foundation.ID(targetID)}
	issue.DetectorID, issue.IdentityHash, issue.Fingerprint, issue.DetectorVersion = detectorID, identityHash, fingerprint, detectorVersion
	issue.Severity, issue.Status = domain.Severity(severity), domain.IssueStatus(status)
	issue.StatusReason, issue.DeferredUntil, issue.ResolvedAt = valueString(statusReason), deferredUntil, resolvedAt
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
	if len(repairOptions) == 0 {
		repairOptions = healthapp.RepairOptionsForIssue(issue.Type)
	}
	issue.RepairOptions = append([]domain.RepairOption(nil), repairOptions...)
	if err := repository.loadLatestObservation(ctx, queryer, &issue); err != nil {
		return domain.Issue{}, false, err
	}
	return issue, true, nil
}

func (repository *IssueRepository) loadLatestObservation(ctx context.Context, queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, issue *domain.Issue) error {
	rows, err := queryer.Query(ctx, `SELECT o.target_versions FROM ops.health_issue_observation o WHERE o.workspace_id=$1 AND o.issue_id=$2 AND o.fingerprint=$3`, string(issue.WorkspaceID), string(issue.ID), issue.Fingerprint)
	if err != nil {
		return err
	}
	if rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var versions []domain.ObjectVersion
		if err := json.Unmarshal(raw, &versions); err != nil {
			return err
		}
		issue.ObjectVersions = versions
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = queryer.Query(ctx, `SELECT e.ref_type,e.ref_id::text,e.hash,e.summary FROM ops.health_issue_evidence e JOIN ops.health_issue_observation o ON o.id=e.observation_id AND o.workspace_id=e.workspace_id WHERE o.workspace_id=$1 AND o.issue_id=$2 AND o.fingerprint=$3 ORDER BY e.evidence_no`, string(issue.WorkspaceID), string(issue.ID), issue.Fingerprint)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var refType, refID, hash, summary string
		if err := rows.Scan(&refType, &refID, &hash, &summary); err != nil {
			return err
		}
		issue.Evidence = append(issue.Evidence, domain.IssueEvidence{Ref: domain.ObjectRef{Type: domain.ObjectType(refType), ID: foundation.ID(refID)}, Hash: hash, Summary: summary})
	}
	return rows.Err()
}

func (repository *IssueRepository) insertIssue(ctx context.Context, tx pgx.Tx, issue domain.Issue) error {
	proposalVersions, err := proposalObjectVersions(issue.Proposal)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ops.health_issue(
 id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,status,status_reason,deferred_until,repair_proposal_id,repair_option_code,repair_binding_fingerprint,repair_binding_object_versions,repair_proposal_created_at,version,first_detected_at,last_detected_at,last_verified_at,resolved_at,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::jsonb,$20,$21,$22,$23,$24,$25,$26,$27)`, string(issue.ID), string(issue.WorkspaceID), string(issue.Type), string(issue.Target.Type), string(issue.Target.ID), issue.DetectorID, issue.IdentityHash, issueFingerprintSchemaVersion, issue.Fingerprint, issue.DetectorVersion, string(issue.Severity), issue.EvidenceSummary, string(issue.Status), nullableString(issue.StatusReason), issue.DeferredUntil, proposalID(issue.Proposal), proposalRepairOption(issue.Proposal), proposalFingerprint(issue.Proposal), proposalVersions, proposalCreatedAt(issue.Proposal), issue.Version, issue.FirstDetectedAt.UTC(), issue.LastDetectedAt.UTC(), issue.LastVerifiedAt.UTC(), issue.ResolvedAt, issue.CreatedAt.UTC(), issue.UpdatedAt.UTC())
	return err
}

func (repository *IssueRepository) updateIssue(ctx context.Context, tx pgx.Tx, issue domain.Issue) error {
	if issue.Version < 2 {
		return errors.New("health issue update version is invalid")
	}
	proposalVersions, err := proposalObjectVersions(issue.Proposal)
	if err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_issue SET fingerprint_schema_version=$3,fingerprint=$4,detector_version=$5,severity=$6,evidence_summary=$7,status=$8,status_reason=$9,deferred_until=$10,repair_proposal_id=$11,repair_option_code=$12,repair_binding_fingerprint=$13,repair_binding_object_versions=$14::jsonb,repair_proposal_created_at=$15,version=$16,last_detected_at=$17,last_verified_at=$18,resolved_at=$19,updated_at=$20 WHERE workspace_id=$1 AND id=$2 AND version=$21`, string(issue.WorkspaceID), string(issue.ID), issueFingerprintSchemaVersion, issue.Fingerprint, issue.DetectorVersion, string(issue.Severity), issue.EvidenceSummary, string(issue.Status), nullableString(issue.StatusReason), issue.DeferredUntil, proposalID(issue.Proposal), proposalRepairOption(issue.Proposal), proposalFingerprint(issue.Proposal), proposalVersions, proposalCreatedAt(issue.Proposal), issue.Version, issue.LastDetectedAt.UTC(), issue.LastVerifiedAt.UTC(), issue.ResolvedAt, issue.UpdatedAt.UTC(), issue.Version-1)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIssueTransitionInvalid, false, errors.New("health issue update expected version did not match"))
	}
	return nil
}

func (repository *IssueRepository) updateLastVerifiedAt(ctx context.Context, tx pgx.Tx, issue domain.Issue) error {
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_issue SET last_verified_at=$3 WHERE workspace_id=$1 AND id=$2 AND version=$4`, string(issue.WorkspaceID), string(issue.ID), issue.LastVerifiedAt.UTC(), issue.Version)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIssueTransitionInvalid, false, errors.New("health issue verification expected version did not match"))
	}
	return nil
}

func (repository *IssueRepository) insertObservation(ctx context.Context, tx pgx.Tx, issue domain.Issue, scanID foundation.ID, observation domain.IssueObservation, observedAt time.Time) error {
	observationID, err := repository.generator.New()
	if err != nil {
		return err
	}
	versions, err := json.Marshal(observation.ObjectVersions)
	if err != nil {
		return err
	}
	fingerprint, err := domain.ComputeIssueFingerprint(issue.WorkspaceID, observation)
	if err != nil {
		return err
	}
	evidenceFingerprint := fingerprintEvidence(observation.Evidence)
	var insertedID string
	err = tx.QueryRow(ctx, `INSERT INTO ops.health_issue_observation(id,workspace_id,issue_id,issue_version,scan_id,detector_version,fingerprint_schema_version,fingerprint,evidence_fingerprint,target_versions,severity,observed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(issue_id,fingerprint) DO NOTHING RETURNING id::text`, string(observationID), string(issue.WorkspaceID), string(issue.ID), issue.Version, string(scanID), observation.DetectorVersion, issueFingerprintSchemaVersion, fingerprint, evidenceFingerprint, versions, string(observation.Severity), observedAt.UTC()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for index, evidence := range observation.Evidence {
		evidenceID, idErr := repository.generator.New()
		if idErr != nil {
			return idErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO ops.health_issue_evidence(id,workspace_id,observation_id,evidence_no,ref_type,ref_id,summary,hash,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(observation_id,evidence_no) DO NOTHING`, string(evidenceID), string(issue.WorkspaceID), string(observationID), index+1, string(evidence.Ref.Type), string(evidence.Ref.ID), evidence.Summary, evidence.Hash, observedAt.UTC())
		if err != nil {
			return err
		}
	}
	return nil
}

func decisionHash(decision domain.IssueDecision) (string, error) {
	payload, err := json.Marshal(struct {
		ExpectedVersion int64                      `json:"expected_version"`
		Action          domain.IssueDecisionAction `json:"action"`
		Reason          string                     `json:"reason"`
		DeferredUntil   *time.Time                 `json:"deferred_until"`
		ProposalID      *foundation.ID             `json:"proposal_id"`
		RepairOption    string                     `json:"repair_option_code"`
	}{decision.ExpectedVersion, decision.Action, decision.Reason, decision.DeferredUntil, decision.ProposalID, decision.RepairOptionCode})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("health-decision/v1\n"), payload...))
	return hex.EncodeToString(digest[:]), nil
}

func fingerprintEvidence(evidence []domain.IssueEvidence) string {
	hashes := make([]string, 0, len(evidence))
	for _, item := range evidence {
		hashes = append(hashes, item.Hash)
	}
	sort.Strings(hashes)
	digest := sha256.Sum256([]byte("health-evidence-fingerprint/v1\n" + stringsJoin(hashes, "\n")))
	return hex.EncodeToString(digest[:])
}

func stringsJoin(values []string, separator string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += separator
		}
		result += value
	}
	return result
}

func validRepositoryID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func proposalID(binding *domain.ProposalBinding) any {
	if binding == nil {
		return nil
	}
	return string(binding.ProposalID)
}

func proposalRepairOption(binding *domain.ProposalBinding) any {
	if binding == nil {
		return nil
	}
	return binding.RepairOptionCode
}

func proposalFingerprint(binding *domain.ProposalBinding) any {
	if binding == nil {
		return nil
	}
	return binding.Fingerprint
}

func proposalObjectVersions(binding *domain.ProposalBinding) (any, error) {
	if binding == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(binding.ObjectVersions)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func proposalCreatedAt(binding *domain.ProposalBinding) any {
	if binding == nil {
		return nil
	}
	return binding.CreatedAt.UTC()
}

func issueDatabaseTime(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (time.Time, error) {
	var now time.Time
	if err := queryer.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

func nullableProposalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
