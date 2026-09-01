//go:build integration

package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func repositoryObservation(topicID foundation.ID, hash, summary string) domain.IssueObservation {
	ref := domain.ObjectRef{Type: domain.ObjectTypeTopic, ID: topicID}
	return domain.IssueObservation{
		Type:            domain.IssueTypeMissingSource,
		Target:          ref,
		DetectorID:      "health.detector.missing_source",
		DetectorVersion: "detector/v1",
		Severity:        domain.SeverityHigh,
		EvidenceSummary: summary,
		Evidence:        []domain.IssueEvidence{{Ref: ref, Hash: hash, Summary: summary}},
		ObjectVersions:  []domain.ObjectVersion{{Ref: ref, Version: 1}},
	}
}
