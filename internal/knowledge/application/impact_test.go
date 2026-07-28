package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestImpactAnalyzeDeduplicatesSortsReplaysAndReturnsNoDraftsWithoutProposalRequiredObjects(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	conflict := impactApplicationObject(domain.ImpactObjectConflict, testID(42), domain.ImpactActionResolveConflict, false)
	health := impactApplicationObject(domain.ImpactObjectHealthIssue, testID(41), domain.ImpactActionRefreshHealth, false)
	repository := &impactRepositoryStub{event: event, objects: []domain.ImpactObject{health, conflict, conflict}}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || repository.saveCalls != 1 || len(result.Report.Objects) != 2 || result.Report.AnalysisVersion != domain.ImpactAnalysisVersionV2 {
		t.Fatalf("unexpected analysis result: %#v saves=%d", result, repository.saveCalls)
	}
	if result.Report.Objects[0].Type != domain.ImpactObjectConflict || result.Report.Objects[1].Type != domain.ImpactObjectHealthIssue {
		t.Fatalf("objects are not stably sorted: %#v", result.Report.Objects)
	}
	if len(result.ProposalDrafts) != 0 {
		t.Fatalf("read-only analysis returned proposal drafts: %#v", result.ProposalDrafts)
	}
	firstAuditID, err := deriveImpactAuditID(result.Report.ID, "impact-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.records) != 1 || audit.records[0].AuditID != firstAuditID || audit.records[0].IdempotencyKey != "impact:impact-1" || !audit.records[0].OccurredAt.Equal(result.Report.GeneratedAt) {
		t.Fatalf("audit record=%#v", audit.records)
	}

	replayed, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || repository.saveCalls != 1 || len(audit.records) != 2 || audit.records[1].AuditID != firstAuditID || !audit.records[1].Replayed || !audit.records[1].OccurredAt.Equal(audit.records[0].OccurredAt) {
		t.Fatalf("unexpected replay: %#v saves=%d audit=%#v", replayed, repository.saveCalls, audit.records)
	}
}

func TestImpactAnalyzeUsesDistinctAuditIDsForDifferentHTTPIdempotencyKeys(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	repository := &impactRepositoryStub{event: event}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-a"})
	if err != nil || created.Replayed {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-b"})
	if err != nil || !replayed.Replayed || replayed.Report.ID != created.Report.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if repository.saveCalls != 1 || len(audit.records) != 2 || audit.records[0].ReportID != audit.records[1].ReportID || audit.records[0].AuditID == audit.records[1].AuditID || audit.records[0].IdempotencyKey != "impact:impact-a" || audit.records[1].IdempotencyKey != "impact:impact-b" {
		t.Fatalf("saves=%d audit=%#v", repository.saveCalls, audit.records)
	}
}

func TestImpactAnalyzeRollsBackNewReportWhenTransactionalAuditFails(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	repository := &impactRepositoryStub{event: event}
	auditFailure := errors.New("audit write failed")
	audit := &impactAuditStub{transactionError: auditFailure}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-atomic"})
	if !errors.Is(err, auditFailure) || repository.report != nil || repository.saveCalls != 1 || audit.transactionCalls != 1 || len(audit.records) != 0 {
		t.Fatalf("error=%v report=%#v saves=%d tx_calls=%d audit=%#v", err, repository.report, repository.saveCalls, audit.transactionCalls, audit.records)
	}
}

func TestImpactAnalyzeRejectsSensitiveIdempotencyKeyBeforeWrites(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	repository := &impactRepositoryStub{event: event}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "token=plain-token"})
	if errorCode(err) != domain.ErrorCodeImpactInvalid || repository.saveCalls != 0 || len(audit.records) != 0 {
		t.Fatalf("error=%v saves=%d audit=%#v", err, repository.saveCalls, audit.records)
	}
}

