package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

var profileTestNow = time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)

func TestGeneratorReplaysExactReadyProfileWithoutProviderOrAttempt(t *testing.T) {
	request := profileGenerationRequest()
	repository := &profileRepositoryFake{
		ready: captureapp.ReadyProfile{ProfileID: profileTestID(20), RevisionID: profileTestID(21)}, readyFound: true,
	}
	generator, err := NewUnavailableGenerator(repository, &profileIDs{next: 90}, &profileClock{next: profileTestNow})
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProfileID != repository.ready.ProfileID || result.RevisionID != repository.ready.RevisionID {
		t.Fatalf("result = %#v", result)
	}
	if repository.lookupCalls != 1 || repository.prepareCalls != 0 || repository.loadCalls != 0 || repository.failCalls != 0 {
		t.Fatalf("lookup=%d prepare=%d load=%d fail=%d", repository.lookupCalls, repository.prepareCalls, repository.loadCalls, repository.failCalls)
	}
}

func TestGeneratorPersistsCapabilityUnavailableWithWorkspaceBinding(t *testing.T) {
	request := profileGenerationRequest()
	repository := &profileRepositoryFake{}
	generator, err := NewUnavailableGenerator(repository, &profileIDs{next: 90}, &profileClock{next: profileTestNow})
	if err != nil {
		t.Fatal(err)
	}
	_, err = generator.Generate(context.Background(), request)
	if profileTestErrorCode(err) != ErrorCodeCapabilityUnavailable {
		t.Fatalf("Generate() error = %#v", err)
	}
	if repository.failCalls != 1 || repository.loadCalls != 0 {
		t.Fatalf("fail=%d load=%d", repository.failCalls, repository.loadCalls)
	}
	failed := repository.failed
	if failed.WorkspaceID != request.Capture.WorkspaceID || failed.ProfileID != request.ProfileID ||
		failed.ProfileStatus != domain.ProfileStatusCapabilityUnavailable || failed.ModelRunID != "" ||
		failed.ErrorCode != ErrorCodeCapabilityUnavailable || failed.Retryable {
		t.Fatalf("failed = %#v", failed)
	}
}

func TestGeneratorFailsPreparedProfileWhenFrozenSourceCannotLoad(t *testing.T) {
	request := profileGenerationRequest()
	sourceErr := foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_PROFILE_SOURCE_MISSING", false, errors.New("missing source"))
	repository := &profileRepositoryFake{loadErr: sourceErr}
	modelRuns := &profileModelRunRepository{}
	generator := newProfileGenerator(t, repository, modelRuns, &profileChatModel{})

	_, err := generator.Generate(context.Background(), request)
	if profileTestErrorCode(err) != "CAPTURE_PROFILE_SOURCE_MISSING" {
		t.Fatalf("Generate() error = %#v", err)
	}
	if repository.loadCalls != 1 || repository.failCalls != 1 || modelRuns.createCalls != 0 {
		t.Fatalf("load=%d fail=%d model-runs=%d", repository.loadCalls, repository.failCalls, modelRuns.createCalls)
	}
	if repository.failed.WorkspaceID != request.Capture.WorkspaceID || repository.failed.ProfileStatus != domain.ProfileStatusFailed {
		t.Fatalf("failed = %#v", repository.failed)
	}
}

func TestGeneratorFailsUnboundProfileWhenModelRunCreationFails(t *testing.T) {
	request := profileGenerationRequest()
	runErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "AGENT_DATABASE_UNAVAILABLE", true,
		errors.New("model run write failed"))
	repository := &profileRepositoryFake{source: profileSourceSnapshot(request)}
	modelRuns := &profileModelRunRepository{createErr: runErr}
	generator := newProfileGenerator(t, repository, modelRuns, &profileChatModel{})

	_, err := generator.Generate(context.Background(), request)
	if profileTestErrorCode(err) != "AGENT_DATABASE_UNAVAILABLE" {
		t.Fatalf("Generate() error = %#v", err)
	}
	if repository.failCalls != 1 || repository.failed.ModelRunID != "" ||
		repository.failed.ProfileStatus != domain.ProfileStatusFailed || !repository.failed.Retryable {
		t.Fatalf("failed = %#v calls=%d", repository.failed, repository.failCalls)
	}
}

