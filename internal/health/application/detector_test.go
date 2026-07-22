package application

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestRegistrySortsDescriptorsAndExposesUnavailableCoverage(t *testing.T) {
	available := Descriptor{ID: "health.detector.a", Version: "detector/v1", IssueType: domain.IssueTypeOrphan, SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow}
	registry, err := NewRegistry([]Detector{descriptorDetector{available}}, []Descriptor{{ID: "health.detector.review_invalidated", Version: "detector/v1", IssueType: domain.IssueTypeReviewInvalidated, UnavailableReason: "review owner is unavailable"}})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 2 || descriptors[0].ID != "health.detector.a" || descriptors[1].Available {
		t.Fatalf("descriptors = %#v", descriptors)
	}
	coverage := registry.Coverage(Scope{WorkspaceID: foundation.ID("10000000-0000-4000-8000-000000000001"), Type: domain.ScanScopeTypeWorkspace})
	if len(coverage) != 2 || coverage[1].Status != domain.DetectorCoverageStatusUnavailable || coverage[1].UnavailableReason == "" {
		t.Fatalf("coverage = %#v", coverage)
	}
	if _, err := registry.Get("health.detector.review_invalidated"); err == nil {
		t.Fatal("unavailable detector unexpectedly executable")
	}
}

type descriptorDetector struct{ descriptor Descriptor }

func (detector descriptorDetector) Descriptor() Descriptor { return detector.descriptor }
func (detector descriptorDetector) ScanPage(context.Context, PageRequest) (Page, error) {
	return Page{}, nil
}
