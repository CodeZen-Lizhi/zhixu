// Package detector 实现 Health detector 的确定性 observation 生成。
package detector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const (
	DetectorOrphan            = "health.detector.orphan"
	DetectorDuplicate         = "health.detector.duplicate"
	DetectorConflict          = "health.detector.conflict"
	DetectorStale             = "health.detector.stale"
	DetectorMissingSource     = "health.detector.missing_source"
	DetectorLowConfidence     = "health.detector.low_confidence"
	DetectorBrokenReference   = "health.detector.broken_reference"
	DetectorIndexError        = "health.detector.index_error"
	DetectorSupersededUsage   = "health.detector.superseded_usage"
	DetectorReviewInvalidated = "health.detector.review_invalidated"
	DefaultDetectorVersion    = "detector/v1"
)

// Config 冻结 detector 的版本化规则。
type Config struct {
	LowConfidenceThreshold float64
}

// DefaultConfig 返回保守的首版低置信度阈值。
func DefaultConfig() Config { return Config{LowConfidenceThreshold: 0.5} }

// DefaultSupportedScopes 返回首版 detector 的冻结 scope 能力。
func DefaultSupportedScopes(detectorID string) []domain.ScanScopeType {
	switch detectorID {
	case DetectorIndexError:
		return []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}
	case DetectorOrphan, DetectorDuplicate, DetectorConflict, DetectorStale, DetectorMissingSource,
		DetectorLowConfidence, DetectorBrokenReference, DetectorSupersededUsage, DetectorReviewInvalidated:
		return []domain.ScanScopeType{domain.ScanScopeTypeWorkspace, domain.ScanScopeTypeTopic, domain.ScanScopeTypeSmartCollection}
	default:
		return nil
	}
}

// DefaultSupportsScope 判断首版 detector 是否有指定 scope 的完整对象映射。
func DefaultSupportsScope(detectorID string, scope domain.ScanScopeType) bool {
	for _, supported := range DefaultSupportedScopes(detectorID) {
		if supported == scope {
			return true
		}
	}
	return false
}

type implementation struct {
	descriptor healthapp.Descriptor
	reader     healthapp.FactReader
	config     Config
}

func (item *implementation) Descriptor() healthapp.Descriptor { return item.descriptor }

func (item *implementation) ScanPage(ctx context.Context, request healthapp.PageRequest) (healthapp.Page, error) {
	if item.reader == nil {
		return healthapp.Page{}, healthapp.ErrDetectorUnavailable
	}
	if item.descriptor.ID == DetectorLowConfidence {
		request.LowConfidenceThreshold = item.config.LowConfidenceThreshold
	}
	return item.reader.Find(ctx, item.descriptor.ID, request)
}

// NewDefaultRegistry 构造首版 registry；Review detector 永远以 unavailable coverage 暴露。
func NewDefaultRegistry(reader healthapp.FactReader, config Config) (*healthapp.Registry, error) {
	if config.LowConfidenceThreshold <= 0 || config.LowConfidenceThreshold >= 1 {
		return nil, errors.New("low confidence threshold must be between zero and one")
	}
	topicClaim := []domain.ObjectType{domain.ObjectTypeTopic, domain.ObjectTypeClaim}
	relation := []domain.ObjectType{domain.ObjectTypeRelation}
	all := []healthapp.Detector{
		newImplementation(reader, config, DetectorOrphan, domain.IssueTypeOrphan, topicClaim, domain.SeverityMedium),
		newImplementation(reader, config, DetectorDuplicate, domain.IssueTypeDuplicate, relation, domain.SeverityHigh),
		newImplementation(reader, config, DetectorConflict, domain.IssueTypeConflict, []domain.ObjectType{domain.ObjectTypeConflict}, domain.SeverityHigh),
		newImplementation(reader, config, DetectorStale, domain.IssueTypeStale, relation, domain.SeverityMedium),
		newImplementation(reader, config, DetectorMissingSource, domain.IssueTypeMissingSource, []domain.ObjectType{domain.ObjectTypeClaim, domain.ObjectTypeRelation}, domain.SeverityHigh),
		newImplementation(reader, config, DetectorLowConfidence, domain.IssueTypeLowConfidence, []domain.ObjectType{domain.ObjectTypeClaim, domain.ObjectTypeRelation}, domain.SeverityLow),
		newImplementation(reader, config, DetectorBrokenReference, domain.IssueTypeBrokenReference, []domain.ObjectType{domain.ObjectTypeClaim, domain.ObjectTypeRelation}, domain.SeverityCritical),
		newImplementation(reader, config, DetectorIndexError, domain.IssueTypeIndexError, []domain.ObjectType{domain.ObjectTypeIndexVersion}, domain.SeverityCritical),
		newImplementation(reader, config, DetectorSupersededUsage, domain.IssueTypeSupersededUsage, []domain.ObjectType{domain.ObjectTypeClaim, domain.ObjectTypeRelation}, domain.SeverityMedium),
	}
	unavailable := []healthapp.Descriptor{
		{ID: DetectorReviewInvalidated, Version: DefaultDetectorVersion, IssueType: domain.IssueTypeReviewInvalidated, SupportedScopes: DefaultSupportedScopes(DetectorReviewInvalidated), SupportedTarget: []domain.ObjectType{domain.ObjectTypeClaim}, DefaultSeverity: domain.SeverityHigh, UnavailableReason: "review owner and invalidation schema are not implemented"},
	}
	return healthapp.NewRegistry(all, unavailable)
}

