package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestServiceStartFreezesOnlyEvidenceBoundQuestionMaterial(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})
	result, err := service.Start(context.Background(), StartCommand{WorkspaceID: applicationID(1), Config: applicationConfig(), IdempotencyKey: "interview-start-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Questions) != 1 || len(store.start.Questions) != 1 {
		t.Fatalf("questions=%d persisted=%d", len(result.Questions), len(store.start.Questions))
	}
	question := result.Questions[0]
	if question.ClaimID != applicationMaterial(t).ClaimID || question.Evidence[0].SupportType != "SUPPORTS" || question.Status != domain.QuestionStatusPending {
		t.Fatalf("unexpected frozen question: %+v", question)
	}
	if !result.Session.StartedAt.Equal(now) {
		t.Fatalf("started_at=%s", result.Session.StartedAt)
	}
}

func TestServiceSubmitCreatesDeterministicFollowUpWithoutFSRSPort(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})
	started, err := service.Start(context.Background(), StartCommand{WorkspaceID: applicationID(1), Config: applicationConfig(), IdempotencyKey: "interview-start-2"})
	if err != nil {
		t.Fatal(err)
	}
	store.snapshot = Snapshot{Session: started.Session, Questions: started.Questions}
	result, err := service.SubmitTurn(context.Background(), SubmitTurnCommand{WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, QuestionID: started.Questions[0].ID, UserAnswer: "", IdempotencyKey: "interview-turn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FollowUp == nil || !result.Turn.Decision.FollowUpCreated || store.submit.FollowUp == nil {
		t.Fatalf("low deterministic score must create a follow-up: %+v", result)
	}
	if store.submit.Turn.Score.SchemaVersion != domain.ScoreSchemaVersion || store.submit.Turn.Score.Evidence[0].EvidenceHash != started.Questions[0].Evidence[0].EvidenceHash {
		t.Fatalf("turn score is not evidence-bound: %+v", store.submit.Turn.Score)
	}
	if store.submit.Turn.QuestionID != started.Questions[0].ID || store.submit.ExpectedVersion != 1 {
		t.Fatalf("unexpected submit record: %+v", store.submit)
	}
	if len(store.candidateWriter.requests) != 0 {
		t.Fatalf("submitting an answer must not create Memory candidates: %+v", store.candidateWriter.requests)
	}
	_ = now
}

