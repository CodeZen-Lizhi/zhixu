//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"

	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changedomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"

	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func newCandidateRemergeFixture(t *testing.T) (*synthesisGitFixture, app.SynthesisNoteDetail) {
	t.Helper()
	f := newSynthesisGitFixtureWithRootBinding(t, 125, true)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	initial := f.generation(t, 61000, nil, "Scheduling is workload dependent.")
	initial.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(initial, "candidate remerge scheduling")}
	created, err := f.service.ApplyGeneration(ctx, initial.Input, initial.Generation)
	check(err)
	first, err := f.service.GetNote(ctx, f.workspace, created.Publications[0].NoteID)
	check(err)
	prepared := f.preparePublication(t, first)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	check(err)
	path, err := authoringdomain.DefaultGeneratedTargetPath(first.Note.ID)
	check(err)
	f.assertPublished(t, first, path, written)
	published, err := f.service.GetNote(ctx, f.workspace, first.Note.ID)
	check(err)
	pending := f.generation(t, 62500, []app.SynthesisGenerationNote{{Note: published.Note, Revision: *published.CurrentRevision}}, "Pending scheduling evidence.")
	pending.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(published, pending.Input.Sources[0].Reference)}
	_, err = f.service.ApplyGeneration(ctx, pending.Input, pending.Generation)
	check(err)
	draft, err := f.service.GetNote(ctx, f.workspace, first.Note.ID)
	check(err)
	bytes, err := os.ReadFile(filepath.Join(f.root, path))
	check(err)
	check(os.WriteFile(filepath.Join(f.root, path), append([]byte("<!-- User transaction annotation: keep verbatim. -->\n\n"), bytes...), 0600))
	// 复用真实 source-ready、River、模型日志和手稿流水线；仅提供方输出固定，不伪造业务状态。
	runManuscriptRefreshThroughRiver(t, f, app.SynthesisBodyRefreshRequest{}, draft, initial.Input.Sources[0].Reference, initial.Generation.ModelRunID, "unanchored")
	result, err := f.service.GetNote(ctx, f.workspace, first.Note.ID)
	check(err)
	return f, result
}

