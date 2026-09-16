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
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

// 此测试证明持久化、真实文件、Git 合并及来源校验，不证明生产模型执行或已组装的 RootGrant 实现；这些限定作用域权限仍是必需但尚未实现的组装依赖。
type manuscriptProofFixture struct {
	f        *synthesisGitFixture
	prepared app.SynthesisManuscriptPrepared
	fail     bool
}

func (p *manuscriptProofFixture) LoadSynthesisManuscriptBaseline(_ context.Context, _ app.PrepareSynthesisManuscript) (app.SynthesisManuscriptPrepared, error) {
	b, _ := json.Marshal(p.prepared)
	var out app.SynthesisManuscriptPrepared
	err := json.Unmarshal(b, &out)
	return out, err
}
func (p *manuscriptProofFixture) VerifySynthesisManuscriptPreparedScoped(ctx context.Context, scope foundation.TransactionScope, prepared app.SynthesisManuscriptPrepared) (app.SynthesisManuscriptAuthority, error) {
	if p.fail {
		return app.SynthesisManuscriptAuthority{}, manuscriptStoreConflict("test owner authority changed")
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	note, err := loadSynthesisNote(tx, p.f.workspace, prepared.MergeInput.Latest.NoteID, true)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	if note.CurrentRevisionID != prepared.MergeInput.Latest.ID {
		return app.SynthesisManuscriptAuthority{}, manuscriptStoreConflict("test note changed")
	}
	refs := []domain.SynthesisSourceRef{}
	for _, source := range prepared.GenerationInput.Sources {
		refs = append(refs, source.Reference)
	}
	if err := p.f.sources.VerifySynthesisSourcesScoped(ctx, scope, p.f.workspace, refs); err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	return p.prepared.Authority, nil
}

type manuscriptCommitLoss struct{ foundation.UnitOfWork }

func (u manuscriptCommitLoss) Within(ctx context.Context, opts foundation.TransactionOptions, fn foundation.TransactionFunc) error {
	if err := u.UnitOfWork.Within(ctx, opts, fn); err != nil {
		return err
	}
	return errors.New("simulated lost commit response")
}

// 两名调用方都必须先未命中初始幂等查询，之后任一方才能开始准备。
type manuscriptConcurrentBaseline struct {
	app.SynthesisManuscriptBaselineReader
	arrived chan struct{}
	release chan struct{}
}

func (b manuscriptConcurrentBaseline) LoadSynthesisManuscriptBaseline(ctx context.Context, command app.PrepareSynthesisManuscript) (app.SynthesisManuscriptPrepared, error) {
	b.arrived <- struct{}{}
	select {
	case <-b.release:
		return b.SynthesisManuscriptBaselineReader.LoadSynthesisManuscriptBaseline(ctx, command)
	case <-ctx.Done():
		return app.SynthesisManuscriptPrepared{}, ctx.Err()
	}
}

func TestSynthesisManuscriptStorageCaptureReceiptAndV2(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 121)
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
	command := app.PrepareSynthesisManuscript{WorkspaceID: f.workspace, NoteID: l.NoteID, ProcessingID: next.Input.ProcessingID, IdempotencyKey: "manual-capture-clean"}
	file := "人工批注，保留原字节🙂。\n\n" + original
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))
	t.Run("concurrent exact prepare recovers the winning attempt", func(t *testing.T) {
		concurrent := *store
		barrier := manuscriptConcurrentBaseline{SynthesisManuscriptBaselineReader: proof, arrived: make(chan struct{}, 2), release: make(chan struct{})}
		concurrent.dependencies.Baselines = barrier
		c := command
		c.IdempotencyKey = "manual-concurrent-prepare"
		type outcome struct {
			attempt app.SynthesisManuscriptAttempt
			err     error
		}
		results := make(chan outcome, 2)
		for range 2 {
			go func() {
				a, err := concurrent.Prepare(ctx, c)
				results <- outcome{a, err}
			}()
		}
		<-barrier.arrived
		<-barrier.arrived
		close(barrier.release)
		first, second := <-results, <-results
		if first.err != nil || second.err != nil {
			t.Fatalf("concurrent prepare: %v; %v", first.err, second.err)
		}
		if !reflect.DeepEqual(first.attempt, second.attempt) {
			t.Fatal("concurrent prepare returned different attempts")
		}
		for _, table := range []string{"organizing.synthesis_manuscript_capture", "organizing.synthesis_manuscript_attempt"} {
			var count int64
			if err := f.db.Table(table).Where("workspace_id=?", string(f.workspace)).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("%s count=%d want 1: %v", table, count, err)
			}
		}
		changed := c
		changed.NoteID = organizingIntegrationID(73999)
		if _, err := store.Prepare(ctx, changed); err == nil {
			t.Fatal("concurrent winner accepted another command")
		}
	})
	attempt, err := store.Prepare(ctx, command)
	check(err)
	if attempt.Preview.Manuscript == nil || !strings.Contains(attempt.Preview.Manuscript.FullContent, "人工批注") || len(attempt.Preview.Manuscript.Assessment.Mappings) != 0 {
		t.Fatalf("manual text or trust boundary lost: %+v", attempt.Preview)
	}
	restarted, err := NewGORMSynthesisManuscriptStore(f.platform, store.dependencies)
	check(err)
	recovered, err := restarted.GetAttempt(ctx, f.workspace, attempt.ID)
	check(err)
	if !reflect.DeepEqual(attempt, recovered) {
		t.Fatal("attempt did not recover exactly")
	}
	if _, err := store.GetAttempt(ctx, f.otherWorkspace, attempt.ID); err == nil {
		t.Fatal("cross-workspace attempt leaked")
	}
	changedCommand := command
	changedCommand.NoteID = organizingIntegrationID(73999)
	if _, err := store.Prepare(ctx, changedCommand); err == nil {
		t.Fatal("key rebinding accepted")
	}
	// 文件或所属模块状态变化必须导致首次封存失败，且不留下回执。
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file+"new"), 0600))
	if _, err := store.SealClean(ctx, f.workspace, attempt.ID); err == nil {
		t.Fatal("stale file sealed")
	}
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))
	proof.fail = true
	if _, err := store.SealClean(ctx, f.workspace, attempt.ID); err == nil {
		t.Fatal("stale scoped proof sealed")
	}
	proof.fail = false
	store.uow = manuscriptCommitLoss{f.uow}
	if _, err := store.SealClean(ctx, f.workspace, attempt.ID); err == nil {
		t.Fatal("commit response loss was hidden")
	}
	store.uow = f.uow
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file+"later edit"), 0600))
	proof.fail = true
	receipt, err := restarted.SealClean(ctx, f.workspace, attempt.ID)
	check(err)
	replay, err := store.SealClean(ctx, f.workspace, attempt.ID)
	check(err)
	if !reflect.DeepEqual(receipt, replay) {
		t.Fatal("sealed receipt lost on later drift")
	}
	historical, err := store.GetAttempt(ctx, f.workspace, attempt.ID)
	check(err)
	if !reflect.DeepEqual(historical, attempt) {
		t.Fatal("history depends on current authority")
	}
	if _, err := store.SealClean(ctx, f.otherWorkspace, attempt.ID); err == nil {
		t.Fatal("cross-workspace seal leaked")
	}
	proof.fail = false
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))
	// v2 候选仅在此测试环境内构造；生产应用路径尚未接通。
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
	forged := revision
	forged.Delta = initial.Generation.Notes[0].Delta
	forged.Hash, err = domain.ComputeSynthesisRevisionHash(forged)
	check(err)
	if err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.VerifyReceiptScoped(ctx, scope, f.workspace, receipt.ID, forged)
	}); err == nil {
		t.Fatal("receipt allowed another delta")
	}
	// 数据库拒绝修改、清空、被改动的校验和及伪造回执。
	for _, statement := range []string{"UPDATE organizing.synthesis_manuscript_capture SET payload=payload", "DELETE FROM organizing.synthesis_manuscript_attempt", "TRUNCATE organizing.synthesis_manuscript_receipt"} {
		if err := f.db.Exec(statement).Error; err == nil {
			t.Fatal("immutable manuscript record changed")
		}
	}
	var receiptRow manuscriptReceiptRow
	check(f.db.Where("id=?", string(receipt.ID)).Take(&receiptRow).Error)
	receiptRow.ID = string(organizingIntegrationID(74003))
	receiptRow.Payload = append(receiptRow.Payload, ' ')
	if err := f.db.Create(&receiptRow).Error; err == nil {
		t.Fatal("corrupt receipt payload inserted")
	}

	t.Run("conflict remains recoverable and cannot seal", func(t *testing.T) {
		conflicting := original + "\n人工在相同末尾追加，不能静默裁决。\n"
		check(os.WriteFile(filepath.Join(f.root, target), []byte(conflicting), 0600))
		c := command
		c.IdempotencyKey = "manual-conflict"
		pending, err := store.Prepare(ctx, c)
		check(err)
		if pending.Preview.Review == nil || pending.Preview.Manuscript != nil {
			t.Fatal("fixture must create a real Git conflict")
		}
		recovered, err := restarted.GetAttempt(ctx, f.workspace, pending.ID)
		check(err)
		if !reflect.DeepEqual(pending, recovered) {
			t.Fatal("conflict preview changed after restart")
		}
		if _, err := store.SealClean(ctx, f.workspace, pending.ID); err == nil {
			t.Fatal("conflict fallback became a sealed manuscript")
		}
	})
	t.Run("empty file is present and invalid captures are rejected", func(t *testing.T) {
		check(os.WriteFile(filepath.Join(f.root, target), nil, 0600))
		c := command
		c.IdempotencyKey = "empty-file"
		empty, err := store.Prepare(ctx, c)
		check(err)
		if !empty.Prepared.MergeInput.FileExists || empty.Prepared.MergeInput.FileContent != "" {
			t.Fatal("empty file became absence")
		}
		check(os.WriteFile(filepath.Join(f.root, target), []byte("bad\x00bytes"), 0600))
		c.IdempotencyKey = "nul-file"
		if _, err := store.Prepare(ctx, c); err == nil {
			t.Fatal("NUL captured")
		}
		check(os.WriteFile(filepath.Join(f.root, target), []byte(strings.Repeat("x", domain.MaxSynthesisManuscriptBytes+1)), 0600))
		c.IdempotencyKey = "oversized-file"
		if _, err := store.Prepare(ctx, c); err == nil {
			t.Fatal("oversized file captured")
		}
		check(os.Remove(filepath.Join(f.root, target)))
		c.IdempotencyKey = "missing-published-file"
		if _, err := store.Prepare(ctx, c); err == nil {
			t.Fatal("missing published file became absence")
		}
	})
	check(os.WriteFile(filepath.Join(f.root, target), []byte(file), 0600))

	t.Run("unpublished existing L freezes real absence without claiming another file", func(t *testing.T) {
		seed := f.generation(t, 75000, nil, "Another note's source.")
		seed.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(seed, "unpublished manual storage")}
		result, err := f.service.ApplyGeneration(ctx, seed.Input, seed.Generation)
		check(err)
		detail, err := f.service.GetNote(ctx, f.workspace, result.Publications[0].NoteID)
		check(err)
		latest := *detail.CurrentRevision
		proposed := f.generation(t, 76000, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: latest}}, "More evidence for unpublished note.")
		proposed.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(detail, proposed.Input.Sources[0].Reference)}
		added := []domain.SynthesisSourceRef{proposed.Input.Sources[0].Reference}
		delta, err := domain.ApplySynthesisDelta(f.workspace, latest.Items, proposed.Generation.Notes[0].Delta, added)
		check(err)
		var state authoringapp.GeneratedDocumentState
		check(f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			var err error
			state, err = f.authoring.ReadGeneratedDocumentScoped(ctx, scope, f.workspace, latest.DocumentID, latest.NoteID)
			return err
		}))
		old := proof.prepared
		defer func() { proof.prepared = old }()
		auth := old.Authority
		auth.DocumentID = latest.DocumentID
		auth.DocumentVersion = state.Document.Version
		auth.NoteVersion = detail.Note.Version
		auth.LatestArticleID = latest.ArticleRevisionID
		auth.PublishedPublicationID = ""
		auth.PublishedProposalCommitID = ""
		auth.PublishedGitCommit = ""
		auth.TargetPath = state.Document.CanonicalPath
		auth.Anchor.AllowedSources = added
		proof.prepared = app.SynthesisManuscriptPrepared{Authority: auth, GenerationInput: proposed.Input, Generation: proposed.Generation, SemanticModelRunID: seed.Generation.ModelRunID, SemanticOutputHash: organizingIntegrationHash("semantic fixture"), MergeInput: app.SynthesisManuscriptMergeInput{Latest: latest, NextMachine: domain.SynthesisManuscriptMachine{WorkspaceID: f.workspace, NoteID: latest.NoteID, MachineTitle: latest.Title, MachineItems: delta.Items, IneligibleItemIDs: []foundation.ID{}}}}
		c := app.PrepareSynthesisManuscript{WorkspaceID: f.workspace, NoteID: latest.NoteID, ProcessingID: proposed.Input.ProcessingID, IdempotencyKey: "real-absence"}
		absent, err := store.Prepare(ctx, c)
		check(err)
		if absent.Prepared.MergeInput.FileExists || absent.Prepared.MergeInput.FileContent != "" {
			t.Fatal("missing target captured as present")
		}
		check(os.WriteFile(filepath.Join(f.root, auth.TargetPath), nil, 0600))
		if _, err := store.SealClean(ctx, f.workspace, absent.ID); err == nil {
			t.Fatal("file created after absence was authorized")
		}
		c.IdempotencyKey = "existing-unpublished-path"
		if _, err := store.Prepare(ctx, c); err == nil {
			t.Fatal("unpublished target collision captured as ancestor")
		}
		check(os.Remove(filepath.Join(f.root, auth.TargetPath)))
	})
	t.Run("independently rehashed preview fails immutable reread", func(t *testing.T) {
		rollback := errors.New("rollback corruption fixture")
		err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			tx, err := platformpostgres.GORMTransaction(scope)
			if err != nil {
				return err
			}
			forged := attempt
			forged.Preview.Fingerprint = organizingIntegrationHash("forged preview")
			forged.Hash = ""
			forged.Hash, err = manuscriptValueHash(forged)
			if err != nil {
				return err
			}
			payload, err := encodeManuscriptRecord(forged)
			if err != nil {
				return err
			}
			if err = tx.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
				return err
			}
			if err = tx.Model(&manuscriptAttemptRow{}).Where("id=? AND workspace_id=?", string(attempt.ID), string(f.workspace)).Updates(map[string]any{"payload": payload, "payload_hash": manuscriptBytesHash(payload)}).Error; err != nil {
				return err
			}
			if _, _, err = store.readAttempt(ctx, tx, f.workspace, attempt.ID); err == nil {
				return errors.New("rehashed forged preview read as valid")
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
	})
	testPersistManuscriptRevision(t, f, store, attempt, receipt, revision, next)
	testPublishManuscriptRevision(t, f, attempt, receipt, revision)
}

