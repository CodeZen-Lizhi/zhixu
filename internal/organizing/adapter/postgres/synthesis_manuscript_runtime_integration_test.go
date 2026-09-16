//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	authpostgres "github.com/CodeZen-Lizhi/zhixu/internal/auth/adapter/postgres"
	authapp "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	synthesispostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/synthesispostgres"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
)

func TestSynthesisManuscriptRuntimeBodyRefresh(t *testing.T) {
	for _, mode := range []string{"admitted", "audit_duplicate", "ordinary", "unanchored", "anchor_created", "source_changed", "root_changed", "binding_changed", "owner_changed", "scope_changed", "conflict", "human_unanchored", "human_two_stage", "human_cancel", "human_forged", "human_owner", "human_scope", "human_multi"} {
		t.Run(mode, func(t *testing.T) { testSynthesisManuscriptRuntime(t, mode) })
	}
}
func testSynthesisManuscriptRuntime(t *testing.T, mode string) {
	testSynthesisManuscriptRuntimeWithHook(t, mode, nil)
}
func testSynthesisManuscriptRuntimeWithHook(t *testing.T, mode string, sourceReview *sourceReviewRuntimeTestHook) {
	version := int64(134)
	f := newSynthesisGitFixtureWithRootBinding(t, version, true)
	ctx := t.Context()
	apply := func(record app.SynthesisApplyRecord) app.SynthesisNoteDetail {
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
	publish := func(detail app.SynthesisNoteDetail) {
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
	initial.Generation.Notes = []app.SynthesisGeneratedNote{upstream}
	first := apply(initial)
	publish(first)
	listed, err := f.store.ListSynthesisCandidates(ctx, f.workspace)
	if err != nil || len(listed) != 1 || listed[0].PublicationID == "" {
		t.Fatalf("published source unavailable: %+v %v", listed, err)
	}
	published := listed[0]
	inclusion := f.generation(t, 62000, []app.SynthesisGenerationNote{published}, "Application scheduling context.")
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
	inclusion.Generation.Notes = []app.SynthesisGeneratedNote{downstreamNote}
	downstream := apply(inclusion)
	publish(downstream)
	downstreamPath, _ := authoringdomain.DefaultGeneratedTargetPath(downstream.Note.ID)
	before, err := os.ReadFile(filepath.Join(f.root, downstreamPath))
	if err != nil {
		t.Fatal(err)
	}
	// 较新的下游候选 C 待处理时，默认读取仍返回 P。上游发布后续版本后，已发布的 P 仍须作为影响基线；C 也作为可编辑基线保留，并接收相同观察。
	downstreamCandidate := f.generation(t, 62500, []app.SynthesisGenerationNote{{Note: downstream.Note, Revision: *downstream.CurrentRevision}}, "Pending downstream review.")
	downstreamCandidate.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(downstream, downstreamCandidate.Input.Sources[0].Reference)}
	if sourceReview != nil && sourceReview.mode == "multi_obligation" {
		note := &downstreamCandidate.Generation.Notes[0]
		note.Delta.Operations = append(note.Delta.Operations, domain.SynthesisOperation{Kind: domain.SynthesisAddFact, Item: &domain.SynthesisItem{ID: organizingIntegrationID(62531), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "Second historical scheduling boundary.", Sources: []domain.SynthesisSourceRef{downstreamCandidate.Input.Sources[0].Reference}}}})
	}
	downstreamDraft := apply(downstreamCandidate)
	if downstreamDraft.PublishedRevision == nil || downstreamDraft.PublishedRevision.ID != downstream.CurrentRevision.ID || downstreamDraft.CurrentRevision.ID == downstream.CurrentRevision.ID {
		t.Fatalf("published base was not retained under a pending draft: %+v", downstreamDraft)
	}
	reconcile(0)
	// 对未引用事实的补源及新增无关条目不计入影响。
	unrelated := f.generation(t, 63000, []app.SynthesisGenerationNote{{Note: first.Note, Revision: *first.CurrentRevision}}, "New unrelated scheduling information.")
	unrelated.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(first, unrelated.Input.Sources[0].Reference)}
	second := apply(unrelated)
	publish(second)
	reconcile(0)
	resolve := f.generation(t, 64000, []app.SynthesisGenerationNote{{Note: second.Note, Revision: *second.CurrentRevision}}, "Version 2 raises the workload limit.")
	changed := supportedSynthesisNote(second, resolve.Input.Sources[0].Reference)
	changed.Delta.Operations = []domain.SynthesisOperation{{Kind: domain.SynthesisResolveGap, TargetItemID: gapID, Resolution: &domain.SynthesisStatement{Text: "Version 2 raises the limit.", Applicability: "Version 2", Sources: []domain.SynthesisSourceRef{resolve.Input.Sources[0].Reference}}}}
	resolve.Generation.Notes = []app.SynthesisGeneratedNote{changed}
	resolved := apply(resolve)
	// 后续新来源 A 保留刷新后缺口的证据 B。发布来源为 A，而下游准入仅针对 B 及缺口的原始证据。
	provenanceA := f.generation(t, 64500, []app.SynthesisGenerationNote{{Note: resolved.Note, Revision: *resolved.CurrentRevision}}, "Unrelated scheduling provenance.")
	provenanceA.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(resolved, provenanceA.Input.Sources[0].Reference)}
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
	var request app.SynthesisBodyRefreshRequest
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
	manual := "<!-- User transaction annotation: keep verbatim. -->\n\n" + string(before)
	if strings.HasPrefix(mode, "human_") && mode != "human_two_stage" {
		manual = "<!-- User transaction annotation: keep verbatim. -->\n\n# Entire human rewrite\nCompeting ordinary note body.\n"
	}
	if mode == "conflict" || mode == "human_two_stage" {
		manual = strings.Replace(manual, "待确认：", "人工问题：", 1)
	}
	if err := os.WriteFile(filepath.Join(f.root, downstreamPath), []byte(manual), 0600); err != nil {
		t.Fatal(err)
	}
	runManuscriptRefreshThroughRiver(t, f, request, downstreamDraft, resolve.Input.Sources[0].Reference, resolve.Generation.ModelRunID, mode, sourceReview)
	disk, err := os.ReadFile(filepath.Join(f.root, downstreamPath))
	if err != nil || (mode != "human_two_stage" || os.Getenv("ZHIXU_MANUSCRIPT_BROWSER_DIR") == "") && string(disk) != manual {
		t.Fatalf("candidate changed workspace file: %v", err)
	}
	if err := f.store.database.WithContext(ctx).Exec("UPDATE organizing.synthesis_body_refresh_request SET note_id=? WHERE id=?", string(first.Note.ID), string(request.ID)).Error; err == nil {
		t.Fatal("immutable request changed")
	}
}

