//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/changecontrol"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapp "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// Publication uses the production approval, authorization, audit, filesystem,
// Git, Saga and Authoring finalizer. Only source/model inputs and the running
// Workflow lease are fixture data; no approval, commit or published pointer is
// fabricated. Every filesystem write and git commit stays under t.TempDir().
func TestSynthesisPostgreSQLRealGitPublication(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 116)
	ctx := t.Context()
	first := f.generation(t, 54000, nil, "The scheduler manages runnable work. Fairness depends on workload. A version-specific limit remains unknown.")
	first.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(first, "scheduling publication")}
	ref := first.Input.Sources[0].Reference
	first.Generation.Notes[0].Delta.Operations = append(first.Generation.Notes[0].Delta.Operations,
		domain.SynthesisOperation{Kind: domain.SynthesisAddConflict, Item: &domain.SynthesisItem{ID: organizingIntegrationID(54030), Kind: domain.SynthesisConflictItem, Conflict: &domain.SynthesisConflictContent{Subject: "Scheduling fairness", Alternatives: []domain.SynthesisStatement{
			{Text: "Runnable tasks receive execution time.", Applicability: "Ordinary workload", Sources: []domain.SynthesisSourceRef{ref}},
			{Text: "Execution time can be delayed.", Applicability: "Saturated workload", Sources: []domain.SynthesisSourceRef{ref}},
		}}}},
		domain.SynthesisOperation{Kind: domain.SynthesisAddGap, Item: &domain.SynthesisItem{ID: organizingIntegrationID(54031), Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "Which runtime version changes the limit?", Context: "The source leaves the version-specific limit open.", Sources: []domain.SynthesisSourceRef{ref}}}},
	)
	created, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil || len(created.Publications) != 1 {
		t.Fatalf("create governed candidate: %+v %v", created, err)
	}
	noteID := created.Publications[0].NoteID
	pending, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil || pending.Publication == nil || pending.PublishedRevision != nil {
		t.Fatalf("pending candidate: %+v %v", pending, err)
	}
	target, err := authoringdomain.DefaultGeneratedTargetPath(noteID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, target)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved candidate touched a file: %v", err)
	}
	if head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD"); head != f.initialHead {
		t.Fatal("unapproved candidate created a git commit")
	}
	if _, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_NOTE_NOT_PUBLISHED") {
		t.Fatalf("unapproved candidate became interview material: %v", err)
	}

	if n, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 1); err != nil || n != 0 {
		t.Fatalf("pending candidate emitted a publication: %d %v", n, err)
	}
	firstExecution := f.preparePublication(t, pending)
	firstWritten, err := f.node.Execute(ctx, firstExecution.input, firstExecution.identity)
	if err != nil {
		t.Fatalf("first real writeback: %v", err)
	}
	f.assertPublished(t, pending, target, firstWritten)
	if replay, err := f.node.Execute(ctx, firstExecution.input, firstExecution.identity); err != nil || replay != firstWritten {
		t.Fatalf("writeback replay changed its commit: %+v %v", replay, err)
	}
	if count := synthesisGit(t, ctx, f.root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("first publication/replay commit count=%s", count)
	}
	firstContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil || ready.Note.Status != domain.SynthesisReady || ready.PublishedRevision == nil {
		t.Fatalf("first ready note: %+v %v", ready, err)
	}
	listed, err := f.store.ListSynthesisCandidates(ctx, f.workspace)
	if err != nil || len(listed) != 1 || listed[0].PublicationID == "" || listed[0].Revision.ID != ready.CurrentRevision.ID {
		t.Fatalf("published candidate body availability: %+v %v", listed, err)
	}
	publishedSource := listed[0]
	second := f.generation(t, 55000, []organizingapp.SynthesisGenerationNote{{Note: ready.Note, Revision: *ready.CurrentRevision}}, "Additional evidence for the existing scheduling fact.")
	second.Generation.Notes = []organizingapp.SynthesisGeneratedNote{extendedSynthesisNote(ready, second.Input.Sources[0].Reference)}
	if _, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation); err != nil {
		t.Fatalf("prepare governed replacement: %v", err)
	}
	replacement, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil || replacement.Publication == nil || replacement.PublishedRevision == nil || replacement.PublishedRevision.ID != pending.CurrentRevision.ID || replacement.CurrentRevision.RevisionNo != 2 {
		t.Fatalf("replacement lost the published baseline: %+v %v", replacement, err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != string(firstContent) {
		t.Fatalf("unapproved replacement changed the file: %v", err)
	}
	if snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); err != nil || snapshot.RevisionID != pending.CurrentRevision.ID {
		t.Fatalf("unapproved replacement changed interview material: %+v %v", snapshot, err)
	}
	secondExecution := f.preparePublication(t, replacement)
	secondWritten, err := f.node.Execute(ctx, secondExecution.input, secondExecution.identity)
	if err != nil {
		t.Fatalf("second real writeback: %v", err)
	}
	f.assertPublished(t, replacement, target, secondWritten)
	// 切换离开工作区不能抹去已提交的发布事实。
	if err := f.store.database.WithContext(ctx).Exec(`UPDATE core.workspace SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,clock_timestamp()) WHERE id=?`, string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	// 等两次发布都完成后再观察：被替代的首个版本仍须在有界批次中恰好发出一次。
	type scanResult struct {
		count int
		err   error
	}
	scans := make(chan scanResult, 2)
	for range 2 {
		go func() {
			n, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 1)
			scans <- scanResult{n, err}
		}()
	}
	total := 0
	for range 2 {
		r := <-scans
		if r.err != nil || r.count < 0 || r.count > 1 {
			t.Fatalf("concurrent publication scan: %+v", r)
		}
		total += r.count
	}
	if total < 1 || total > 2 {
		t.Fatalf("concurrent publication count=%d", total)
	}
	for i := total; i < 2; i++ {
		if n, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 1); err != nil || n != 1 {
			t.Fatalf("publication history batch %d: %d %v", i, n, err)
		}
	}
	if n, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 100); err != nil || n != 0 {
		t.Fatalf("replayed publication scan: %d %v", n, err)
	}
	var events []struct {
		ID      string
		Payload json.RawMessage
	}
	if err := f.store.database.WithContext(ctx).Raw(`SELECT id,payload FROM workflow.outbox_event
	 WHERE workspace_id=? AND event_type='organizing.synthesis.published'
	 ORDER BY occurred_at,payload->>'revision_id'`, string(f.workspace)).Scan(&events).Error; err != nil || len(events) != 2 {
		t.Fatalf("publication event read: %+v %v", events, err)
	}
	for i, revision := range []domain.SynthesisRevision{*pending.CurrentRevision, *replacement.CurrentRevision} {
		var payload map[string]string
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil || len(payload) != 10 ||
			payload["revision_id"] != string(revision.ID) || payload["note_id"] != string(noteID) ||
			payload["article_revision_id"] != string(revision.ArticleRevisionID) || payload["projection_hash"] != revision.Hash ||
			payload["content_hash"] != revision.ContentHash || payload["workspace_id"] != string(f.workspace) {
			t.Fatalf("publication event lost exact historical binding: %s %v", events[i].Payload, err)
		}
	}
	if err := f.store.database.WithContext(ctx).Exec(`UPDATE workflow.outbox_event SET published_at=clock_timestamp() WHERE id=?`, events[0].ID).Error; err != nil {
		t.Fatalf("publication event consumption acknowledgement: %v", err)
	}
	for _, mutation := range []string{
		`UPDATE workflow.outbox_event SET payload=jsonb_set(payload,'{note_id}',to_jsonb('forged'::text)) WHERE id=?`,
		`DELETE FROM workflow.outbox_event WHERE id=?`,
		`UPDATE workflow.outbox_event SET published_at=NULL WHERE id=?`,
	} {
		if err := f.store.database.WithContext(ctx).Exec(mutation, events[0].ID).Error; err == nil {
			t.Fatal("publication event mutation accepted")
		}
	}
	if secondWritten.GitCommit == firstWritten.GitCommit || synthesisGit(t, ctx, f.root, "rev-list", "--count", "HEAD") != "3" {
		t.Fatal("replacement did not make exactly one new commit")
	}
	history, err := f.service.GetSynthesisRevision(ctx, f.workspace, noteID, pending.CurrentRevision.ID)
	if err != nil || !reflect.DeepEqual(history, *pending.CurrentRevision) || replacement.CurrentRevision.Items[0].ID != history.Items[0].ID || replacement.CurrentRevision.Items[0].Fact.Text != history.Items[0].Fact.Text {
		t.Fatalf("published history or stable item changed: %v", err)
	}

	if err := f.store.database.WithContext(ctx).Exec(`UPDATE core.workspace SET status='active',version=version+1,updated_at=GREATEST(updated_at,clock_timestamp()) WHERE id=?`, string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	// A human edit after approval and authorization must survive a resumed Saga.
	latest, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	third := f.generation(t, 56000, []organizingapp.SynthesisGenerationNote{{Note: latest.Note, Revision: *latest.CurrentRevision}}, "Further evidence pending user review.")
	third.Generation.Notes = []organizingapp.SynthesisGeneratedNote{extendedSynthesisNote(latest, third.Input.Sources[0].Reference)}
	if _, err := f.service.ApplyGeneration(ctx, third.Input, third.Generation); err != nil {
		t.Fatal(err)
	}
	drifted, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	thirdExecution := f.preparePublication(t, drifted)
	humanContent := "# Human change\n\nPreserve this edit after approval.\n"
	if err := os.WriteFile(path, []byte(humanContent), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.node.Execute(ctx, thirdExecution.input, thirdExecution.identity)
	var conflict *foundation.Error
	if !errors.As(err, &conflict) || conflict.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("changed approved baseline did not produce a version conflict: %v", err)
	}
	failed, err := f.proposals.GetWritebackExecution(ctx, thirdExecution.input.ExecutionID)
	if err != nil || failed.Status != changecontroldomain.WritebackStatusNeedsRevision {
		t.Fatalf("conflicting writeback has no visible needs-revision state: status=%s err=%v", failed.Status, err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != humanContent {
		t.Fatalf("human edit was overwritten: %v", err)
	}
	if synthesisGit(t, ctx, f.root, "rev-parse", "HEAD") != secondWritten.GitCommit {
		t.Fatal("failed writeback created a git commit")
	}
	f.count(t, "change_control.proposal_commit", "proposal_id", string(drifted.Publication.ProposalID), 0)
	if n, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 100); err != nil || n != 0 {
		t.Fatalf("failed writeback emitted a publication: %d %v", n, err)
	}
	if err := f.store.database.WithContext(ctx).Exec(`INSERT INTO workflow.outbox_event
	 (id,workspace_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at)
	 SELECT gen_random_uuid(),workspace_id,event_type,'forged-event','forged-event',schema_version,event_version,
	 jsonb_set(payload,'{revision_id}',to_jsonb(?::text)),occurred_at FROM workflow.outbox_event WHERE id=?`,
		string(drifted.CurrentRevision.ID), events[0].ID).Error; err == nil {
		t.Fatal("unpublished revision forged a publication event")
	}
	if snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); err != nil || snapshot.RevisionID != replacement.CurrentRevision.ID {
		t.Fatalf("failed candidate replaced the published snapshot: %+v %v", snapshot, err)
	}

	// 即使上游版本已被替代，且新候选写回失败，纳入关系仍保留精确的已发布快照。
	listed, err = f.store.ListSynthesisCandidates(ctx, f.workspace)
	if err != nil || len(listed) != 1 || listed[0].PublicationID != "" {
		t.Fatalf("unpublished current candidate offered as body evidence: %+v %v", listed, err)
	}
	// 普通本地条目中相同的措辞，不得静默消除随后明确请求的已发布正文纳入操作。
	plain := f.generation(t, 56500, []organizingapp.SynthesisGenerationNote{publishedSource}, "Local scheduling context.")
	plain.Input.Sources = append(plain.Input.Sources, first.Input.Sources...)
	plainNote := f.newTopic(plain, "application scheduling")
	plainItem := publishedSource.Revision.Items[0]
	plainItem.ID = organizingIntegrationID(56530)
	plainNote.Delta.Operations = []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &plainItem}}
	plain.Generation.Notes = []organizingapp.SynthesisGeneratedNote{plainNote}
	plainResult, err := f.service.ApplyGeneration(ctx, plain.Input, plain.Generation)
	if err != nil || len(plainResult.Publications) != 1 {
		t.Fatalf("ordinary local item: %+v %v", plainResult, err)
	}
	plainDetail, err := f.service.GetNote(ctx, f.workspace, plainResult.Publications[0].NoteID)
	if err != nil || plainDetail.CurrentRevision == nil {
		t.Fatalf("ordinary candidate: %+v %v", plainDetail, err)
	}
	inclusion := f.generation(t, 57000, []organizingapp.SynthesisGenerationNote{publishedSource, {Note: plainDetail.Note, Revision: *plainDetail.CurrentRevision}}, "An application note needs scheduling context.")
	inclusion.Input.Sources = append(inclusion.Input.Sources, first.Input.Sources...)
	included := supportedSynthesisNote(plainDetail, inclusion.Input.Sources[0].Reference)
	included.Delta.Operations = nil
	for i, item := range publishedSource.Revision.Items {
		op, err := domain.IncludeSynthesisPublishedItem(publishedSource.Revision, publishedSource.PublicationID, item.ID, organizingIntegrationID(57030+i))
		if err != nil {
			t.Fatal(err)
		}
		included.Delta.Operations = append(included.Delta.Operations, op)
	}
	inclusion.Generation.Notes = []organizingapp.SynthesisGeneratedNote{included}
	bound, err := f.service.ApplyGeneration(ctx, inclusion.Input, inclusion.Generation)
	if err != nil || len(bound.Publications) != 1 {
		t.Fatalf("include published body: %+v %v", bound, err)
	}
	downstream, err := f.service.GetNote(ctx, f.workspace, bound.Publications[0].NoteID)
	if err != nil || downstream.PublishedRevision != nil || downstream.CurrentRevision == nil {
		t.Fatalf("included candidate: %+v %v", downstream, err)
	}
	f.count(t, "organizing.synthesis_revision_body_reference", "revision_id", string(downstream.CurrentRevision.ID), 3)
	if downstream.CurrentRevision.RevisionNo != 2 || downstream.CurrentRevision.Items[0].ID != plainItem.ID || downstream.CurrentRevision.Items[0].BodyReference != nil {
		t.Fatal("explicit inclusion did not preserve the ordinary local item in a new version")
	}
	for _, item := range downstream.CurrentRevision.Items[1:] {
		if !domain.MatchesSynthesisBodyItem(item, publishedSource.Revision, publishedSource.PublicationID) {
			t.Fatal("included body changed original content/conditions/evidence")
		}
	}
	if replay, err := f.service.ApplyGeneration(ctx, inclusion.Input, inclusion.Generation); err != nil || !replay.Replayed || !reflect.DeepEqual(replay.RevisionIDs, bound.RevisionIDs) {
		t.Fatalf("body inclusion replay changed candidate: %+v %v", replay, err)
	}
	for _, mutation := range []string{
		`UPDATE organizing.synthesis_revision_body_reference SET upstream_item_id=item_id WHERE revision_id=?`,
		`DELETE FROM organizing.synthesis_revision_body_reference WHERE revision_id=?`,
	} {
		if err := f.store.database.WithContext(ctx).Exec(mutation, string(downstream.CurrentRevision.ID)).Error; err == nil {
			t.Fatal("immutable body reference mutation accepted")
		}
	}
	// 保留合法结构和原始证据，但伪造发布身份；数据库所属模块校验仍须拒绝整个候选。
	forged := f.generation(t, 58000, []organizingapp.SynthesisGenerationNote{publishedSource}, "Another context for scheduling.")
	forged.Input.Notes[0].PublicationID = organizingIntegrationID(58090)
	forged.Input.Sources = append(forged.Input.Sources, first.Input.Sources...)
	forgedNote := f.newTopic(forged, "forged inclusion")
	op, err := domain.IncludeSynthesisPublishedItem(publishedSource.Revision, forged.Input.Notes[0].PublicationID, publishedSource.Revision.Items[0].ID, organizingIntegrationID(58030))
	if err != nil {
		t.Fatal(err)
	}
	forgedNote.Delta.Operations = []domain.SynthesisOperation{op}
	forged.Generation.Notes = []organizingapp.SynthesisGeneratedNote{forgedNote}
	if _, err := f.service.ApplyGeneration(ctx, forged.Input, forged.Generation); err == nil {
		t.Fatal("forged published body accepted")
	}
	f.count(t, "organizing.synthesis_apply_receipt", "processing_id", string(forged.Input.ProcessingID), 0)
}