func candidateRemergeOwner(t *testing.T, f *synthesisGitFixture) (*SynthesisManuscriptRuntime, app.SynthesisCandidateRemergeOwner) {
	t.Helper()
	check := func(e error) {
		t.Helper()
		if e != nil {
			for cause := e; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(e)
		}
	}
	rootStore, err := rootgrant.NewGORMAuthoritativeStore(f.platform, rootgrant.RuntimeGrantDirect)
	check(err)
	resolver, err := rootgrant.NewDirectRootGrantResolver(rootStore)
	check(err)
	t.Cleanup(func() { _ = resolver.Close() })
	workspaceOwner, err := workspacepostgres.NewGORMRepository(f.platform)
	check(err)
	files, err := localfs.NewReader(workspaceOwner)
	check(err)
	roots, err := organizingowner.NewSynthesisManuscriptRoot(resolver, files)
	check(err)
	merger, err := gitmerge.New(gitcli.New(""))
	check(err)
	creator, err := authoringchange.NewProposalCreator(f.changes, files)
	check(err)
	publications, err := authoringapp.NewService(authoringapp.Dependencies{Repository: f.authoring, Proposals: creator, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
	check(err)
	// 重合并刻意不依赖模型或执行上下文；只构建其生产所属模块端口，并使用同一真实根目录和 Authoring 服务。
	runtime := &SynthesisManuscriptRuntime{dependencies: SynthesisManuscriptRuntimeDependencies{Pool: f.platform, Candidates: f.store, Roots: roots, Service: app.SynthesisDependencies{Publications: publications}, Storage: SynthesisManuscriptStoreDependencies{Files: roots, Mapper: manuscript.Mapper{}, Merge: merger, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}}}
	owner, err := runtime.CandidateRemerge(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
	check(err)
	return runtime, owner
}
func candidateRemergeCommand(t *testing.T, f *synthesisGitFixture, d app.SynthesisNoteDetail, key string) app.BeginSynthesisCandidateRemerge {
	t.Helper()
	_, owner := candidateRemergeOwner(t, f)
	target, err := owner.Target(t.Context(), f.workspace, d.Note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target.ExpectedRevisionID != d.CurrentRevision.ID || target.ExpectedNoteVersion != d.Note.Version {
		t.Fatal("target current note differs")
	}
	out := app.BeginSynthesisCandidateRemerge{WorkspaceID: target.WorkspaceID, NoteID: target.NoteID, ExpectedRevisionID: target.ExpectedRevisionID, ExpectedNoteVersion: target.ExpectedNoteVersion, ExpectedDocumentID: target.ExpectedDocumentID, ExpectedDocumentVersion: target.ExpectedDocumentVersion, ExpectedPublicationID: target.ExpectedPublicationID, ExpectedProposalID: target.ExpectedProposalID, ExpectedProposalRevisionID: target.ExpectedProposalRevisionID, ExpectedProposalVersion: target.ExpectedProposalVersion, IdempotencyKey: key}
	return out
}
func TestSynthesisCandidateRemergePostgreSQLGit(t *testing.T) {
	f, r1 := newCandidateRemergeFixture(t)
	runtime, owner := candidateRemergeOwner(t, f)
	ctx := t.Context()
	check := func(e error) {
		t.Helper()
		if e != nil {
			for cause := e; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(e)
		}
	}
	path, _ := authoringdomain.DefaultGeneratedTargetPath(r1.Note.ID)
	absolute := filepath.Join(f.root, path)
	f0, err := os.ReadFile(absolute)
	check(err)
	// 捕获快照包括不可变的处理、模型、回执及历史记录行。
	snapshot := func() string {
		var v string
		check(f.db.Raw(`SELECT jsonb_build_object('processing',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM organizing.synthesis_processing p),'model',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM agent.model_run m),'receipt',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM organizing.synthesis_manuscript_receipt r),'apply',(SELECT jsonb_agg(to_jsonb(a) ORDER BY processing_id) FROM organizing.synthesis_apply_receipt a))::text`).Scan(&v).Error)
		return v
	}
	before := snapshot()
	f1 := append([]byte("<!-- Later workspace annotation: retain. -->\n\n"), f0...)
	check(os.WriteFile(absolute, f1, 0600))
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "User workspace edit before remerge")
	var eventsBefore, capturesBefore int64
	check(f.db.Table("organizing.synthesis_candidate_remerge_event").Count(&eventsBefore).Error)
	check(f.db.Table("organizing.synthesis_manuscript_capture").Count(&capturesBefore).Error)
	c := candidateRemergeCommand(t, f, r1, "remerge-clean")
	var eventsAfter, capturesAfter int64
	check(f.db.Table("organizing.synthesis_candidate_remerge_event").Count(&eventsAfter).Error)
	check(f.db.Table("organizing.synthesis_manuscript_capture").Count(&capturesAfter).Error)
	if eventsAfter != eventsBefore || capturesAfter != capturesBefore {
		t.Fatal("Target persisted merge state")
	}
	if _, err = runtime.CandidateRemerge(app.SynthesisManuscriptCaller{}); err == nil {
		t.Fatal("missing permissions accepted")
	}
	forged := c
	forged.ExpectedProposalRevisionID = organizingIntegrationID(99001)
	if _, err = owner.Begin(ctx, forged); err == nil {
		t.Fatal("forged binding accepted")
	}
	if _, err = owner.Target(ctx, f.otherWorkspace, r1.Note.ID); err == nil {
		t.Fatal("cross workspace target accepted")
	}
	cross := c
	cross.WorkspaceID = f.otherWorkspace
	if _, err = owner.Begin(ctx, cross); err == nil {
		t.Fatal("cross workspace accepted")
	}
	review, err := owner.Begin(ctx, c)
	check(err)
	if review.State != "READY" || review.Candidate == "" || !strings.Contains(review.Candidate, "Later workspace annotation: retain.") {
		t.Fatalf("clean preview: %+v", review)
	}
	restored, err := owner.Read(ctx, app.ReadSynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, IdempotencyKey: c.IdempotencyKey})
	check(err)
	if restored.AttemptID != review.AttemptID || restored.Candidate != review.Candidate {
		t.Fatal("GET lost attempt")
	}
	replay, err := owner.Begin(ctx, c)
	check(err)
	if !replay.Replayed || replay.AttemptID != review.AttemptID || replay.Candidate != review.Candidate {
		t.Fatal("begin replay differs")
	}
	apply := app.ApplySynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, AttemptID: review.AttemptID, IdempotencyKey: "apply-clean"}
	// 第二次文件变化不得使 P1 退役，也不得生成 R2。
	check(os.WriteFile(absolute, append(append([]byte{}, f1...), []byte("\nDrift again\n")...), 0600))
	if _, err = owner.Apply(ctx, apply); err == nil {
		t.Fatal("second file drift accepted")
	}
	current, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	if current.CurrentRevision.ID != r1.CurrentRevision.ID || current.Publication.ProposalID != r1.Publication.ProposalID {
		t.Fatal("failed apply retired original")
	}
	check(os.WriteFile(absolute, f1, 0600))
	oldProposal, err := f.proposals.GetProposal(ctx, c.ExpectedProposalID)
	check(err)
	approvalErrors := make(chan error, 1)
	go func() {
		_, e := f.changes.DecideProposal(ctx, oldProposal.ID, oldProposal.Revision.ID, oldProposal.Revision.ChangeHash, changedomain.DecisionApproved)
		approvalErrors <- e
	}()
	applied, err := owner.Apply(ctx, apply)
	if oldErr := <-approvalErrors; oldErr == nil {
		t.Fatal("concurrent stale old approval succeeded")
	}
	if err != nil {
		// 旧审批失败时，仍可能赢得版本 CAS，但不创建 Approval。
		refreshed, e := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
		check(e)
		next := candidateRemergeCommand(t, f, refreshed, "after-concurrent-old-rejection")
		review, e = owner.Begin(ctx, next)
		check(e)
		apply.AttemptID = review.AttemptID
		apply.IdempotencyKey = "apply-after-old-rejection"
		applied, err = owner.Apply(ctx, apply)
	}
	check(err)
	if applied.State != "APPLIED" || applied.Result == nil || applied.Result.ProposalID == "" {
		t.Fatalf("missing new candidate: %+v", applied)
	}
	replay, err = owner.Apply(ctx, apply)
	check(err)
	if !replay.Replayed || !reflect.DeepEqual(applied.Result, replay.Result) {
		t.Fatal("apply replay changed candidate")
	}
	r2, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	if r2.CurrentRevision.Manuscript == nil || r2.CurrentRevision.Manuscript.FullContent != review.Candidate || applied.Candidate != "" {
		t.Fatal("READY preview differs from applied full manuscript")
	}
	if r2.CurrentRevision.Remerge == nil || r2.CurrentRevision.Remerge.SourceRevisionID != r1.CurrentRevision.ID || r2.CurrentRevision.Manuscript == nil || !strings.Contains(r2.CurrentRevision.Manuscript.FullContent, "Later workspace annotation") || !strings.Contains(r2.CurrentRevision.Manuscript.FullContent, "User transaction annotation") {
		t.Fatal("remerge lost provenance or full human text")
	}
	if before != snapshot() {
		t.Fatal("remerge modified processing/model/receipt history")
	}
	bytes, err := os.ReadFile(absolute)
	check(err)
	if string(bytes) != string(f1) {
		t.Fatal("candidate remerge published a file")
	}
	old, err := f.service.GetSynthesisRevision(ctx, f.workspace, r1.Note.ID, r1.CurrentRevision.ID)
	check(err)
	original, _ := json.Marshal(r1.CurrentRevision)
	retained, _ := json.Marshal(old)
	if string(original) != string(retained) {
		t.Fatal("original revision changed")
	}
	prepared := f.preparePublication(t, r2)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	check(err)
	f.assertPublished(t, r2, path, written)
}

func TestSynthesisCandidateRemergeConflictPostgreSQLGit(t *testing.T) {
	f, r1 := newCandidateRemergeFixture(t)
	runtime, owner := candidateRemergeOwner(t, f)
	ctx := t.Context()
	check := func(e error) {
		t.Helper()
		if e != nil {
			for cause := e; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(e)
		}
	}
	path, _ := authoringdomain.DefaultGeneratedTargetPath(r1.Note.ID)
	absolute := filepath.Join(f.root, path)
	base, err := os.ReadFile(absolute)
	check(err)
	current := "# Human workspace rewrite\n\nKeep this changed workspace paragraph.\n"
	check(os.WriteFile(absolute, []byte(current), 0600))
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "User conflicting workspace edit")
	c := candidateRemergeCommand(t, f, r1, "conflict")
	preview, err := owner.Begin(ctx, c)
	check(err)
	if preview.State != "CONFLICTS" || preview.Review == nil || len(preview.Review.Conflicts) == 0 {
		t.Fatalf("missing conflicts: %+v", preview)
	}
	if preview.Review.Base != string(base) || preview.Review.Current != current || preview.Review.Proposed != r1.CurrentRevision.Manuscript.FullContent {
		t.Fatal("merge replaced complete candidate with machine-only content")
	}
	read, err := owner.Read(ctx, app.ReadSynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, IdempotencyKey: c.IdempotencyKey})
	check(err)
	if !reflect.DeepEqual(preview.Review, read.Review) || preview.PreviewFingerprint != read.PreviewFingerprint {
		t.Fatal("refresh changed conflict ordinals")
	}
	command := app.ApplySynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, AttemptID: preview.AttemptID, IdempotencyKey: "conflict-resolution"}
	if _, err = owner.Apply(ctx, command); err == nil {
		t.Fatal("conflict automatically sealed")
	}
	command.Resolution = &app.SynthesisManuscriptResolution{Stage: preview.Review.Stage, PreviewFingerprint: preview.PreviewFingerprint, FinalContent: current + "\n" + preview.Review.Proposed}
	for _, conflict := range preview.Review.Conflicts {
		command.Resolution.AcknowledgedOrdinals = append(command.Resolution.AcknowledgedOrdinals, conflict.Ordinal)
	}
	bad := command
	bad.Resolution = &app.SynthesisManuscriptResolution{Stage: command.Resolution.Stage, PreviewFingerprint: command.Resolution.PreviewFingerprint, FinalContent: command.Resolution.FinalContent, AcknowledgedOrdinals: append([]int{}, command.Resolution.AcknowledgedOrdinals...)}
	bad.Resolution.AcknowledgedOrdinals[0]++
	if _, err = owner.Apply(ctx, bad); err == nil {
		t.Fatal("unacknowledged ordinal accepted")
	}
	// 两个独立开始的重合并操作竞争同一个原始候选。
	c2 := c
	c2.IdempotencyKey = "conflict-competing"
	preview2, err := owner.Begin(ctx, c2)
	check(err)
	command2 := command
	command2.AttemptID = preview2.AttemptID
	command2.IdempotencyKey = "competing-resolution"
	type outcome struct {
		command app.ApplySynthesisCandidateRemerge
		result  app.SynthesisCandidateRemergeReview
		err     error
	}
	runtime.dependencies.Service.Publications = &candidateRemergeLostResponse{publisher: runtime.dependencies.Service.Publications}
	outcomes := make(chan outcome, 2)
	for _, candidate := range []app.ApplySynthesisCandidateRemerge{command, command2} {
		go func(c app.ApplySynthesisCandidateRemerge) { r, e := owner.Apply(ctx, c); outcomes <- outcome{c, r, e} }(candidate)
	}
	var winner outcome
	success := 0
	for range 2 {
		o := <-outcomes
		if o.result.State == "APPLIED" {
			success++
			winner = o
		}
	}
	if success != 1 {
		t.Fatalf("concurrent remerge success=%d", success)
	}
	key := c.IdempotencyKey
	if winner.command.AttemptID == preview2.AttemptID {
		key = c2.IdempotencyKey
	}
	recovered, e := owner.Read(ctx, app.ReadSynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, IdempotencyKey: key})
	check(e)
	if recovered.State != "APPLIED" || recovered.Result == nil || recovered.Result.ProposalID == "" {
		t.Fatal("GET did not recover unknown publication response")
	}
	replay, err := owner.Apply(ctx, winner.command)
	check(err)
	if !replay.Replayed || !reflect.DeepEqual(replay.Result, recovered.Result) {
		t.Fatal("resolution replay changed result")
	}
	r2, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	if r2.CurrentRevision.Manuscript.FullContent != command.Resolution.FinalContent {
		t.Fatal("resolved human content changed")
	}
	f.count(t, "organizing.synthesis_candidate_remerge_event", "kind", "APPLY", 1)
	prepared := f.preparePublication(t, r2)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	check(err)
	f.assertPublished(t, r2, path, written)
}