func runManuscriptRefreshThroughRiver(t *testing.T, f *synthesisGitFixture, request app.SynthesisBodyRefreshRequest, before app.SynthesisNoteDetail, incoming domain.SynthesisSourceRef, seedModelID foundation.ID, mode string, sourceReviewHooks ...*sourceReviewRuntimeTestHook) {
	t.Helper()
	ctx := t.Context()
	unanchored := mode == "unanchored" || mode == "anchor_created" || mode == "human_unanchored" || mode == "human_cancel" || mode == "human_forged" || mode == "human_owner" || mode == "human_scope" || mode == "human_multi"
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	modelRuns, err := agentpostgres.NewGORMRepository(f.platform)
	check(err)
	migrator, err := riveradapter.NewMigrator(f.platform.DB())
	check(err)
	check(migrator.Up(ctx))
	check(migrator.Validate(ctx))
	anchors, err := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	check(err)
	var evidence []domain.SynthesisSourceRef
	if !unanchored {
		created, err := anchors.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "refresh-anchor", NoteID: before.Note.ID, ExpectedNoteVersion: before.Note.Version, BasisRevisionID: before.CurrentRevision.ID, Title: "Scheduling", Scope: domain.AnchorScope{Topics: []string{"Scheduling"}, Audiences: []string{"Application developers"}, Description: "Version-specific scheduling limits"}})
		check(err)
		modelRecord, err := modelRuns.GetModelRun(ctx, f.workspace, seedModelID)
		check(err)
		call := agentdomain.ModelCall{ID: organizingIntegrationID(89001), ModelRunID: seedModelID, CallNo: 1, Phase: agentdomain.ModelCallInitial, Model: modelRecord.Run.Model, Profile: modelRecord.Run.Profile, Prompt: modelRecord.Run.Prompt, Schema: modelRecord.Run.Schema, MaxOutputTokens: 128, Status: agentdomain.ModelCallStarted, RequestHash: organizingIntegrationHash("refresh-association"), RequestBytes: 128, Version: 1, StartedAt: modelRecord.Run.CreatedAt.Add(time.Second)}
		_, _, err = modelRuns.StartModelCall(ctx, f.workspace, call)
		check(err)
		completed := call.StartedAt.Add(time.Second)
		call.Status = agentdomain.ModelCallSucceeded
		call.ResponseHash = organizingIntegrationHash("refresh-association-response")
		call.ResponseBytes = 64
		call.Version = 2
		call.CompletedAt = &completed
		_, _, err = modelRuns.CompleteModelCall(ctx, agentapp.CompleteModelCallCommand{WorkspaceID: f.workspace, ExpectedVersion: 1, Call: call})
		check(err)
		modelRecord.Run.Status = agentdomain.ModelRunSucceeded
		modelRecord.Run.FinalResultType = agentdomain.ResultTypeRAGAnswer
		modelRecord.Run.Version++
		now := time.Now().UTC().Truncate(time.Microsecond)
		modelRecord.Run.UpdatedAt = now
		modelRecord.Run.CompletedAt = &now
		_, _, err = modelRuns.FinalizeModelRun(ctx, agentapp.FinalizeModelRunCommand{ExpectedVersion: 1, Run: modelRecord.Run})
		check(err)
		preparedEvidence, err := f.store.PrepareSynthesisBodyRefresh(ctx, f.workspace, request.ID)
		check(err)
		for _, item := range preparedEvidence.Items {
			for _, updated := range item.Updated.Items {
				if updated.ID == item.Impact.UpstreamItemID {
					evidence = append(evidence, updated.SourceReferences()...)
				}
			}
		}
		proposal, err := anchors.RecordAnchorRecommendation(ctx, app.RecordAnchorRecommendation{WorkspaceID: f.workspace, AnchorID: created.Anchor.ID, IdempotencyKey: "refresh-source-association", ExpectedScopeVersion: 1, Kind: domain.AnchorSourceAssociation, Reason: "Version-specific scheduling limits belong to this target", Evidence: evidence, ModelRunID: seedModelID})
		check(err)
		_, err = anchors.DecideAnchor(ctx, app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: created.Anchor.ID, IdempotencyKey: "refresh-source-accept", Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: created.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: proposal.Proposal.ID, ExpectedVersion: proposal.Proposal.Version}}})
		check(err)
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(f.platform)
	check(err)
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(f.platform)
	check(err)
	store, err := synthesispostgres.NewStore(f.platform, synthesispostgres.Dependencies{ModelRuns: modelRuns, WorkflowFence: fence, WorkflowBindings: bindings})
	check(err)
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.platform, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: store})
	check(err)
	runs, err := workflowpostgres.NewGORMRepository(f.platform)
	check(err)
	// 在任何刷新生成开始前，将既有所属模块服务从初始测试校验切换为生产模型日志校验。
	f.store.dependencies.Validated = store
	f.store.dependencies.Anchors = anchors
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "body-refresh", ModelVersion: "v1"}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "body-refresh", Version: "v1"}, Model: modelRef, Timeout: time.Second, MaxOutputTokens: 2048}
	catalog := agentapp.NewRuntimeCatalog()
	check(organizingagent.RegisterSynthesisRuntimeCatalog(catalog))
	check(catalog.RegisterProfile(profile))
	check(catalog.Freeze())
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	check(err)
	output := `{"notes":[{"note":"N001","operations":[{"kind":"REFRESH_ITEM","target":"I002"}]}]}`
	review := `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]},{"index":2,"verdict":"SUPPORTED","sources":[{"source":"S002","verdict":"SUPPORTED"}]},{"index":3,"verdict":"SUPPORTED","sources":[]}]}`

	if mode == "ordinary" || unanchored {
		output = `{"notes":[{"note":"N001","operations":[{"op":"ADD_FACT","statement":{"text":"Scheduling requires a version-specific workload limit.","applicability":"","sources":["S001"]}}]}]}`
		review = `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`
	}
	if unanchored {
		// 先结束测试来源通知，再让真实 source-ready outbox 与调度器领取新来源，且不创建锚点。
		check(f.db.Exec("UPDATE workflow.outbox_event SET published_at=clock_timestamp() WHERE workspace_id=? AND event_type='ingestion.source.ready' AND published_at IS NULL", string(f.workspace)).Error)
		f.generation(t, 88000, nil, "Scheduling requires a version-specific workload limit.")
		candidates, err := f.service.ListCandidates(ctx, f.workspace)
		check(err)
		for index, candidate := range candidates {
			if candidate.Note.ID == before.Note.ID {
				output = strings.Replace(output, "N001", fmt.Sprintf("N%03d", index+1), 1)
			}
		}
		if mode == "human_multi" {
			var document map[string]any
			check(json.Unmarshal([]byte(output), &document))
			for index, candidate := range candidates {
				if candidate.Note.ID != before.Note.ID {
					document["notes"] = append(document["notes"].([]any), map[string]any{"note": fmt.Sprintf("N%03d", index+1), "operations": []any{map[string]any{"op": "ADD_FACT", "statement": map[string]any{"text": "Scheduling requires a version-specific workload limit.", "applicability": "", "sources": []string{"S001"}}}}})
				}
			}
			encoded, err := json.Marshal(document)
			check(err)
			output = string(encoded)
			review = `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]},{"index":2,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`
		}
	}
	nextReview := `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`
	nextOutput := `{"notes":[{"note":"N001","operations":[{"op":"ADD_FACT","statement":{"text":"Additional scheduling boundary.","applicability":"","sources":["S001"]}}]}]}`
	if mode == "audit_duplicate" {
		item := before.CurrentRevision.Items[2]
		encoded, err := json.Marshal(map[string]any{"notes": []any{map[string]any{"note": "N001", "operations": []any{map[string]any{"op": "ADD_FACT", "statement": map[string]any{"text": item.Fact.Text, "applicability": item.Fact.Applicability, "sources": []string{"S001"}}}}}}})
		check(err)
		nextOutput = string(encoded)
		if len(sourceReviewHooks) > 0 && sourceReviewHooks[0] != nil && sourceReviewHooks[0].mode == "multi_obligation" {
			operations := []any{}
			for _, index := range []int{2, 3} {
				fact := before.CurrentRevision.Items[index].Fact
				operations = append(operations, map[string]any{"op": "ADD_FACT", "statement": map[string]any{"text": fact.Text, "applicability": fact.Applicability, "sources": []string{"S001"}}})
			}
			encoded, err = json.Marshal(map[string]any{"notes": []any{map[string]any{"note": "N001", "operations": operations}}})
			check(err)
			nextOutput = string(encoded)
			nextReview = `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]},{"index":2,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`
		}
	}
	if mode == "human_cancel" {
		nextOutput = output
	}
	provider := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}, agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(review), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}, agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(nextOutput), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}, agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(nextReview), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}})
	model, err := organizingagent.NewSynthesisModel(organizingagent.SynthesisModelDependencies{Model: provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: modelRuns, Store: store, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	check(err)
	rootStore, err := rootgrant.NewGORMAuthoritativeStore(f.platform, rootgrant.RuntimeGrantDirect)
	check(err)
	view, rootErr := rootStore.CurrentRootGrant(ctx)
	if rootErr != nil {
		t.Fatalf("root projection: %+v %v", view, rootErr)
	}
	resolver, err := rootgrant.NewDirectRootGrantResolver(rootStore)
	check(err)
	t.Cleanup(func() { _ = resolver.Close() })
	rootCapability, err := resolver.Resolve(ctx, f.workspace)
	check(err)
	check(rootCapability.Close())
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
	manuscripts, err := NewSynthesisManuscriptRuntime(SynthesisManuscriptRuntimeDependencies{Pool: f.platform, Candidates: f.store, Models: store, Executions: store, Roots: roots, Service: app.SynthesisDependencies{Store: f.store, Sources: f.sources, Publications: publications, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}, Storage: SynthesisManuscriptStoreDependencies{Files: roots, Mapper: manuscript.Mapper{}, Merge: merger, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}})
	check(err)
	executor, err := organizingworkflow.NewSynthesisExecutor(organizingworkflow.SynthesisExecutorDependencies{Manuscripts: manuscriptRuntimeMutation{runtime: manuscripts, f: f, anchors: anchors, source: incoming.Source, mode: mode}, Runs: runs, Store: store, Candidates: f.service, Sources: f.sources, Anchors: anchors, BodyRefresh: f.store, Model: model, Clock: foundation.SystemClock{}})
	check(err)
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	check(err)
	executors, err := workflowapp.NewExecutorRegistry(validation)
	check(err)
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		check(executors.Register(kind, 1, executor))
	}
	check(executors.Freeze())
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	check(err)
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions() {
		check(definitions.Register(definition))
	}
	check(definitions.Freeze())
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	check(err)
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "body-refresh-integration", 10*time.Second, time.Second)
	check(err)
	workers := riveradapter.NewWorkers()
	check(riveradapter.AddRuntimeWorkerSafely(workers, worker))
	client, err := riveradapter.NewClient(f.platform.DB(), workers)
	check(err)
	check(client.Start(ctx))
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	check(err)
	bodyDispatcher := &organizingworkflow.BodyRefreshGenerationDispatcher{Manuscripts: true, Workspaces: workspaces, Store: store, UnitOfWork: f.uow, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}}
	var dispatcher interface {
		DispatchBatch(context.Context, int) (organizingworkflow.SynthesisDispatchBatchResult, error)
	} = bodyDispatcher
	if mode == "ordinary" || unanchored {
		dispatcher = &organizingworkflow.AnchorFusionDispatcher{Manuscripts: true, UnitOfWork: f.uow, Requests: anchors, Processing: store, Sources: f.sources, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}
	}

	if unanchored {
		outbox, err := workflowpostgres.NewGORMSourceReadyOutbox(f.platform)
		check(err)
		dispatcher, err = organizingworkflow.NewSynthesisDispatcher(organizingworkflow.SynthesisDispatcherDependencies{Manuscripts: true, UnitOfWork: f.uow, Outbox: outbox, Sources: f.sources, Processing: store, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
		check(err)
	}

	type dispatchOutcome struct {
		result organizingworkflow.SynthesisDispatchBatchResult
		err    error
	}
	dispatches := make(chan dispatchOutcome, 2)
	for range 2 {
		go func() { result, err := dispatcher.DispatchBatch(ctx, 1); dispatches <- dispatchOutcome{result, err} }()
	}
	started := 0
	for range 2 {
		dispatched := <-dispatches
		check(dispatched.err)
		started += dispatched.result.Started
	}
	if started != 1 {
		t.Fatalf("concurrent dispatch started=%d", started)
	}
	var processing app.SynthesisProcessing
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var id string
		if unanchored {
			check(f.db.Raw("SELECT id FROM organizing.synthesis_processing WHERE workspace_id=? AND fusion_request_id IS NULL AND body_refresh_request_id IS NULL ORDER BY created_at DESC LIMIT 1", string(f.workspace)).Scan(&id).Error)
		} else if mode == "ordinary" {
			check(f.db.Raw("SELECT id FROM organizing.synthesis_processing WHERE workspace_id=? AND fusion_request_id IS NOT NULL ORDER BY created_at LIMIT 1", string(f.workspace)).Scan(&id).Error)
		} else {
			check(f.db.Raw("SELECT id FROM organizing.synthesis_processing WHERE body_refresh_request_id=?", string(request.ID)).Scan(&id).Error)
		}
		processing, err = store.GetSynthesisProcessing(ctx, f.workspace, foundation.ID(id))
		check(err)
		if mode == "conflict" || strings.HasPrefix(mode, "human_") {
			var waiting int64
			check(f.db.Raw("SELECT count(*) FROM workflow.human_task WHERE run_id=? AND status='pending'", string(processing.WorkflowRunID)).Scan(&waiting).Error)
			if waiting == 1 {
				break
			}
		}
		if processing.Status != app.SynthesisProcessingPending && processing.Status != app.SynthesisProcessingRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mode == "conflict" || strings.HasPrefix(mode, "human_") {
		if mode == "human_owner" || mode == "human_scope" {
			human, err := workflowapp.NewRuntimeHumanCoordinator(runtime)
			check(err)
			service, err := manuscripts.HumanReview(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}), human)
			check(err)
			preview, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
			check(err)
			if mode == "human_owner" {
				check(f.db.Exec("UPDATE core.document SET version=version+1,updated_at=clock_timestamp() WHERE id=?", string(before.Note.DocumentID)).Error)
			} else {
				_, err = anchors.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "late-human-scope", NoteID: before.Note.ID, ExpectedNoteVersion: before.Note.Version, BasisRevisionID: before.CurrentRevision.ID, Title: "Late scope", Scope: domain.AnchorScope{Topics: []string{"Scheduling"}, Audiences: []string{"Developers"}, Description: "Human scope changed while waiting"}})
				check(err)
			}
			command := app.DecideSynthesisManuscript{Binding: preview.Binding, NoteID: before.Note.ID, AttemptID: preview.Target.AttemptID, CaptureID: preview.Target.CaptureID, IdempotencyKey: "stale-human-owner", Resolution: app.SynthesisManuscriptResolution{Stage: preview.Review.Stage, PreviewFingerprint: preview.Target.PreviewFingerprint, FinalContent: preview.Review.Proposed}}
			for _, c := range preview.Review.Conflicts {
				command.Resolution.AcknowledgedOrdinals = append(command.Resolution.AcknowledgedOrdinals, c.Ordinal)
			}
			if _, err = service.Decide(ctx, command); err == nil {
				t.Fatal("stale owner/scope accepted human decision")
			}
			f.count(t, "organizing.synthesis_manuscript_review_decision", "workspace_id", string(f.workspace), 0)
			f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), 0)
			return
		}
		if mode == "human_forged" {
			human, err := workflowapp.NewRuntimeHumanCoordinator(runtime)
			check(err)
			service, err := manuscripts.HumanReview(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}), human)
			check(err)
			pending, err := service.Read(ctx, f.workspace, processing.ID)
			check(err)
			notes := []foundation.ID{}
			for _, target := range pending.Targets {
				notes = append(notes, target.NoteID)
			}
			decision, err := json.Marshal(map[string]any{"version": app.SynthesisManuscriptReviewVersion, "processing_id": processing.ID, "workflow_run_id": processing.WorkflowRunID, "note_ids": notes, "result_hash": strings.Repeat("0", 64)})
			check(err)
			_, err = human.SubmitHuman(ctx, workflowapp.HumanDecisionCommand{RunID: processing.WorkflowRunID, TaskID: pending.Binding.HumanTaskID, TargetVersion: pending.Binding.TargetVersion, Decision: decision, CallerCapabilities: []capability.Capability{capability.ReadLocal, capability.WriteProposal}})
			check(err)
			until := time.Now().Add(12 * time.Second)
			for time.Now().Before(until) {
				processing, err = store.GetSynthesisProcessing(ctx, f.workspace, processing.ID)
				check(err)
				if processing.Status == app.SynthesisProcessingFailed {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if processing.Status != app.SynthesisProcessingFailed || provider.CallCount() != 2 {
				t.Fatalf("forged task not rejected: %+v calls=%d", processing, provider.CallCount())
			}
			after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
			check(err)
			if after.CurrentRevision.ID != before.CurrentRevision.ID {
				t.Fatal("forged task created candidate")
			}
			f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), 0)
			return
		}
		if mode == "human_cancel" {
			human, err := workflowapp.NewRuntimeHumanCoordinator(runtime)
			check(err)
			service, err := manuscripts.HumanReview(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}), human)
			check(err)
			old, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
			check(err)
			path, _ := authoringdomain.DefaultGeneratedTargetPath(before.Note.ID)
			original, err := os.ReadFile(filepath.Join(f.root, path))
			check(err)
			check(os.WriteFile(filepath.Join(f.root, path), append(append([]byte{}, original...), []byte("\nNew manual bytes\n")...), 0600))
			command := app.DecideSynthesisManuscript{Binding: old.Binding, NoteID: before.Note.ID, AttemptID: old.Target.AttemptID, CaptureID: old.Target.CaptureID, IdempotencyKey: "stale-before-cancel", Resolution: app.SynthesisManuscriptResolution{Stage: old.Review.Stage, PreviewFingerprint: old.Target.PreviewFingerprint, FinalContent: old.Review.Proposed}}
			for _, c := range old.Review.Conflicts {
				command.Resolution.AcknowledgedOrdinals = append(command.Resolution.AcknowledgedOrdinals, c.Ordinal)
			}
			if _, err = service.Decide(ctx, command); err == nil {
				t.Fatal("stale file accepted before cancel")
			}
			var version int64
			check(f.db.Raw("SELECT version FROM workflow.run WHERE id=?", string(processing.WorkflowRunID)).Scan(&version).Error)
			_, err = coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: processing.WorkflowRunID, ExpectedVersion: version, IdempotencyKey: "cancel-stale-manuscript", CallerCapabilities: []capability.Capability{capability.ReadLocal, capability.WriteProposal}})
			check(err)
			failed, err := store.GetSynthesisProcessing(ctx, f.workspace, processing.ID)
			check(err)
			if failed.Status != app.SynthesisProcessingFailed || failed.Failure == nil || !failed.Failure.Retryable {
				t.Fatalf("cancel not safely retryable: %+v", failed)
			}
			if _, err = service.Decide(ctx, command); err == nil {
				t.Fatal("cancelled old task accepted")
			}
			ps, err := organizingworkflow.NewSynthesisProcessingService(organizingworkflow.SynthesisProcessingServiceDependencies{UnitOfWork: f.uow, Queries: store, Retries: store, Applied: f.store, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}})
			check(err)
			retry, err := ps.RetryProcessing(ctx, app.RetrySynthesisCommand{WorkspaceID: f.workspace, ProcessingID: processing.ID, ExpectedVersion: failed.Version, IdempotencyKey: "retry-stale-manuscript"})
			check(err)
			if retry.Processing.WorkflowRunID == processing.WorkflowRunID {
				t.Fatal("retry reused old run")
			}
			until := time.Now().Add(15 * time.Second)
			for time.Now().Before(until) {
				var count int
				check(f.db.Raw("SELECT count(*) FROM workflow.human_task WHERE run_id=? AND status='pending'", string(retry.Processing.WorkflowRunID)).Scan(&count).Error)
				if count == 1 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			fresh, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
			check(err)
			if fresh.Binding.HumanTaskID == old.Binding.HumanTaskID || fresh.Target.AttemptID == old.Target.AttemptID || fresh.Target.CaptureID == old.Target.CaptureID {
				t.Fatal("retry reused stale identity")
			}
			resolveRuntimeHuman(t, f, manuscripts, runtime, definitions, store, retry.Processing, before, provider.CallCount, 4, false, false)
			// 外层调用方断言其原始受控文件尚未发布。
			check(os.WriteFile(filepath.Join(f.root, path), original, 0600))
			return
		}
		resolveRuntimeHuman(t, f, manuscripts, runtime, definitions, store, processing, before, provider.CallCount, 2, false, mode == "human_two_stage")
		if mode == "human_two_stage" {
			current, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
			check(err)
			fusion := &organizingworkflow.AnchorFusionDispatcher{Manuscripts: true, UnitOfWork: f.uow, Requests: anchors, Processing: store, Sources: f.sources, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}
			started, err := fusion.DispatchBatch(ctx, 1)
			check(err)
			if started.Started != 1 {
				t.Fatalf("second human generation: %+v", started)
			}
			deadline := time.Now().Add(20 * time.Second)
			var next app.SynthesisProcessing
			for time.Now().Before(deadline) {
				var id foundation.ID
				check(f.db.Raw("SELECT id FROM organizing.synthesis_processing WHERE workspace_id=? AND fusion_request_id IS NOT NULL ORDER BY created_at DESC LIMIT 1", string(f.workspace)).Scan(&id).Error)
				next, err = store.GetSynthesisProcessing(ctx, f.workspace, id)
				check(err)
				var waiting int
				check(f.db.Raw("SELECT count(*) FROM workflow.human_task WHERE run_id=? AND status='pending'", string(next.WorkflowRunID)).Scan(&waiting).Error)
				if waiting == 1 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			resolveRuntimeHuman(t, f, manuscripts, runtime, definitions, store, next, current, provider.CallCount, 4, true, false)
		}
		return
	}
	if mode != "admitted" && mode != "audit_duplicate" && mode != "ordinary" && mode != "unanchored" {
		expectedStatus := app.SynthesisProcessingFailed
		if mode == "conflict" {
			expectedStatus = app.SynthesisProcessingRecoveryRequired
		}
		if processing.Status != expectedStatus || processing.Failure == nil || provider.CallCount() != 2 {
			t.Fatalf("changed admission was not rejected at apply: status=%s failure=%+v calls=%d", processing.Status, processing.Failure, provider.CallCount())
		}
		after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
		check(err)
		if after.CurrentRevision.ID != before.CurrentRevision.ID || after.PublishedRevision.ID != before.PublishedRevision.ID || len(processing.RevisionIDs) != 0 {
			t.Fatal("rejected refresh appended or published a revision")
		}
		f.count(t, "organizing.synthesis_manuscript_capture", "workspace_id", string(f.workspace), 1)
		f.count(t, "organizing.synthesis_manuscript_application", "workspace_id", string(f.workspace), 0)
		if mode == "conflict" {
			f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), 0)
		}
		return
	}
	if processing.Status != app.SynthesisProcessingSucceeded || len(processing.RevisionIDs) != 1 || provider.CallCount() != 2 {
		t.Fatalf("refresh status=%s failure=%+v calls=%d", processing.Status, processing.Failure, provider.CallCount())
	}
	execution, err := store.LoadSynthesisExecution(ctx, f.workspace, processing.ID, processing.WorkflowRunID)
	if err == nil && execution.Input != nil && execution.Input.SemanticPromptVersion != app.SynthesisSourceIdentitySemanticPromptVersion {
		t.Fatal("new execution lost semantic format version")
	}
	check(err)
	if execution.Input == nil || (mode != "ordinary" && mode != "unanchored") && execution.Input.BodyRefresh == nil || execution.Generation == nil || execution.Semantic == nil || !execution.Semantic.Semantic.Accepted || execution.Generation.ModelRunID == execution.Semantic.ModelRunID {
		t.Fatal("refresh lost separate persisted model proofs")
	}
	if mode != "ordinary" && mode != "unanchored" {
		if execution.Input.SourceEvent.Source == incoming.Source {
			t.Fatal("fixture must use provenance A distinct from evidence B")
		}
		if len(execution.Input.Sources) != len(evidence) || !reflect.DeepEqual(execution.Input.Notes[0].Anchor.AllowedSources, execution.Input.Sources) {
			t.Fatal("frozen admission does not exactly match refreshed evidence")
		}
		for _, ref := range execution.Input.Sources {
			if ref.Source == execution.Input.SourceEvent.Source {
				t.Fatal("provenance seed leaked into refresh evidence")
			}
		}

	}
	frozen, _ := json.Marshal(execution.Input)
	if len(frozen) == 0 {
		t.Fatal("missing frozen input")
	}
	after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
	check(err)
	if after.CurrentRevision.RevisionNo != before.CurrentRevision.RevisionNo+1 || after.CurrentRevision.Manuscript == nil || len(after.CurrentRevision.Items) != 0 || (mode != "ordinary" && mode != "unanchored") && (after.CurrentRevision.Manuscript.Machine.MachineItems[1].Gap.Resolution == nil || !reflect.DeepEqual(after.CurrentRevision.Manuscript.Machine.MachineItems[2:], before.CurrentRevision.Items[2:])) || after.PublishedRevision.ID != before.PublishedRevision.ID {
		t.Fatalf("local candidate or published pointer changed incorrectly: %+v", after)
	}
	content, err := after.CurrentRevision.Content()
	check(err)
	if !strings.Contains(content, "User transaction annotation: keep verbatim.") {
		t.Fatal("manual annotation lost")
	}
	f.count(t, "organizing.synthesis_manuscript_capture", "workspace_id", string(f.workspace), 1)
	f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), 1)
	recovered, found, err := f.service.RecoverAppliedGeneration(ctx, f.workspace, processing.ID)
	check(err)
	if !found || !reflect.DeepEqual(recovered.RevisionIDs, processing.RevisionIDs) || provider.CallCount() != 2 {
		t.Fatal("exact replay changed model or revision")
	}
	if mode == "ordinary" || unanchored {
		return
	}
	repeated, err := dispatcher.DispatchBatch(ctx, 1)
	check(err)
	if repeated.Started != 0 || provider.CallCount() != 2 {
		t.Fatal("same publication generated twice")
	}
	if mode == "admitted" || mode == "audit_duplicate" {
		fusion := &organizingworkflow.AnchorFusionDispatcher{Manuscripts: true, UnitOfWork: f.uow, Requests: anchors, Processing: store, Sources: f.sources, Starter: runtime, Definitions: definitions, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}}
		started, err := fusion.DispatchBatch(ctx, 1)
		check(err)
		if started.Started != 1 {
			t.Fatalf("next generation dispatch: %+v", started)
		}
		var next app.SynthesisProcessing
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			var id foundation.ID
			check(f.db.Raw("SELECT id FROM organizing.synthesis_processing WHERE workspace_id=? AND fusion_request_id IS NOT NULL ORDER BY created_at DESC LIMIT 1", string(f.workspace)).Scan(&id).Error)
			next, err = store.GetSynthesisProcessing(ctx, f.workspace, id)
			check(err)
			if next.Status != app.SynthesisProcessingPending && next.Status != app.SynthesisProcessingRunning {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if mode == "audit_duplicate" {
			if next.Status != app.SynthesisProcessingRecoveryRequired || next.Failure == nil || next.Failure.Code != "SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED" || provider.CallCount() != 4 {
				t.Fatalf("audit duplicate must require source review: %+v calls=%d", next, provider.CallCount())
			}
			unchanged, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
			check(err)
			if unchanged.CurrentRevision.ID != after.CurrentRevision.ID || unchanged.CurrentRevision.Manuscript == nil || len(unchanged.CurrentRevision.Items) != 0 || len(next.RevisionIDs) != 0 {
				t.Fatal("audit duplicate changed manuscript or promoted audit items")
			}
			f.count(t, "organizing.synthesis_manuscript_capture", "workspace_id", string(f.workspace), 1)
			if len(sourceReviewHooks) > 0 && sourceReviewHooks[0] != nil {
				sourceReviewHooks[0].run(t, f, manuscripts, modelRuns, runs, runtime, next, unchanged, provider.CallCount, client)
			}
			return
		}
		if next.Status != app.SynthesisProcessingSucceeded || provider.CallCount() != 4 {
			t.Fatalf("next manuscript generation: %+v calls=%d", next, provider.CallCount())
		}
		updated, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
		check(err)
		full, err := updated.CurrentRevision.Content()
		check(err)
		if updated.CurrentRevision.Manuscript == nil || len(updated.CurrentRevision.Items) != 0 || updated.CurrentRevision.ParentRevisionID != after.CurrentRevision.ID || !strings.Contains(full, "Additional scheduling boundary") || !strings.Contains(full, "User transaction annotation: keep verbatim.") || len(updated.CurrentRevision.Manuscript.Machine.MachineItems) != len(after.CurrentRevision.Manuscript.Machine.MachineItems)+1 {
			t.Fatal("next machine delta lost prior unpublished/manual branch")
		}
	}

}

