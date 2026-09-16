//go:build integration

package postgres

import (
	"context"
	"encoding/json"

	"os"
	"path/filepath"
	"reflect"

	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
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
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

// 明确的授权替代边界：存储检查真实 Workflow 归属；当前操作者能力和根目录授权仅由此测试数据表示。生产认证、模型及 HumanAuthority 的组装在后续阶段实现。
type manuscriptHumanAuthorityFixture struct {
	proof   *manuscriptProofFixture
	binding app.SynthesisManuscriptHumanBinding
}

func (h manuscriptHumanAuthorityFixture) AuthorizeSynthesisManuscriptHumanScoped(_ context.Context, _ foundation.TransactionScope, b app.SynthesisManuscriptHumanBinding) error {
	if b != h.binding {
		return manuscriptStoreConflict("fixture caller is scoped to another task")
	}
	return nil
}
func (h manuscriptHumanAuthorityFixture) VerifyPendingSynthesisManuscriptHumanScoped(ctx context.Context, scope foundation.TransactionScope, b app.SynthesisManuscriptHumanBinding, p app.SynthesisManuscriptPrepared) (app.SynthesisManuscriptAuthority, error) {
	if err := h.AuthorizeSynthesisManuscriptHumanScoped(ctx, scope, b); err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	return h.proof.VerifySynthesisManuscriptPreparedScoped(ctx, scope, p)
}
func TestSynthesisManuscriptReviewPersistence(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 124)
	var ledgerExists bool
	if err := f.db.Raw("SELECT to_regclass('organizing.synthesis_manuscript_review_decision') IS NOT NULL").Scan(&ledgerExists).Error; err != nil || !ledgerExists {
		t.Fatalf("requires main to integrate 00124 into the formal Atlas directory: %v", err)
	}
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

	file := original + "\nHuman appendix competing with machine appendix.\n"
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))
	attempt, err := store.Prepare(ctx, app.PrepareSynthesisManuscript{WorkspaceID: f.workspace, NoteID: l.NoteID, ProcessingID: next.Input.ProcessingID, IdempotencyKey: "review-attempt"})
	check(err)
	if attempt.Preview.Review == nil {
		t.Fatal("fixture requires real Git conflict")
	}
	binding := app.SynthesisManuscriptHumanBinding{WorkspaceID: f.workspace, ProcessingID: next.Input.ProcessingID, HumanTaskID: organizingIntegrationID(79001), RunID: next.Input.WorkflowRunID, NodeRunID: next.Input.NodeRunID, TargetVersion: 1}
	review, err := NewGORMSynthesisManuscriptReviewStore(store, manuscriptHumanAuthorityFixture{proof, binding})
	check(err)
	command := app.DecideSynthesisManuscript{Binding: binding, NoteID: l.NoteID, AttemptID: attempt.ID, CaptureID: attempt.CaptureID, IdempotencyKey: "decision-1", Resolution: app.SynthesisManuscriptResolution{Stage: attempt.Preview.Review.Stage, PreviewFingerprint: attempt.Preview.Fingerprint, FinalContent: attempt.Preview.Review.Current}}
	for _, conflict := range attempt.Preview.Review.Conflicts {
		command.Resolution.AcknowledgedOrdinals = append(command.Resolution.AcknowledgedOrdinals, conflict.Ordinal)
	}
	if _, err = review.Decide(ctx, command); err == nil {
		t.Fatal("preallocated task ID authorized review")
	}
	workflow, err := workflowpostgres.NewGORMRepository(f.platform)
	check(err)
	at := time.Now().UTC().Truncate(time.Microsecond)
	runID := binding.RunID
	_, err = workflow.CreateHumanTask(ctx, workflowdomain.HumanTask{ID: binding.HumanTaskID, RunID: runID, NodeRunID: binding.NodeRunID, Status: workflowdomain.HumanTaskPending, ExpectedInputSchema: json.RawMessage(`{}`), TargetVersion: 1, CreatedAt: at}, workflowdomain.OutboxEvent{ID: organizingIntegrationID(79002), WorkspaceID: f.workspace, RunID: &runID, Type: "workflow.node.changed", IdempotencyKey: "review-human-wait", Payload: json.RawMessage(`{}`), OccurredAt: at}, at)
	check(err)
	manifest, err := review.ReadManifest(ctx, binding)
	check(err)
	if manifest.Ready || len(manifest.Targets) != 1 {
		t.Fatal("unresolved target was ready")
	}
	proof.fail = true
	if _, err = review.Decide(ctx, command); err == nil {
		t.Fatal("current human owner authority drift accepted")
	}
	proof.fail = false
	omitted := command
	omitted.Resolution.AcknowledgedOrdinals = nil
	if _, err = review.Decide(ctx, omitted); err == nil {
		t.Fatal("missing conflict ordinals accepted")
	}
	wrongPreview := command
	wrongPreview.Resolution.PreviewFingerprint = organizingIntegrationHash("another preview")
	if _, err = review.Decide(ctx, wrongPreview); err == nil {
		t.Fatal("wrong conflict fingerprint accepted")
	}
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file+"drift"), 0600))
	if _, err = review.Decide(ctx, command); err == nil {
		t.Fatal("F drift accepted")
	}
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))
	type outcome struct {
		d   app.SynthesisManuscriptStageDecision
		err error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() { d, err := review.Decide(ctx, command); results <- outcome{d, err} }()
	}
	first, second := <-results, <-results
	check(first.err)
	check(second.err)
	if !reflect.DeepEqual(first.d, second.d) {
		t.Fatal("concurrent exact decision diverged")
	}
	if first.d.Result.Preview.Manuscript == nil {
		t.Fatal("final decision did not seal")
	}
	changed := command
	changed.Resolution.FinalContent += "different"
	if _, err = review.Decide(ctx, changed); err == nil {
		t.Fatal("same key changed input accepted")
	}
	cross := command
	cross.Binding.WorkspaceID = f.otherWorkspace
	if _, err = review.Decide(ctx, cross); err == nil {
		t.Fatal("cross workspace accepted")
	}
	restarted, err := NewGORMSynthesisManuscriptReviewStore(store, manuscriptHumanAuthorityFixture{proof, binding})
	check(err)
	reopened, err := restarted.ReadManifest(ctx, binding)
	check(err)
	if !reopened.Ready || reopened.Targets[0].Receipt == nil || reopened.Targets[0].Receipt.Review == nil {
		t.Fatal("receipt not restored")
	}
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file+"later"), 0600))
	proof.fail = true
	replay, err := restarted.Decide(ctx, command)
	check(err)
	if !reflect.DeepEqual(replay, first.d) {
		t.Fatal("exact recovery changed on drift")
	}
	if err = f.db.Exec("UPDATE organizing.synthesis_manuscript_review_decision SET sequence=2 WHERE id=?", string(first.d.ID)).Error; err == nil {
		t.Fatal("immutable decision updated")
	}
	// 从实际持久化、含人工文本的 v2 L 继续。其机器基线保留了被删除的机器新增内容；另一次新增与该人工删除竞争时，应产生真实内部冲突。
	proof.fail = false
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))
	receipt := *reopened.Targets[0].Receipt
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
	check(f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.VerifyReceiptScoped(ctx, scope, f.workspace, receipt.ID, revision)
	}))

	testPersistManuscriptRevision(t, f, store, attempt, receipt, revision, next)
	detail, err := f.service.GetNote(ctx, f.workspace, l.NoteID)
	check(err)
	latest := *detail.CurrentRevision
	otherSeed := f.generation(t, 80000, nil, "Second target evidence.")
	otherSeed.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(otherSeed, "second review target")}
	otherApplied, err := f.service.ApplyGeneration(ctx, otherSeed.Input, otherSeed.Generation)
	check(err)
	other, err := f.service.GetNote(ctx, f.workspace, otherApplied.Publications[0].NoteID)
	check(err)
	newer := f.generation(t, 78000, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: latest}, {Note: other.Note, Revision: *other.CurrentRevision}}, "Third independent finding.")
	// 处理重试保留处理身份，但使用新的运行和不可变生成结果；在真实数据库中保留较早的尝试和回执。
	newer.Input.ProcessingID = next.Input.ProcessingID
	source := newer.Input.Sources[0].Reference
	addition := domain.SynthesisItem{ID: source.SourceSpanID, Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "A third machine finding.", Sources: []domain.SynthesisSourceRef{source}}}
	newer.Generation.Notes = []app.SynthesisGeneratedNote{{NoteID: latest.NoteID, BaseRevisionID: latest.ID, TopicKey: detail.Note.TopicKey, Title: detail.Note.Title, Aliases: detail.Note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &addition}}}}}
	newer.Generation.Notes = append(newer.Generation.Notes, extendedSynthesisNote(other, source))
	newerDelta, err := app.SynthesisMachineDelta(newer.Input, newer.Generation.Notes[0], &latest)
	check(err)
	check(f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		owner, err = f.authoring.ReadGeneratedDocumentScoped(ctx, scope, f.workspace, latest.DocumentID, latest.NoteID)
		return err
	}))
	auth := authority
	auth.DocumentVersion = owner.Document.Version
	auth.NoteVersion = detail.Note.Version
	auth.LatestArticleID = latest.ArticleRevisionID
	auth.Anchor.AllowedSources = []domain.SynthesisSourceRef{source}
	proof.prepared = app.SynthesisManuscriptPrepared{Authority: auth, GenerationInput: newer.Input, Generation: newer.Generation, SemanticModelRunID: initial.Generation.ModelRunID, SemanticOutputHash: organizingIntegrationHash("second semantic fixture"), MergeInput: app.SynthesisManuscriptMergeInput{Latest: latest, Published: &l, NextMachine: domain.SynthesisManuscriptMachine{WorkspaceID: f.workspace, NoteID: latest.NoteID, MachineTitle: latest.Title, MachineItems: newerDelta.Items, IneligibleItemIDs: latest.Manuscript.Machine.IneligibleItemIDs}}}
	secondFile := original + "\nA different workspace appendix.\n"
	check(os.WriteFile(filepath.Join(f.root, target), []byte(secondFile), 0600))
	two, err := store.Prepare(ctx, app.PrepareSynthesisManuscript{WorkspaceID: f.workspace, NoteID: latest.NoteID, ProcessingID: newer.Input.ProcessingID, IdempotencyKey: "two-stage-attempt"})
	check(err)
	if two.Preview.Review == nil || two.Preview.Review.Stage != app.SynthesisMergeCandidateStage {
		t.Fatal("fixture did not create inner-stage conflict")
	}
	b2 := app.SynthesisManuscriptHumanBinding{WorkspaceID: f.workspace, ProcessingID: newer.Input.ProcessingID, HumanTaskID: organizingIntegrationID(79003), RunID: newer.Input.WorkflowRunID, NodeRunID: newer.Input.NodeRunID, TargetVersion: 1}
	secondReview, err := NewGORMSynthesisManuscriptReviewStore(store, manuscriptHumanAuthorityFixture{proof, b2})
	check(err)
	at = time.Now().UTC().Truncate(time.Microsecond)
	runID = b2.RunID
	_, err = workflow.CreateHumanTask(ctx, workflowdomain.HumanTask{ID: b2.HumanTaskID, RunID: runID, NodeRunID: b2.NodeRunID, Status: workflowdomain.HumanTaskPending, ExpectedInputSchema: json.RawMessage(`{}`), TargetVersion: 1, CreatedAt: at}, workflowdomain.OutboxEvent{ID: organizingIntegrationID(79004), WorkspaceID: f.workspace, RunID: &runID, Type: "workflow.node.changed", IdempotencyKey: "two-stage-human-wait", Payload: json.RawMessage(`{}`), OccurredAt: at}, at)
	check(err)
	decide := func(preview app.SynthesisManuscriptMergePreview, key, content string) app.SynthesisManuscriptStageDecision {
		t.Helper()
		c := app.DecideSynthesisManuscript{Binding: b2, NoteID: latest.NoteID, AttemptID: two.ID, CaptureID: two.CaptureID, IdempotencyKey: key, Resolution: app.SynthesisManuscriptResolution{Stage: preview.Review.Stage, PreviewFingerprint: preview.Fingerprint, FinalContent: content}}
		for _, conflict := range preview.Review.Conflicts {
			c.Resolution.AcknowledgedOrdinals = append(c.Resolution.AcknowledgedOrdinals, conflict.Ordinal)
		}
		d, err := secondReview.Decide(ctx, c)
		check(err)
		return d
	}
	inner := decide(two.Preview, "inner-stage", two.Preview.Review.Proposed)
	if inner.Result.Preview.Review == nil || inner.Result.Preview.Review.Stage != app.SynthesisMergeWorkspaceStage || inner.Result.Preview.Manuscript != nil {
		t.Fatal("inner decision sealed outer conflict")
	}
	midway, err := secondReview.ReadManifest(ctx, b2)
	check(err)
	if midway.Ready || midway.Targets[0].Receipt != nil {
		t.Fatal("intermediate stage got receipt")
	}
	innerReplay, err := secondReview.Decide(ctx, inner.Command)
	check(err)
	if !reflect.DeepEqual(innerReplay, inner) {
		t.Fatal("persisted first stage changed on retry")
	}
	wrongStage := inner.Command
	wrongStage.IdempotencyKey = "stale-inner-fingerprint"
	if _, err = secondReview.Decide(ctx, wrongStage); err == nil {
		t.Fatal("first fingerprint authorized second stage")
	}
	outer := decide(inner.Result.Preview, "outer-stage", inner.Result.Preview.Review.Current)
	if outer.Sequence != 2 || outer.PreviousHash != inner.Hash || outer.Result.Preview.Manuscript == nil {
		t.Fatal("second stage did not finish")
	}
	final, err := secondReview.ReadManifest(ctx, b2)
	check(err)
	if final.Ready || len(final.Targets) != 1 || len(final.Targets[0].Receipt.Review.Decisions) != 2 {
		t.Fatal("missing unprepared target was lost from frozen generation")
	}
	// 第二个目标虽干净，却尚未准备。只有其原始 119 回执存在后，整个等待中的任务才可就绪。
	otherLatest := *other.CurrentRevision
	otherDelta, err := app.SynthesisMachineDelta(newer.Input, newer.Generation.Notes[1], &otherLatest)
	check(err)
	check(f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		owner, err = f.authoring.ReadGeneratedDocumentScoped(ctx, scope, f.workspace, otherLatest.DocumentID, otherLatest.NoteID)
		return err
	}))
	otherAuth := auth
	otherAuth.DocumentID = otherLatest.DocumentID
	otherAuth.DocumentVersion = owner.Document.Version
	otherAuth.NoteVersion = other.Note.Version
	otherAuth.LatestArticleID = otherLatest.ArticleRevisionID
	otherAuth.TargetPath = owner.Document.CanonicalPath
	otherAuth.PublishedPublicationID = ""
	otherAuth.PublishedProposalCommitID = ""
	otherAuth.PublishedGitCommit = ""
	proof.prepared = app.SynthesisManuscriptPrepared{Authority: otherAuth, GenerationInput: newer.Input, Generation: newer.Generation, SemanticModelRunID: initial.Generation.ModelRunID, SemanticOutputHash: organizingIntegrationHash("second semantic fixture"), MergeInput: app.SynthesisManuscriptMergeInput{Latest: otherLatest, NextMachine: domain.SynthesisManuscriptMachine{WorkspaceID: f.workspace, NoteID: otherLatest.NoteID, MachineTitle: otherLatest.Title, MachineItems: otherDelta.Items, IneligibleItemIDs: []foundation.ID{}}}}
	clean, err := store.Prepare(ctx, app.PrepareSynthesisManuscript{WorkspaceID: f.workspace, NoteID: otherLatest.NoteID, ProcessingID: newer.Input.ProcessingID, IdempotencyKey: "second-clean-target"})
	check(err)
	pendingClean, err := secondReview.ReadManifest(ctx, b2)
	check(err)
	if pendingClean.Ready || len(pendingClean.Targets) != 2 {
		t.Fatal("unsealed clean target was ready")
	}
	cleanReceipt, err := store.SealClean(ctx, f.workspace, clean.ID)
	check(err)
	if cleanReceipt.Review != nil {
		t.Fatal("clean target did not retain 119 receipt shape")
	}
	ready, err := secondReview.ReadManifest(ctx, b2)
	check(err)
	if !ready.Ready || len(ready.Targets) != 2 {
		t.Fatal("complete multi-target receipts were not ready")
	}
	reopenedStore, err := NewGORMSynthesisManuscriptStore(f.platform, store.dependencies)
	check(err)
	reopenedReview, err := NewGORMSynthesisManuscriptReviewStore(reopenedStore, manuscriptHumanAuthorityFixture{proof, b2})
	check(err)
	restored, err := reopenedReview.ReadManifest(ctx, b2)
	check(err)
	if !reflect.DeepEqual(ready, restored) {
		t.Fatal("multi-target manifest changed on reopen")
	}
	oldAttempt, err := store.GetAttempt(ctx, f.workspace, attempt.ID)
	check(err)
	if !reflect.DeepEqual(oldAttempt, attempt) {
		t.Fatal("retry modified the old run attempt")
	}
	oldManifest, err := review.ReadManifest(ctx, binding)
	check(err)
	if !reflect.DeepEqual(oldManifest, reopened) {
		t.Fatal("new run polluted the old run manifest")
	}
	var sameProcessingAttempts int64
	check(f.db.Model(&manuscriptAttemptRow{}).Where("workspace_id=? AND convert_from(payload,'UTF8')::jsonb->'command'->>'processing_id'=?", string(f.workspace), string(binding.ProcessingID)).Count(&sameProcessingAttempts).Error)
	if sameProcessingAttempts != 3 {
		t.Fatalf("old/new run attempts not retained: %d", sameProcessingAttempts)
	}
	// 通过真实的限定作用域应用校验，核验最终两阶段回执。
	proof.prepared = two.Prepared
	var resolvedReceipt app.SynthesisManuscriptReceipt
	for _, targetState := range restored.Targets {
		if targetState.Attempt.ID == two.ID {
			resolvedReceipt = *targetState.Receipt
		}
	}
	candidate := latest
	candidate.ID = organizingIntegrationID(81001)
	candidate.ArticleRevisionID = organizingIntegrationID(81002)
	candidate.ParentRevisionID = latest.ID
	candidate.RevisionNo++
	candidate.ArticleRevisionNo++
	candidate.Manuscript = &resolvedReceipt.Manuscript
	candidate.RendererVersion = domain.SynthesisRendererVersionV2
	candidate.Items, err = resolvedReceipt.Manuscript.TrustedItems(resolvedReceipt.Manuscript.Machine, manuscript.Mapper{})
	check(err)
	candidate.Delta = newer.Generation.Notes[0].Delta
	candidate.ContentHash = resolvedReceipt.Manuscript.ContentHash
	candidate.SourceEventID = newer.Input.SourceEvent.ID
	candidate.WorkflowRunID = newer.Input.WorkflowRunID
	candidate.ModelRunID = newer.Generation.ModelRunID
	candidate.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	candidate.Hash, err = domain.ComputeSynthesisRevisionHash(candidate)
	check(err)
	verifyFinal := func() error {
		return f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			return store.VerifyReceiptScoped(ctx, scope, f.workspace, resolvedReceipt.ID, candidate)
		})
	}
	check(verifyFinal())
	check(os.WriteFile(filepath.Join(f.root, target), []byte(secondFile+"drift after seal"), 0600))
	if err = verifyFinal(); err == nil {
		t.Fatal("apply fence accepted F drift after final receipt")
	}
	stillHistorical, err := reopenedReview.ReadManifest(ctx, b2)
	check(err)
	if !reflect.DeepEqual(restored, stillHistorical) {
		t.Fatal("historical receipts changed after file drift")
	}
	t.Log("PASS: real Git inner/outer decisions, atomic final receipt, exact concurrency/recovery, F/cross-workspace rejection, missing and unsealed second target not ready, clean 119 receipt, reopened complete manifest")

}
