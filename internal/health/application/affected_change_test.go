package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestAffectedChangePlannerMapsEverySourceToWorkspaceScope(t *testing.T) {
	planner := affectedChangePlannerFixture(t)
	events := []AffectedChangeEvent{
		affectedChangeEventFixture(AffectedChangeSourceKnowledgeReceipt),
		affectedChangeEventFixture(AffectedChangeSourceIndexVersion),
		affectedChangeEventFixture(AffectedChangeSourceChunkProjection),
		affectedLexicalChangeEventFixture(),
	}
	for _, event := range events {
		t.Run(string(event.SourceKind), func(t *testing.T) {
			request, err := planner.Plan(event, 7)
			if err != nil {
				t.Fatal(err)
			}
			if request.Scope.Type != domain.ScanScopeTypeWorkspace || request.Scope.Ref != event.WorkspaceID || request.Scope.Version != 7 || request.Scope.SchemaVersion != "health-scope/workspace/v1" || request.Scope.Hash != "" {
				t.Fatalf("scope=%#v", request.Scope)
			}
			if request.IdempotencyKey != "health-affected-change:"+string(event.ID) || !request.PreventScopeConcurrency || request.MaxItems != domain.MaxScanItems || len(request.Coverage) != 1 {
				t.Fatalf("request=%#v", request)
			}
		})
	}
}

func TestAffectedChangePlannerRejectsUnsupportedSchemaAndSourceDrift(t *testing.T) {
	planner := affectedChangePlannerFixture(t)
	tests := []struct {
		name string
		edit func(*AffectedChangeEvent)
		code string
	}{
		{name: "schema", edit: func(event *AffectedChangeEvent) { event.SchemaVersion = 2 }, code: ErrorCodeAffectedChangeSchemaUnsupported},
		{name: "event", edit: func(event *AffectedChangeEvent) { event.EventVersion = 2 }, code: ErrorCodeAffectedChangeSchemaUnsupported},
		{name: "source", edit: func(event *AffectedChangeEvent) { event.ChangeStatus = "claim.confirm" }, code: ErrorCodeAffectedChangeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := affectedChangeEventFixture(AffectedChangeSourceKnowledgeReceipt)
			test.edit(&event)
			_, err := planner.Plan(event, 1)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired || classified.Code != test.code {
				t.Fatalf("err=%v classified=%#v", err, classified)
			}
		})
	}
}

func TestAffectedChangeDispatcherStopsAtLimitAndCountsOutcomes(t *testing.T) {
	port := &affectedChangeDispatchFake{results: []AffectedChangeDispatchResult{
		{EventID: testID(61), Outcome: AffectedChangeDispatchPublished},
		{EventID: testID(62), Outcome: AffectedChangeDispatchDeferred},
		{EventID: testID(63), Outcome: AffectedChangeDispatchPublished},
	}}
	dispatcher, err := NewAffectedChangeDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := dispatcher.DispatchBatch(context.Background(), 2)
	if err != nil || batch.Processed != 2 || batch.Published != 1 || batch.Deferred != 1 || batch.Poisoned != 0 || port.calls != 2 {
		t.Fatalf("batch=%#v calls=%d err=%v", batch, port.calls, err)
	}
}

func TestAffectedChangeDispatcherReportsPersistedPoison(t *testing.T) {
	manual := foundation.NewError(foundation.ErrorManualRecoveryRequired, ErrorCodeAffectedChangeSourceBindingInvalid, false, errors.New("drift"))
	port := &affectedChangeDispatchFake{results: []AffectedChangeDispatchResult{{EventID: testID(64), Outcome: AffectedChangeDispatchPoisoned}}, errors: []error{manual}}
	dispatcher, err := NewAffectedChangeDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := dispatcher.DispatchBatch(context.Background(), 3)
	if !errors.Is(err, manual) || batch.Processed != 1 || batch.Poisoned != 1 {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
}

func affectedChangePlannerFixture(t *testing.T) *AffectedChangePlanner {
	t.Helper()
	descriptor := Descriptor{
		ID: "health.detector.affected", Version: "detector/v1", IssueType: domain.IssueTypeOrphan,
		SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace},
		SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow,
	}
	registry, err := NewRegistry([]Detector{descriptorDetector{descriptor}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := NewAffectedChangePlanner(registry)
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func affectedChangeEventFixture(kind AffectedChangeSourceKind) AffectedChangeEvent {
	event := AffectedChangeEvent{
		ID: testID(65), WorkspaceID: testID(66), SchemaVersion: HealthAffectedChangeSchemaVersion,
		EventVersion: HealthAffectedChangeEventVersion, SourceKind: kind, AggregateVersion: 3,
	}
	switch kind {
	case AffectedChangeSourceKnowledgeReceipt:
		event.EventType = AffectedChangeEventKnowledgeChanged
		event.SourceKey = "knowledge-command-1"
		event.SourceHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		event.AggregateType = "RELATION"
		event.AggregateID = testID(67)
		event.ChangeStatus = "relation.confirm"
	case AffectedChangeSourceIndexVersion:
		event.EventType = AffectedChangeEventIndexFailed
		event.AggregateType = "INDEX_VERSION"
		event.AggregateID = testID(68)
		event.SourceKey = string(event.AggregateID)
		event.ChangeStatus = "failed"
		event.ChangeCode = "INDEX_BUILD_FAILED"
	case AffectedChangeSourceChunkProjection:
		event.EventType = AffectedChangeEventVectorDegraded
		event.AggregateType = "CHUNK_PROJECTION"
		event.AggregateID = testID(69)
		event.AggregateSubID = testID(70)
		event.SourceKey = string(event.AggregateID) + ":" + string(event.AggregateSubID)
		event.ChangeStatus = "skipped_oversized"
		event.ChangeCode = "EMBEDDING_INPUT_OVERSIZED"
	}
	return event
}

func affectedLexicalChangeEventFixture() AffectedChangeEvent {
	event := affectedChangeEventFixture(AffectedChangeSourceChunkProjection)
	event.EventType = AffectedChangeEventLexicalDegraded
	event.ChangeStatus = "failed"
	event.ChangeCode = "LEXICAL_BUILD_FAILED"
	return event
}

type affectedChangeDispatchFake struct {
	results []AffectedChangeDispatchResult
	errors  []error
	calls   int
}

func (fake *affectedChangeDispatchFake) DispatchNext(context.Context) (AffectedChangeDispatchResult, bool, error) {
	index := fake.calls
	fake.calls++
	if index >= len(fake.results) {
		return AffectedChangeDispatchResult{}, false, nil
	}
	var err error
	if index < len(fake.errors) {
		err = fake.errors[index]
	}
	return fake.results[index], true, err
}