func TestSynthesisCandidateRemergeOldApprovalPostgreSQLGit(t *testing.T) {
	f, r1 := newCandidateRemergeFixture(t)
	_, owner := candidateRemergeOwner(t, f)
	ctx := t.Context()
	check := func(e error) {
		t.Helper()
		if e != nil {
			for cause := e; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(e)
		}
	}
	path, _ := authoringdomain.DefaultGeneratedTargetPath(r1.Note.ID)
	absolute := filepath.Join(f.root, path)
	original, err := os.ReadFile(absolute)
	check(err)
	changed := append([]byte("Later user file change\n\n"), original...)
	check(os.WriteFile(absolute, changed, 0600))
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "User changed original candidate baseline")
	c := candidateRemergeCommand(t, f, r1, "before-old-approval")
	attempt, err := owner.Begin(ctx, c)
	check(err)
	proposal, err := f.proposals.GetProposal(ctx, c.ExpectedProposalID)
	check(err)
	if _, err = f.changes.DecideProposal(ctx, proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, changedomain.DecisionApproved); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT") {
		t.Fatalf("old R1 approval did not reject F1: %v", err)
	}
	disk, err := os.ReadFile(absolute)
	check(err)
	if string(disk) != string(changed) {
		t.Fatal("rejected old approval changed F1")
	}
	// 失败的旧审批处于未评审的 needs_revision 状态，可以用最新预期身份安全重合并；不执行普通提案追加。
	latest, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	fresh := candidateRemergeCommand(t, f, latest, "after-base-rejection")
	preview, err := owner.Begin(ctx, fresh)
	check(err)
	_, err = owner.Apply(ctx, app.ApplySynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, AttemptID: preview.AttemptID, IdempotencyKey: "apply-after-base-rejection"})
	check(err)
	r2, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	c2 := candidateRemergeCommand(t, f, r2, "before-real-approval")
	attempt, err = owner.Begin(ctx, c2)
	check(err)
	proposal, err = f.proposals.GetProposal(ctx, c2.ExpectedProposalID)
	check(err)
	_, err = f.changes.DecideProposal(ctx, proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, changedomain.DecisionApproved)
	check(err)
	if _, err = owner.Target(ctx, f.workspace, r1.Note.ID); err == nil {
		t.Fatal("reviewed target accepted")
	}
	if _, err = owner.Apply(ctx, app.ApplySynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, AttemptID: attempt.AttemptID, IdempotencyKey: "after-real-approval"}); err == nil {
		t.Fatal("reviewed candidate was retired")
	}
	f.count(t, "organizing.synthesis_candidate_remerge_event", "kind", "APPLY", 1)
}

