//go:build integration

package postgres

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
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactauthoring "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/authoring"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	interviewagent "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/agent"
	interviewartifact "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/artifact"
	interviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/postgres"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	interviewworkflow "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const synthesisInterviewPlan = `{"questions":[{"item_label":"I001","prompt":"Explain the scheduler's responsibility.","answer_point_labels":["P001"],"follow_ups":[{"condition":"LOW_COVERAGE","prompt":"Describe runnable work.","answer_point_labels":["P001"]},{"condition":"LOW_BOUNDARIES","prompt":"Clarify the scheduler's responsibility.","answer_point_labels":["P001"]}]},{"item_label":"I002","prompt":"How does fairness depend on workload?","answer_point_labels":["P001","P002","P003","P004"],"follow_ups":[{"condition":"LOW_BOUNDARIES","prompt":"Explain both workload conditions.","answer_point_labels":["P001","P002","P003","P004"]}]},{"item_label":"I003","prompt":"Can the material determine the version-specific limit?","answer_point_labels":["P001"],"follow_ups":[{"condition":"LOW_CORRECTNESS","prompt":"What additional evidence is needed to determine the limit?","answer_point_labels":["P001"]}]}]}`

// Source inputs and the deterministic Provider are explicit test seams. The
// publication, approval, Git writeback, Eino execution, model journal, River
// worker, Interview session/turns and Artifact revisions use real owners.
func TestSynthesisNoteInterviewPostgreSQLPublishedLifecycle(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 97)
	runtime := newSynthesisInterviewRuntime(t, f, `{"questions":[],"unexpected":true}`, synthesisInterviewPlan,
		synthesisInterviewPlan, `{}`, `{}`, `{}`, synthesisInterviewPlan, synthesisInterviewPlan)
	ctx := t.Context()
	first := f.generation(t, 58000, nil, "The scheduler manages runnable work. Fairness depends on workload. A version-specific limit remains unknown.")
	first.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(first, "scheduling interview")}
	ref := first.Input.Sources[0].Reference
	first.Generation.Notes[0].Delta.Operations = append(first.Generation.Notes[0].Delta.Operations,
		domain.SynthesisOperation{Kind: domain.SynthesisAddConflict, Item: &domain.SynthesisItem{ID: organizingIntegrationID(58030), Kind: domain.SynthesisConflictItem, Conflict: &domain.SynthesisConflictContent{Subject: "Scheduling fairness", Alternatives: []domain.SynthesisStatement{
			{Text: "Runnable tasks receive execution time.", Applicability: "Ordinary workload", Sources: []domain.SynthesisSourceRef{ref}},
			{Text: "Execution time can be delayed.", Applicability: "Saturated workload", Sources: []domain.SynthesisSourceRef{ref}},
		}}}},
		domain.SynthesisOperation{Kind: domain.SynthesisAddGap, Item: &domain.SynthesisItem{ID: organizingIntegrationID(58031), Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "Which runtime version changes the limit?", Context: "The source leaves the version-specific limit open.", Sources: []domain.SynthesisSourceRef{ref}}}},
	)
	created, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil || len(created.Publications) != 1 {
		t.Fatalf("create interview material: %+v %v", created, err)
	}
	noteID := created.Publications[0].NoteID
	command := interviewapp.PrepareNoteInterviewCommand{WorkspaceID: f.workspace, NoteID: noteID,
		Options: interviewapp.NoteInterviewOptions{Role: "Backend engineer", Difficulty: interviewdomain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 3, MaxFollowUps: 2}, IdempotencyKey: "published-note-interview"}
	if _, err := runtime.preparation.Prepare(ctx, command); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_NOTE_NOT_PUBLISHED") {
		t.Fatalf("unapproved material became an interview: %v", err)
	}
	f.count(t, "learning.note_interview_preparation", "workspace_id", string(f.workspace), 0)
	detail, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	prepared := f.preparePublication(t, detail)
	if _, err := f.node.Execute(ctx, prepared.input, prepared.identity); err != nil {
		t.Fatalf("publish interview material through real writeback: %v", err)
	}
	published, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}

	// Two API requests share one preparation and one paid model chain. Losing
	// the successful transaction response must still recover that same session.
	runtime.modelStore.afterCommit.Store(true)
	var results [2]interviewapp.NotePreparationResult
	var failures [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], failures[i] = runtime.preparation.Prepare(ctx, command)
		}()
	}
	close(start)
	wg.Wait()
	if failures[0] != nil || failures[1] != nil || results[0].Preparation.ID != results[1].Preparation.ID || results[0].Replayed == results[1].Replayed {
		t.Fatalf("concurrent prepare failed exact replay: failures=%v replay=%v/%v", failures, results[0].Replayed, results[1].Replayed)
	}
	ready := runtime.wait(t, noteID, results[0].Preparation.ID, interviewapp.NotePreparationReady)
	if ready.SessionID == nil || !reflect.DeepEqual(ready.Snapshot, published) || runtime.provider.CallCount() != 2 || !runtime.modelStore.responseLost.Load() {
		t.Fatalf("ready preparation lost its source or repeated calls: status=%s calls=%d", ready.Status, runtime.provider.CallCount())
	}
	record, err := runtime.modelRuns.GetModelRun(ctx, f.workspace, ready.ModelRunID)
	if err != nil || len(record.Calls) != 2 || record.Calls[0].Phase != agentdomain.ModelCallInitial || record.Calls[1].Phase != agentdomain.ModelCallRepair || record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.Retrieval.IsBound() {
		t.Fatalf("recorded Eino model chain: %+v %v", record, err)
	}
	if err := interviewapp.ValidateNoteModelOutput(ready, record, ready.ModelOutput); err != nil {
		t.Fatal(err)
	}
	runtime.redeliver(t, ready)
	if runtime.provider.CallCount() != 2 {
		t.Fatal("terminal River redelivery called the Provider again")
	}
	f.count(t, "learning.note_interview_preparation", "workspace_id", string(f.workspace), 1)
	f.count(t, "learning.interview_session", "workspace_id", string(f.workspace), 1)
	f.count(t, "core.claim", "workspace_id", string(f.workspace), 0)
	f.count(t, "retrieval.index_version", "workspace_id", string(f.workspace), 1) // Source fixture only.
	snapshot, err := runtime.interview.Get(ctx, f.workspace, *ready.SessionID)
	if err != nil || len(snapshot.Questions) != 3 || snapshot.Session.Config.Scope.NoteRevision == nil {
		t.Fatalf("reload generated interview: %+v %v", snapshot, err)
	}
	for i, question := range snapshot.Questions {
		expected := interviewdomain.NoteQuestionSource{Revision: ready.NoteRevision, ItemID: published.Items[i].ID, ItemKind: published.Items[i].Kind, Sources: published.Items[i].SourceReferences()}
		if question.SourceKind != interviewdomain.QuestionSourceNoteRevision || question.ClaimID != "" || len(question.Evidence) != 0 || !interviewdomain.SameNoteSource(question.NoteSource, &expected) {
			t.Fatalf("question %d lost immutable original sources", i)
		}
	}
	if len(snapshot.Questions[1].AnswerPoints) != 4 || len(snapshot.Questions[1].NoteSource.Sources) != 2 {
		t.Fatal("conflict dropped an alternative, condition or repeated raw source")
	}
	if _, err := runtime.preparation.Get(ctx, f.otherWorkspace, noteID, ready.ID); !organizingIntegrationError(err, foundation.ErrorNotFound, interviewapp.ErrorCodeNotePreparationNotFound) {
		t.Fatalf("cross-workspace preparation lookup: %v", err)
	}
	if _, err := runtime.interview.Get(ctx, f.otherWorkspace, *ready.SessionID); err == nil {
		t.Fatal("cross-workspace interview lookup succeeded")
	}
	if err := f.db.Exec(`UPDATE learning.interview_question SET note_source=jsonb_set(note_source,'{item_id}',to_jsonb(?::text)) WHERE id=?`, string(published.Items[1].ID), string(snapshot.Questions[0].ID)).Error; platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("question original source could mutate: %v", err)
	}

	question := snapshot.Questions[0]
	for i, answer := range []string{"", "", "The scheduler manages runnable work."} {
		turnCommand := interviewapp.SubmitTurnCommand{WorkspaceID: f.workspace, SessionID: *ready.SessionID, QuestionID: question.ID, UserAnswer: answer, IdempotencyKey: fmt.Sprintf("note-turn-%d", i)}
		turn, err := runtime.interview.SubmitTurn(ctx, turnCommand)
		if err != nil || !interviewdomain.SameNoteSource(turn.Turn.Score.NoteSource, question.NoteSource) {
			t.Fatalf("answer source/turn %d: %v", i, err)
		}
		if replay, err := runtime.interview.SubmitTurn(ctx, turnCommand); err != nil || !replay.Replayed || replay.Turn.ID != turn.Turn.ID {
			t.Fatalf("answer exact replay: %v", err)
		}
		if i < 2 {
			if turn.FollowUp == nil || turn.FollowUp.FollowUpNo != i+1 || turn.FollowUp.ParentQuestionID == nil || *turn.FollowUp.ParentQuestionID != question.ID {
				t.Fatalf("conditional follow-up %d did not extend its chain", i)
			}
			question = *turn.FollowUp
		} else if turn.FollowUp != nil || turn.NextQuestion == nil || turn.NextQuestion.ID != snapshot.Questions[1].ID {
			t.Fatal("completed question chain did not advance")
		}
	}
	conflict, err := runtime.interview.SubmitTurn(ctx, interviewapp.SubmitTurnCommand{WorkspaceID: f.workspace, SessionID: *ready.SessionID, QuestionID: snapshot.Questions[1].ID, UserAnswer: "", IdempotencyKey: "note-conflict-answer"})
	if err != nil || conflict.FollowUp != nil {
		t.Fatalf("session-wide follow-up budget was not retained: %v", err)
	}
	gap, err := runtime.interview.SubmitTurn(ctx, interviewapp.SubmitTurnCommand{WorkspaceID: f.workspace, SessionID: *ready.SessionID, QuestionID: snapshot.Questions[2].ID, UserAnswer: "不足以判断，需要补充资料。", IdempotencyKey: "note-gap-answer"})
	if err != nil || gap.Turn.Score.Correctness.Value != 1 || gap.Turn.Score.Boundaries.Value != 1 {
		t.Fatalf("unresolved gap punished insufficient evidence: %+v %v", gap.Turn.Score, err)
	}
	completionCommand := interviewapp.CompleteCommand{WorkspaceID: f.workspace, SessionID: *ready.SessionID, IdempotencyKey: "complete-note-interview"}
	completed, err := runtime.interview.Complete(ctx, completionCommand)
	if err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("note completion cause: %T %v", cause, cause)
		}
		t.Fatal("persisted note report/path failed")
	}
	if completed.Report.Summary.QuestionsTotal != 3 || completed.Report.Summary.AnsweredTotal != 3 || len(completed.Report.NoteSources) != 3 || len(completed.Steps) == 0 {
		t.Fatalf("persisted note report/path totals: %+v", completed.Report.Summary)
	}
	if replay, err := runtime.interview.Complete(ctx, completionCommand); err != nil || !replay.Replayed || replay.Report.ID != completed.Report.ID || replay.Path.ID != completed.Path.ID {
		t.Fatalf("completion exact replay changed reports: %v", err)
	}
	reloaded, err := runtime.interview.Get(ctx, f.workspace, *ready.SessionID)
	if err != nil || len(reloaded.Questions) != 5 || len(reloaded.Turns) != 5 || reloaded.Report == nil || !reflect.DeepEqual(*reloaded.Report, completed.Report) || !reflect.DeepEqual(reloaded.Steps, completed.Steps) {
		t.Fatalf("refresh lost answers, report or learning source: %v", err)
	}
	for _, step := range completed.Steps {
		if step.SourceKind != interviewdomain.QuestionSourceNoteRevision || step.ClaimID != "" || step.SourceVersionID != "" || step.SourceSpanID != "" || step.EvidenceHash != "" || step.NoteSource == nil || step.NoteSource.Revision != ready.NoteRevision {
			t.Fatal("learning path substituted Claim evidence for note material")
		}
	}
	for _, binding := range []interviewdomain.ArtifactBinding{completed.Report.Artifact, completed.Path.Artifact} {
		state, err := runtime.artifacts.Get(ctx, f.workspace, binding.ArtifactID)
		if err != nil || state.Revision.ID != binding.RevisionID || len(state.Revision.Sections) != 1 {
			t.Fatalf("real Artifact revision missing: %v", err)
		}
		section := state.Revision.Sections[0]
		if len(section.Citations) != 0 || len(section.DocumentSources) != 1 || !section.DocumentSources[0].Verified || section.DocumentSources[0].ArticleRevisionID != ready.NoteRevision.ArticleRevisionID || section.DocumentSources[0].VerifiedContentHash != ready.NoteRevision.ContentHash || !strings.Contains(section.Content, "source_span_id=") {
			t.Fatal("Artifact lost verified document source or original-source navigation")
		}
	}
	for _, noteSource := range completed.Report.NoteSources {
		for _, source := range noteSource.Sources {
			view, err := f.service.OpenSource(ctx, f.workspace, noteID, published.RevisionID, source)
			if err != nil || view.Availability != domain.MaterialAvailable || view.Text != first.Input.Sources[0].Text {
				t.Fatalf("report source cannot be opened through its owner: %v", err)
			}
		}
	}

	// An explicit retry keeps its original published material even after a new
	// generated revision has been approved and committed to the same note.
	runtime.model.unavailable.Store(true)
	command.IdempotencyKey = "unavailable-note-interview"
	unavailable, err := runtime.preparation.Prepare(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	failed := runtime.wait(t, noteID, unavailable.Preparation.ID, interviewapp.NotePreparationUnavailable)
	if !failed.RetryAllowed() || runtime.provider.CallCount() != 2 {
		t.Fatal("unavailable model called a Provider or lost explicit retry")
	}
	latest, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	second := f.generation(t, 59000, []organizingapp.SynthesisGenerationNote{{Note: latest.Note, Revision: *latest.CurrentRevision}}, "Further support for the scheduler's responsibility.")
	second.Generation.Notes = []organizingapp.SynthesisGeneratedNote{supportedSynthesisNote(latest, second.Input.Sources[0].Reference)}
	if _, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation); err != nil {
		t.Fatal(err)
	}
	latest, err = f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	nextPublication := f.preparePublication(t, latest)
	if _, err := f.node.Execute(ctx, nextPublication.input, nextPublication.identity); err != nil {
		t.Fatal(err)
	}
	if newer, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); err != nil || newer.RevisionID == ready.NoteRevision.RevisionID {
		t.Fatalf("new note revision was not really published: %v", err)
	}
	runtime.model.unavailable.Store(false)
	retryCommand := interviewapp.RetryNoteInterviewCommand{WorkspaceID: f.workspace, NoteID: noteID, PreparationID: failed.ID, Options: command.Options, IdempotencyKey: "retry-unavailable-note"}
	retried, err := runtime.preparation.Retry(ctx, retryCommand)
	if err != nil {
		t.Fatal(err)
	}
	retryReady := runtime.wait(t, noteID, retried.Preparation.ID, interviewapp.NotePreparationReady)
	if !reflect.DeepEqual(retryReady.Snapshot, published) || retryReady.RetryOf == nil || *retryReady.RetryOf != failed.ID || runtime.provider.CallCount() != 3 {
		t.Fatal("retry substituted the latest note or repeated a paid call")
	}
	if replay, err := runtime.preparation.Retry(ctx, retryCommand); err != nil || !replay.Replayed || replay.Preparation.ID != retryReady.ID {
		t.Fatalf("retry exact replay: %v", err)
	}
	command.IdempotencyKey = "published-note-interview"
	if replay, err := runtime.preparation.Prepare(ctx, command); err != nil || !replay.Replayed || replay.Preparation.ID != ready.ID || replay.Preparation.NoteRevision != ready.NoteRevision {
		t.Fatalf("historical prepare replay changed published material: %v", err)
	}
	retryCommand.IdempotencyKey = "duplicate-explicit-retry"
	if _, err := runtime.preparation.Retry(ctx, retryCommand); err == nil {
		t.Fatal("same failed preparation created a second explicit retry")
	}

	command.IdempotencyKey = "invalid-note-plan"
	invalid, err := runtime.preparation.Prepare(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	invalidResult := runtime.wait(t, noteID, invalid.Preparation.ID, interviewapp.NotePreparationFailed)
	if !invalidResult.RetryAllowed() || invalidResult.SessionID != nil || runtime.provider.CallCount() != 6 {
		t.Fatal("invalid plans exceeded their three-call budget or created a session")
	}
	if calls, err := runtime.modelRuns.GetModelRun(ctx, f.workspace, invalidResult.ModelRunID); err != nil || len(calls.Calls) != 3 || calls.Run.Status != agentdomain.ModelRunFailed {
		t.Fatalf("invalid-plan model journal is not terminal: %+v %v", calls, err)
	}
	retryCommand.PreparationID, retryCommand.IdempotencyKey = invalidResult.ID, "retry-invalid-note-plan"
	validRetry, err := runtime.preparation.Retry(ctx, retryCommand)
	if err != nil {
		t.Fatal(err)
	}
	runtime.wait(t, noteID, validRetry.Preparation.ID, interviewapp.NotePreparationReady)
	if runtime.provider.CallCount() != 7 {
		t.Fatal("explicit invalid-plan retry repeated Provider calls")
	}

	// A successful Provider response without a known completion outcome is a
	// visible recovery state; it must never buy another call automatically.
	runtime.modelStore.beforeCommit.Store(true)
	command.IdempotencyKey = "uncertain-note-completion"
	uncertain, err := runtime.preparation.Prepare(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	recovery := runtime.wait(t, noteID, uncertain.Preparation.ID, interviewapp.NotePreparationRecoveryRequired)
	runtime.redeliver(t, recovery)
	if recovery.RetryAllowed() || recovery.SessionID != nil || runtime.provider.CallCount() != 8 {
		t.Fatal("unknown completion allowed automatic paid work")
	}
	retryCommand.PreparationID, retryCommand.IdempotencyKey = recovery.ID, "forbidden-unknown-retry"
	if _, err := runtime.preparation.Retry(ctx, retryCommand); err == nil {
		t.Fatal("unknown completion permitted explicit paid retry")
	}
	if list, err := runtime.preparation.List(ctx, f.workspace, noteID); err != nil || len(list) != 6 {
		t.Fatalf("preparation refresh history: count=%d err=%v", len(list), err)
	}
}

type synthesisInterviewRuntime struct {
	fixture     *synthesisGitFixture
	preparation *interviewapp.NotePreparationService
	interview   *interviewapp.Service
	artifacts   *artifactpostgres.GORMRepository
	modelRuns   *agentpostgres.GORMRepository
	provider    *agentapp.DeterministicChatModel
	model       *synthesisInterviewSwitchModel
	modelStore  *synthesisInterviewModelStore
	worker      *riveradapter.RuntimeNodeWorker
}

func newSynthesisInterviewRuntime(t *testing.T, fixture *synthesisGitFixture, outputs ...string) *synthesisInterviewRuntime {
	t.Helper()
	f := &synthesisInterviewRuntime{fixture: fixture}
	ids, clock := foundation.UUIDGenerator{}, foundation.SystemClock{}
	var err error
	migrator, err := riveradapter.NewMigrator(fixture.platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.modelRuns, err = agentpostgres.NewGORMRepository(fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := workflowpostgres.NewGORMToolExecutionPolicySnapshot(fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	store, err := interviewpostgres.NewGORMNotePreparationRepository(fixture.platform, interviewpostgres.NotePreparationDependencies{IDs: ids, Clock: clock, ModelRuns: f.modelRuns, Fence: fence, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(fixture.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindWorkflowStarter(runtime); err != nil {
		t.Fatal(err)
	}
	f.preparation, err = interviewapp.NewNotePreparationService(interviewapp.NotePreparationDependencies{Store: store, Snapshots: fixture.service, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := workflowpostgres.NewGORMRepository(fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "note-interview-integration", ModelVersion: "v1"}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "note-interview-integration", Version: "v1"}, Model: modelRef, Timeout: time.Second, MaxOutputTokens: 4096}
	catalog := agentapp.NewRuntimeCatalog()
	if err := interviewagent.RegisterNoteInterviewRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]agentapp.DeterministicChatStep, len(outputs))
	for i, output := range outputs {
		steps[i] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}}
	}
	f.provider = agentapp.NewDeterministicChatModel(steps...)
	f.modelStore = &synthesisInterviewModelStore{NoteGenerationStore: store}
	model, err := interviewagent.NewNoteInterviewModel(interviewagent.NoteInterviewModelDependencies{Model: f.provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: f.modelRuns, Store: f.modelStore, ProfileRef: profile.Ref, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.model = &synthesisInterviewSwitchModel{ready: model, missing: interviewagent.NewUnavailableNoteInterviewModel()}
	executor, err := interviewworkflow.NewNotePreparationExecutor(interviewworkflow.NotePreparationExecutorDependencies{Runs: runs, Store: store, Model: f.model, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(interviewapp.NotePreparationNodeKind, 1, executor); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := interviewworkflow.RegisterNotePreparationDefinition(definitions); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "synthesis-note-interview-integration", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.worker = worker
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(fixture.platform.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(ctx); err != nil {
			t.Errorf("stop note interview River worker: %v", err)
		}
	})
	f.artifacts, err = artifactpostgres.NewGORMRepository(fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := artifactauthoring.NewVerifier(fixture.authoring)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := artifactapp.NewCommandService(artifactapp.Dependencies{Repository: f.artifacts, Documents: documents, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := interviewartifact.NewBridge(commands)
	if err != nil {
		t.Fatal(err)
	}
	interviews, err := interviewpostgres.NewGORMRepository(fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	f.interview, err = interviewapp.NewService(interviewapp.Dependencies{Store: interviews, QuestionSource: interviews, ContextLoader: synthesisInterviewNoMemory{}, CandidateWriter: synthesisInterviewNoMemory{}, Scorer: interviewapp.DeterministicScorer{}, ArtifactBridge: bridge, IDGenerator: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *synthesisInterviewRuntime) wait(t *testing.T, noteID, preparationID foundation.ID, want interviewapp.NotePreparationStatus) interviewapp.NotePreparation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := f.preparation.Get(ctx, f.fixture.workspace, noteID, preparationID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status == want {
			var status string
			if err := f.fixture.db.WithContext(ctx).Raw(`SELECT status FROM workflow.run WHERE id=?`, string(p.WorkflowRunID)).Scan(&status).Error; err != nil {
				t.Fatal(err)
			}
			if status == "succeeded" || status == "failed" || status == "cancelled" {
				return p
			}
		} else if p.Status != interviewapp.NotePreparationQueued && p.Status != interviewapp.NotePreparationGenerating {
			t.Fatalf("note preparation status=%s want=%s failure=%+v", p.Status, want, p.Failure)
		}
		select {
		case <-ctx.Done():
			var nodes []struct {
				NodeKey, Status         string
				ErrorCode, ErrorSummary *string
			}
			f.fixture.db.Raw(`SELECT node_key,status,error_code,error_summary FROM workflow.node_run WHERE run_id=?`, string(p.WorkflowRunID)).Scan(&nodes)
			t.Fatalf("note preparation did not finish: status=%s nodes=%+v", p.Status, nodes)
		case <-ticker.C:
		}
	}
}

func (f *synthesisInterviewRuntime) redeliver(t *testing.T, preparation interviewapp.NotePreparation) {
	t.Helper()
	var persisted struct {
		ID       int64
		Attempt  int
		Args     []byte
		Metadata []byte
	}
	if err := f.fixture.db.WithContext(t.Context()).Raw(`SELECT id,attempt,args,metadata FROM workflow.river_job WHERE args->>'node_run_id'=? ORDER BY id LIMIT 1`, string(preparation.NodeRunID)).Scan(&persisted).Error; err != nil || persisted.ID == 0 {
		t.Fatalf("original River delivery is missing: %v", err)
	}
	var args riveradapter.NodeJobArgs
	if err := json.Unmarshal(persisted.Args, &args); err != nil {
		t.Fatal(err)
	}
	job := &river.Job[riveradapter.NodeJobArgs]{JobRow: &rivertype.JobRow{ID: persisted.ID, Attempt: persisted.Attempt + 1, EncodedArgs: persisted.Args, Metadata: persisted.Metadata}, Args: args}
	if err := f.worker.Work(t.Context(), job); err != nil {
		t.Fatalf("terminal River redelivery: %v", err)
	}
}

type synthesisInterviewSwitchModel struct {
	ready, missing interviewapp.NoteInterviewGenerator
	unavailable    atomic.Bool
}

func (m *synthesisInterviewSwitchModel) GenerateNoteInterview(ctx context.Context, execution workflowapp.ExecutionContext, preparation interviewapp.NotePreparation) (interviewapp.NotePreparation, error) {
	if m.unavailable.Load() {
		return m.missing.GenerateNoteInterview(ctx, execution, preparation)
	}
	return m.ready.GenerateNoteInterview(ctx, execution, preparation)
}

type synthesisInterviewModelStore struct {
	interviewapp.NoteGenerationStore
	afterCommit, beforeCommit, responseLost atomic.Bool
}

func (s *synthesisInterviewModelStore) CompleteNoteGeneration(ctx context.Context, command interviewapp.NoteGenerationCompletion) (interviewapp.NotePreparation, error) {
	if s.beforeCommit.Swap(false) {
		return interviewapp.NotePreparation{}, errors.New("test completion outcome unavailable")
	}
	p, err := s.NoteGenerationStore.CompleteNoteGeneration(ctx, command)
	if err == nil && s.afterCommit.Swap(false) {
		s.responseLost.Store(true)
		return interviewapp.NotePreparation{}, errors.New("test successful commit response lost")
	}
	return p, err
}

// This suite has no user-confirmed Memory and never creates Memory candidates.
type synthesisInterviewNoMemory struct{}

func (synthesisInterviewNoMemory) Load(context.Context, interviewapp.ContextQuery) (interviewapp.PersonalContext, error) {
	return interviewapp.PersonalContext{Preferences: []json.RawMessage{}, Context: []json.RawMessage{}}, nil
}

func (synthesisInterviewNoMemory) CreateCandidate(context.Context, interviewapp.MemoryCandidateRequest) (interviewapp.MemoryCandidateResult, error) {
	return interviewapp.MemoryCandidateResult{}, errors.New("note completion must not create a Memory candidate")
}