func testPersistManuscriptRevision(t *testing.T, f *synthesisGitFixture, store *GORMSynthesisManuscriptStore, a app.SynthesisManuscriptAttempt, r app.SynthesisManuscriptReceipt, revision domain.SynthesisRevision, next app.SynthesisApplyRecord) {
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
	if err != nil || content != r.Manuscript.FullContent || len(reloaded.Items) != 0 {
		t.Fatal("v2 stored full content or empty trusted subset lost")
	}
}

func TestSynthesisManuscriptCaptureAbsenceShape(t *testing.T) {
	c := app.SynthesisManuscriptCapture{ID: organizingIntegrationID(79001), WorkspaceID: organizingIntegrationID(79002), TargetPath: "notes/example.md", RootGrantID: organizingIntegrationID(79003), RootFingerprint: organizingIntegrationHash("root"), WorkspaceBindingVersion: 1, Bytes: []byte{}, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	var err error
	c.AbsenceToken, err = authoringdomain.ComputeAbsenceToken(c.WorkspaceID, c.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateManuscriptCapture(c); err != nil {
		t.Fatal(err)
	}
	c.Exists = true
	if validateManuscriptCapture(c) == nil {
		t.Fatal("absence token authorized an empty existing file")
	}
	c.AbsenceToken = ""
	c.ContentHash = manuscriptBytesHash(nil)
	if err = validateManuscriptCapture(c); err != nil {
		t.Fatal(err)
	}
	c.Exists = false
	if validateManuscriptCapture(c) == nil {
		t.Fatal("empty file hash authorized absence")
	}
}

func testPublishManuscriptRevision(t *testing.T, f *synthesisGitFixture, attempt app.SynthesisManuscriptAttempt, receipt app.SynthesisManuscriptReceipt, revision domain.SynthesisRevision) {
	t.Helper()
	ctx := t.Context()
	target := attempt.Prepared.Authority.TargetPath
	originalF := attempt.Prepared.MergeInput.FileContent
	// 在最终回滚的事务中验证 P 变化后的预留恢复；不可变历史证明始终保留。
	rollback := errors.New("rollback P drift")
	err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err = tx.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
			return err
		}
		if err = tx.Exec("UPDATE core.document SET current_published_revision_id=NULL WHERE id=? AND workspace_id=?", string(revision.DocumentID), string(f.workspace)).Error; err != nil {
			return err
		}
		// 独立的 Authoring 回调无法观察未提交变更；因此在当前作用域内验证精确预留恢复。
		if err = tx.Exec("SET LOCAL session_replication_role = origin").Error; err != nil {
			return err
		}
		if err = tx.SavePoint("stale_p").Error; err != nil {
			return err
		}
		hash, err := authoringdomain.ComputePublishRequestHash(f.workspace, revision.DocumentID, revision.ArticleRevisionID)
		if err != nil {
			return err
		}
		_, err = f.authoring.ReservePublicationScoped(ctx, scope, authoringapp.ReservePublicationRecord{Binding: authoringapp.PublishBinding{WorkspaceID: f.workspace, DocumentID: revision.DocumentID, RevisionID: revision.ArticleRevisionID, IdempotencyKey: "synthesis-publish:" + string(revision.ID), RequestHash: hash}, ReservationID: organizingIntegrationID(79990), ReservedAt: time.Now().UTC()})
		if err == nil {
			return errors.New("stale published pointer accepted")
		}
		if err = tx.RollbackTo("stale_p").Error; err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	// 回执已提交；创建 Proposal 时仍须检查实际 F。
	if err := os.WriteFile(filepath.Join(f.root, target), []byte(originalF+" changed after review"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.RecoverAppliedGeneration(ctx, f.workspace, attempt.Command.ProcessingID); err == nil {
		t.Fatal("changed F accepted for Proposal")
	}
	if err := os.WriteFile(filepath.Join(f.root, target), []byte(originalF), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.service.RecoverAppliedGeneration(ctx, f.workspace, attempt.Command.ProcessingID); err != nil || !found {
		t.Fatalf("recover merged publication: found=%t err=%v", found, err)
	}
	detail, err := f.service.GetNote(ctx, f.workspace, revision.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := f.proposals.GetProposal(ctx, detail.Publication.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Revision.BaseHash != manuscriptBytesHash([]byte(originalF)) || proposal.Revision.BaseHash == attempt.Prepared.MergeInput.Published.ContentHash || proposal.Revision.Content != receipt.Manuscript.FullContent {
		t.Fatal("Proposal did not preserve separate F/P baselines")
	}
	if err = f.authoring.ValidatePublicationWriteback(ctx, f.workspace, proposal.ID, proposal.Revision.ID); err != nil {
		t.Fatal(err)
	}
	// 既有审批要求 Git 工作区干净，继续保留此门禁：拒绝未提交的人工修改，用户须在审批前提交 F。
	if _, err := f.changes.DecideProposal(ctx, proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, changecontroldomain.DecisionApproved); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "GIT_REPOSITORY_DIRTY") {
		t.Fatalf("uncommitted manual file approval=%v", err)
	}
	synthesisGit(t, ctx, f.root, "add", "--", target)
	synthesisGit(t, ctx, f.root, "commit", "-m", "preserve manual manuscript edit before approval")
	prepared := f.preparePublication(t, detail)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	if err != nil {
		t.Fatal(err)
	}
	f.assertPublished(t, detail, target, written)
	var status string
	if err = f.db.Raw("SELECT status FROM core.article_revision WHERE id=?", string(attempt.Prepared.MergeInput.Published.ArticleRevisionID)).Scan(&status).Error; err != nil || status != "SUPERSEDED" {
		t.Fatalf("old P status=%s err=%v", status, err)
	}
	if _, found, err := f.service.RecoverAppliedGeneration(ctx, f.workspace, attempt.Command.ProcessingID); err != nil || !found {
		t.Fatalf("published exact replay: %t %v", found, err)
	}
	history, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, revision.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := history.Content()
	if err != nil || content != receipt.Manuscript.FullContent || history.ContentHash != written.ResultHash {
		t.Fatal("reopened publication differs from reviewed full manuscript")
	}
}
