package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
)

func TestImpactRecorderUsesStableRequestBindingAcrossReplay(t *testing.T) {
	repository := &impactAuditRepository{events: make(map[string]auditdomain.Event)}
	auditRecorder, err := auditapplication.NewRecorder(repository)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewImpactRecorder(auditRecorder, func(context.Context) (auditdomain.ActorType, string) {
		return auditdomain.ActorUser, "user-1"
	})
	if err != nil {
		t.Fatal(err)
	}
	record := knowledgeapplication.ImpactAuditRecord{
		WorkspaceID:    impactAuditID(1),
		ReportID:       impactAuditID(2),
		AuditID:        impactAuditID(4),
		SourceEventID:  impactAuditID(3),
		ObjectCount:    2,
		IdempotencyKey: "impact:request-1",
		OccurredAt:     time.Date(2026, 7, 23, 8, 9, 10, 123456789, time.UTC),
	}
	if err := recorder.RecordImpactAnalysis(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.Replayed = true
	if err := recorder.RecordImpactAnalysisTx(context.Background(), struct{}{}, record); err != nil {
		t.Fatal(err)
	}
	if len(repository.events) != 1 {
		t.Fatalf("audit events=%#v", repository.events)
	}
	event := repository.events[record.IdempotencyKey]
	if event.ID != record.AuditID || event.WorkspaceID == nil || *event.WorkspaceID != record.WorkspaceID || event.ActorType != auditdomain.ActorUser || event.ActorRef != "user-1" || event.Action != impactAnalyzedAction || event.ResourceType != impactReportResourceType || event.ResourceRef != string(record.ReportID) || event.Outcome != auditdomain.OutcomeSucceeded || !event.OccurredAt.Equal(record.OccurredAt.Truncate(time.Microsecond)) {
		t.Fatalf("audit event=%#v", event)
	}
	var correlation struct {
		ReportID      string `json:"report_id"`
		TimelineEvent string `json:"timeline_event_id"`
	}
	if err := json.Unmarshal(event.Correlation, &correlation); err != nil || correlation.ReportID != string(record.ReportID) || correlation.TimelineEvent != string(record.SourceEventID) {
		t.Fatalf("correlation=%s err=%v", event.Correlation, err)
	}
	var metadata struct {
		ObjectCount int `json:"object_count"`
	}
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil || metadata.ObjectCount != record.ObjectCount {
		t.Fatalf("metadata=%s err=%v", event.Metadata, err)
	}
}

func TestImpactRecorderUsesDistinctEventsForDifferentRequestKeys(t *testing.T) {
	repository := &impactAuditRepository{events: make(map[string]auditdomain.Event)}
	auditRecorder, err := auditapplication.NewRecorder(repository)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewImpactRecorder(auditRecorder, func(context.Context) (auditdomain.ActorType, string) {
		return auditdomain.ActorAPIToken, "token-1"
	})
	if err != nil {
		t.Fatal(err)
	}
	first := knowledgeapplication.ImpactAuditRecord{
		WorkspaceID: impactAuditID(1), ReportID: impactAuditID(2), AuditID: impactAuditID(4), SourceEventID: impactAuditID(3),
		IdempotencyKey: "impact:request-1", OccurredAt: time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC),
	}
	second := first
	second.AuditID = impactAuditID(5)
	second.IdempotencyKey = "impact:request-2"
	second.Replayed = true
	if err := recorder.RecordImpactAnalysis(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordImpactAnalysis(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	firstEvent, firstFound := repository.events[first.IdempotencyKey]
	secondEvent, secondFound := repository.events[second.IdempotencyKey]
	if len(repository.events) != 2 || !firstFound || !secondFound || firstEvent.ID == secondEvent.ID || secondEvent.ActorType != auditdomain.ActorAPIToken {
		t.Fatalf("audit events=%#v", repository.events)
	}
}

func TestNewImpactRecorderRejectsMissingDependencies(t *testing.T) {
	if _, err := NewImpactRecorder(nil, func(context.Context) (auditdomain.ActorType, string) { return auditdomain.ActorSystem, "" }); err == nil {
		t.Fatal("expected nil recorder error")
	}
	repository := &impactAuditRepository{events: make(map[string]auditdomain.Event)}
	auditRecorder, err := auditapplication.NewRecorder(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewImpactRecorder(auditRecorder, nil); err == nil {
		t.Fatal("expected nil actor resolver error")
	}
}

type impactAuditRepository struct{ events map[string]auditdomain.Event }

func (repository *impactAuditRepository) Append(_ context.Context, event auditdomain.Event) (auditdomain.Event, bool, error) {
	if existing, found := repository.events[event.IdempotencyKey]; found {
		if !auditdomain.EqualBinding(existing, event) {
			return auditdomain.Event{}, false, errors.New("audit binding drift")
		}
		return existing, true, nil
	}
	repository.events[event.IdempotencyKey] = event
	return event, false, nil
}

func (repository *impactAuditRepository) AppendTx(ctx context.Context, _ any, event auditdomain.Event) (auditdomain.Event, bool, error) {
	return repository.Append(ctx, event)
}

func impactAuditID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-%012d", seed))
}