func TestServiceSubmitLoadsTaskScopedPersonalContextForScorerWithoutPersistingIt(t *testing.T) {
	store := &fakeStore{}
	memoryIdentityCanary := "memory-identity-canary-4f2391"
	loader := &fakeContextLoader{result: PersonalContext{
		Preferences: []json.RawMessage{json.RawMessage(`{"answer_style":"concise"}`)},
		Context: []json.RawMessage{
			json.RawMessage(`{"goal":"practice-boundaries"}`),
			json.RawMessage(`{"memory_ref":"` + memoryIdentityCanary + `"}`),
		},
	}}
	scorer := &recordingScorer{}
	service := newTestServiceWithContext(t, store, []domain.Material{applicationMaterial(t)}, loader, scorer)
	started, err := service.Start(context.Background(), StartCommand{
		WorkspaceID: applicationID(1), Config: applicationConfig(), IdempotencyKey: "interview-context-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.snapshot = Snapshot{Session: started.Session, Questions: started.Questions}

	result, err := service.SubmitTurn(context.Background(), SubmitTurnCommand{
		WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, QuestionID: started.Questions[0].ID,
		UserAnswer: "A service must preserve evidence-backed idempotency.", IdempotencyKey: "interview-context-turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if loader.calls != 1 || loader.query.WorkspaceID != started.Session.WorkspaceID || loader.query.Limit != maxContextEntries ||
		loader.query.TaskScopeID != started.Session.ID {
		t.Fatalf("context query=%+v calls=%d", loader.query, loader.calls)
	}
	if scorer.calls != 1 || len(scorer.input.PersonalContext.Preferences) != 1 || len(scorer.input.PersonalContext.Context) != 2 ||
		string(scorer.input.PersonalContext.Preferences[0]) != `{"answer_style":"concise"}` ||
		string(scorer.input.PersonalContext.Context[0]) != `{"goal":"practice-boundaries"}` {
		t.Fatalf("scorer input=%+v calls=%d", scorer.input, scorer.calls)
	}
	if result.Turn.ScorerVersion != (DeterministicScorer{}).Version() || len(result.Turn.Score.Evidence) != 1 ||
		result.Turn.Score.Evidence[0] != started.Questions[0].Evidence[0] ||
		result.Turn.Score.Boundaries.Rationale != "Boundary practice focused on explicit conditions and limits." {
		t.Fatalf("context changed evidence-bound score: %+v", result.Turn.Score)
	}
	for _, turn := range []domain.Turn{result.Turn, store.submit.Turn} {
		persisted, err := json.Marshal(turn)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"answer_style", "practice-boundaries", "memory_ref", memoryIdentityCanary} {
			if strings.Contains(string(persisted), forbidden) {
				t.Fatalf("personal context leaked into turn: %s", persisted)
			}
		}
	}
}

func TestServiceSubmitFailsClosedBeforeScoringWhenPersonalContextIsUnavailableOrInvalid(t *testing.T) {
	tests := []struct {
		name   string
		loader *fakeContextLoader
	}{
		{name: "loader unavailable", loader: &fakeContextLoader{err: domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "memory unavailable")}},
		{name: "non canonical context", loader: &fakeContextLoader{result: PersonalContext{Preferences: []json.RawMessage{json.RawMessage(`{ "answer_style": "concise" }`)}}}},
		{name: "duplicate nested context key", loader: &fakeContextLoader{result: PersonalContext{Context: []json.RawMessage{json.RawMessage(`{"goal":{"name":"one","name":"two"}}`)}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			scorer := &recordingScorer{}
			service := newTestServiceWithContext(t, store, []domain.Material{applicationMaterial(t)}, test.loader, scorer)
			started, err := service.Start(context.Background(), StartCommand{
				WorkspaceID: applicationID(1), Config: applicationConfig(), IdempotencyKey: "interview-context-failure-start",
			})
			if err != nil {
				t.Fatal(err)
			}
			store.snapshot = Snapshot{Session: started.Session, Questions: started.Questions}
			_, err = service.SubmitTurn(context.Background(), SubmitTurnCommand{
				WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, QuestionID: started.Questions[0].ID,
				UserAnswer: "answer", IdempotencyKey: "interview-context-failure-turn",
			})
			if err == nil {
				t.Fatal("context failure must reject scoring")
			}
			if scorer.calls != 0 || store.submit.Turn.ID != "" {
				t.Fatalf("rejected context reached scorer/store: scorer_calls=%d turn=%+v", scorer.calls, store.submit.Turn)
			}
		})
	}
}

func TestServiceSuggestMemoryCandidateUsesPersistedStepBindingAndPreservesClientKey(t *testing.T) {
	path, step := applicationPathFixture()
	store := &fakeStore{path: PathSnapshot{Path: path, Steps: []domain.PathStep{step}}}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})

	first, err := service.SuggestMemoryCandidate(context.Background(), SuggestMemoryCandidateCommand{
		WorkspaceID: path.WorkspaceID, SessionID: path.SessionID, PathID: path.ID, StepID: step.ID,
		IdempotencyKey: "interview-memory-candidate-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SuggestMemoryCandidate(context.Background(), SuggestMemoryCandidateCommand{
		WorkspaceID: path.WorkspaceID, SessionID: path.SessionID, PathID: path.ID, StepID: step.ID,
		IdempotencyKey: "interview-memory-candidate-after-refresh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.candidateWriter.requests) != 2 {
		t.Fatalf("candidate requests=%+v", store.candidateWriter.requests)
	}
	firstRequest, secondRequest := store.candidateWriter.requests[0], store.candidateWriter.requests[1]
	if firstRequest.WorkspaceID != path.WorkspaceID || firstRequest.SessionID != path.SessionID || firstRequest.PathID != path.ID || firstRequest.StepID != step.ID ||
		string(firstRequest.Content) != `{"goal":"Practice cancellation","rationale":"Close the scored evidence gap."}` ||
		firstRequest.IdempotencyKey != "interview-memory-candidate-first" || secondRequest.IdempotencyKey != "interview-memory-candidate-after-refresh" ||
		firstRequest.WorkspaceID != secondRequest.WorkspaceID || firstRequest.SessionID != secondRequest.SessionID || firstRequest.PathID != secondRequest.PathID ||
		firstRequest.StepID != secondRequest.StepID || !reflect.DeepEqual(firstRequest.Content, secondRequest.Content) {
		t.Fatalf("candidate binding drifted: first=%+v second=%+v", firstRequest, secondRequest)
	}
	if first.MemoryID == "" || second.MemoryID != first.MemoryID || first.Replayed || second.Replayed {
		t.Fatalf("candidate results first=%+v second=%+v", first, second)
	}
}

func TestServiceSuggestMemoryCandidateRejectsCrossBoundSessionPathAndStep(t *testing.T) {
	path, step := applicationPathFixture()
	tests := []struct {
		name    string
		command SuggestMemoryCandidateCommand
		mutate  func(*PathSnapshot)
	}{
		{
			name:    "session mismatch",
			command: SuggestMemoryCandidateCommand{WorkspaceID: path.WorkspaceID, SessionID: applicationID(90), PathID: path.ID, StepID: step.ID, IdempotencyKey: "interview-memory-session-mismatch"},
		},
		{
			name:    "path mismatch",
			command: SuggestMemoryCandidateCommand{WorkspaceID: path.WorkspaceID, SessionID: path.SessionID, PathID: path.ID, StepID: step.ID, IdempotencyKey: "interview-memory-path-mismatch"},
			mutate:  func(snapshot *PathSnapshot) { snapshot.Path.ID = applicationID(91) },
		},
		{
			name:    "step missing",
			command: SuggestMemoryCandidateCommand{WorkspaceID: path.WorkspaceID, SessionID: path.SessionID, PathID: path.ID, StepID: applicationID(92), IdempotencyKey: "interview-memory-step-missing"},
		},
		{
			name:    "step path mismatch",
			command: SuggestMemoryCandidateCommand{WorkspaceID: path.WorkspaceID, SessionID: path.SessionID, PathID: path.ID, StepID: step.ID, IdempotencyKey: "interview-memory-step-path-mismatch"},
			mutate:  func(snapshot *PathSnapshot) { snapshot.Steps[0].PathID = applicationID(93) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := PathSnapshot{Path: path, Steps: []domain.PathStep{step}}
			if test.mutate != nil {
				test.mutate(&snapshot)
			}
			store := &fakeStore{path: snapshot}
			service := newTestService(t, store, []domain.Material{applicationMaterial(t)})
			if _, err := service.SuggestMemoryCandidate(context.Background(), test.command); err == nil {
				t.Fatal("cross-bound candidate request must fail")
			}
			if len(store.candidateWriter.requests) != 0 {
				t.Fatalf("rejected candidate reached Memory: %+v", store.candidateWriter.requests)
			}
		})
	}
}

func TestServiceCompletePersistsArtifactBindingsAndStableCompletionIdentity(t *testing.T) {
	store := &fakeStore{}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})
	started, err := service.Start(context.Background(), StartCommand{WorkspaceID: applicationID(1), Config: applicationConfig(), IdempotencyKey: "interview-start-3"})
	if err != nil {
		t.Fatal(err)
	}
	question := started.Questions[0]
	answeredAt := time.Date(2026, 7, 27, 9, 1, 0, 0, time.UTC)
	question.Status, question.AnsweredAt = domain.QuestionStatusAnswered, &answeredAt
	score := domain.Score{SchemaVersion: domain.ScoreSchemaVersion, Correctness: scoreDimension(0.1), Coverage: scoreDimension(0.1), Boundaries: scoreDimension(0.1), Clarity: scoreDimension(0.4), Omissions: []string{"Missing coverage for: evidence"}, Evidence: question.Evidence}
	rawAnswerCanary := "raw-answer-canary-9472"
	turn := domain.Turn{ID: applicationID(50), WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, QuestionID: question.ID, IdempotencyKey: "turn-frozen", RequestHash: strings.Repeat("b", 64), UserAnswer: rawAnswerCanary, Score: score, Decision: domain.TurnDecision{}, ScorerVersion: (DeterministicScorer{}).Version(), CreatedAt: answeredAt}
	if err := domain.ValidateTurn(turn, question); err != nil {
		t.Fatal(err)
	}
	store.snapshot = Snapshot{Session: started.Session, Questions: []domain.Question{question}, Turns: []domain.Turn{turn}}
	result, err := service.Complete(context.Background(), CompleteCommand{WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, IdempotencyKey: "interview-complete-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Artifact.Kind != ArtifactKindInterviewDocument || result.Path.Artifact.Kind != ArtifactKindLearningPath || result.Path.Artifact.ArtifactID == "" || len(store.complete.Steps) != 1 {
		t.Fatalf("artifact/path binding not persisted: %+v", result)
	}
	if len(store.bridge.requests) != 2 {
		t.Fatalf("unexpected bridge requests: %+v", store.bridge.requests)
	}
	reportRequest, pathRequest := store.bridge.requests[0], store.bridge.requests[1]
	reportPrefix := "iv1:" + string(started.Session.ID) + ":" + ArtifactKindInterviewDocument + ":"
	pathPrefix := "iv1:" + string(started.Session.ID) + ":" + ArtifactKindLearningPath + ":"
	reportDigest := strings.TrimPrefix(reportRequest.IdempotencyBaseKey, reportPrefix)
	pathDigest := strings.TrimPrefix(pathRequest.IdempotencyBaseKey, pathPrefix)
	if reportRequest.WorkspaceID != started.Session.WorkspaceID || reportRequest.Kind != ArtifactKindInterviewDocument ||
		pathRequest.WorkspaceID != started.Session.WorkspaceID || pathRequest.Kind != ArtifactKindLearningPath ||
		reportRequest.VisibilityHold.OwnerID != started.Session.ID || reportRequest.VisibilityHold.Role != ArtifactVisibilityHoldRoleReport ||
		pathRequest.VisibilityHold.OwnerID != started.Session.ID || pathRequest.VisibilityHold.Role != ArtifactVisibilityHoldRolePath ||
		reportRequest.VisibilityHold.AttemptDigest != reportDigest || pathRequest.VisibilityHold.AttemptDigest != reportDigest ||
		len(reportDigest) != 64 || reportDigest != pathDigest || len(reportRequest.IdempotencyBaseKey)+2 > 128 {
		t.Fatalf("unexpected Artifact draft identity: report=%+v path=%+v", reportRequest, pathRequest)
	}
	if _, err := hex.DecodeString(reportDigest); err != nil {
		t.Fatalf("invalid Artifact completion digest %q: %v", reportDigest, err)
	}
	if reportRequest.Section.Key != "report" || !strings.Contains(reportRequest.Section.Markdown, "# Interview report") ||
		pathRequest.Section.Key != "learning-path" || !strings.Contains(pathRequest.Section.Markdown, "# Learning path") ||
		strings.Contains(pathRequest.Section.Markdown, "- Status:") ||
		reportRequest.Section.Markdown != strings.TrimSpace(reportRequest.Section.Markdown) ||
		pathRequest.Section.Markdown != strings.TrimSpace(pathRequest.Section.Markdown) ||
		!reflect.DeepEqual(reportRequest.Section.Evidence, question.Evidence) || !reflect.DeepEqual(pathRequest.Section.Evidence, question.Evidence) ||
		string(reportRequest.Scope) == "" || string(pathRequest.Scope) == "" {
		t.Fatalf("incomplete Artifact draft content: report=%+v path=%+v", reportRequest, pathRequest)
	}
	for _, markdown := range []string{reportRequest.Section.Markdown, pathRequest.Section.Markdown} {
		if strings.Contains(markdown, rawAnswerCanary) || strings.Contains(markdown, "memory-identity-canary") {
			t.Fatalf("Artifact Markdown leaked private answer or Memory context: %s", markdown)
		}
	}
	firstReportID, firstPathID := result.Report.ID, result.Path.ID
	secondReportID, err := derivedID(started.Session.ID, "report")
	if err != nil || firstReportID != secondReportID {
		t.Fatalf("report ID is not retry-stable: %s %s %v", firstReportID, secondReportID, err)
	}
	secondPathID, err := derivedID(started.Session.ID, "learning-path")
	if err != nil || firstPathID != secondPathID {
		t.Fatalf("path ID is not retry-stable: %s %s %v", firstPathID, secondPathID, err)
	}
	if len(store.candidateWriter.requests) != 0 {
		t.Fatalf("completing an interview must not create Memory candidates: %+v", store.candidateWriter.requests)
	}
}

func TestRenderLearningPathMarkdownFreezesDefinitionWithoutRuntimeStatus(t *testing.T) {
	steps := []domain.PathStep{{
		StepNo: 1, Title: "Review the concurrency contract", Rationale: "Close the scored evidence gap.",
		ClaimID: applicationID(2), SourceVersionID: applicationID(3), SourceSpanID: applicationID(4),
		Status: domain.StepStatusInProgress,
	}}

	markdown := renderLearningPathMarkdown(steps)
	if strings.Contains(markdown, "- Status:") || strings.Contains(markdown, string(domain.StepStatusInProgress)) ||
		!strings.Contains(markdown, steps[0].Title) || !strings.Contains(markdown, string(steps[0].ClaimID)) ||
		!strings.Contains(markdown, string(steps[0].SourceVersionID)) || !strings.Contains(markdown, string(steps[0].SourceSpanID)) {
		t.Fatalf("learning path Artifact must contain only the immutable plan definition: %s", markdown)
	}
}

func TestServiceCompleteReusesArtifactDraftsAfterInterviewStoreFailure(t *testing.T) {
	store := &fakeStore{completeFailures: 1}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})
	started, err := service.Start(context.Background(), StartCommand{
		WorkspaceID: applicationID(1), Config: applicationConfig(), IdempotencyKey: "interview-recovery-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	question := started.Questions[0]
	answeredAt := time.Date(2026, 7, 27, 9, 1, 0, 0, time.UTC)
	question.Status, question.AnsweredAt = domain.QuestionStatusAnswered, &answeredAt
	score := domain.Score{
		SchemaVersion: domain.ScoreSchemaVersion,
		Correctness:   scoreDimension(1), Coverage: scoreDimension(1), Boundaries: scoreDimension(1), Clarity: scoreDimension(1),
		Evidence: question.Evidence,
	}
	turn := domain.Turn{
		ID: applicationID(51), WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, QuestionID: question.ID,
		IdempotencyKey: "interview-recovery-turn", RequestHash: strings.Repeat("c", 64), UserAnswer: "recovery answer",
		Score: score, Decision: domain.TurnDecision{}, ScorerVersion: (DeterministicScorer{}).Version(), CreatedAt: answeredAt,
	}
	store.snapshot = Snapshot{Session: started.Session, Questions: []domain.Question{question}, Turns: []domain.Turn{turn}}
	command := CompleteCommand{WorkspaceID: started.Session.WorkspaceID, SessionID: started.Session.ID, IdempotencyKey: "interview-recovery-complete"}
	if _, err := service.Complete(context.Background(), command); err == nil {
		t.Fatal("first Interview store failure must be observable")
	}
	if len(store.bridge.requests) != 2 {
		t.Fatalf("first completion Artifact requests=%d", len(store.bridge.requests))
	}
	result, err := service.Complete(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if store.completeCalls != 2 || len(store.bridge.requests) != 4 || result.Report.Artifact.ArtifactVersion != 4 || result.Path.Artifact.ArtifactVersion != 4 {
		t.Fatalf("completion recovery result=%+v calls=%d Artifact requests=%d", result, store.completeCalls, len(store.bridge.requests))
	}
	if !reflect.DeepEqual(store.bridge.requests[0], store.bridge.requests[2]) || !reflect.DeepEqual(store.bridge.requests[1], store.bridge.requests[3]) {
		t.Fatalf("completion recovery changed Artifact identities: %+v", store.bridge.requests)
	}
	for _, request := range store.bridge.requests[:2] {
		if !strings.Contains(string(request.Scope), string(started.Session.ID)) {
			t.Fatalf("Artifact scope cannot be associated with Interview session: %s", request.Scope)
		}
	}
}

func TestServiceUpdatePathStatusRequiresTerminalStepsForCompletion(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	path := domain.LearningPath{
		ID:          applicationID(80),
		WorkspaceID: applicationID(1),
		SessionID:   applicationID(10),
		ReportID:    applicationID(13),
		Artifact:    domain.ArtifactBinding{Kind: ArtifactKindLearningPath, ArtifactID: applicationID(73), RevisionID: applicationID(74), ArtifactVersion: 1},
		Status:      domain.PathStatusActive,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	store := &fakeStore{path: PathSnapshot{
		Path:  path,
		Steps: []domain.PathStep{{ID: applicationID(81), Status: domain.StepStatusPending}},
	}}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})

	_, err := service.UpdatePathStatus(context.Background(), UpdatePathStatusCommand{
		WorkspaceID: path.WorkspaceID, PathID: path.ID, ExpectedVersion: path.Version,
		Status: domain.PathStatusCompleted, IdempotencyKey: "learning-path-complete-pending",
	})
	if err == nil {
		t.Fatal("completion with a pending step must fail")
	}
	if store.pathStatus.IdempotencyKey != "" {
		t.Fatal("rejected completion must not reach the persistence update")
	}

	store.path.Steps[0].Status = domain.StepStatusSkipped
	result, err := service.UpdatePathStatus(context.Background(), UpdatePathStatusCommand{
		WorkspaceID: path.WorkspaceID, PathID: path.ID, ExpectedVersion: path.Version,
		Status: domain.PathStatusCompleted, IdempotencyKey: "learning-path-complete-terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path.Status != domain.PathStatusCompleted || store.pathStatus.Status != domain.PathStatusCompleted || store.pathStatus.ExpectedVersion != 1 {
		t.Fatalf("terminal path was not completed: result=%+v record=%+v", result, store.pathStatus)
	}
}

type fakeStore struct {
	start            StartRecord
	submit           SubmitRecord
	complete         CompleteRecord
	pathStatus       UpdatePathStatusRecord
	snapshot         Snapshot
	path             PathSnapshot
	list             SessionListPage
	bridge           *fakeArtifactBridge
	candidateWriter  *fakeMemoryCandidateWriter
	completeFailures int
	completeCalls    int
	reservation      CompletionReservation
}

func (s *fakeStore) FindStartReplay(context.Context, foundation.ID, string, string) (StartResult, bool, error) {
	return StartResult{}, false, nil
}
func (s *fakeStore) Start(_ context.Context, record StartRecord) (StartResult, error) {
	s.start = record
	return StartResult{Session: record.Session, Questions: record.Questions}, nil
}
func (s *fakeStore) Get(context.Context, foundation.ID, foundation.ID) (Snapshot, error) {
	return s.snapshot, nil
}
func (s *fakeStore) List(context.Context, SessionListQuery) (SessionListPage, error) {
	return s.list, nil
}
func (s *fakeStore) FindSubmitReplay(context.Context, foundation.ID, string, string) (SubmitTurnResult, bool, error) {
	return SubmitTurnResult{}, false, nil
}
func (s *fakeStore) Submit(_ context.Context, record SubmitRecord) (SubmitTurnResult, error) {
	s.submit = record
	questions := append([]domain.Question(nil), s.snapshot.Questions...)
	for index := range questions {
		if questions[index].ID == record.Question.ID {
			answeredAt := record.Turn.CreatedAt
			questions[index].Status, questions[index].AnsweredAt = domain.QuestionStatusAnswered, &answeredAt
		}
	}
	if record.FollowUp != nil {
		questions = append(questions, *record.FollowUp)
	}
	s.snapshot.Questions = questions
	s.snapshot.Turns = append(s.snapshot.Turns, record.Turn)
	s.snapshot.Session.Version++
	if record.FollowUp != nil {
		s.snapshot.Session.FollowUpCount++
	}
	return SubmitTurnResult{Turn: record.Turn, FollowUp: record.FollowUp, NextQuestion: record.FollowUp}, nil
}
func (s *fakeStore) FindCompleteReplay(context.Context, foundation.ID, string, string) (CompleteResult, bool, error) {
	return CompleteResult{}, false, nil
}
func (s *fakeStore) BeginComplete(_ context.Context, record BeginCompleteRecord) (BeginCompleteResult, error) {
	if s.reservation.Status == "" {
		createdAt := s.snapshot.Session.StartedAt.UTC()
		s.reservation = CompletionReservation{
			WorkspaceID: record.WorkspaceID, SessionID: record.SessionID, IdempotencyKey: record.IdempotencyKey,
			RequestHash: record.RequestHash, ManualEnd: record.ManualEnd, SnapshotVersion: s.snapshot.Session.Version,
			Status: CompletionReservationPending, CreatedAt: createdAt, UpdatedAt: createdAt,
		}
	}
	return BeginCompleteResult{Reservation: s.reservation, Snapshot: s.snapshot}, nil
}
func (s *fakeStore) PrepareComplete(_ context.Context, record PrepareCompleteRecord) (PrepareCompleteResult, error) {
	preparedAt := s.reservation.CreatedAt
	s.reservation.ArtifactDigest = record.ArtifactDigest
	s.reservation.PreparedAt = &preparedAt
	s.reservation.UpdatedAt = preparedAt
	return PrepareCompleteResult{Reservation: s.reservation}, nil
}
func (s *fakeStore) Complete(_ context.Context, record CompleteRecord) (CompleteResult, error) {
	s.completeCalls++
	s.complete = record
	if s.completeFailures > 0 {
		s.completeFailures--
		return CompleteResult{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "interview completion persistence is unavailable")
	}
	completed := s.snapshot.Session
	completed.Status, completed.Version = domain.SessionStatusCompleted, completed.Version+1
	endedAt := record.At
	completed.EndedAt = &endedAt
	return CompleteResult{Session: completed, Report: record.Report, Path: record.Path, Steps: record.Steps}, nil
}
func (s *fakeStore) GetPath(context.Context, foundation.ID, foundation.ID) (PathSnapshot, error) {
	return s.path, nil
}
func (s *fakeStore) FindPathStepReplay(context.Context, foundation.ID, string, string) (PathStepResult, bool, error) {
	return PathStepResult{}, false, nil
}
func (s *fakeStore) UpdatePathStep(context.Context, UpdatePathStepRecord) (PathStepResult, error) {
	return PathStepResult{}, fmt.Errorf("not used")
}
func (s *fakeStore) FindPathStatusReplay(context.Context, foundation.ID, string, string) (PathStatusResult, bool, error) {
	return PathStatusResult{}, false, nil
}
func (s *fakeStore) UpdatePathStatus(_ context.Context, record UpdatePathStatusRecord) (PathStatusResult, error) {
	s.pathStatus = record
	s.path.Path.Status = record.Status
	s.path.Path.Version++
	s.path.Path.UpdatedAt = record.At
	return PathStatusResult{Path: s.path.Path}, nil
}

type fakeQuestionSource struct{ materials []domain.Material }

func (s fakeQuestionSource) Select(context.Context, Selection) ([]domain.Material, error) {
	return append([]domain.Material(nil), s.materials...), nil
}

type fakeContextLoader struct {
	result PersonalContext
	err    error
	query  ContextQuery
	calls  int
}

func (loader *fakeContextLoader) Load(_ context.Context, query ContextQuery) (PersonalContext, error) {
	loader.calls++
	loader.query = query
	return loader.result, loader.err
}

type recordingScorer struct {
	input ScoreInput
	calls int
}

func (scorer *recordingScorer) Version() string { return DeterministicScorer{}.Version() }

func (scorer *recordingScorer) Score(ctx context.Context, input ScoreInput) (domain.Score, error) {
	scorer.calls++
	scorer.input = input
	return DeterministicScorer{}.Score(ctx, input)
}

type fakeArtifactBridge struct{ requests []ArtifactDraftRequest }

func (b *fakeArtifactBridge) CreateDraft(_ context.Context, request ArtifactDraftRequest) (ArtifactDraftResult, error) {
	b.requests = append(b.requests, request)
	if request.Kind == ArtifactKindInterviewDocument {
		return ArtifactDraftResult{Binding: domain.ArtifactBinding{Kind: ArtifactKindInterviewDocument, ArtifactID: applicationID(71), RevisionID: applicationID(72), ArtifactVersion: 4}}, nil
	}
	return ArtifactDraftResult{Binding: domain.ArtifactBinding{Kind: ArtifactKindLearningPath, ArtifactID: applicationID(73), RevisionID: applicationID(74), ArtifactVersion: 4}}, nil
}

type fakeMemoryCandidateWriter struct {
	requests []MemoryCandidateRequest
	memoryID foundation.ID
	err      error
}

func (writer *fakeMemoryCandidateWriter) CreateCandidate(_ context.Context, request MemoryCandidateRequest) (MemoryCandidateResult, error) {
	if writer.err != nil {
		return MemoryCandidateResult{}, writer.err
	}
	replayed := false
	for _, previous := range writer.requests {
		if previous.IdempotencyKey == request.IdempotencyKey {
			replayed = true
			break
		}
	}
	writer.requests = append(writer.requests, request)
	return MemoryCandidateResult{MemoryID: writer.memoryID, Replayed: replayed}, nil
}

type fakeIDs struct{ next int }

func (g *fakeIDs) New() (foundation.ID, error) {
	g.next++
	return applicationID(100 + g.next), nil
}

func newTestService(t *testing.T, store *fakeStore, materials []domain.Material) *Service {
	return newTestServiceWithContext(t, store, materials, &fakeContextLoader{}, DeterministicScorer{})
}

func newTestServiceWithContext(t *testing.T, store *fakeStore, materials []domain.Material, loader ContextLoader, scorer Scorer) *Service {
	t.Helper()
	bridge := &fakeArtifactBridge{}
	candidateWriter := &fakeMemoryCandidateWriter{memoryID: applicationID(90)}
	store.bridge = bridge
	store.candidateWriter = candidateWriter
	service, err := NewService(Dependencies{Store: store, QuestionSource: fakeQuestionSource{materials: materials}, ContextLoader: loader, CandidateWriter: candidateWriter, Scorer: scorer, ArtifactBridge: bridge, IDGenerator: &fakeIDs{}, Clock: foundation.FixedClock{Value: time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func applicationPathFixture() (domain.LearningPath, domain.PathStep) {
	now := time.Date(2026, 7, 27, 9, 2, 0, 0, time.UTC)
	path := domain.LearningPath{
		ID: applicationID(80), WorkspaceID: applicationID(1), SessionID: applicationID(10), ReportID: applicationID(13),
		Artifact: domain.ArtifactBinding{Kind: ArtifactKindLearningPath, ArtifactID: applicationID(73), RevisionID: applicationID(74), ArtifactVersion: 1},
		Status:   domain.PathStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	step := domain.PathStep{
		ID: applicationID(81), WorkspaceID: path.WorkspaceID, PathID: path.ID, StepNo: 1,
		ClaimID: applicationID(2), SourceVersionID: applicationID(3), SourceSpanID: applicationID(4), EvidenceHash: strings.Repeat("a", 64),
		Title: "Practice cancellation", Rationale: "Close the scored evidence gap.", Status: domain.StepStatusPending, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	return path, step
}

func applicationConfig() domain.Config {
	return domain.Config{SchemaVersion: domain.SchemaVersion, Role: "backend engineer", Scope: domain.Scope{ClaimIDs: []foundation.ID{applicationID(2)}}, Difficulty: domain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1, MaxFollowUps: 1}
}

func applicationMaterial(t *testing.T) domain.Material {
	t.Helper()
	claimID := applicationID(2)
	return domain.Material{ClaimID: claimID, ClaimStatus: "CONFIRMED", Statement: "A service must preserve evidence-backed idempotency.", AnswerPoints: []string{"A service must preserve evidence-backed idempotency."}, Evidence: []domain.EvidenceRef{{SchemaVersion: domain.EvidenceSchemaVersion, ClaimID: claimID, IndexVersionID: applicationID(5), ChunkID: applicationID(6), SourceVersionID: applicationID(3), SourceSpanID: applicationID(4), EvidenceHash: strings.Repeat("a", 64), SupportType: "SUPPORTS"}}}
}

func applicationID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("00000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}

func scoreDimension(value float64) domain.ScoreDimension {
	return domain.ScoreDimension{Value: value, Rationale: "deterministic test rationale"}
}
