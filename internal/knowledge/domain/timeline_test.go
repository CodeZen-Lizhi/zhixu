package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestKnowledgeEventValidateAcceptsCanonicalSafeProjection(t *testing.T) {
	event := timelineEventFixture()
	if err := event.Validate(); err != nil {
		t.Fatalf("validate event: %v", err)
	}
}

func TestKnowledgeEventValidateRejectsSecretPathAndInvalidCorrelation(t *testing.T) {
	invalidID := foundation.ID("not-an-id")
	tests := []struct {
		name   string
		mutate func(*KnowledgeEvent)
	}{
		{name: "secret source event", mutate: func(event *KnowledgeEvent) { event.SourceEventRef = "token=plain-text" }},
		{name: "absolute source ref", mutate: func(event *KnowledgeEvent) { event.SourceRef = "/Users/example/private.md" }},
		{name: "secret summary", mutate: func(event *KnowledgeEvent) { event.Summary = "Bearer plain-text" }},
		{name: "bare jwt summary", mutate: func(event *KnowledgeEvent) { event.Summary = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.signature123" }},
		{name: "quoted secret payload", mutate: func(event *KnowledgeEvent) { event.Payload = json.RawMessage(`{"token":"plain-text"}`) }},
		{name: "absolute payload path", mutate: func(event *KnowledgeEvent) { event.Payload = json.RawMessage(`{"path":"/tmp/private.md"}`) }},
		{name: "absolute commit path", mutate: func(event *KnowledgeEvent) { event.Correlation.GitCommitRef = "/tmp/repository" }},
		{name: "invalid correlation id", mutate: func(event *KnowledgeEvent) { event.Correlation.ProposalID = &invalidID }},
		{name: "non canonical payload", mutate: func(event *KnowledgeEvent) { event.Payload = json.RawMessage(`{"z":1,"a":2}`) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := timelineEventFixture()
			test.mutate(&event)
			assertTimelineInvalid(t, event.Validate())
		})
	}
}

func TestKnowledgeEventValidateRejectsUnknownCatalogValuesAndNonCanonicalTimes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*KnowledgeEvent)
	}{
		{name: "unknown event type", mutate: func(event *KnowledgeEvent) { event.EventType = "FUTURE_EVENT" }},
		{name: "unknown aggregate type", mutate: func(event *KnowledgeEvent) { event.AggregateType = "FUTURE_AGGREGATE" }},
		{name: "nanosecond occurred at", mutate: func(event *KnowledgeEvent) { event.OccurredAt = event.OccurredAt.Add(time.Nanosecond) }},
		{name: "non utc created at", mutate: func(event *KnowledgeEvent) { event.CreatedAt = event.CreatedAt.In(time.FixedZone("UTC+8", 8*60*60)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := timelineEventFixture()
			test.mutate(&event)
			assertTimelineInvalid(t, event.Validate())
		})
	}
}

func TestTimelineQueryRejectsTooManyEventTypeFilters(t *testing.T) {
	filters := make([]EventType, MaxTimelineEventTypeFilters+1)
	for index := range filters {
		filters[index] = EventGitCommitted
	}
	query := TimelineQuery{WorkspaceID: timelineDomainID(1), Filter: TimelineFilter{EventTypes: filters}, Limit: 1}
	assertTimelineInvalid(t, ValidateTimelineQuery(query))
}

func TestTimelineQueryRejectsUnknownCatalogFiltersAndNonCanonicalTimes(t *testing.T) {
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	tests := []TimelineQuery{
		{WorkspaceID: timelineDomainID(1), Filter: TimelineFilter{EventTypes: []EventType{"FUTURE_EVENT"}}, Limit: 1},
		{WorkspaceID: timelineDomainID(1), Filter: TimelineFilter{AggregateType: "FUTURE_AGGREGATE"}, Limit: 1},
		{WorkspaceID: timelineDomainID(1), Filter: TimelineFilter{OccurredAfter: timelineTimePointer(now.Add(time.Nanosecond))}, Limit: 1},
	}
	for _, query := range tests {
		assertTimelineInvalid(t, ValidateTimelineQuery(query))
	}
}

func TestImpactFingerprintIsOrderIndependentAndRejectsDuplicateIdentity(t *testing.T) {
	first := impactObjectFixture(ImpactObjectConflict, timelineDomainID(4), ImpactActionResolveConflict, true)
	second := impactObjectFixture(ImpactObjectHealthIssue, timelineDomainID(5), ImpactActionRefreshHealth, false)
	objects := []ImpactObject{first, second}
	reversed := []ImpactObject{second, first}

	left, err := ComputeImpactFingerprint(timelineDomainID(3), 7, objects)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ComputeImpactFingerprint(timelineDomainID(3), 7, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("fingerprint must be stable: %q != %q", left, right)
	}
	if !reflect.DeepEqual(objects, []ImpactObject{first, second}) {
		t.Fatal("fingerprint calculation mutated caller objects")
	}
	if _, err := ComputeImpactFingerprint(timelineDomainID(3), 7, []ImpactObject{first, first}); err == nil {
		t.Fatal("duplicate impact identity must be rejected")
	}
}

