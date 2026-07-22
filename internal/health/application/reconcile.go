package application

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

// ReconcileResult 描述一次 observation 对 Health Issue 事实的确定性结果。
type ReconcileResult struct {
	Issue   domain.Issue
	Created bool
	Outcome domain.ObservationOutcome
}

// ReconcileObservation 创建或更新同一 identity 的 Issue。
// Issue 的 identity/fingerprint 计算和状态迁移仍由 domain 拥有；application 只负责组装和安全边界。
func ReconcileObservation(workspaceID, issueID foundation.ID, existing *domain.Issue, observation domain.IssueObservation, repairOptions []domain.RepairOption, observedAt time.Time) (ReconcileResult, error) {
	if existing == nil {
		if err := domain.ValidateIssueObservation(workspaceID, observation); err != nil {
			return ReconcileResult{}, err
		}
		identity, err := domain.ComputeIssueIdentityHash(workspaceID, observation)
		if err != nil {
			return ReconcileResult{}, err
		}
		fingerprint, err := domain.ComputeIssueFingerprint(workspaceID, observation)
		if err != nil {
			return ReconcileResult{}, err
		}
		if observedAt.IsZero() || !validID(issueID) {
			return ReconcileResult{}, errors.New("new health issue requires id and observation time")
		}
		issue := domain.Issue{ID: issueID, WorkspaceID: workspaceID, Type: observation.Type, Target: observation.Target, DetectorID: observation.DetectorID, DetectorVersion: observation.DetectorVersion, IdentityHash: identity, Fingerprint: fingerprint, Severity: observation.Severity, EvidenceSummary: observation.EvidenceSummary, Evidence: append([]domain.IssueEvidence(nil), observation.Evidence...), ObjectVersions: append([]domain.ObjectVersion(nil), observation.ObjectVersions...), RepairOptions: append([]domain.RepairOption(nil), repairOptions...), Status: domain.IssueStatusOpen, FirstDetectedAt: observedAt, LastDetectedAt: observedAt, LastVerifiedAt: observedAt, Version: 1, CreatedAt: observedAt, UpdatedAt: observedAt}
		if err := domain.ValidateIssue(issue); err != nil {
			return ReconcileResult{}, err
		}
		return ReconcileResult{Issue: issue, Created: true, Outcome: domain.ObservationOutcomeReopened}, nil
	}
	if existing.WorkspaceID != workspaceID {
		return ReconcileResult{}, errors.New("health issue workspace does not match observation workspace")
	}
	if existing.Severity == domain.SeverityCritical && observation.Severity != domain.SeverityCritical {
		observation.Severity = domain.SeverityCritical
	}
	updated, outcome, err := domain.ApplyIssueObservation(*existing, observation, observedAt)
	if err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{Issue: updated, Outcome: outcome}, nil
}

// ReconcileMissingIssue 仅在 detector/scope 完整成功覆盖时自动解决未见 identity 的 Issue。
func ReconcileMissingIssue(issue domain.Issue, scan domain.Scan, seenIdentityHashes map[string]struct{}, resolvedAt time.Time) (domain.Issue, bool, error) {
	return domain.ResolveIssueFromScan(issue, scan, seenIdentityHashes, resolvedAt)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
