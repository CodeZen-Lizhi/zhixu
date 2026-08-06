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

func TestIssueHistoryCursorBindsWorkspaceIssueKindAndLimit(t *testing.T) {
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	request := IssueHistoryRequest{WorkspaceID: testID(1), IssueID: testID(2), Limit: 10}
	position := IssueHistoryPosition{At: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC), ID: testID(3)}
	binding := makeHistoryCursorBinding(request, issueObservationHistory, request.Limit)
	raw, err := codec.encodeHistory(historyCursorDocument{Binding: binding, Position: position})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.decodeHistory(raw, binding)
	if err != nil || !decoded.At.Equal(position.At) || decoded.ID != position.ID {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	bindings := []historyCursorBinding{
		makeHistoryCursorBinding(IssueHistoryRequest{WorkspaceID: testID(4), IssueID: request.IssueID}, issueObservationHistory, request.Limit),
		makeHistoryCursorBinding(IssueHistoryRequest{WorkspaceID: request.WorkspaceID, IssueID: testID(4)}, issueObservationHistory, request.Limit),
		makeHistoryCursorBinding(request, issueDecisionHistory, request.Limit),
		makeHistoryCursorBinding(request, issueObservationHistory, request.Limit+1),
	}
	for _, other := range bindings {
		if _, err := codec.decodeHistory(raw, other); err == nil {
			t.Fatalf("expected cursor binding rejection for %+v", other)
		}
	}
	if _, err := codec.decodeHistory(raw[:len(raw)-1]+"A", binding); err == nil {
		t.Fatal("expected tampered history cursor rejection")
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

func TestIssueReadServicePaginatesObservationHistoryWithOpaqueCursor(t *testing.T) {
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	reader := &fakeIssueReadPort{issue: domain.Issue{ID: testID(2), WorkspaceID: testID(1)}}
	reader.observationResults = []IssueObservationResult{{
		Items: []IssueObservationRecord{
			{ID: testID(3), ObservedAt: now},
			{ID: testID(4), ObservedAt: now.Add(-time.Minute)},
		},
		HasMore: true,
		Next:    &IssueHistoryPosition{At: now.Add(-time.Minute), ID: testID(4)},
	}, {
		Items: []IssueObservationRecord{{ID: testID(5), ObservedAt: now.Add(-2 * time.Minute)}},
	}}
	service, err := NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	request := IssueHistoryRequest{WorkspaceID: testID(1), IssueID: testID(2), Limit: 2}
	first, err := service.ListIssueObservations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" || first.WorkspaceID != request.WorkspaceID || first.IssueID != request.IssueID {
		t.Fatalf("first=%+v", first)
	}
	request.Cursor = first.NextCursor
	second, err := service.ListIssueObservations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.HasMore || second.NextCursor != "" || len(reader.observationQueries) != 2 || reader.observationQueries[1].After == nil || reader.observationQueries[1].After.ID != testID(4) {
		t.Fatalf("second=%+v queries=%+v", second, reader.observationQueries)
	}
	if reader.getIssueCalls != 2 {
		t.Fatalf("issue existence checks=%d want=2", reader.getIssueCalls)
	}
}

func TestIssueReadServicePaginatesDecisionHistoryWithOpaqueCursor(t *testing.T) {
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	reader := &fakeIssueReadPort{issue: domain.Issue{ID: testID(2), WorkspaceID: testID(1)}}
	reader.decisionResults = []IssueDecisionResult{{
		Items:   []IssueDecisionRecord{{ID: testID(3), CreatedAt: now}, {ID: testID(4), CreatedAt: now.Add(-time.Minute)}},
		HasMore: true,
		Next:    &IssueHistoryPosition{At: now.Add(-time.Minute), ID: testID(4)},
	}, {Items: []IssueDecisionRecord{{ID: testID(5), CreatedAt: now.Add(-2 * time.Minute)}}}}
	service, err := NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	request := IssueHistoryRequest{WorkspaceID: testID(1), IssueID: testID(2), Limit: 2}
	first, err := service.ListIssueDecisions(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor = first.NextCursor
	second, err := service.ListIssueDecisions(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.HasMore || len(reader.decisionQueries) != 2 || reader.decisionQueries[1].After == nil || reader.decisionQueries[1].After.ID != testID(4) {
		t.Fatalf("first=%+v second=%+v queries=%+v", first, second, reader.decisionQueries)
	}
}

func TestIssueReadServiceEnforcesHistoryLimitBoundaries(t *testing.T) {
	for _, limit := range []int{0, 1, MaxIssueHistoryLimit, -1, MaxIssueHistoryLimit + 1} {
		for _, kind := range []issueHistoryKind{issueObservationHistory, issueDecisionHistory} {
			t.Run(string(kind)+"/"+strings.ReplaceAll(time.Duration(limit).String(), "-", "negative-"), func(t *testing.T) {
				reader := &fakeIssueReadPort{issue: domain.Issue{ID: testID(2), WorkspaceID: testID(1)}}
				codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
				if err != nil {
					t.Fatal(err)
				}
				service, err := NewIssueReadService(reader, codec)
				if err != nil {
					t.Fatal(err)
				}
				request := IssueHistoryRequest{WorkspaceID: testID(1), IssueID: testID(2), Limit: limit}
				if kind == issueObservationHistory {
					_, err = service.ListIssueObservations(context.Background(), request)
				} else {
					_, err = service.ListIssueDecisions(context.Background(), request)
				}
				invalid := limit < 0 || limit > MaxIssueHistoryLimit
				if invalid != (err != nil) {
					t.Fatalf("limit=%d kind=%s err=%v", limit, kind, err)
				}
				if invalid {
					return
				}
				want := limit
				if want == 0 {
					want = DefaultIssueHistoryLimit
				}
				if kind == issueObservationHistory && (len(reader.observationQueries) != 1 || reader.observationQueries[0].Limit != want) {
					t.Fatalf("observation queries=%+v want limit=%d", reader.observationQueries, want)
				}
				if kind == issueDecisionHistory && (len(reader.decisionQueries) != 1 || reader.decisionQueries[0].Limit != want) {
					t.Fatalf("decision queries=%+v want limit=%d", reader.decisionQueries, want)
				}
			})
		}
	}
}

func TestIssueReadServiceRejectsMalformedHistoryProjection(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	position := func(at time.Time, id foundation.ID) *IssueHistoryPosition {
		return &IssueHistoryPosition{At: at, ID: id}
	}
	tests := []struct {
		name   string
		limit  int
		result IssueObservationResult
	}{
		{name: "over limit", limit: 1, result: IssueObservationResult{Items: []IssueObservationRecord{{ID: testID(3), ObservedAt: now}, {ID: testID(4), ObservedAt: now.Add(-time.Minute)}}}},
		{name: "newer item after older", limit: 2, result: IssueObservationResult{Items: []IssueObservationRecord{{ID: testID(3), ObservedAt: now.Add(-time.Minute)}, {ID: testID(4), ObservedAt: now}}}},
		{name: "same timestamp id not ascending", limit: 2, result: IssueObservationResult{Items: []IssueObservationRecord{{ID: testID(4), ObservedAt: now}, {ID: testID(3), ObservedAt: now}}}},
		{name: "more without next", limit: 1, result: IssueObservationResult{Items: []IssueObservationRecord{{ID: testID(3), ObservedAt: now}}, HasMore: true}},
		{name: "next without more", limit: 1, result: IssueObservationResult{Items: []IssueObservationRecord{{ID: testID(3), ObservedAt: now}}, Next: position(now, testID(3))}},
		{name: "mismatched next", limit: 1, result: IssueObservationResult{Items: []IssueObservationRecord{{ID: testID(3), ObservedAt: now}}, HasMore: true, Next: position(now, testID(4))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeIssueReadPort{issue: domain.Issue{ID: testID(2), WorkspaceID: testID(1)}, observationResults: []IssueObservationResult{test.result}}
			codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewIssueReadService(reader, codec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ListIssueObservations(context.Background(), IssueHistoryRequest{WorkspaceID: testID(1), IssueID: testID(2), Limit: test.limit}); err == nil {
				t.Fatal("expected malformed repository projection rejection")
			}
		})
	}
}

func TestIssueReadServiceBuildsBoundedDetailFromSharedHistoryPages(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	issue := issueFixture(t, now)
	observation := IssueObservationRecord{
		ID: testID(30), IssueVersion: issue.Version, ScanID: testID(31), DetectorVersion: issue.DetectorVersion,
		Fingerprint: issue.Fingerprint, EvidenceFingerprint: strings.Repeat("d", 64), TargetVersions: issue.ObjectVersions,
		Severity: issue.Severity, ObservedAt: now, Evidence: issue.Evidence,
	}
	historyObservation := observation
	historyObservation.ID = testID(33)
	historyObservation.Fingerprint = strings.Repeat("e", 64)
	decision := IssueDecisionRecord{ID: testID(32), IssueVersion: issue.Version, IdempotencyKey: "decision-1", Action: domain.IssueDecisionAcknowledge, CreatedAt: now.Add(-time.Minute)}
	reader := &fakeIssueReadPort{
		issue:             issue,
		latestObservation: observation,
		observationResults: []IssueObservationResult{{Items: []IssueObservationRecord{historyObservation}, HasMore: true,
			Next: &IssueHistoryPosition{At: historyObservation.ObservedAt, ID: historyObservation.ID}}},
		decisionResults: []IssueDecisionResult{{Items: []IssueDecisionRecord{decision}, HasMore: true,
			Next: &IssueHistoryPosition{At: decision.CreatedAt, ID: decision.ID}}},
	}
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.GetIssueDetail(context.Background(), issue.WorkspaceID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.LatestObservation.ID != observation.ID || len(detail.Observations) != 1 || detail.Observations[0].ID != historyObservation.ID || len(detail.Decisions) != 1 ||
		!detail.ObservationsHasMore || detail.ObservationsNextCursor == "" || !detail.DecisionsHasMore || detail.DecisionsNextCursor == "" {
		t.Fatalf("detail=%+v", detail)
	}
	if reader.getIssueCalls != 1 || len(reader.observationQueries) != 1 || len(reader.decisionQueries) != 1 ||
		reader.observationQueries[0].Limit != DefaultIssueHistoryLimit || reader.decisionQueries[0].Limit != DefaultIssueHistoryLimit {
		t.Fatalf("calls issue=%d observation=%+v decision=%+v", reader.getIssueCalls, reader.observationQueries, reader.decisionQueries)
	}
}

func TestIssueReadServiceRejectsStaleCurrentObservationBindings(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	issue := issueFixture(t, now)
	current := IssueObservationRecord{ID: testID(30), IssueVersion: issue.Version, ScanID: testID(31), DetectorVersion: issue.DetectorVersion, Fingerprint: issue.Fingerprint, EvidenceFingerprint: strings.Repeat("d", 64), TargetVersions: issue.ObjectVersions, Severity: issue.Severity, ObservedAt: now, Evidence: issue.Evidence}
	tests := []struct {
		name   string
		mutate func(*IssueObservationRecord)
	}{
		{name: "missing identity", mutate: func(value *IssueObservationRecord) { value.ID = "" }},
		{name: "future issue version", mutate: func(value *IssueObservationRecord) { value.IssueVersion++ }},
		{name: "fingerprint", mutate: func(value *IssueObservationRecord) { value.Fingerprint = strings.Repeat("e", 64) }},
		{name: "detector", mutate: func(value *IssueObservationRecord) { value.DetectorVersion = "detector/v2" }},
		{name: "severity", mutate: func(value *IssueObservationRecord) { value.Severity = domain.SeverityHigh }},
		{name: "evidence", mutate: func(value *IssueObservationRecord) {
			value.Evidence = append([]domain.IssueEvidence(nil), value.Evidence...)
			value.Evidence[0].Summary = "stale"
		}},
		{name: "object version", mutate: func(value *IssueObservationRecord) {
			value.TargetVersions = append([]domain.ObjectVersion(nil), value.TargetVersions...)
			value.TargetVersions[0].Version++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stale := current
			test.mutate(&stale)
			reader := &fakeIssueReadPort{issue: issue, latestObservation: stale, observationResults: []IssueObservationResult{{Items: []IssueObservationRecord{current}}}}
			codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewIssueReadService(reader, codec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.GetIssueDetail(context.Background(), issue.WorkspaceID, issue.ID); err == nil {
				t.Fatal("expected stale current observation rejection")
			}
		})
	}

	reader := &fakeIssueReadPort{issue: issue, latestObservation: current}
	codec, err := NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetIssueDetail(context.Background(), issue.WorkspaceID, issue.ID); err == nil {
		t.Fatal("expected empty current history rejection")
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

type fakeIssueReadPort struct {
	issue              domain.Issue
	latestObservation  IssueObservationRecord
	summary            HealthSummary
	observationResults []IssueObservationResult
	decisionResults    []IssueDecisionResult
	observationQueries []IssueObservationQuery
	decisionQueries    []IssueDecisionQuery
	getIssueCalls      int
}

func (fakeIssueReadPort) ListIssues(context.Context, IssueListQuery) (IssueListResult, error) {
	return IssueListResult{}, nil
}
func (fake *fakeIssueReadPort) GetIssue(context.Context, foundation.ID, foundation.ID) (IssueSnapshot, error) {
	fake.getIssueCalls++
	return IssueSnapshot{Issue: fake.issue, LatestObservation: fake.latestObservation}, nil
}
func (fake *fakeIssueReadPort) ListIssueObservations(_ context.Context, query IssueObservationQuery) (IssueObservationResult, error) {
	fake.observationQueries = append(fake.observationQueries, query)
	index := len(fake.observationQueries) - 1
	if index >= len(fake.observationResults) {
		return IssueObservationResult{}, nil
	}
	return fake.observationResults[index], nil
}
func (fake *fakeIssueReadPort) ListIssueDecisions(_ context.Context, query IssueDecisionQuery) (IssueDecisionResult, error) {
	fake.decisionQueries = append(fake.decisionQueries, query)
	index := len(fake.decisionQueries) - 1
	if index >= len(fake.decisionResults) {
		return IssueDecisionResult{}, nil
	}
	return fake.decisionResults[index], nil
}
func (fake *fakeIssueReadPort) GetHealthSummary(context.Context, foundation.ID) (HealthSummary, error) {
	return fake.summary, nil
}