func TestImpactReportValidatesEnumsSummaryAndStatusDetails(t *testing.T) {
	report := impactReportFixture(t)
	if err := ValidateImpactReport(report); err != nil {
		t.Fatalf("validate report: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ImpactReport)
	}{
		{name: "summary mismatch", mutate: func(report *ImpactReport) { report.Summary["CONFLICT"]++ }},
		{name: "unknown object type", mutate: func(report *ImpactReport) { report.Objects[0].Type = "UNKNOWN" }},
		{name: "unknown action", mutate: func(report *ImpactReport) { report.Objects[0].Action = "DELETE" }},
		{name: "noop proposal", mutate: func(report *ImpactReport) {
			report.Objects[0].Action = ImpactActionNoop
			report.Objects[0].RequiresProposal = true
		}},
		{name: "stale without reason", mutate: func(report *ImpactReport) { report.Status = ImpactReportStale; report.StaleReason = "" }},
		{name: "ready with stale reason", mutate: func(report *ImpactReport) { report.StaleReason = "outdated" }},
		{name: "nanosecond generated at", mutate: func(report *ImpactReport) { report.GeneratedAt = report.GeneratedAt.Add(time.Nanosecond) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := impactReportFixture(t)
			test.mutate(&candidate)
			assertTimelineInvalid(t, ValidateImpactReport(candidate))
		})
	}
}

func TestProposalDraftRequiresApprovalAndWriteAuthorization(t *testing.T) {
	draft := ProposalDraft{
		WorkspaceID: timelineDomainID(1), SourceEventID: timelineDomainID(3), Operation: string(ImpactActionReview),
		TargetType: ImpactObjectRelation, TargetID: timelineDomainID(4), BaseVersion: 2, Reason: "relation requires review",
		RequiresApproval: true, RequiresWriteAuthorization: true,
	}
	if err := ValidateProposalDraft(draft); err != nil {
		t.Fatalf("validate proposal draft: %v", err)
	}
	draft.RequiresWriteAuthorization = false
	assertTimelineInvalid(t, ValidateProposalDraft(draft))
}

func timelineEventFixture() KnowledgeEvent {
	occurredAt := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	aggregateID := timelineDomainID(2)
	return KnowledgeEvent{
		ID: timelineDomainID(3), WorkspaceID: timelineDomainID(1), EventType: EventConflictResolved,
		AggregateType: TimelineAggregateConflict, AggregateID: &aggregateID, SourceEventRef: "conflict:resolved:7",
		SourceRef: "conflict:2", EventVersion: 7, SchemaVersion: KnowledgeEventSchemaVersion,
		Summary: "conflict resolved", Payload: json.RawMessage(`{"resolution":"approved"}`),
		Correlation: EventCorrelation{GitCommitRef: "0123456789abcdef"}, OccurredAt: occurredAt, CreatedAt: occurredAt.Add(time.Second),
	}
}

func impactReportFixture(t *testing.T) ImpactReport {
	t.Helper()
	objects := []ImpactObject{impactObjectFixture(ImpactObjectConflict, timelineDomainID(4), ImpactActionResolveConflict, true)}
	fingerprint, err := ComputeImpactFingerprint(timelineDomainID(3), 7, objects)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	return ImpactReport{
		ID: timelineDomainID(6), WorkspaceID: timelineDomainID(1), SourceEventID: timelineDomainID(3), SourceEventRef: "conflict:resolved:7",
		SourceVersion: 7, Status: ImpactReportReady, Objects: objects, Summary: SummarizeImpactObjects(objects), Fingerprint: fingerprint,
		GeneratedAt: now, CreatedAt: now, Version: 1,
	}
}

func impactObjectFixture(objectType ImpactObjectType, id foundation.ID, action ImpactAction, requiresProposal bool) ImpactObject {
	return ImpactObject{Type: objectType, ID: id, WorkspaceID: timelineDomainID(1), Version: 2, Action: action, Reason: "downstream fact requires review", RequiresProposal: requiresProposal}
}

func timelineTimePointer(value time.Time) *time.Time { return &value }

func timelineDomainID(seed int) foundation.ID {
	return foundation.ID([]string{
		"00000000-0000-4000-8000-000000000000",
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000004",
		"00000000-0000-4000-8000-000000000005",
		"00000000-0000-4000-8000-000000000006",
	}[seed])
}

func assertTimelineInvalid(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeTimelineInvalid {
		t.Fatalf("error = %v, want %s", err, ErrorCodeTimelineInvalid)
	}
}