// 仅在真实 Authoring 所属模块提交 P2 后返回未知结果；GET 和相同命令必须恢复该精确绑定，不新增模型调用。
type candidateRemergeLostResponse struct {
	publisher app.SynthesisPublicationPublisher
	once      sync.Once
}

func (p *candidateRemergeLostResponse) PublishArticleRevision(ctx context.Context, c authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
	result, err := p.publisher.PublishArticleRevision(ctx, c)
	if err != nil {
		return result, err
	}
	lost := false
	p.once.Do(func() { lost = true })
	if lost {
		return authoringapp.PublishResult{}, context.DeadlineExceeded
	}
	return result, nil
}

// 应用事务提交后、调用底层发布器前中断；重建后不保留原始 Apply 命令。
func TestSynthesisCandidateRemergeResumePostgreSQLGit(t *testing.T) {
	f, r1 := newCandidateRemergeFixture(t)
	runtime, owner := candidateRemergeOwner(t, f)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	path, err := authoringdomain.DefaultGeneratedTargetPath(r1.Note.ID)
	check(err)
	current := "# New workspace text\n\nA conflicting human rewrite.\n"
	check(os.WriteFile(filepath.Join(f.root, path), []byte(current), 0600))
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "Workspace edit before interrupted remerge")
	c := candidateRemergeCommand(t, f, r1, "resume-before-publisher")
	preview, err := owner.Begin(ctx, c)
	check(err)
	if preview.Review == nil {
		t.Fatal("expected conflict")
	}
	resume := app.ResumeSynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, AttemptID: preview.AttemptID}
	if _, err = owner.Resume(ctx, resume); err == nil {
		t.Fatal("unapplied attempt resumed")
	}
	resolution := &app.SynthesisManuscriptResolution{Stage: preview.Review.Stage, PreviewFingerprint: preview.PreviewFingerprint, FinalContent: current + "\n" + preview.Review.Proposed}
	for _, conflict := range preview.Review.Conflicts {
		resolution.AcknowledgedOrdinals = append(resolution.AcknowledgedOrdinals, conflict.Ordinal)
	}
	runtime.dependencies.Service.Publications = candidateRemergeBeforePublisher{}
	interrupted, err := owner.Apply(ctx, app.ApplySynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, AttemptID: preview.AttemptID, IdempotencyKey: "discarded-apply-key", Resolution: resolution})
	if err == nil || interrupted.State != "APPLIED" || interrupted.Result == nil || interrupted.Result.ProposalID != "" {
		t.Fatalf("wrong interruption: %+v %v", interrupted, err)
	}
	resolution = nil
	snapshot := func() string {
		var value string
		check(f.db.Raw(`SELECT jsonb_build_object('events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM organizing.synthesis_candidate_remerge_event e),'captures',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM organizing.synthesis_manuscript_capture c),'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM organizing.synthesis_revision r),'models',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM agent.model_run m),'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY processing_id) FROM organizing.synthesis_apply_receipt r))::text`).Scan(&value).Error)
		return value
	}
	before := snapshot()
	var reservationID string
	check(f.db.Raw(`SELECT id FROM authoring.document_publication_reservation WHERE workspace_id=? AND article_revision_id=?`, string(f.workspace), string(interrupted.Result.ArticleRevisionID)).Scan(&reservationID).Error)
	if reservationID == "" {
		t.Fatal("missing committed reservation")
	}
	_, owner = candidateRemergeOwner(t, f)
	recovered, err := owner.Read(ctx, app.ReadSynthesisCandidateRemerge{WorkspaceID: f.workspace, NoteID: r1.Note.ID, IdempotencyKey: c.IdempotencyKey})
	check(err)
	if recovered.State != "APPLIED" || recovered.Result == nil || recovered.Result.ProposalID != "" || snapshot() != before {
		t.Fatal("GET wrote or lost committed candidate")
	}
	resume.AttemptID = recovered.AttemptID
	wrong := resume
	wrong.WorkspaceID = f.otherWorkspace
	if _, err = owner.Resume(ctx, wrong); err == nil {
		t.Fatal("cross-scope resume accepted")
	}
	result, err := owner.Resume(ctx, resume)
	check(err)
	if result.Result == nil || result.Result.ProposalID == "" || result.Result.RevisionID != interrupted.Result.RevisionID {
		t.Fatal("resume did not finish original candidate")
	}
	repeat, err := owner.Resume(ctx, resume)
	check(err)
	if !repeat.Replayed || !reflect.DeepEqual(repeat.Result, result.Result) || snapshot() != before {
		t.Fatal("resume created new merge/model facts")
	}
	var bindingReservation string
	check(f.db.Raw(`SELECT reservation_id FROM authoring.document_publication_binding WHERE id=?`, string(result.Result.PublicationID)).Scan(&bindingReservation).Error)
	if bindingReservation != reservationID {
		t.Fatal("resume replaced reservation")
	}
	var count int64
	check(f.db.Table("authoring.document_publication_reservation").Where("workspace_id=? AND article_revision_id=?", string(f.workspace), string(result.Result.ArticleRevisionID)).Count(&count).Error)
	if count != 1 {
		t.Fatal("duplicate reservation")
	}
	r2, err := f.service.GetNote(ctx, f.workspace, r1.Note.ID)
	check(err)
	prepared := f.preparePublication(t, r2)
	written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
	check(err)
	f.assertPublished(t, r2, path, written)
}

type candidateRemergeBeforePublisher struct{}

func (candidateRemergeBeforePublisher) PublishArticleRevision(context.Context, authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
	return authoringapp.PublishResult{}, context.DeadlineExceeded
}
