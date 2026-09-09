package application

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func applicationNoteFixture(t *testing.T) NotePreparation {
	t.Helper()
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	ref := organizingdomain.SynthesisSourceRef{Source: organizingdomain.SynthesisSourceVersion{WorkspaceID: applicationID(1), SourceID: applicationID(20), SourceVersionID: applicationID(21), ContentArtifactID: applicationID(22), ParseProjectionID: applicationID(23), ContentHash: strings.Repeat("a", 64)}, SourceSpanID: applicationID(24), ExcerptHash: strings.Repeat("b", 64), Title: "Original TTL policy"}
	snapshot := organizingdomain.SynthesisNoteSnapshot{WorkspaceID: applicationID(1), NoteID: applicationID(2), RevisionID: applicationID(3), RevisionNo: 1, DocumentID: applicationID(4), ArticleRevisionID: applicationID(5), ArticleRevisionNo: 1, ProjectionHash: strings.Repeat("c", 64), Title: "Cache policy", RendererVersion: organizingdomain.SynthesisRendererVersion, Items: []organizingdomain.SynthesisItem{{ID: applicationID(25), Kind: organizingdomain.SynthesisFactItem, Fact: &organizingdomain.SynthesisStatement{Text: "Entries expire after five minutes.", Applicability: "Default configuration", Sources: []organizingdomain.SynthesisSourceRef{ref}}}}}
	body, err := organizingdomain.RenderSynthesisMarkdown(snapshot.WorkspaceID, snapshot.NoteID, snapshot.Title, snapshot.Items)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ContentHash = NoteOutputHash([]byte(body))
	noteRef, err := domain.NoteRevisionFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	p := NotePreparation{ID: applicationID(6), WorkspaceID: applicationID(1), NoteRevision: noteRef, Snapshot: snapshot, Status: NotePreparationGenerating, WorkflowRunID: applicationID(7), NodeRunID: applicationID(8), NodeAttemptID: applicationID(9), Options: NoteInterviewOptions{Role: "Backend engineer", Difficulty: domain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 2, MaxFollowUps: 2}, Version: 2, IdempotencyKey: "application-note", RequestHash: strings.Repeat("d", 64), CreatedAt: now, UpdatedAt: now}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNoteSessionReusesTurnsConditionalFollowUpsCompletionAndArtifactSources(t *testing.T) {
	p := applicationNoteFixture(t)
	source := domain.NoteQuestionSource{Revision: p.NoteRevision, ItemID: p.Snapshot.Items[0].ID, ItemKind: organizingdomain.SynthesisFactItem, Sources: p.Snapshot.Items[0].SourceReferences()}
	points := []string{p.Snapshot.Items[0].Fact.Text, p.Snapshot.Items[0].Fact.Applicability}
	followups := []domain.NoteFollowUp{{Condition: domain.NoteFollowUpLowCoverage, Prompt: "Clarify expiry.", AnswerPoints: points}, {Condition: domain.NoteFollowUpLowBoundaries, Prompt: "State expiry conditions.", AnswerPoints: points}}
	plan := []NotePlannedQuestion{{Prompt: "Explain default expiry.", AnswerPoints: points, Source: source, FollowUps: followups}, {Prompt: "Which configuration controls expiry?", AnswerPoints: points, Source: source, FollowUps: followups}}
	start, err := BuildNoteInterviewStart(p, plan, p.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{snapshot: Snapshot{Session: start.Session, Questions: start.Questions}}
	service := newTestService(t, store, nil)
	question := start.Questions[0]
	for i := 0; i < 3; i++ {
		result, err := service.SubmitTurn(t.Context(), SubmitTurnCommand{WorkspaceID: p.WorkspaceID, SessionID: start.Session.ID, QuestionID: question.ID, UserAnswer: "", IdempotencyKey: fmt.Sprintf("note-turn-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if !domain.SameNoteSource(result.Turn.Score.NoteSource, &source) || len(result.Turn.Score.Evidence) != 0 {
			t.Fatal("turn lost note source")
		}
		if i < 2 {
			if result.FollowUp == nil || result.FollowUp.FollowUpNo != i+1 || !domain.SameNoteSource(result.FollowUp.NoteSource, &source) {
				t.Fatal("conditional follow-up chain was not retained")
			}
			question = *result.FollowUp
		} else if result.FollowUp != nil {
			t.Fatal("follow-up session budget was exceeded")
		}
	}
	_, err = service.SubmitTurn(t.Context(), SubmitTurnCommand{WorkspaceID: p.WorkspaceID, SessionID: start.Session.ID, QuestionID: start.Questions[1].ID, UserAnswer: "", IdempotencyKey: "note-turn-final"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.Complete(t.Context(), CompleteCommand{WorkspaceID: p.WorkspaceID, SessionID: start.Session.ID, IdempotencyKey: "note-complete"})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Report.Summary.QuestionsTotal != 2 || completed.Report.Summary.AnsweredTotal != 2 || len(completed.Report.NoteSources) != 1 || len(completed.Steps) != 2 {
		t.Fatalf("completion source/chain totals=%+v", completed.Report)
	}
	for _, step := range completed.Steps {
		if step.ClaimID != "" || step.SourceSpanID != "" || !domain.SameNoteSource(step.NoteSource, &source) {
			t.Fatal("path step acquired fabricated Claim provenance")
		}
	}
	for _, request := range store.bridge.requests {
		for _, section := range []ArtifactDraftSection{request.Section} {
			if len(section.DocumentSources) != 1 || section.DocumentSources[0] != p.NoteRevision || len(section.Evidence) != 0 || !strings.Contains(section.Markdown, "source_span_id=") {
				t.Fatal("artifact failed to retain real document and original-source navigation")
			}
		}
	}
	if len(store.candidateWriter.requests) != 0 {
		t.Fatal("completion created unrelated memory candidates")
	}
}

func TestNoteSessionStopsFollowUpWhenPlannedConditionIsSatisfied(t *testing.T) {
	p := applicationNoteFixture(t)
	p.Options.QuestionCount = 1
	source := domain.NoteQuestionSource{Revision: p.NoteRevision, ItemID: p.Snapshot.Items[0].ID, ItemKind: organizingdomain.SynthesisFactItem, Sources: p.Snapshot.Items[0].SourceReferences()}
	points := []string{"Entries expire after five minutes.", "Default configuration"}
	start, err := BuildNoteInterviewStart(p, []NotePlannedQuestion{{Prompt: "Explain default expiry.", AnswerPoints: points, Source: source, FollowUps: []domain.NoteFollowUp{{Condition: domain.NoteFollowUpLowCoverage, Prompt: "Clarify expiry.", AnswerPoints: points}, {Condition: domain.NoteFollowUpLowBoundaries, Prompt: "Explain conditions.", AnswerPoints: points}}}}, p.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{snapshot: Snapshot{Session: start.Session, Questions: start.Questions}}
	service := newTestService(t, store, nil)
	result, err := service.SubmitTurn(t.Context(), SubmitTurnCommand{WorkspaceID: p.WorkspaceID, SessionID: start.Session.ID, QuestionID: start.Questions[0].ID, UserAnswer: strings.Join(points, " "), IdempotencyKey: "covered-note"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FollowUp != nil {
		t.Fatal("satisfied condition should end the chain")
	}
}

type noteSnapshotReader struct {
	snapshot organizingdomain.SynthesisNoteSnapshot
	reads    int
}

func (reader *noteSnapshotReader) ReadPublishedSynthesisNote(context.Context, foundation.ID, foundation.ID) (organizingdomain.SynthesisNoteSnapshot, error) {
	reader.reads++
	return reader.snapshot, nil
}

type notePreparationMemory struct {
	values map[foundation.ID]NotePreparation
	count  int
}

func (s *notePreparationMemory) FindNotePreparationReplay(_ context.Context, workspace foundation.ID, key, hash string) (NotePreparationResult, bool, error) {
	for _, p := range s.values {
		if p.WorkspaceID == workspace && p.IdempotencyKey == key {
			if p.RequestHash != hash {
				return NotePreparationResult{}, false, domain.ConflictError(ErrorCodeNotePreparationConflict, "key conflict")
			}
			return NotePreparationResult{Preparation: p, Replayed: true}, true, nil
		}
	}
	return NotePreparationResult{}, false, nil
}
func (s *notePreparationMemory) CreateNotePreparation(_ context.Context, p NotePreparation) (NotePreparationResult, error) {
	s.count++
	p.WorkflowRunID = applicationID(300 + s.count*2)
	p.NodeRunID = applicationID(301 + s.count*2)
	s.values[p.ID] = p
	return NotePreparationResult{Preparation: p}, nil
}
func (s *notePreparationMemory) GetNotePreparation(_ context.Context, workspace, id foundation.ID) (NotePreparation, error) {
	p, found := s.values[id]
	if !found || p.WorkspaceID != workspace {
		return p, domain.NotFoundError(ErrorCodeNotePreparationNotFound, "missing")
	}
	return p, nil
}
func (s *notePreparationMemory) ListNotePreparations(_ context.Context, workspace, note foundation.ID) ([]NotePreparation, error) {
	result := []NotePreparation{}
	for _, p := range s.values {
		if p.WorkspaceID == workspace && p.NoteRevision.NoteID == note {
			result = append(result, p)
		}
	}
	return result, nil
}

func TestNotePreparationReplayAndExplicitRetryKeepOriginalPublishedSnapshot(t *testing.T) {
	p := applicationNoteFixture(t)
	reader := &noteSnapshotReader{snapshot: p.Snapshot}
	store := &notePreparationMemory{values: map[foundation.ID]NotePreparation{}}
	service, err := NewNotePreparationService(NotePreparationDependencies{Store: store, Snapshots: reader, IDs: &fakeIDs{}, Clock: foundation.FixedClock{Value: p.CreatedAt}})
	if err != nil {
		t.Fatal(err)
	}
	command := PrepareNoteInterviewCommand{WorkspaceID: p.WorkspaceID, NoteID: p.NoteRevision.NoteID, Options: p.Options, IdempotencyKey: "prepare-note"}
	first, err := service.Prepare(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	reader.snapshot = organizingdomain.SynthesisNoteSnapshot{}
	replay, err := service.Prepare(t.Context(), command)
	if err != nil || !replay.Replayed || replay.Preparation.ID != first.Preparation.ID || reader.reads != 1 {
		t.Fatalf("prepare response loss replay: %v", err)
	}
	old := first.Preparation
	old.Status = NotePreparationFailed
	old.Failure = &NotePreparationFailure{Code: ErrorCodeNotePlanInvalid, Retryable: true}
	old.Version++
	store.values[old.ID] = old
	retryCommand := RetryNoteInterviewCommand{WorkspaceID: p.WorkspaceID, NoteID: p.NoteRevision.NoteID, PreparationID: old.ID, Options: p.Options, IdempotencyKey: "retry-note"}
	retry, err := service.Retry(t.Context(), retryCommand)
	if err != nil || retry.Preparation.ID == old.ID || !reflect.DeepEqual(retry.Preparation.Snapshot, p.Snapshot) || reader.reads != 1 {
		t.Fatalf("retry changed material: %v", err)
	}
	retryCommand.IdempotencyKey = "retry-unknown"
	old.Status = NotePreparationRecoveryRequired
	old.Failure.Retryable = false
	store.values[old.ID] = old
	if _, err := service.Retry(t.Context(), retryCommand); err == nil {
		t.Fatal("unknown result accepted another paid retry")
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Entries expire") || strings.Contains(string(raw), `"snapshot"`) {
		t.Fatal("preparation exposed frozen answers")
	}
}
