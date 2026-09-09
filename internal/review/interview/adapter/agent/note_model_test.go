package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const noteTestPlan = `{"questions":[{"item_label":"I001","prompt":"When do entries expire?","answer_point_labels":["P001","P002"],"follow_ups":[{"condition":"LOW_COVERAGE","prompt":"Explain the default expiry boundary.","answer_point_labels":["P001","P002"]}]},{"item_label":"I002","prompt":"How do the two duration policies differ by condition?","answer_point_labels":["P001","P002","P003","P004"],"follow_ups":[{"condition":"LOW_BOUNDARIES","prompt":"Explain both policy conditions.","answer_point_labels":["P001","P002","P003","P004"]}]},{"item_label":"I003","prompt":"Can the available material determine memory capacity?","answer_point_labels":["P001"],"follow_ups":[{"condition":"LOW_CORRECTNESS","prompt":"What evidence is missing for capacity?","answer_point_labels":["P001"]}]}]}`

var noteTestTime = time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
var noteTestModel = agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "note-fixture", ModelVersion: "v1"}

func noteTestID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("79000000-0000-4000-8000-%012d", n))
}

type noteTestIDs struct{ n atomic.Int64 }

func (ids *noteTestIDs) New() (foundation.ID, error) {
	return noteTestID(1000 + int(ids.n.Add(1))), nil
}

// The in-memory snapshot is an explicit unit-test seam, not publication proof.
func noteFixture(t *testing.T) interviewapp.NotePreparation {
	t.Helper()
	source := organizingdomain.SynthesisSourceRef{Source: organizingdomain.SynthesisSourceVersion{WorkspaceID: noteTestID(1), SourceID: noteTestID(11), SourceVersionID: noteTestID(12), ContentArtifactID: noteTestID(13), ParseProjectionID: noteTestID(14), ContentHash: strings.Repeat("a", 64)}, SourceSpanID: noteTestID(15), ExcerptHash: strings.Repeat("b", 64), Title: "Original material"}
	items := []organizingdomain.SynthesisItem{
		{ID: noteTestID(21), Kind: organizingdomain.SynthesisFactItem, Fact: &organizingdomain.SynthesisStatement{Text: "Entries expire after five minutes.", Applicability: "Default configuration", Sources: []organizingdomain.SynthesisSourceRef{source}}},
		{ID: noteTestID(22), Kind: organizingdomain.SynthesisConflictItem, Conflict: &organizingdomain.SynthesisConflictContent{Subject: "Cache lifetime", Alternatives: []organizingdomain.SynthesisStatement{{Text: "Use five minutes.", Applicability: "Stable values", Sources: []organizingdomain.SynthesisSourceRef{source}}, {Text: "Use one minute.", Applicability: "Changing values", Sources: []organizingdomain.SynthesisSourceRef{source}}}}},
		{ID: noteTestID(23), Kind: organizingdomain.SynthesisGapItem, Gap: &organizingdomain.SynthesisGapContent{Question: "How is memory bounded?", Context: "", Sources: []organizingdomain.SynthesisSourceRef{}}},
	}
	snapshot := organizingdomain.SynthesisNoteSnapshot{WorkspaceID: noteTestID(1), NoteID: noteTestID(2), RevisionID: noteTestID(3), RevisionNo: 1, DocumentID: noteTestID(4), ArticleRevisionID: noteTestID(5), ArticleRevisionNo: 1, ProjectionHash: strings.Repeat("c", 64), Title: "Cache lifetime", RendererVersion: organizingdomain.SynthesisRendererVersion, Items: items}
	body, err := organizingdomain.RenderSynthesisMarkdown(snapshot.WorkspaceID, snapshot.NoteID, snapshot.Title, items)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ContentHash = interviewapp.NoteOutputHash([]byte(body))
	ref, err := domain.NoteRevisionFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	p := interviewapp.NotePreparation{ID: noteTestID(6), WorkspaceID: snapshot.WorkspaceID, NoteRevision: ref, Snapshot: snapshot, Options: interviewapp.NoteInterviewOptions{Role: "Backend engineer", Difficulty: domain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 3, MaxFollowUps: 3}, Status: interviewapp.NotePreparationGenerating, WorkflowRunID: noteTestID(7), NodeRunID: noteTestID(8), NodeAttemptID: noteTestID(9), Version: 2, IdempotencyKey: "note-fixture", RequestHash: strings.Repeat("d", 64), CreatedAt: noteTestTime, UpdatedAt: noteTestTime}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}