// 这些变更发生在干净捕获、封存之后、实际应用之前；模型、提供方及所有所属模块证明实现仍使用生产代码。
type manuscriptRuntimeMutation struct {
	runtime *SynthesisManuscriptRuntime
	f       *synthesisGitFixture
	anchors *GORMAnchorStore
	source  domain.SynthesisSourceVersion
	mode    string
}

func (m manuscriptRuntimeMutation) PrepareManuscripts(ctx context.Context, e workflowapp.ExecutionContext, i app.SynthesisGenerationInput, g app.SynthesisGenerationResult) (*workflowapp.HumanWaitResult, error) {
	return m.runtime.PrepareManuscripts(ctx, e, i, g)
}
func (m manuscriptRuntimeMutation) ApplyManuscripts(ctx context.Context, e workflowapp.ExecutionContext, i app.SynthesisGenerationInput, g app.SynthesisGenerationResult) (app.SynthesisApplyResult, error) {
	var err error
	switch m.mode {
	case "anchor_created":
		for _, note := range i.Notes {
			if note.Note.ID == g.Notes[0].NoteID {
				_, err = m.anchors.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: m.f.workspace, IdempotencyKey: "late-anchor", NoteID: note.Note.ID, ExpectedNoteVersion: note.Note.Version, BasisRevisionID: note.Revision.ID, Title: "Late anchor", Scope: domain.AnchorScope{Topics: []string{"Scheduling"}, Audiences: []string{"Application developers"}, Description: "Added after capture"}})
			}
		}
	case "source_changed":
		err = m.f.db.WithContext(ctx).Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(m.source.SourceID)).Error
	case "root_changed":
		err = m.f.db.WithContext(ctx).Exec(`UPDATE core.workspace SET status='inactive',version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(m.f.workspace)).Error
	case "binding_changed":
		err = m.f.db.WithContext(ctx).Exec(`UPDATE core.workspace SET binding_version=binding_version+1,version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(m.f.workspace)).Error
	case "owner_changed":
		err = m.f.db.WithContext(ctx).Exec(`UPDATE core.document SET version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(i.Notes[0].Note.DocumentID)).Error
	case "scope_changed":
		m.f.store.dependencies.Anchors = bodyRefreshAdmissionMutation{owner: m.anchors, mode: "scope_changed", source: m.source}
	}
	if err != nil {
		return app.SynthesisApplyResult{}, err
	}
	return m.runtime.ApplyManuscripts(ctx, e, i, g)
}

// 此路径使用真实 HumanTask 状态、生产 HumanAuthority 和模型日志；仅提供方响应固定。
func resolveRuntimeHuman(t *testing.T, f *synthesisGitFixture, manuscripts *SynthesisManuscriptRuntime, runtime *workflowpostgres.GORMRuntimeRepository, definitions *workflowapp.DefinitionRegistry, store *synthesispostgres.Store, processing app.SynthesisProcessing, before app.SynthesisNoteDetail, calls func() int, expectedCalls int, twoStage, rewrite bool) {
	t.Helper()
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	coordinator, err := workflowapp.NewRuntimeHumanCoordinator(runtime)
	check(err)
	if _, err = manuscripts.HumanReview(app.SynthesisManuscriptCaller{}, coordinator); err == nil {
		t.Fatal("zero caller authorized")
	}
	service, err := manuscripts.HumanReview(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}), coordinator)
	check(err)
	summary, err := service.Read(ctx, f.workspace, processing.ID)
	check(err)
	if summary.Ready || summary.Submitted || len(summary.Targets) < 1 || len(summary.Targets) > 2 || calls() != expectedCalls {
		t.Fatalf("not waiting: %+v calls=%d", summary, calls())
	}
	if twoStage && os.Getenv("ZHIXU_MANUSCRIPT_BROWSER_DIR") != "" {
		driveManuscriptBrowser(t, f, manuscripts, runtime, definitions, store, coordinator, processing, before, calls, expectedCalls)
		return
	}
	if len(summary.Targets) == 2 {
		ready := 0
		for _, target := range summary.Targets {
			if target.Ready {
				ready++
			}
		}
		if ready != 1 {
			t.Fatalf("conflict-first prepare omitted clean target: %+v", summary.Targets)
		}
		f.count(t, "organizing.synthesis_manuscript_capture", "workspace_id", string(f.workspace), 2)
		f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), 1)
	}
	if _, err = service.Read(ctx, f.otherWorkspace, processing.ID); err == nil {
		t.Fatal("cross workspace allowed")
	}
	detail, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
	check(err)
	if detail.Review == nil {
		t.Fatal("real conflict missing")
	}
	same, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
	check(err)
	if !reflect.DeepEqual(detail, same) {
		t.Fatal("refresh changed preview")
	}
	if _, err = service.ReadNote(ctx, f.workspace, processing.ID, organizingIntegrationID(99001)); err == nil {
		t.Fatal("unrelated note allowed")
	}
	path, _ := authoringdomain.DefaultGeneratedTargetPath(before.Note.ID)
	original, err := os.ReadFile(filepath.Join(f.root, path))
	check(err)
	command := app.DecideSynthesisManuscript{Binding: detail.Binding, NoteID: before.Note.ID, AttemptID: detail.Target.AttemptID, CaptureID: detail.Target.CaptureID, IdempotencyKey: "human-exact-command:" + string(processing.ID), Resolution: app.SynthesisManuscriptResolution{Stage: detail.Review.Stage, PreviewFingerprint: detail.Target.PreviewFingerprint, FinalContent: "<!-- User transaction annotation: keep verbatim. -->\n\n" + detail.Review.Proposed}}
	if rewrite {
		command.Resolution.FinalContent = "<!-- User transaction annotation: keep verbatim. -->\n\n# Human candidate rewrite\nRetain this complete manual manuscript.\n"
	}
	for _, conflict := range detail.Review.Conflicts {
		command.Resolution.AcknowledgedOrdinals = append(command.Resolution.AcknowledgedOrdinals, conflict.Ordinal)
	}
	check(os.WriteFile(filepath.Join(f.root, path), append(append([]byte{}, original...), []byte("\nDrift\n")...), 0600))
	if _, err = service.Decide(ctx, command); err == nil {
		t.Fatal("file drift accepted")
	}
	check(os.WriteFile(filepath.Join(f.root, path), original, 0600))
	forged := command
	forged.CaptureID = organizingIntegrationID(99002)
	if _, err = service.Decide(ctx, forged); err == nil {
		t.Fatal("forged capture accepted")
	}
	// 每次人工调用都通过真实 Resolver 检查根目录是否仍有效。
	check(f.db.Exec("UPDATE core.workspace SET status='inactive',version=version+1,updated_at=clock_timestamp() WHERE id=?", string(f.workspace)).Error)
	if _, err = service.Decide(ctx, command); err == nil {
		t.Fatal("inactive root accepted human decision")
	}
	check(f.db.Exec("UPDATE core.workspace SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id=?", string(f.workspace)).Error)
	post := manuscriptAuthenticatedHTTP(t, f, manuscripts, coordinator, processing.ID, before.Note.ID)
	resumeBody, err := json.Marshal(map[string]any{"binding": summary.Binding})
	check(err)
	if response := post(http.MethodPost, "/resume", resumeBody, true); response.Code < 400 {
		t.Fatal("resume accepted incomplete receipts")
	}
	// 持久化所属模块结果后，在通用提交前丢失响应。
	h := &synthesisManuscriptHuman{runtime: manuscripts, caller: app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal})}
	d := manuscripts.dependencies.Storage
	d.Baselines = h
	d.Proof = h
	raw, err := NewGORMSynthesisManuscriptStore(f.platform, d)
	check(err)
	reviews, err := NewGORMSynthesisManuscriptReviewStore(raw, h)
	check(err)
	stage, err := reviews.Decide(ctx, command)
	check(err)
	if twoStage {
		if stage.Result.Preview.Review == nil || stage.Result.Preview.Review.Stage != app.SynthesisMergeWorkspaceStage || command.Resolution.Stage != app.SynthesisMergeCandidateStage {
			t.Fatal("did not reach second distinct stage")
		}
		stageBody, err := json.Marshal(command)
		check(err)
		stageHTTP := post(http.MethodPost, "/"+string(before.Note.ID)+"/decisions", stageBody, true)
		if stageHTTP.Code != http.StatusOK {
			t.Fatalf("first stage HTTP: %d %s", stageHTTP.Code, stageHTTP.Body.String())
		}
		var pending app.SynthesisManuscriptReviewSummary
		check(json.Unmarshal(stageHTTP.Body.Bytes(), &pending))
		if pending.Ready || pending.Submitted {
			t.Fatal("first stage released human task")
		}
		secondHTTP := post(http.MethodGet, "/"+string(before.Note.ID), nil, true)
		if secondHTTP.Code != http.StatusOK {
			t.Fatal("second stage HTTP detail failed")
		}
		second, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
		check(err)
		if second.Binding != command.Binding || second.Target.PreviewFingerprint == command.Resolution.PreviewFingerprint {
			t.Fatal("second stage lost same-task distinct fingerprint")
		}
		command.IdempotencyKey += "-second"
		command.Resolution = app.SynthesisManuscriptResolution{Stage: second.Review.Stage, PreviewFingerprint: second.Target.PreviewFingerprint, FinalContent: "<!-- User transaction annotation: keep verbatim. -->\n\n" + second.Review.Proposed}
		for _, conflict := range second.Review.Conflicts {
			command.Resolution.AcknowledgedOrdinals = append(command.Resolution.AcknowledgedOrdinals, conflict.Ordinal)
		}
		_, err = reviews.Decide(ctx, command)
		check(err)
	}
	// 丢失最终命令后，重建所属模块服务和 HTTP 工厂。两阶段场景验证仅靠绑定的恢复；其他模式保留正常最终 Decide 和精确命令重放的覆盖。
	binding := command.Binding
	if twoStage {
		command = app.DecideSynthesisManuscript{}
		service, err = manuscripts.HumanReview(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}), coordinator)
		check(err)
		post = manuscriptAuthenticatedHTTP(t, f, manuscripts, coordinator, processing.ID, before.Note.ID)
		refreshed := post(http.MethodGet, "", nil, true)
		var ready app.SynthesisManuscriptReviewSummary
		check(json.Unmarshal(refreshed.Body.Bytes(), &ready))
		if refreshed.Code != http.StatusOK || !ready.Ready || ready.Submitted || ready.Binding != binding {
			t.Fatalf("receipt gap not recoverable: %d %+v", refreshed.Code, ready)
		}
		detail, err := service.ReadNote(ctx, f.workspace, processing.ID, before.Note.ID)
		check(err)
		if detail.Review != nil {
			t.Fatal("completed receipt still exposes review")
		}
		for _, bad := range []app.SynthesisManuscriptHumanBinding{
			{WorkspaceID: f.otherWorkspace, ProcessingID: binding.ProcessingID, RunID: binding.RunID, NodeRunID: binding.NodeRunID, HumanTaskID: binding.HumanTaskID, TargetVersion: binding.TargetVersion},
			{WorkspaceID: binding.WorkspaceID, ProcessingID: binding.ProcessingID, RunID: binding.RunID, NodeRunID: binding.NodeRunID, HumanTaskID: organizingIntegrationID(99003), TargetVersion: binding.TargetVersion},
			{WorkspaceID: binding.WorkspaceID, ProcessingID: binding.ProcessingID, RunID: binding.RunID, NodeRunID: binding.NodeRunID, HumanTaskID: binding.HumanTaskID, TargetVersion: binding.TargetVersion + 1},
		} {
			body, err := json.Marshal(map[string]any{"binding": bad})
			check(err)
			if response := post(http.MethodPost, "/resume", body, true); response.Code < 400 {
				t.Fatal("forged resume accepted")
			}
		}
		if response := post(http.MethodPost, "/resume", resumeBody, false); response.Code != http.StatusForbidden {
			t.Fatal("readonly resume accepted")
		}
		check(f.db.Exec("UPDATE core.workspace SET status='inactive',version=version+1,updated_at=clock_timestamp() WHERE id=?", string(f.workspace)).Error)
		if response := post(http.MethodPost, "/resume", resumeBody, true); response.Code < 400 {
			t.Fatal("inactive root resume accepted")
		}
		check(f.db.Exec("UPDATE core.workspace SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id=?", string(f.workspace)).Error)
		for i := 0; i < 2; i++ {
			response := post(http.MethodPost, "/resume", resumeBody, true)
			var result app.SynthesisManuscriptReviewSummary
			check(json.Unmarshal(response.Body.Bytes(), &result))
			if response.Code != http.StatusOK || !result.Ready || !result.Submitted || result.Binding != binding || calls() != expectedCalls {
				t.Fatalf("resume failed: %d %s", response.Code, response.Body.String())
			}
		}
	} else {
		body, err := json.Marshal(command)
		check(err)
		response := post(http.MethodPost, "/"+string(before.Note.ID)+"/decisions", body, true)
		if response.Code != http.StatusOK {
			t.Fatalf("authenticated human POST: %d %s", response.Code, response.Body.String())
		}
		var result app.SynthesisManuscriptReviewSummary
		check(json.Unmarshal(response.Body.Bytes(), &result))
		if !result.Ready || !result.Submitted {
			t.Fatalf("not submitted: %+v", result)
		}
		_, err = service.Decide(ctx, command)
		check(err)
		changed := command
		changed.Resolution.FinalContent += "different"
		if _, err = service.Decide(ctx, changed); err == nil {
			t.Fatal("same key different command accepted")
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		processing, err = store.GetSynthesisProcessing(ctx, f.workspace, processing.ID)
		check(err)
		if processing.Status != app.SynthesisProcessingRunning && processing.Status != app.SynthesisProcessingPending {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if processing.Status != app.SynthesisProcessingSucceeded || len(processing.RevisionIDs) != len(summary.Targets) || calls() != expectedCalls {
		t.Fatalf("human apply failed: %+v calls=%d", processing, calls())
	}
	after, err := f.service.GetNote(ctx, f.workspace, before.Note.ID)
	check(err)
	if after.CurrentRevision.Manuscript == nil || after.CurrentRevision.ParentRevisionID != before.CurrentRevision.ID || after.PublishedRevision.ID != before.PublishedRevision.ID {
		t.Fatal("human candidate or publication binding wrong")
	}
	disk, err := os.ReadFile(filepath.Join(f.root, path))
	check(err)
	if string(disk) != string(original) {
		t.Fatal("human decision published file")
	}
	if twoStage {
		_, err = service.Resume(ctx, binding)
	} else {
		_, err = service.Decide(ctx, command)
	}
	check(err)
	wantDecisions, wantReceipts := 1, len(summary.Targets)
	if twoStage {
		wantDecisions, wantReceipts = 3, 2
	}
	f.count(t, "organizing.synthesis_manuscript_review_decision", "workspace_id", string(f.workspace), wantDecisions)
	f.count(t, "organizing.synthesis_manuscript_receipt", "workspace_id", string(f.workspace), wantReceipts)
}

func manuscriptAuthenticatedHTTP(t *testing.T, f *synthesisGitFixture, manuscripts *SynthesisManuscriptRuntime, human *workflowapp.RuntimeHumanCoordinator, processing, note foundation.ID, processingServices ...organizinghttp.SynthesisProcessingService) func(string, string, []byte, bool) *httptest.ResponseRecorder {
	t.Helper()
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repository, err := authpostgres.NewGORMRepository(f.db)
	check(err)
	bootstrap := "manuscript-integration-bootstrap-credential"
	auth, err := authapp.NewService(repository, foundation.UUIDGenerator{}, foundation.SystemClock{}, authapp.Options{BootstrapToken: bootstrap})
	check(err)
	session, err := auth.ExchangeBootstrap(ctx, bootstrap)
	check(err)
	principal, err := auth.AuthenticateSession(ctx, session.Token, session.CSRFToken, false)
	check(err)
	credential, err := auth.CreateAPIToken(ctx, principal, "manuscript review", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, time.Hour)
	check(err)
	readonly, err := auth.CreateAPIToken(ctx, principal, "manuscript readonly", []capability.Capability{capability.ReadLocal}, time.Hour)
	check(err)
	middleware, err := authhttp.NewHandler(auth, authhttp.Options{AllowedOrigins: []string{"http://127.0.0.1:8080"}})
	check(err)
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(middleware.Middleware)
	var processingService organizinghttp.SynthesisProcessingService
	if len(processingServices) > 0 {
		processingService = processingServices[0]
	}
	organizinghttp.NewSynthesisHandler(f.service, processingService, 10*time.Second).WithManuscriptReviews(manuscripts, human).Routes(group)
	base := "/api/v1/workspaces/" + string(f.workspace) + "/synthesis/processing/" + string(processing) + "/manuscript-review"
	artifactDir := filepath.Join("/tmp/zhixu-manuscript-http-fixtures", filepath.Base(t.Name()))
	check(os.MkdirAll(artifactDir, 0700))
	getNo, postNo := 0, 0
	call := func(method, suffix string, body []byte, allowed bool) *httptest.ResponseRecorder {
		requestPath := base + suffix
		if strings.HasPrefix(suffix, "/api/v1/") {
			requestPath = suffix
		}
		request := httptest.NewRequest(method, requestPath, bytes.NewReader(body))
		token := credential.Plain
		if !allowed {
			token = readonly.Plain
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if allowed {
			name := "summary"
			if method == http.MethodPost {
				postNo++
				name = fmt.Sprintf("decision-%d", postNo)
				check(os.WriteFile(filepath.Join(artifactDir, name+"-command.json"), body, 0600))
			} else if suffix != "" {
				getNo++
				name = fmt.Sprintf("detail-%d", getNo)
			}
			check(os.WriteFile(filepath.Join(artifactDir, name+".json"), response.Body.Bytes(), 0600))
		}
		return response
	}
	denied := call(http.MethodGet, "", nil, false)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("readonly auth allowed review: %d", denied.Code)
	}
	summary := call(http.MethodGet, "", nil, true)
	if summary.Code != http.StatusOK {
		t.Fatalf("summary GET: %d %s", summary.Code, summary.Body.String())
	}
	if bytes.Contains(summary.Body.Bytes(), []byte("prepared")) || bytes.Contains(summary.Body.Bytes(), []byte("model_run")) || bytes.Contains(summary.Body.Bytes(), []byte("authority")) {
		t.Fatal("summary leaked server proof")
	}
	detail := call(http.MethodGet, "/"+string(note), nil, true)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail GET: %d", detail.Code)
	}
	return call
}
