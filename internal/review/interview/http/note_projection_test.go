package interviewhttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestNoteInterviewHTTPKeepsQuestionMaterialPrivateAndExposesAnsweredSources(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	id := func(n string) foundation.ID { return foundation.ID("88000000-0000-4000-8000-0000000000" + n) }
	ref := domain.NoteRevisionRef{WorkspaceID: fixture.workspaceID, NoteID: id("01"), RevisionID: id("02"), DocumentID: id("03"), ArticleRevisionID: id("04"), RevisionNo: 1, ArticleRevisionNo: 1, ContentHash: strings.Repeat("a", 64), ProjectionHash: strings.Repeat("b", 64), Title: "Published cache note"}
	source := &domain.NoteQuestionSource{Revision: ref, ItemID: id("05"), ItemKind: organizingdomain.SynthesisFactItem, Sources: []organizingdomain.SynthesisSourceRef{{Source: organizingdomain.SynthesisSourceVersion{WorkspaceID: fixture.workspaceID, SourceID: id("06"), SourceVersionID: id("07"), ContentArtifactID: id("08"), ParseProjectionID: id("09"), ContentHash: strings.Repeat("c", 64)}, SourceSpanID: id("10"), ExcerptHash: strings.Repeat("d", 64), Title: "Original source"}}}
	question := fixture.answeredQuestion
	question.SourceKind = domain.QuestionSourceNoteRevision
	question.NoteSource = source
	question.ClaimID = ""
	question.TopicID = nil
	question.Evidence = []domain.EvidenceRef{}
	question.AnswerPoints = []string{"PRIVATE NOTE ANSWER"}
	question.FollowUpPlan = []domain.NoteFollowUp{{Condition: domain.NoteFollowUpLowCoverage, Prompt: "PRIVATE FOLLOWUP", AnswerPoints: question.AnswerPoints}}
	fixture.activeSession.Config.Scope = domain.Scope{NoteRevision: &ref}
	fixture.turn.Score.NoteSource = domain.CloneNoteSource(source)
	fixture.turn.Score.Evidence = []domain.EvidenceRef{}
	service := &fakeInterviewService{getResult: interviewapp.Snapshot{Session: fixture.activeSession, Questions: []domain.Question{question}, Turns: []domain.Turn{fixture.turn}}}
	router := protectedInterviewRouter(t, service, defaultInterviewPrincipal())
	response := serveInterview(t, router, http.MethodGet, "/api/v2/review/interviews/"+string(fixture.sessionID)+"?workspace_id="+string(fixture.workspaceID), "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, private := range []string{"PRIVATE NOTE ANSWER", "PRIVATE FOLLOWUP", `"answer_points"`, `"follow_up_plan"`, `"user_answer"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("leaked %s", private)
		}
	}
	var wire struct {
		Questions []map[string]json.RawMessage `json:"questions"`
		Session   struct {
			Config struct {
				Scope map[string]json.RawMessage `json:"scope"`
			} `json:"config"`
		} `json:"session"`
		Turns []struct {
			Score struct {
				NoteSource *domain.NoteQuestionSource `json:"note_source"`
			} `json:"score"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire.Questions[0]["claim_id"]) != "null" || string(wire.Questions[0]["source_kind"]) != `"NOTE_REVISION"` || len(wire.Session.Config.Scope) != 1 || !domain.SameNoteSource(wire.Turns[0].Score.NoteSource, source) {
		t.Fatal("note wire union changed")
	}
	var item map[string]any
	if err := json.Unmarshal(wire.Questions[0]["note_item"], &item); err != nil {
		t.Fatal(err)
	}
	if len(item) != 3 || item["sources"] != nil {
		t.Fatal("pre-answer note item exposed original material")
	}
	if err := validateScoreResponse(fixture.turn.Score, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := validateScoreResponse(fixture.turn.Score, id("20")); err == nil {
		t.Fatal("cross workspace note score accepted")
	}
}

func TestNoteOptionsRequireExplicitZeroFollowUpBudget(t *testing.T) {
	role := "backend"
	difficulty := domain.DifficultyIntermediate
	duration, count, followups := 30, 3, 0
	wire := noteOptionsRequest{Role: &role, Difficulty: &difficulty, DurationMinutes: &duration, QuestionCount: &count, MaxFollowUps: &followups}
	if _, err := wire.options(); err != nil {
		t.Fatal(err)
	}
	wire.MaxFollowUps = nil
	if _, err := wire.options(); err == nil {
		t.Fatal("missing required max_follow_ups was accepted")
	}
}
