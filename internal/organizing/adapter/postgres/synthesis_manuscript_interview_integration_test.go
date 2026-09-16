//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	localfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

// 本测试使用 119 的可信捕获与回执事务，以及 121 的真实 Authoring、Approval、Git 发布。合成权限和模型证明仍使用既有测试替代边界；Interview 使用真实 River 和记录模型的运行时。
func TestSynthesisManuscriptInterviewPublishedTrustBoundary(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "trusted"
		if manual {
			name = "manual_review"
		}
		t.Run(name, func(t *testing.T) { testPublishedManuscriptInterview(t, manual) })
	}
}

func testPublishedManuscriptInterview(t *testing.T, manual bool) {
	f := newSynthesisGitFixtureAtVersion(t, 123)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	initial := f.generation(t, 71000, nil, "The scheduler manages runnable work.")
	initial.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(initial, "manual persistence")}
	applied, err := f.service.ApplyGeneration(ctx, initial.Input, initial.Generation)
	check(err)
	before, err := f.service.GetNote(ctx, f.workspace, applied.Publications[0].NoteID)
	check(err)
	publish := f.preparePublication(t, before)
	written, err := f.node.Execute(ctx, publish.input, publish.identity)
	check(err)
	target, err := authoringdomain.DefaultGeneratedTargetPath(before.Note.ID)
	check(err)
	f.assertPublished(t, before, target, written)
	before, err = f.service.GetNote(ctx, f.workspace, before.Note.ID)
	check(err)
	l := *before.CurrentRevision
	original, err := l.Content()
	check(err)
	// 既有 v1 投影在迁移 119 下往返转换后保持不变。
	got, err := f.store.GetSynthesisRevision(ctx, f.workspace, l.NoteID, l.ID)
	check(err)
	if got.Hash != l.Hash || got.Manuscript != nil {
		t.Fatal("v1 projection changed")
	}
	const plan = `{"questions":[{"item_label":"I001","prompt":"Explain the scheduler responsibility.","answer_point_labels":["P001"],"follow_ups":[]}]}`
	var runtime *synthesisInterviewRuntime
	var v1 interviewapp.NotePreparation
	var v1Command interviewapp.PrepareNoteInterviewCommand
	if !manual {
		runtime = newSynthesisInterviewRuntime(t, f, plan, plan)
		v1Command = interviewapp.PrepareNoteInterviewCommand{WorkspaceID: f.workspace, NoteID: l.NoteID, Options: interviewapp.NoteInterviewOptions{Role: "Backend engineer", Difficulty: interviewdomain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1, MaxFollowUps: 0}, IdempotencyKey: "v1-before-manuscript"}
		first, err := runtime.preparation.Prepare(ctx, v1Command)
		check(err)
		v1 = runtime.wait(t, l.NoteID, first.Preparation.ID, interviewapp.NotePreparationReady)
		encoded, err := json.Marshal(v1.Snapshot)
		check(err)
		if strings.Contains(string(encoded), "manuscript") || v1.Snapshot.ProjectionHash != l.Hash || v1.Snapshot.ContentHash != l.ContentHash {
			t.Fatal("v1 serialized snapshot/hash changed under 123")
		}
	}
	next := f.generation(t, 72000, []app.SynthesisGenerationNote{{Note: before.Note, Revision: l}}, "Independent new scheduling evidence.")
	next.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(before, next.Input.Sources[0].Reference)}
	refs := []domain.SynthesisSourceRef{next.Input.Sources[0].Reference}
	delta, err := domain.ApplySynthesisDelta(f.workspace, l.Items, next.Generation.Notes[0].Delta, refs)
	check(err)
	var owner authoringapp.GeneratedDocumentState
	check(f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		owner, err = f.authoring.ReadGeneratedDocumentScoped(ctx, scope, f.workspace, l.DocumentID, l.NoteID)
		return err
	}))
	var publishedCommitID foundation.ID
	check(f.db.Raw("SELECT proposal_commit_id FROM organizing.synthesis_proven_publication WHERE workspace_id=? AND revision_id=?", string(f.workspace), string(l.ID)).Scan(&publishedCommitID).Error)
	authority := app.SynthesisManuscriptAuthority{DocumentID: l.DocumentID, DocumentVersion: owner.Document.Version, NoteVersion: before.Note.Version, LatestArticleID: l.ArticleRevisionID, PublishedPublicationID: owner.Publication.ID, PublishedProposalCommitID: publishedCommitID, PublishedGitCommit: owner.Publication.GitCommit, TargetPath: target, RootGrantID: organizingIntegrationID(73002), RootFingerprint: organizingIntegrationHash("root"), WorkspaceBindingVersion: 1, Anchor: app.SynthesisAnchorBinding{AnchorID: organizingIntegrationID(73003), ScopeVersion: 1, Scope: domain.AnchorScope{Topics: []string{"scheduling"}, Audiences: []string{"developers"}, Description: "Scheduling evidence"}, AllowedSources: refs}}
	proof := &manuscriptProofFixture{f: f, prepared: app.SynthesisManuscriptPrepared{Authority: authority, GenerationInput: next.Input, Generation: next.Generation, SemanticModelRunID: initial.Generation.ModelRunID, SemanticOutputHash: organizingIntegrationHash("semantic fixture"), MergeInput: app.SynthesisManuscriptMergeInput{Latest: l, Published: &l, NextMachine: domain.SynthesisManuscriptMachine{WorkspaceID: f.workspace, NoteID: l.NoteID, MachineTitle: l.Title, MachineItems: delta.Items, IneligibleItemIDs: []foundation.ID{}}}}}
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	check(err)
	files, err := localfs.NewReader(workspaces)
	check(err)
	engine, err := gitmerge.New(gitcli.New("git"))
	check(err)
	store, err := NewGORMSynthesisManuscriptStore(f.platform, SynthesisManuscriptStoreDependencies{Baselines: proof, Proof: proof, Files: files, Mapper: manuscript.Mapper{}, Merge: engine, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	check(err)
	command := app.PrepareSynthesisManuscript{WorkspaceID: f.workspace, NoteID: l.NoteID, ProcessingID: next.Input.ProcessingID, IdempotencyKey: "manual-capture-clean"}
	file := original
	if manual {
		file = "人工批注，保留原字节🙂。\n\n" + original
	}
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))

	attempt, err := store.Prepare(ctx, command)
	check(err)
	receipt, err := store.SealClean(ctx, f.workspace, attempt.ID)
	check(err)
	revision := l
	revision.ID = organizingIntegrationID(74001)
	revision.ArticleRevisionID = organizingIntegrationID(74002)
	revision.ParentRevisionID = l.ID
	revision.RevisionNo++
	revision.ArticleRevisionNo++
	revision.RendererVersion = domain.SynthesisRendererVersionV2
	revision.Manuscript = &receipt.Manuscript
	revision.Items, err = receipt.Manuscript.TrustedItems(receipt.Manuscript.Machine, manuscript.Mapper{})
	check(err)
	revision.Delta = next.Generation.Notes[0].Delta
	revision.ContentHash = receipt.Manuscript.ContentHash
	revision.SourceEventID = next.Input.SourceEvent.ID
	revision.WorkflowRunID = next.Input.WorkflowRunID
	revision.ModelRunID = next.Generation.ModelRunID
	revision.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	check(err)

	persistInterviewManuscriptRevision(t, f, store, attempt, receipt, revision, next)
	// 保留既有 Git 工作区干净的要求。普通审批前仅提交隔离环境中的人工文件，不禁用任何发布校验。
	if manual {
		synthesisGit(t, ctx, f.root, "add", "--", target)
		synthesisGit(t, ctx, f.root, "commit", "-m", "preserve interview manuscript fixture edit")
	}
	_, found, err := f.service.RecoverAppliedGeneration(ctx, f.workspace, next.Input.ProcessingID)
	check(err)
	if !found {
		t.Fatal("missing v2 apply receipt")
	}
	detail, err := f.service.GetNote(ctx, f.workspace, revision.NoteID)
	check(err)
	publication := f.preparePublication(t, detail)
	written, err = f.node.Execute(ctx, publication.input, publication.identity)
	check(err)
	f.assertPublished(t, detail, target, written)
	published, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, revision.NoteID)
	check(err)
	content, err := published.Content()
	check(err)
	if published.Manuscript == nil || content != receipt.Manuscript.FullContent || published.ContentHash != written.ResultHash {
		t.Fatal("published v2 full content identity changed")
	}
	if runtime == nil {
		runtime = newSynthesisInterviewRuntime(t, f, plan)
	}
	interviewCommand := interviewapp.PrepareNoteInterviewCommand{WorkspaceID: f.workspace, NoteID: revision.NoteID,
		Options: interviewapp.NoteInterviewOptions{Role: "Backend engineer", Difficulty: interviewdomain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1, MaxFollowUps: 0}, IdempotencyKey: "v2-interview"}
	if manual {
		if len(published.Items) != 0 || len(published.Manuscript.Machine.MachineItems) == 0 {
			t.Fatal("manual fixture lost zero-trusted/audit distinction")
		}
		for range 2 {
			_, err := runtime.preparation.Prepare(ctx, interviewCommand)
			if !organizingIntegrationError(err, foundation.ErrorInvalidInput, interviewdomain.ErrorCodeEvidenceInvalid) {
				t.Fatalf("zero trusted preparation: %v", err)
			}
		}
		f.count(t, "learning.note_interview_preparation", "workspace_id", string(f.workspace), 0)
		f.count(t, "learning.interview_session", "workspace_id", string(f.workspace), 0)
		if runtime.provider.CallCount() != 0 {
			t.Fatal("zero trusted material reached paid model")
		}
		return
	}
	if len(published.Items) == 0 {
		t.Fatal("clean v2 has no trusted items")
	}
	prepared, err := runtime.preparation.Prepare(ctx, interviewCommand)
	check(err)
	ready := runtime.wait(t, revision.NoteID, prepared.Preparation.ID, interviewapp.NotePreparationReady)
	if !reflect.DeepEqual(ready.Snapshot, published) || runtime.provider.CallCount() != 2 || ready.SessionID == nil {
		t.Fatal("v2 frozen snapshot/model binding lost")
	}
	snapshot, err := runtime.interview.Get(ctx, f.workspace, *ready.SessionID)
	check(err)
	expected := interviewdomain.NoteQuestionSource{Revision: ready.NoteRevision, ItemID: published.Items[0].ID, ItemKind: published.Items[0].Kind, Sources: published.Items[0].SourceReferences()}
	if len(snapshot.Questions) != 1 || !interviewdomain.SameNoteSource(snapshot.Questions[0].NoteSource, &expected) || *snapshot.Session.Config.Scope.NoteRevision != ready.NoteRevision {
		t.Fatal("v2 question/session lost original identity or sources")
	}
	replay, err := runtime.preparation.Prepare(ctx, interviewCommand)
	check(err)
	if !replay.Replayed || replay.Preparation.ID != ready.ID || !reflect.DeepEqual(replay.Preparation.Snapshot, published) {
		t.Fatal("v2 preparation replay drifted")
	}
	runtime.redeliver(t, ready)
	if runtime.provider.CallCount() != 2 {
		t.Fatal("v2 replay called provider again")
	}
	// 重新插入带伪造快照的排队准备记录；精确的不可变版本比较必须先于外键和运行时完整性检查拒绝该记录。
	for _, path := range []string{"{manuscript,full_content}", "{manuscript,machine,machine_items,0,fact,sources,0,source_span_id}"} {
		err := f.db.Exec(`INSERT INTO learning.note_interview_preparation
   (id,workspace_id,note_id,revision_id,document_id,article_revision_id,projection_hash,snapshot,options,status,workflow_run_id,node_run_id,version,idempotency_key,request_hash,created_at,updated_at)
   SELECT ?,workspace_id,note_id,revision_id,document_id,article_revision_id,projection_hash,
    jsonb_set(snapshot,?::text[],to_jsonb('forged interview source'::text)),options,'QUEUED',workflow_run_id,node_run_id,1,?,request_hash,created_at,created_at
   FROM learning.note_interview_preparation WHERE id=?`, string(organizingIntegrationID(74901)), path, "forged-"+path, string(ready.ID)).Error
		if platformpostgres.SQLState(err) != "23514" || !strings.Contains(err.Error(), "snapshot differs from immutable revision") {
			t.Fatalf("forged manuscript accepted or wrong failure: %v", err)
		}
	}
	v1Replay, err := runtime.preparation.Prepare(ctx, v1Command)
	check(err)
	if !v1Replay.Replayed || !reflect.DeepEqual(v1Replay.Preparation, v1) {
		t.Fatal("v1 replay drifted after v2 publication")
	}
	v1Session, err := runtime.interview.Get(ctx, f.workspace, *v1.SessionID)
	check(err)
	if len(v1Session.Questions) != 1 || v1Session.Questions[0].NoteSource.Revision != v1.NoteRevision {
		t.Fatal("historical v1 question acquired v2 identity")
	}
	f.count(t, "learning.note_interview_preparation", "workspace_id", string(f.workspace), 2)
}

