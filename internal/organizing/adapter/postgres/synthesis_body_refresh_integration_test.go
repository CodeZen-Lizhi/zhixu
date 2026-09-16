//go:build integration

package postgres

import (
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBodyRefreshGroupsPublishedAndCandidateImpacts(t *testing.T) {
	for _, mode := range []string{"admitted", "revoked", "scope_changed"} {
		t.Run(mode, func(t *testing.T) { testBodyRefreshGroupsPublishedAndCandidateImpacts(t, mode) })
	}
}

func testBodyRefreshGroupsPublishedAndCandidateImpacts(t *testing.T, mode string) {
	f := newSynthesisGitFixtureAtVersion(t, 134)
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
	resolved := apply(resolve)
	// 后续新来源 A 保留刷新后缺口的证据 B。发布来源为 A，而下游准入仅针对 B 及缺口的原始证据。
	provenanceA := f.generation(t, 64500, []organizingapp.SynthesisGenerationNote{{Note: resolved.Note, Revision: *resolved.CurrentRevision}}, "Unrelated scheduling provenance.")
	provenanceA.Generation.Notes = []organizingapp.SynthesisGeneratedNote{extendedSynthesisNote(resolved, provenanceA.Input.Sources[0].Reference)}
	third := apply(provenanceA)
	reconcile(0) // 草稿中的解决结论不是发布证据。
	publish(third)
	if _, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	reconcile(2)
	counts := make(chan int, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() { n, err := f.store.ReconcileSynthesisBodyRefreshRequests(ctx, 100); counts <- n; errors <- err }()
	}
	total := 0
	for range 2 {
		total += <-counts
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if total != 1 {
		t.Fatalf("requests=%d want 1", total)
	}
	var request organizingapp.SynthesisBodyRefreshRequest
	if err := f.store.database.WithContext(ctx).Raw("SELECT * FROM organizing.synthesis_body_refresh_request WHERE workspace_id=?", string(f.workspace)).Scan(&request).Error; err != nil {
		t.Fatal(err)
	}
	prepared, err := f.store.PrepareSynthesisBodyRefresh(ctx, f.workspace, request.ID)
	if err != nil || prepared.Target.Revision.ID != downstreamDraft.CurrentRevision.ID || len(prepared.Items) != 1 {
		t.Fatalf("latest editable preparation: %+v %v", prepared, err)
	}
	if prepared.Request.CreatedAt.Location() != time.UTC {
		t.Fatal("prepared request timestamp is not UTC")
	}
	if prepared.Items[0].Original.ID != first.CurrentRevision.ID || prepared.Items[0].Updated.ID != third.CurrentRevision.ID {
		t.Fatal("historical publication binding lost")
	}
	if n, err := f.store.ReconcileSynthesisBodyRefreshRequests(ctx, 100); err != nil || n != 0 {
		t.Fatalf("request replay=%d %v", n, err)
	}
	after, err := os.ReadFile(filepath.Join(f.root, downstreamPath))
	if err != nil || string(before) != string(after) {
		t.Fatalf("published content changed: %v", err)
	}
	runBodyRefreshThroughRiver(t, f, request, downstreamDraft, resolve.Input.Sources[0].Reference, resolve.Generation.ModelRunID, mode)
	if err := f.store.database.WithContext(ctx).Exec("UPDATE organizing.synthesis_body_refresh_request SET note_id=? WHERE id=?", string(first.Note.ID), string(request.ID)).Error; err == nil {
		t.Fatal("immutable request changed")
	}
}
