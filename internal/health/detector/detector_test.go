package detector

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

type fakeReader struct{}

func (fakeReader) Find(context.Context, string, healthapp.PageRequest) (healthapp.Page, error) {
	return healthapp.Page{Findings: []healthapp.FindingFact{{Target: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: foundation.ID("10000000-0000-4000-8000-000000000002")}, TargetVersion: 3, Summary: "low confidence"}}}, nil
}

func TestDefaultRegistryHasStableAvailableAndUnavailableEntries(t *testing.T) {
	registry, err := NewDefaultRegistry(fakeReader{}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 10 {
		t.Fatalf("descriptor count = %d", len(descriptors))
	}
	for i := 1; i < len(descriptors); i++ {
		if descriptors[i-1].ID >= descriptors[i].ID {
			t.Fatalf("descriptors are not sorted: %#v", descriptors)
		}
	}
	coverage := registry.Coverage(healthapp.Scope{WorkspaceID: foundation.ID("10000000-0000-4000-8000-000000000001"), Type: domain.ScanScopeTypeSmartCollection})
	for _, item := range coverage {
		switch item.DetectorID {
		case DetectorIndexError, DetectorReviewInvalidated:
			if item.Status != domain.DetectorCoverageStatusUnavailable || item.UnavailableReason == "" {
				t.Fatalf("unavailable smart collection coverage = %#v", item)
			}
		default:
			if item.Status != domain.DetectorCoverageStatusPending {
				t.Fatalf("supported smart collection coverage = %#v", item)
			}
		}
	}
	topicCoverage := registry.Coverage(healthapp.Scope{WorkspaceID: foundation.ID("10000000-0000-4000-8000-000000000001"), Type: domain.ScanScopeTypeTopic})
	for _, item := range topicCoverage {
		switch item.DetectorID {
		case DetectorIndexError:
			if item.Status != domain.DetectorCoverageStatusUnavailable || item.UnavailableReason == "" {
				t.Fatalf("index detector topic coverage = %#v", item)
			}
		case DetectorReviewInvalidated:
			if item.Status != domain.DetectorCoverageStatusUnavailable || item.UnavailableReason == "" {
				t.Fatalf("review detector topic coverage = %#v", item)
			}
		default:
			if item.Status != domain.DetectorCoverageStatusPending {
				t.Fatalf("supported detector topic coverage = %#v", item)
			}
		}
	}
	if !DefaultSupportsScope(DetectorIndexError, domain.ScanScopeTypeWorkspace) || DefaultSupportsScope(DetectorIndexError, domain.ScanScopeTypeTopic) || DefaultSupportsScope(DetectorIndexError, domain.ScanScopeTypeSmartCollection) {
		t.Fatal("index detector scope capability is inconsistent")
	}
}

func TestFindingToObservationIsDeterministicAndDomainValid(t *testing.T) {
	registry, err := NewDefaultRegistry(fakeReader{}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	detector, err := registry.Get(DetectorLowConfidence)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	finding := healthapp.FindingFact{Target: domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: foundation.ID("10000000-0000-4000-8000-000000000002")}, TargetVersion: 3, Summary: "low confidence"}
	a, err := FindingToObservation(detector.Descriptor(), workspaceID, finding)
	if err != nil {
		t.Fatal(err)
	}
	b, err := FindingToObservation(detector.Descriptor(), workspaceID, finding)
	if err != nil || a.Evidence[0].Hash != b.Evidence[0].Hash {
		t.Fatalf("non-deterministic observation: a=%#v b=%#v err=%v", a, b, err)
	}
	if err := domain.ValidateIssueObservation(workspaceID, a); err != nil {
		t.Fatalf("observation rejected by domain: %v", err)
	}
}