func newImplementation(reader healthapp.FactReader, config Config, id string, issueType domain.IssueType, targets []domain.ObjectType, severity domain.Severity) healthapp.Detector {
	return &implementation{reader: reader, config: config, descriptor: healthapp.Descriptor{ID: id, Version: DefaultDetectorVersion, IssueType: issueType, SupportedScopes: DefaultSupportedScopes(id), SupportedTarget: append([]domain.ObjectType(nil), targets...), DefaultSeverity: severity}}
}

// FindingToObservation 将 adapter fact 转成 domain observation，统一计算确定性证据 hash。
func FindingToObservation(descriptor healthapp.Descriptor, workspaceID foundation.ID, finding healthapp.FindingFact) (domain.IssueObservation, error) {
	if finding.Target.ID == "" || finding.Target.Type == "" || finding.TargetVersion < 1 {
		return domain.IssueObservation{}, errors.New("health finding target is incomplete")
	}
	if !descriptor.SupportsTarget(finding.Target.Type) {
		return domain.IssueObservation{}, fmt.Errorf("health detector target type %s is unsupported", finding.Target.Type)
	}
	severity := finding.Severity
	if severity == "" {
		severity = descriptor.DefaultSeverity
	}
	summary := finding.Summary
	if summary == "" {
		summary = fmt.Sprintf("%s detector found a quality issue", descriptor.ID)
	}
	evidence := make([]domain.IssueEvidence, 0, len(finding.Evidence)+1)
	versions := []domain.ObjectVersion{{Ref: finding.Target, Version: finding.TargetVersion}}
	if len(finding.Evidence) == 0 {
		finding.Evidence = []healthapp.EvidenceFact{{Ref: finding.Target, Summary: summary}}
	}
	for _, item := range finding.Evidence {
		hash := item.Hash
		if hash == "" {
			hash = evidenceHash(workspaceID, descriptor.ID, finding.Target, item.Ref, item.Summary)
		}
		evidence = append(evidence, domain.IssueEvidence{Ref: item.Ref, Hash: hash, Summary: item.Summary})
	}
	return domain.IssueObservation{Type: descriptor.IssueType, Target: finding.Target, DetectorID: descriptor.ID, DetectorVersion: descriptor.Version, Severity: severity, EvidenceSummary: summary, Evidence: evidence, ObjectVersions: versions}, nil
}

func evidenceHash(workspaceID foundation.ID, detectorID string, target, evidence domain.ObjectRef, summary string) string {
	digest := sha256.Sum256([]byte("health-evidence/v1\n" + string(workspaceID) + "\n" + detectorID + "\n" + string(target.Type) + "\n" + string(target.ID) + "\n" + string(evidence.Type) + "\n" + string(evidence.ID) + "\n" + summary))
	return hex.EncodeToString(digest[:])
}
