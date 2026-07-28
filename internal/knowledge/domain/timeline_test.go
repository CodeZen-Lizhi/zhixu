package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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

func TestKnowledgeEventV2ValidatesOwnerBindings(t *testing.T) {
	artifactEvent := timelineArtifactEventFixture()
	if err := artifactEvent.Validate(); err != nil {
		t.Fatalf("validate artifact event: %v", err)
	}
	reviewEvent := timelineReviewCardEventFixture()
	if err := reviewEvent.Validate(); err != nil {
		t.Fatalf("validate review card event: %v", err)
	}

	operatorID := timelineDomainID(5)
	tests := []struct {
		name   string
		event  KnowledgeEvent
		mutate func(*KnowledgeEvent)
	}{
		{name: "v1 owner event", event: artifactEvent, mutate: func(event *KnowledgeEvent) { event.SchemaVersion = KnowledgeEventSchemaVersion }},
		{name: "system operator identity", event: artifactEvent, mutate: func(event *KnowledgeEvent) { event.Operator.ID = &operatorID }},
		{name: "artifact aggregate mismatch", event: artifactEvent, mutate: func(event *KnowledgeEvent) { event.AggregateType = TimelineAggregateReviewCard }},
		{name: "artifact owner mismatch", event: artifactEvent, mutate: func(event *KnowledgeEvent) { event.OwnerBinding.Artifact.ArtifactID = timelineDomainID(5) }},
		{name: "review status not invalidated", event: reviewEvent, mutate: func(event *KnowledgeEvent) { event.OwnerBinding.ReviewCard.Status = "APPROVED" }},
		{name: "review owner union collision", event: reviewEvent, mutate: func(event *KnowledgeEvent) { event.OwnerBinding.Artifact = artifactImpactBindingFixture() }},
		{name: "legacy event with owner binding", event: artifactEvent, mutate: func(event *KnowledgeEvent) {
			event.EventType = EventConflictResolved
			event.AggregateType = TimelineAggregateConflict
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.event
			candidate.Operator = cloneEventOperator(test.event.Operator)
			candidate.OwnerBinding = cloneEventOwnerBinding(test.event.OwnerBinding)
			test.mutate(&candidate)
			assertTimelineInvalid(t, candidate.Validate())
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

func TestImpactFingerprintV2IsOrderIndependentAndDomainSeparated(t *testing.T) {
	artifact := impactArtifactObjectFixture()
	reviewCard := impactReviewCardObjectFixture()
	objects := []ImpactObject{artifact, reviewCard}
	reversed := []ImpactObject{reviewCard, artifact}

	left, err := ComputeImpactFingerprintForVersion(ImpactAnalysisVersionV2, timelineDomainID(3), 7, objects)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ComputeImpactFingerprintForVersion(ImpactAnalysisVersionV2, timelineDomainID(3), 7, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("v2 fingerprint must be stable: %q != %q", left, right)
	}

	legacyObject := impactObjectFixture(ImpactObjectConflict, timelineDomainID(4), ImpactActionResolveConflict, true)
	v1, err := ComputeImpactFingerprint(timelineDomainID(3), 7, []ImpactObject{legacyObject})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := ComputeImpactFingerprintForVersion(ImpactAnalysisVersionV2, timelineDomainID(3), 7, []ImpactObject{legacyObject})
	if err != nil {
		t.Fatal(err)
	}
	if v1 == v2 {
		t.Fatal("v1 and v2 fingerprints must be domain separated")
	}
	if _, err := ComputeImpactFingerprintForVersion("impact-analysis/future", timelineDomainID(3), 7, objects); err == nil {
		t.Fatal("unknown analysis version must be rejected")
	}
}

func TestImpactObjectValidatesOwnerSpecificBindings(t *testing.T) {
	artifact := impactArtifactObjectFixture()
	if err := ValidateImpactObject(artifact); err != nil {
		t.Fatalf("validate artifact impact: %v", err)
	}
	reviewCard := impactReviewCardObjectFixture()
	if err := ValidateImpactObject(reviewCard); err != nil {
		t.Fatalf("validate review card impact: %v", err)
	}

	tests := []struct {
		name   string
		object ImpactObject
		mutate func(*ImpactObject)
	}{
		{name: "artifact id drift", object: artifact, mutate: func(object *ImpactObject) { object.ArtifactBinding.ArtifactID = timelineDomainID(5) }},
		{name: "artifact version drift", object: artifact, mutate: func(object *ImpactObject) { object.Version++ }},
		{name: "artifact invalid hash", object: artifact, mutate: func(object *ImpactObject) {
			object.ArtifactBinding.ContentHash = strings.ToUpper(object.ArtifactBinding.ContentHash)
		}},
		{name: "artifact unsupported action", object: artifact, mutate: func(object *ImpactObject) { object.Action = ImpactActionReview }},
		{name: "review claim collision", object: reviewCard, mutate: func(object *ImpactObject) { object.ReviewCardBinding.ClaimID = object.ID }},
		{name: "review unknown status", object: reviewCard, mutate: func(object *ImpactObject) { object.ReviewCardBinding.Status = "ARCHIVED" }},
		{name: "review missing proposal", object: reviewCard, mutate: func(object *ImpactObject) { object.RequiresProposal = false }},
		{name: "legacy object with owner binding", object: artifact, mutate: func(object *ImpactObject) {
			object.Type = ImpactObjectConflict
			object.Action = ImpactActionResolveConflict
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneImpactObject(test.object)
			test.mutate(&candidate)
			assertTimelineInvalid(t, ValidateImpactObject(candidate))
		})
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

func TestImpactReportV2ValidatesVersionAndSupersession(t *testing.T) {
	objects := []ImpactObject{impactArtifactObjectFixture(), impactReviewCardObjectFixture()}
	fingerprint, err := ComputeImpactFingerprintForVersion(ImpactAnalysisVersionV2, timelineDomainID(3), 7, objects)
	if err != nil {
		t.Fatal(err)
	}
	previousID := timelineDomainID(5)
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	report := ImpactReport{
		ID: timelineDomainID(6), WorkspaceID: timelineDomainID(1), SourceEventID: timelineDomainID(3), SourceEventRef: "conflict:resolved:7",
		SourceVersion: 7, AnalysisVersion: ImpactAnalysisVersionV2, SupersedesReportID: &previousID,
		Status: ImpactReportReady, Objects: objects, Summary: SummarizeImpactObjects(objects), Fingerprint: fingerprint,
		GeneratedAt: now, CreatedAt: now, Version: 1,
	}
	if err := ValidateImpactReport(report); err != nil {
		t.Fatalf("validate v2 report: %v", err)
	}
	if got := report.SchemaVersion(); got != ImpactReportSchemaVersionV2 {
		t.Fatalf("schema version = %q, want %q", got, ImpactReportSchemaVersionV2)
	}

	legacy := impactReportFixture(t)
	legacy.Objects = objects
	legacy.Summary = SummarizeImpactObjects(objects)
	legacy.Fingerprint, err = ComputeImpactFingerprintForVersion(ImpactAnalysisVersionV2, legacy.SourceEventID, legacy.SourceVersion, objects)
	if err != nil {
		t.Fatal(err)
	}
	assertTimelineInvalid(t, ValidateImpactReport(legacy))

	legacy = impactReportFixture(t)
	legacy.SupersedesReportID = &previousID
	assertTimelineInvalid(t, ValidateImpactReport(legacy))

	self := report
	self.SupersedesReportID = &self.ID
	assertTimelineInvalid(t, ValidateImpactReport(self))

	cycle := report
	cycle.SupersededByReportID = &previousID
	assertTimelineInvalid(t, ValidateImpactReport(cycle))
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

func timelineArtifactEventFixture() KnowledgeEvent {
	event := timelineEventFixture()
	aggregateID := timelineDomainID(4)
	event.EventType = EventArtifactGenerated
	event.AggregateType = TimelineAggregateArtifact
	event.AggregateID = &aggregateID
	event.SourceEventRef = "artifact-generated:4:5:v2"
	event.SourceRef = "artifact:4"
	event.EventVersion = 2
	event.SchemaVersion = KnowledgeEventSchemaVersionV2
	event.Operator = &EventOperator{Type: EventOperatorSystem}
	event.OwnerBinding = &EventOwnerBinding{Artifact: artifactImpactBindingFixture()}
	return event
}

func timelineReviewCardEventFixture() KnowledgeEvent {
	event := timelineEventFixture()
	aggregateID := timelineDomainID(4)
	event.EventType = EventReviewCardInvalidated
	event.AggregateType = TimelineAggregateReviewCard
	event.AggregateID = &aggregateID
	event.SourceEventRef = "review-card-invalidated:4:v3"
	event.SourceRef = "review-card:4"
	event.EventVersion = 3
	event.SchemaVersion = KnowledgeEventSchemaVersionV2
	event.Operator = &EventOperator{Type: EventOperatorUnknown}
	event.OwnerBinding = &EventOwnerBinding{ReviewCard: reviewCardImpactBindingFixture()}
	return event
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

func impactArtifactObjectFixture() ImpactObject {
	binding := artifactImpactBindingFixture()
	return ImpactObject{
		Type: ImpactObjectArtifact, ID: binding.ArtifactID, WorkspaceID: timelineDomainID(1), Version: binding.ArtifactVersion,
		Action: ImpactActionRegenerateArtifact, Reason: "artifact citation depends on the changed source", RequiresProposal: true,
		ArtifactBinding: binding,
	}
}

func impactReviewCardObjectFixture() ImpactObject {
	binding := reviewCardImpactBindingFixture()
	return ImpactObject{
		Type: ImpactObjectReviewCard, ID: binding.CardID, WorkspaceID: timelineDomainID(1), Version: binding.CardVersion,
		Action: ImpactActionRevalidateReviewCard, Reason: "review card evidence depends on the changed claim", RequiresProposal: true,
		ReviewCardBinding: binding,
	}
}

func artifactImpactBindingFixture() *ArtifactImpactBinding {
	return &ArtifactImpactBinding{
		ArtifactID: timelineDomainID(4), ArtifactVersion: 2, RevisionID: timelineDomainID(5), RevisionNo: 3,
		ContentHash: strings.Repeat("a", 64),
	}
}

func reviewCardImpactBindingFixture() *ReviewCardImpactBinding {
	return &ReviewCardImpactBinding{
		CardID: timelineDomainID(4), CardVersion: 3, Status: "INVALIDATED", Fingerprint: strings.Repeat("b", 64),
		ClaimID: timelineDomainID(5), EvidenceBindingFingerprint: strings.Repeat("c", 64),
	}
}

func cloneImpactObject(object ImpactObject) ImpactObject {
	clone := object
	if object.ArtifactBinding != nil {
		binding := *object.ArtifactBinding
		clone.ArtifactBinding = &binding
	}
	if object.ReviewCardBinding != nil {
		binding := *object.ReviewCardBinding
		clone.ReviewCardBinding = &binding
	}
	return clone
}

func cloneEventOperator(operator *EventOperator) *EventOperator {
	if operator == nil {
		return nil
	}
	clone := *operator
	if operator.ID != nil {
		id := *operator.ID
		clone.ID = &id
	}
	return &clone
}

func cloneEventOwnerBinding(binding *EventOwnerBinding) *EventOwnerBinding {
	if binding == nil {
		return nil
	}
	clone := &EventOwnerBinding{}
	if binding.Artifact != nil {
		artifact := *binding.Artifact
		clone.Artifact = &artifact
	}
	if binding.ReviewCard != nil {
		reviewCard := *binding.ReviewCard
		clone.ReviewCard = &reviewCard
	}
	return clone
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
