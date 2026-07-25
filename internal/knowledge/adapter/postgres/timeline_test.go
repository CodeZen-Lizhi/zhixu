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

type impactReportRowStub struct {
	reportSourceVersion int64
	eventVersion        int64
}

func (row impactReportRowStub) Scan(destinations ...any) error {
	if len(destinations) != 16 {
		return fmt.Errorf("destination count = %d, want 16", len(destinations))
	}
	now := time.Date(2026, 7, 25, 8, 9, 10, 123456000, time.UTC)
	*destinations[0].(*string) = "10000000-0000-4000-8000-000000000001"
	*destinations[1].(*string) = "10000000-0000-4000-8000-000000000002"
	*destinations[2].(*string) = "10000000-0000-4000-8000-000000000003"
	*destinations[3].(*string) = string(domain.ImpactReportReady)
	*destinations[4].(*string) = "[]"
	*destinations[5].(*string) = "{}"
	*destinations[6].(*time.Time) = now
	*destinations[7].(*string) = domain.ImpactReportSchemaVersion
	*destinations[8].(*int64) = row.reportSourceVersion
	*destinations[9].(*string) = strings.Repeat("a", 64)
	*destinations[10].(**string) = nil
	*destinations[11].(**string) = nil
	*destinations[12].(*int64) = 1
	*destinations[13].(*time.Time) = now
	*destinations[14].(*string) = "conflict:resolved:2"
	*destinations[15].(*int64) = row.eventVersion
	return nil
}
