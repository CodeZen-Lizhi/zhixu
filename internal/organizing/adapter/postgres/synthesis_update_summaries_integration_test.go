//go:build integration

package postgres

import (
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"reflect"
	"testing"
)

func TestSynthesisUpdateSummariesCurrentRevisions(t *testing.T) {
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
	reconcile(2)
	query := organizingapp.SynthesisUpdateSummaryQuery{WorkspaceID: f.workspace, NoteIDs: []foundation.ID{downstream.Note.ID, third.Note.ID}}
	read := func() organizingapp.SynthesisUpdateSummaries {
		t.Helper()
		r, e := f.store.ReadSynthesisUpdateSummaries(ctx, query)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	result := read()
	if len(result.Items) != 2 || len(result.Items[0].Items) != 2 || len(result.Items[1].Items) != 1 {
		t.Fatalf("wrong P/C dedup %+v", result)
	}
	if result.Items[0].PublishedRevisionID != downstream.CurrentRevision.ID || result.Items[0].CurrentRevisionID != downstreamDraft.CurrentRevision.ID || result.Items[1].PublishedRevisionID != third.CurrentRevision.ID {
		t.Fatalf("wrong identities %+v", result)
	}
	for _, r := range result.Items[0].Items {
		if r.BodyReviewCount != 1 || r.SourceReviewCount != 0 {
			t.Fatalf("shared source invented body dependency %+v", r)
		}
	}
	if result.Items[1].Items[0].BodyReviewCount != 0 {
		t.Fatal("upstream acquired body impact")
	}
	ref := initial.Input.Sources[0].Reference
	if err := f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(ref.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 100); err != nil {
		t.Fatal(err)
	}
	result = read()
	for _, n := range result.Items {
		for _, r := range n.Items {
			detail, err := f.store.ReadSynthesisSourceImpacts(ctx, organizingapp.SynthesisSourceImpactQuery{WorkspaceID: f.workspace, NoteID: n.NoteID, RevisionID: r.RevisionID})
			if err != nil || r.SourceReviewCount != int64(len(detail.Items)) || r.SourceReviewCount != 1 {
				t.Fatalf("source relevance drift summary=%+v detail=%+v err=%v", r, detail, err)
			}
		}
	}
	if again := read(); !reflect.DeepEqual(again, result) {
		t.Fatal("repeat changed observations")
	}
	// 发布 C 后，P 成为历史版本；P 保留的账本不得叠加到 C。
	if err := f.db.Exec(`UPDATE core.source SET removed_at=NULL WHERE id=?`, string(ref.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	publish(downstreamDraft)
	result = read()
	if len(result.Items[0].Items) != 1 || result.Items[0].Items[0].RevisionID != downstreamDraft.CurrentRevision.ID || result.Items[0].Items[0].BodyReviewCount != 1 {
		t.Fatalf("historical base leaked %+v", result)
	}
	query.WorkspaceID = organizingIntegrationID(999001)
	if _, err := f.store.ReadSynthesisUpdateSummaries(ctx, query); err == nil {
		t.Fatal("cross-workspace notes accepted")
	}
	query.WorkspaceID = f.workspace
	query.NoteIDs = append(query.NoteIDs, query.NoteIDs[0])
	if _, err := f.store.ReadSynthesisUpdateSummaries(ctx, query); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
}