func TestImpactAnalyzeRejectsConflictingDuplicateObjects(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	first := impactApplicationObject(domain.ImpactObjectConflict, testID(42), domain.ImpactActionResolveConflict, true)
	second := first
	second.Version++
	repository := &impactRepositoryStub{event: event, objects: []domain.ImpactObject{first, second}}
	service, err := NewImpactService(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-duplicate"})
	if errorCode(err) != domain.ErrorCodeImpactConflict || repository.saveCalls != 0 {
		t.Fatalf("error=%v saves=%d", err, repository.saveCalls)
	}
}

func TestImpactAnalyzeRejectsInternallyValidReportBoundToDifferentEventVersion(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	objects := []domain.ImpactObject{}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, 2, objects)
	if err != nil {
		t.Fatal(err)
	}
	report := &domain.ImpactReport{
		ID: testID(50), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: 2, AnalysisVersion: domain.ImpactAnalysisVersionV2, Status: domain.ImpactReportReady, Objects: objects, Summary: domain.SummarizeImpactObjects(objects),
		Fingerprint: fingerprint, GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	repository := &impactRepositoryStub{event: event, report: report}
	service, err := NewImpactService(repository, &sequenceIDGenerator{}, foundation.FixedClock{Value: testTime()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-version-drift"})
	if errorCode(err) != domain.ErrorCodeImpactConflict || repository.saveCalls != 0 {
		t.Fatalf("error=%v saves=%d", err, repository.saveCalls)
	}
}

func TestImpactAnalyzeRejectsCurrentV2FingerprintDriftBeforeAudit(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := byte('0')
	if fingerprint[0] == replacement {
		replacement = '1'
	}
	report := &domain.ImpactReport{
		ID: testID(50), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil),
		Fingerprint: string(replacement) + fingerprint[1:], GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	repository := &impactRepositoryStub{event: event, report: report}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-fingerprint-drift"})
	if errorCode(err) != domain.ErrorCodeImpactConflict || repository.eventCalls != 0 || repository.readinessCalls != 0 || repository.listCalls != 0 || repository.saveCalls != 0 || len(audit.records) != 0 {
		t.Fatalf("error=%v events=%d readiness=%d lists=%d saves=%d audit=%#v", err, repository.eventCalls, repository.readinessCalls, repository.listCalls, repository.saveCalls, audit.records)
	}
}

func TestImpactAnalyzeRejectsStaleCurrentReport(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	object := impactApplicationObject(domain.ImpactObjectRelation, testID(42), domain.ImpactActionReview, true)
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), []domain.ImpactObject{object})
	if err != nil {
		t.Fatal(err)
	}
	report := &domain.ImpactReport{
		ID: testID(50), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2, Status: domain.ImpactReportStale, Objects: []domain.ImpactObject{object},
		Summary: domain.SummarizeImpactObjects([]domain.ImpactObject{object}), Fingerprint: fingerprint,
		StaleReason: "source requires reanalysis", GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	repository := &impactRepositoryStub{event: event, report: report}
	service, err := NewImpactService(repository, &sequenceIDGenerator{}, foundation.FixedClock{Value: testTime()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-stale"})
	if errorCode(err) != domain.ErrorCodeImpactConflict || repository.listCalls != 0 || repository.saveCalls != 0 {
		t.Fatalf("error=%v lists=%d saves=%d", err, repository.listCalls, repository.saveCalls)
	}
}

func TestImpactAnalyzeRejectsCurrentV2OwnerBindingDriftBeforeAudit(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	frozen := impactApplicationArtifactObject(testID(42), 2, testID(43), 1, strings.Repeat("a", 64))
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(
		domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), []domain.ImpactObject{frozen},
	)
	if err != nil {
		t.Fatal(err)
	}
	report := &domain.ImpactReport{
		ID: testID(50), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{frozen}, Summary: domain.SummarizeImpactObjects([]domain.ImpactObject{frozen}),
		Fingerprint: fingerprint, GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	current := impactApplicationArtifactObject(testID(42), 3, testID(44), 2, strings.Repeat("b", 64))
	repository := &impactRepositoryStub{event: event, report: report, objects: []domain.ImpactObject{current}}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-owner-drift"})
	if errorCode(err) != domain.ErrorCodeImpactConflict || repository.eventCalls != 1 || repository.listCalls != 1 || repository.saveCalls != 0 || len(audit.records) != 0 {
		t.Fatalf("error=%v events=%d lists=%d saves=%d audit=%#v", err, repository.eventCalls, repository.listCalls, repository.saveCalls, audit.records)
	}
}

func TestImpactAnalyzeReturnsNonExecutableProposalDraft(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	object := impactApplicationObject(domain.ImpactObjectRelation, testID(42), domain.ImpactActionReview, true)
	repository := &impactRepositoryStub{event: event, objects: []domain.ImpactObject{object}}
	service, err := NewImpactService(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-draft"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ProposalDrafts) != 1 || repository.saveCalls != 1 {
		t.Fatalf("result=%#v saves=%d", result, repository.saveCalls)
	}
	draft := result.ProposalDrafts[0]
	expected := domain.ProposalDraft{
		WorkspaceID: workspaceID, SourceEventID: event.ID, Operation: string(object.Action), TargetType: object.Type,
		TargetID: object.ID, BaseVersion: object.Version, Reason: object.Reason, RequiresApproval: true, RequiresWriteAuthorization: true,
	}
	if draft != expected || draft.ID != "" {
		t.Fatalf("draft=%#v expected=%#v", draft, expected)
	}
	if err := domain.ValidateProposalDraft(draft); err != nil {
		t.Fatalf("validate non-executable draft: %v", err)
	}
}

func TestImpactAnalyzeCreatesV2ThatSupersedesHistoricalV1(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	legacyObject := impactApplicationObject(domain.ImpactObjectConflict, testID(42), domain.ImpactActionResolveConflict, false)
	legacyFingerprint, err := domain.ComputeImpactFingerprint(event.ID, int64(event.EventVersion), []domain.ImpactObject{legacyObject})
	if err != nil {
		t.Fatal(err)
	}
	legacy := domain.ImpactReport{
		ID: testID(49), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{legacyObject},
		Summary: domain.SummarizeImpactObjects([]domain.ImpactObject{legacyObject}), Fingerprint: legacyFingerprint,
		GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	repository := &impactRepositoryStub{event: event, legacyReport: &legacy, objects: []domain.ImpactObject{legacyObject}}
	service, err := NewImpactService(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), []domain.ImpactObject{legacyObject})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Report.AnalysisVersion != domain.ImpactAnalysisVersionV2 || result.Report.SupersedesReportID == nil || *result.Report.SupersedesReportID != legacy.ID || result.Report.Fingerprint != wantFingerprint {
		t.Fatalf("v2 result=%#v", result)
	}
	if repository.saveCalls != 1 || repository.readinessCalls != 1 || repository.legacyReport.ID != legacy.ID || repository.legacyReport.Fingerprint != legacyFingerprint {
		t.Fatalf("saves=%d readiness=%d legacy=%#v", repository.saveCalls, repository.readinessCalls, repository.legacyReport)
	}
}

func TestImpactAnalyzeUnavailableBeforeSelectorCompletionHasZeroSideEffects(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	repository := &impactRepositoryStub{event: event, notReady: true}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{ids: []foundation.ID{testID(50)}}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-not-ready"})
	if errorCode(err) != domain.ErrorCodeImpactUnavailable || repository.readinessCalls != 1 || repository.eventCalls != 0 || repository.listCalls != 0 || repository.saveCalls != 0 || len(audit.records) != 0 {
		t.Fatalf("error=%v readiness=%d events=%d lists=%d saves=%d audit=%#v", err, repository.readinessCalls, repository.eventCalls, repository.listCalls, repository.saveCalls, audit.records)
	}
}

func TestImpactAnalyzeReplaysCurrentV2BeforeReadinessAndHistoricalGetRemainsAvailable(t *testing.T) {
	workspaceID := testID(1)
	event := timelineApplicationEvent(testID(30), workspaceID, testTime())
	object := impactApplicationObject(domain.ImpactObjectConflict, testID(42), domain.ImpactActionResolveConflict, false)
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), []domain.ImpactObject{object})
	if err != nil {
		t.Fatal(err)
	}
	current := domain.ImpactReport{
		ID: testID(50), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{object}, Summary: domain.SummarizeImpactObjects([]domain.ImpactObject{object}),
		Fingerprint: fingerprint, GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	legacyFingerprint, err := domain.ComputeImpactFingerprint(event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := domain.ImpactReport{
		ID: testID(49), WorkspaceID: workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil),
		Fingerprint: legacyFingerprint, GeneratedAt: testTime(), CreatedAt: testTime(), Version: 1,
	}
	repository := &impactRepositoryStub{event: event, objects: []domain.ImpactObject{object}, report: &current, legacyReport: &legacy, notReady: true}
	audit := &impactAuditStub{}
	service, err := NewImpactServiceWithAudit(repository, &sequenceIDGenerator{}, foundation.FixedClock{Value: testTime()}, audit)
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := service.Analyze(context.Background(), ImpactAnalysisRequest{WorkspaceID: workspaceID, SourceEventID: event.ID, IdempotencyKey: "impact-v2-replay"})
	if err != nil || !replayed.Replayed || replayed.Report.ID != current.ID || repository.readinessCalls != 0 || repository.listCalls != 1 || repository.saveCalls != 0 || len(audit.records) != 1 {
		t.Fatalf("replayed=%#v err=%v readiness=%d lists=%d saves=%d audit=%#v", replayed, err, repository.readinessCalls, repository.listCalls, repository.saveCalls, audit.records)
	}
	loadedLegacy, err := service.GetReport(context.Background(), workspaceID, legacy.ID)
	if err != nil || loadedLegacy.ID != legacy.ID || loadedLegacy.EffectiveAnalysisVersion() != domain.ImpactAnalysisVersionV1 || repository.readinessCalls != 0 {
		t.Fatalf("legacy=%#v err=%v readiness=%d", loadedLegacy, err, repository.readinessCalls)
	}
}

type impactRepositoryStub struct {
	event          domain.KnowledgeEvent
	objects        []domain.ImpactObject
	report         *domain.ImpactReport
	legacyReport   *domain.ImpactReport
	notReady       bool
	readinessCalls int
	eventCalls     int
	listCalls      int
	saveCalls      int
}

func (repository *impactRepositoryStub) GetEvent(_ context.Context, workspaceID, eventID foundation.ID) (domain.KnowledgeEvent, error) {
	repository.eventCalls++
	if repository.event.WorkspaceID != workspaceID || repository.event.ID != eventID {
		return domain.KnowledgeEvent{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeTimelineNotFound, false, errors.New("not found"))
	}
	return repository.event, nil
}

func (repository *impactRepositoryStub) GetImpactReport(_ context.Context, workspaceID, sourceEventID foundation.ID, analysisVersion domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error) {
	for _, report := range []*domain.ImpactReport{repository.report, repository.legacyReport} {
		if report != nil && report.WorkspaceID == workspaceID && report.SourceEventID == sourceEventID && report.EffectiveAnalysisVersion() == analysisVersion {
			return *report, true, nil
		}
	}
	return domain.ImpactReport{}, false, nil
}

func (repository *impactRepositoryStub) ImpactAnalysisReady(_ context.Context, _ foundation.ID) (bool, error) {
	repository.readinessCalls++
	return !repository.notReady, nil
}

func (repository *impactRepositoryStub) ListImpactObjects(_ context.Context, _ domain.KnowledgeEvent) ([]domain.ImpactObject, error) {
	repository.listCalls++
	return append([]domain.ImpactObject(nil), repository.objects...), nil
}

func (repository *impactRepositoryStub) SaveImpactReport(_ context.Context, report domain.ImpactReport) (domain.ImpactReport, bool, error) {
	repository.saveCalls++
	repository.report = &report
	return report, false, nil
}

func (repository *impactRepositoryStub) SaveImpactReportWithAudit(ctx context.Context, report domain.ImpactReport, idempotencyKey string, audit ImpactAuditPort) (domain.ImpactReport, bool, error) {
	previous := repository.report
	persisted, replayed, err := repository.SaveImpactReport(ctx, report)
	if err != nil {
		return domain.ImpactReport{}, false, err
	}
	record, err := BuildImpactAuditRecord(persisted, idempotencyKey, replayed)
	if err == nil {
		err = audit.RecordImpactAnalysisTx(ctx, struct{}{}, record)
	}
	if err != nil {
		repository.report = previous
		return domain.ImpactReport{}, false, err
	}
	return persisted, replayed, nil
}

func (repository *impactRepositoryStub) GetImpactReportByID(_ context.Context, workspaceID, reportID foundation.ID) (domain.ImpactReport, error) {
	for _, report := range []*domain.ImpactReport{repository.report, repository.legacyReport} {
		if report != nil && report.WorkspaceID == workspaceID && report.ID == reportID {
			return *report, nil
		}
	}
	return domain.ImpactReport{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeImpactNotFound, false, errors.New("not found"))
}

type impactAuditStub struct {
	records          []ImpactAuditRecord
	transactionError error
	transactionCalls int
}

func (audit *impactAuditStub) RecordImpactAnalysis(_ context.Context, record ImpactAuditRecord) error {
	audit.records = append(audit.records, record)
	return nil
}

func (audit *impactAuditStub) RecordImpactAnalysisTx(_ context.Context, _ any, record ImpactAuditRecord) error {
	audit.transactionCalls++
	if audit.transactionError != nil {
		return audit.transactionError
	}
	audit.records = append(audit.records, record)
	return nil
}

func impactApplicationObject(objectType domain.ImpactObjectType, id foundation.ID, action domain.ImpactAction, requiresProposal bool) domain.ImpactObject {
	return domain.ImpactObject{Type: objectType, ID: id, WorkspaceID: testID(1), Version: 2, Action: action, Reason: "downstream fact requires review", RequiresProposal: requiresProposal}
}

func impactApplicationArtifactObject(id foundation.ID, version int64, revisionID foundation.ID, revisionNo int64, contentHash string) domain.ImpactObject {
	binding := &domain.ArtifactImpactBinding{
		ArtifactID: id, ArtifactVersion: version, RevisionID: revisionID, RevisionNo: revisionNo, ContentHash: contentHash,
	}
	return domain.ImpactObject{
		Type: domain.ImpactObjectArtifact, ID: id, WorkspaceID: testID(1), Version: version,
		Action: domain.ImpactActionRegenerateArtifact, Reason: "artifact depends on changed evidence", RequiresProposal: true,
		ArtifactBinding: binding,
	}
}