func TestGeneratorRecordsModelCallAndCompletesEvidenceBoundRevision(t *testing.T) {
	request := profileGenerationRequest()
	repository := &profileRepositoryFake{source: profileSourceSnapshot(request)}
	modelRuns := &profileModelRunRepository{}
	chat := &profileChatModel{content: validProfileOutput("E0001")}
	generator := newProfileGenerator(t, repository, modelRuns, chat)

	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProfileID != request.ProfileID || result.RevisionID == "" || chat.calls != 1 ||
		modelRuns.createCalls != 1 || len(modelRuns.calls) != 1 || repository.completeCalls != 1 {
		t.Fatalf("result=%#v chat=%d runs=%d calls=%d completes=%d", result, chat.calls, modelRuns.createCalls, len(modelRuns.calls), repository.completeCalls)
	}
	call := modelRuns.calls[0]
	if call.Status != agentdomain.ModelCallSucceeded || call.CallNo != 1 || call.ModelRunID != modelRuns.run.ID {
		t.Fatalf("call = %#v", call)
	}
	completed := repository.completed
	if completed.Revision.ID != result.RevisionID || completed.Revision.ModelRunID != modelRuns.run.ID ||
		completed.Revision.IndexVersionID != request.IndexVersionID || completed.Revision.ParseProjectionID != request.ParseProjectionID ||
		completed.ExpectedModelRunVersion != modelRuns.run.Version || len(completed.Evidence) != 1 ||
		completed.Evidence[0].SourceSpanID != repository.source.Chunks[0].SourceSpanID {
		t.Fatalf("completed = %#v", completed)
	}
	if completed.Revision.Content.Topics[0].SourceSpanIDs[0] != repository.source.Chunks[0].SourceSpanID ||
		completed.Revision.Content.KnowledgePoints[0].SourceSpanIDs[0] != repository.source.Chunks[0].SourceSpanID {
		t.Fatalf("content = %#v", completed.Revision.Content)
	}
	messages := make([]string, len(chat.request.Messages))
	for index, message := range chat.request.Messages {
		messages[index] = message.Content
	}
	modelInput := strings.Join(messages, "\n")
	if !strings.Contains(modelInput, "E0001") || strings.Contains(modelInput, string(repository.source.Chunks[0].ChunkID)) ||
		strings.Contains(modelInput, string(repository.source.Chunks[0].SourceSpanID)) ||
		strings.Contains(modelInput, string(request.Capture.WorkspaceID)) {
		t.Fatalf("model input leaked internal identity: %s", modelInput)
	}
}

func TestGeneratorRetainsConfiguredEinoStructuredScheduler(t *testing.T) {
	scheduler := newProfileTrackingEinoScheduler(t)
	generator := newProfileGeneratorWithScheduler(
		t,
		&profileRepositoryFake{},
		&profileModelRunRepository{},
		&profileChatModel{},
		scheduler,
	)
	if generator.scheduler != scheduler {
		t.Fatalf("scheduler=%T want=%T", generator.scheduler, scheduler)
	}
}

func TestGeneratorExecutesEinoStructuredSchedulerRepairAndCompletesProfile(t *testing.T) {
	request := profileGenerationRequest()
	repository := &profileRepositoryFake{source: profileSourceSnapshot(request)}
	modelRuns := &profileModelRunRepository{}
	chat := &profileChatModel{responses: [][]byte{
		[]byte(`{}`),
		validProfileOutput("E0001"),
	}}
	scheduler := newProfileTrackingEinoScheduler(t)
	generator := newProfileGeneratorWithScheduler(t, repository, modelRuns, chat, scheduler)

	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.calls.Load() != 1 || result.ProfileID != request.ProfileID || result.RevisionID == "" || chat.calls != 2 ||
		modelRuns.createCalls != 1 || len(modelRuns.calls) != 2 || repository.completeCalls != 1 || repository.failCalls != 0 {
		t.Fatalf("scheduler_calls=%d result=%#v chat=%d model-runs=%d calls=%d completes=%d failures=%d", scheduler.calls.Load(), result, chat.calls,
			modelRuns.createCalls, len(modelRuns.calls), repository.completeCalls, repository.failCalls)
	}
	if modelRuns.calls[0].Phase != agentdomain.ModelCallInitial || modelRuns.calls[1].Phase != agentdomain.ModelCallRepair ||
		modelRuns.calls[0].CallNo != 1 || modelRuns.calls[1].CallNo != 2 {
		t.Fatalf("model calls = %#v", modelRuns.calls)
	}
	completed := repository.completed
	if completed.Revision.ID != result.RevisionID || completed.Revision.ProfileID != request.ProfileID ||
		completed.Revision.ModelRunID != modelRuns.run.ID || completed.Revision.Content.Topics[0].SourceSpanIDs[0] != repository.source.Chunks[0].SourceSpanID ||
		completed.ExpectedModelRunVersion != modelRuns.run.Version {
		t.Fatalf("completed = %#v", completed)
	}
}

