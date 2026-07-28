package postgres

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestScanImpactReportRejectsSourceEventVersionDrift(t *testing.T) {
	_, err := scanImpactReport(impactReportRowStub{reportSourceVersion: 1, eventVersion: 2})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != domain.ErrorCodeTimelineInconsistent {
		t.Fatalf("classified=%#v err=%v", classified, err)
	}
}

func TestScanImpactReportAcceptsMatchingSourceEventVersion(t *testing.T) {
	report, err := scanImpactReport(impactReportRowStub{reportSourceVersion: 2, eventVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceVersion != 2 || report.SourceEventRef != "conflict:resolved:2" {
		t.Fatalf("report=%#v", report)
	}
}

func TestScanImpactReportRejectsFingerprintDrift(t *testing.T) {
	_, err := scanImpactReport(impactReportRowStub{
		reportSourceVersion: 2,
		eventVersion:        2,
		fingerprint:         strings.Repeat("f", 64),
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != domain.ErrorCodeTimelineInconsistent {
		t.Fatalf("classified=%#v err=%v", classified, err)
	}
}

func TestScanImpactReportAcceptsV2SupersessionProjection(t *testing.T) {
	predecessor := "10000000-0000-4000-8000-000000000004"
	successor := "10000000-0000-4000-8000-000000000005"
	report, err := scanImpactReport(impactReportRowStub{
		reportSourceVersion:  2,
		eventVersion:         2,
		analysisVersion:      domain.ImpactAnalysisVersionV2,
		schemaVersion:        domain.ImpactReportSchemaVersionV2,
		supersedesReportID:   &predecessor,
		supersededByReportID: &successor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AnalysisVersion != domain.ImpactAnalysisVersionV2 || report.SupersedesReportID == nil || *report.SupersedesReportID != foundation.ID(predecessor) || report.SupersededByReportID == nil || *report.SupersededByReportID != foundation.ID(successor) {
		t.Fatalf("report=%#v", report)
	}
}

type impactReportRowStub struct {
	reportSourceVersion  int64
	eventVersion         int64
	analysisVersion      domain.ImpactAnalysisVersion
	schemaVersion        string
	supersedesReportID   *string
	supersededByReportID *string
	fingerprint          string
}

func (row impactReportRowStub) Scan(destinations ...any) error {
	if len(destinations) != 19 {
		return fmt.Errorf("destination count = %d, want 19", len(destinations))
	}
	if row.analysisVersion == "" {
		row.analysisVersion = domain.ImpactAnalysisVersionV1
	}
	if row.schemaVersion == "" {
		row.schemaVersion = domain.ImpactReportSchemaVersion
	}
	if row.fingerprint == "" {
		fingerprint, err := domain.ComputeImpactFingerprintForVersion(
			row.analysisVersion, foundation.ID("10000000-0000-4000-8000-000000000003"), row.reportSourceVersion, nil,
		)
		if err != nil {
			return err
		}
		row.fingerprint = fingerprint
	}
	now := time.Date(2026, 7, 25, 8, 9, 10, 123456000, time.UTC)
	*destinations[0].(*string) = "10000000-0000-4000-8000-000000000001"
	*destinations[1].(*string) = "10000000-0000-4000-8000-000000000002"
	*destinations[2].(*string) = "10000000-0000-4000-8000-000000000003"
	*destinations[3].(*string) = string(domain.ImpactReportReady)
	*destinations[4].(*string) = "[]"
	*destinations[5].(*string) = "{}"
	*destinations[6].(*time.Time) = now
	*destinations[7].(*string) = row.schemaVersion
	*destinations[8].(*string) = string(row.analysisVersion)
	*destinations[9].(**string) = row.supersedesReportID
	*destinations[10].(**string) = row.supersededByReportID
	*destinations[11].(*int64) = row.reportSourceVersion
	*destinations[12].(*string) = row.fingerprint
	*destinations[13].(**string) = nil
	*destinations[14].(**string) = nil
	*destinations[15].(*int64) = 1
	*destinations[16].(*time.Time) = now
	*destinations[17].(*string) = "conflict:resolved:2"
	*destinations[18].(*int64) = row.eventVersion
	return nil
}