func noteExecution(p interviewapp.NotePreparation) workflowapp.ExecutionContext {
	return workflowapp.ExecutionContext{WorkspaceID: p.WorkspaceID, RunID: p.WorkflowRunID, NodeRunID: p.NodeRunID, NodeAttemptID: p.NodeAttemptID}
}
func noteHarness(t *testing.T, outputs ...string) (*NoteInterviewModel, *noteMemoryStore, *agentapp.DeterministicChatModel) {
	t.Helper()
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterNoteInterviewRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "note-test", Version: "v1"}, Model: noteTestModel, Timeout: time.Second, MaxOutputTokens: 4096}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]agentapp.DeterministicChatStep, len(outputs))
	for i, output := range outputs {
		steps[i] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: noteTestModel, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}}
	}
	provider := agentapp.NewDeterministicChatModel(steps...)
	store := &noteMemoryStore{p: noteFixture(t), runs: map[foundation.ID]agentdomain.ModelRun{}, calls: map[foundation.ID][]agentdomain.ModelCall{}}
	model, err := NewNoteInterviewModel(NoteInterviewModelDependencies{Model: provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: store, Store: store, ProfileRef: profile.Ref, IDs: &noteTestIDs{}, Clock: foundation.FixedClock{Value: noteTestTime}})
	if err != nil {
		t.Fatal(err)
	}
	return model, store, provider
}

func TestNoteInterviewUsesRecordedEinoPlanAndFreezesEachSourceKind(t *testing.T) {
	model, store, provider := noteHarness(t, `{"questions":[],"extra":true}`, " \n"+noteTestPlan+"\n")
	ready, err := model.GenerateNoteInterview(t.Context(), noteExecution(store.p), store.p)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != interviewapp.NotePreparationReady || ready.SessionID == nil || provider.CallCount() != 2 || len(store.start.Questions) != 3 {
		t.Fatalf("ready=%s calls=%d", ready.Status, provider.CallCount())
	}
	record, err := store.GetModelRun(t.Context(), ready.WorkspaceID, ready.ModelRunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.FinalResultType != agentdomain.ResultTypeSynthesisNoteInterviewPlan || len(record.Calls) != 2 || record.Calls[1].Phase != agentdomain.ModelCallRepair {
		t.Fatalf("model journal=%+v", record)
	}
	if err := interviewapp.ValidateNoteModelOutput(ready, record, ready.ModelOutput); err != nil {
		t.Fatal(err)
	}
	if record.Run.Retrieval.IsBound() || record.Run.MemoryContext.IsBound() {
		t.Fatal("note run acquired fabricated Claim/Index memory")
	}
	conflict := store.start.Questions[1]
	if len(conflict.AnswerPoints) != 4 || len(conflict.NoteSource.Sources) != 2 || conflict.ClaimID != "" || len(conflict.Evidence) != 0 {
		t.Fatal("conflict lost an alternative, condition or exact repeated source")
	}
	for _, request := range provider.Calls() {
		for _, message := range request.Messages {
			for _, secret := range []string{string(ready.ID), string(ready.NoteRevision.NoteID), string(ready.Snapshot.Items[0].Fact.Sources[0].SourceSpanID), ready.NoteRevision.ContentHash, ready.RequestHash} {
				if strings.Contains(message.Content, secret) {
					t.Fatal("Provider received a trusted identity or hash")
				}
			}
		}
	}
	raw, err := json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"answer_points", "model_output", "snapshot", `"follow_ups"`, "Entries expire"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("preparation response leaked model answers")
		}
	}
}

