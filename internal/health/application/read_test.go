package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestIssueCursorBindsWorkspaceFiltersAndLimit(t *testing.T) {
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	workspace := foundation.ID("10000000-0000-4000-8000-000000000001")
	request := IssueListRequest{WorkspaceID: workspace, Limit: 10, Statuses: []domain.IssueStatus{domain.IssueStatusOpen}, Severities: []domain.Severity{domain.SeverityHigh}}
	position := IssueListPosition{UpdatedAt: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC), ID: foundation.ID("10000000-0000-4000-8000-000000000002")}
	raw, err := codec.Encode(cursorDocument{Binding: makeCursorBinding(request, request.Limit), Position: position})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode(raw, makeCursorBinding(request, request.Limit)); err != nil {
		t.Fatal(err)
	}
	other := request
	other.WorkspaceID = foundation.ID("10000000-0000-4000-8000-000000000003")
	if _, err := codec.Decode(raw, makeCursorBinding(other, other.Limit)); err == nil {
		t.Fatal("expected cross workspace cursor rejection")
	}
	if _, err := codec.Decode(raw[:len(raw)-1]+"A", makeCursorBinding(request, request.Limit)); err == nil {
		t.Fatal("expected tampered cursor rejection")
	}
}

func TestIssueReadServiceBoundsListAndRejectsInvalidFilters(t *testing.T) {
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeIssueReadPort{}
	service, err := NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	workspace := foundation.ID("10000000-0000-4000-8000-000000000001")
	if _, err := service.ListIssues(context.Background(), IssueListRequest{WorkspaceID: workspace, Limit: MaxIssueListLimit + 1}); err == nil {
		t.Fatal("expected bounded limit rejection")
	}
	if _, err := service.ListIssues(context.Background(), IssueListRequest{WorkspaceID: workspace, Statuses: []domain.IssueStatus{domain.IssueStatus("bad")}}); err == nil {
		t.Fatal("expected invalid status rejection")
	}
}

func TestIssueReadServiceAcceptsSevenConsecutiveUTCTrendDays(t *testing.T) {
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	workspace := foundation.ID("10000000-0000-4000-8000-000000000001")
	reader := &fakeIssueReadPort{summary: HealthSummary{WorkspaceID: workspace, Trend: trendFixture(time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC))}}
	service, err := NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := service.GetHealthSummary(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Trend) != HealthTrendDays || summary.Trend[0].Date != "2026-07-16" || summary.Trend[6].Date != "2026-07-22" {
		t.Fatalf("trend=%#v", summary.Trend)
	}
}

func TestIssueReadServiceRejectsMalformedTrendProjection(t *testing.T) {
	workspace := foundation.ID("10000000-0000-4000-8000-000000000001")
	valid := trendFixture(time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC))
	tests := []struct {
		name  string
		trend []HealthTrendPoint
	}{
		{name: "empty", trend: nil},
		{name: "too short", trend: valid[:HealthTrendDays-1]},
		{name: "invalid date", trend: append([]HealthTrendPoint(nil), valid...)},
		{name: "non consecutive", trend: append([]HealthTrendPoint(nil), valid...)},
		{name: "negative detected", trend: append([]HealthTrendPoint(nil), valid...)},
		{name: "negative resolved", trend: append([]HealthTrendPoint(nil), valid...)},
	}
	tests[2].trend[3].Date = "2026-02-30"
	tests[3].trend[3].Date = "2026-07-20"
	tests[4].trend[3].DetectedCount = -1
	tests[5].trend[3].ResolvedCount = -1
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewIssueReadService(&fakeIssueReadPort{summary: HealthSummary{WorkspaceID: workspace, Trend: test.trend}}, codec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.GetHealthSummary(context.Background(), workspace); err == nil {
				t.Fatal("expected malformed trend rejection")
			}
		})
	}
}

func trendFixture(start time.Time) []HealthTrendPoint {
	result := make([]HealthTrendPoint, 0, HealthTrendDays)
	for offset := 0; offset < HealthTrendDays; offset++ {
		result = append(result, HealthTrendPoint{Date: start.AddDate(0, 0, offset).Format("2006-01-02"), DetectedCount: int64(offset), ResolvedCount: int64(offset / 2)})
	}
	return result
}

type fakeIssueReadPort struct{ summary HealthSummary }

func (fakeIssueReadPort) ListIssues(context.Context, IssueListQuery) (IssueListResult, error) {
	return IssueListResult{}, nil
}
func (fakeIssueReadPort) GetIssueDetail(context.Context, foundation.ID, foundation.ID) (IssueDetail, error) {
	return IssueDetail{}, nil
}
func (fake fakeIssueReadPort) GetHealthSummary(context.Context, foundation.ID) (HealthSummary, error) {
	return fake.summary, nil
}