type profileTrackingScheduler struct {
	delegate agentapp.StructuredPhaseScheduler
	calls    atomic.Int64
}

func newProfileTrackingEinoScheduler(t *testing.T) *profileTrackingScheduler {
	t.Helper()
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &profileTrackingScheduler{delegate: scheduler}
}

func (scheduler *profileTrackingScheduler) Schedule(ctx context.Context, run *agentapp.StructuredPhaseRun) error {
	scheduler.calls.Add(1)
	return scheduler.delegate.Schedule(ctx, run)
}

func TestProfileFromOutputRejectsUnknownEvidenceLabel(t *testing.T) {
	output, err := DecodeOutput(validProfileOutput("E0002"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = profileFromOutput(output, map[string]labeledEvidence{
		"E0001": {ChunkID: profileTestID(50), SourceSpanID: profileTestID(51)},
	}, profileTestID(1), profileTestID(4), profileTestNow)
	if profileTestErrorCode(err) != ErrorCodeOutputInvalid {
		t.Fatalf("profileFromOutput() error = %#v", err)
	}
}

func TestDecodeOutputRejectsUnorderedAndUnknownEvidenceShape(t *testing.T) {
	for name, labels := range map[string][]string{
		"unordered": {"E0002", "E0001"},
		"identity":  {"30000000-0000-4000-8000-000000000001"},
	} {
		t.Run(name, func(t *testing.T) {
			document := map[string]any{
				"result_type": agentdomain.ResultTypeDocumentKnowledgeProfile, "schema_id": SchemaID,
				"schema_version": domain.ProfileSchemaVersion, "summary": "Java AI summary",
				"topics":           []any{map[string]any{"label": "Java AI", "aliases": []string{}, "evidence_labels": labels}},
				"terms":            []any{},
				"knowledge_points": []any{map[string]any{"text": "Spring AI integrates model providers.", "evidence_labels": []string{"E0001"}}},
				"examples":         []any{},
			}
			raw, marshalErr := json.Marshal(document)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err := DecodeOutput(raw); profileTestErrorCode(err) != ErrorCodeOutputInvalid {
				t.Fatalf("DecodeOutput() error = %#v", err)
			}
		})
	}
}

func newProfileGenerator(t *testing.T, repository *profileRepositoryFake, modelRuns *profileModelRunRepository, model *profileChatModel) *Generator {
	return newProfileGeneratorWithScheduler(t, repository, modelRuns, model, newProfileTrackingEinoScheduler(t))
}

func newProfileGeneratorWithScheduler(
	t *testing.T,
	repository *profileRepositoryFake,
	modelRuns *profileModelRunRepository,
	model *profileChatModel,
	scheduler agentapp.StructuredPhaseScheduler,
) *Generator {
	t.Helper()
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{
		Ref: profileModelProfileRef(), Model: profileModelRef(), Timeout: time.Second, MaxOutputTokens: 1024,
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(GeneratorDependencies{
		Repository: repository, ModelRuns: modelRuns, Model: model, Scheduler: scheduler, Catalog: catalog,
		ModelProfileRef: profile.Ref, IDs: &profileIDs{next: 90}, Clock: &profileClock{next: profileTestNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generator
}

func profileGenerationRequest() captureapp.ProfileGenerationRequest {
	capture := domain.Capture{
		ID: profileTestID(2), WorkspaceID: profileTestID(1), Kind: domain.KindText, DisplayName: "Java AI",
		OriginalLocation: "captures/java-ai", OriginalInputHash: strings.Repeat("a", 64), SourceID: profileTestID(3),
		LatestSourceVersionID: profileTestID(4), Status: domain.StatusProcessing, FetchStatus: domain.StageNotApplicable,
		IngestionStatus: domain.StageReady, IndexStatus: domain.StageReady, ProfileStatus: domain.StageRunning,
		Version: 4, CapturedAt: profileTestNow.Add(-time.Hour), UpdatedAt: profileTestNow.Add(-time.Minute),
	}
	attempt := domain.ProcessingAttempt{
		ID: profileTestID(5), WorkspaceID: capture.WorkspaceID, CaptureID: capture.ID,
		SourceVersionID: capture.LatestSourceVersionID, WorkflowRunID: profileTestID(6), IndexVersionID: profileTestID(9),
		AttemptNumber: 1, Stage: domain.AttemptStageProfile, Status: domain.AttemptStatusRunning,
		StartedAt: profileTestNow.Add(-time.Minute), Version: 4,
	}
	revision := int64(3)
	return captureapp.ProfileGenerationRequest{
		ProfileID: profileTestID(10), Capture: capture, Attempt: attempt, ParseProjectionID: profileTestID(8),
		IndexVersionID: profileTestID(9), WorkflowRunID: attempt.WorkflowRunID,
		NodeRunID: profileTestID(7), NodeAttemptID: profileTestID(11), ModelSettingsRevision: &revision,
	}
}

func profileSourceSnapshot(request captureapp.ProfileGenerationRequest) captureapp.ProfileSourceSnapshot {
	return captureapp.ProfileSourceSnapshot{
		WorkspaceID: request.Capture.WorkspaceID, SourceVersionID: request.Capture.LatestSourceVersionID,
		ParseProjectionID: request.ParseProjectionID, IndexVersionID: request.IndexVersionID,
		Chunks: []captureapp.ProfileSourceChunk{{
			ChunkID: profileTestID(30), SourceSpanID: profileTestID(31), Sequence: 0,
			HeadingPath: []string{"Java AI"}, Content: "Spring AI integrates model providers.",
		}},
	}
}

func validProfileOutput(label string) []byte {
	document := map[string]any{
		"result_type": agentdomain.ResultTypeDocumentKnowledgeProfile, "schema_id": SchemaID,
		"schema_version": domain.ProfileSchemaVersion, "summary": "Java AI summary",
		"topics": []any{map[string]any{"label": "Java AI", "aliases": []string{"Spring AI"}, "evidence_labels": []string{label}}},
		"terms":  []any{},
		"knowledge_points": []any{map[string]any{
			"text": "Spring AI integrates model providers.", "evidence_labels": []string{label},
		}},
		"examples": []any{},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return encoded
}

func profileModelProfileRef() agentdomain.ModelProfileRef {
	return agentdomain.ModelProfileRef{ID: "capture.profile.test", Version: "v1"}
}

func profileModelRef() agentdomain.ModelRef {
	return agentdomain.ModelRef{
		AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "profile-test", ModelVersion: "profile-test-v1",
	}
}

type profileRepositoryFake struct {
	ready         captureapp.ReadyProfile
	readyFound    bool
	lookupErr     error
	loadErr       error
	source        captureapp.ProfileSourceSnapshot
	failed        captureapp.FailProfileCommand
	completed     captureapp.CompleteProfileCommand
	lookupCalls   int
	prepareCalls  int
	loadCalls     int
	failCalls     int
	completeCalls int
}

func (repository *profileRepositoryFake) LookupReady(context.Context, captureapp.ProfileLookup, captureapp.ProfileContract) (captureapp.ReadyProfile, bool, error) {
	repository.lookupCalls++
	return repository.ready, repository.readyFound, repository.lookupErr
}

func (repository *profileRepositoryFake) LoadSource(context.Context, captureapp.ProfileLookup, foundation.ID, foundation.ID) (captureapp.ProfileSourceSnapshot, error) {
	repository.loadCalls++
	return repository.source, repository.loadErr
}

func (repository *profileRepositoryFake) PrepareProfile(_ context.Context, command captureapp.PrepareProfileCommand) (captureapp.PreparedProfile, error) {
	repository.prepareCalls++
	return captureapp.PreparedProfile{
		ProfileID: command.RequestedProfileID, ProfileAttemptID: command.ProfileAttemptID,
		ProfileAttemptNumber: 1, ProfileAttemptVersion: 1, StartedAt: command.StartedAt,
	}, nil
}

func (*profileRepositoryFake) BindProfileModelRun(_ context.Context, command captureapp.BindProfileModelRunCommand) (captureapp.PreparedProfile, error) {
	return captureapp.PreparedProfile{
		ProfileID: command.ProfileID, ProfileAttemptID: command.ProfileAttemptID,
		ProfileAttemptNumber: 1, ProfileAttemptVersion: command.ExpectedProfileAttemptVersion + 1,
		StartedAt: profileTestNow, ModelRunID: command.ModelRunID,
	}, nil
}

func (repository *profileRepositoryFake) CompleteProfile(_ context.Context, command captureapp.CompleteProfileCommand) (captureapp.ReadyProfile, error) {
	repository.completeCalls++
	repository.completed = command
	return captureapp.ReadyProfile{ProfileID: command.Revision.ProfileID, RevisionID: command.Revision.ID}, nil
}

func (repository *profileRepositoryFake) FailProfile(_ context.Context, command captureapp.FailProfileCommand) error {
	repository.failCalls++
	repository.failed = command
	return nil
}

type profileModelRunRepository struct {
	run         agentdomain.ModelRun
	calls       []agentdomain.ModelCall
	createErr   error
	createCalls int
}

func (repository *profileModelRunRepository) CreateModelRun(_ context.Context, run agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	repository.createCalls++
	if repository.createErr != nil {
		return agentdomain.ModelRun{}, false, repository.createErr
	}
	repository.run = run
	return run, false, nil
}

func (repository *profileModelRunRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapp.ModelRunRecord, error) {
	return agentapp.ModelRunRecord{Run: repository.run, Calls: append([]agentdomain.ModelCall(nil), repository.calls...)}, nil
}

func (repository *profileModelRunRepository) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	if err := agentdomain.ValidateModelCall(call); err != nil {
		return agentdomain.ModelCall{}, false, err
	}
	repository.calls = append(repository.calls, call)
	return call, false, nil
}

func (repository *profileModelRunRepository) CompleteModelCall(_ context.Context, command agentapp.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	if err := agentdomain.ValidateModelCall(command.Call); err != nil {
		return agentdomain.ModelCall{}, false, err
	}
	repository.calls[command.Call.CallNo-1] = command.Call
	return command.Call, false, nil
}

func (repository *profileModelRunRepository) FinalizeModelRun(_ context.Context, command agentapp.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	repository.run = command.Run
	return command.Run, false, nil
}

func (*profileModelRunRepository) MarkStaleModelCallsUnknown(context.Context, agentapp.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, nil
}

func (*profileModelRunRepository) MarkStaleModelRunsUnknown(context.Context, agentapp.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, nil
}

type profileChatModel struct {
	content   []byte
	responses [][]byte
	calls     int
	request   agentapp.ChatRequest
}

func (model *profileChatModel) Chat(_ context.Context, request agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	model.calls++
	model.request = request
	content := model.content
	if len(model.responses) >= model.calls {
		content = model.responses[model.calls-1]
	}
	return agentapp.ChatResponse{
		Model: request.Model, Content: append([]byte(nil), content...),
		Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}, nil
}

type profileIDs struct{ next int }

func (ids *profileIDs) New() (foundation.ID, error) {
	value := profileTestID(ids.next)
	ids.next++
	return value, nil
}

type profileClock struct{ next time.Time }

func (clock *profileClock) Now() time.Time {
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}

func profileTestID(value int) foundation.ID {
	id, err := foundation.ParseID(fmt.Sprintf("93000000-0000-4000-8000-%012d", value))
	if err != nil {
		panic(err)
	}
	return id
}

func profileTestErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

var _ captureapp.ProfileGenerationRepository = (*profileRepositoryFake)(nil)
var _ agentapp.ModelRunRepository = (*profileModelRunRepository)(nil)
var _ agentapp.ChatModel = (*profileChatModel)(nil)