// 既有存储辅助函数要求可信子集为空；保留该测试数据不变，并保留此事务用于非空 v2 场景。
func persistInterviewManuscriptRevision(t *testing.T, f *synthesisGitFixture, store *GORMSynthesisManuscriptStore, a app.SynthesisManuscriptAttempt, r app.SynthesisManuscriptReceipt, revision domain.SynthesisRevision, next app.SynthesisApplyRecord) {
	t.Helper()
	ctx := t.Context()
	err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err = store.VerifyReceiptScoped(ctx, scope, f.workspace, r.ID, revision); err != nil {
			return err
		}
		request := authoringdomain.GeneratedRevisionRequest{WorkspaceID: f.workspace, DocumentID: revision.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: revision.NoteID, OriginRevisionID: revision.ID, ProjectionHash: revision.Hash, ExpectedDocumentVersion: a.Prepared.Authority.DocumentVersion, RevisionNo: int(revision.ArticleRevisionNo), Title: revision.Title, TargetPath: a.Prepared.Authority.TargetPath, Content: r.Manuscript.FullContent, ParentRevisionID: a.Prepared.Authority.LatestArticleID}
		hash, err := authoringdomain.ComputeGeneratedRevisionRequestHash(request)
		if err != nil {
			return err
		}
		if _, err = f.authoring.AppendGeneratedRevisionScoped(ctx, scope, authoringapp.GeneratedRevisionRecord{Request: request, RequestHash: hash, IdempotencyKey: "manual-test-v2", CreatedAt: revision.CreatedAt}); err != nil {
			return err
		}
		row, err := synthesisRevisionRecord(revision)
		if err != nil {
			return err
		}
		// SQL 必须在提交前拒绝缺少回执的 v2 封装。
		if err = tx.SavePoint("without_receipt").Error; err != nil {
			return err
		}
		if err = tx.Create(&row).Error; err == nil {
			return errors.New("v2 inserted without a receipt")
		}
		if err = tx.RollbackTo("without_receipt").Error; err != nil {
			return err
		}
		receiptID := string(r.ID)
		row.ManuscriptReceiptID = &receiptID
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		loaded, err := loadSynthesisRevision(tx, f.workspace, revision.NoteID, revision.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(loaded, revision) {
			return errors.New("stored v2 revision changed")
		}
		if err = insertSynthesisRevisionSources(tx, revision); err != nil {
			return err
		}
		if err = tx.Model(&synthesisNoteModel{}).Where("id=? AND workspace_id=?", string(revision.NoteID), string(f.workspace)).Updates(map[string]any{"current_revision_id": string(revision.ID), "version": a.Prepared.Authority.NoteVersion + 1, "status": string(domain.SynthesisPendingApproval), "workflow_run_id": string(revision.WorkflowRunID), "updated_at": revision.CreatedAt}).Error; err != nil {
			return err
		}
		key := "synthesis-publish:" + string(revision.ID)
		publishHash, err := authoringdomain.ComputePublishRequestHash(f.workspace, revision.DocumentID, revision.ArticleRevisionID)
		if err != nil {
			return err
		}
		preparation, err := f.authoring.ReservePublicationScoped(ctx, scope, authoringapp.ReservePublicationRecord{Binding: authoringapp.PublishBinding{WorkspaceID: f.workspace, DocumentID: revision.DocumentID, RevisionID: revision.ArticleRevisionID, IdempotencyKey: key, RequestHash: publishHash}, ReservationID: organizingIntegrationID(74004), ReservedAt: revision.CreatedAt})
		if err != nil {
			return err
		}
		if preparation.Reservation.MergeBaseline == nil || preparation.Reservation.MergeBaseline.ReceiptID != r.ID || preparation.Reservation.MergeBaseline.CaptureID != a.CaptureID || preparation.Reservation.BaseVersion != manuscriptBytesHash([]byte(a.Prepared.MergeInput.FileContent)) || preparation.Reservation.MergeBaseline.PublishedRevisionID != a.Prepared.MergeInput.Published.ArticleRevisionID {
			return errors.New("reservation lost proven F/P baselines")
		}
		result := app.SynthesisApplyResult{ProcessingID: next.Input.ProcessingID, Changed: true, RevisionIDs: []foundation.ID{revision.ID}, Publications: []app.SynthesisPublicationCommand{{NoteID: revision.NoteID, RevisionID: revision.ID, DocumentID: revision.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, ContentHash: revision.ContentHash, IdempotencyKey: key}}}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		processingKey, err := app.SynthesisExecutionKey(next.Input.SourceEvent, "", "")
		if err != nil {
			return err
		}
		return tx.Create(&synthesisApplyModel{WorkspaceID: string(f.workspace), ProcessingID: string(next.Input.ProcessingID), ProcessingKey: processingKey, SourceEventID: string(revision.SourceEventID), WorkflowRunID: string(revision.WorkflowRunID), ModelRunID: string(revision.ModelRunID), RequestHash: next.Input.RequestHash, OutputHash: next.Generation.OutputHash, BindingHash: organizingIntegrationHash("test-v2-receipt-binding"), Changed: true, Result: organizingJSONB(encoded), CreatedAt: revision.CreatedAt}).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := f.store.GetSynthesisRevision(ctx, f.workspace, revision.NoteID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := reloaded.Content()
	if err != nil || content != r.Manuscript.FullContent || !reflect.DeepEqual(reloaded.Items, revision.Items) {
		t.Fatal("v2 stored full content or trusted subset lost")
	}
}
