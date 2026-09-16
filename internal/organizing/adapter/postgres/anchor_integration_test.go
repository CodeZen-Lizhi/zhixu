//go:build integration

package postgres

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestAnchorPostgreSQLLifecycle(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 102)
	ctx := t.Context()
	generation := f.generation(t, 74100, nil, "Redis memory management.")
	generation.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(generation, "redis review")}
	applied, e := f.service.ApplyGeneration(ctx, generation.Input, generation.Generation)
	if e != nil {
		t.Fatal(e)
	}
	noteID := applied.Publications[0].NoteID
	detail, e := f.store.GetSynthesisNote(ctx, f.workspace, noteID)
	if e != nil {
		t.Fatal(e)
	}
	store, e := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	if e != nil {
		t.Fatal(e)
	}
	command := app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "create-anchor", NoteID: noteID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, Title: "Redis 复习", Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"面试复习"}, Description: "Redis 专项面试知识"}}
	created, e := store.CreateAnchor(ctx, command)
	if e != nil {
		t.Fatal(e)
	}
	if created.Anchor.ScopeVersion != 1 || created.Replayed {
		t.Fatal(created)
	}
	replay, e := store.CreateAnchor(ctx, command)
	if e != nil || !replay.Replayed || !reflect.DeepEqual(created.Anchor, replay.Anchor) {
		t.Fatalf("create replay: %+v %v", replay, e)
	}
	changed := command
	changed.Title = "changed"
	if _, e = store.CreateAnchor(ctx, changed); e == nil {
		t.Fatal("changed request replay accepted")
	}
	if _, e = store.GetAnchor(ctx, f.otherWorkspace, created.Anchor.ID); e == nil {
		t.Fatal("cross workspace read accepted")
	}
	// 锚点创建前冻结的生成任务没有已批准范围绑定，因此用户创建新锚点后，该任务不得提交。
	plainGeneration := f.generation(t, 74150, nil, "Unanchored cache material.")
	plainGeneration.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(plainGeneration, "cache review")}
	plainApplied, e := f.service.ApplyGeneration(ctx, plainGeneration.Input, plainGeneration.Generation)
	if e != nil {
		t.Fatal(e)
	}
	plainDetail, e := f.store.GetSynthesisNote(ctx, f.workspace, plainApplied.Publications[0].NoteID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.VerifySynthesisAnchorAdmissionScoped(ctx, scope, f.workspace, plainDetail.Note.ID, plainGeneration.Input.Sources[0].Reference.Source, nil)
	}); e != nil {
		t.Fatalf("unanchored frozen candidate rejected: %v", e)
	}
	if _, e = store.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "create-late-anchor", NoteID: plainDetail.Note.ID, ExpectedNoteVersion: plainDetail.Note.Version, BasisRevisionID: plainDetail.CurrentRevision.ID, Title: "Cache 复习", Scope: domain.AnchorScope{Topics: []string{"Cache"}, Audiences: []string{"面试复习"}, Description: "缓存专项知识"}}); e != nil {
		t.Fatal(e)
	}
	if e = f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.VerifySynthesisAnchorAdmissionScoped(ctx, scope, f.workspace, plainDetail.Note.ID, plainGeneration.Input.Sources[0].Reference.Source, nil)
	}); !organizingIntegrationError(e, foundation.ErrorVersionConflict, "ANCHOR_VERSION_CONFLICT") {
		t.Fatalf("late-created anchor did not invalidate unanchored freeze: %v", e)
	}
	// 模型测试数据通过所属模块完成；生产环境必须先接入真实 AI 推荐输出，再调用仅供 Worker 使用的推荐端口。
	agent, e := agentpostgres.NewGORMRepository(f.platform)
	if e != nil {
		t.Fatal(e)
	}
	model, e := agent.GetModelRun(ctx, f.workspace, generation.Generation.ModelRunID)
	if e != nil {
		t.Fatal(e)
	}
	call := agentdomain.ModelCall{ID: organizingIntegrationID(79001), ModelRunID: model.Run.ID, CallNo: 1, Phase: agentdomain.ModelCallInitial, Model: model.Run.Model, Profile: model.Run.Profile, Prompt: model.Run.Prompt, Schema: model.Run.Schema, MaxOutputTokens: 128, Status: agentdomain.ModelCallStarted, RequestHash: organizingIntegrationHash("anchor-model-request"), RequestBytes: 128, Version: 1, StartedAt: model.Run.CreatedAt.Add(time.Second)}
	if _, _, e = agent.StartModelCall(ctx, f.workspace, call); e != nil {
		t.Fatal(e)
	}
	completedAt := call.StartedAt.Add(time.Second)
	call.Status = agentdomain.ModelCallSucceeded
	call.ResponseHash = organizingIntegrationHash("anchor-model-response")
	call.ResponseBytes = 64
	call.Version = 2
	call.CompletedAt = &completedAt
	if _, _, e = agent.CompleteModelCall(ctx, agentapp.CompleteModelCallCommand{WorkspaceID: f.workspace, ExpectedVersion: 1, Call: call}); e != nil {
		t.Fatal(e)
	}
	model.Run.Status = agentdomain.ModelRunSucceeded
	model.Run.FinalResultType = agentdomain.ResultTypeRAGAnswer
	model.Run.Version++
	now := time.Now().UTC().Truncate(time.Microsecond)
	model.Run.UpdatedAt = now
	model.Run.CompletedAt = &now
	if _, _, e = agent.FinalizeModelRun(ctx, agentapp.FinalizeModelRunCommand{ExpectedVersion: 1, Run: model.Run}); e != nil {
		t.Fatal(e)
	}
	a := created.Anchor
	association := app.RecordAnchorRecommendation{WorkspaceID: f.workspace, AnchorID: a.ID, IdempotencyKey: "recommend-association", ExpectedScopeVersion: 1, Kind: domain.AnchorSourceAssociation, Reason: "补充 Redis 复习的来源", Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, ModelRunID: generation.Generation.ModelRunID}
	recommended, e := store.RecordAnchorRecommendation(ctx, association)
	if e != nil {
		t.Fatal(e)
	}
	// 只要任何被引用提案缺失，原子批处理就拒绝全部操作。
	decision := app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: a.ID, IdempotencyKey: "batch", Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: 1, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: recommended.Proposal.ID, ExpectedVersion: 1}, {ProposalID: organizingIntegrationID(79999), ExpectedVersion: 1}}}
	if _, e = store.DecideAnchor(ctx, decision); e == nil {
		t.Fatal("invalid batch committed")
	}
	listed, e := store.ListAnchorProposals(ctx, app.AnchorProposalQuery{WorkspaceID: f.workspace, AnchorID: a.ID, Kind: domain.AnchorSourceAssociation, Limit: 20})
	if e != nil || len(listed.Items) != 1 || listed.Items[0].Status != domain.AnchorPending {
		t.Fatalf("atomic rejection %+v %v", listed, e)
	}
	decision.Items = decision.Items[:1]
	accepted, e := store.DecideAnchor(ctx, decision)
	if e != nil {
		t.Fatal(e)
	}
	if accepted.Anchor.ScopeVersion != 1 || !reflect.DeepEqual(accepted.Anchor.Scope, command.Scope) {
		t.Fatal("association expanded scope")
	}
	if accepted.Items[0].Status != domain.AnchorAccepted {
		t.Fatal(accepted)
	}
	var fusion anchorFusionRequestModel
	if e = f.db.Where("workspace_id=? AND proposal_id=?", string(f.workspace), string(recommended.Proposal.ID)).Take(&fusion).Error; e != nil || fusion.Status != "PENDING" || fusion.ProcessingID != nil || fusion.NoteID != string(noteID) || fusion.SourceEventID != string(generation.Input.SourceEvent.ID) {
		t.Fatalf("accepted association did not create exact pending fusion request: %+v %v", fusion, e)
	}
	request, e := store.GetAnchorFusionRequest(ctx, f.workspace, foundation.ID(fusion.ID))
	if e != nil || request.Status != "PENDING" || request.SourceEvent != generation.Input.SourceEvent || len(request.AllowedSources) != 1 || request.AllowedSources[0] != generation.Input.Sources[0].Reference {
		t.Fatalf("fusion request row=%+v read projection=%+v err=%v", fusion, request, e)
	}
	requests, e := store.ListAnchorFusionRequests(ctx, f.workspace, created.Anchor.ID, 20)
	if e != nil || len(requests) != 1 || requests[0].ID != request.ID {
		t.Fatalf("fusion request page=%+v err=%v", requests, e)
	}
	admitted, e := store.ReadAcceptedAnchorSources(ctx, f.workspace, noteID, generation.Input.Sources[0].Reference.Source)
	if e != nil || len(admitted) != 1 || admitted[0] != generation.Input.Sources[0].Reference {
		t.Fatalf("exact admission %+v %v", admitted, e)
	}
	binding := &app.SynthesisAnchorBinding{AnchorID: created.Anchor.ID, ScopeVersion: accepted.Anchor.ScopeVersion, Scope: accepted.Anchor.Scope, AllowedSources: admitted}
	if e = f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.VerifySynthesisAnchorAdmissionScoped(ctx, scope, f.workspace, noteID, generation.Input.Sources[0].Reference.Source, binding)
	}); e != nil {
		t.Fatalf("accepted anchor admission fence: %v", e)
	}
	forgedSource := generation.Input.Sources[0].Reference.Source
	forgedSource.ContentHash = organizingIntegrationHash("different content")
	admitted, e = store.ReadAcceptedAnchorSources(ctx, f.workspace, noteID, forgedSource)
	if e != nil || len(admitted) != 0 {
		t.Fatalf("forged admission %+v %v", admitted, e)
	}
	replayed, e := store.DecideAnchor(ctx, decision)
	if e != nil || !replayed.Replayed {
		t.Fatalf("decision replay %+v %v", replayed, e)
	}
	f.count(t, "organizing.anchor_fusion_request", "proposal_id", string(recommended.Proposal.ID), 1)
	scope := domain.AnchorScope{Topics: []string{"Redis", "MySQL"}, Audiences: []string{"面试复习"}, Description: "数据库面试知识"}
	proposal := association
	proposal.IdempotencyKey = "expand"
	proposal.Kind = domain.AnchorScopeAdjustment
	proposal.Suggested = &scope
	proposal.Reason = "建议扩充数据库主题"
	proposed, e := store.RecordAnchorRecommendation(ctx, proposal)
	if e != nil {
		t.Fatal(e)
	}
	before, e := store.GetAnchor(ctx, f.workspace, a.ID)
	if e != nil || before.ScopeVersion != 1 {
		t.Fatal("unconfirmed scope changed", e)
	}
	approve := app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: a.ID, IdempotencyKey: "accept-scope", Kind: domain.AnchorScopeAdjustment, ExpectedAnchorVersion: accepted.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: proposed.Proposal.ID, ExpectedVersion: 1}}}
	expanded, e := store.DecideAnchor(ctx, approve)
	if e != nil {
		t.Fatal(e)
	}
	if expanded.Anchor.ScopeVersion != 2 || !reflect.DeepEqual(expanded.Anchor.Scope, scope) {
		t.Fatal(expanded)
	}
	if e = f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return store.VerifySynthesisAnchorAdmissionScoped(ctx, scope, f.workspace, noteID, generation.Input.Sources[0].Reference.Source, binding)
	}); !organizingIntegrationError(e, foundation.ErrorVersionConflict, "ANCHOR_VERSION_CONFLICT") {
		t.Fatalf("stale scope binding reached candidate application: %v", e)
	}
	admitted, e = store.ReadAcceptedAnchorSources(ctx, f.workspace, noteID, generation.Input.Sources[0].Reference.Source)
	if e != nil || len(admitted) != 0 {
		t.Fatalf("old scope admission survived without re-evaluation %+v %v", admitted, e)
	}
	scopes, e := store.ListAnchorProposals(ctx, app.AnchorProposalQuery{WorkspaceID: f.workspace, AnchorID: a.ID, Kind: domain.AnchorScopeAdjustment, Limit: 20})
	if e != nil || len(scopes.Items) != 1 || scopes.Items[0].Status != domain.AnchorAccepted {
		t.Fatalf("scope listing %+v %v", scopes, e)
	}
	anchors, e := store.ListAnchors(ctx, app.AnchorListQuery{WorkspaceID: f.workspace, NoteID: noteID, Limit: 1})
	if e != nil || len(anchors.Items) != 1 || anchors.Items[0].ScopeVersion != 2 {
		t.Fatalf("anchor listing %+v %v", anchors, e)
	}
	// 范围变化后重试旧的已接受关联，应重放精确的原回执。
	replayed, e = store.DecideAnchor(ctx, decision)
	if e != nil || !replayed.Replayed || replayed.Anchor.ScopeVersion != 1 {
		t.Fatalf("historical replay lost %+v %v", replayed, e)
	}
	persisted, e := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	if e != nil {
		t.Fatal(e)
	}
	current, e := persisted.GetAnchor(ctx, f.workspace, a.ID)
	if e != nil || current.ScopeVersion != 2 {
		t.Fatal("scope not durable", e)
	}
	original, e := f.store.GetSynthesisRevision(ctx, f.workspace, noteID, detail.CurrentRevision.ID)
	if e != nil || !reflect.DeepEqual(original, *detail.CurrentRevision) {
		t.Fatal("scope decision mutated body", e)
	}
	if e = f.db.Exec(`UPDATE organizing.anchor_scope_revision SET scope='{}' WHERE anchor_id=?`, string(a.ID)).Error; platformpostgres.SQLState(e) != "55000" {
		t.Fatalf("history mutable %v", e)
	}
	// 范围拒绝结果已持久化，不能用新键重新考虑。
	proposal.IdempotencyKey = "expand-again"
	proposal.ExpectedScopeVersion = 2
	proposal.Suggested = &domain.AnchorScope{Topics: []string{"Redis", "MySQL", "Oracle"}, Audiences: []string{"面试复习"}, Description: "数据库全景"}
	p2, e := store.RecordAnchorRecommendation(ctx, proposal)
	if e != nil {
		t.Fatal(e)
	}
	approve.IdempotencyKey = "reject-scope"
	approve.ExpectedAnchorVersion = current.Version
	approve.Decision = domain.AnchorRejected
	approve.Items[0].ProposalID = p2.Proposal.ID
	rejected, e := store.DecideAnchor(ctx, approve)
	if e != nil || rejected.Anchor.ScopeVersion != 2 {
		t.Fatalf("rejection changed scope %+v %v", rejected, e)
	}
	approve.IdempotencyKey = "resubmit"
	approve.Decision = domain.AnchorAccepted
	approve.ExpectedAnchorVersion = rejected.Anchor.Version
	if _, e = store.DecideAnchor(ctx, approve); e == nil {
		t.Fatal("decided proposal accepted again")
	}
	association.ExpectedScopeVersion = 2
	association.IdempotencyKey = "concurrent-recommend-1"
	p3, e := store.RecordAnchorRecommendation(ctx, association)
	if e != nil {
		t.Fatal(e)
	}
	association.IdempotencyKey = "concurrent-recommend-2"
	p4, e := store.RecordAnchorRecommendation(ctx, association)
	if e != nil {
		t.Fatal(e)
	}
	commands := []app.DecideAnchorCommand{
		{WorkspaceID: f.workspace, AnchorID: a.ID, IdempotencyKey: "concurrent-decide-1", Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: rejected.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: p3.Proposal.ID, ExpectedVersion: 1}}},
		{WorkspaceID: f.workspace, AnchorID: a.ID, IdempotencyKey: "concurrent-decide-2", Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: rejected.Anchor.Version, Decision: domain.AnchorAccepted, Items: []app.AnchorDecisionItem{{ProposalID: p4.Proposal.ID, ExpectedVersion: 1}}},
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range commands {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = store.DecideAnchor(ctx, commands[i]) }(i)
	}
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		} else if !organizingIntegrationError(err, foundation.ErrorVersionConflict, "ANCHOR_VERSION_CONFLICT") {
			t.Fatalf("unexpected concurrent failure %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent accepted count %d", success)
	}
	// 被拒绝的关联只作为评审历史记录保存，绝不能再次排入正文融合请求。
	var requestsBefore int64
	if e = f.db.Table("organizing.anchor_fusion_request").Where("workspace_id=?", string(f.workspace)).Count(&requestsBefore).Error; e != nil {
		t.Fatal(e)
	}
	latest, e := store.GetAnchor(ctx, f.workspace, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	rejectedAssociation := association
	rejectedAssociation.IdempotencyKey = "reject-association"
	rejectedAssociation.ExpectedScopeVersion = latest.ScopeVersion
	rejectedAssociation.Reason = "不纳入当前主笔记"
	rejectedProposal, e := store.RecordAnchorRecommendation(ctx, rejectedAssociation)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.DecideAnchor(ctx, app.DecideAnchorCommand{WorkspaceID: f.workspace, AnchorID: a.ID, IdempotencyKey: "reject-association-decision", Kind: domain.AnchorSourceAssociation, ExpectedAnchorVersion: latest.Version, Decision: domain.AnchorRejected, Items: []app.AnchorDecisionItem{{ProposalID: rejectedProposal.Proposal.ID, ExpectedVersion: 1}}}); e != nil {
		t.Fatal(e)
	}
	var requestsAfter int64
	if e = f.db.Table("organizing.anchor_fusion_request").Where("workspace_id=?", string(f.workspace)).Count(&requestsAfter).Error; e != nil || requestsAfter != requestsBefore {
		t.Fatalf("rejected association queued fusion before=%d after=%d err=%v", requestsBefore, requestsAfter, e)
	}

}

// 仅此测试数据有意替代语义模型证明；它不证明生产推荐模型工作流已接通。
type anchorModelProofFixture struct{}

func (anchorModelProofFixture) VerifyAnchorRecommendationScoped(context.Context, foundation.TransactionScope, domain.Anchor, app.RecordAnchorRecommendation) error {
	return nil
}
