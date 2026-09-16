//go:build integration

package postgres

import (
	"strings"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestAnchorRecommendationProofPersistsOnlyExactClaimedModelResult(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 103)
	ctx := t.Context()
	generation := f.generation(t, 99000, nil, "Redis exact recommendation evidence.")
	generation.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(generation, "redis recommendation")}
	applied, err := f.service.ApplyGeneration(ctx, generation.Input, generation.Generation)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, applied.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	anchorStore, err := NewGORMAnchorStore(f.platform, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := anchorStore.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "proof-anchor", NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, Title: "Redis", Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"review"}, Description: "Redis review"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := anchorStore.RequestAnchorSourceRecommendation(ctx, app.RequestAnchorSourceRecommendationCommand{WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, ExpectedScopeVersion: anchor.Anchor.ScopeVersion, Source: generation.Input.Sources[0].Reference.Source, Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, IdempotencyKey: "proof-request"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"recommendation":{"title":"Redis","kind":"SOURCE_ASSOCIATION","scope":null,"reason":"Redis source is relevant","evidence":["S001"]},"no_recommendation":false}`)
	model, outputHash := createSuccessfulAnchorRecommendationModelRun(t, f, agent, anchorStore, request, 99020, raw)
	verifier, err := NewAnchorRecommendationModelVerifier(agent)
	if err != nil {
		t.Fatal(err)
	}
	anchorStore.verifier = verifier
	recommendation := app.RecordAnchorRecommendation{RequestID: request.ID, WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, IdempotencyKey: "proof-proposal", ExpectedScopeVersion: anchor.Anchor.ScopeVersion, Kind: domain.AnchorSourceAssociation, Reason: "Redis source is relevant", Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, ModelRunID: model.ID, ModelOutputHash: outputHash, ModelOutput: raw}
	created, err := anchorStore.RecordAnchorRecommendation(ctx, recommendation)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := anchorStore.RecordAnchorRecommendation(ctx, recommendation)
	if err != nil || !replayed.Replayed || replayed.Proposal.ID != created.Proposal.ID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	stored, err := anchorStore.GetAnchorRecommendation(ctx, f.workspace, request.ID)
	if err != nil || stored.Status != domain.AnchorRecommendationSucceeded || stored.ProposalID != created.Proposal.ID || stored.ModelRunID != model.ID {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}

	second, err := anchorStore.RequestAnchorSourceRecommendation(ctx, app.RequestAnchorSourceRecommendationCommand{WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, ExpectedScopeVersion: anchor.Anchor.ScopeVersion, Source: generation.Input.Sources[0].Reference.Source, Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, IdempotencyKey: "proof-forged"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = anchorStore.ClaimAnchorRecommendation(ctx, app.ClaimAnchorRecommendationCommand{WorkspaceID: f.workspace, RequestID: second.ID, ExpectedVersion: second.Version, WorkflowRunID: model.WorkflowRunID, NodeRunID: model.NodeRunID, NodeAttemptID: model.NodeAttemptID, ModelInputHash: organizingIntegrationHash("recommendation-input-" + string(second.ID))}); err == nil {
		t.Fatal("a completed model node was reused by another recommendation request")
	}
	secondRaw := []byte(`{"recommendation":{"title":"Redis","kind":"SOURCE_ASSOCIATION","scope":null,"reason":"Redis source is relevant","evidence":["S001"]},"no_recommendation":false}`)
	secondModel, _ := createSuccessfulAnchorRecommendationModelRun(t, f, agent, anchorStore, second, 99040, secondRaw)
	recommendation.RequestID, recommendation.IdempotencyKey, recommendation.ModelRunID, recommendation.ModelOutputHash = second.ID, "proof-forged-proposal", secondModel.ID, strings.Repeat("b", 64)
	if _, err = anchorStore.RecordAnchorRecommendation(ctx, recommendation); err == nil {
		t.Fatal("forged recorded response hash created a proposal")
	}
	stored, err = anchorStore.GetAnchorRecommendation(ctx, f.workspace, second.ID)
	if err != nil || stored.Status != domain.AnchorRecommendationRunning || stored.ProposalID != "" {
		t.Fatalf("forged proof changed request=%+v err=%v", stored, err)
	}

	stale, err := anchorStore.RequestAnchorSourceRecommendation(ctx, app.RequestAnchorSourceRecommendationCommand{WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, ExpectedScopeVersion: anchor.Anchor.ScopeVersion, Source: generation.Input.Sources[0].Reference.Source, Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, IdempotencyKey: "proof-stale-scope"})
	if err != nil {
		t.Fatal(err)
	}
	staleModel, staleHash := createSuccessfulAnchorRecommendationModelRun(t, f, agent, anchorStore, stale, 99060, raw)
	fixtureStore, err := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.AnchorScope{Topics: []string{"Redis", "MySQL"}, Audiences: []string{"review"}, Description: "Database review"}
	scopeProposal, err := fixtureStore.RecordAnchorRecommendation(ctx, app.RecordAnchorRecommendation{WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, IdempotencyKey: "proof-expand-scope", ExpectedScopeVersion: anchor.Anchor.ScopeVersion, Kind: domain.AnchorScopeAdjustment, Suggested: &scope, Reason: "The reviewed database scope expanded", Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, ModelRunID: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixtureStore.DecideAnchor(ctx, app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, IdempotencyKey: "proof-accept-scope", Kind: domain.AnchorScopeAdjustment, ExpectedAnchorVersion: anchor.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: scopeProposal.Proposal.ID, ExpectedVersion: scopeProposal.Proposal.Version}}}); err != nil {
		t.Fatal(err)
	}
	recommendation.RequestID, recommendation.IdempotencyKey, recommendation.ModelRunID, recommendation.ModelOutputHash = stale.ID, "proof-stale-scope-proposal", staleModel.ID, staleHash
	if _, err = anchorStore.RecordAnchorRecommendation(ctx, recommendation); err == nil {
		t.Fatal("stale scope recommendation created a proposal")
	}
	stored, err = anchorStore.GetAnchorRecommendation(ctx, f.workspace, stale.ID)
	if err != nil || stored.Status != domain.AnchorRecommendationRunning || stored.ProposalID != "" {
		t.Fatalf("stale scope changed request=%+v err=%v", stored, err)
	}
}

func createSuccessfulAnchorRecommendationModelRun(t *testing.T, f *synthesisDBFixture, agent *agentpostgres.GORMRepository, store *GORMAnchorStore, request app.AnchorRecommendationRequest, seed int, output []byte) (agentdomain.ModelRun, string) {
	t.Helper()
	id := func(offset int) foundation.ID { return organizingIntegrationID(seed + offset) }
	at := f.now.Add(time.Duration(seed) * time.Microsecond)
	workflow := f.generation(t, seed+100, nil, "recommendation workflow "+string(request.ID)).Input.WorkflowRunID
	// 使用专用的活跃工作流，让推荐 ModelRun 拥有精确且不可复用的节点尝试，并独立于合成测试数据。共享测试数据从一小时前开始，因此保留一小时有效租约。
	seedOrganizingRunningNodeAttempt(t, t.Context(), f.platform.DB(), workflow, id(1), id(2), "anchor-recommendation", at.Add(2*time.Hour), at)
	inputHash := organizingIntegrationHash("recommendation-input-" + string(request.ID))
	claimed, err := store.ClaimAnchorRecommendation(t.Context(), app.ClaimAnchorRecommendationCommand{WorkspaceID: f.workspace, RequestID: request.ID, ExpectedVersion: request.Version, WorkflowRunID: workflow, NodeRunID: id(1), NodeAttemptID: id(2), ModelInputHash: inputHash})
	if err != nil || claimed.Status != domain.AnchorRecommendationRunning {
		t.Fatalf("claim recommendation=%+v err=%v", claimed, err)
	}
	run := agentdomain.ModelRun{ID: id(3), WorkspaceID: f.workspace, WorkflowRunID: workflow, NodeRunID: id(1), NodeAttemptID: id(2), Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "anchor", ModelVersion: "v1"}, Profile: agentdomain.ModelProfileRef{ID: "anchor", Version: "v1"}, Prompt: anchorRecommendationPromptRef(), Schema: anchorRecommendationSchemaRef(), ReducedSchema: anchorRecommendationSchemaRef(), Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at}
	if _, replayed, err := agent.CreateModelRun(t.Context(), run); err != nil || replayed {
		t.Fatalf("create recommendation model run=%+v replayed=%t err=%v", run, replayed, err)
	}
	call := agentdomain.ModelCall{ID: id(4), ModelRunID: run.ID, CallNo: 1, Phase: agentdomain.ModelCallInitial, Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 128, Status: agentdomain.ModelCallStarted, RequestHash: inputHash, RequestBytes: 64, Version: 1, StartedAt: at.Add(time.Second)}
	if _, replayed, err := agent.StartModelCall(t.Context(), f.workspace, call); err != nil || replayed {
		t.Fatalf("start recommendation model call replayed=%t err=%v", replayed, err)
	}
	outputHash := sha256Hex(output)
	completed := call.StartedAt.Add(time.Second)
	call.Status, call.ResponseHash, call.ResponseBytes, call.Usage, call.LatencyMillis, call.Version, call.CompletedAt = agentdomain.ModelCallSucceeded, outputHash, int64(len(output)), agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, 1, 2, &completed
	if _, replayed, err := agent.CompleteModelCall(t.Context(), agentapp.CompleteModelCallCommand{WorkspaceID: f.workspace, ExpectedVersion: 1, Call: call}); err != nil || replayed {
		t.Fatalf("complete recommendation model call replayed=%t err=%v", replayed, err)
	}
	run.Status, run.FinalResultType, run.Version, run.UpdatedAt, run.CompletedAt = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeAnchorRecommendation, 2, completed, &completed
	if _, replayed, err := agent.FinalizeModelRun(t.Context(), agentapp.FinalizeModelRunCommand{ExpectedVersion: 1, Run: run}); err != nil || replayed {
		t.Fatalf("finalize recommendation model run replayed=%t err=%v", replayed, err)
	}
	if err := agentdomain.ValidateModelRun(run); err != nil {
		t.Fatal(err)
	}
	return run, outputHash
}