func TestNotePlanRejectsIncompleteOrForgedLabelsAndRetainsGapUncertainty(t *testing.T) {
	p := noteFixture(t)
	bad := []string{strings.Replace(noteTestPlan, `"P003","P004"`, `"P003"`, 1), strings.Replace(noteTestPlan, `"I002"`, `"I999"`, 1), strings.Replace(noteTestPlan, `"I003"`, `"I001"`, 1), strings.Replace(noteTestPlan, `"item_label":"I001"`, `"item_label":"I001","item_label":"I001"`, 1), strings.Replace(noteTestPlan, `"follow_ups":[`, `"authority":true,"follow_ups":[`, 1), strings.Replace(noteTestPlan, `"condition":"LOW_COVERAGE",`, "", 1), noteTestPlan + `{}`}
	for i, raw := range bad {
		if _, err := interviewapp.DecodeNotePlan([]byte(raw), p); err == nil {
			t.Fatalf("invalid plan %d accepted", i)
		}
	}
	plan, err := interviewapp.DecodeNotePlan([]byte(noteTestPlan), p)
	if err != nil {
		t.Fatal(err)
	}
	start, err := interviewapp.BuildNoteInterviewStart(p, plan, noteTestTime)
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"不足以判断，需要补充资料。", "The evidence is insufficient; cannot determine capacity.", "证据不足"} {
		score, err := (interviewapp.DeterministicScorer{}).Score(t.Context(), interviewapp.ScoreInput{Question: start.Questions[2], UserAnswer: answer})
		if err != nil {
			t.Fatal(err)
		}
		if score.Correctness.Value != 1 || score.Boundaries.Value != 1 || score.Coverage.Value != 1 || !strings.Contains(score.Correctness.Rationale, "no model grading") {
			t.Fatalf("gap answer %q score=%+v", answer, score)
		}
	}
	score, err := (interviewapp.DeterministicScorer{}).Score(t.Context(), interviewapp.ScoreInput{Question: start.Questions[2], UserAnswer: "The capacity is 100 entries."})
	if err != nil {
		t.Fatal(err)
	}
	if score.Correctness.Value >= 0.65 {
		t.Fatal("unfounded capacity answer was accepted")
	}
	before := start.Questions[0].AnswerPoints[0]
	p.Snapshot.Items[0].Fact.Text = "Changed after start"
	if start.Questions[0].AnswerPoints[0] != before {
		t.Fatal("session material was not frozen")
	}
}

