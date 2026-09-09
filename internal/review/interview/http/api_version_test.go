package interviewhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestInterviewAPIVersionsGuardNoteCommandsBeforeReplayAndEffects(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	fixture.activeSession.Config.MaxFollowUps = 0
	source := versionedHTTPNoteSource(t, fixture.workspaceID)
	fixture.activeSession.Config.Scope = domain.Scope{NoteRevision: &source.Revision}
	fixture.pendingQuestion.SourceKind, fixture.pendingQuestion.NoteSource = domain.QuestionSourceNoteRevision, source
	fixture.pendingQuestion.ClaimID, fixture.pendingQuestion.Evidence = "", []domain.EvidenceRef{}
	store, scorer, bridge, router := versionedHTTPApplication(t, fixture)
	workspace := string(fixture.workspaceID)
	session := "/review/interviews/" + string(fixture.sessionID)
	submitBody := `{"workspace_id":"` + workspace + `","question_id":"` + string(fixture.questionID) + `","user_answer":""}`
	completeBody := `{"workspace_id":"` + workspace + `","manual_end":true}`

	rejectWithoutEffects := func(method, path, body, key string) {
		t.Helper()
		before, err := json.Marshal(store.snapshot)
		if err != nil {
			t.Fatal(err)
		}
		counts, scores, artifacts := store.calls, scorer.calls, bridge.calls
		response := versionedHTTPRequest(t, router, method, "/api/v1"+path, body, key)
		requireInterviewProblem(t, response, http.StatusConflict, interviewapp.ErrorCodeAPIVersionUnsupported, false)
		after, err := json.Marshal(store.snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) || store.calls != counts || scorer.calls != scores || bridge.calls != artifacts {
			t.Fatalf("v1 NOTE request reached replay or mutation: before=%v after=%v scores=%d artifacts=%d", counts, store.calls, scorer.calls-scores, bridge.calls-artifacts)
		}
	}

	rejectWithoutEffects(http.MethodGet, session+"?workspace_id="+workspace, "", "")
	rejectWithoutEffects(http.MethodPost, session+"/turns", submitBody, "note-submit")
	rejectWithoutEffects(http.MethodPost, session+"/complete", completeBody, "note-complete")
	get := versionedHTTPRequest(t, router, http.MethodGet, "/api/v2"+session+"?workspace_id="+workspace, "", "")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"claim_id":null`) || !strings.Contains(get.Body.String(), `"source_kind":"NOTE_REVISION"`) {
		t.Fatalf("v2 NOTE read: status=%d body=%s", get.Code, get.Body.String())
	}

	first := versionedHTTPRequest(t, router, http.MethodPost, "/api/v2"+session+"/turns", submitBody, "note-submit")
	requireVersionedOK(t, first, false)
	if scorer.calls != 1 || store.calls.submit != 1 || store.submitRecord.ClaimOnly {
		t.Fatal("v2 NOTE submit did not reach the unrestricted application path")
	}
	rejectWithoutEffects(http.MethodPost, session+"/turns", submitBody, "note-submit")
	replay := versionedHTTPRequest(t, router, http.MethodPost, "/api/v2"+session+"/turns", submitBody, "note-submit")
	requireVersionedOK(t, replay, true)
	if scorer.calls != 1 || store.calls.submit != 1 {
		t.Fatal("v2 submit replay repeated scoring or persistence")
	}

	first = versionedHTTPRequest(t, router, http.MethodPost, "/api/v2"+session+"/complete", completeBody, "note-complete")
	requireVersionedOK(t, first, false)
	if store.calls.begin != 1 || store.calls.prepare != 1 || store.calls.complete != 1 || bridge.calls != 2 || len(store.snapshot.Steps) != 1 {
		t.Fatalf("v2 NOTE completion did not produce one report/path: calls=%+v artifacts=%d", store.calls, bridge.calls)
	}
	rejectWithoutEffects(http.MethodPost, session+"/complete", completeBody, "note-complete")
	replay = versionedHTTPRequest(t, router, http.MethodPost, "/api/v2"+session+"/complete", completeBody, "note-complete")
	requireVersionedOK(t, replay, true)
	if store.calls.complete != 1 || bridge.calls != 2 {
		t.Fatal("v2 completion replay repeated an Artifact or completion")
	}

	step := store.snapshot.Steps[0]
	stepPath := "/review/learning-paths/" + string(step.PathID) + "/steps/" + string(step.ID)
	stepBody := `{"workspace_id":"` + workspace + `","expected_version":1,"status":"IN_PROGRESS"}`
	rejectWithoutEffects(http.MethodPut, stepPath, stepBody, "note-step")
	first = versionedHTTPRequest(t, router, http.MethodPut, "/api/v2"+stepPath, stepBody, "note-step")
	requireVersionedOK(t, first, false)
	if store.calls.step != 1 || store.stepRecord.ClaimOnly || store.snapshot.Steps[0].Status != domain.StepStatusInProgress {
		t.Fatal("v2 NOTE step update did not persist its progress")
	}
	rejectWithoutEffects(http.MethodPut, stepPath, stepBody, "note-step")
	replay = versionedHTTPRequest(t, router, http.MethodPut, "/api/v2"+stepPath, stepBody, "note-step")
	requireVersionedOK(t, replay, true)
	if store.calls.step != 1 {
		t.Fatal("v2 step replay repeated persistence")
	}
	for _, version := range []string{"v1", "v2"} {
		response := versionedHTTPRequest(t, router, http.MethodGet, "/api/"+version+session+"?workspace_id="+string(interviewHTTPID(t, 99)), "", "")
		requireInterviewProblem(t, response, http.StatusNotFound, domain.ErrorCodeSessionNotFound, false)
	}
}

func TestInterviewClaimCommandsReplayAcrossAPIVersions(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	fixture.activeSession.Config.MaxFollowUps = 0
	store, scorer, bridge, router := versionedHTTPApplication(t, fixture)
	workspace := string(fixture.workspaceID)
	session := "/review/interviews/" + string(fixture.sessionID)
	submitBody := `{"workspace_id":"` + workspace + `","question_id":"` + string(fixture.questionID) + `","user_answer":""}`
	first := versionedHTTPRequest(t, router, http.MethodPost, "/api/v1"+session+"/turns", submitBody, "claim-submit")
	requireVersionedOK(t, first, false)
	if !store.submitRecord.ClaimOnly {
		t.Fatal("v1 source constraint was lost before Store.Submit")
	}
	replay := versionedHTTPRequest(t, router, http.MethodPost, "/api/v2"+session+"/turns", submitBody, "claim-submit")
	requireVersionedOK(t, replay, true)
	if scorer.calls != 1 || store.calls.submit != 1 {
		t.Fatal("changing API version changed submit identity")
	}
	completeBody := `{"workspace_id":"` + workspace + `","manual_end":true}`
	first = versionedHTTPRequest(t, router, http.MethodPost, "/api/v1"+session+"/complete", completeBody, "claim-complete")
	requireVersionedOK(t, first, false)
	if !store.beginRecord.ClaimOnly || !store.prepareRecord.ClaimOnly || !store.completeRecord.ClaimOnly {
		t.Fatal("v1 source constraint was lost during completion")
	}
	replay = versionedHTTPRequest(t, router, http.MethodPost, "/api/v2"+session+"/complete", completeBody, "claim-complete")
	requireVersionedOK(t, replay, true)
	if store.calls.complete != 1 || bridge.calls != 2 {
		t.Fatal("changing API version changed completion identity")
	}
	step := store.snapshot.Steps[0]
	stepPath := "/review/learning-paths/" + string(step.PathID) + "/steps/" + string(step.ID)
	stepBody := `{"workspace_id":"` + workspace + `","expected_version":1,"status":"IN_PROGRESS"}`
	first = versionedHTTPRequest(t, router, http.MethodPut, "/api/v1"+stepPath, stepBody, "claim-step")
	requireVersionedOK(t, first, false)
	if !store.stepRecord.ClaimOnly {
		t.Fatal("v1 source constraint was lost before Store.UpdatePathStep")
	}
	replay = versionedHTTPRequest(t, router, http.MethodPut, "/api/v2"+stepPath, stepBody, "claim-step")
	requireVersionedOK(t, replay, true)
	if store.calls.step != 1 {
		t.Fatal("changing API version changed path-step identity")
	}
	for _, response := range []*httptest.ResponseRecorder{first, replay} {
		for _, forbidden := range []string{`"source_kind"`, `"note_revision"`, `"note_source"`, `"claim_id":null`} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("Claim response broke the legacy projection: %s", response.Body.String())
			}
		}
	}
}

func TestInterviewListCursorBindsAPISourceSet(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	service := &fakeInterviewService{listResult: interviewapp.SessionListPage{Items: []domain.Session{fixture.activeSession}, Next: &interviewapp.SessionCursor{StartedAt: fixture.activeSession.StartedAt, ID: fixture.sessionID}}}
	router := protectedInterviewRouter(t, service, defaultInterviewPrincipal())
	for _, version := range []string{"v1", "v2"} {
		path := "/api/" + version + "/review/interviews?workspace_id=" + string(fixture.workspaceID) + "&limit=1"
		first := serveInterview(t, router, http.MethodGet, path, "", nil)
		if first.Code != http.StatusOK || service.listQuery.ClaimOnly != (version == "v1") {
			t.Fatalf("%s list did not forward its source constraint: status=%d query=%+v", version, first.Code, service.listQuery)
		}
		var page sessionListResponse
		decodeInterviewJSON(t, first, &page)
		same := serveInterview(t, router, http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), "", nil)
		if same.Code != http.StatusOK {
			t.Fatalf("same-version cursor failed: %d %s", same.Code, same.Body.String())
		}
		other := "v1"
		if version == "v1" {
			other = "v2"
		}
		before := service.listCalls
		cross := serveInterview(t, router, http.MethodGet, strings.Replace(path, version, other, 1)+"&cursor="+url.QueryEscape(page.NextCursor), "", nil)
		requireInterviewProblem(t, cross, http.StatusBadRequest, errorCodeCursorInvalid, false)
		if service.listCalls != before {
			t.Fatal("cross-version cursor reached the application")
		}
	}
	ref := versionedHTTPNoteSource(t, fixture.workspaceID).Revision
	service.listResult.Items[0].Config.Scope = domain.Scope{NoteRevision: &ref}
	response := serveInterview(t, router, http.MethodGet, "/api/v1/review/interviews?workspace_id="+string(fixture.workspaceID)+"&limit=1", "", nil)
	requireInterviewProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid, false)
}

func versionedHTTPNoteSource(t *testing.T, workspace foundation.ID) *domain.NoteQuestionSource {
	t.Helper()
	id := func(n int) foundation.ID { return interviewHTTPID(t, n+300) }
	return &domain.NoteQuestionSource{Revision: domain.NoteRevisionRef{WorkspaceID: workspace, NoteID: id(1), RevisionID: id(2), DocumentID: id(3), ArticleRevisionID: id(4), RevisionNo: 1, ArticleRevisionNo: 1, ContentHash: strings.Repeat("a", 64), ProjectionHash: strings.Repeat("b", 64), Title: "Published note"}, ItemID: id(5), ItemKind: organizingdomain.SynthesisFactItem, Sources: []organizingdomain.SynthesisSourceRef{{Source: organizingdomain.SynthesisSourceVersion{WorkspaceID: workspace, SourceID: id(6), SourceVersionID: id(7), ContentArtifactID: id(8), ParseProjectionID: id(9), ContentHash: strings.Repeat("c", 64)}, SourceSpanID: id(10), ExcerptHash: strings.Repeat("d", 64), Title: "Original source"}}}
}

func versionedHTTPRequest(t *testing.T, router http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return serveInterview(t, router, method, path, body, func(request *http.Request) {
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
	})
}

func requireVersionedOK(t *testing.T, response *httptest.ResponseRecorder, replayed bool) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Replayed *bool `json:"replayed"`
	}
	decodeInterviewJSON(t, response, &body)
	if body.Replayed == nil || *body.Replayed != replayed {
		t.Fatalf("response replay flag does not match %t: %s", replayed, response.Body.String())
	}
}

func versionedHTTPApplication(t *testing.T, fixture interviewHTTPFixture) (*versionedHTTPStore, *versionedHTTPScorer, *versionedHTTPBridge, http.Handler) {
	t.Helper()
	store := &versionedHTTPStore{snapshot: interviewapp.Snapshot{Session: fixture.activeSession, Questions: []domain.Question{fixture.pendingQuestion}}}
	scorer := &versionedHTTPScorer{}
	bridge := &versionedHTTPBridge{fixture: fixture}
	service, err := interviewapp.NewService(interviewapp.Dependencies{Store: store,
		QuestionSource: struct{ interviewapp.QuestionSource }{}, CandidateWriter: struct {
			interviewapp.MemoryCandidateWriter
		}{},
		ContextLoader: versionedHTTPContext{}, Scorer: scorer, ArtifactBridge: bridge,
		IDGenerator: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: fixture.activeSession.StartedAt.Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	return store, scorer, bridge, protectedInterviewRouter(t, service, defaultInterviewPrincipal())
}

type versionedHTTPStore struct {
	interviewapp.Store // Unexpected application paths fail instead of producing a fake success.
	snapshot           interviewapp.Snapshot
	calls              struct{ submitReplay, completeReplay, stepReplay, submit, begin, prepare, complete, step int }
	submitRecord       interviewapp.SubmitRecord
	submitResult       interviewapp.SubmitTurnResult
	beginRecord        interviewapp.BeginCompleteRecord
	prepareRecord      interviewapp.PrepareCompleteRecord
	completeRecord     interviewapp.CompleteRecord
	completeResult     interviewapp.CompleteResult
	stepRecord         interviewapp.UpdatePathStepRecord
	stepResult         interviewapp.PathStepResult
}

func (s *versionedHTTPStore) Get(_ context.Context, workspace, session foundation.ID) (interviewapp.Snapshot, error) {
	if s.snapshot.Session.WorkspaceID != workspace || s.snapshot.Session.ID != session {
		return interviewapp.Snapshot{}, domain.NotFoundError(domain.ErrorCodeSessionNotFound, "not found")
	}
	return s.snapshot, nil
}
func (s *versionedHTTPStore) GetPath(_ context.Context, workspace, path foundation.ID) (interviewapp.PathSnapshot, error) {
	if s.snapshot.Path == nil || s.snapshot.Path.WorkspaceID != workspace || s.snapshot.Path.ID != path {
		return interviewapp.PathSnapshot{}, domain.NotFoundError(domain.ErrorCodePathInvalid, "not found")
	}
	return interviewapp.PathSnapshot{Path: *s.snapshot.Path, Steps: s.snapshot.Steps}, nil
}
func (s *versionedHTTPStore) FindSubmitReplay(_ context.Context, _ foundation.ID, key, hash string) (interviewapp.SubmitTurnResult, bool, error) {
	s.calls.submitReplay++
	result := s.submitResult
	result.Replayed = true
	return result, s.submitRecord.IdempotencyKey == key && s.submitRecord.RequestHash == hash, nil
}
func (s *versionedHTTPStore) Submit(_ context.Context, record interviewapp.SubmitRecord) (interviewapp.SubmitTurnResult, error) {
	s.calls.submit++
	s.submitRecord = record
	s.snapshot.Session.Version++
	s.snapshot.Questions[0].Status, s.snapshot.Questions[0].AnsweredAt = domain.QuestionStatusAnswered, &record.Turn.CreatedAt
	s.snapshot.Turns = append(s.snapshot.Turns, record.Turn)
	s.submitResult = interviewapp.SubmitTurnResult{Turn: record.Turn, FollowUp: record.FollowUp}
	return s.submitResult, nil
}
func (s *versionedHTTPStore) FindCompleteReplay(_ context.Context, _ foundation.ID, key, hash string) (interviewapp.CompleteResult, bool, error) {
	s.calls.completeReplay++
	result := s.completeResult
	result.Replayed = true
	return result, s.completeRecord.IdempotencyKey == key && s.completeRecord.RequestHash == hash, nil
}
func (s *versionedHTTPStore) BeginComplete(_ context.Context, record interviewapp.BeginCompleteRecord) (interviewapp.BeginCompleteResult, error) {
	s.calls.begin++
	s.beginRecord = record
	return interviewapp.BeginCompleteResult{Snapshot: s.snapshot, Reservation: interviewapp.CompletionReservation{Status: interviewapp.CompletionReservationPending, SnapshotVersion: s.snapshot.Session.Version, CreatedAt: s.snapshot.Turns[0].CreatedAt}}, nil
}
func (s *versionedHTTPStore) PrepareComplete(_ context.Context, record interviewapp.PrepareCompleteRecord) (interviewapp.PrepareCompleteResult, error) {
	s.calls.prepare++
	s.prepareRecord = record
	return interviewapp.PrepareCompleteResult{Reservation: interviewapp.CompletionReservation{Status: interviewapp.CompletionReservationPending, ArtifactDigest: record.ArtifactDigest}}, nil
}
func (s *versionedHTTPStore) Complete(_ context.Context, record interviewapp.CompleteRecord) (interviewapp.CompleteResult, error) {
	s.calls.complete++
	s.completeRecord = record
	s.snapshot.Session.Status, s.snapshot.Session.EndedAt = domain.SessionStatusCompleted, &record.At
	s.snapshot.Session.Version++
	s.snapshot.Report, s.snapshot.Path, s.snapshot.Steps = &record.Report, &record.Path, record.Steps
	s.completeResult = interviewapp.CompleteResult{Session: s.snapshot.Session, Report: record.Report, Path: record.Path, Steps: record.Steps}
	return s.completeResult, nil
}
func (s *versionedHTTPStore) FindPathStepReplay(_ context.Context, _ foundation.ID, key, hash string) (interviewapp.PathStepResult, bool, error) {
	s.calls.stepReplay++
	result := s.stepResult
	result.Replayed = true
	return result, s.stepRecord.IdempotencyKey == key && s.stepRecord.RequestHash == hash, nil
}
func (s *versionedHTTPStore) UpdatePathStep(_ context.Context, record interviewapp.UpdatePathStepRecord) (interviewapp.PathStepResult, error) {
	s.calls.step++
	s.stepRecord = record
	step := &s.snapshot.Steps[0]
	if step.ID != record.StepID {
		return interviewapp.PathStepResult{}, fmt.Errorf("unexpected step")
	}
	step.Status, step.UpdatedAt = record.Status, record.At
	step.Version++
	s.snapshot.Path.Version++
	s.snapshot.Path.UpdatedAt = record.At
	s.stepResult = interviewapp.PathStepResult{Path: *s.snapshot.Path, Step: *step}
	return s.stepResult, nil
}

type versionedHTTPScorer struct{ calls int }

func (*versionedHTTPScorer) Version() string { return interviewapp.DeterministicScorer{}.Version() }
func (s *versionedHTTPScorer) Score(ctx context.Context, input interviewapp.ScoreInput) (domain.Score, error) {
	s.calls++
	return interviewapp.DeterministicScorer{}.Score(ctx, input)
}

type versionedHTTPContext struct{}

func (versionedHTTPContext) Load(context.Context, interviewapp.ContextQuery) (interviewapp.PersonalContext, error) {
	return interviewapp.PersonalContext{}, nil
}

type versionedHTTPBridge struct {
	fixture interviewHTTPFixture
	calls   int
}

func (b *versionedHTTPBridge) CreateDraft(_ context.Context, request interviewapp.ArtifactDraftRequest) (interviewapp.ArtifactDraftResult, error) {
	b.calls++
	if request.Kind == interviewapp.ArtifactKindInterviewDocument {
		return interviewapp.ArtifactDraftResult{Binding: b.fixture.report.Artifact}, nil
	}
	return interviewapp.ArtifactDraftResult{Binding: b.fixture.path.Artifact}, nil
}