// 所有测试发布都经过 Approval 和真实 Git 写回；只有已发布且涉及明确纳入条目的变更，才形成持久化影响。
func TestSynthesisBodyImpactsFromRealPublications(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 117)
	ctx := t.Context()
	apply := func(record organizingapp.SynthesisApplyRecord) organizingapp.SynthesisNoteDetail {
		t.Helper()
		result, err := f.service.ApplyGeneration(ctx, record.Input, record.Generation)
		if err != nil || len(result.Publications) != 1 {
			t.Fatalf("apply candidate: %+v %v", result, err)
		}
		detail, err := f.service.GetNote(ctx, f.workspace, result.Publications[0].NoteID)
		if err != nil {
			t.Fatal(err)
		}
		return detail
	}
	publish := func(detail organizingapp.SynthesisNoteDetail) {
		t.Helper()
		prepared := f.preparePublication(t, detail)
		written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
		if err != nil {
			t.Fatal(err)
		}
		target, err := authoringdomain.DefaultGeneratedTargetPath(detail.Note.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.assertPublished(t, detail, target, written)
	}
	reconcile := func(want int) {
		t.Helper()
		if _, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if count, err := f.store.ReconcileSynthesisBodyImpacts(ctx, 100); err != nil || count != want {
			t.Fatalf("impact count=%d want=%d err=%v", count, want, err)
		}
	}
	initial := f.generation(t, 61000, nil, "Scheduling is workload dependent. The version-specific limit is unknown.")
	upstream := f.newTopic(initial, "upstream scheduler")
	gapID := organizingIntegrationID(61030)
	upstream.Delta.Operations = append(upstream.Delta.Operations, domain.SynthesisOperation{Kind: domain.SynthesisAddGap, Item: &domain.SynthesisItem{ID: gapID, Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "Which version changes the limit?", Context: "The version is unspecified.", Sources: []domain.SynthesisSourceRef{initial.Input.Sources[0].Reference}}}})
	initial.Generation.Notes = []organizingapp.SynthesisGeneratedNote{upstream}
	first := apply(initial)
	publish(first)
	listed, err := f.store.ListSynthesisCandidates(ctx, f.workspace)
	if err != nil || len(listed) != 1 || listed[0].PublicationID == "" {
		t.Fatalf("published source unavailable: %+v %v", listed, err)
	}
	published := listed[0]
	inclusion := f.generation(t, 62000, []organizingapp.SynthesisGenerationNote{published}, "Application scheduling context.")
	inclusion.Input.Sources = append(inclusion.Input.Sources, initial.Input.Sources...)
	downstreamNote := f.newTopic(inclusion, "downstream application")
	includedID := organizingIntegrationID(62030)
	op, err := domain.IncludeSynthesisPublishedItem(published.Revision, published.PublicationID, gapID, includedID)
	if err != nil {
		t.Fatal(err)
	}
	// 普通事实共享上游来源，但没有正文引用。
	downstreamNote.Delta.Operations[0].Item.Fact.Sources = initial.Generation.Notes[0].Delta.Operations[0].Item.Fact.Sources
	downstreamNote.Delta.Operations = append(downstreamNote.Delta.Operations, op)
	inclusion.Generation.Notes = []organizingapp.SynthesisGeneratedNote{downstreamNote}
	downstream := apply(inclusion)
	publish(downstream)
	downstreamPath, _ := authoringdomain.DefaultGeneratedTargetPath(downstream.Note.ID)
	before, err := os.ReadFile(filepath.Join(f.root, downstreamPath))
	if err != nil {
		t.Fatal(err)
	}
	// 较新的下游候选 C 待处理时，默认读取仍返回 P。上游发布后续版本后，已发布的 P 仍须作为影响基线；C 也作为可编辑基线保留，并接收相同观察。
	downstreamCandidate := f.generation(t, 62500, []organizingapp.SynthesisGenerationNote{{Note: downstream.Note, Revision: *downstream.CurrentRevision}}, "Pending downstream review.")
	downstreamCandidate.Generation.Notes = []organizingapp.SynthesisGeneratedNote{extendedSynthesisNote(downstream, downstreamCandidate.Input.Sources[0].Reference)}
	downstreamDraft := apply(downstreamCandidate)
	if downstreamDraft.PublishedRevision == nil || downstreamDraft.PublishedRevision.ID != downstream.CurrentRevision.ID || downstreamDraft.CurrentRevision.ID == downstream.CurrentRevision.ID {
		t.Fatalf("published base was not retained under a pending draft: %+v", downstreamDraft)
	}
	reconcile(0)
	// 对未引用事实的补源及新增无关条目不计入影响。
	unrelated := f.generation(t, 63000, []organizingapp.SynthesisGenerationNote{{Note: first.Note, Revision: *first.CurrentRevision}}, "New unrelated scheduling information.")
	unrelated.Generation.Notes = []organizingapp.SynthesisGeneratedNote{extendedSynthesisNote(first, unrelated.Input.Sources[0].Reference)}
	second := apply(unrelated)
	publish(second)
	reconcile(0)
	resolve := f.generation(t, 64000, []organizingapp.SynthesisGenerationNote{{Note: second.Note, Revision: *second.CurrentRevision}}, "Version 2 raises the workload limit.")
	changed := supportedSynthesisNote(second, resolve.Input.Sources[0].Reference)
	changed.Delta.Operations = []domain.SynthesisOperation{{Kind: domain.SynthesisResolveGap, TargetItemID: gapID, Resolution: &domain.SynthesisStatement{Text: "Version 2 raises the limit.", Applicability: "Version 2", Sources: []domain.SynthesisSourceRef{resolve.Input.Sources[0].Reference}}}}
	resolve.Generation.Notes = []organizingapp.SynthesisGeneratedNote{changed}
	third := apply(resolve)
	reconcile(0) // 草稿中的解决结论不是发布证据。
	publish(third)
	if _, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// 其他消费者的确认不能隐藏此事件。
	if err := f.store.database.WithContext(ctx).Exec(`UPDATE workflow.outbox_event SET published_at=clock_timestamp() WHERE workspace_id=? AND event_type='organizing.synthesis.published' AND published_at IS NULL`, string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		count int
		err   error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() { n, err := f.store.ReconcileSynthesisBodyImpacts(ctx, 1); results <- outcome{n, err} }()
	}
	total := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		total += result.count
	}
	if total < 1 || total > 2 {
		t.Fatalf("concurrent impacts=%d", total)
	}
	// 并发有界扫描可能选中同一待处理行。先完成剩余可见基线，再检查精确数量和重放结果。
	reconcile(2 - total)
	f.count(t, "organizing.synthesis_body_impact", "workspace_id", string(f.workspace), 2)
	reconcile(0)
	query := organizingapp.SynthesisBodyImpactQuery{WorkspaceID: f.workspace, NoteID: downstream.Note.ID, BaseRevisionID: downstream.CurrentRevision.ID, Limit: 1}
	impacts, err := f.service.ReadSynthesisBodyImpacts(ctx, query)
	if err != nil || len(impacts.Items) != 1 || impacts.NextAfterID != "" {
		t.Fatalf("read exact impacts: %+v %v", impacts, err)
	}
	impact := impacts.Items[0]
	if impact.DetectedAt.Location() != time.UTC {
		t.Fatal("body impact timestamp is not canonical UTC")
	}
	if impact.ItemID != includedID || impact.UpstreamItemID != gapID || impact.UpstreamRevisionID != first.CurrentRevision.ID || impact.UpstreamPublicationID != published.PublicationID || impact.PublishedRevisionID != third.CurrentRevision.ID || impact.Reason != "CONTENT_CHANGED" {
		t.Fatalf("impact bindings: %+v", impact)
	}
	draftImpacts, err := f.service.ReadSynthesisBodyImpacts(ctx, organizingapp.SynthesisBodyImpactQuery{WorkspaceID: f.workspace, NoteID: downstream.Note.ID, BaseRevisionID: downstreamDraft.CurrentRevision.ID, Limit: 1})
	if err != nil || len(draftImpacts.Items) != 1 || draftImpacts.Items[0].UpstreamPublicationID != impact.UpstreamPublicationID || draftImpacts.Items[0].PublishedRevisionID != impact.PublishedRevisionID {
		t.Fatalf("impact bindings for pending draft base: %+v %v", draftImpacts, err)
	}
	query.AfterID = impact.ID
	if page, err := f.service.ReadSynthesisBodyImpacts(ctx, query); err != nil || len(page.Items) != 0 {
		t.Fatalf("cursor replay: %+v %v", page, err)
	}
	query.WorkspaceID = organizingIntegrationID(64999)
	if _, err := f.service.ReadSynthesisBodyImpacts(ctx, query); !organizingIntegrationError(err, foundation.ErrorNotFound, organizingapp.ErrorCodeSynthesisNotFound) {
		t.Fatalf("cross-workspace impact read: %v", err)
	}
	for _, statement := range []string{
		`UPDATE organizing.synthesis_body_impact SET reason='ITEM_MISSING' WHERE id=?`,
		`DELETE FROM organizing.synthesis_body_impact WHERE id=?`,
		`INSERT INTO organizing.synthesis_body_impact(workspace_id,note_id,base_revision_id,item_id,upstream_note_id,upstream_revision_id,upstream_item_id,upstream_publication_id,publication_id,published_revision_id,event_id,reason) SELECT workspace_id,note_id,base_revision_id,item_id,upstream_note_id,upstream_revision_id,upstream_item_id,upstream_publication_id,upstream_publication_id,upstream_revision_id,event_id,reason FROM organizing.synthesis_body_impact WHERE id=?`,
	} {
		if err := f.store.database.WithContext(ctx).Exec(statement, string(impact.ID)).Error; err == nil {
			t.Fatalf("impact mutation accepted: %s", statement)
		}
	}
	current, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, downstream.Note.ID)
	if err != nil || current.RevisionID != downstream.CurrentRevision.ID {
		t.Fatalf("impact moved publication: %+v %v", current, err)
	}
	after, err := os.ReadFile(filepath.Join(f.root, downstreamPath))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("impact rewrote downstream file: %v", err)
	}
	f.count(t, "organizing.synthesis_body_impact", "workspace_id", string(f.workspace), 2)
}