func TestNotePlanBoundedAnswerPointsPreserveMaximumLengthConditions(t *testing.T) {
	p := noteFixture(t)
	p.Snapshot.Items[0].Fact.Text = strings.Repeat("a", 4096)
	p.Snapshot.Items[0].Fact.Applicability = strings.Repeat("b", 2048)
	body, err := organizingdomain.RenderSynthesisMarkdown(p.WorkspaceID, p.NoteRevision.NoteID, p.Snapshot.Title, p.Snapshot.Items)
	if err != nil {
		t.Fatal(err)
	}
	p.Snapshot.ContentHash = interviewapp.NoteOutputHash([]byte(body))
	p.NoteRevision.ContentHash = p.Snapshot.ContentHash
	plan, err := interviewapp.DecodeNotePlan([]byte(noteTestPlan), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := interviewapp.BuildNoteInterviewStart(p, plan, noteTestTime); err != nil {
		t.Fatal(err)
	}
}

func TestNoteModelUnknownResultDoesNotRepeatProviderCall(t *testing.T) {
	for _, lostCommit := range []bool{false, true} {
		t.Run(fmt.Sprint(lostCommit), func(t *testing.T) {
			model, store, provider := noteHarness(t, noteTestPlan)
			store.dropCommit = !lostCommit
			store.loseCommit = lostCommit
			p := store.p
			ready, err := model.GenerateNoteInterview(t.Context(), noteExecution(p), p)
			if lostCommit {
				if err != nil || ready.Status != interviewapp.NotePreparationReady {
					t.Fatalf("lost commit recovery: %v", err)
				}
			} else {
				var classified *foundation.Error
				if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired {
					t.Fatalf("unknown result: %v", err)
				}
				_, err = model.GenerateNoteInterview(t.Context(), noteExecution(p), store.p)
				if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired {
					t.Fatalf("unsafe replay: %v", err)
				}
			}
			if provider.CallCount() != 1 {
				t.Fatalf("duplicate paid calls=%d", provider.CallCount())
			}
		})
	}
}

func TestNoteModelStopsAfterThreeInvalidResponsesAndHonorsInputBudget(t *testing.T) {
	model, store, provider := noteHarness(t, `{}`, `{}`, `{}`)
	_, err := model.GenerateNoteInterview(t.Context(), noteExecution(store.p), store.p)
	if err == nil || provider.CallCount() != 3 {
		t.Fatalf("validation error=%v calls=%d", err, provider.CallCount())
	}
	model, store, provider = noteHarness(t, noteTestPlan)
	model.dependencies.Budget.MaxRequestBytes = 1
	_, err = model.GenerateNoteInterview(t.Context(), noteExecution(store.p), store.p)
	if err == nil || provider.CallCount() != 0 {
		t.Fatalf("budget error=%v calls=%d", err, provider.CallCount())
	}
}

// Only this store is simulated; the Eino scheduler, structured validation,
// Provider boundary and ModelRun/Call recording are the production components.
type noteMemoryStore struct {
	agentapp.ModelRunRepository
	interviewapp.NoteGenerationStore
	mu                     sync.Mutex
	p                      interviewapp.NotePreparation
	runs                   map[foundation.ID]agentdomain.ModelRun
	calls                  map[foundation.ID][]agentdomain.ModelCall
	start                  interviewapp.StartRecord
	loseCommit, dropCommit bool
}

func (s *noteMemoryStore) CreateModelRun(_ context.Context, run agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return run, false, err
	}
	for _, old := range s.runs {
		if old.NodeAttemptID == run.NodeAttemptID {
			return old, true, nil
		}
	}
	s.runs[run.ID] = run
	return run, false, nil
}
func (s *noteMemoryStore) GetModelRun(_ context.Context, workspace, id foundation.ID) (agentapp.ModelRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, found := s.runs[id]
	if !found || run.WorkspaceID != workspace {
		return agentapp.ModelRunRecord{}, errors.New("model run not found")
	}
	return agentapp.ModelRunRecord{Run: run, Calls: append([]agentdomain.ModelCall(nil), s.calls[id]...)}, nil
}
func (s *noteMemoryStore) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := agentdomain.ValidateModelCall(call); err != nil {
		return call, false, err
	}
	for _, old := range s.calls[call.ModelRunID] {
		if old.CallNo == call.CallNo {
			return old, true, nil
		}
	}
	s.calls[call.ModelRunID] = append(s.calls[call.ModelRunID], call)
	return call, false, nil
}
func (s *noteMemoryStore) CompleteModelCall(_ context.Context, c agentapp.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := agentdomain.ValidateModelCall(c.Call); err != nil {
		return c.Call, false, err
	}
	for i, old := range s.calls[c.Call.ModelRunID] {
		if old.ID == c.Call.ID && old.Version == c.ExpectedVersion {
			s.calls[c.Call.ModelRunID][i] = c.Call
			return c.Call, false, nil
		}
	}
	return c.Call, false, errors.New("call CAS conflict")
}
func (s *noteMemoryStore) BindNoteModelRun(_ context.Context, p interviewapp.NotePreparation, run agentdomain.ModelRun) (interviewapp.NotePreparation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Version != s.p.Version || p.ID != s.p.ID {
		return s.p, errors.New("binding conflict")
	}
	s.p.ModelRunID = run.ID
	s.p.Version++
	return s.p, nil
}
func (s *noteMemoryStore) CompleteNoteGeneration(_ context.Context, c interviewapp.NoteGenerationCompletion) (interviewapp.NotePreparation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropCommit {
		return s.p, errors.New("lost completion result")
	}
	run := s.runs[c.ModelRun.ID]
	if err := interviewapp.ValidateNoteModelOutput(s.p, agentapp.ModelRunRecord{Run: run, Calls: s.calls[run.ID]}, c.Output); err != nil {
		return s.p, err
	}
	start, err := interviewapp.BuildNoteInterviewStart(s.p, c.Plan, c.At)
	if err != nil || !reflect.DeepEqual(start, c.Start) {
		return s.p, errors.New("completion plan differs")
	}
	run.Status = agentdomain.ModelRunSucceeded
	run.FinalResultType = agentdomain.ResultTypeSynthesisNoteInterviewPlan
	run.Version++
	run.CompletedAt = &c.At
	run.UpdatedAt = c.At
	s.runs[run.ID] = run
	s.start = start
	s.p.Status = interviewapp.NotePreparationReady
	s.p.SessionID = &start.Session.ID
	s.p.ModelOutput = append(json.RawMessage(nil), c.Output...)
	s.p.Version++
	if s.loseCommit {
		return s.p, errors.New("commit response lost")
	}
	return s.p, nil
}
func (s *noteMemoryStore) GetNotePreparation(_ context.Context, workspace, id foundation.ID) (interviewapp.NotePreparation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p.WorkspaceID != workspace || s.p.ID != id {
		return interviewapp.NotePreparation{}, errors.New("preparation missing")
	}
	return s.p, nil
}