type synthesisGitFixture struct {
	*synthesisDBFixture
	root, initialHead string
	definitionID      foundation.ID
	changes           *changecontrolapp.Service
	writeback         *changecontrolapp.WritebackService
	node              *changecontrolworkflow.Node
}

func newSynthesisGitFixture(t *testing.T) *synthesisGitFixture {
	t.Helper()
	return newSynthesisGitFixtureAtVersion(t, 95)
}

func newSynthesisGitFixtureAtVersion(t *testing.T, version int64) *synthesisGitFixture {
	return newSynthesisGitFixtureWithRootBinding(t, version, false)
}

func newSynthesisGitFixtureWithRootBinding(t *testing.T, version int64, rootBound bool) *synthesisGitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("real publication validation requires git")
	}
	f := &synthesisGitFixture{synthesisDBFixture: newSynthesisDBFixtureAtVersion(t, version), root: t.TempDir(), definitionID: organizingIntegrationID(53001)}
	if rootBound {
		canonical, err := filepath.EvalSymlinks(f.root)
		if err != nil {
			t.Fatal(err)
		}
		f.root = canonical
	}
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(f.root, ".gitignore"), []byte(".knowledge/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	synthesisGit(t, ctx, f.root, "init", "--initial-branch=main")
	synthesisGit(t, ctx, f.root, "config", "user.name", "Synthesis Integration")
	synthesisGit(t, ctx, f.root, "config", "user.email", "synthesis@example.invalid")
	synthesisGit(t, ctx, f.root, "config", "commit.gpgSign", "false")
	synthesisGit(t, ctx, f.root, "config", "core.hooksPath", os.DevNull)
	synthesisGit(t, ctx, f.root, "add", "--", ".gitignore")
	synthesisGit(t, ctx, f.root, "commit", "-m", "isolated synthesis baseline")
	f.initialHead = synthesisGit(t, ctx, f.root, "rev-parse", "HEAD")
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	f.workspace = organizingIntegrationID(53000)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceRecord := workspacedomain.Workspace{ID: f.workspace, Name: "Synthesis publication integration", RootPath: f.root,
		Git: workspacedomain.GitBaseline{RepositoryPath: f.root, Branch: "main", Head: f.initialHead, CheckedAt: now}, Status: workspacedomain.WorkspaceStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if rootBound {
		workspaceRecord.RootFingerprint = organizingIntegrationHash(f.root)
		workspaceRecord.BindingVersion = 1
		workspaceRecord.Availability = workspacedomain.WorkspaceAvailabilityAvailable
		workspaceRecord.AvailabilityCheckedAt = now
	}
	if _, err := workspaces.CreateWorkspace(ctx, workspaceRecord); err != nil {
		t.Fatal(err)
	}
	targets, err := changecontrollocalfs.NewReader(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	git, err := gitcli.NewWritebackClient(gitcli.New(""), workspaces)
	if err != nil {
		t.Fatal(err)
	}
	ids, clock := foundation.UUIDGenerator{}, foundation.SystemClock{}
	f.changes, err = changecontrolapp.NewService(f.proposals, ids, clock, targets, git)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := authoringchange.NewProposalCreator(f.changes, targets)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := authoringapp.NewService(authoringapp.Dependencies{Repository: f.authoring, IDs: ids, Clock: clock, Proposals: creator})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = organizingapp.NewSynthesisService(organizingapp.SynthesisDependencies{Store: f.store, Sources: f.sources, Publications: publisher, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := changecontrollocalfs.NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := changecontrollocalfs.NewWriter(workspaces, validator)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := workflowpostgres.NewGORMToolExecutionPolicySnapshot(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := workflowpostgres.NewGORMToolCallRecoveryFence(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	toolStore, err := toolpostgres.NewGORMRepository(f.platform, policy, recovery)
	if err != nil {
		t.Fatal(err)
	}
	auditService, err := toolsapp.NewTrustedWriteAuditService(registry, toolStore, ids, clock)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := toolchangecontrol.NewWritebackAuditRecorder(auditService)
	if err != nil {
		t.Fatal(err)
	}
	locker, err := gitoperation.NewPostgresLocker(f.platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := authoringchange.NewPublicationFinalizer(f.authoring, clock)
	if err != nil {
		t.Fatal(err)
	}
	f.writeback, err = changecontrolapp.NewWritebackService(changecontrolapp.WritebackServiceDependencies{Repository: f.proposals, Workspace: writer, Git: git, GitOperations: locker, Audit: audit, Publication: finalizer, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.node, err = changecontrolworkflow.NewNode(f.writeback)
	if err != nil {
		t.Fatal(err)
	}
	definition := changecontrolworkflow.RegisteredDefinition()
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES(?,?,?,?,?::jsonb,?)`, string(f.definitionID), string(f.workspace), definition.Key, definition.Version, string(graph), now).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

type synthesisPreparedPublication struct {
	input    changecontrolworkflow.Input
	identity changecontrolapp.WritebackResumeIdentity
}

func (f *synthesisGitFixture) preparePublication(t *testing.T, detail organizingapp.SynthesisNoteDetail) synthesisPreparedPublication {
	t.Helper()
	ctx := t.Context()
	if detail.Publication == nil {
		t.Fatal("candidate has no publication")
	}
	proposal, err := f.proposals.GetProposal(ctx, detail.Publication.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD")
	approval, err := f.changes.DecideProposal(ctx, proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, changecontroldomain.DecisionApproved)
	if err != nil || approval.ApprovedGitHead == nil || *approval.ApprovedGitHead != head {
		t.Fatalf("real proposal approval: %v", err)
	}
	ids := foundation.UUIDGenerator{}
	identity := changecontrolapp.WritebackResumeIdentity{WorkspaceID: f.workspace, DefinitionID: f.definitionID, DefinitionVersion: changecontrolworkflow.RegisteredDefinition().Version, DefinitionHash: changecontrolworkflow.RegisteredDefinition().GraphHash, NodeKey: changecontrolworkflow.SafeWritebackNodeKey, LeaseOwner: "synthesis-publication-test", LeaseFence: 1}
	for _, target := range []*foundation.ID{&identity.WorkflowRunID, &identity.NodeRunID, &identity.NodeAttemptID} {
		*target, err = ids.New()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	lease := now.Add(10 * time.Minute)
	// The fixed running node is the only Workflow fixture seam. Approval and
	// authorizations are persisted through their actual owners, as in the
	// existing direct-node Safe Writeback integration test.
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES(?,?,?,'running','{}',1,?,?)`, []any{string(identity.WorkflowRunID), string(f.workspace), string(f.definitionID), now, now}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at) VALUES(?,?,?,?,'running',1,'{}',?,1,1,1,?,?,2,?,?)`, []any{string(identity.NodeRunID), string(identity.WorkflowRunID), identity.NodeKey, changecontrolworkflow.SafeWritebackNodeKind, "synthesis-writeback-node:" + string(identity.NodeRunID), identity.LeaseOwner, lease, now, now}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at) VALUES(?,?,1,1,0,?,?,?,'running',?,?)`, []any{string(identity.NodeAttemptID), string(identity.NodeRunID), "synthesis-writeback-delivery:" + string(identity.NodeAttemptID), identity.LeaseOwner, lease, now, now}},
		{`UPDATE change_control.proposal SET workflow_run_id=?,version=version+1,updated_at=? WHERE id=? AND workflow_run_id IS NULL`, []any{string(identity.WorkflowRunID), now, string(proposal.ID)}},
		{`INSERT INTO change_control.proposal_revision_dispatch(workspace_id,proposal_id,revision_id,approval_id,workflow_run_id,created_at) VALUES(?,?,?,?,?,?)`, []any{string(f.workspace), string(proposal.ID), string(proposal.Revision.ID), string(approval.ID), string(identity.WorkflowRunID), now}},
	}
	for _, statement := range statements {
		if err := f.db.Exec(statement.sql, statement.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	issue := func(tool string, capability changecontroldomain.Capability) changecontroldomain.AuthorizationIssueResult {
		result, err := f.changes.IssueWriteAuthorization(ctx, changecontroldomain.AuthorizationIssue{WorkspaceID: f.workspace, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: approval.ID, ToolName: tool, Capability: capability, Scope: changecontroldomain.ExpectedAuthorizationScopeForTarget(proposal.TargetPath, proposal.Revision.TargetMode), IdempotencyKey: "synthesis-auth:" + tool + ":" + string(identity.WorkflowRunID), TTL: 2 * time.Minute})
		if err != nil {
			t.Fatalf("real writeback authorization: %v", err)
		}
		return result
	}
	writeAuth := issue("ApplyApprovedPatch", changecontroldomain.CapabilityWriteKnowledge)
	gitAuth := issue("CreateGitCommit", changecontroldomain.CapabilityGitWrite)
	begin, err := f.writeback.Begin(ctx, changecontrolapp.BeginWritebackCommand{WorkspaceID: f.workspace, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID, ProposalID: proposal.ID, LeaseOwner: identity.LeaseOwner, IdempotencyKey: "synthesis-writeback:" + string(identity.WorkflowRunID), WriteCredential: writeAuth.Credential, GitCredential: gitAuth.Credential, WriteAuthorizationKey: writeAuth.Authorization.IdempotencyKey, GitAuthorizationKey: gitAuth.Authorization.IdempotencyKey})
	if err != nil || begin.Status != changecontroldomain.WritebackStatusPrepared {
		t.Fatalf("real atomic writeback begin: status=%s err=%v", begin.Status, err)
	}
	return synthesisPreparedPublication{input: changecontrolworkflow.Input{SchemaVersion: changecontrolapp.SafeWritebackSchemaVersion, ExecutionID: begin.ExecutionID, WorkspaceID: f.workspace, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID}, identity: identity}
}

func (f *synthesisGitFixture) assertPublished(t *testing.T, detail organizingapp.SynthesisNoteDetail, target string, written changecontrolworkflow.Output) {
	t.Helper()
	ctx := t.Context()
	snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, detail.Note.ID)
	if err != nil {
		t.Fatalf("read proven published snapshot: %v", err)
	}
	want, err := domain.SynthesisSnapshotFromRevision(*detail.CurrentRevision)
	if err != nil || !reflect.DeepEqual(snapshot, want) || written.ResultHash != snapshot.ContentHash {
		t.Fatalf("published snapshot differs from the approved revision: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(f.root, target))
	if err != nil || authoringdomain.ComputeContentHash(string(content)) != snapshot.ContentHash {
		t.Fatalf("published file/hash mismatch: %v", err)
	}
	if head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD"); head != written.GitCommit {
		t.Fatalf("git HEAD does not match publication: %s", head)
	}
	committed := synthesisGitOutput(t, ctx, f.root, "show", written.GitCommit+":"+target)
	if committed != string(content) {
		t.Fatal("git contains different published content")
	}
	var count int64
	err = f.db.Raw(`SELECT count(*) FROM change_control.proposal_commit WHERE workspace_id=? AND proposal_id=? AND revision_id=? AND writeback_execution_id=? AND target_path=? AND result_hash=? AND git_commit=?`, string(f.workspace), string(detail.Publication.ProposalID), string(detail.Publication.ProposalRevisionID), string(written.ExecutionID), target, snapshot.ContentHash, written.GitCommit).Scan(&count).Error
	if err != nil || count != 1 {
		t.Fatalf("publication cannot be traced to its exact commit: count=%d err=%v", count, err)
	}
	f.count(t, "workflow.tool_call", "side_effect_id", string(written.ExecutionID), 2)
}

func supportedSynthesisNote(detail organizingapp.SynthesisNoteDetail, source domain.SynthesisSourceRef) organizingapp.SynthesisGeneratedNote {
	return organizingapp.SynthesisGeneratedNote{NoteID: detail.Note.ID, BaseRevisionID: detail.CurrentRevision.ID, TopicKey: detail.Note.TopicKey, Title: detail.Note.Title, Aliases: detail.Note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddSupport, TargetItemID: detail.CurrentRevision.Items[0].ID, Sources: []domain.SynthesisSourceRef{source}}}}}
}

func synthesisGit(t *testing.T, ctx context.Context, root string, arguments ...string) string {
	t.Helper()
	return strings.TrimSpace(synthesisGitOutput(t, ctx, root, arguments...))
}

func synthesisGitOutput(t *testing.T, ctx context.Context, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	// Like the production Git runner, discard process-level repository/index
	// overrides so even fixture setup cannot escape the temporary repository.
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && !strings.HasPrefix(name, "GIT_") && name != "LC_ALL" && name != "LANG" {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated git %v failed: %v: %s", arguments, err, output)
	}
	return string(output)
}

// 发布和退役场景需要真实正文变化；仅增加证据不再创建新候选，也不替代提案。
func extendedSynthesisNote(detail organizingapp.SynthesisNoteDetail, source domain.SynthesisSourceRef) organizingapp.SynthesisGeneratedNote {
	generated := supportedSynthesisNote(detail, source)
	item := domain.SynthesisItem{ID: source.SourceSpanID, Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "Additional finding from " + source.Title, Sources: []domain.SynthesisSourceRef{source}}}
	generated.Delta.Operations = append(generated.Delta.Operations, domain.SynthesisOperation{Kind: domain.SynthesisAddFact, Item: &item})
	return generated
}
